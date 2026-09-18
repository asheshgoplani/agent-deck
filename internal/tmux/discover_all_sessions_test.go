package tmux

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestDiscoverAllTmuxSessions_WorkDirWithColonSurvives is a regression guard
// for a bug introduced while adding Created/Command reporting (finding 1,
// 2026-09-18 live UI audit's doctor/health diagnostic): the `-F` format grew
// from two fields to four, and an earlier version put pane_current_path
// second, so SplitN(line, ":", 4) truncated any working directory containing
// a colon at the first embedded colon. The format now puts pane_current_path
// LAST — the only field that can legitimately contain one — so SplitN's
// final part keeps it whole regardless of how many colons it holds. This
// also matters beyond the new diagnostic: DiscoverExistingTmuxSessions
// (discovery.go) uses WorkDir for real session import.
func TestDiscoverAllTmuxSessions_WorkDirWithColonSurvives(t *testing.T) {
	skipIfNoTmuxBinary(t)

	name := "agentdeck_colon-workdir-test_" + time.Now().Format("150405")
	workDir := t.TempDir() + "/weird:path"
	if err := exec.Command("mkdir", "-p", workDir).Run(); err != nil {
		t.Fatalf("mkdir %q: %v", workDir, err)
	}
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", name, "-c", workDir, "sh", "-c", "sleep 3600").CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })

	sessions, err := DiscoverAllTmuxSessions()
	if err != nil {
		t.Fatalf("DiscoverAllTmuxSessions: %v", err)
	}
	var found *Session
	for _, s := range sessions {
		if s.Name == name {
			found = s
			break
		}
	}
	if found == nil {
		t.Fatalf("session %q not found in %+v", name, sessions)
	}
	// tmux reports pane_current_path through the OS's resolved path (macOS:
	// /var is a symlink to /private/var), so compare the suffix rather than
	// exact equality — what matters here is that the colon survived intact.
	if !strings.HasSuffix(found.WorkDir, "/weird:path") {
		t.Fatalf("WorkDir = %q, want it to end with %q (colon must not truncate the path)", found.WorkDir, "/weird:path")
	}
	if found.Created.IsZero() {
		t.Fatalf("Created = zero, want a real creation time")
	}
}
