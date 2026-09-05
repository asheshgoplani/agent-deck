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
	// child is the embedded PTY. It is nil between Prepare and Activate, the
	// interval in which Home is still opening the tmux client; bytes routed to
	// the pane during that interval wait in held.
	child io.Writer
	// held collects pane-bound bytes that arrived before the child existed, or
	// while Activate was still draining an earlier batch, so the first thing
	// the client reads is everything the user typed, in order.
	held []byte
	// draining is set while Activate writes held bytes outside the lock; Read
	// keeps appending to held until it clears so the two cannot interleave.
	draining bool
	rect     terminalCellRect
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

// Prepare switches the router into session mode before the child PTY exists.
// Home calls it the moment Enter is accepted, so keystrokes read while the
// tmux client is still connecting are held for the pane instead of reaching
// Bubble Tea, where they would be dashboard hotkeys or, in embedded mode,
// silently dropped. Ctrl+Q, the switch chord, and out-of-pane mouse still
// return to the dashboard during this interval.
func (r *SessionInputRouter) Prepare(rect terminalCellRect, switchByte byte) {
	r.mu.Lock()
	r.active = true
	r.child = nil
	r.rect = rect
	r.switchByte = switchByte
	r.rawBuf = r.rawBuf[:0]
	r.held = r.held[:0]
	r.draining = false
	r.inPaste = false
	r.mu.Unlock()
}

// Activate installs the child PTY. Bytes held since Prepare are written to it
// first, outside the lock; Read keeps holding new pane bytes until that drain
// completes, so the client sees the user's keystrokes in the order typed.
func (r *SessionInputRouter) Activate(child io.Writer, rect terminalCellRect, switchByte byte) {
	r.mu.Lock()
	wasActive := r.active
	r.active = child != nil
	r.child = child
	r.rect = rect
	r.switchByte = switchByte
	if !wasActive {
		// A fresh activation (no Prepare) starts from a clean token stream.
		// After Prepare the partial token in rawBuf belongs to the session.
		r.rawBuf = r.rawBuf[:0]
		r.inPaste = false
		r.held = r.held[:0]
	}
	if child == nil {
		r.held = r.held[:0]
		r.draining = false
		r.mu.Unlock()
		return
	}
	r.draining = true
	r.mu.Unlock()
	r.drainHeld(child)
}

func (r *SessionInputRouter) drainHeld(child io.Writer) {
	for {
		r.mu.Lock()
		if len(r.held) == 0 || r.child != child {
			r.held = r.held[:0]
			r.draining = false
			r.mu.Unlock()
			return
		}
		batch := append([]byte(nil), r.held...)
		r.held = r.held[:0]
		r.mu.Unlock()
		writeChild(child, batch)
	}
}

// Forward hands the router bytes that Bubble Tea has already parsed as key
// events before session mode began. When Enter and the following keystrokes
// share one stdin read, the router translates the whole read for the
// dashboard, so those keys arrive at Home as messages after the mode switch.
// Home re-encodes them and forwards them here rather than dropping them.
func (r *SessionInputRouter) Forward(raw []byte) {
	if len(raw) == 0 {
		return
	}
	r.mu.Lock()
	if !r.active {
		r.mu.Unlock()
		return
	}
	if r.child == nil || r.draining {
		r.held = append(r.held, raw...)
		r.mu.Unlock()
		return
	}
	child := r.child
	r.mu.Unlock()
	writeChild(child, raw)
}

// writeChild delivers pane bytes with no router lock held. A PTY write can
// block when the client stops reading, and it fails once the client has gone;
// neither may stall Activate/Deactivate/UpdateRect on the Bubble Tea goroutine
// or surface as a stdin error that ends the dashboard's input loop. Home
// notices the exited client through embeddedFrameMsg and detaches.
func writeChild(child io.Writer, data []byte) {
	if child == nil || len(data) == 0 {
		return
	}
	_, _ = child.Write(data)
}

func (r *SessionInputRouter) Deactivate() {
	r.mu.Lock()
	r.deactivateLocked()
	r.mu.Unlock()
}

func (r *SessionInputRouter) deactivateLocked() {
	r.active = false
	r.child = nil
	r.held = r.held[:0]
	r.draining = false
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
		child := r.child
		toDashboard, toChild := r.routeEmbeddedLocked(err != nil)
		if len(toDashboard) > 0 {
			copied := copy(p, toDashboard)
			if copied < len(toDashboard) {
				r.pending = append(r.pending, toDashboard[copied:]...)
			}
			r.mu.Unlock()
			writeChild(child, toChild)
			return copied, nil
		}
		partial := len(r.rawBuf) > 0
		r.mu.Unlock()
		writeChild(child, toChild)
		// A lone Escape is both a valid key and the prefix of every CSI/SS3
		// token. Give the kernel the conventional short ESC-delay window to
		// deliver the rest of a split sequence, then forward it as-is rather
		// than blocking until the user's next keypress.
		if partial && !pollFdReady(int(r.File.Fd()), 50*time.Millisecond) {
			r.mu.Lock()
			child = r.child
			toDashboard, toChild = r.routeEmbeddedLocked(true)
			if len(toDashboard) > 0 {
				copied := copy(p, toDashboard)
				if copied < len(toDashboard) {
					r.pending = append(r.pending, toDashboard[copied:]...)
				}
				r.mu.Unlock()
				writeChild(child, toChild)
				return copied, nil
			}
			r.mu.Unlock()
			writeChild(child, toChild)
		}
	}
}

// routeEmbeddedLocked consumes complete tokens from rawBuf. It returns bytes
// that should go back through Bubble Tea (detach and out-of-pane mouse) and
// bytes for the pane. The caller writes the pane bytes after releasing r.mu;
// every pane byte precedes any dashboard byte in the stream, so writing them
// first and then returning the dashboard bytes preserves the typed order.
// While the child does not exist yet (Prepare) or Activate is still draining,
// pane bytes are appended to held instead and nothing is returned for it.
func (r *SessionInputRouter) routeEmbeddedLocked(final bool) (toDashboard, toChild []byte) {
	var child bytes.Buffer
	var dashboard bytes.Buffer
	data := r.rawBuf
	i := 0
	holdForChild := r.child == nil || r.draining

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
	if holdForChild {
		// A detach or switch chord while connecting already cleared held via
		// deactivateLocked; do not resurrect bytes for a client Home is
		// about to abandon.
		if r.active {
			r.held = append(r.held, child.Bytes()...)
		}
		return dashboard.Bytes(), nil
	}
	return dashboard.Bytes(), child.Bytes()
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
