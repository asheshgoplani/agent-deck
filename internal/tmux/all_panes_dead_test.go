package tmux

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestAllPanesDeadFromList(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"1\n", true},
		{"1\n1\n1\n", true},
		{"0\n", false},
		{"1\n0\n", false},
		{"0\n1\n", false},
		{"", false},
		{"\n\n", false},
		{"garbage\n", false},
	}
	for _, tc := range cases {
		if got := allPanesDeadFromList(tc.raw); got != tc.want {
			t.Errorf("allPanesDeadFromList(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// allPanesDeadSocket returns a -L socket private to this test and kills that
// server (and only that one) when the test ends.
func allPanesDeadSocket(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	socket := fmt.Sprintf("ad-alldead-%d", time.Now().UnixNano()%1_000_000)
	kill := func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() }
	kill()
	t.Cleanup(kill)
	return socket
}

func allPanesDeadTmux(t *testing.T, socket string, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", append([]string{"-L", socket}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func waitPaneDeadList(t *testing.T, socket, name, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := strings.Join(strings.Fields(allPanesDeadTmux(t, socket, "list-panes", "-s", "-t", name, "-F", "#{pane_dead}")), ",")
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane_dead list of %s = %q, want %q", name, got, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// With remain-on-exit on, tmux keeps a session whose process has exited:
// Exists() is true, but every pane is dead, so the session is not running.
// A live pane, in the primary pane or in any other, keeps it running.
func TestAllPanesDead_RemainOnExitIsolatedSocket(t *testing.T) {
	socket := allPanesDeadSocket(t)
	const name = "alldead"
	allPanesDeadTmux(t, socket, "new-session", "-d", "-s", name, "sleep 6000")
	// Global window option on this private server so later windows keep
	// their dead panes too.
	allPanesDeadTmux(t, socket, "set-option", "-gw", "remain-on-exit", "on")
	allPanesDeadTmux(t, socket, "set-option", "-w", "-t", name, "remain-on-exit", "on")
	s := &Session{Name: name, SocketName: socket}

	if !s.Exists() {
		t.Fatal("Exists() = false for a live session")
	}
	if s.AllPanesDead() {
		t.Fatal("AllPanesDead() = true for a live pane")
	}

	// A second window whose process exits at once: one dead pane, one live.
	allPanesDeadTmux(t, socket, "new-window", "-d", "-t", name, "true")
	waitPaneDeadList(t, socket, name, "0,1")
	if s.AllPanesDead() {
		t.Fatal("AllPanesDead() = true while pane 0.0 is still alive")
	}

	// End the primary pane's process: every pane is now dead and tmux keeps
	// the session.
	allPanesDeadTmux(t, socket, "respawn-pane", "-k", "-t", name+":0.0", "true")
	waitPaneDeadList(t, socket, name, "1,1")
	if !s.Exists() {
		t.Fatal("remain-on-exit should keep the session; Exists() = false")
	}
	if !s.AllPanesDead() {
		t.Fatal("AllPanesDead() = false for a session whose panes are all dead")
	}

	// A live pane in a later window keeps the session running even though
	// the primary pane is dead (IsPaneDead alone would say dead).
	allPanesDeadTmux(t, socket, "new-window", "-d", "-t", name, "sleep 6000")
	waitPaneDeadList(t, socket, name, "1,1,0")
	if s.AllPanesDead() {
		t.Fatal("AllPanesDead() = true while a later window has a live pane")
	}
}

// A session that is not there is not "all dead": callers must not read a
// failed probe as proof that nothing runs.
func TestAllPanesDead_MissingSessionIsNotDead(t *testing.T) {
	socket := allPanesDeadSocket(t)
	s := &Session{Name: "no-such-session", SocketName: socket}
	if s.AllPanesDead() {
		t.Fatal("AllPanesDead() = true for a session that does not exist")
	}
}

// A plain "-t NAME" on list-panes resolves NAME as a window of the current
// session before it tries a session of that name. Here the most recent
// session ("decoy") has a window named like the target session, and every
// decoy pane is dead while the target's pane is live. The probes must look
// at the target session, not at the decoy window's session.
func TestAllPanesDead_WindowNamedLikeSessionDoesNotCollide(t *testing.T) {
	socket := allPanesDeadSocket(t)
	const target = "collide-target"
	const decoy = "collide-decoy"
	allPanesDeadTmux(t, socket, "new-session", "-d", "-s", target, "sleep 6000")
	allPanesDeadTmux(t, socket, "set-option", "-gw", "remain-on-exit", "on")
	// Created last, so it is the server's current session for "-t" lookups.
	allPanesDeadTmux(t, socket, "new-session", "-d", "-s", decoy, "true")
	allPanesDeadTmux(t, socket, "new-window", "-d", "-t", decoy+":", "-n", target, "true")
	waitPaneDeadList(t, socket, "="+decoy+":", "1,1")
	waitPaneDeadList(t, socket, "="+target+":", "0")
	if got := allPanesDeadTmux(t, socket, "list-panes", "-s", "-t", target, "-F", "#{session_name}"); !strings.Contains(got, decoy) {
		t.Logf("this tmux resolves plain -t %s to the session itself (%q); collision not reproducible here", target, got)
	}

	s := &Session{Name: target, SocketName: socket}
	if s.AllPanesDead() {
		t.Fatalf("AllPanesDead() = true for live session %q: probe read the panes of window %q in session %q", target, target, decoy)
	}

	wantPID := allPanesDeadTmux(t, socket, "list-panes", "-s", "-t", "="+target+":", "-F", "#{pane_pid}")
	pids, err := s.AllPanePIDs()
	if err != nil {
		t.Fatalf("AllPanePIDs: %v", err)
	}
	if len(pids) != 1 || fmt.Sprint(pids[0]) != wantPID {
		t.Fatalf("AllPanePIDs() = %v, want [%s] (the panes of session %q)", pids, wantPID, target)
	}
}
