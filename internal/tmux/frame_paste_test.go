// Regression tests for a swallowed Codex submit.
//
// Failure mechanism (measured on codex-cli 0.159.3 under tmux 3.4): the body
// used to go out as one unframed `send-keys -l` write. Codex re-processes a
// typed burst character by character; while that ingestion is still running its
// paste-burst detector treats the arriving Enter as a newline INSIDE the paste
// and re-arms a 120ms suppression window from it. A 513-byte single-line body
// was therefore never submitted with Enter at 100ms or 200ms (only at 500ms),
// leaving the message in the composer while the send reported
// typed_not_submitted.
//
// The fix (Session.SetFramePastes, wired for Codex by the session layer) sends
// even a short single-line body through the framed paste transport, so the
// composer receives ONE paste event and is ready before the pre-Enter delay.
// Those transports are recorded here through the keySenderExec seam, so the
// tests need no tmux server.
package tmux

import (
	"strings"
	"testing"
)

// TestSendKeysAndEnter_FramePastes_SingleLineUsesPasteTransport is the core
// regression: with framing enabled, a short single-line body must reach the
// pane as a framed paste (load-buffer + paste-buffer -p), never as an unframed
// `send-keys -l` burst.
//
// Pre-fix: the body is typed with send-keys -l and this test FAILS.
func TestSendKeysAndEnter_FramePastes_SingleLineUsesPasteTransport(t *testing.T) {
	calls := recordKeySender(t)

	s := &Session{Name: "codex-framed"}
	s.SetFramePastes(true)
	if err := s.SendKeysAndEnter("a short single-line message"); err != nil {
		t.Fatalf("SendKeysAndEnter returned error: %v", err)
	}

	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "load-buffer -b ") {
		t.Fatalf("framed send did not stage the body with load-buffer; calls:\n%s", joined)
	}
	if !strings.Contains(joined, "paste-buffer -p -r -d -b ") {
		t.Fatalf("framed send did not deliver the body with paste-buffer -p; calls:\n%s", joined)
	}
	for _, c := range *calls {
		if strings.Contains(c, "send-keys -l") {
			t.Fatalf("framed send typed the body with send-keys -l instead of pasting it; calls:\n%s", joined)
		}
	}
	// The submit is still one plain Enter, after the paste.
	if got := sentKey((*calls)[len(*calls)-1]); got != "Enter" {
		t.Fatalf("last key = %q, want Enter; calls:\n%s", got, joined)
	}
}

// TestSendKeysAndEnter_Default_SingleLineTypesDirectly pins the other half of
// the contract: every tool that does not ask for framing keeps the historical
// byte-for-byte transport, so this change is invisible to it.
func TestSendKeysAndEnter_Default_SingleLineTypesDirectly(t *testing.T) {
	calls := recordKeySender(t)

	s := &Session{Name: "claude-direct"}
	if err := s.SendKeysAndEnter("a short single-line message"); err != nil {
		t.Fatalf("SendKeysAndEnter returned error: %v", err)
	}

	joined := strings.Join(*calls, "\n")
	if strings.Contains(joined, "paste-buffer") || strings.Contains(joined, "load-buffer") {
		t.Fatalf("unframed session took the paste transport; calls:\n%s", joined)
	}
	found := false
	for _, c := range *calls {
		if strings.Contains(c, "send-keys -l") && strings.Contains(c, "a short single-line message") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unframed session did not type the body with send-keys -l; calls:\n%s", joined)
	}
}
