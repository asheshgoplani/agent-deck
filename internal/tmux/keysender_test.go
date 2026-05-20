package tmux

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// requireTmux skips the test when no `tmux` binary is on PATH. Several CI
// stages run in containers without tmux, and the package's existing tests
// (e.g. TestPersistence_*) use the same skip pattern.
func requireTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux binary not on PATH; skipping")
	}
}

// makeIsolatedServer creates a tmux server on a per-test socket with one
// detached session and returns the socket name plus a cleanup that kills
// the server. Isolating the server prevents test cross-talk with the
// developer's interactive tmux and survives parallel runs.
func makeIsolatedServer(t *testing.T) (socket, target string) {
	t.Helper()
	// Hash the test name into a short socket selector — Unix socket paths
	// have a hard ~108 byte limit and long test names like
	// TestOpenKeySender_Issue1102_PerfBudget100KeysUnder500ms blow it out.
	socket = fmt.Sprintf("ks%x", sha256.Sum256([]byte(t.Name())))[:14]
	target = "tgt"
	if out, err := exec.Command("tmux", "-L", socket, "new-session", "-d", "-s", target, "bash").CombinedOutput(); err != nil {
		t.Fatalf("create tmux session: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-L", socket, "kill-server").Run()
	})
	return socket, target
}

// TestOpenKeySender_StreamsLiteralKeysToPane verifies that SendKeys writes
// the literal bytes to the target pane via the persistent control-mode
// client, instead of fork+exec'ing a tmux client per call.
func TestOpenKeySender_StreamsLiteralKeysToPane(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	sender, err := OpenKeySender(socket, target)
	if err != nil {
		t.Fatalf("OpenKeySender: %v", err)
	}
	defer sender.Close()

	for _, chunk := range []string{"he", "llo", " world"} {
		if err := sender.SendKeys(chunk); err != nil {
			t.Fatalf("SendKeys(%q): %v", chunk, err)
		}
	}

	// Give tmux a moment to flush; control mode is async.
	time.Sleep(100 * time.Millisecond)

	pane, err := exec.Command("tmux", "-L", socket, "capture-pane", "-t", target, "-p").CombinedOutput()
	if err != nil {
		t.Fatalf("capture-pane: %v: %s", err, pane)
	}
	if !strings.Contains(string(pane), "hello world") {
		t.Errorf("pane does not contain typed text; got:\n%s", pane)
	}
}

// TestOpenKeySender_Issue1102_PerfBudget100KeysUnder500ms is the regression
// test for #1102's headline complaint: per-keystroke fork+exec made typing
// visibly slow even with #1096's batching. With the persistent client, 100
// individual SendKeys calls must complete well under the 500ms budget the
// user-facing fix targets.
//
// The old implementation (`(*Session).SendKeys` calling `tmux send-keys -l`
// per call) costs ~3-50ms per call depending on platform load — on Apple
// Silicon under heavy AV the user reported 100 keys taking many seconds.
// The new path streams stdin to one already-running tmux client, so each
// call is a memcpy plus syscall.
func TestOpenKeySender_Issue1102_PerfBudget100KeysUnder500ms(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	sender, err := OpenKeySender(socket, target)
	if err != nil {
		t.Fatalf("OpenKeySender: %v", err)
	}
	defer sender.Close()

	const n = 100
	start := time.Now()
	for i := 0; i < n; i++ {
		if err := sender.SendKeys("x"); err != nil {
			t.Fatalf("SendKeys: %v", err)
		}
	}
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("100 SendKeys took %v, want <500ms (#1102 perf budget)", elapsed)
	}
	t.Logf("100 SendKeys via persistent client: %v (%.2fms/keystroke)",
		elapsed, float64(elapsed.Microseconds())/1000.0/float64(n))
}

// TestOpenKeySender_NamedKeyAndEnter exercises the non-rune dispatch paths
// (Up/Down/Backspace via SendNamedKey, Enter via SendEnter). These are the
// keystrokes #1094 added so users could navigate claude pickers and submit
// prompts — they must keep working through the persistent client.
func TestOpenKeySender_NamedKeyAndEnter(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	sender, err := OpenKeySender(socket, target)
	if err != nil {
		t.Fatalf("OpenKeySender: %v", err)
	}
	defer sender.Close()

	if err := sender.SendKeys("abc"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	if err := sender.SendNamedKey("BSpace"); err != nil {
		t.Fatalf("SendNamedKey: %v", err)
	}
	if err := sender.SendEnter(); err != nil {
		t.Fatalf("SendEnter: %v", err)
	}

	time.Sleep(150 * time.Millisecond)

	pane, err := exec.Command("tmux", "-L", socket, "capture-pane", "-t", target, "-p").CombinedOutput()
	if err != nil {
		t.Fatalf("capture-pane: %v: %s", err, pane)
	}
	// After "abc" + BSpace + Enter, the bash prompt sees "ab" submitted.
	// Without asserting exact prompt layout (varies by user shell), we
	// at least require "ab" appears and "abc" does not (BSpace took effect).
	got := string(pane)
	if !strings.Contains(got, "ab") {
		t.Errorf("pane missing 'ab' after BSpace+Enter:\n%s", got)
	}
	if strings.Contains(got, "abc") {
		t.Errorf("pane still shows 'abc' — BSpace did not take effect:\n%s", got)
	}
}

// TestOpenKeySender_CloseIsIdempotent guards against the common refcount-
// driven double-close pattern (exit insert mode → close, then defer-close on
// panic recovery). The second Close must return nil and not crash.
func TestOpenKeySender_CloseIsIdempotent(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	sender, err := OpenKeySender(socket, target)
	if err != nil {
		t.Fatalf("OpenKeySender: %v", err)
	}

	if err := sender.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sender.Close(); err != nil {
		t.Errorf("second Close should be a no-op; got %v", err)
	}
	if err := sender.SendKeys("x"); err == nil {
		t.Error("SendKeys after Close should fail")
	} else if !strings.Contains(err.Error(), "closed") {
		t.Errorf("error after Close = %v, want one mentioning 'closed'", err)
	}
}

// TestOpenKeySender_RejectsEmptyTarget guards the caller contract: a blank
// target is a programming error (e.g. a stub Session.Name), not user input,
// and would otherwise produce an opaque tmux usage error on the first send.
func TestOpenKeySender_RejectsEmptyTarget(t *testing.T) {
	if _, err := OpenKeySender("", ""); err == nil {
		t.Error("empty target should be rejected")
	} else if !strings.Contains(err.Error(), "target") {
		t.Errorf("error = %v, want mention of 'target'", err)
	}
}

// TestTmuxQuote_HandlesEmbeddedSingleQuotes verifies the tmux-side quoting
// keeps user input opaque to the command parser. Insert mode bursts may
// contain apostrophes, backticks, dollars — all of which tmux's parser would
// interpret in unquoted or double-quoted contexts.
func TestTmuxQuote_HandlesEmbeddedSingleQuotes(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", `''`},
		{"abc", `'abc'`},
		{"it's", `'it'\''s'`},
		{`$( foo )`, `'$( foo )'`},
		{`"hi"`, `'"hi"'`},
	}
	for _, tc := range cases {
		got := tmuxQuote(tc.in)
		if got != tc.want {
			t.Errorf("tmuxQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestOpenKeySender_ErrAfterCloseIsTyped is a tiny robustness check: the
// "closed" error path is exercised by callers in the UI on panic recovery,
// so make sure it's a real error value (not nil masquerading as success).
func TestOpenKeySender_ErrAfterCloseIsTyped(t *testing.T) {
	requireTmux(t)
	socket, target := makeIsolatedServer(t)

	sender, err := OpenKeySender(socket, target)
	if err != nil {
		t.Fatalf("OpenKeySender: %v", err)
	}
	_ = sender.Close()

	err = sender.SendKeys("x")
	if err == nil || errors.Is(err, nil) {
		t.Fatalf("expected non-nil error after Close, got %v", err)
	}
}
