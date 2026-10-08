//go:build !windows

package web

import (
	"bytes"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestTmuxAttachCommand_RGBOutput(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	t.Setenv("TERM", "xterm-ghostty")
	t.Setenv("COLORTERM", "")
	socket := fmt.Sprintf("web-rgb-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	// A private server and synthetic pane keep live sessions and user config
	// out of this reproduction of the Ghostty-launched web daemon.
	output, err := exec.Command("tmux", "-L", socket, "-f", "/dev/null", "new-session", "-d",
		"-s", "colors", "-x", "80", "-y", "24",
		"sh -c 'printf \"\\033[48;2;24;25;38m\\033[38;2;202;211;245mRGB sample\"; sleep 30'").CombinedOutput()
	if err != nil {
		t.Fatalf("create private RGB pane: %v: %s", err, output)
	}
	cmd := tmuxAttachCommand("colors", socket)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	done := make(chan bool, 1)
	go func() {
		var data []byte
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			data = append(data, buf[:n]...)
			if bytes.Contains(data, []byte("\x1b[48;2;24;25;38m")) &&
				bytes.Contains(data, []byte("\x1b[38;2;202;211;245m")) {
				done <- true
				return
			}
			if err != nil {
				done <- false
				return
			}
		}
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("attach ended without xterm-compatible RGB output")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tmux did not emit xterm-compatible RGB foreground and background")
	}
}
