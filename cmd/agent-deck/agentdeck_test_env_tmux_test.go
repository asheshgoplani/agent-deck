package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunAgentDeckSessionLandsOnIsolatedTmuxSocket: a session that a
// runAgentDeck subprocess starts, with no TMUX_TMPDIR of its own in extraEnv,
// runs on the tmux server under the package's isolated TMUX_TMPDIR (set by
// TestMain's testutil.IsolateTmuxSocket), never on the user's default server
// under /tmp/tmux-UID. agentDeckTestEnv used to strip every TMUX* variable,
// TMUX_TMPDIR included, which sent such sessions to the real server.
func TestRunAgentDeckSessionLandsOnIsolatedTmuxSocket(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	isolated := os.Getenv("TMUX_TMPDIR")
	if isolated == "" {
		t.Fatal("TestMain left TMUX_TMPDIR unset")
	}
	env := agentDeckTestEnv(t.TempDir(), nil)
	var gotTmpdir string
	for _, kv := range env {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			t.Errorf("subprocess env keeps %q; TMUX/TMUX_PANE must be stripped", kv)
		}
		if v, ok := strings.CutPrefix(kv, "TMUX_TMPDIR="); ok {
			gotTmpdir = v
		}
	}
	if gotTmpdir != isolated {
		t.Errorf("subprocess TMUX_TMPDIR = %q, want the isolated %q", gotTmpdir, isolated)
	}

	home := t.TempDir()
	project := filepath.Join(home, "sh")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "isolated-socket", "-c", "shell", "--no-parent", "--json", project)
	if code != 0 {
		t.Fatalf("add: %d %s %s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add --json: %v %q", err, stdout)
	}
	if stdout, stderr, code := runAgentDeck(t, home, "session", "start", added.ID, "--json"); code != 0 {
		t.Fatalf("start: %d %s %s", code, stdout, stderr)
	}
	t.Cleanup(func() { _, _, _ = runAgentDeck(t, home, "session", "stop", added.ID) })

	stdout, stderr, code = runAgentDeck(t, home, "session", "show", added.ID, "--json")
	if code != 0 {
		t.Fatalf("show: %d %s %s", code, stdout, stderr)
	}
	var shown struct {
		TmuxSession string `json:"tmux_session"`
	}
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil || shown.TmuxSession == "" {
		t.Fatalf("show --json has no tmux_session: %v %q", err, stdout)
	}

	// Ask the isolated server by its socket path; -S ignores TMUX/TMUX_TMPDIR.
	socket := filepath.Join(isolated, fmt.Sprintf("tmux-%d", os.Getuid()), "default")
	if out, err := exec.Command("tmux", "-S", socket, "has-session", "-t", "="+shown.TmuxSession).CombinedOutput(); err != nil {
		t.Fatalf("session %q is not on the isolated server %s: %v %s", shown.TmuxSession, socket, err, out)
	}
}
