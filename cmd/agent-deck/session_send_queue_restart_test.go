package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
)

// forbidSendChild fails the test if anything would type the message.
func forbidSendChild(t *testing.T) *int {
	t.Helper()
	calls := 0
	prev := sendChild
	sendChild = func(profile, id, message, resultPath string) (int, func() int, error) {
		calls++
		t.Errorf("message typed again (call %d)", calls)
		return 0, nil, errors.New("forbidden")
	}
	t.Cleanup(func() { sendChild = prev })
	return &calls
}

// seedTyping writes the record a worker leaves behind when it dies after
// handing the message to its `session send` child.
func seedTyping(t *testing.T, dir, tool, transcript string, childPID int, sentAt time.Time) *sendqueue.Record {
	t.Helper()
	now := time.Now()
	rec := &sendqueue.Record{
		SendID: sendqueue.NewID(now), State: sendqueue.StateTyping, SessionID: "target-1", Tool: tool,
		Message: "restart probe: reply OK", Attempts: 1, ChildPID: childPID, TranscriptPath: transcript,
		SentAt:    sentAt.UTC().Format(time.RFC3339Nano),
		CreatedAt: now.UTC().Format(time.RFC3339Nano), UpdatedAt: now.UTC().Format(time.RFC3339Nano),
		Deadline: now.Add(time.Hour).UTC().Format(time.RFC3339Nano),
	}
	if err := sendqueue.Save(dir, rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func claudeUserLine(uuid, text string, ts time.Time) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q},"uuid":%q,"timestamp":%q,"sessionId":"s","isSidechain":false}`+"\n",
		text, uuid, ts.UTC().Format("2006-01-02T15:04:05.000Z"))
}

// TestSendWorkerRestartWhileChildRunsDoesNotRetype: the worker died while its
// child was still typing (review of the macapp core surface: the next worker
// saw "queued" and typed the message a second time). The restarted worker
// waits for the child, takes its result, finds the landed row, and never
// types again.
func TestSendWorkerRestartWhileChildRunsDoesNotRetype(t *testing.T) {
	t.Setenv("AGENTDECK_SEND_LAND_WINDOW", "5s")
	calls := forbidSendChild(t)
	dir := t.TempDir()
	sentAt := time.Now()
	transcript := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(transcript, []byte(claudeUserLine("old", "restart probe: reply OK", sentAt.Add(-time.Hour))), 0o644); err != nil {
		t.Fatal(err)
	}
	// The orphaned child: it lands the message, then reports submitted.
	resultPath := ""
	child := exec.Command("sh", "-c", `sleep 1; printf '%s' "$LINE" >> "$TRANSCRIPT"; printf '%s' '{"success":true,"submitted":true,"delivery":"submitted"}' > "$RESULT"`)
	rec := seedTyping(t, dir, "claude", transcript, 0, sentAt)
	resultPath = sendqueue.ResultPath(dir, rec.SendID)
	child.Env = append(os.Environ(), "LINE="+claudeUserLine("landed-1", "restart probe: reply OK", sentAt.Add(time.Second)), "TRANSCRIPT="+transcript, "RESULT="+resultPath)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = child.Wait() }()
	if _, err := sendqueue.Update(dir, rec.SendID, time.Now(), func(r *sendqueue.Record) { r.ChildPID = child.Process.Pid }); err != nil {
		t.Fatal(err)
	}
	rec, _ = sendqueue.Load(dir, rec.SendID)

	deliverQueued("", dir, rec)

	got, err := sendqueue.Load(dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 0 || got.Attempts != 1 {
		t.Fatalf("retyped: %d child starts, attempts %d", *calls, got.Attempts)
	}
	if got.State != sendqueue.StateLanded || got.LandedRowID != "landed-1" {
		t.Fatalf("record = %+v, want landed on the child's row (not the earlier identical one)", got)
	}
}

// TestSendWorkerRestartAfterChildVanishedSettlesUnknown: the worker and its
// child are both gone and no result was written. The outcome is unknown:
// the record settles typed with that reason — never failed (a client that
// resends on failed would double it) and never typed again.
func TestSendWorkerRestartAfterChildVanishedSettlesUnknown(t *testing.T) {
	t.Setenv("AGENTDECK_SEND_LAND_WINDOW", "300ms")
	calls := forbidSendChild(t)
	dir := t.TempDir()
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(transcript, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rec := seedTyping(t, dir, "claude", transcript, dead.Process.Pid, time.Now())

	deliverQueued("", dir, rec)

	got, _ := sendqueue.Load(dir, rec.SendID)
	if *calls != 0 || got.Attempts != 1 {
		t.Fatalf("retyped: %d child starts, attempts %d", *calls, got.Attempts)
	}
	if got.State != sendqueue.StateTyped || !got.Settled || !got.Final() || !strings.Contains(got.Reason, "outcome unknown") {
		t.Fatalf("record = %+v, want settled typed with an unknown outcome", got)
	}
	// A later worker has nothing left to do for this target.
	if next := nextPending(dir, "target-1"); next != nil {
		t.Fatalf("settled record still pending: %+v", next)
	}
}

// TestTypeQueuedPersistsTypingBeforeTheChild: the record is typing, with its
// attempt counted, on disk before the child that types it starts.
func TestTypeQueuedPersistsTypingBeforeTheChild(t *testing.T) {
	t.Setenv("AGENTDECK_SEND_LAND_WINDOW", "300ms")
	dir := t.TempDir()
	now := time.Now()
	rec := &sendqueue.Record{SendID: sendqueue.NewID(now), State: sendqueue.StateQueued, SessionID: "target-2", Tool: "shell", Message: "echo hi",
		CreatedAt: now.UTC().Format(time.RFC3339Nano), Deadline: now.Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	if err := sendqueue.Save(dir, rec); err != nil {
		t.Fatal(err)
	}
	prev := sendChild
	t.Cleanup(func() { sendChild = prev })
	sendChild = func(profile, id, message, resultPath string) (int, func() int, error) {
		onDisk, err := sendqueue.Load(dir, rec.SendID)
		if err != nil || onDisk.State != sendqueue.StateTyping || onDisk.Attempts != 1 || onDisk.SentAt == "" {
			t.Fatalf("record when the child starts: %+v %v", onDisk, err)
		}
		_ = os.WriteFile(resultPath, []byte(`{"success":false,"error":"timeout waiting for agent: pane not ready","code":"INVALID_OPERATION"}`), 0o600)
		return 4242, func() int { return 1 }, nil
	}
	set := func(fn func(*sendqueue.Record)) error {
		r, err := sendqueue.Update(dir, rec.SendID, time.Now(), fn)
		if err == nil {
			*rec = *r
		}
		return err
	}
	if !typeQueued("", dir, rec, "waiting", "", 0, set) {
		t.Fatal("typeQueued reported a failed write")
	}
	// A readiness timeout after the hand-off is not a failure: unknown.
	if rec.State != sendqueue.StateTyped || !strings.Contains(rec.Reason, "outcome unknown") || rec.ChildPID != 0 {
		t.Fatalf("after a ready timeout: %+v", rec)
	}
}

// TestClassifyChild: only refusals that guarantee nothing was typed retry;
// menu_open, a readiness timeout, no_evidence and crashes are unknown, never
// failed.
func TestClassifyChild(t *testing.T) {
	cases := []struct {
		name   string
		result map[string]interface{}
		code   int
		want   childOutcome
	}{
		{"submitted", map[string]interface{}{"success": true, "submitted": true}, 0, childSubmitted},
		{"confirmed codex", map[string]interface{}{"success": true, "confirmation": "confirmed"}, 0, childSubmitted},
		{"delivered unconfirmed", map[string]interface{}{"success": true, "delivery": "delivered"}, 0, childTyped},
		{"target busy", map[string]interface{}{"success": false, "delivery": "target_busy"}, 1, childNotSent},
		{"composer blocked", map[string]interface{}{"success": false, "delivery": "composer_blocked"}, 1, childNotSent},
		{"codex acceptance refused", map[string]interface{}{"success": false, "delivery": "acceptance_refused"}, 1, childNotSent},
		{"menu open", map[string]interface{}{"success": false, "delivery": "menu_open"}, 1, childUnknown},
		{"no evidence", map[string]interface{}{"success": false, "delivery": "no_evidence"}, 1, childUnknown},
		{"ready timeout", map[string]interface{}{"success": false, "error": "timeout waiting for agent: x"}, 1, childUnknown},
		{"crash, no output", map[string]interface{}{}, 2, childUnknown},
	}
	for _, tc := range cases {
		if got, _ := classifyChild(tc.result, tc.code); got != tc.want {
			t.Errorf("%s: outcome %d, want %d", tc.name, got, tc.want)
		}
	}
}
