package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// seedDeferHookFile writes an on-disk hook status file for instanceID, the same
// artifact Claude's UserPromptSubmit/Stop hooks write and that `list --json`
// reads. ts=now keeps it inside the hook fast-path freshness window.
func seedDeferHookFile(t *testing.T, instanceID, event, sessionID, status string) {
	t.Helper()
	dir := session.GetHooksDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	payload := map[string]any{
		"status":     status,
		"session_id": sessionID,
		"event":      event,
		"ts":         time.Now().Unix(),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal hook payload: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, instanceID+".json"), data, 0o644); err != nil {
		t.Fatalf("write hook file: %v", err)
	}
}

func setupDeferTest(t *testing.T, profile string) *session.Instance {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("new storage: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := &session.Instance{
		ID:          "inst-defer-1578",
		Title:       "busy-parent",
		Tool:        "claude",
		ProjectPath: projectDir,
		Command:     "claude",
		// A bound session id so the seeded hook (carrying the same id) is
		// accepted by UpdateHookStatus's ownership guard rather than rejected as
		// a foreign ephemeral.
		ClaudeSessionID: "sid-defer-1578",
	}
	if err := storage.Save([]*session.Instance{inst}); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	return inst
}

// The core #1578 correctness claim: fetchHookDrivenStatus reports the target as
// busy ("running") while its hook file shows the UserPromptSubmit edge, and as
// not-busy ("waiting") once the Stop hook fires — the true turn-finished signal,
// available even without a live tmux handle.
func TestFetchHookDrivenStatus_BusyThenIdle(t *testing.T) {
	profile := "defer_hook_status"
	inst := setupDeferTest(t, profile)

	seedDeferHookFile(t, inst.ID, "UserPromptSubmit", inst.ClaudeSessionID, "running")
	got, err := fetchHookDrivenStatus(profile, inst.Title)
	if err != nil {
		t.Fatalf("fetchHookDrivenStatus (running): %v", err)
	}
	if !send.StatusIsBusy(got) {
		t.Fatalf("status = %q, want a busy status while the hook shows running", got)
	}

	seedDeferHookFile(t, inst.ID, "Stop", inst.ClaudeSessionID, "waiting")
	got, err = fetchHookDrivenStatus(profile, inst.Title)
	if err != nil {
		t.Fatalf("fetchHookDrivenStatus (waiting): %v", err)
	}
	if send.StatusIsBusy(got) {
		t.Fatalf("status = %q, want a non-busy status after the Stop hook", got)
	}
}

// End-to-end gate: the real fetchHookDrivenStatus wired into the real
// WaitUntilNotBusy hold loop. The delivery gate stays closed across busy polls
// and opens only after the Stop-hook edge flips the hook file — proving the
// message is HELD (not interrupt-delivered) mid-turn.
func TestDeferIfBusy_GateOpensOnlyAfterStopHook(t *testing.T) {
	profile := "defer_gate"
	inst := setupDeferTest(t, profile)
	seedDeferHookFile(t, inst.ID, "UserPromptSubmit", inst.ClaudeSessionID, "running")

	var polls int32
	fetch := func() (string, error) {
		n := atomic.AddInt32(&polls, 1)
		// Flip to the Stop edge on the 3rd poll; earlier polls must keep the
		// gate closed.
		if n == 3 {
			seedDeferHookFile(t, inst.ID, "Stop", inst.ClaudeSessionID, "waiting")
		}
		return fetchHookDrivenStatus(profile, inst.Title)
	}

	err := send.WaitUntilNotBusy(fetch, 30*time.Minute, 10*time.Millisecond, func(time.Duration) {})
	if err != nil {
		t.Fatalf("WaitUntilNotBusy returned error: %v", err)
	}
	if got := atomic.LoadInt32(&polls); got < 3 {
		t.Fatalf("gate opened after %d polls, want >= 3 (must hold through busy polls)", got)
	}
}

// Timeout path: a target that never leaves "running" is never delivered to;
// the hold returns a non-zero error (drop-on-timeout).
func TestDeferIfBusy_TimeoutDropsMessage(t *testing.T) {
	profile := "defer_timeout"
	inst := setupDeferTest(t, profile)
	seedDeferHookFile(t, inst.ID, "UserPromptSubmit", inst.ClaudeSessionID, "running")

	fetch := func() (string, error) { return fetchHookDrivenStatus(profile, inst.Title) }
	err := send.WaitUntilNotBusy(fetch, 1*time.Nanosecond, 10*time.Millisecond, func(time.Duration) {})
	if err == nil {
		t.Fatal("WaitUntilNotBusy returned nil for a perpetually-busy target; message must be dropped")
	}
}

// A ref that resolves to no session surfaces an error from fetchHookDrivenStatus
// (which the fail-fast path in WaitUntilNotBusy then bounds).
func TestFetchHookDrivenStatus_UnknownSession(t *testing.T) {
	profile := "defer_unknown"
	setupDeferTest(t, profile)
	if _, err := fetchHookDrivenStatus(profile, "no-such-session-xyz"); err == nil {
		t.Fatal("fetchHookDrivenStatus returned nil error for an unknown session")
	}
}
