package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestPrimaryWindow_LiveAuxWindowFocused is an integration test (real tmux, on
// an isolated -L socket): when the operator has opened and focused an
// auxiliary window next to the managed agent, the primary-window helpers must
// still deliver keys to, and capture from, the first window, while the
// existing session-name target keeps following the active (auxiliary) window.
func TestPrimaryWindow_LiveAuxWindowFocused(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	socket := fmt.Sprintf("adeck-primary-test-%d", os.Getpid())
	name := "agentdeck_primary_window_probe"
	tm := func(args ...string) {
		t.Helper()
		full := append([]string{"-L", socket, "-f", "/dev/null"}, args...)
		if out, err := exec.Command("tmux", full...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	// Window 1 is the "agent": cat echoes whatever reaches its tty.
	tm("new-session", "-d", "-s", name, "cat")
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	// The operator opens an auxiliary window; new-window makes it active.
	tm("new-window", "-t", name, "sleep 30")

	sess := &Session{Name: name, SocketName: socket}
	const marker = "primary-window-marker"
	if err := sess.SendKeysToPrimaryWindow(marker); err != nil {
		t.Fatalf("SendKeysToPrimaryWindow: %v", err)
	}
	if err := sess.SendNamedKeyToPrimaryWindow("Space"); err != nil {
		t.Fatalf("SendNamedKeyToPrimaryWindow: %v", err)
	}
	if err := sess.SendEnterToPrimaryWindow(); err != nil {
		t.Fatalf("SendEnterToPrimaryWindow: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		primary, err := sess.CapturePrimaryFullHistory()
		if err != nil {
			t.Fatalf("CapturePrimaryFullHistory: %v", err)
		}
		if strings.Contains(primary, marker) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("primary window never received the keys; captured %q", primary)
		}
		time.Sleep(20 * time.Millisecond)
	}

	active, err := sess.CaptureFullHistory()
	if err != nil {
		t.Fatalf("CaptureFullHistory: %v", err)
	}
	if strings.Contains(active, marker) {
		t.Fatalf("keys leaked into the focused auxiliary window: %q", active)
	}
}
