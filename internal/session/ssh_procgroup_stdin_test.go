package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// fakeQuickSSH puts an ssh on PATH that records its argv and exits 0 at
// once without reading stdin.
func fakeQuickSSH(t *testing.T) (argvLog string) {
	t.Helper()
	dir := t.TempDir()
	argvLog = filepath.Join(dir, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + argvLog + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvLog
}

// A forwarded command run from a terminal: ssh exits 0 straight away while
// the CLI's stdin is a tty nobody types into. The command must succeed, not
// fail with "exec: WaitDelay expired before I/O complete", and no stdin
// copier may be left blocked on the terminal.
func TestSSHRunIOTerminalStdinNoWaitDelayError(t *testing.T) {
	fakeQuickSSH(t)
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	defer func() { _ = tty.Close() }()

	r := NewSSHRunner("box", RemoteConfig{Host: "user@box.example"})
	before := runtime.NumGoroutine()
	for i := 0; i < 5; i++ {
		done := make(chan error, 1)
		go func() {
			done <- r.RunIO(context.Background(), tty, &strings.Builder{}, &strings.Builder{}, "session", "send", "s1", "--message-file", "-")
		}()
		var err error
		select {
		case err = <-done:
		case <-time.After(10 * time.Second):
			// A stdin copier is stuck reading the terminal: type a line so
			// it can fail on the closed pipe and let Wait return.
			_, _ = ptmx.Write([]byte("x\n"))
			select {
			case err = <-done:
				t.Fatalf("run %d: RunIO hung on terminal stdin until a key was typed, then returned %v", i, err)
			case <-time.After(10 * time.Second):
				t.Fatalf("run %d: RunIO hung on terminal stdin", i)
			}
		}
		if errors.Is(err, exec.ErrWaitDelay) || err != nil {
			t.Fatalf("run %d: RunIO with terminal stdin = %v, want nil", i, err)
		}
	}
	// Give exited goroutines a moment to unwind before counting.
	deadline := time.Now().Add(2 * time.Second)
	after := runtime.NumGoroutine()
	for after > before+1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		after = runtime.NumGoroutine()
	}
	if after > before+1 {
		t.Fatalf("goroutines leaked: %d before, %d after five runs", before, after)
	}
}

// The remote-side timeout(1) wrapper only bounds read-only verbs. A slow
// mutating verb (add, fork, restart, remove, ...) killed at the deadline
// could leave half-made worktrees behind, so it runs unwrapped.
func TestSSHRunRemoteTimeoutOnlyForReadOnlyVerbs(t *testing.T) {
	cases := []struct {
		args    []string
		wrapped bool
	}{
		{[]string{"list", "--json"}, true},
		{[]string{"session", "show", "s1", "--json"}, true},
		{[]string{"add", "/repo", "-t", "x", "--json"}, false},
		{[]string{"session", "fork", "s1", "--json"}, false},
		{[]string{"session", "restart", "s1"}, false},
		{[]string{"remove", "s1"}, false},
		{[]string{"session", "send", "s1", "hi"}, false},
	}
	for _, tc := range cases {
		argvLog := fakeQuickSSH(t)
		r := NewSSHRunner("box", RemoteConfig{Host: "user@box.example", CommandTimeoutSeconds: 1})
		r.cleanChannelSocketsFn = func() {}
		_, _ = r.Run(context.Background(), tc.args...)
		data, err := os.ReadFile(argvLog)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Contains(string(data), "command -v timeout")
		if got != tc.wrapped {
			t.Fatalf("%v: remote timeout wrapper = %v, want %v\nargv: %s", tc.args, got, tc.wrapped, data)
		}
	}
}
