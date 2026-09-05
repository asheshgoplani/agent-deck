package ui

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
)

// These tests are the adoption gate for Charm VT. They pin the terminal
// semantics Agent Deck's embedded tmux client depends on instead of merely
// asserting that the dependency compiles.
// RemoteSession: these gate the vendored Charm VT behavior the embedded
// terminal depends on; the emulator has no notion of a session's transport.
func TestCharmVTCompatibilityScreenAndAlternateBuffer(t *testing.T) {
	emu := vt.NewSafeEmulator(20, 4)
	defer func() { _ = emu.Close() }()

	_, _ = emu.WriteString("main界e\u0301")
	if got := emu.String(); !strings.Contains(got, "main界é") {
		t.Fatalf("main screen lost wide/combining graphemes: %q", got)
	}
	_, _ = emu.WriteString("\x1b[?1049h\x1b[38;2;1;2;3mALT\x1b[0m")
	if !emu.IsAltScreen() || !strings.Contains(emu.String(), "ALT") {
		t.Fatalf("alternate screen not active/rendered: alt=%v screen=%q", emu.IsAltScreen(), emu.String())
	}
	if rendered := emu.Render(); !strings.Contains(rendered, "38;2;1;2;3") {
		t.Fatalf("truecolor style missing from rendered cell buffer: %q", rendered)
	}
	_, _ = emu.WriteString("\x1b[?1049l")
	if emu.IsAltScreen() || !strings.Contains(emu.String(), "main界é") {
		t.Fatalf("main screen was not restored: alt=%v screen=%q", emu.IsAltScreen(), emu.String())
	}
}

// awaitReply bounds a wait on the emulator's reply stream so a Charm VT
// regression that stops emitting a reply fails this gate instead of hanging
// the suite.
func awaitReply(t *testing.T, readErr <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-readErr:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s from Charm VT", what)
		return nil
	}
}

func TestCharmVTCompatibilityTerminalRepliesAndResize(t *testing.T) {
	emu := vt.NewSafeEmulator(10, 3)
	defer func() { _ = emu.Close() }()

	want := "\x1b[2;4R"
	reply := make([]byte, len(want))
	readErr := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(emu, reply)
		readErr <- err
	}()
	_, _ = emu.WriteString("\x1b[2;4H\x1b[6n")
	if err := awaitReply(t, readErr, "cursor-position reply"); err != nil {
		t.Fatalf("read cursor-position reply: %v", err)
	}
	if string(reply) != want {
		t.Fatalf("cursor-position reply = %q, want %q", reply, want)
	}

	emu.Resize(32, 8)
	if gotW, gotH := emu.Width(), emu.Height(); gotW != 32 || gotH != 8 {
		t.Fatalf("resize = %dx%d, want 32x8", gotW, gotH)
	}
}

func TestCharmVTCompatibilityBracketedPasteAndApplicationCursor(t *testing.T) {
	emu := vt.NewSafeEmulator(10, 3)
	defer func() { _ = emu.Close() }()
	_, _ = emu.WriteString("\x1b[?2004h")
	wantPaste := bracketedPasteStart + "hello" + bracketedPasteEnd
	paste := make([]byte, len(wantPaste))
	readErr := make(chan error, 1)
	go func() {
		_, err := io.ReadFull(emu, paste)
		readErr <- err
	}()
	emu.Paste("hello")
	if err := awaitReply(t, readErr, "bracketed paste"); err != nil {
		t.Fatalf("read bracketed paste: %v", err)
	}
	if string(paste) != wantPaste {
		t.Fatalf("paste = %q, want %q", paste, wantPaste)
	}
}
