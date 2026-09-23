package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Status-detection audit, review round 3 (2026-09-23).

func piCorpusFrame(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "tmux", "testdata", "status_corpus", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Review r2 P1, through GetStatus in a fresh process: an idle pi pane whose
// finished answer names delegate_task must not read running, and a live pi
// subagent (Working loader under the subagent tool call) must.
func TestAuditR3_PiDelegateTaskProseVsLiveSubagent(t *testing.T) {
	cases := []struct {
		frame     string
		persisted Status
		want      Status
		wantTmux  string
	}{
		{"pi-synth-idle-delegate-task-prose", StatusRunning, StatusWaiting, "waiting"},
		{"pi-synth-subagent-working", StatusWaiting, StatusRunning, "active"},
	}
	for _, c := range cases {
		t.Run(c.frame, func(t *testing.T) {
			inst, cleanup := startPaneInstance(t, "pi", "r3-"+c.frame, piCorpusFrame(t, c.frame))
			defer cleanup()
			if got, err := inst.tmuxSession.GetStatus(); err != nil || got != c.wantTmux {
				t.Fatalf("tmux GetStatus = %q (err %v), want %q", got, err, c.wantTmux)
			}
			storage, err := NewStorageWithProfile("_test-audit-r3-pi")
			if err != nil {
				t.Fatalf("storage: %v", err)
			}
			defer storage.Close()
			fresh := persistAndReload(t, storage, inst, c.persisted)
			if status, _ := cliPass(t, fresh); status != c.want {
				t.Fatalf("fresh process = %q, want %q", status, c.want)
			}
		})
	}
}
