package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/selfheal"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// fakeSend records every delivery attempt and returns a canned verdict, so the
// executor is exercised with no tmux pane and no real send path.
type fakeSend struct {
	calls    []fakeSendCall
	delivery string
	err      error
}

type fakeSendCall struct {
	instanceID string
	message    string
}

func (f *fakeSend) fn(inst *Instance, message string) (string, error) {
	f.calls = append(f.calls, fakeSendCall{instanceID: inst.ID, message: message})
	return f.delivery, f.err
}

func executorWith(t *testing.T, f *fakeSend, insts ...*Instance) *ResumeExecutor {
	t.Helper()
	x := NewResumeExecutor()
	x.setSendForTest(f.fn)
	x.SetInstances(insts)
	return x
}

func resumeCandidate(id string, sub tmux.Substate) selfheal.Candidate {
	return selfheal.Candidate{SessionID: id, Title: "worker-3", Substate: sub}
}

// §6: exactly one delivery per confirmed candidate, and the outcome carries the
// real delivery value.
func TestSelfHealResume_Submitted_OneDelivery(t *testing.T) {
	f := &fakeSend{delivery: "submitted"}
	inst := &Instance{ID: "s1", Title: "worker-3"}
	x := executorWith(t, f, inst)

	outcome, err := x.Execute(resumeCandidate("s1", tmux.SubstateAPIError), selfheal.ActionResume)
	if err != nil {
		t.Fatalf("a delivered resume must not error, got %v", err)
	}
	if outcome != "resumed:submitted" {
		t.Fatalf("outcome = %q, want resumed:submitted", outcome)
	}
	if len(f.calls) != 1 {
		t.Fatalf("exactly one delivery, got %d", len(f.calls))
	}
	if f.calls[0].instanceID != "s1" {
		t.Fatalf("delivered to the wrong session: %q", f.calls[0].instanceID)
	}
}

// §6: typed_not_submitted is recorded as a FAILURE. The outcome is not the
// delivered one, so the engine's outcomeIsDelivered rejects it — and the send
// path's own error must not swallow the delivery string.
func TestSelfHealResume_TypedNotSubmitted_IsAFailureOutcome(t *testing.T) {
	f := &fakeSend{delivery: "typed_not_submitted", err: errors.New("message typed but not submitted")}
	inst := &Instance{ID: "s1", Title: "worker-3"}
	x := executorWith(t, f, inst)

	outcome, err := x.Execute(resumeCandidate("s1", tmux.SubstateAPIError), selfheal.ActionResume)
	if err != nil {
		t.Fatalf("a delivery verdict must be reported, not collapsed into an error: %v", err)
	}
	if outcome != "resumed:typed_not_submitted" {
		t.Fatalf("outcome = %q, want resumed:typed_not_submitted", outcome)
	}
	if outcome == "resumed:submitted" {
		t.Fatal("a typed-but-unsubmitted resume must never read as delivered")
	}
}

// Every other delivery verdict is passed through verbatim.
func TestSelfHealResume_DeliveryVerdictsPassThrough(t *testing.T) {
	for _, d := range []string{"typed", "unverified", "no_evidence", "line_too_long", "send_failed"} {
		f := &fakeSend{delivery: d}
		x := executorWith(t, f, &Instance{ID: "s1"})
		outcome, err := x.Execute(resumeCandidate("s1", tmux.SubstateAPIError), selfheal.ActionResume)
		if err != nil {
			t.Fatalf("delivery %q: unexpected error %v", d, err)
		}
		if outcome != "resumed:"+d {
			t.Fatalf("delivery %q: outcome = %q", d, outcome)
		}
	}
}

// An empty delivery from a send path that failed before touching the pane is
// reported as no_evidence rather than as an empty, unmatchable outcome.
func TestSelfHealResume_EmptyDelivery_IsNoEvidence(t *testing.T) {
	f := &fakeSend{delivery: "", err: errors.New("pane vanished")}
	x := executorWith(t, f, &Instance{ID: "s1"})
	outcome, err := x.Execute(resumeCandidate("s1", tmux.SubstateAPIError), selfheal.ActionResume)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if outcome != "resumed:no_evidence" {
		t.Fatalf("outcome = %q, want resumed:no_evidence", outcome)
	}
}

// The prompts differ by reason, and the usage-limit one must warn about a
// terminated subagent — without it a parent silently loses a child's work.
func TestSelfHealResume_PromptDiffersByReason(t *testing.T) {
	f := &fakeSend{delivery: "submitted"}
	x := executorWith(t, f, &Instance{ID: "s1"})

	if _, err := x.Execute(resumeCandidate("s1", tmux.SubstateAPIError), selfheal.ActionResume); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Execute(resumeCandidate("s1", tmux.SubstateUsageLimit), selfheal.ActionResume); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("want 2 deliveries, got %d", len(f.calls))
	}
	transport, usage := f.calls[0].message, f.calls[1].message
	if transport == usage {
		t.Fatal("the transport and usage-limit prompts must differ")
	}
	if !strings.Contains(strings.ToLower(usage), "subagent") {
		t.Fatalf("the usage-limit prompt must warn about a terminated subagent, got %q", usage)
	}
	if !strings.Contains(strings.ToLower(usage), "re-dispatch") {
		t.Fatalf("the usage-limit prompt must tell the parent to re-dispatch, got %q", usage)
	}
	if strings.Contains(strings.ToLower(transport), "subagent") {
		t.Fatalf("the transport prompt must not carry the usage-limit warning, got %q", transport)
	}
}

func TestSelfHealResume_ModelCapacity_PreservesSelectedModel(t *testing.T) {
	f := &fakeSend{delivery: "submitted"}
	x := executorWith(t, f, &Instance{ID: "s1"})

	if _, err := x.Execute(resumeCandidate("s1", tmux.SubstateModelUnavailable), selfheal.ActionResume); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("want one delivery, got %d", len(f.calls))
	}
	prompt := strings.ToLower(f.calls[0].message)
	if !strings.Contains(prompt, "at capacity") {
		t.Fatalf("capacity retry prompt must name the condition, got %q", f.calls[0].message)
	}
	if !strings.Contains(prompt, "selected model") {
		t.Fatalf("capacity retry prompt must preserve the selected model, got %q", f.calls[0].message)
	}
	if strings.Contains(prompt, "different model") || strings.Contains(prompt, "switch model") {
		t.Fatalf("capacity retry must not request a model change, got %q", f.calls[0].message)
	}
}

// Neither prompt may assert recovery as FACT. Nothing on this path checks
// reachability, and the schedule that released a usage-limit send is frequently
// the flat backoff guess rather than a parsed reset — so a strong claim is
// simply false, the agent acts on it, is rejected again, and burns one of its
// two 6-hour recoveries. Pinned on both so the pair cannot drift apart again.
func TestSelfHealResume_PromptsHedgeRecovery(t *testing.T) {
	f := &fakeSend{delivery: "submitted"}
	x := executorWith(t, f, &Instance{ID: "s1"})

	for _, s := range []tmux.Substate{tmux.SubstateAPIError, tmux.SubstateUsageLimit} {
		if _, err := x.Execute(resumeCandidate("s1", s), selfheal.ActionResume); err != nil {
			t.Fatal(err)
		}
	}
	transport, usage := strings.ToLower(f.calls[0].message), strings.ToLower(f.calls[1].message)

	if !strings.Contains(transport, "may have recovered") {
		t.Fatalf("the transport prompt must hedge, got %q", transport)
	}
	if !strings.Contains(usage, "may have reset") {
		t.Fatalf("the usage-limit prompt must hedge, got %q", usage)
	}
	for _, claim := range []string{"has recovered", "has since reset", "has reset"} {
		if strings.Contains(transport, claim) {
			t.Fatalf("the transport prompt must not assert %q as fact, got %q", claim, transport)
		}
		if strings.Contains(usage, claim) {
			t.Fatalf("the usage-limit prompt must not assert %q as fact, got %q", claim, usage)
		}
	}
}

// A non-resume action is a mis-wire alarm, not a silent no-op.
func TestSelfHealResume_WrongAction_Errors(t *testing.T) {
	f := &fakeSend{delivery: "submitted"}
	x := executorWith(t, f, &Instance{ID: "s1"})
	outcome, err := x.Execute(resumeCandidate("s1", tmux.SubstateAPIError), selfheal.ActionRestart)
	if !errors.Is(err, ErrSelfHealWrongAction) {
		t.Fatalf("want ErrSelfHealWrongAction, got %v", err)
	}
	if outcome != "" {
		t.Fatalf("no outcome without an action, got %q", outcome)
	}
	if len(f.calls) != 0 {
		t.Fatal("a non-resume action must never reach the send path")
	}
}

// A candidate whose session vanished between the poll and the action.
func TestSelfHealResume_UnknownSession_Errors(t *testing.T) {
	f := &fakeSend{delivery: "submitted"}
	x := executorWith(t, f) // empty view
	_, err := x.Execute(resumeCandidate("ghost", tmux.SubstateAPIError), selfheal.ActionResume)
	if !errors.Is(err, ErrSelfHealUnknownSession) {
		t.Fatalf("want ErrSelfHealUnknownSession, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatal("a vanished session must never reach the send path")
	}
}

// With no registered sender the executor fails loudly rather than reporting a
// resume it never made.
func TestSelfHealResume_NoSender_Errors(t *testing.T) {
	x := NewResumeExecutor()
	x.SetInstances([]*Instance{{ID: "s1"}})
	// selfHealSender is process-wide: leaving it nulled would silently unregister
	// the send path for every test that runs after this one in the package.
	prev := registeredSelfHealSender()
	SetSelfHealSender(nil)
	t.Cleanup(func() { SetSelfHealSender(prev) })
	_, err := x.Execute(resumeCandidate("s1", tmux.SubstateAPIError), selfheal.ActionResume)
	if !errors.Is(err, ErrSelfHealNoSender) {
		t.Fatalf("want ErrSelfHealNoSender, got %v", err)
	}
}

// SetInstances replaces rather than merges: a session dropped from the fleet must
// stop being reachable.
func TestSelfHealResume_SetInstances_Replaces(t *testing.T) {
	f := &fakeSend{delivery: "submitted"}
	x := executorWith(t, f, &Instance{ID: "s1"})
	x.SetInstances([]*Instance{{ID: "s2"}})
	if _, err := x.Execute(resumeCandidate("s1", tmux.SubstateAPIError), selfheal.ActionResume); !errors.Is(err, ErrSelfHealUnknownSession) {
		t.Fatalf("s1 must be gone after a replace, got %v", err)
	}
	if _, err := x.Execute(resumeCandidate("s2", tmux.SubstateAPIError), selfheal.ActionResume); err != nil {
		t.Fatalf("s2 must be reachable, got %v", err)
	}
}

// The executor satisfies the engine's interface — a compile-time assertion, so a
// signature drift in either package fails the build rather than the wiring.
var _ selfheal.ActionExecutor = (*ResumeExecutor)(nil)
