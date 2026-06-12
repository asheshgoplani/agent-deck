// Regression tests for issue #1369: quick-approve (`a`) must be able to deliver
// "1"+Enter to a SPECIFIC tmux window — the one showing the Claude prompt —
// not just the session's active window. SendKeysAndEnterToWindow targets
// "<session>:<windowIndex>"; the existing SendKeysAndEnter keeps targeting the
// session's active window (no window suffix). These tests record the exact
// send-keys argv via the keySenderExec seam (see recordKeySender in
// tmux_vim_mode_test.go), so they run without a real tmux server.
package tmux

import (
	"strings"
	"testing"
)

// TestSendKeysAndEnterToWindow_TargetsWindowIndex: the non-vim window send must
// emit exactly the literal paste then Enter, both aimed at "<session>:<idx>".
func TestSendKeysAndEnterToWindow_TargetsWindowIndex(t *testing.T) {
	calls := recordKeySender(t)

	s := &Session{Name: "multiwin"} // VimMode defaults to false
	if err := s.SendKeysAndEnterToWindow(3, "1"); err != nil {
		t.Fatalf("SendKeysAndEnterToWindow returned error: %v", err)
	}

	c := *calls
	if len(c) != 2 {
		t.Fatalf("expected 2 tmux calls (paste, Enter), got %d: %v", len(c), c)
	}
	for _, call := range c {
		if !strings.Contains(call, "-t multiwin:3") {
			t.Fatalf("call not targeted at window multiwin:3: %q", call)
		}
	}
	if !strings.Contains(c[0], "-l") || !strings.Contains(c[0], "-- 1") {
		t.Fatalf("first call must be the literal paste of \"1\": %q", c[0])
	}
	if sentKey(c[1]) != "Enter" {
		t.Fatalf("second call must be Enter, got %q (%v)", sentKey(c[1]), c)
	}
}

// TestSendKeysAndEnter_ActiveWindow_NoWindowSuffix locks the existing behavior:
// the active-window send must target the bare session name with NO ":<idx>"
// suffix, so the window-targeted variant is the only thing that addresses a
// specific window.
func TestSendKeysAndEnter_ActiveWindow_NoWindowSuffix(t *testing.T) {
	calls := recordKeySender(t)

	s := &Session{Name: "activewin"}
	if err := s.SendKeysAndEnter("1"); err != nil {
		t.Fatalf("SendKeysAndEnter returned error: %v", err)
	}

	for _, call := range *calls {
		if strings.Contains(call, "activewin:") {
			t.Fatalf("active-window send must not carry a window suffix: %q", call)
		}
		if !strings.Contains(call, "-t activewin") {
			t.Fatalf("call must target the session: %q", call)
		}
	}
}

// TestSendKeysAndEnterToWindow_VimMode_GuardsThenTargetsWindow verifies the
// window send still honors the #1264 vim insert-guard (Escape, i) and that the
// guard, paste, and Enter are ALL aimed at the window target.
func TestSendKeysAndEnterToWindow_VimMode_GuardsThenTargetsWindow(t *testing.T) {
	calls := recordKeySender(t)

	s := &Session{Name: "vimwin", VimMode: true}
	if err := s.SendKeysAndEnterToWindow(2, "1"); err != nil {
		t.Fatalf("SendKeysAndEnterToWindow returned error: %v", err)
	}

	c := *calls
	if len(c) != 4 {
		t.Fatalf("expected 4 calls (Escape, i, paste, Enter), got %d: %v", len(c), c)
	}
	for _, call := range c {
		if !strings.Contains(call, "-t vimwin:2") {
			t.Fatalf("vim window send not targeted at vimwin:2: %q", call)
		}
	}
	if sentKey(c[0]) != "Escape" || sentKey(c[1]) != "i" {
		t.Fatalf("vim guard must be Escape then i first: %v", c)
	}
	if sentKey(c[3]) != "Enter" {
		t.Fatalf("final key must be Enter, got %q (%v)", sentKey(c[3]), c)
	}
}
