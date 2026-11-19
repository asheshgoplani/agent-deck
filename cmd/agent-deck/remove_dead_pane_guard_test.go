package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// With remain-on-exit on, tmux keeps a session whose pane process has exited:
// has-session succeeds, but the pane is dead (#{pane_dead} = 1) and nothing
// is left to kill. The remove guard used to read "tmux session exists" as
// "running" and demand --force for such a row when its stored status still
// said running. A session whose panes are all dead is not running.

// removeDeadPaneSocket returns a -L socket name private to this test and
// registers a kill-server for that socket only. The live server is never
// touched.
func removeDeadPaneSocket(t *testing.T, tag string) string {
	t.Helper()
	socket := fmt.Sprintf("ad-rmdead-%s-%d", tag, time.Now().UnixNano()%1_000_000)
	kill := func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() }
	t.Cleanup(kill)
	return socket
}

func tmuxOnSocket(t *testing.T, socket string, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux -L %s %s: %v: %s", socket, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// startDeadPaneSession adds and starts a session on its own socket, turns
// remain-on-exit on, then ends the pane's process so tmux keeps a dead pane.
// It returns the session id with the stored status set to running.
func startDeadPaneSession(t *testing.T, home, socket, title string) string {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "add", t.TempDir(), "--title", title, "-c", "bash",
		"--no-parent", "--tmux-socket", socket, "--json")
	if code != 0 {
		t.Fatalf("add: exit %d\n%s\n%s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add result: %s %v", stdout, err)
	}
	if stdout, stderr, code := runAgentDeck(t, home, "session", "start", added.ID); code != 0 {
		t.Fatalf("session start: exit %d\n%s\n%s", code, stdout, stderr)
	}
	names := strings.Fields(tmuxOnSocket(t, socket, "list-sessions", "-F", "#{session_name}"))
	if len(names) != 1 {
		t.Fatalf("want one tmux session on %s, got %q", socket, names)
	}
	name := names[0]
	tmuxOnSocket(t, socket, "set-option", "-w", "-t", name, "remain-on-exit", "on")
	pid, err := strconv.Atoi(tmuxOnSocket(t, socket, "display-message", "-p", "-t", name+":0.0", "#{pane_pid}"))
	if err != nil || pid <= 1 {
		t.Fatalf("pane pid: %d %v", pid, err)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	deadline := time.Now().Add(10 * time.Second)
	for tmuxOnSocket(t, socket, "list-panes", "-s", "-t", name, "-F", "#{pane_dead}") != "1" {
		if time.Now().After(deadline) {
			t.Fatalf("pane of %s did not die", name)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The retained session is still there: this is the remain-on-exit state.
	tmuxOnSocket(t, socket, "has-session", "-t", name)
	forceSetStatus(t, home, added.ID, session.StatusRunning)
	return added.ID
}

func TestRemove_RemainOnExitDeadPaneNeedsNoForce(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	testutil.RequireTmux(t)
	for i, rc := range removeCommands {
		t.Run(rc.name, func(t *testing.T) {
			home := t.TempDir()
			socket := removeDeadPaneSocket(t, strconv.Itoa(i))
			id := startDeadPaneSession(t, home, socket, "dead-pane-rm")
			stdout, stderr, code := runAgentDeck(t, home, rc.args(id, "--json")...)
			if code != 0 {
				t.Fatalf("%s of a running row whose pane is dead (remain-on-exit): exit %d, want 0\n%s\n%s",
					rc.name, code, stdout, stderr)
			}
			if removeGuardListed(t, home, id) {
				t.Errorf("%s left the dead-pane row listed", rc.name)
			}
		})
	}
}

// A session with remain-on-exit on whose pane is still alive is still
// running: the guard keeps refusing it without --force.
func TestRemove_RemainOnExitLivePaneStillNeedsForce(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	testutil.RequireTmux(t)
	home := t.TempDir()
	socket := removeDeadPaneSocket(t, "live")
	stdout, stderr, code := runAgentDeck(t, home, "add", filepath.Join(t.TempDir()), "--title", "live-pane-rm",
		"-c", "bash", "--no-parent", "--tmux-socket", socket, "--json")
	if code != 0 {
		t.Fatalf("add: exit %d\n%s\n%s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil || added.ID == "" {
		t.Fatalf("add result: %s %v", stdout, err)
	}
	if stdout, stderr, code := runAgentDeck(t, home, "session", "start", added.ID); code != 0 {
		t.Fatalf("session start: exit %d\n%s\n%s", code, stdout, stderr)
	}
	name := tmuxOnSocket(t, socket, "list-sessions", "-F", "#{session_name}")
	tmuxOnSocket(t, socket, "set-option", "-w", "-t", name, "remain-on-exit", "on")
	forceSetStatus(t, home, added.ID, session.StatusRunning)
	if _, _, code := runAgentDeck(t, home, "session", "remove", added.ID, "--json"); code != 1 {
		t.Fatalf("remove of a live remain-on-exit pane without --force: exit %d, want 1", code)
	}
	if !removeGuardListed(t, home, added.ID) {
		t.Fatal("a live session was removed without --force")
	}
}
