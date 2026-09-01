package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	run := func(args ...string) {
		_ = exec.Command("tmux", append([]string{"-L", socket}, args...)...).Run()
	}
	// Keep the pane alive during capture so we test a present session, not a
	// teardown race.
	run("new-session", "-d", "-s", name, "printf 'done-marker\\n'; sleep 5")
	defer run("kill-server")
	time.Sleep(400 * time.Millisecond)

	sess := &Session{Name: name, SocketName: socket}
	out, err := sess.CaptureFullHistory()
	if errors.Is(err, ErrCaptureGone) {
		t.Fatalf("present session misclassified as gone: %v", err)
	}
	if err != nil {
		t.Fatalf("CaptureFullHistory on live session errored: %v", err)
	}
	if !strings.Contains(out, "done-marker") {
		t.Fatalf("captured history missing expected content; got %q", out)
	}
}
