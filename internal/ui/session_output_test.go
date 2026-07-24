package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestSessionOutputPlacesAndShapesHardwareCursor(t *testing.T) {
	w := &SessionOutput{
		active: true,
		rect:   terminalCellRect{X: 20, Y: 4, Width: 40, Height: 12},
		cursor: embeddedCursorState{X: 3, Y: 2, Visible: true, Style: vt.CursorBar, Steady: true},
	}
	seq := w.cursorStateSequenceLocked(true, true)
	for _, want := range []string{"\x1b[6 q", "\x1b[7;24H", "\x1b[?25h"} {
		if !strings.Contains(seq, want) {
			t.Fatalf("cursor sequence %q missing %q", seq, want)
		}
	}

	w.cursor.Visible = false
	if got := w.cursorStateSequenceLocked(false, true); got != "\x1b[?25l" {
		t.Fatalf("hidden cursor sequence = %q", got)
	}
}

func TestSessionOutputPlacesCursorWithoutRendererWrite(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "cursor-output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()

	w := NewSessionOutput(output)
	w.SetEmbeddedCursor(
		terminalCellRect{X: 10, Y: 3, Width: 20, Height: 6},
		embeddedCursorState{X: 4, Y: 1, Visible: true, Style: vt.CursorBlock},
	)
	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\x1b[1 q", "\x1b[5;15H", "\x1b[?25h"} {
		if !strings.Contains(string(written), want) {
			t.Fatalf("immediate cursor output %q missing %q", written, want)
		}
	}
}

func TestSessionOutputDoesNotRestartCursorBlinkOnBusyFrames(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "cursor-output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()

	w := NewSessionOutput(output)
	rect := terminalCellRect{X: 10, Y: 3, Width: 20, Height: 6}
	cursor := embeddedCursorState{X: 4, Y: 1, Visible: true, Style: vt.CursorBlock}
	w.SetEmbeddedCursor(rect, cursor)
	// A dirty PTY wakes the model before every rendered frame. An unchanged
	// cursor must not emit an eager update, and renderer writes must restore
	// only its position; repeated DECSCUSR/DECTCEM controls restart cursor
	// animation in outer terminals and caused output-rate flicker.
	w.SetEmbeddedCursor(rect, cursor)
	if _, err := w.Write([]byte("frame one")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("frame two")); err != nil {
		t.Fatal(err)
	}

	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	got := string(written)
	if count := strings.Count(got, "\x1b[1 q"); count != 1 {
		t.Fatalf("cursor shape emitted %d times, want once: %q", count, got)
	}
	if count := strings.Count(got, "\x1b[?25h"); count != 1 {
		t.Fatalf("cursor show emitted %d times, want once: %q", count, got)
	}
	if count := strings.Count(got, "\x1b[5;15H"); count != 3 {
		t.Fatalf("cursor position emitted %d times, want activation plus two frames: %q", count, got)
	}
	if count := strings.Count(got, ansi.SetModeSynchronizedOutput); count != 2 {
		t.Fatalf("synchronized frame start emitted %d times, want twice: %q", count, got)
	}
	if count := strings.Count(got, ansi.ResetModeSynchronizedOutput); count != 2 {
		t.Fatalf("synchronized frame end emitted %d times, want twice: %q", count, got)
	}
	for _, frame := range []string{"frame one", "frame two"} {
		want := ansi.SetModeSynchronizedOutput + frame + "\x1b[5;15H" + ansi.ResetModeSynchronizedOutput
		if !strings.Contains(got, want) {
			t.Fatalf("renderer frame was not committed atomically with cursor placement; missing %q in %q", want, got)
		}
	}
}

func TestSessionOutputEmitsOnlyChangedCursorState(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "cursor-output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()

	w := NewSessionOutput(output)
	rect := terminalCellRect{Width: 20, Height: 6}
	w.SetEmbeddedCursor(rect, embeddedCursorState{Visible: true, Style: vt.CursorBlock})
	w.SetEmbeddedCursor(rect, embeddedCursorState{X: 1, Visible: true, Style: vt.CursorBlock})
	w.SetEmbeddedCursor(rect, embeddedCursorState{X: 1, Visible: true, Style: vt.CursorBar, Steady: true})
	w.SetEmbeddedCursor(rect, embeddedCursorState{X: 1, Visible: false, Style: vt.CursorBar, Steady: true})

	if _, err := output.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	got := string(written)
	if count := strings.Count(got, "\x1b[?25h"); count != 1 {
		t.Fatalf("cursor show emitted %d times, want activation only: %q", count, got)
	}
	if count := strings.Count(got, "\x1b[?25l"); count != 1 {
		t.Fatalf("cursor hide emitted %d times, want visibility transition only: %q", count, got)
	}
	if count := strings.Count(got, " q"); count != 2 {
		t.Fatalf("cursor shape emitted %d times, want activation plus style transition: %q", count, got)
	}
}
