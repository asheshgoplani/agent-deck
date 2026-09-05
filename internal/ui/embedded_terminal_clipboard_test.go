package ui

import (
	"encoding/base64"
	"testing"
)

func TestEmbeddedTerminalOSC52CopiesToHostClipboard(t *testing.T) {
	want := "selected in codex or a shell"
	var got string

	emulator := newEmbeddedTerminalEmulator(
		embeddedTerminalSize{Cols: 40, Rows: 8},
		func(text string) { got = text },
	)
	defer func() { _ = emulator.Close() }()

	encoded := base64.StdEncoding.EncodeToString([]byte(want))
	_, _ = emulator.WriteString("\x1b]52;c;" + encoded + "\x07")

	if got != want {
		t.Fatalf("host clipboard payload = %q, want %q", got, want)
	}
}

func TestEmbeddedTerminalOSC52RejectsMalformedPayload(t *testing.T) {
	called := false
	emulator := newEmbeddedTerminalEmulator(
		embeddedTerminalSize{Cols: 40, Rows: 8},
		func(string) { called = true },
	)
	defer func() { _ = emulator.Close() }()

	_, _ = emulator.WriteString("\x1b]52;c;not-base64!\x07")

	if called {
		t.Fatal("malformed OSC 52 payload reached the host clipboard")
	}
}

func TestEmbeddedTerminalOSC52IgnoresClipboardQueries(t *testing.T) {
	called := false
	emulator := newEmbeddedTerminalEmulator(
		embeddedTerminalSize{Cols: 40, Rows: 8},
		func(string) { called = true },
	)
	defer func() { _ = emulator.Close() }()

	// "?" asks the terminal to report its clipboard; the embedded host never
	// answers that, and it must not be mistaken for a copy.
	_, _ = emulator.WriteString("\x1b]52;c;?\x07")

	if called {
		t.Fatal("clipboard query reached the host clipboard as a copy")
	}
}

func TestEmbeddedClipboardQueueKeepsNewestPendingSelection(t *testing.T) {
	requests := make(chan string, 1)
	enqueueLatestClipboardSelection(requests, "older selection")
	enqueueLatestClipboardSelection(requests, "newer selection")

	if got := <-requests; got != "newer selection" {
		t.Fatalf("pending clipboard selection = %q, want newest selection", got)
	}
}
