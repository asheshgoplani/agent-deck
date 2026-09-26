package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// newIssue2361Instance returns a Claude instance on a real, inert tmux pane
// (never renders a prompt or busy signal) whose startup clock is already past
// the 2-minute window, bound to Claude session "owner-session".
func newIssue2361Instance(t *testing.T, name string) *Instance {
	t.Helper()
	skipIfNoTmuxBinary(t)

	dir := t.TempDir()
	inst := NewInstance(name, dir)
	inst.Tool = "claude"
	inst.ClaudeSessionID = "owner-session"
	inst.tmuxSession = tmux.NewSession(inst.Title, dir)
	if err := inst.tmuxSession.Start("sleep 300"); err != nil {
		t.Fatalf("start inert pane: %v", err)
	}
	t.Cleanup(func() { _ = inst.tmuxSession.Kill() })

	inst.lastStartTime = time.Now().Add(-3 * time.Minute)
	inst.tmuxSession.SetStartupAtForTest(time.Now().Add(-3 * time.Minute))
	return inst
}

// The owning agent's hook ends the startup phase, so when the hook goes stale
// and UpdateStatus falls through to tmux.GetStatus, the pane is not expired.
func TestIssue2361_OwnHookDisarmsStartupTimeout(t *testing.T) {
	inst := newIssue2361Instance(t, "test-2361-own-hook")

	// Past hookFastPathWindow, so UpdateStatus falls through to GetStatus, but
	// after the pane's startup clock began.
	inst.UpdateHookStatus(&HookStatus{
		Status:    "waiting",
		Event:     "Stop",
		SessionID: "owner-session",
		Cwd:       inst.ProjectPath,
		UpdatedAt: time.Now().Add(-150 * time.Second),
	})

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if got := inst.GetStatusThreadSafe(); got == StatusError {
		t.Fatalf("status = %q: startup timeout fired on a pane whose agent reported a hook", got)
	}
}

// A foreign ephemeral's hook (a `claude -p` child that inherited our
// AGENTDECK_INSTANCE_ID, cwd outside the instance) is rejected and must not
// disarm the watchdog for a pane that never became interactive.
func TestIssue2361_ForeignHookDoesNotDisarmStartupTimeout(t *testing.T) {
	inst := newIssue2361Instance(t, "test-2361-foreign-hook")

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		Event:     "UserPromptSubmit",
		SessionID: "foreign-ephemeral",
		Cwd:       t.TempDir(),
		UpdatedAt: time.Now().Add(-150 * time.Second),
	})

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if got := inst.GetStatusThreadSafe(); got != StatusError {
		t.Fatalf("status = %q, want %q: a rejected foreign hook disarmed the startup timeout", got, StatusError)
	}
}

// writeIssue2361HookFile writes a hook status file on disk in the layout
// readHookStatusFile expects, so an instance's cold-load branch in
// updateStatus picks it up the same way it would a real SessionStart hook
// written before the daemon's StatusFileWatcher ever ran.
func writeIssue2361HookFile(t *testing.T, instanceID, status, event, sessionID, cwd string, ts time.Time) {
	t.Helper()
	path := filepath.Join(GetHooksDir(), instanceID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir hooks dir: %v", err)
	}
	record := map[string]any{
		"status":     status,
		"session_id": sessionID,
		"event":      event,
		"cwd":        cwd,
		"ts":         ts.Unix(),
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal hook record: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write hook file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
}

// TestIssue2361_ColdLoadFirstStillDisarms covers the race the review flagged:
// updateStatus's COLD LOAD branch reads the SessionStart hook file straight
// off disk and stamps i.hookLastUpdate from it, before the watcher ever calls
// UpdateHookStatus with that same event. The old code gated the disarm on
// isNewEvent (status.UpdatedAt.After(i.hookLastUpdate)), which is false once
// cold-load has already recorded that exact timestamp — so the watcher's feed
// of the same hook never cleared the startup clock, and a later fallthrough
// to GetStatus killed a pane whose agent had, in fact, reported in.
func TestIssue2361_ColdLoadFirstStillDisarms(t *testing.T) {
	inst := newIssue2361Instance(t, "test-2361-cold-load")

	// Status "waiting" (rather than "running") on the cold-load sample keeps
	// i.Status off StatusRunning here, so the tmux-flip debounce below (which
	// only holds a flip AWAY from a StatusRunning sample) cannot mask what
	// the fallthrough GetStatus call actually decides.
	//
	// Truncated to whole seconds to match what the cold-load branch actually
	// records: readHookStatusFile rebuilds UpdatedAt from the hook file's "ts"
	// (unix seconds) via time.Unix(ts, 0), which drops any sub-second
	// component. Feeding UpdateHookStatus a nanosecond-precision "same"
	// timestamp below would make it compare as strictly after the cold-load
	// value and mask the exact race this test exists to catch.
	hookTS := time.Unix(time.Now().Add(-100*time.Second).Unix(), 0)
	writeIssue2361HookFile(t, inst.ID, "waiting", "SessionStart", "owner-session", inst.ProjectPath, hookTS)

	// Cold load: hookStatus is empty, so this reads the file above straight off
	// disk and stamps i.hookLastUpdate = hookTS. 100s is within the 2-minute
	// hook fast-path window, so this call stays on the hook fast path and
	// never reaches GetStatus.
	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus (cold load): %v", err)
	}

	// The watcher's feed of the identical hook record arrives after cold load
	// already recorded it — same status, same UpdatedAt.
	inst.UpdateHookStatus(&HookStatus{
		Status:    "waiting",
		Event:     "SessionStart",
		SessionID: "owner-session",
		Cwd:       inst.ProjectPath,
		UpdatedAt: hookTS,
	})

	// Make the hook stale past the 2-minute fast-path window so the next
	// UpdateStatus falls through to tmux.GetStatus, where a still-armed
	// startup clock gets expired.
	inst.mu.Lock()
	inst.hookLastUpdate = time.Now().Add(-150 * time.Second)
	inst.mu.Unlock()

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus (post cold-load fallthrough): %v", err)
	}
	if got := inst.GetStatusThreadSafe(); got == StatusError {
		t.Fatalf("status = %q: startup timeout fired despite a real hook recorded via cold load first (#2361 cold-load race)", got)
	}
}

// TestIssue2361_NoConversationDataHookDoesNotDisarm mirrors the foreign-hook
// test above, but for the sibling rejection branch: an established instance
// (ClaudeSessionID already bound) sees a hook reporting a DIFFERENT session id
// whose cwd is inside the instance's own path (so it is not a foreign-cwd
// rejection) but which has no conversation data on disk. That candidate hits
// candidate_has_no_conversation_data, calls restoreHook, and must not disarm
// the startup watchdog.
func TestIssue2361_NoConversationDataHookDoesNotDisarm(t *testing.T) {
	inst := newIssue2361Instance(t, "test-2361-no-convo-data")

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		Event:     "UserPromptSubmit",
		SessionID: "other-session-no-data",
		Cwd:       inst.ProjectPath,
		UpdatedAt: time.Now().Add(-150 * time.Second),
	})

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if got := inst.GetStatusThreadSafe(); got != StatusError {
		t.Fatalf("status = %q, want %q: a hook with no conversation data disarmed the startup timeout", got, StatusError)
	}
}

// TestIssue2361_SpawnSeedDoesNotDisarm covers item #2: the synthetic
// agentdeck_spawn_seed event agent-deck itself writes when handing a pane to
// Hermes is not evidence that an agent became interactive, and must not
// disarm the startup watchdog.
func TestIssue2361_SpawnSeedDoesNotDisarm(t *testing.T) {
	inst := newIssue2361Instance(t, "test-2361-spawn-seed")

	inst.UpdateHookStatus(&HookStatus{
		Status:    "waiting",
		Event:     "agentdeck_spawn_seed",
		SessionID: "owner-session",
		Cwd:       inst.ProjectPath,
		UpdatedAt: time.Now().Add(-150 * time.Second),
	})

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if got := inst.GetStatusThreadSafe(); got != StatusError {
		t.Fatalf("status = %q, want %q: an agentdeck_spawn_seed hook disarmed the startup timeout", got, StatusError)
	}
}
