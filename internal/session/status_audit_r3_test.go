package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
// subagent (real capture: "── ⠴ Working ──" banner over the running
// subagent step) must.
func TestAuditR3_PiDelegateTaskProseVsLiveSubagent(t *testing.T) {
	cases := []struct {
		frame     string
		persisted Status
		want      Status
		wantTmux  string
	}{
		{"pi-synth-idle-delegate-task-prose", StatusRunning, StatusWaiting, "waiting"},
		{"pi-local-idle-delegate-task-prose_747fe48a", StatusRunning, StatusWaiting, "waiting"},
		{"pi-local-subagent-working_747fe48a", StatusWaiting, StatusRunning, "active"},
	}
	for _, c := range cases {
		t.Run(c.frame, func(t *testing.T) {
			inst, cleanup := startPaneInstance(t, "pi", "r3-"+c.frame, piCorpusFrame(t, c.frame))
			defer cleanup()
			storage, err := NewStorageWithProfile("_test-audit-r3-pi")
			if err != nil {
				t.Fatalf("storage: %v", err)
			}
			defer storage.Close()
			fresh := persistAndReload(t, storage, inst, c.persisted)
			if status, _ := cliPass(t, fresh); status != c.want {
				t.Fatalf("fresh process = %q, want %q", status, c.want)
			}

			// The tmux layer alone, on another freshly loaded instance. The
			// first reads can fall in the startup window ("starting").
			tsess := persistAndReload(t, storage, inst, c.persisted).tmuxSession
			got, err := tsess.GetStatus()
			for i := 0; i < 20 && err == nil && got == "starting"; i++ {
				time.Sleep(250 * time.Millisecond)
				got, err = tsess.GetStatus()
			}
			if err != nil || got != c.wantTmux {
				t.Fatalf("tmux GetStatus = %q (err %v), want %q", got, err, c.wantTmux)
			}
		})
	}
}
