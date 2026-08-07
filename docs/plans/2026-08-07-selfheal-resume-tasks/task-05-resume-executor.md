# Task 05 — resume executor + send seam

tier: strong
depends on: task 02 (`ActionResume`), task 03 (`ActionExecutor` contract, `"resumed:<delivery>"` outcome format)
parallel with: nothing
worktree: `/Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` (branch `feature/selfheal-auto-resume`)

Use absolute paths under that worktree for every Read/Edit/Write, and
`git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` for
every git command. Never run `git stash`, `git checkout`, `git switch`, or
`git reset`; never edit the root checkout at `/Users/doozyx/DoozyX/agent-deck`.

**Precondition to check first:**
```sh
grep -n 'outcomeDeliveredPrefix\|ActionResume' internal/selfheal/engine.go internal/selfheal/selfheal.go
```
must print at least two lines. If not, tasks 02/03 have not landed — stop and report BLOCKED.

---

## Design extracts (verbatim from the approved design)

> ### D5 — One action, one executor
>
> ```go
> ActionResume Action = "resume"   // params: {"reason": "transport" | "usage_limit"}
> ```
>
> Not `ActionResend` — that name means "replay the last intent", which can redo
> work when a turn partially completed and has nothing to replay when
> `LastSentAt` is zero. Not two actions either: both triggers deliver one
> continuation prompt through one send path, and splitting them would duplicate
> the executor.
>
> `ActionResend` itself is untouched: it remains the observe-mode `would_have` for
> `idle-at-empty-prompt` and stays unexecutable.
>
> The executor is the first real `ActionExecutor`. It **calls the existing verified
> send path that backs `session nudge`** (`sendWithRetryTarget`,
> `cmd/agent-deck/session_cmd.go`) — preconditions, delivery verification, and the
> Escape+Enter escalation that is the only thing that ungates a wedged composer. It
> does not reimplement send. It records the real `delivery` value (`submitted` /
> `typed_not_submitted` / `no_evidence`) into the audit event, so a resume that was
> typed but never submitted is visible as a failure rather than a success.
>
> Prompt text differs by reason. The `usage_limit` prompt must state that a
> subagent may have been terminated by the limit and needs re-dispatching —
> otherwise a parent silently loses a child's work.

> Note the shape: `Agent terminated early due to an API error: …` means a
> *subagent* hit the limit. Resuming the parent does not restore the subagent's
> work; the parent must re-dispatch it.

> ### 1.1 Transport error
>
> The panes were not frozen — they repainted and accepted keystrokes. The network
> recovered long before anyone noticed. A single continuation prompt resumed all
> three on the first attempt (`delivery: submitted`, 3/3).

> ## 3. Architecture
>
> ```
> internal/session/<new>.go          ActionExecutor impl → sendWithRetryTarget
> ```

> ## 6. Verification
>
> **Executor (integration).** A fake send path asserts exactly one delivery per
> confirmed candidate, that the audit event carries the real `delivery` value, and
> that `typed_not_submitted` is recorded as a failure.

---

## Spec gap this task resolves

§3 places the executor in `internal/session`, calling `sendWithRetryTarget`. That
import is impossible: `sendWithRetryTarget` and `executeSend` live in
`package main` (`cmd/agent-deck/session_cmd.go`), and `internal/session` cannot
import `cmd/agent-deck`.

Resolution: a registration seam. `internal/session` declares the send function
type and a setter; `cmd/agent-deck` registers a thin wrapper over `executeSend`
in an `init()`. The executor still calls the one verified send path — nothing is
reimplemented, and `executeSend` is precisely the function `session nudge` calls
(`cmd/agent-deck/session_nudge_cmd.go:154`), carrying the composer-draft guard,
the submit verification and the Escape+Enter escalation.

## Outcome-string contract (from task 03 — do not deviate)

`internal/selfheal/engine.go` defines:

```go
const outcomeDeliveredPrefix = "resumed:submitted"
func outcomeIsDelivered(outcome string) bool { return outcome == outcomeDeliveredPrefix }
```

So the executor **must** return `"resumed:" + delivery`, where `delivery` is the
`session send` contract value. Only `"resumed:submitted"` counts as a healthy
recovery; every other delivery is a failed one and feeds the circuit breaker.

A send that produced a delivery verdict returns `(outcome, nil)` **even when the
underlying send path errored** — `executeSend` returns a non-nil error for
`typed_not_submitted`, and swallowing the delivery string into `"error:…"` would
lose exactly the machine-checkable value §6 requires in the audit. The send
error is logged, not discarded. An error is returned only when there was no
delivery verdict at all (no instance, no registered sender, no pane).

## Acceptance criteria

1. `session.SelfHealSendFunc` and `session.SetSelfHealSender` exist.
2. `session.NewResumeExecutor()` returns a `*ResumeExecutor` satisfying
   `selfheal.ActionExecutor`.
3. `(*ResumeExecutor).SetInstances([]*Instance)` refreshes the id→instance view.
4. `Execute` returns `"resumed:submitted"` for a submitted delivery and
   `"resumed:typed_not_submitted"` for a typed-but-unsubmitted one, both with a
   nil error.
5. `Execute` returns a non-nil error and an empty outcome when the action is not
   `ActionResume`, the session id is unknown, or no sender is registered.
6. The transport and usage-limit prompts differ, and the usage-limit prompt
   mentions re-dispatching a terminated subagent.
7. `cmd/agent-deck` registers the real sender at `init()` and `go build ./...` passes.
8. `go test ./internal/session/ -run SelfHealResume -v` green.

## Edits

### 1. New file `internal/session/selfheal_resume.go`

```go
package session

import (
	"errors"
	"fmt"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/selfheal"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// The self-heal resume executor: the FIRST real selfheal.ActionExecutor.
//
// It delivers exactly one continuation prompt to a session wedged by a transport
// error or an exhausted usage window, through the same verified send path that
// backs `session nudge` — composer-draft guard, submit verification, and the
// Escape+Enter escalation that is the only thing which ungates a wedged composer.
// It does not reimplement send, and it never restarts anything.
//
// Why the send path arrives by registration rather than by import: executeSend
// lives in package main (cmd/agent-deck/session_cmd.go) and internal/session
// cannot import it. cmd/agent-deck registers a wrapper at init, so the one
// verified implementation is still what runs.

// SelfHealSendFunc delivers a message to a session and reports the `session send`
// delivery verdict — one of "submitted", "typed", "typed_not_submitted",
// "unverified", "line_too_long", "no_evidence", "send_failed". The error is the
// send path's own; a non-nil error alongside a delivery string is normal (the
// send path fails loudly for every delivery except "submitted").
type SelfHealSendFunc func(inst *Instance, message string) (delivery string, err error)

var (
	selfHealSenderMu sync.RWMutex
	selfHealSender   SelfHealSendFunc
)

// SetSelfHealSender registers the process-wide send implementation. Called once
// from cmd/agent-deck's init. Passing nil unregisters (tests).
func SetSelfHealSender(fn SelfHealSendFunc) {
	selfHealSenderMu.Lock()
	defer selfHealSenderMu.Unlock()
	selfHealSender = fn
}

func registeredSelfHealSender() SelfHealSendFunc {
	selfHealSenderMu.RLock()
	defer selfHealSenderMu.RUnlock()
	return selfHealSender
}

// Sentinel errors. They are the only three conditions under which a resume
// produces no delivery verdict at all; everything else reports the verdict it got.
var (
	// ErrSelfHealNoSender means no send implementation was registered. In a
	// correctly linked binary this is unreachable — it means the daemon is
	// running a build whose cmd/agent-deck init never ran.
	ErrSelfHealNoSender = errors.New("selfheal: no send implementation registered")
	// ErrSelfHealUnknownSession means the candidate's session id is not in the
	// executor's current view (the session vanished between the poll that built
	// the candidate and the action).
	ErrSelfHealUnknownSession = errors.New("selfheal: candidate session not found")
	// ErrSelfHealWrongAction means the engine asked for something other than a
	// resume. The chokepoint should make this unreachable; it is a mis-wire alarm.
	ErrSelfHealWrongAction = errors.New("selfheal: executor only performs resume")
)

// Resume prompt text. It differs by reason because the two wedges need different
// things said.
const (
	// resumePromptTransport is the transport-error continuation. Field evidence
	// 2026-08-07: one prompt of this shape resumed all three wedged sessions on
	// the first attempt.
	resumePromptTransport = "[agent-deck self-heal] Your previous turn ended on an API transport error " +
		"(the API was unreachable). The connection has since recovered and nothing else has changed. " +
		"Continue exactly where you left off: re-run the step that failed, then carry on."

	// resumePromptUsageLimit is the usage-limit continuation. The subagent warning
	// is mandatory, not decorative: "Agent terminated early due to an API error"
	// means a SUBAGENT hit the limit, and resuming the parent does not restore the
	// subagent's work — without this sentence a parent silently loses a child's
	// output and reports the task done.
	resumePromptUsageLimit = "[agent-deck self-heal] Your previous turn was rejected because the plan usage " +
		"window was exhausted. The window has since reset. Continue exactly where you left off. " +
		"IMPORTANT: if you had dispatched a subagent when the limit hit, that subagent was terminated " +
		"by the limit and its work was NOT saved — re-dispatch it rather than assuming it finished."
)

// resumePromptFor picks the prompt for a candidate's substate. It reads the
// substate rather than the audit's action_params so the executor cannot be
// steered by a malformed params map.
func resumePromptFor(s tmux.Substate) string {
	if s == tmux.SubstateUsageLimit {
		return resumePromptUsageLimit
	}
	return resumePromptTransport
}

// ResumeExecutor is the selfheal.ActionExecutor for ActionResume.
//
// It holds a per-cycle view of the profile's instances because the engine is
// long-lived (the two-read confirm and the cap windows must accumulate across
// polls) while the instance slice is rebuilt every poll. SetInstances refreshes
// the view at the top of each pass.
type ResumeExecutor struct {
	mu   sync.RWMutex
	byID map[string]*Instance
	// send overrides the registered process-wide sender. Non-nil only in tests.
	send SelfHealSendFunc
}

// NewResumeExecutor returns an executor with an empty instance view. Call
// SetInstances before the first Execute.
func NewResumeExecutor() *ResumeExecutor {
	return &ResumeExecutor{byID: map[string]*Instance{}}
}

// SetInstances replaces the id→instance view with this cycle's instances.
func (x *ResumeExecutor) SetInstances(instances []*Instance) {
	view := make(map[string]*Instance, len(instances))
	for _, inst := range instances {
		if inst == nil || inst.ID == "" {
			continue
		}
		view[inst.ID] = inst
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.byID = view
}

func (x *ResumeExecutor) instance(id string) *Instance {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.byID[id]
}

func (x *ResumeExecutor) sender() SelfHealSendFunc {
	x.mu.RLock()
	override := x.send
	x.mu.RUnlock()
	if override != nil {
		return override
	}
	return registeredSelfHealSender()
}

// Execute delivers ONE continuation prompt and reports the real delivery verdict.
//
// The outcome is "resumed:<delivery>", matching the engine's
// outcomeDeliveredPrefix contract: only "resumed:submitted" counts as a healthy
// recovery, so a message typed into a composer that never accepted Enter is
// recorded as the failure it is and feeds the circuit breaker.
//
// A delivery verdict is returned with a NIL error even when the send path itself
// errored — it errors for every delivery except "submitted", and collapsing that
// into an error string would discard the machine-checkable value the audit exists
// to carry. The send error is logged instead. Only the three conditions that
// produce no verdict at all return an error.
func (x *ResumeExecutor) Execute(c selfheal.Candidate, a selfheal.Action) (string, error) {
	if a != selfheal.ActionResume {
		return "", fmt.Errorf("%w: got %q", ErrSelfHealWrongAction, a)
	}
	inst := x.instance(c.SessionID)
	if inst == nil {
		return "", fmt.Errorf("%w: %s", ErrSelfHealUnknownSession, c.SessionID)
	}
	send := x.sender()
	if send == nil {
		return "", ErrSelfHealNoSender
	}

	prompt := resumePromptFor(c.Substate)
	delivery, sendErr := send(inst, prompt)
	if delivery == "" {
		// The send path always sets a delivery on any path that touched the pane.
		// An empty one means it failed before that, which is a verdict of its own.
		delivery = "no_evidence"
	}
	logging.ForComponent(logging.CompNotif).Info("selfheal_resume_delivered",
		"session_id", c.SessionID,
		"title", inst.Title,
		"substate", string(c.Substate),
		"delivery", delivery,
		"send_error", errString(sendErr),
	)
	return "resumed:" + delivery, nil
}

// errString renders an error for a structured log field without a nil check at
// every call site.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
```

**Before writing this file, confirm the logging component constant exists:**
```sh
grep -n 'CompNotif' internal/logging/*.go | head -3
```
It is used by `cmd/agent-deck/notify_daemon_cmd.go:47`, so it exists. If
`logging.ForComponent(logging.CompNotif).Info` does not compile with those exact
arguments, match the call shape used at `cmd/agent-deck/notify_daemon_cmd.go:47`.

### 2. New file `cmd/agent-deck/selfheal_send.go`

```go
package main

import (
	"errors"
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Self-heal's send seam.
//
// The resume executor lives in internal/session, but the verified send path —
// composer-draft guard (#1409), submit verification (#1413), and the Escape+Enter
// escalation that is the only thing which ungates a wedged composer — is
// executeSend here in package main, which internal/session cannot import. So the
// implementation is registered rather than imported. This is the SAME call
// `session nudge` makes (session_nudge_cmd.go), with the same tuning: a self-heal
// resume must not be a looser send than a supervised one.

// errNoTmuxSessionForResume means the instance has no pane to send into. Not a
// delivery failure — there was nothing to deliver to.
var errNoTmuxSessionForResume = errors.New("selfheal: instance has no tmux session")

func init() {
	session.SetSelfHealSender(sendForSelfHeal)
}

// sendForSelfHeal delivers a self-heal resume prompt and returns the delivery
// verdict verbatim, so the audit records the machine-checkable value rather than
// a prose summary of it.
func sendForSelfHeal(inst *session.Instance, message string) (string, error) {
	if inst == nil {
		return "", fmt.Errorf("%w: nil instance", errNoTmuxSessionForResume)
	}
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return "", fmt.Errorf("%w: %s", errNoTmuxSessionForResume, inst.Title)
	}
	res, err := executeSend(tmuxSess, inst.Tool, message, false, defaultSendTuning())
	return res.delivery, err
}
```

## Tests — new file `internal/session/selfheal_resume_test.go`

```go
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
	x.send = f.fn
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
	SetSelfHealSender(nil)
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
```

## Verification

```sh
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume
gofmt -l internal/session/selfheal_resume.go internal/session/selfheal_resume_test.go cmd/agent-deck/selfheal_send.go
```
Expected: **nothing** (empty).

```sh
go build ./... && go vet ./internal/session/ ./cmd/agent-deck/
```
Expected: no output, exit 0. The build is the check that the seam is wired: if
`cmd/agent-deck/selfheal_send.go` does not compile, `executeSend` /
`defaultSendTuning` / `sendDeliveryResult.delivery` were misnamed.

```sh
go test ./internal/session/ -run SelfHealResume -count=1 -v
```
Expected: `ok  	github.com/asheshgoplani/agent-deck/internal/session`, nine
`--- PASS` lines. Run-specific sentinel:
`TestSelfHealResume_TypedNotSubmitted_IsAFailureOutcome` must appear as
`--- PASS` — it is the §6 requirement that a typed-but-unsubmitted resume is
recorded as a failure.

Confirm the registration actually runs in the linked binary (the seam's one real
failure mode is an `init()` in a file the build drops):
```sh
grep -n 'SetSelfHealSender' cmd/agent-deck/selfheal_send.go internal/session/selfheal_resume.go
```
Expected: one hit in each file.

## Commit

```sh
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume add \
  internal/session/selfheal_resume.go internal/session/selfheal_resume_test.go \
  cmd/agent-deck/selfheal_send.go
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume commit -m "feat(session): add the self-heal resume executor over the verified send path

The first real ActionExecutor. It delivers one continuation prompt through
executeSend — the same call session nudge makes, with the same tuning — so it
inherits the composer-draft guard, the submit verification and the Escape+Enter
escalation rather than reimplementing any of them.

executeSend lives in package main and internal/session cannot import it, so the
implementation is registered at init instead. The executor reports the delivery
verdict verbatim as \"resumed:<delivery>\" and returns a nil error alongside it:
the send path errors for every delivery except submitted, and collapsing that
into an error string would discard the one machine-checkable value the audit
exists to carry. A resume typed into a composer that never accepted Enter is
therefore visible as the failure it is.

The usage-limit prompt warns that a subagent may have been terminated by the
limit and needs re-dispatching — without it a parent silently loses a child's
work and reports the task done."
```

## Interfaces

### consumes
- `internal/selfheal`: `selfheal.Candidate` (fields `SessionID`, `Title`, `Substate`), `selfheal.Action`, `selfheal.ActionResume` (**task 02**), `selfheal.ActionRestart`, `selfheal.ActionExecutor` interface `Execute(c Candidate, a Action) (outcome string, err error)` (**task 03**)
- `internal/selfheal/engine.go` (**task 03**): the outcome contract `"resumed:" + delivery`, where only `"resumed:submitted"` is healthy
- `internal/tmux`: `tmux.Substate`, `tmux.SubstateAPIError` (**task 01**), `tmux.SubstateUsageLimit`
- `internal/session/instance.go`: `Instance` (fields `ID`, `Title`, `Tool`), `(*Instance).GetTmuxSession() *tmux.Session`
- `cmd/agent-deck/session_cmd.go`: `executeSend(target sendRetryTarget, tool, message string, noWait bool, tun sendExecTuning) (sendDeliveryResult, error)`, `defaultSendTuning() sendExecTuning`, `sendDeliveryResult.delivery string`
- `internal/logging`: `ForComponent(logging.CompNotif)`

### produces
- `internal/session/selfheal_resume.go`: `type SelfHealSendFunc func(inst *Instance, message string) (delivery string, err error)`
- `internal/session/selfheal_resume.go`: `func SetSelfHealSender(fn SelfHealSendFunc)`
- `internal/session/selfheal_resume.go`: `type ResumeExecutor struct{…}` implementing `selfheal.ActionExecutor`
- `internal/session/selfheal_resume.go`: `func NewResumeExecutor() *ResumeExecutor` — **task 06 constructs one per profile, holds it for the engine's lifetime, and calls `SetInstances` at the top of every pass**
- `internal/session/selfheal_resume.go`: `func (x *ResumeExecutor) SetInstances(instances []*Instance)`
- `internal/session/selfheal_resume.go`: `func (x *ResumeExecutor) Execute(c selfheal.Candidate, a selfheal.Action) (string, error)`
- `internal/session/selfheal_resume.go`: `var ErrSelfHealNoSender`, `ErrSelfHealUnknownSession`, `ErrSelfHealWrongAction`
- `cmd/agent-deck/selfheal_send.go`: `func init()` registering `sendForSelfHeal`

## Record (append-only)
