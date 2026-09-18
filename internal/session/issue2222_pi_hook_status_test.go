// issue2222_pi_hook_status_test.go covers pi (pi-coding-agent) hook-driven
// status.
//
// Before #2222 a pi session's status came purely from pane-regex heuristics:
// the launcher already injected AGENTDECK_INSTANCE_ID and `hook-handler`
// already accepted any payload, but the two consumption allowlists (the
// UpdateStatus hook fast path and the CLI cold load) named claude/codex/
// gemini/hermes/cursor and not pi, so every event pi emitted was written to
// disk and then ignored.
//
// Covered here:
//   - HookStatusTool, the single registry that replaced the inline allowlists.
//   - The UpdateStatus fast path for a live pi session, per hook status.
//   - The CLI cold load (RefreshInstancesForCLIStatus) for a pi instance.
//   - Fallback precedence: a STALE pi hook sample must not drive status, so
//     content detection stays in charge exactly as it did before.
package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHookStatusTool_Registry(t *testing.T) {
	tests := []struct {
		tool string
		want bool
	}{
		{"claude", true},
		{"codex", true},
		{"gemini", true},
		{"hermes", true},
		{"cursor", true},
		{"pi", true},
		{"PI", true},
		{" pi ", true},
		{"shell", false},
		{"opencode", false},
		{"deepseek", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			if got := HookStatusTool(tt.tool); got != tt.want {
				t.Errorf("HookStatusTool(%q) = %v, want %v", tt.tool, got, tt.want)
			}
		})
	}
}

// startPiPaneInstance returns a started instance that reports Tool "pi" over a
// live, harmless pane. It starts as a "shell" instance so the launcher does not
// wrap the pane command in pi's own flags (there is no pi binary in CI) and is
// relabelled afterwards: what is under test is the tool gate in UpdateStatus,
// not how a pi command line is assembled (that is command_override_test.go's).
func startPiPaneInstance(t *testing.T, name string) *Instance {
	t.Helper()
	inst := NewInstanceWithTool(name, "/tmp", "shell")
	inst.Command = "sleep 30"
	if err := inst.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	t.Cleanup(func() { _ = inst.Kill() })
	// Past the instance-level grace period.
	time.Sleep(2 * time.Second)
	inst.Tool = "pi"
	return inst
}

// TestIssue2222_PiHookFastPath drives the real UpdateStatus fast path for a
// live pi session, one row per hook status the pi extension can produce.
func TestIssue2222_PiHookFastPath(t *testing.T) {
	skipIfNoTmuxBinary(t)

	tests := []struct {
		name         string
		hookStatus   string
		hookEvent    string
		acknowledged bool
		want         Status
	}{
		{
			name:       "turn_start running",
			hookStatus: "running",
			hookEvent:  "turn_start",
			want:       StatusRunning,
		},
		{
			name:       "turn_end waiting unacknowledged",
			hookStatus: "waiting",
			hookEvent:  "turn_end",
			want:       StatusWaiting,
		},
		{
			name:         "turn_end waiting acknowledged settles to idle",
			hookStatus:   "waiting",
			hookEvent:    "turn_end",
			acknowledged: true,
			want:         StatusIdle,
		},
		{
			name:       "session_start waiting",
			hookStatus: "waiting",
			hookEvent:  "session_start",
			want:       StatusWaiting,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inst := startPiPaneInstance(t, "test-pi-hook-"+tt.name)

			tmuxSess := inst.GetTmuxSession()
			if tmuxSess == nil {
				t.Fatal("tmux session should not be nil")
			}
			if tt.acknowledged {
				tmuxSess.Acknowledge()
			} else {
				tmuxSess.ResetAcknowledged()
			}

			inst.UpdateHookStatus(&HookStatus{
				Status:    tt.hookStatus,
				Event:     tt.hookEvent,
				UpdatedAt: time.Now(),
			})
			// A "running" hook resets acknowledged, so re-apply the row's
			// intent after the update for the acknowledged rows.
			if tt.acknowledged && tt.hookStatus == "waiting" {
				tmuxSess.Acknowledge()
			}

			if err := inst.UpdateStatus(); err != nil {
				t.Fatalf("UpdateStatus() failed: %v", err)
			}
			if inst.Status != tt.want {
				t.Errorf("pi hook %q/%q gave status %v, want %v",
					tt.hookEvent, tt.hookStatus, inst.Status, tt.want)
			}
		})
	}
}

// TestIssue2222_PiStaleHookFallsBackToContentDetection pins the precedence
// rule: the hook signal wins only while it is FRESH. A stale sample must leave
// the pane (content detection) in charge, which for a `sleep 30` pane is not
// running.
func TestIssue2222_PiStaleHookFallsBackToContentDetection(t *testing.T) {
	skipIfNoTmuxBinary(t)

	inst := startPiPaneInstance(t, "test-pi-stale-hook")

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		Event:     "turn_start",
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	})

	if err := inst.UpdateStatus(); err != nil {
		t.Fatalf("UpdateStatus() failed: %v", err)
	}
	if inst.Status == StatusRunning {
		t.Error("a stale pi hook sample must not drive status; content detection should own it")
	}
}

// TestIssue2222_PiColdLoadFromCLI covers the second allowlist: a fresh CLI
// process runs no status-file watcher, so RefreshInstancesForCLIStatus is the
// only thing that puts a pi session's on-disk hook sample in front of
// UpdateStatus and `session show --json`'s hook_status field.
func TestIssue2222_PiColdLoadFromCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	inst := NewInstanceWithTool("pi-cold-load", "/tmp/test", "pi")

	hooksDir := GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hookData := fmt.Sprintf(`{"status":"running","event":"turn_start","ts":%d}`, time.Now().Unix())
	if err := os.WriteFile(filepath.Join(hooksDir, inst.ID+".json"), []byte(hookData), 0644); err != nil {
		t.Fatal(err)
	}

	RefreshInstancesForCLIStatus([]*Instance{inst})

	status, fresh := inst.GetHookStatus()
	if status != "running" {
		t.Errorf("pi hook status after CLI cold load = %q, want %q", status, "running")
	}
	if !fresh {
		t.Error("a hook sample written just now must read as fresh")
	}
}

// TestIssue2222_PiTerminatedPaneUsesHookEvidence covers the third consumer of
// the registry: when a pane vanishes without an exit code, the last hook sample
// decides whether that was a clean end or a crash. A pi session that finished
// its turn (turn_end → waiting) and was then closed must read as stopped, not
// as an error, exactly as claude/codex/gemini/hermes/cursor already do.
func TestIssue2222_PiTerminatedPaneUsesHookEvidence(t *testing.T) {
	tests := []struct {
		name         string
		tool         string
		hookStatus   string
		wantStatus   Status
		wantSubstate Substate
	}{
		{"pi waiting is a clean end", "pi", "waiting", StatusStopped, SubstateNone},
		{"pi idle is a clean end", "pi", "idle", StatusStopped, SubstateNone},
		{"pi with no hook evidence is an unknown exit", "pi", "", StatusError, SubstateUnknownExit},
		// A shell is not in the registry, so its hook sample is not evidence
		// and it keeps the plain no-exit-code verdict.
		{"shell ignores hook evidence", "shell", "waiting", StatusError, SubstateNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStatus, gotSubstate := classifyTerminatedPane(0, false, tt.tool, tt.hookStatus)
			if gotStatus != tt.wantStatus || gotSubstate != tt.wantSubstate {
				t.Errorf("classifyTerminatedPane(_, false, %q, %q) = %v/%v, want %v/%v",
					tt.tool, tt.hookStatus, gotStatus, gotSubstate, tt.wantStatus, tt.wantSubstate)
			}
		})
	}
}

// TestIssue2222_ShellStillIgnoresHooks guards the registry's negative side: a
// shell session must keep ignoring hook samples entirely.
func TestIssue2222_ShellStillIgnoresHooks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	inst := NewInstanceWithTool("shell-cold-load", "/tmp/test", "shell")

	hooksDir := GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hookData := fmt.Sprintf(`{"status":"running","event":"turn_start","ts":%d}`, time.Now().Unix())
	if err := os.WriteFile(filepath.Join(hooksDir, inst.ID+".json"), []byte(hookData), 0644); err != nil {
		t.Fatal(err)
	}

	RefreshInstancesForCLIStatus([]*Instance{inst})

	if status, _ := inst.GetHookStatus(); status != "" {
		t.Errorf("shell hook status after CLI cold load = %q, want empty", status)
	}
}
