package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Round 2 of PR #2043, the maintainer's red path: `session send --wait` lands
// while the target is mid-turn, so the message is queued. The in-flight turn
// finishes first, and its reply is written AFTER sentAt — exactly what a
// sentAt-only freshness check (the pre-#2043 waitForFreshOutput) accepts as
// the queued message's answer. The reply returned must belong to the queued
// turn, and the in-flight tail must never be returned as its reply.

func appendJSONL(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, line := range lines {
		fmt.Fprintln(f, line)
	}
}

func jsonlUser(uuid, ts, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":%q,"message":{"role":"user","content":%q}}`, uuid, ts, text)
}

func jsonlAssistant(uuid, ts, text, stop string) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}],"stop_reason":%q}}`, uuid, ts, text, stop)
}

func TestIssue1978_WaitReplyBelongsToQueuedTurnNotInFlightOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return base.Add(d).Format(time.RFC3339Nano) }
	const queued = "queued question for the busy target"

	// Before the send: a turn is in flight.
	appendJSONL(t, path, jsonlUser("inflight-user", ts(-10*time.Second), "long task"))
	cursor, err := session.TranscriptCursor(path)
	if err != nil {
		t.Fatal(err)
	}
	sentAt := base
	deadline := time.Now().Add(5 * time.Second)

	// Claude finishes the in-flight turn after the send, then starts the
	// queued turn and answers it.
	go func() {
		time.Sleep(30 * time.Millisecond)
		appendJSONL(t, path, jsonlAssistant("inflight-reply", ts(2*time.Second), "IN-FLIGHT TAIL", "end_turn"))
		time.Sleep(30 * time.Millisecond)
		appendJSONL(t, path, jsonlUser("queued-user", ts(3*time.Second), queued))
		time.Sleep(30 * time.Millisecond)
		appendJSONL(t, path, jsonlAssistant("queued-reply", ts(4*time.Second), "QUEUED REPLY", "end_turn"))
	}()

	// Phase 1: identity. Must block until the queued turn's own user record
	// exists, not return on the in-flight turn's completion.
	turnID, err := session.AwaitTurnIdentity(session.TurnQuery{
		Path: path, Prompt: queued, Cursor: cursor, NotBefore: sentAt.Add(-turnIdentityClockSkew),
	}, time.Until(deadline), 5*time.Millisecond)
	if err != nil {
		t.Fatalf("turn identity: %v", err)
	}
	if turnID.UUID != "queued-user" {
		t.Fatalf("bound to %q, want the queued turn's user record", turnID.UUID)
	}

	// Phase 2: completion + reply. The injected completion stands in for the
	// pane status poll; it reports the prompt reappearing immediately, which
	// is also what happens when the spike-filtered heuristic reads idle
	// mid-turn (#1578) — the reply phase must still wait for THIS turn.
	resp, _, completionErr, responseErr := awaitClaudeTurnReply(turnID, deadline, func(time.Duration) (string, error) {
		return "waiting", nil
	})
	if completionErr != nil || responseErr != nil {
		t.Fatalf("completion=%v response=%v", completionErr, responseErr)
	}
	if resp.Content != "QUEUED REPLY" {
		t.Fatalf("--wait returned %q, want the queued turn's reply, never the in-flight tail", resp.Content)
	}
}

// TestIssue1978_WaitReplyHonoursOneDeadline: the identity, completion and
// reply phases share ONE --timeout budget (CodeRabbit round-2 finding: the
// flag was spent up to three times). A turn that never ends returns within
// the deadline with the partial text flagged incomplete.
func TestIssue1978_WaitReplyHonoursOneDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	prefix := jsonlUser("mine", "", "q") + "\n"
	if err := os.WriteFile(path, []byte(prefix), 0o600); err != nil {
		t.Fatal(err)
	}
	appendJSONL(t, path, jsonlAssistant("partial", "", "working on it", "tool_use"))
	turnID := session.TurnIdentity{UUID: "mine", Path: path, StartOffset: int64(len(prefix))}

	deadline := time.Now().Add(300 * time.Millisecond)
	var completionBudget time.Duration
	start := time.Now()
	resp, _, completionErr, responseErr := awaitClaudeTurnReply(turnID, deadline, func(remaining time.Duration) (string, error) {
		completionBudget = remaining
		time.Sleep(100 * time.Millisecond)
		return "waiting", nil
	})
	elapsed := time.Since(start)

	if completionErr != nil {
		t.Fatalf("completion: %v", completionErr)
	}
	if completionBudget > 300*time.Millisecond {
		t.Fatalf("completion phase was handed %s, more than the whole budget", completionBudget)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("reply phase ran %s past a 300ms shared deadline", elapsed)
	}
	if !errors.Is(responseErr, session.ErrTurnResponseIncomplete) || resp == nil || resp.Content != "working on it" {
		t.Fatalf("resp=%+v err=%v, want the partial text flagged incomplete", resp, responseErr)
	}
}
