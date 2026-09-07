package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

const maxSessionOutputPayloadBytes = 16 << 20

var errSessionOutputPayloadTooLarge = errors.New("embedded terminal frame exceeds 16 MiB payload limit")

// SessionOutput preserves stdout's file-descriptor identity for Bubble Tea
// and restores the embedded terminal's hardware cursor after every renderer
// write. Bubble Tea v1 has no model-level cursor-position API and otherwise
// parks the cursor on the final row after painting a frame.
//
// Cursor shape and visibility are state transitions, not frame decoration.
// Re-emitting either on every busy-session frame restarts the outer terminal's
// cursor animation and makes the cursor flicker at the session refresh rate.
// Renderer writes therefore restore position only; SetEmbeddedCursor emits
// shape/visibility controls only when that state actually changes. Active
// frames use synchronized output so the hardware cursor never visibly walks
// through Bubble Tea's top-to-bottom repaint before returning to the embedded
// terminal cursor.
type SessionOutput struct {
	*os.File

	mu           sync.Mutex
	active       bool
	rect         terminalCellRect
	cursor       embeddedCursorState
	pointerShape string
}

func NewSessionOutput(stdout *os.File) *SessionOutput {
	return &SessionOutput{File: stdout}
}

func (w *SessionOutput) SetEmbeddedCursor(rect terminalCellRect, cursor embeddedCursorState) {
	w.mu.Lock()
	wasActive := w.active
	oldRect := w.rect
	oldCursor := w.cursor
	w.active = true
	w.rect = rect
	w.cursor = cursor
	// Cursor-only terminal changes (for example, echoing a space or entering
	// an editor mode with Escape) can leave the rendered cell grid byte-for-byte
	// identical. Bubble Tea elides those frames, so place the hardware cursor
	// immediately as well as after the next renderer write.
	if w.File != nil && (!wasActive || oldRect != rect || oldCursor != cursor) {
		shapeChanged := !wasActive || oldCursor.Style != cursor.Style || oldCursor.Steady != cursor.Steady
		visibilityChanged := !wasActive || cursorVisible(oldRect, oldCursor) != cursorVisible(rect, cursor)
		_, _ = w.File.WriteString(w.cursorStateSequenceLocked(shapeChanged, visibilityChanged))
	}
	w.mu.Unlock()
}

// Mouse-pointer shapes for SetPointerShape. These are CSS cursor names, the
// vocabulary OSC 22 uses.
//
// The reset is the literal name "default", not an empty payload. Ghostty
// resolves the payload against its list of CSS cursor names and ignores
// anything that does not match, so `OSC 22 ; BEL` is silently a no-op there —
// which is exactly the shape of "the hand cursor gets stuck": the highlight
// clears, the grab cursor never does, and nothing in the code looks wrong.
const (
	pointerShapeDefault  = "default"
	pointerShapeGrab     = "grab"     // hovering a draggable handle
	pointerShapeGrabbing = "grabbing" // handle currently held
)

// SetPointerShape asks the outer terminal to change the mouse-pointer shape
// via OSC 22, the same escape kitty and Ghostty use. A terminal that does not
// implement it parses the OSC and discards it, so this is a no-op there rather
// than stray output.
//
// It goes through the same mutex and file handle as the embedded cursor
// sequences because it is the same kind of thing: a terminal state transition,
// not frame decoration. Emitting it per frame would be wasteful and could fight
// the renderer, so the last shape is remembered and only transitions are sent.
func (w *SessionOutput) SetPointerShape(shape string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.File == nil || w.pointerShape == shape {
		return
	}
	w.pointerShape = shape
	_, _ = w.File.WriteString("\x1b]22;" + shape + "\a")
}

func (w *SessionOutput) DeactivateEmbeddedCursor() {
	w.mu.Lock()
	wasActive := w.active
	w.active = false
	if wasActive {
		_, _ = w.File.WriteString("\x1b[0 q\x1b[?25l")
	}
	w.mu.Unlock()
}

// ReleaseEmbeddedCursor is the process-exit counterpart to dashboard
// deactivation: restore the outer terminal's default cursor shape and leave
// it visible for the user's shell after Bubble Tea exits.
func (w *SessionOutput) ReleaseEmbeddedCursor() {
	w.mu.Lock()
	w.active = false
	// Also hand the mouse pointer back: a grab cursor left behind on exit
	// follows the user into their shell.
	reset := ""
	if w.pointerShape != "" && w.pointerShape != pointerShapeDefault {
		w.pointerShape = pointerShapeDefault
		reset = "\x1b]22;" + pointerShapeDefault + "\a"
	}
	_, _ = w.File.WriteString("\x1b[0 q\x1b[?25h" + reset)
	w.mu.Unlock()
}

func (w *SessionOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.active {
		return w.File.Write(p)
	}

	// Bubble Tea paints each frame from the screen origin and parks its
	// logical cursor on the last row. If the hardware cursor remains live
	// during that write, busy sessions make it visibly race through the frame
	// at the renderer refresh rate. DEC synchronized output presents the paint
	// plus final embedded-cursor placement atomically, without toggling cursor
	// visibility/shape and restarting its animation on every frame.
	if err := validateSessionOutputPayloadSize(len(p)); err != nil {
		return 0, err
	}
	position := w.cursorPositionSequenceLocked()
	if err := writeSessionOutputString(w.File, ansi.SetModeSynchronizedOutput); err != nil {
		return 0, err
	}
	// Synchronized output is a terminal protocol transaction, not a syscall
	// boundary: writing the bounded payload and framing as separate chunks
	// remains atomic on screen while avoiding an overflow-prone summed
	// allocation capacity.
	written, err := w.File.Write(p)
	if err != nil || written != len(p) {
		_, _ = io.WriteString(w.File, ansi.ResetModeSynchronizedOutput)
		if err == nil {
			err = io.ErrShortWrite
		}
		return written, err
	}
	if err := writeSessionOutputString(w.File, position); err != nil {
		_, _ = io.WriteString(w.File, ansi.ResetModeSynchronizedOutput)
		return len(p), err
	}
	if err := writeSessionOutputString(w.File, ansi.ResetModeSynchronizedOutput); err != nil {
		return len(p), err
	}
	return len(p), nil
}

func validateSessionOutputPayloadSize(size int) error {
	if size < 0 || size > maxSessionOutputPayloadBytes {
		return errSessionOutputPayloadTooLarge
	}
	return nil
}

func writeSessionOutputString(w io.StringWriter, value string) error {
	written, err := w.WriteString(value)
	if err != nil {
		return err
	}
	if written != len(value) {
		return io.ErrShortWrite
	}
	return nil
}

func (w *SessionOutput) cursorStateSequenceLocked(shapeChanged, visibilityChanged bool) string {
	visible := cursorVisible(w.rect, w.cursor)
	var seq string
	if shapeChanged {
		seq += fmt.Sprintf("\x1b[%d q", cursorShape(w.cursor))
	}
	if visible {
		seq += w.cursorPositionSequenceLocked()
	}
	if visibilityChanged {
		if visible {
			seq += "\x1b[?25h"
		} else {
			seq += "\x1b[?25l"
		}
	}
	return seq
}

func (w *SessionOutput) cursorPositionSequenceLocked() string {
	if !cursorVisible(w.rect, w.cursor) {
		return ""
	}
	x := min(max(w.cursor.X, 0), w.rect.Width-1)
	y := min(max(w.cursor.Y, 0), w.rect.Height-1)
	return fmt.Sprintf("\x1b[%d;%dH", w.rect.Y+y+1, w.rect.X+x+1)
}

func cursorVisible(rect terminalCellRect, cursor embeddedCursorState) bool {
	return cursor.Visible && rect.Width > 0 && rect.Height > 0
}

func cursorShape(cursor embeddedCursorState) int {
	shape := 1 // blinking block
	switch cursor.Style {
	case vt.CursorUnderline:
		shape = 3
	case vt.CursorBar:
		shape = 5
	}
	if cursor.Steady {
		shape++
	}
	return shape
}
