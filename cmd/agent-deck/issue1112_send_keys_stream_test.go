// Issue #1112 — bug 2 CLI side: `agent-deck session send-keys <id>
// --stream` reads stdin lines until EOF and dispatches each to the
// session's tmux pane. These tests verify the wire-format parser
// matches the StreamingRemoteKeySender's emitter shape
// (internal/session/remote_keysender.go) — if either drifts, the
// remote loop silently ignores commands and the user's typing
// disappears.

package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// recordingSink captures every dispatch the stream loop performs.
type recordingSink struct {
	texts  []string
	named  []string
	enters int
	errOn  string // when non-empty, error on the first verb matching this prefix
}

func (r *recordingSink) SendKeys(text string) error {
	if r.errOn == "T" {
		r.errOn = ""
		return errors.New("forced")
	}
	r.texts = append(r.texts, text)
	return nil
}
func (r *recordingSink) SendNamedKey(key string) error {
	if r.errOn == "N" {
		r.errOn = ""
		return errors.New("forced")
	}
	r.named = append(r.named, key)
	return nil
}
func (r *recordingSink) SendEnter() error {
	if r.errOn == "E" {
		r.errOn = ""
		return errors.New("forced")
	}
	r.enters++
	return nil
}

// hexLine builds a "T <hex>\n" line so tests stay readable.
func hexLine(verb, payload string) string {
	switch verb {
	case "T":
		return "T " + hex.EncodeToString([]byte(payload)) + "\n"
	case "N":
		return "N " + payload + "\n"
	case "E":
		return "E\n"
	}
	panic("unknown verb")
}

// TestIssue1112_runSendKeysStream_HappyPath verifies the parser
// dispatches a realistic mixed sequence in order.
func TestIssue1112_runSendKeysStream_HappyPath(t *testing.T) {
	var input bytes.Buffer
	input.WriteString(hexLine("T", "hi"))
	input.WriteString(hexLine("N", "Down"))
	input.WriteString(hexLine("E", ""))
	input.WriteString(hexLine("T", "bye"))

	sink := &recordingSink{}
	if err := runSendKeysStream(&input, sink); err != nil {
		t.Fatalf("runSendKeysStream: %v", err)
	}
	if got, want := sink.texts, []string{"hi", "bye"}; !equalStrings(got, want) {
		t.Errorf("texts = %v, want %v", got, want)
	}
	if got, want := sink.named, []string{"Down"}; !equalStrings(got, want) {
		t.Errorf("named = %v, want %v", got, want)
	}
	if sink.enters != 1 {
		t.Errorf("enters = %d, want 1", sink.enters)
	}
}

// TestIssue1112_runSendKeysStream_IgnoresBlanksAndUnknownVerbs covers
// the forward-compatibility guarantee: blank lines and lines starting
// with verbs the current binary doesn't know must NOT halt the loop.
// Otherwise an older remote agent-deck would brick the moment the
// local sender added a new verb.
func TestIssue1112_runSendKeysStream_IgnoresBlanksAndUnknownVerbs(t *testing.T) {
	var input bytes.Buffer
	input.WriteString("\n")            // blank
	input.WriteString("X something\n") // unknown verb
	input.WriteString("# comment\n")   // also unknown
	input.WriteString(hexLine("T", "ok"))

	sink := &recordingSink{}
	if err := runSendKeysStream(&input, sink); err != nil {
		t.Fatalf("runSendKeysStream: %v", err)
	}
	if got, want := sink.texts, []string{"ok"}; !equalStrings(got, want) {
		t.Errorf("texts = %v, want %v — unknown verbs must not halt stream", got, want)
	}
}

// TestIssue1112_runSendKeysStream_BinarySafeText round-trips bytes
// that would corrupt a text-only protocol (newlines, NULL, high-bit).
func TestIssue1112_runSendKeysStream_BinarySafeText(t *testing.T) {
	payload := "line1\nline2\x00\xff"
	var input bytes.Buffer
	input.WriteString(hexLine("T", payload))

	sink := &recordingSink{}
	if err := runSendKeysStream(&input, sink); err != nil {
		t.Fatalf("runSendKeysStream: %v", err)
	}
	if len(sink.texts) != 1 || sink.texts[0] != payload {
		t.Errorf("texts = %q, want [%q]", sink.texts, payload)
	}
}

// TestIssue1112_runSendKeysStream_InvalidHex surfaces a malformed
// payload as an error — the local sender shouldn't have produced it,
// but if it did we want a loud failure rather than silent garbage.
func TestIssue1112_runSendKeysStream_InvalidHex(t *testing.T) {
	var input bytes.Buffer
	input.WriteString("T zzznotahex\n")

	sink := &recordingSink{}
	if err := runSendKeysStream(&input, sink); err == nil {
		t.Fatal("expected error for invalid hex")
	} else if !strings.Contains(err.Error(), "invalid hex") {
		t.Errorf("error = %v, want one mentioning 'invalid hex'", err)
	}
}

// TestIssue1112_runSendKeysStream_DispatchErrorPropagates verifies a
// per-keystroke error doesn't get silently swallowed.
func TestIssue1112_runSendKeysStream_DispatchErrorPropagates(t *testing.T) {
	var input bytes.Buffer
	input.WriteString(hexLine("T", "x"))

	sink := &recordingSink{errOn: "T"}
	if err := runSendKeysStream(&input, sink); err == nil {
		t.Fatal("expected error")
	}
}

// TestIssue1112_runSendKeysStream_HighThroughput sanity-checks the
// parser's overhead: 1000 lines through the loop must complete in
// well under a second. This is the inverse of the perf test in
// internal/session — we want the receiver to be cheap too.
func TestIssue1112_runSendKeysStream_HighThroughput(t *testing.T) {
	var input bytes.Buffer
	for i := 0; i < 1000; i++ {
		input.WriteString(hexLine("T", fmt.Sprintf("k%d", i)))
	}
	sink := &recordingSink{}
	if err := runSendKeysStream(&input, sink); err != nil {
		t.Fatalf("runSendKeysStream: %v", err)
	}
	if len(sink.texts) != 1000 {
		t.Errorf("dispatched %d, want 1000", len(sink.texts))
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
