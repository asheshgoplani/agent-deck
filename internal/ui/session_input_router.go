package ui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	bracketedPasteStart         = "\x1b[200~"
	bracketedPasteEnd           = "\x1b[201~"
	embeddedSidebarToggleSignal = byte(0x07) // private Bubble Tea Ctrl+G signal
)

// terminalCellRect describes the child terminal's location in the outer
// terminal. X and Y are zero-based; Width and Height are measured in cells.
type terminalCellRect struct {
	X, Y          int
	Width, Height int
}

func (r terminalCellRect) contains(x, y int) bool {
	return x >= r.X && x < r.X+r.Width && y >= r.Y && y < r.Y+r.Height
}

// SessionInputRouter keeps Bubble Tea as the owner of the real stdin file
// descriptor while allowing an embedded tmux client to receive the original
// byte stream. In dashboard mode it uses the existing compatibility reader;
// in session mode it bypasses Bubble Tea entirely except for Ctrl+Q, the
// configured session-switch key, and mouse events outside the embedded cell
// rectangle.
type SessionInputRouter struct {
	*os.File // preserve Fd() so Bubble Tea can put stdin into raw mode
	normal   *csiuReader

	mu     sync.RWMutex
	active bool
	child  io.Writer
	rect   terminalCellRect
	// switchByte is the configured portable Ctrl+<key> chord that returns an
	// embedded local session to Bubble Tea's MRU switcher. Zero disables it
	// (including remote sessions, whose switcher path is not local-tmux based).
	switchByte byte
	pending    []byte
	rawBuf     []byte
	inPaste    bool
}

func NewSessionInputRouter(stdin *os.File) *SessionInputRouter {
	return &SessionInputRouter{
		File: stdin,
		// This translator consumes bytes only after the router's single raw
		// read has established that they arrived in dashboard mode.
		normal: newCSIuReader(nil),
		rawBuf: make([]byte, 0, 256),
	}
}

func (r *SessionInputRouter) Activate(child io.Writer, rect terminalCellRect, switchByte byte) {
	r.mu.Lock()
	r.active = child != nil
	r.child = child
	r.rect = rect
	r.switchByte = switchByte
	r.rawBuf = r.rawBuf[:0]
	r.inPaste = false
	r.mu.Unlock()
}

func (r *SessionInputRouter) Deactivate() {
	r.mu.Lock()
	r.deactivateLocked()
	r.mu.Unlock()
}

func (r *SessionInputRouter) deactivateLocked() {
	r.active = false
	r.child = nil
	r.switchByte = 0
	r.rawBuf = r.rawBuf[:0]
	r.inPaste = false
}

func (r *SessionInputRouter) UpdateRect(rect terminalCellRect) {
	r.mu.Lock()
	r.rect = rect
	r.mu.Unlock()
}

func (r *SessionInputRouter) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		r.mu.Lock()
		if len(r.pending) > 0 {
			n := copy(p, r.pending)
			r.pending = r.pending[n:]
			r.mu.Unlock()
			return n, nil
		}
		r.mu.Unlock()

		buf := make([]byte, max(256, len(p)))
		n, err := r.File.Read(buf)
		if n == 0 {
			return 0, err
		}

		r.mu.Lock()
		if !r.active {
			translated := r.normal.consume(buf[:n], err != nil)
			if len(translated) > 0 {
				copied := copy(p, translated)
				if copied < len(translated) {
					r.pending = append(r.pending, translated[copied:]...)
				}
				r.mu.Unlock()
				return copied, nil
			}
			standaloneEscape := len(r.normal.inBuf) == 1 && r.normal.inBuf[0] == 0x1b
			r.mu.Unlock()
			if err != nil {
				return 0, err
			}
			// Match csiuReader's ncurses-style ESC delay without allowing that
			// compatibility reader to own a blocking raw read across activation.
			if standaloneEscape &&
				!pollFdReady(int(r.File.Fd()), 50*time.Millisecond) {
				r.mu.Lock()
				translated = r.normal.consume(nil, true)
				copied := copy(p, translated)
				if copied < len(translated) {
					r.pending = append(r.pending, translated[copied:]...)
				}
				r.mu.Unlock()
				return copied, nil
			}
			continue
		}
		r.rawBuf = append(r.rawBuf, buf[:n]...)
		toDashboard, routeErr := r.routeEmbeddedLocked(err != nil)
		if len(toDashboard) > 0 {
			copied := copy(p, toDashboard)
			if copied < len(toDashboard) {
				r.pending = append(r.pending, toDashboard[copied:]...)
			}
			r.mu.Unlock()
			return copied, nil
		}
		partial := len(r.rawBuf) > 0
		r.mu.Unlock()
		if routeErr != nil {
			return 0, routeErr
		}
		// A lone Escape is both a valid key and the prefix of every CSI/SS3
		// token. Give the kernel the conventional short ESC-delay window to
		// deliver the rest of a split sequence, then forward it as-is rather
		// than blocking until the user's next keypress.
		if partial && !pollFdReady(int(r.File.Fd()), 50*time.Millisecond) {
			r.mu.Lock()
			toDashboard, routeErr = r.routeEmbeddedLocked(true)
			if len(toDashboard) > 0 {
				copied := copy(p, toDashboard)
				if copied < len(toDashboard) {
					r.pending = append(r.pending, toDashboard[copied:]...)
				}
				r.mu.Unlock()
				return copied, routeErr
			}
			r.mu.Unlock()
			if routeErr != nil {
				return 0, routeErr
			}
		}
	}
}

// routeEmbeddedLocked consumes complete tokens from rawBuf. It returns bytes
// that should go back through Bubble Tea (detach and out-of-pane mouse).
func (r *SessionInputRouter) routeEmbeddedLocked(final bool) ([]byte, error) {
	var child bytes.Buffer
	var dashboard bytes.Buffer
	data := r.rawBuf
	i := 0

	flushChild := func() error {
		if child.Len() == 0 || r.child == nil {
			return nil
		}
		_, err := r.child.Write(child.Bytes())
		child.Reset()
		return err
	}

	for i < len(data) {
		if r.inPaste {
			if end := bytes.Index(data[i:], []byte(bracketedPasteEnd)); end >= 0 {
				end += i + len(bracketedPasteEnd)
				child.Write(data[i:end])
				i = end
				r.inPaste = false
				continue
			}
			keep := 0
			if !final {
				keep = longestSuffixPrefix(data[i:], []byte(bracketedPasteEnd))
			}
			child.Write(data[i : len(data)-keep])
			i = len(data) - keep
			break
		}

		if bytes.HasPrefix(data[i:], []byte(bracketedPasteStart)) {
			child.WriteString(bracketedPasteStart)
			i += len(bracketedPasteStart)
			r.inPaste = true
			continue
		}
		if !final && isIncompletePrefix(data[i:], []byte(bracketedPasteStart)) {
			break
		}

		if detachLen := ctrlQSequenceLen(data[i:]); detachLen > 0 {
			if err := flushChild(); err != nil {
				return nil, err
			}
			dashboard.WriteByte(0x11)
			// Match the full-screen attach path: bytes coalesced after the
			// detach chord are discarded so they cannot become accidental
			// dashboard hotkeys, and never leak to the dying client.
			i = len(data)
			r.deactivateLocked()
			break
		}
		if !final && couldBeCtrlQPrefix(data[i:]) {
			break
		}

		if switchLen := controlSequenceLen(data[i:], r.switchByte); switchLen > 0 {
			if err := flushChild(); err != nil {
				return nil, err
			}
			// Normalize enhanced keyboard encodings back to the configured raw
			// control byte so Bubble Tea receives the same key in every terminal.
			dashboard.WriteByte(r.switchByte)
			// Match full-screen attach: discard bytes coalesced after the switch
			// chord and stop routing into the client that Home is about to close.
			i = len(data)
			r.deactivateLocked()
			break
		}
		if !final && couldBeControlSequencePrefix(data[i:], r.switchByte) {
			break
		}

		if toggleLen := sidebarToggleSequenceLen(data[i:]); toggleLen > 0 {
			if err := flushChild(); err != nil {
				return nil, err
			}
			// The raw Ctrl+Alt+B chord is deliberately consumed. Ctrl+G is a
			// private signal to Home; a literal Ctrl+G still goes to the pane.
			dashboard.WriteByte(embeddedSidebarToggleSignal)
			i += toggleLen
			continue
		}
		if !final && couldBeSidebarTogglePrefix(data[i:]) {
			break
		}

		if bytes.HasPrefix(data[i:], []byte("\x1b[<")) {
			end := mouseSequenceEnd(data[i:])
			if end <= 0 {
				if !final {
					if end < 0 {
						child.WriteByte(data[i])
						i++
						continue
					}
					break
				}
				child.WriteByte(data[i])
				i++
				continue
			}
			seq := data[i : i+end]
			translated, inside, ok := translateSGRMouse(seq, r.rect)
			switch {
			case ok && inside:
				child.Write(translated)
			case ok:
				if err := flushChild(); err != nil {
					return nil, err
				}
				dashboard.Write(seq)
			default:
				child.Write(seq)
			}
			i += end
			continue
		}

		child.WriteByte(data[i])
		i++
	}

	r.rawBuf = append(r.rawBuf[:0], data[i:]...)
	if err := flushChild(); err != nil {
		return dashboard.Bytes(), err
	}
	return dashboard.Bytes(), nil
}

func ctrlQSequenceLen(data []byte) int {
	return controlSequenceLen(data, 0x11)
}

func couldBeCtrlQPrefix(data []byte) bool {
	return couldBeControlSequencePrefix(data, 0x11)
}

func controlSequenceLen(data []byte, controlByte byte) int {
	if controlByte == 0 || len(data) == 0 {
		return 0
	}
	if data[0] == controlByte {
		return 1
	}
	for _, seq := range encodedControlSequences(controlByte) {
		if bytes.HasPrefix(data, []byte(seq)) {
			return len(seq)
		}
	}
	return 0
}

func couldBeControlSequencePrefix(data []byte, controlByte byte) bool {
	if controlByte == 0 {
		return false
	}
	for _, seq := range encodedControlSequences(controlByte) {
		if isIncompletePrefix(data, []byte(seq)) {
			return true
		}
	}
	return false
}

func encodedControlSequences(controlByte byte) []string {
	var keyCode byte
	switch {
	case controlByte >= 1 && controlByte <= 26:
		keyCode = controlByte + 96 // Ctrl+A..Z -> a..z
	case controlByte >= 28 && controlByte <= 31:
		keyCode = controlByte + 64 // Ctrl+\\, ], ^, _
	default:
		return nil
	}
	return []string{
		fmt.Sprintf("\x1b[27;5;%d~", keyCode),
		fmt.Sprintf("\x1b[%d;5u", keyCode),
	}
}

// sidebarToggleSequenceLen recognizes Ctrl+Alt+B across the keyboard
// protocols used by Ghostty, xterm/tmux, and legacy terminals. Uppercase
// codepoint variants cover terminals that preserve the shifted key label.
func sidebarToggleSequenceLen(data []byte) int {
	for _, seq := range []string{
		"\x1b\x02",      // legacy Alt + Ctrl+B
		"\x1b[98;7u",    // Kitty CSI-u Ctrl+Alt+b
		"\x1b[66;8u",    // Kitty CSI-u Ctrl+Alt+Shift+B
		"\x1b[27;7;98~", // xterm modifyOtherKeys Ctrl+Alt+b
		"\x1b[27;8;66~", // xterm modifyOtherKeys Ctrl+Alt+Shift+B
	} {
		if bytes.HasPrefix(data, []byte(seq)) {
			return len(seq)
		}
	}
	return 0
}

func couldBeSidebarTogglePrefix(data []byte) bool {
	for _, seq := range []string{
		"\x1b\x02",
		"\x1b[98;7u",
		"\x1b[66;8u",
		"\x1b[27;7;98~",
		"\x1b[27;8;66~",
	} {
		if isIncompletePrefix(data, []byte(seq)) {
			return true
		}
	}
	return false
}

func isIncompletePrefix(data, complete []byte) bool {
	return len(data) < len(complete) && bytes.Equal(data, complete[:len(data)])
}

func longestSuffixPrefix(data, marker []byte) int {
	limit := min(len(data), len(marker)-1)
	for n := limit; n > 0; n-- {
		if bytes.Equal(data[len(data)-n:], marker[:n]) {
			return n
		}
	}
	return 0
}

func mouseSequenceEnd(data []byte) int {
	for i := 3; i < len(data); i++ {
		if data[i] == 'M' || data[i] == 'm' {
			return i + 1
		}
		if (data[i] < '0' || data[i] > '9') && data[i] != ';' {
			return -1
		}
	}
	return 0
}

func translateSGRMouse(seq []byte, rect terminalCellRect) ([]byte, bool, bool) {
	if len(seq) < 7 || !bytes.HasPrefix(seq, []byte("\x1b[<")) {
		return nil, false, false
	}
	action := seq[len(seq)-1]
	parts := strings.Split(string(seq[3:len(seq)-1]), ";")
	if len(parts) != 3 {
		return nil, false, false
	}
	button, err1 := strconv.Atoi(parts[0])
	x1, err2 := strconv.Atoi(parts[1])
	y1, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || x1 < 1 || y1 < 1 {
		return nil, false, false
	}
	x0, y0 := x1-1, y1-1
	if !rect.contains(x0, y0) {
		return seq, false, true
	}
	return []byte(fmt.Sprintf("\x1b[<%d;%d;%d%c", button, x0-rect.X+1, y0-rect.Y+1, action)), true, true
}
