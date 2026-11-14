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
	workDir := t.TempDir() + "/weird:path"
	name := startColonWorkDirSession(t, "agentdeck_colon-workdir-test_", workDir, "-c", workDir, "sh", "-c", "sleep 3600")
	assertDiscoveredWorkDirKeepsColon(t, name)
}

// TestDiscoverAllTmuxSessions_ColonWorkDirReachedLate covers a pane that
// reaches its working directory after new-session returns. tmux 3.6 does
// that on hosts with a systemd user manager (Ubuntu 26.04 CI runners): the
// pane child moves itself into a systemd cgroup BEFORE its chdir (3.4 did the
// move in the server), so for a moment pane_current_path is the server's own
// cwd. The pane here cds late on purpose, to make that window reproducible.
func TestDiscoverAllTmuxSessions_ColonWorkDirReachedLate(t *testing.T) {
	skipIfNoTmuxBinary(t)
	base := t.TempDir()
	workDir := base + "/weird:path"
	name := startColonWorkDirSession(t, "agentdeck_colon-late-cd_", workDir,
		"-c", base, "sh", "-c", "sleep 0.3; cd 'weird:path' && exec sleep 3600")
	assertDiscoveredWorkDirKeepsColon(t, name)
}

// startColonWorkDirSession creates workDir and a detached tmux session with
// the given new-session arguments, waits until tmux reports the pane inside
// workDir, and returns the session name. The wait reads pane_current_path on
// its own (display-message, one field), so it shares nothing with the
// list-sessions line parsing the tests check.
func startColonWorkDirSession(t *testing.T, prefix, workDir string, args ...string) string {
	t.Helper()
	name := prefix + time.Now().Format("150405")
	if err := exec.Command("mkdir", "-p", workDir).Run(); err != nil {
		t.Fatalf("mkdir %q: %v", workDir, err)
	}
	full := append([]string{"new-session", "-d", "-s", name}, args...)
	if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })

	deadline := time.Now().Add(5 * time.Second)
	for {
		out, err := exec.Command("tmux", "display-message", "-p", "-t", name+":", "#{pane_current_path}").Output()
		if err == nil && strings.HasSuffix(strings.TrimSpace(string(out)), "/weird:path") {
			return name
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane never reached %q (pane_current_path %q, err=%v)", workDir, strings.TrimSpace(string(out)), err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func assertDiscoveredWorkDirKeepsColon(t *testing.T, name string) {
	t.Helper()
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
