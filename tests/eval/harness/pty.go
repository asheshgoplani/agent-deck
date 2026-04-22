package harness

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// PTYSession is a running process attached to a pseudo-terminal. Output
// streams through the PTY (not an io.Pipe) so line-buffered stdout behaves
// exactly as it would in a real user's terminal — which is the entire point
// of this harness (see Bug 1 in the evaluator-harness RFC).
type PTYSession struct {
	t *testing.T

	cmd *exec.Cmd
	f   *os.File // master side of the PTY

	mu     sync.Mutex
	buf    bytes.Buffer // accumulated output since start
	closed bool
	exit   error
	wg     sync.WaitGroup
}

// Spawn starts the agent-deck binary with args attached to a PTY. The caller
// is responsible for calling Close() — tests typically `defer p.Close()`.
func (s *Sandbox) Spawn(args ...string) *PTYSession {
	return s.SpawnWithEnv(nil, args...)
}

// SpawnWithEnv is like Spawn but overlays extraEnv on top of Sandbox.Env().
// Each entry in extraEnv is a "KEY=VALUE" string; it replaces any earlier
// entry with the same key, matching the convention of exec.Cmd.Env. Use
// this when a test needs real terminal capabilities (TERM=xterm-256color)
// — the default Env sets TERM=dumb to keep termenv probes quiet.
func (s *Sandbox) SpawnWithEnv(extraEnv []string, args ...string) *PTYSession {
	s.t.Helper()
	cmd := exec.Command(s.BinPath, args...)
	cmd.Env = mergeEnv(s.Env(), extraEnv)
	cmd.Dir = s.Home

	f, err := pty.Start(cmd)
	if err != nil {
		s.t.Fatalf("pty.Start: %v", err)
	}

	p := &PTYSession{t: s.t, cmd: cmd, f: f}
	p.wg.Add(1)
	go p.drain()
	return p
}

func mergeEnv(base, overlay []string) []string {
	if len(overlay) == 0 {
		return base
	}
	keyOf := func(s string) string {
		if i := strings.IndexByte(s, '='); i >= 0 {
			return s[:i]
		}
		return s
	}
	out := make([]string, 0, len(base)+len(overlay))
	overridden := make(map[string]bool, len(overlay))
	for _, e := range overlay {
		overridden[keyOf(e)] = true
	}
	for _, e := range base {
		if !overridden[keyOf(e)] {
			out = append(out, e)
		}
	}
	out = append(out, overlay...)
	return out
}

func (p *PTYSession) drain() {
	defer p.wg.Done()
	buf := make([]byte, 4096)
	for {
		n, err := p.f.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.buf.Write(buf[:n])
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Output returns a snapshot of everything read from the PTY so far.
func (p *PTYSession) Output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.String()
}

// Send writes s to the PTY master (i.e. as if the user typed it).
func (p *PTYSession) Send(s string) {
	p.t.Helper()
	if _, err := io.WriteString(p.f, s); err != nil {
		p.t.Fatalf("pty write: %v", err)
	}
}

// ExpectOutput waits up to timeout for want to appear in the PTY output and
// returns the index of the first match. Fails the test on timeout.
func (p *PTYSession) ExpectOutput(want string, timeout time.Duration) int {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if idx := strings.Index(p.Output(), want); idx >= 0 {
			return idx
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("ExpectOutput %q: timeout after %s.\nOutput so far:\n%s",
				want, timeout, p.Output())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ExpectOutputBefore waits up to timeout for want to appear, and asserts that
// want appears strictly before before in the output stream. If before has
// already appeared at the moment want arrives, or want never arrives, the
// test fails with a diagnostic dump.
//
// This is the core assertion for Bug 1 (strings.Builder buffering hid the
// disclosure behind a stdin read): in production, "posted PUBLICLY" must
// arrive before "[y/N]" does.
func (p *PTYSession) ExpectOutputBefore(want, before string, timeout time.Duration) {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		out := p.Output()
		wantIdx := strings.Index(out, want)
		beforeIdx := strings.Index(out, before)
		if wantIdx >= 0 && beforeIdx >= 0 {
			if wantIdx >= beforeIdx {
				p.t.Fatalf("ExpectOutputBefore: want %q must appear before %q.\nwant-idx=%d before-idx=%d\nOutput:\n%s",
					want, before, wantIdx, beforeIdx, out)
			}
			return
		}
		if wantIdx >= 0 && beforeIdx < 0 {
			// want arrived, before has not yet. Success — the order is
			// satisfied the moment want is visible.
			return
		}
		if wantIdx < 0 && beforeIdx >= 0 {
			p.t.Fatalf("ExpectOutputBefore: %q appeared before %q ever did.\nOutput:\n%s",
				before, want, out)
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("ExpectOutputBefore %q before %q: timeout after %s — neither seen.\nOutput:\n%s",
				want, before, timeout, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ExpectExit waits up to timeout for the child to exit and asserts on the
// exit code.
func (p *PTYSession) ExpectExit(wantCode int, timeout time.Duration) {
	p.t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		p.exit = err
		p.closed = true
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				p.t.Fatalf("unexpected exit error: %v", err)
			}
		}
		if code != wantCode {
			p.t.Fatalf("exit code: got %d, want %d.\nOutput:\n%s",
				code, wantCode, p.Output())
		}
	case <-time.After(timeout):
		p.t.Fatalf("ExpectExit: timeout after %s.\nOutput so far:\n%s",
			timeout, p.Output())
	}
}

// Close tears down the PTY session. Idempotent.
func (p *PTYSession) Close() {
	p.mu.Lock()
	already := p.closed
	p.closed = true
	p.mu.Unlock()
	if already {
		return
	}
	_ = p.f.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	// drain goroutine exits once f is closed.
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// Leak the goroutine rather than hang the test.
	}
}

// Dump is a helper for `t.Logf(p.Dump())` when a test is failing.
func (p *PTYSession) Dump() string {
	return fmt.Sprintf("--- PTY output (%d bytes) ---\n%s\n--- end ---",
		len(p.Output()), p.Output())
}
