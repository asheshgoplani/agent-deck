package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestTUIAttachKeepsKeysTypedRightAfterEnter runs the real TUI on a pty and
// attaches a session with Enter, typing a marker 8 ms later. Before the fix,
// the attach's synchronous setup (tmux calls) ran with the TUI still reading
// stdin, so keys typed in those milliseconds became TUI keys (k moved the
// cursor, ...) and never reached the pane.
func TestTUIAttachKeepsKeysTypedRightAfterEnter(t *testing.T) {
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
	socket := fmt.Sprintf("earlykeys-%d", time.Now().UnixNano())
	write(filepath.Join(home, ".config", "agent-deck", "config.toml"), "[telemetry]\ndisabled = true\n[updates]\ncheck_enabled = false\n[claude]\nhooks_enabled = false\n[tmux]\nsocket_name = '"+socket+"'\n") // no first-run dialogs
	write(filepath.Join(home, ".zshrc"), "# early keys test\n")
	receiver := filepath.Join(home, "receiver.py")
	write(receiver, `import os, tty, sys
tty.setraw(0)
sys.stdout.write("\033[2J\033[HREADY\r\n")
sys.stdout.flush()
while True:
    data = os.read(0, 8192)
    if not data: break
    sys.stdout.write("RX:" + data.hex() + "\r\n")
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
	received := func() string { // every byte the pane's program read, hex
		var b strings.Builder
		for _, line := range strings.Split(tmux("capture-pane", "-p", "-S", "-500", "-t", name), "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "RX:"); ok {
				b.WriteString(rest)
			}
		}
		return b.String()
	}
	attached := func() bool { // a terminal client, not the TUI's control-mode one
		return strings.Contains(tmux("list-clients", "-t", name, "-F", "c#{client_control_mode}"), "c0")
	}
	waitFor("receiver ready", func() bool { return strings.Contains(tmux("capture-pane", "-p", "-t", name), "READY") })

	cmd := exec.Command(bin)
	cmd.Env = env
	cmd.Dir = home
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	processDone := make(chan struct{})
	go func() { _ = cmd.Wait(); close(processDone) }()
	t.Cleanup(func() {
		_ = terminal.Close()
		_ = cmd.Process.Kill()
		select {
		case <-processDone:
		case <-time.After(3 * time.Second):
			t.Error("TUI cleanup timed out")
		}
	})
	// Drain the TUI's output so it never blocks, keep the tail for the
	// readiness check, and answer terminal queries the way a terminal would.
	var outMu sync.Mutex
	var tail []byte
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := terminal.Read(buf)
			if n > 0 {
				outMu.Lock()
				tail = append(tail, buf[:n]...)
				for _, query := range []struct{ request, response string }{
					{"\x1b]11;?\x1b\\", "\x1b]11;rgb:0000/0000/0000\x1b\\"},
					{"\x1b]11;?\a", "\x1b]11;rgb:0000/0000/0000\x1b\\"},
					{"\x1b[6n", "\x1b[1;1R"},
				} {
					for bytes.Contains(tail, []byte(query.request)) {
						_, _ = terminal.WriteString(query.response)
						tail = bytes.Replace(tail, []byte(query.request), nil, 1)
					}
				}
				if len(tail) > 1<<16 {
					tail = tail[len(tail)-1<<16:]
				}
				outMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	listed := func() bool {
		outMu.Lock()
		defer outMu.Unlock()
		return bytes.Contains(tail, []byte("native"))
	}
	for deadline := time.Now().Add(20 * time.Second); !listed(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			outMu.Lock()
			screen := string(tail)
			outMu.Unlock()
			if len(screen) > 4000 {
				screen = screen[len(screen)-4000:]
			}
			t.Fatalf("timed out: the TUI to list the session; its output ends %q", screen)
		}
	}
	time.Sleep(1500 * time.Millisecond) // let startup work settle
	// The cursor starts on the group row.
	if _, err := terminal.WriteString("j"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)

	// The first attach's setup is quick; the loss showed from the second on.
	for round := 0; round < 3; round++ {
		marker := fmt.Sprintf("mk%dz", round)
		if _, err := terminal.WriteString("\r"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(8 * time.Millisecond)
		if _, err := terminal.WriteString(marker); err != nil {
			t.Fatal(err)
		}
		waitFor(fmt.Sprintf("round %d: Enter to attach the session", round), attached)
		deadline := time.Now().Add(3 * time.Second)
		for !strings.Contains(received(), hex.EncodeToString([]byte(marker))) {
			if time.Now().After(deadline) {
				t.Fatalf("round %d: %q typed 8 ms after Enter never reached the pane (pane read %s)", round, marker, received())
			}
			time.Sleep(20 * time.Millisecond)
		}
		if _, err := terminal.WriteString("\x11"); err != nil { // Ctrl+Q detaches
			t.Fatal(err)
		}
		waitFor(fmt.Sprintf("round %d: Ctrl+Q to detach", round), func() bool { return !attached() })
		time.Sleep(1500 * time.Millisecond) // the TUI settles after the detach
	}
}
