package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// TestFormatWatcherDispatchMsg_UsesFullBody pins the second half of the
// Slack-truncation fix: the native conductor-pane delivery path must send the
// full message Body (not the first-line/200-byte Subject), fall back to Subject
// when Body is empty, and collapse newlines so tmux send-keys does not submit
// the line prematurely.
func TestFormatWatcherDispatchMsg_UsesFullBody(t *testing.T) {
	full := "first line\nsecond line — раньше терялась\nthird line"
	evt := watcher.Event{
		Source:   "slack",
		Sender:   "slack:D0B434J6BTR",
		Subject:  "first line", // first-line label the bug used to deliver
		Body:     full,
		RoutedTo: "intelas-conductor",
	}
	msg := formatWatcherDispatchMsg(evt)

	if !strings.Contains(msg, "second line — раньше терялась") || !strings.Contains(msg, "third line") {
		t.Errorf("dispatch msg dropped body lines: %q", msg)
	}
	if strings.ContainsAny(msg, "\n\r") {
		t.Errorf("dispatch msg must be single-line for tmux send-keys, got %q", msg)
	}
	if want := "[slack] slack:D0B434J6BTR: "; !strings.HasPrefix(msg, want) {
		t.Errorf("prefix: want %q, got %q", want, msg)
	}
}

// TestFormatWatcherDispatchMsg_FallsBackToSubject covers v1 / pre-fix events
// that carry no Body — delivery must still produce the Subject rather than an
// empty message.
func TestFormatWatcherDispatchMsg_FallsBackToSubject(t *testing.T) {
	evt := watcher.Event{
		Source:   "slack",
		Sender:   "slack:unknown",
		Subject:  "only a subject",
		Body:     "",
		RoutedTo: "intelas-conductor",
	}
	msg := formatWatcherDispatchMsg(evt)
	if want := "[slack] slack:unknown: only a subject"; msg != want {
		t.Errorf("fallback: want %q, got %q", want, msg)
	}
}
