// Regression tests for issue #1855: `session send --message-file` (and any
// other multi-line SendKeysAndEnter/SendKeysChunked call) glued adjacent
// lines together with no separator once it reached an Ink-style composer
// (Claude Code, confirmed manually against a real `claude` process).
//
// The transport itself never lost the bytes — a real pane driven with plain
// `send-keys -l` receives every embedded LF byte-for-byte. The problem is
// semantic: without a bracketed-paste envelope telling the composer "this is
// pasted text, take it literally", an Ink text input has no way to
// distinguish a bare LF from an unrecognized keystroke and drops it, which is
// what glues the lines together.
//
// These tests drive a real tmux pane (same style as issue1793) and assert
// what actually arrives at the other end, rather than the shape of the argv
// agent-deck emits.
package tmux

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// awaitBracketPasteFlag polls tmux's tracked bracket-paste state for the pane
// until it matches want. A pane only reaches 1 after its own process has
// written the DECSET 2004 enable sequence to stdout and tmux's terminal
// emulation has observed it — both async relative to pane startup.
func awaitBracketPasteFlag(t *testing.T, name string, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, err := exec.Command("tmux", "display-message", "-t", name, "-p", "#{bracket_paste_flag}").Output()
		if err == nil {
			last = strings.TrimSpace(string(out))
			if last == want {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("pane bracket_paste_flag never reached %q (last observed: %q)", want, last)
}

// TestIssue1855_MultilinePayload_BracketedWhenPaneRequestsIt is the fix's
// core claim: a short multi-line payload (well under canonicalSafeBytes, so
// pre-fix this went out as a bare `send-keys -l`) against a pane that has
// itself enabled bracketed-paste mode must arrive wrapped in \e[200~...\e[201~
// with the embedded newlines preserved as LF. That envelope is what tells an
// Ink composer the body is pasted text rather than a keystroke stream.
func TestIssue1855_MultilinePayload_BracketedWhenPaneRequestsIt(t *testing.T) {
	payload := "line1\nline2\nline3"
	// \e[200~ + payload + \e[201~ + the trailing Enter (raw mode: \r).
	want := "\x1b[200~" + payload + "\x1b[201~" + "\r"

	sess, out := paneReader(t, "ad1855-bracketed",
		fmt.Sprintf("stty raw -echo; printf '\\033[?2004h'; head -c %d", len(want)))
	awaitDiscipline(t, sess, false)
	awaitBracketPasteFlag(t, sess.Name, "1")

	if err := sess.SendKeysAndEnter(payload); err != nil {
		t.Fatalf("SendKeysAndEnter(%q) to a bracket-paste-aware pane must succeed: %v", payload, err)
	}

	got := string(awaitBytes(out, len(want), 15*time.Second))
	if got != want {
		t.Fatalf("bracket-paste-aware pane received %q, want %q (payload glued together with no bracket "+
			"envelope is exactly issue #1855)", got, want)
	}
}

// TestIssue1855_MultilinePayload_PlainWhenPaneDoesNotRequestIt guards the
// no-op side of -p: a pane that never enabled bracketed-paste mode (every
// non-Ink reader, and any Ink reader that hasn't gotten there yet) must still
// receive the literal bytes with embedded LF preserved — no bracket markers
// bolted on, no LF-to-CR substitution from tmux's other paste-buffer default.
func TestIssue1855_MultilinePayload_PlainWhenPaneDoesNotRequestIt(t *testing.T) {
	payload := "line1\nline2\nline3"
	want := payload + "\r"

	sess, out := paneReader(t, "ad1855-plain", fmt.Sprintf("stty raw -echo; head -c %d", len(want)))
	awaitDiscipline(t, sess, false)
	awaitBracketPasteFlag(t, sess.Name, "0")

	if err := sess.SendKeysAndEnter(payload); err != nil {
		t.Fatalf("SendKeysAndEnter(%q) to a plain pane must succeed: %v", payload, err)
	}

	got := string(awaitBytes(out, len(want), 15*time.Second))
	if got != want {
		t.Fatalf("plain pane received %q, want %q (unwrapped, LF preserved)", got, want)
	}
}

// TestIssue1855_SingleLinePayload_UnaffectedByBracketPasteMode pins that the
// fix is scoped to multi-line content: a single-line payload must stay on the
// fast `send-keys -l` path even against a pane that has enabled bracketed
// paste, since there is no embedded newline for a composer to mishandle.
func TestIssue1855_SingleLinePayload_UnaffectedByBracketPasteMode(t *testing.T) {
	payload := "no newlines here"
	want := payload + "\r"

	sess, out := paneReader(t, "ad1855-singleline",
		fmt.Sprintf("stty raw -echo; printf '\\033[?2004h'; head -c %d", len(want)))
	awaitDiscipline(t, sess, false)
	awaitBracketPasteFlag(t, sess.Name, "1")

	if err := sess.SendKeysAndEnter(payload); err != nil {
		t.Fatalf("SendKeysAndEnter(%q) must succeed: %v", payload, err)
	}

	got := string(awaitBytes(out, len(want), 15*time.Second))
	if got != want {
		t.Fatalf("single-line payload against a bracket-paste-aware pane got %q, want %q unwrapped "+
			"(no newline means nothing for a composer to mishandle, so no envelope is needed)", got, want)
	}
}
