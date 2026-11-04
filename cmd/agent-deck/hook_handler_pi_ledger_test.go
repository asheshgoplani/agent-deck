package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// PR-B commit 3a: hook-handler gains a pi branch for before_agent_start (design
// A3): spool the prompt_start edge with the doorbell's nonce, ask the ledger
// for the run's context, print it as plain text (pi injects stdout as the
// before_agent_start message, not Claude's JSON envelope), and tell the ledger
// whether it was printed. It never runs the bare inbox drain for a pi session
// the ledger does not serve.

const piParentID = "pi-parent-3a"

// enrolPiParent builds an enrolled pi parent the way the daemon leaves it: a
// ledger with the parent's consumer state in ledger mode, one pending record,
// and the marker its hooks find the ledger through.
func enrolPiParent(t *testing.T, text string) (*comms.Reader, comms.Record) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	notABackgroundWorker(t)
	t.Setenv("AGENTDECK_PROFILE", "default")
	t.Setenv("AGENTDECK_INSTANCE_ID", piParentID)
	t.Cleanup(session.SetCommsLedgerForTest(true))
	l, err := comms.Open("default")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	r := l.Reader()
	if _, err := r.Enroll(piParentID, false); err != nil {
		t.Fatal(err)
	}
	rec, _, err := l.Commit(comms.Record{Kind: comms.KindTurn, From: "child-1", To: []string{piParentID},
		Tier: comms.TierInfo, Text: text, TRecord: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	dir, err := agentpaths.EffectiveDataPath(filepath.Join("runtime", "comms", "consumers"), "runtime")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(map[string]any{"profile": "default", "dir": r.Dir, "tool": "pi", "at": time.Now().UnixMilli(), "pi_socket": true})
	if err := os.WriteFile(filepath.Join(dir, piParentID+".json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := session.LoadCommsEnrollment(piParentID); !ok {
		t.Fatal("test setup: the marker is not readable")
	}
	return r, rec
}

func spoolJSON(t *testing.T, id string) []string {
	t.Helper()
	entries, err := session.ReadCommsSpool(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		raw, _ := json.Marshal(e)
		out = append(out, string(raw))
	}
	return out
}

func TestHookHandler_PiBeforeAgentStartSpoolsPromptStartAndPrintsContext(t *testing.T) {
	r, rec := enrolPiParent(t, "child result text for the model")
	payload := `{"hook_event_name":"before_agent_start","source":"pi","pane_host":true,"cwd":"/tmp","session_id":"pi-sess","prompt":"what is the status?"}`
	out := captureStdout(t, func() { runHookHandlerWith(t, payload) })

	// Plain text: pi returns stdout as the injected message.
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("a pi context is plain text, not a JSON envelope: %.120q", out)
	}
	if !strings.Contains(out, "child result text for the model") || !strings.Contains(out, comms.WakePrefix) {
		t.Errorf("stdout lacks the pending record: %q", out)
	}
	// The prompt_start edge is spooled for trigger classification.
	spooled := spoolJSON(t, piParentID)
	found := false
	for _, s := range spooled {
		if strings.Contains(s, `"harness":"pi"`) && strings.Contains(s, `"edge":"prompt_start"`) && strings.Contains(s, "what is the status?") {
			found = true
		}
	}
	if !found {
		t.Errorf("no pi prompt_start edge in the spool: %v", spooled)
	}
	// R-A: printing is not proof pi took the context (the extension discards
	// stdout on a timeout or a non-zero exit), so the attempt stays attempted and
	// carries the delivery token the extension must echo; the token is printed as
	// the last line, in the form the extension strips.
	st, ok, err := comms.ReadConsumer(r.Dir, piParentID)
	if err != nil || !ok {
		t.Fatalf("consumer state: %v %v", ok, err)
	}
	if len(st.Inflight) != 1 || st.Inflight[0].ID != rec.ID || st.Inflight[0].State != comms.ReceiptAttempted || st.Inflight[0].Via != comms.ViaPrompt {
		t.Fatalf("inflight after a printed injection: %+v", st.Inflight)
	}
	raw, _ := json.Marshal(st.Inflight[0])
	var attempt map[string]any
	_ = json.Unmarshal(raw, &attempt)
	m := regexp.MustCompile(`(?m)^<!-- agent-deck-delivery:([0-9A-Z]{26}) -->$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("stdout carries no delivery token line: %q", out)
	}
	if attempt["token"] != m[1] {
		t.Errorf("the attempt records token %v, stdout printed %q", attempt["token"], m[1])
	}
}

func TestHookHandler_PiDoorbellRunSpoolsTheNonce(t *testing.T) {
	_, _ = enrolPiParent(t, "second result")
	payload := `{"hook_event_name":"before_agent_start","source":"pi","pane_host":true,"cwd":"/tmp","session_id":"pi-sess","prompt":"[agent-deck msg] 1 pending (0 urgent): x","wake_nonce":"01J0ABCDEF0123456789ABCDEF"}`
	_ = captureStdout(t, func() { runHookHandlerWith(t, payload) })
	found := false
	for _, s := range spoolJSON(t, piParentID) {
		if strings.Contains(s, `"edge":"prompt_start"`) && strings.Contains(s, `"wake_nonce":"01J0ABCDEF0123456789ABCDEF"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("the doorbell's nonce did not reach the spooled prompt_start edge: %v", spoolJSON(t, piParentID))
	}
}

// A pi session the ledger does not serve gets nothing printed, and its inbox is
// not drained by this hook (the blast radius is listed parents only), but the
// prompt edge is still spooled.
func TestHookHandler_PiUnenrolledSessionPrintsNothingAndLeavesTheInbox(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	notABackgroundWorker(t)
	t.Setenv("AGENTDECK_PROFILE", "default")
	t.Setenv("AGENTDECK_INSTANCE_ID", "pi-unlisted-3a")
	t.Cleanup(session.SetCommsLedgerForTest(true))
	if err := session.CommitToInbox("pi-unlisted-3a", session.TransitionNotificationEvent{ChildSessionID: "kid", ChildTitle: "kid", ToStatus: "waiting",
		Tier: session.TurnTierInfo, Text: "inbox result", TargetKind: "parent", Profile: "default"}); err != nil {
		t.Fatal(err)
	}
	payload := `{"hook_event_name":"before_agent_start","source":"pi","pane_host":true,"cwd":"/tmp","prompt":"hello"}`
	out := captureStdout(t, func() { runHookHandlerWith(t, payload) })
	if strings.TrimSpace(out) != "" {
		t.Errorf("an unenrolled pi session got output: %q", out)
	}
	if !session.InboxHasPending("pi-unlisted-3a") {
		t.Error("the hook drained the inbox of a pi session the ledger does not serve")
	}
	found := false
	for _, s := range spoolJSON(t, "pi-unlisted-3a") {
		if strings.Contains(s, `"edge":"prompt_start"`) && strings.Contains(s, `"harness":"pi"`) {
			found = true
		}
	}
	if !found {
		t.Error("the prompt_start edge was not spooled for an unenrolled pi session")
	}
}

// Claude's prompt hook is unchanged: it prints the JSON envelope with the
// pending record, and its stdout is consumed by Claude itself, so printing
// still accepts the attempt.
func TestHookHandler_ClaudePromptHookStillPrintsItsEnvelope(t *testing.T) {
	r, rec := enrolPiParent(t, "child result for claude")
	// The instance is a Claude session for this event: the marker names the tool.
	dir, err := agentpaths.EffectiveDataPath(filepath.Join("runtime", "comms", "consumers"), "runtime")
	if err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(map[string]any{"profile": "default", "dir": r.Dir, "tool": "claude", "at": time.Now().UnixMilli()})
	if err := os.WriteFile(filepath.Join(dir, piParentID+".json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"8f3c2a1e-0000-4000-8000-000000000001","prompt":"hi","cwd":"/tmp"}`
	out := captureStdout(t, func() { runHookHandlerWith(t, payload) })
	var env map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		t.Fatalf("Claude's hook output is a JSON envelope: %v\n%q", err, out)
	}
	if !strings.Contains(out, "child result for claude") {
		t.Errorf("the envelope lacks the pending record: %q", out)
	}
	st, _, _ := comms.ReadConsumer(r.Dir, piParentID)
	if len(st.Inflight) != 1 || st.Inflight[0].ID != rec.ID || st.Inflight[0].State != comms.ReceiptTransportAccepted {
		t.Errorf("a Claude injection is accepted when printed: %+v", st.Inflight)
	}
}

// R-A: the extension's receipt, a pi event carrying injected_token, is spooled
// for the daemon as an "injected" edge naming the token; an event without one
// spools nothing (turn_start stays status-only).
func TestHookHandler_PiInjectedTokenIsSpooledOnTurnStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	notABackgroundWorker(t)
	t.Setenv("AGENTDECK_PROFILE", "default")
	t.Setenv("AGENTDECK_INSTANCE_ID", "pi-receipt-3r")
	t.Cleanup(session.SetCommsLedgerForTest(true))
	runHookHandlerWith(t, `{"hook_event_name":"turn_start","source":"pi","cwd":"/tmp"}`)
	if got := spoolJSON(t, "pi-receipt-3r"); len(got) != 0 {
		t.Fatalf("a turn_start without a token spooled %v", got)
	}
	runHookHandlerWith(t, `{"hook_event_name":"turn_start","source":"pi","cwd":"/tmp","injected_token":"01J0DELIVERYTOKEN00000000A"}`)
	found := false
	for _, s := range spoolJSON(t, "pi-receipt-3r") {
		if strings.Contains(s, `"edge":"injected"`) && strings.Contains(s, `"harness":"pi"`) && strings.Contains(s, `"injected_token":"01J0DELIVERYTOKEN00000000A"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("the receipt was not spooled as an injected edge: %v", spoolJSON(t, "pi-receipt-3r"))
	}
}

// The v5 extension sends the receipt on the same turn_start as #2827's run
// fields: one payload carries settles, run_id and injected_token. Both readers
// see their half: the spool gets the injected edge with the token, and the
// status file keeps the capability bit and the run token.
func TestHookHandler_PiTurnStartCarriesTheRunFieldsAndTheReceiptTogether(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	notABackgroundWorker(t)
	t.Setenv("AGENTDECK_PROFILE", "default")
	t.Setenv("AGENTDECK_INSTANCE_ID", "pi-receipt-run")
	t.Cleanup(session.SetCommsLedgerForTest(true))
	runHookHandlerWith(t, `{"hook_event_name":"turn_start","source":"pi","cwd":"/tmp","settles":true,"run_id":"run-A","injected_token":"01J0DELIVERYTOKEN00000000B"}`)
	injected := 0
	for _, s := range spoolJSON(t, "pi-receipt-run") {
		if strings.Contains(s, `"edge":"injected"`) && strings.Contains(s, `"harness":"pi"`) && strings.Contains(s, `"injected_token":"01J0DELIVERYTOKEN00000000B"`) {
			injected++
		}
	}
	if injected != 1 {
		t.Errorf("injected edges with the token = %d, want 1: %v", injected, spoolJSON(t, "pi-receipt-run"))
	}
	got := piHookStatusFile(t, "pi-receipt-run")
	if got["event"] != "turn_start" || got["status"] != "running" {
		t.Errorf("status file event/status = %v/%v, want turn_start/running", got["event"], got["status"])
	}
	if got["settles"] != true || got["pi_run_id"] != "run-A" {
		t.Errorf("settles = %v, pi_run_id = %v, want true and run-A beside the receipt", got["settles"], got["pi_run_id"])
	}
}

// Max 2: the receipt rides the next run start. The daemon's ingest never runs
// here: hook-handler alone gets the first run's token back in the second
// before_agent_start payload, accepts that attempt, and settles it, so the
// record is not injected twice.
func TestHookHandler_PiPriorInjectedTokenAcceptsWithoutTheDaemon(t *testing.T) {
	r, rec := enrolPiParent(t, "child result text for the model")
	first := `{"hook_event_name":"before_agent_start","source":"pi","pane_host":true,"cwd":"/tmp","session_id":"pi-sess","prompt":"run one"}`
	out := captureStdout(t, func() { runHookHandlerWith(t, first) })
	m := regexp.MustCompile(`(?m)^<!-- agent-deck-delivery:([0-9A-Z]{26}) -->$`).FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, "child result text for the model") {
		t.Fatalf("first run start printed no context and token: %q", out)
	}
	second := `{"hook_event_name":"before_agent_start","source":"pi","pane_host":true,"cwd":"/tmp","session_id":"pi-sess","prompt":"run two","prior_injected_token":"` + m[1] + `"}`
	out2 := captureStdout(t, func() { runHookHandlerWith(t, second) })
	if strings.Contains(out2, "child result text for the model") {
		t.Errorf("the record was injected again although the receipt arrived: %q", out2)
	}
	st, ok, err := comms.ReadConsumer(r.Dir, piParentID)
	if err != nil || !ok {
		t.Fatalf("consumer state: %v %v", ok, err)
	}
	if len(st.Inflight) != 0 {
		t.Errorf("the accepted attempt for %s was not settled by the second run start: %+v", rec.ID, st.Inflight)
	}
}

// A stale or foreign prior token accepts nothing: the attempt stays attempted,
// still waiting for its own receipt (or the end of the receipt window).
func TestHookHandler_PiForeignPriorTokenChangesNothing(t *testing.T) {
	r, rec := enrolPiParent(t, "child result text for the model")
	first := `{"hook_event_name":"before_agent_start","source":"pi","pane_host":true,"cwd":"/tmp","session_id":"pi-sess","prompt":"run one"}`
	_ = captureStdout(t, func() { runHookHandlerWith(t, first) })
	second := `{"hook_event_name":"before_agent_start","source":"pi","pane_host":true,"cwd":"/tmp","session_id":"pi-sess","prompt":"run two","prior_injected_token":"01J0FOREIGNTOKEN00000000AB"}`
	_ = captureStdout(t, func() { runHookHandlerWith(t, second) })
	st, ok, err := comms.ReadConsumer(r.Dir, piParentID)
	if err != nil || !ok {
		t.Fatalf("consumer state: %v %v", ok, err)
	}
	if len(st.Inflight) != 1 || st.Inflight[0].ID != rec.ID || st.Inflight[0].State == comms.ReceiptTransportAccepted || st.Pending != 1 {
		t.Errorf("a foreign token moved the attempt or acked the record: pending %d, %+v", st.Pending, st.Inflight)
	}
}
