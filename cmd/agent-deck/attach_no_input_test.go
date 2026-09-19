package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestAttachDeliversNoInput is the tmux-only half of TestNativeSSHAttachLifecycle:
// a fresh session whose pane runs a raw receiver must see no input from
// agent-deck itself, neither on `session start` nor when a client attaches.
// The SSH test found a receipt for an empty line in the initial frame, typed
// by the start path into a pane owned by the session's wrapper.
func TestAttachDeliversNoInput(t *testing.T) {
	for _, tool := range []string{"tmux", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("requires %s", tool)
		}
	}
	bin := channelsCLIBinary(t)
	home := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	socket := fmt.Sprintf("noinput-%d", time.Now().UnixNano())
	write(filepath.Join(home, ".config", "agent-deck", "config.toml"), "[tmux]\nsocket_name = '"+socket+"'\n")
	write(filepath.Join(home, ".zshrc"), "# attach no-input test\n")
	receiver := filepath.Join(home, "receiver.py")
	write(receiver, `import os, tty, sys
tty.setraw(0)
sys.stdout.write("\033[2J\033[HREADY\r\n")
sys.stdout.flush()
while True:
    data = os.read(0, 8192)
    if not data: break
    sys.stdout.write("\r\nRX:" + data.hex())
    sys.stdout.flush()
`)
	var env []string
	for _, kv := range os.Environ() {
		key := strings.SplitN(kv, "=", 2)[0]
		if key == "HOME" || key == "TERM" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "AGENTDECK_") || strings.HasPrefix(key, "TMUX") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "HOME="+home, "TERM=xterm-256color")
	run := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = env
		cmd.Dir = home
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, out)
		}
	}
	tmux := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "tmux", append([]string{"-L", socket}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v %s", args, err, out)
		}
		return string(out)
	}
	run("add", home, "--title", "native", "--cmd", "shell", "--wrapper", "python3 -u "+receiver, "--json")
	run("session", "start", "native", "--json")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "session", "stop", "native")
		cmd.Env = env
		_, _ = cmd.CombinedOutput()
	})
	waitFor := func(what string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if ready() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out: %s", what)
	}
	name := strings.TrimSpace(tmux("display-message", "-p", "#{session_name}"))
	grid := func() string { return tmux("capture-pane", "-p", "-t", name) }
	// Anything typed at the pane is queued by the time the caller gets here;
	// give the receiver a moment to echo it before judging.
	assertNoInput := func(stage string) {
		t.Helper()
		time.Sleep(time.Second)
		if g := grid(); strings.Contains(g, "RX:") {
			t.Fatalf("%s delivered input to the pane: %q", stage, strings.TrimRight(g, "\n"))
		}
	}
	waitFor("receiver ready", func() bool { return strings.Contains(grid(), "READY") })
	assertNoInput("session start")

	cmd := exec.Command(bin, "session", "attach", "native")
	cmd.Env = env
	cmd.Dir = home
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	processDone := make(chan struct{})
	go func() { done <- cmd.Wait(); close(processDone) }()
	t.Cleanup(func() {
		_ = terminal.Close()
		_ = cmd.Process.Kill()
		// The detach below consumes done; processDone is safe to wait on twice.
		select {
		case <-processDone:
		case <-time.After(3 * time.Second):
			t.Error("attach cleanup timed out")
		}
	})
	// Drain the client's output so it never blocks; answer terminal queries
	// the way a real terminal would so no reply is mistaken for typed input.
	go func() {
		buf := make([]byte, 8192)
		var pending []byte
		for {
			n, err := terminal.Read(buf)
			if n > 0 {
				pending = append(pending, buf[:n]...)
				for _, query := range []struct{ request, response string }{
					{"\x1b]11;?\x1b\\", "\x1b]11;rgb:0000/0000/0000\x1b\\"},
					{"\x1b]11;?\a", "\x1b]11;rgb:0000/0000/0000\x1b\\"},
					{"\x1b[6n", "\x1b[1;1R"},
				} {
					for bytes.Contains(pending, []byte(query.request)) {
						_, _ = terminal.WriteString(query.response)
						pending = bytes.Replace(pending, []byte(query.request), nil, 1)
					}
				}
				if len(pending) > 64 {
					pending = pending[len(pending)-64:]
				}
			}
			if err != nil {
				return
			}
		}
	}()
	waitFor("client attached", func() bool {
		return strings.TrimSpace(tmux("display-message", "-p", "-t", name, "#{session_attached}")) != "0"
	})
	assertNoInput("attach")
	if _, err := terminal.WriteString("\x11"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Ctrl+Q detach: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Error("detach timed out")
	}
}
