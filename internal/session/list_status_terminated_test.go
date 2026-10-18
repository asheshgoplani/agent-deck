package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A cached absent error row must keep the terminated-pane classification that
// updateStatus gives it: the tracked command's exit receipt (#2597) and the
// #2091 hook-evidence correction.
func TestCLIStatusCandidatesClassifiesAbsentErrorRows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	old := listStatusSessionNames
	t.Cleanup(func() { listStatusSessionNames = old })
	listStatusSessionNames = func(string) (map[string]struct{}, error) {
		return map[string]struct{}{}, nil
	}
	absent := func(inst *Instance) *Instance {
		inst.tmuxSession = tmux.ReconnectSessionLazy("absent-"+inst.ID, inst.ID, "", "", "inactive")
		inst.tmuxSession.SocketName = "list-terminated-absent"
		return inst
	}

	tracked := absent(&Instance{ID: "tracked-exit-3", Tool: "shell", Command: "exit 3",
		TrackCommandExit: true, Status: StatusError})
	receipt := commandExitReceiptPath(tracked.ID)
	if err := os.MkdirAll(filepath.Dir(receipt), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receipt, []byte("3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanHook := absent(&Instance{ID: "hook-waiting", Tool: "claude", Status: StatusError,
		hookStatus: "waiting"})
	noHook := absent(&Instance{ID: "hook-none", Tool: "claude", Status: StatusError})

	refresh, cached := CLIStatusCandidates([]*Instance{tracked, cleanHook, noHook})
	if len(refresh) != 0 || !cached[tracked] || !cached[cleanHook] || !cached[noHook] {
		t.Fatalf("absent error rows must stay on the shared inventory path: refresh=%d", len(refresh))
	}

	if tracked.Status != StatusError || tracked.HistoricalSubstate() != SubstateProcessExited {
		t.Fatalf("tracked row status=%s substate=%s, want error/process-exited",
			tracked.Status, tracked.HistoricalSubstate())
	}
	if code := tracked.ExitCode(); code == nil || *code != 3 {
		t.Fatalf("tracked row exit_code=%v, want 3", code)
	}
	if cleanHook.Status != StatusStopped {
		t.Fatalf("hook tool with a waiting hook: status=%s, want stopped (#2091)", cleanHook.Status)
	}
	if noHook.Status != StatusError || noHook.HistoricalSubstate() != SubstateUnknownExit {
		t.Fatalf("hook tool without hook evidence: status=%s substate=%s, want error/unknown-exit",
			noHook.Status, noHook.HistoricalSubstate())
	}
}
