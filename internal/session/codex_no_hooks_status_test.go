package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// "Remotes always green" (status-light matrix 2026-09-18, Finding 1): a codex
// session whose CODEX_HOME never had `codex-hooks install` run (every session
// made through `remote add` / `add`, since that step is separate) has no hook
// file, so its light comes from the pane alone. Once any status writer (the
// notify daemon's db.WriteStatus, `session start`) persisted "running", every
// fresh process — `list --json`, `session show --json`, the remote-agent
// probe, each daemon pass — loaded the row as running, saw the idle prompt,
// and held it at running "for one confirming sample" that a one-pass process
// never takes. The pane showed `› Ask Codex to do anything` and the light
// stayed green forever.
//
// Rules under test:
//   - a persisted "running" is not a live observation: a fresh process with
//     no hook evidence reports what the pane shows (waiting), first pass;
//   - the debounce still holds one tick when THIS process saw running itself;
//   - a fresh completion hook (#2190) still pins waiting, a stale one (#2189)
//     yields to the pane, and a busy pane is still running.

const codexIdleFrame = "› Ask Codex to do anything\n"
const codexBusyFrame = "• Working (3s • esc to interrupt)\n"

// startCodexPaneInstance starts a real tmux pane rendering frame for a codex
// instance with no hook file, past UpdateStatus's tmux grace window.
func startCodexPaneInstance(t *testing.T, name, frame string) (*Instance, func()) {
	t.Helper()
	skipIfNoTmuxBinary(t)
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("CODEX_HOME", filepath.Join(tmpHome, ".codex"))

	panePath := filepath.Join(tmpHome, "pane.txt")
	if err := os.WriteFile(panePath, []byte(frame), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := NewInstanceWithTool("codex-nohooks-"+name, tmpHome, "codex")
	inst.Command = "codex"
	inst.tmuxSession.Command = "codex"
	if err := inst.tmuxSession.Start(fmt.Sprintf("sh -c 'cat %q; exec sleep 3600'", panePath)); err != nil {
		t.Fatalf("tmux start: %v", err)
	}
	cleanup := func() { _ = inst.tmuxSession.Kill() }
	time.Sleep(2 * time.Second)
	return inst, cleanup
}

// persistAndReload saves inst with the given status (what the notify
// daemon's db.WriteStatus leaves in the row) and returns a fresh object
// loaded from storage: what every one-pass CLI process starts from.
func persistAndReload(t *testing.T, storage *Storage, inst *Instance, status Status) *Instance {
	t.Helper()
	inst.Status = status
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}
	instances, _, err := storage.LoadWithGroups()
	if err != nil || len(instances) != 1 {
		t.Fatalf("load: %v (%d instances)", err, len(instances))
	}
	return instances[0]
}

func TestCodexNoHooks_PersistedRunningDoesNotStickInFreshProcess(t *testing.T) {
	inst, cleanup := startCodexPaneInstance(t, "stuck", codexIdleFrame)
	defer cleanup()
	storage, err := NewStorageWithProfile("_test-codex-nohooks")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()

	// Three consecutive fresh processes, each loading the row the daemon
	// left at "running" while the pane shows the idle prompt. Before the fix
	// every one of them reported running.
	for pass := 1; pass <= 3; pass++ {
		fresh := persistAndReload(t, storage, inst, StatusRunning)
		if status, _ := cliPass(t, fresh); status != StatusWaiting {
			t.Fatalf("fresh process %d = %q, want waiting: the pane shows the idle prompt and no hook says otherwise", pass, status)
		}
	}
}

func TestCodexNoHooks_BusyPaneStillRunsInFreshProcess(t *testing.T) {
	inst, cleanup := startCodexPaneInstance(t, "busy", codexBusyFrame)
	defer cleanup()
	storage, err := NewStorageWithProfile("_test-codex-nohooks-busy")
	if err != nil {
		t.Fatalf("storage: %v", err)
	}
	defer storage.Close()

	fresh := persistAndReload(t, storage, inst, StatusWaiting)
	if status, _ := cliPass(t, fresh); status != StatusRunning {
		t.Fatalf("fresh process = %q, want running from the busy pane", status)
	}
}

// The one-tick hold is kept where it can be confirmed: a process that saw
// running itself holds the first idle sample and flips on the second.
func TestCodexNoHooks_InProcessHoldStillConfirmsOnSecondSample(t *testing.T) {
	inst, cleanup := startCodexPaneInstance(t, "hold", codexIdleFrame)
	defer cleanup()

	inst.Status = StatusWaiting
	inst.mu.Lock()
	inst.statusSampledLive = true // this process settled a verdict already
	inst.Status = StatusRunning   // ...and it was running
	inst.mu.Unlock()
	if status, _ := cliPass(t, inst); status != StatusRunning {
		t.Fatalf("first idle sample after a live running = %q, want running (one-tick hold)", status)
	}
	if status, _ := cliPass(t, inst); status != StatusWaiting {
		t.Fatalf("second idle sample = %q, want waiting (hold confirmed)", status)
	}
}

// Table over the #2189/#2190 hook fixtures and the hook-less case, all in a
// fresh process that loaded the row as "running".
func TestCodexFreshProcess_HookAndPaneTable(t *testing.T) {
	cases := []struct {
		name    string
		frame   string
		hook    string // "" = no hook file
		hookAge int    // seconds
		want    Status
	}{
		{"no hook, idle prompt (Finding 1)", codexIdleFrame, "", 0, StatusWaiting},
		{"no hook, busy pane", codexBusyFrame, "", 0, StatusRunning},
		{"fresh completion hook pins waiting over a busy pane (#2190)", codexBusyFrame, "waiting", 2, StatusWaiting},
		{"stale completion hook yields to the busy pane (#2189)", codexBusyFrame, "waiting", 6, StatusRunning},
		{"stale completion hook, idle prompt", codexIdleFrame, "waiting", 6, StatusWaiting},
		{"fresh running hook pins running over an idle prompt", codexIdleFrame, "running", 5, StatusRunning},
		{"stale running hook yields to the idle prompt", codexIdleFrame, "running", 30, StatusWaiting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst, cleanup := startCodexPaneInstance(t, "table", tc.frame)
			defer cleanup()
			if tc.hook != "" {
				writeCodexHookFile(t, inst.ID, tc.hook, tc.hookAge)
			}
			storage, err := NewStorageWithProfile("_test-codex-table")
			if err != nil {
				t.Fatalf("storage: %v", err)
			}
			defer storage.Close()
			fresh := persistAndReload(t, storage, inst, StatusRunning)
			if status, _ := cliPass(t, fresh); status != tc.want {
				t.Fatalf("status = %q, want %q", status, tc.want)
			}
		})
	}
}

// writeCodexHookFile writes a codex notify-shaped hook file (no
// session_id: a legacy notify payload carries none, and UpdateHookStatus's
// ownership check does not apply to an instance without a bound session).
func writeCodexHookFile(t *testing.T, instanceID, status string, tsSecondsAgo int) {
	t.Helper()
	hooksDir := GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	event := "agent-turn-complete"
	if status == "running" {
		event = "agent-turn-start"
	}
	ts := time.Now().Add(-time.Duration(tsSecondsAgo) * time.Second).Unix()
	body := fmt.Sprintf(`{"status":%q,"event":%q,"ts":%d}`, status, event, ts)
	if err := os.WriteFile(filepath.Join(hooksDir, instanceID+".json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write hook: %v", err)
	}
}
