// Regression tests for issue #1855: `session send --message-file` (and every
// other multi-line send) delivers the body with its line structure destroyed.
//
// Reported symptom: a two-line prompt file
//
//	/superpowers:writing-skills
//	Write a skill that writes cupcake flavors and recipes for seeded data instead of Lorem Ipsum.
//
// arrives in Claude Code's composer fused into one line, and the agent answers
// `Unknown command: /superpowers:writing-skillsWrite`.
//
// The newlines are NOT lost in the CLI layer — resolveMessageInput preserves
// interior LFs. They are lost in the tmux transport, and the exact mechanism
// was measured against tmux 3.7b on macOS with a pane running
// `sh -c 'stty raw -echo; printf "\033[?2004h"; cat -vet'`, i.e. a raw-mode app
// that advertises bracketed paste exactly like Claude Code's Ink composer does,
// and which prints `$` for LF, `^M` for CR and `^[` for ESC:
//
//	send-keys -l -- "a\nb"          -> a$ b$          (literal LF, NO paste frame)
//	load-buffer + paste-buffer -d    -> a^Mb^M         (LF rewritten to CR)
//	load-buffer + paste-buffer -p -d -> ^[[200~a^Mb^[[201~   (framed, still CR)
//	load-buffer + paste-buffer -r -d -> a$ b$          (literal LF, NO paste frame)
//	load-buffer + paste-buffer -p -r -d -> ^[[200~a$ b$^[[201~  (framed AND literal LF)
//
// So a multi-line body needs BOTH properties to survive into a TUI composer:
//
//  1. the LF bytes must reach the pane as LF, not CR — a CR is a submit key, so
//     `paste-buffer -d` turns one message into N premature submissions; and
//  2. the body must be framed as a bracketed paste — an unframed bare LF is a
//     keystroke, not text, so the composer drops it (the reporter's fusion) or
//     submits on it, rather than inserting a newline into the buffer.
//
// Today neither transport does both: payloads <= canonicalSafeBytes go out as a
// bare `send-keys -l` (property 2 missing) and larger ones take
// `paste-buffer -d` with neither `-p` nor `-r` (property 1 missing).
//
// These tests assert the delivered BYTE STREAM, not a particular tmux
// incantation, so any transport that gets the bytes right passes.
package tmux

import (
	"bytes"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// tmuxCall is one recorded invocation of the keySenderExec seam: the argv after
// the socket name, plus whatever the caller streamed in on stdin (load-buffer
// reads the payload from stdin rather than argv).
type tmuxCall struct {
	argv  []string
	stdin *bytes.Buffer
}

// recordTransport swaps keySenderExec for a recorder that captures the full
// argv of every tmux invocation and, for `load-buffer`, the bytes streamed to
// it. `load-buffer` is backed by a real `cat` so the payload the production
// code writes to cmd.Stdin is copied into our buffer; everything else is backed
// by `true`, which exits 0 without needing a tmux server.
func recordTransport(t *testing.T) *[]*tmuxCall {
	t.Helper()
	original := keySenderExec
	var mu sync.Mutex
	var calls []*tmuxCall
	keySenderExec = func(socketName string, args ...string) *exec.Cmd {
		call := &tmuxCall{argv: append([]string(nil), args...), stdin: &bytes.Buffer{}}
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		if len(args) > 0 && args[0] == "load-buffer" {
			// cat copies the staged payload from stdin (set by pasteToTarget
			// after we return) into our buffer. cmd.Wait waits for that copy,
			// so the buffer is complete once runSendKeysBounded returns.
			cmd := exec.Command("cat")
			cmd.Stdout = call.stdin
			return cmd
		}
		return exec.Command("true")
	}
	t.Cleanup(func() { keySenderExec = original })
	return &calls
}

func hasFlag(argv []string, flag string) bool {
	for _, a := range argv {
		if a == flag {
			return true
		}
		if a == "--" {
			return false
		}
	}
	return false
}

// flagValue returns the argument following flag, e.g. flagValue(argv, "-b").
func flagValue(argv []string, flag string) string {
	for i, a := range argv {
		if a == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

const (
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// deliveredBytes replays the recorded tmux invocations through tmux's own
// documented (and, in the header comment above, empirically measured) delivery
// semantics and returns the byte stream the pane's application actually sees.
//
// This models tmux, not agent-deck: it is what lets the tests assert the
// property that matters (the composer receives a framed, LF-preserving paste)
// without pinning the fix to one particular tmux command.
func deliveredBytes(calls []*tmuxCall) string {
	buffers := map[string]string{}
	var out strings.Builder
	for _, c := range calls {
		if len(c.argv) == 0 {
			continue
		}
		switch c.argv[0] {
		case "load-buffer":
			buffers[flagValue(c.argv, "-b")] = c.stdin.String()
		case "send-keys":
			if !hasFlag(c.argv, "-l") {
				// A named key (Escape, i, Enter) — not part of the body.
				continue
			}
			// `send-keys -l ... -- <body>`: the body is emitted verbatim, with
			// no bracketed-paste framing, even when the pane app has DECSET
			// 2004 enabled.
			out.WriteString(c.argv[len(c.argv)-1])
		case "paste-buffer":
			body := buffers[flagValue(c.argv, "-b")]
			if !hasFlag(c.argv, "-r") {
				// Without -r tmux replaces every LF with CR.
				body = strings.ReplaceAll(body, "\n", "\r")
			}
			if hasFlag(c.argv, "-p") {
				body = pasteStart + body + pasteEnd
			}
			out.WriteString(body)
		}
	}
	return out.String()
}

// assertMultiLineSurvived is the shared assertion: the body reached the pane
// with its LFs intact and framed as a bracketed paste.
func assertMultiLineSurvived(t *testing.T, delivered, message string) {
	t.Helper()

	if strings.Contains(delivered, "\r") {
		t.Errorf("multi-line body reached the pane with CR (\\r) in it — tmux rewrote the "+
			"newlines, and a CR is a submit key, so the message is chopped into premature "+
			"submissions.\ndelivered: %q", delivered)
	}
	if !strings.Contains(delivered, message) {
		t.Errorf("multi-line body did not reach the pane intact.\n want (substring): %q\n got:             %q",
			message, delivered)
	}
	if !strings.Contains(delivered, pasteStart) || !strings.Contains(delivered, pasteEnd) {
		t.Errorf("multi-line body was NOT framed as a bracketed paste (missing ESC[200~ / ESC[201~). "+
			"An unframed bare LF is a keystroke, not text: the composer drops it and the lines "+
			"fuse (issue #1855).\ndelivered: %q", delivered)
	}
}

// reporterMessage is the exact payload from issue #1855 — 132 bytes, i.e. well
// under canonicalSafeBytes, so it takes the small-payload `send-keys -l` branch
// that the reporter hit.
const reporterMessage = "/superpowers:writing-skills\n" +
	"Write a skill that writes cupcake flavors and recipes for seeded data instead of Lorem Ipsum."

// TestSendKeysAndEnter_SmallMultiLine_PreservesLineStructure is the issue #1855
// regression proper: the reporter's two-line prompt file, sent through the same
// entry point `session send` uses.
//
// Pre-fix: the body goes out as one `send-keys -l`, so it reaches the pane as an
// unframed bare LF → test FAILS on the missing bracketed-paste frame.
func TestSendKeysAndEnter_SmallMultiLine_PreservesLineStructure(t *testing.T) {
	if len(reporterMessage) > canonicalSafeBytes {
		t.Fatalf("fixture no longer exercises the small-payload branch: %d bytes > %d",
			len(reporterMessage), canonicalSafeBytes)
	}

	calls := recordTransport(t)
	s := &Session{Name: "ml-small"}
	if err := s.SendKeysAndEnter(reporterMessage); err != nil {
		t.Fatalf("SendKeysAndEnter returned error: %v", err)
	}

	assertMultiLineSurvived(t, deliveredBytes(*calls), reporterMessage)
}

// TestSendKeysAndEnter_LargeMultiLine_PreservesLineStructure covers the other
// half of the same defect: payloads above canonicalSafeBytes take the paste
// transport, which runs `paste-buffer -d` with neither -p nor -r.
//
// Pre-fix: every LF is rewritten to CR → test FAILS on the CR check.
func TestSendKeysAndEnter_LargeMultiLine_PreservesLineStructure(t *testing.T) {
	var b strings.Builder
	b.WriteString("/superpowers:writing-skills\n")
	for b.Len() <= canonicalSafeBytes {
		b.WriteString("Write a skill that writes cupcake flavors for seeded data.\n")
	}
	message := strings.TrimRight(b.String(), "\n")
	if len(message) <= canonicalSafeBytes {
		t.Fatalf("fixture does not exercise the large-payload branch: %d bytes", len(message))
	}

	calls := recordTransport(t)
	s := &Session{Name: "ml-large"}
	if err := s.SendKeysAndEnter(message); err != nil {
		t.Fatalf("SendKeysAndEnter returned error: %v", err)
	}

	assertMultiLineSurvived(t, deliveredBytes(*calls), message)
}

// TestSendKeysAndEnter_SingleLine_KeepsCheapSendKeysPath guards the fix against
// overreach: single-line sends are the overwhelming majority, they have no line
// structure to protect, and they must keep going out as one plain `send-keys -l`
// rather than paying for a buffer round-trip.
//
// This test PASSES before the fix and must still pass after it.
func TestSendKeysAndEnter_SingleLine_KeepsCheapSendKeysPath(t *testing.T) {
	const message = "just one line, nothing to preserve"

	calls := recordTransport(t)
	s := &Session{Name: "ml-single"}
	if err := s.SendKeysAndEnter(message); err != nil {
		t.Fatalf("SendKeysAndEnter returned error: %v", err)
	}

	var bodyCalls int
	for _, c := range *calls {
		if len(c.argv) > 0 && c.argv[0] == "load-buffer" {
			t.Errorf("single-line send used the paste transport: %v", c.argv)
		}
		if len(c.argv) > 0 && c.argv[0] == "send-keys" && hasFlag(c.argv, "-l") {
			bodyCalls++
			if got := c.argv[len(c.argv)-1]; got != message {
				t.Errorf("single-line body altered: got %q, want %q", got, message)
			}
		}
	}
	if bodyCalls != 1 {
		t.Errorf("single-line send emitted %d literal send-keys calls, want exactly 1", bodyCalls)
	}
}
