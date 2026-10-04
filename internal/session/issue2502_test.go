package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestIssue2502_FreshStopWithLiveSpinner(t *testing.T) {
	for _, name := range []string{"pair-006", "pair-066", "pair-088"} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "tmux", "testdata", "issue2502", name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			inst, cleanup := startHookLagInstance(t, name, string(b))
			defer cleanup()
			writeHookLagStopFile(t, inst.ID)
			RefreshInstancesForCLIStatus([]*Instance{inst})
			if err := inst.UpdateStatus(); err != nil {
				t.Fatal(err)
			}
			if got := inst.GetStatusThreadSafe(); got != StatusRunning {
				t.Fatalf("status-only fresh Stop = %q, want running", got)
			}
			if status, sub := cliPass(t, inst); status != StatusRunning || sub != SubstateRunning {
				t.Fatalf("fresh Stop + live spinner = %q/%q, want running/running", status, sub)
			}
			inst.tmuxSession.Acknowledge()
			if status, sub := cliPass(t, inst); status != StatusRunning || sub != SubstateRunning {
				t.Fatalf("acknowledged live spinner = %q/%q, want running/running", status, sub)
			}
		})
	}
}

func TestIssue2502_CompletedAfterTimedSpinner(t *testing.T) {
	frame := "❯ previous task\n✻ Simmering… (2s · ↓ 100 tokens)\n⏺ Finished.\n✻ Simmered for 2s · done 8:34 PM\n────────────────────────\n❯\n────────────────────────\nHaiku 4.5\n"
	inst, cleanup := startHookLagInstance(t, "review-completed", frame)
	defer cleanup()
	writeHookLagStopFile(t, inst.ID)
	status, sub := cliPass(t, inst)
	if status != StatusWaiting {
		t.Fatalf("completed turn with fresh Stop = %q/%q, want waiting", status, sub)
	}
}

func TestIssue2502_CachedIdleThenLiveSpinner(t *testing.T) {
	const composer = "\n────────────────────────\n❯\n────────────────────────\nHaiku 4.5\n"
	inst, cleanup := startHookLagInstance(t, "review-transition", "✻ Simmered for 2s · done 8:34 PM"+composer)
	defer cleanup()
	writeHookLagStopFile(t, inst.ID)
	if status, _ := cliPass(t, inst); status != StatusWaiting {
		t.Fatal(status)
	}
	path := filepath.Join(t.TempDir(), "busy.txt")
	if err := os.WriteFile(path, []byte("\033[2J\033[H✻ Simmering…"+composer), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{}
	if inst.tmuxSession.SocketName != "" {
		args = append(args, "-L", inst.tmuxSession.SocketName)
	}
	args = append(args, "respawn-pane", "-k", "-t", inst.tmuxSession.Name, fmt.Sprintf("sh -c 'cat %q; exec sleep 3600'", path))
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("respawn fixture: %v %s", err, out)
	}
	time.Sleep(600 * time.Millisecond)
	status, sub := cliPass(t, inst)
	if status != StatusRunning || sub != SubstateRunning {
		t.Fatalf("fresh busy pane after cached idle = %q/%q, want running/running", status, sub)
	}
	if err := os.WriteFile(path, []byte("\033[2J\033[H✻ Simmered for 2s · done 8:34 PM"+composer), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("respawn completed fixture: %v %s", err, out)
	}
	time.Sleep(600 * time.Millisecond)
	if status, sub := cliPass(t, inst); status != StatusWaiting || sub != SubstateIdleAtEmptyPrompt {
		t.Fatalf("spinner removed = %q/%q, want waiting/idle-at-empty-prompt", status, sub)
	}
	inst.tmuxSession.Acknowledge()
	if status, _ := cliPass(t, inst); status != StatusIdle {
		t.Fatalf("completed and acknowledged = %q, want idle", status)
	}
}

func TestIssue2502_BlockingHookBeforeMenuPaint(t *testing.T) {
	const frame = "✻ Simmering…\n────────────────────────\n❯\n────────────────────────\n"
	inst, cleanup := startHookLagInstance(t, "permission-before-paint", frame)
	defer cleanup()
	writeHookWaitingEvent(t, inst.ID, "PermissionRequest")
	if status, sub := cliPass(t, inst); status != StatusWaiting || sub != SubstateNone {
		t.Fatalf("blocking hook + previous spinner = %q/%q, want waiting/unknown", status, sub)
	}
	if got := inst.CachedSubstate(); got != SubstateNone {
		t.Fatalf("cached blocking substate = %q, want unknown", got)
	}
}
