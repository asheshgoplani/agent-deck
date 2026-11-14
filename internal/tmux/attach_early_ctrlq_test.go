//go:build !windows

package tmux

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestAttachDetachesOnCtrlQTypedAsTheClientStarts types Ctrl+Q on a real pty
// the moment the tmux attach client exists (from OnClientStarted) and expects
// the attach to end. Bubble Tea hands the terminal over in cooked mode, where
// IXON makes the tty driver itself eat Ctrl+Q (0x11 is the START character).
// Before the fix, raw mode was set only after the client started and after
// OnClientStarted (the TUI's state DB write), so a Ctrl+Q pressed right as the
// session appeared never reached the stdin pump and the attach stayed put.
func TestAttachDetachesOnCtrlQTypedAsTheClientStarts(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	socket := fmt.Sprintf("adeck-early-ctrlq-%d", os.Getpid())
	name := "agentdeck_early_ctrlq_probe"
	if out, err := exec.Command("tmux", "-L", socket, "-f", "/dev/null", "new-session", "-d", "-s", name, "-x", "80", "-y", "24", "sleep 60").CombinedOutput(); err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", name).Run() })

	// The attach owns os.Stdin/os.Stdout; point both at a fresh pty, which
	// starts cooked with IXON on, like the terminal Bubble Tea gives back.
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	if err := pty.Setsize(tty, &pty.Winsize{Cols: 80, Rows: 24}); err != nil {
		t.Fatalf("pty.Setsize: %v", err)
	}
	t.Setenv("TERM", "xterm-256color") // the attach client needs a usable terminal type
	t.Setenv("TMUX", "")               // restored after the test
	_ = os.Unsetenv("TMUX")
	var screen bytes.Buffer // what the terminal showed, for failure messages
	var screenMu sync.Mutex
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			screenMu.Lock()
			screen.Write(buf[:n])
			screenMu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	shown := func() string {
		screenMu.Lock()
		defer screenMu.Unlock()
		b := screen.Bytes()
		if len(b) > 2000 {
			b = b[len(b)-2000:]
		}
		return fmt.Sprintf("%q", b)
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = tty, tty
	t.Cleanup(func() {
		os.Stdin, os.Stdout = oldIn, oldOut
		_ = tty.Close()
		_ = ptmx.Close()
		<-drained
	})

	sess := &Session{Name: name, SocketName: socket}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientUp := false
	opts := AttachOptions{OnClientStarted: func(int) {
		// The TUI's callback writes the state DB here. Meanwhile the client
		// registers with the server and the user, seeing the session, types
		// Ctrl+Q; the tty driver handles it under the terminal's current mode.
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			out, _ := exec.Command("tmux", "-L", socket, "list-clients", "-t", name).Output()
			if clientUp = len(out) > 0; clientUp {
				break
			}
		}
		_, _ = ptmx.Write([]byte{0x11}) // Ctrl+Q
		time.Sleep(20 * time.Millisecond)
	}}
	done := make(chan error, 1)
	go func() {
		_, err := sess.AttachWithOptions(ctx, opts)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("attach returned %v", err)
		}
		if !clientUp {
			t.Fatalf("the tmux attach client never showed up in list-clients; terminal showed %s", shown())
		}
	case <-time.After(5 * time.Second):
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("attach did not return after cancel")
		}
		t.Fatalf("Ctrl+Q typed as the attach client started did not detach (the tty swallowed it before raw mode); terminal showed %s", shown())
	}
}
