package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCaptureGoneFromErr_KnownStderrMarkers verifies that only tmux stderr that
// explicitly says the target is absent is classified as gone. This is the H2
// safeguard from plan 001: capture-pane exit 1 is generic, so an unrecognized
// stderr must NOT be swallowed as a benign "gone".
func TestCaptureGoneFromErr_KnownStderrMarkers(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		want   bool
	}{
		// Absent-target messages (various tmux versions) → gone.
		{"cant find session", "can't find session: agentdeck_foo_123", true},
		{"cant find pane", "can't find pane: %304", true},
		{"cant find window", "can't find window: 0", true},
		{"no such session", "no such session: agentdeck_bar", true},
		{"no server running", "no server running on /private/tmp/tmux-501/default", true},
		{"lost server", "lost server", true},
		{"server exited", "server exited unexpectedly", true},
		{"stale socket", "error connecting to /private/tmp/tmux-501/adtmux (No such file or directory)", true},
		{"permission denied", "error connecting to /tmp/blocked/socket (Permission denied)", false},
		{"bad socket", "error connecting to /tmp/socket (Not a socket)", false},
		{"connection refused", "error connecting to /tmp/socket (Connection refused)", false},
		{"misleading socket path", "error connecting to /tmp/no server running/socket (Permission denied)", false},
		{"case insensitive", "Can't Find Session: X", true},
		{"marker mid-message", "capture-pane: can't find pane: %9", true},

		// NOT absent → must surface as a real error (conservative default).
		{"empty stderr", "", false},
		{"unknown error", "some unexpected tmux failure", false},
		{"usage error", "usage: capture-pane [-aeJNpPqCM]", false},
		{"bad flag", "unknown flag: -Z", false},
		{"ambiguous", "ambiguous option: -S", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := &exec.ExitError{Stderr: []byte(tc.stderr)}
			if got := captureGoneFromErr(err); got != tc.want {
				t.Fatalf("captureGoneFromErr(stderr=%q) = %v, want %v", tc.stderr, got, tc.want)
			}
		})
	}
}

// TestCaptureGoneFromErr_NonExitError verifies that non-ExitError failures
// (e.g. a context kill / timeout wrapped as a generic error, or nil) are never
// classified as gone, so ErrCaptureTimeout handling is unaffected.
func TestCaptureGoneFromErr_NonExitError(t *testing.T) {
	if captureGoneFromErr(nil) {
		t.Fatal("nil error must not be classified as gone")
	}
	if captureGoneFromErr(errors.New("signal: killed")) {
		t.Fatal("plain error must not be classified as gone")
	}
	// A wrapped ExitError with an absent-target stderr is still detected.
	wrapped := fmt.Errorf("run failed: %w", &exec.ExitError{Stderr: []byte("no server running on /x")})
	if !captureGoneFromErr(wrapped) {
		t.Fatal("wrapped ExitError with gone marker must be detected via errors.As")
	}
}

// TestCaptureFullHistory_GoneReturnsSentinel is an integration test (real tmux):
// capturing history from a session on a socket with no running server must
// return ErrCaptureGone, not an opaque "failed to capture history" error. This
// is the exact post-completion/resume-race condition from plan 001, reproduced
// deterministically as "no server running".
func TestCaptureFullHistory_GoneReturnsSentinel(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	sess := &Session{
		Name:       "agentdeck_capture_gone_probe",
		SocketName: fmt.Sprintf("adeck-gone-test-%d", os.Getpid()),
	}
	// No server was ever started on this isolated socket.
	_, err := sess.CaptureFullHistory()
	if !errors.Is(err, ErrCaptureGone) {
		t.Fatalf("CaptureFullHistory on absent server = %v, want ErrCaptureGone", err)
	}
	// CaptureHistoryLines must classify the same condition identically.
	_, err = sess.CaptureHistoryLines(200)
	if !errors.Is(err, ErrCaptureGone) {
		t.Fatalf("CaptureHistoryLines on absent server = %v, want ErrCaptureGone", err)
	}
}

// TestCaptureFullHistory_LiveSessionNotGone is the positive control: a present
// session captures successfully and is NOT classified as gone. This guards
// against captureGoneFromErr over-matching and misrouting a real session's
// output. The pane is kept alive (sleep) so capture races nothing.
func TestCaptureFullHistory_LiveSessionNotGone(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	socket := fmt.Sprintf("adeck-live-test-%d", os.Getpid())
	name := "agentdeck_capture_live_probe"
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", name, "printf 'done-marker\\n'; sleep 30").CombinedOutput(); err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-session", "-t", name).Run() })

	sess := &Session{Name: name, SocketName: socket}
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := sess.CaptureFullHistory()
		if err != nil {
			t.Fatalf("CaptureFullHistory on live session: %v", err)
		}
		if strings.Contains(out, "done-marker") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("captured history missing expected content; got %q", out)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The server remains alive, but this particular capture target is absent.
	missing := &Session{Name: name + "_missing", SocketName: socket}
	if _, err := missing.CaptureFullHistory(); !errors.Is(err, ErrCaptureGone) {
		t.Fatalf("missing target on live server = %v, want ErrCaptureGone", err)
	}
}

func TestCaptureHistory_PermissionDeniedIsNotGone(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	// An inaccessible parent directory makes the actual tmux connect fail with EACCES.
	root := t.TempDir()
	t.Setenv("TMUX_TMPDIR", root)
	dir := filepath.Join(root, fmt.Sprintf("tmux-%d", os.Getuid()), "blocked")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	if _, err := os.ReadDir(dir); err == nil {
		t.Skip("process can bypass directory permissions")
	}
	sess := &Session{Name: "missing", SocketName: "blocked/socket"}
	for _, capture := range []struct {
		name string
		run  func() (string, error)
	}{
		{"full", sess.CaptureFullHistory}, {"lines", func() (string, error) { return sess.CaptureHistoryLines(20) }},
	} {
		t.Run(capture.name, func(t *testing.T) {
			_, err := capture.run()
			if err == nil || errors.Is(err, ErrCaptureGone) {
				t.Fatalf("permission failure = %v, want non-gone error", err)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || !strings.Contains(strings.ToLower(string(exitErr.Stderr)), "permission denied") {
				t.Fatalf("expected real tmux permission error: %v", err)
			}
		})
	}
}
