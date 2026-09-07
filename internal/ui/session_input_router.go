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
	// out carries every pane-bound byte to the embedded PTY. Prepare (or
	// Activate) installs it; between Prepare and Activate it has no child yet
	// and simply queues what the user types. Deactivation closes it but leaves
	// it in place, still delivering what was queued, until the next Prepare or
	// Activate replaces it.
	out  *paneWriter
	rect terminalCellRect
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
	if r.out != nil {
		// A client Home is abandoning never reads what was queued for it.
		r.out.close(false)
	}
	r.out = newPaneWriter()
	r.rect = rect
	r.switchByte = switchByte
	r.rawBuf = r.rawBuf[:0]
	r.inPaste = false
	r.mu.Unlock()
}

// Activate installs the child PTY. Bytes queued since Prepare reach it first,
// then everything routed afterwards, in the order typed; the delivery runs on
// the pane writer's goroutine, so neither this call nor Read waits on the PTY.
func (r *SessionInputRouter) Activate(child io.Writer, rect terminalCellRect, switchByte byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	wasActive := r.active
	r.active = child != nil
	r.rect = rect
	r.switchByte = switchByte
	if !wasActive {
		// A fresh activation (no Prepare) starts from a clean token stream.
		// After Prepare the partial token in rawBuf belongs to the session.
		r.rawBuf = r.rawBuf[:0]
		r.inPaste = false
	}
	if child == nil {
		if r.out != nil {
			r.out.close(false)
			r.out = nil
		}
		return
	}
	if r.out != nil && (r.out.isClosed() || (r.out.hasChild() && !r.out.writesTo(child))) {
		// A closed writer is finishing an earlier client's bytes; replacing
		// one live client with another leaves the old client's queue its own.
		// Either way the new client starts empty.
		r.out.close(true)
		r.out = nil
	}
	if r.out == nil {
		r.out = newPaneWriter()
	}
	r.out.attach(child)
}

// enqueuePaneLocked queues pane bytes for delivery. Called with r.mu held. A
// router that is active without Prepare (tests, or Activate alone) gets its
// writer lazily; the bytes wait there until a child is attached.
func (r *SessionInputRouter) enqueuePaneLocked(data []byte) {
	if len(data) == 0 || !r.active {
		return
	}
	if r.out == nil || r.out.isClosed() {
		r.out = newPaneWriter()
	}
	r.out.enqueue(data)
}

// hasChild reports whether a client PTY is installed; false during the
// Prepare..Activate interval and in dashboard mode.
func (r *SessionInputRouter) hasChild() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.out != nil && r.out.hasChild()
}

// paneIdle reports whether every queued pane byte has been handed to the
// child (bytes parked while no child exists count as idle). Tests use it to
// wait for the asynchronous delivery.
func (r *SessionInputRouter) paneIdle() bool {
	r.mu.RLock()
	out := r.out
	r.mu.RUnlock()
	return out == nil || out.idle()
}

// Forward hands the router bytes that Bubble Tea has already parsed as key
// events before session mode began. When Enter and the following keystrokes
// share one stdin read, the router translates the whole read for the
// dashboard, so those keys arrive at Home as messages after the mode switch.
// Home re-encodes them and forwards them here rather than dropping them.
func (r *SessionInputRouter) Forward(raw []byte) {
	r.mu.Lock()
	r.enqueuePaneLocked(raw)
	r.mu.Unlock()
}

// paneWriter delivers pane-bound bytes to the embedded PTY from its own
// goroutine, in arrival order. A PTY write blocks when the client stops
// reading and fails once the client has gone; neither may delay the dashboard
// bytes Read returns (Ctrl+Q after a pane byte in the same read, the switch
// chord, out-of-pane mouse), stall Activate/Deactivate/UpdateRect on the
// Bubble Tea goroutine, or surface as a stdin error that ends the dashboard's
// input loop. Home notices an exited client through embeddedFrameMsg.
type paneWriter struct {
	mu     sync.Mutex
	child  io.Writer
	queue  []byte
	busy   bool // a batch is being written
	closed bool
	// wake has capacity one: enqueue and close poke the goroutine through it,
	// and a poke that finds it full is already covered by the pending one.
	wake chan struct{}
}

// paneWriterMaxQueue bounds what a client that has stopped reading can pile
// up. Beyond it further pane bytes are dropped; the client was never going to
// read them, and the user sees a frozen pane, not a leaking process.
const paneWriterMaxQueue = 1 << 20

func newPaneWriter() *paneWriter {
	return &paneWriter{wake: make(chan struct{}, 1)}
}

func (w *paneWriter) enqueue(data []byte) {
	if len(data) == 0 {
		return
	}
	w.mu.Lock()
	if w.closed || len(w.queue)+len(data) > paneWriterMaxQueue {
		w.mu.Unlock()
		return
	}
	w.queue = append(w.queue, data...)
	w.mu.Unlock()
	w.poke()
}

// attach installs the client and starts delivery. The first batch it writes is
// everything queued since Prepare, ahead of anything routed later.
func (w *paneWriter) attach(child io.Writer) {
	w.mu.Lock()
	start := w.child == nil && child != nil && !w.closed
	if start {
		w.child = child
	}
	w.mu.Unlock()
	if start {
		go w.run(child)
	}
}

// close stops accepting bytes. With flush set the goroutine still delivers
// what was queued before the close (keys typed ahead of a detach chord reach
// the pane, as they did when the write was synchronous) and then exits;
// otherwise the queue is dropped.
func (w *paneWriter) close(flush bool) {
	w.mu.Lock()
	w.closed = true
	if !flush || w.child == nil {
		w.queue = nil
	}
	w.mu.Unlock()
	w.poke()
}

func (w *paneWriter) hasChild() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.child != nil
}

func (w *paneWriter) writesTo(child io.Writer) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.child == child
}

func (w *paneWriter) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

func (w *paneWriter) idle() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.child == nil || (len(w.queue) == 0 && !w.busy)
}

func (w *paneWriter) poke() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *paneWriter) run(child io.Writer) {
	for {
		w.mu.Lock()
		if len(w.queue) == 0 {
			if w.closed {
				w.mu.Unlock()
				return
			}
			w.mu.Unlock()
			<-w.wake
			continue
		}
		batch := w.queue
		w.queue = nil
		w.busy = true
		w.mu.Unlock()
		_, _ = child.Write(batch)
		w.mu.Lock()
		w.busy = false
		w.mu.Unlock()
	}
}

func (r *SessionInputRouter) Deactivate() {
	r.mu.Lock()
	r.deactivateLocked()
	r.mu.Unlock()
}

func (r *SessionInputRouter) deactivateLocked() {
	r.active = false
	if r.out != nil {
		// Bytes already accepted for the pane still reach a live client;
		// nothing queued while only connecting has anywhere to go.
		r.out.close(r.out.hasChild())
	}
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
		toDashboard := r.routeEmbeddedLocked(err != nil)
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
		// A lone Escape is both a valid key and the prefix of every CSI/SS3
		// token. Give the kernel the conventional short ESC-delay window to
		// deliver the rest of a split sequence, then forward it as-is rather
		// than blocking until the user's next keypress.
		if partial && !pollFdReady(int(r.File.Fd()), 50*time.Millisecond) {
			r.mu.Lock()
			toDashboard = r.routeEmbeddedLocked(true)
			if len(toDashboard) > 0 {
				copied := copy(p, toDashboard)
				if copied < len(toDashboard) {
					r.pending = append(r.pending, toDashboard[copied:]...)
				}
				r.mu.Unlock()
				return copied, nil
			}
			r.mu.Unlock()
		}
	}
}

// routeEmbeddedLocked consumes complete tokens from rawBuf. It returns the
// bytes that go back through Bubble Tea (detach, switch chord, sidebar
// toggle, out-of-pane mouse) and queues the pane's bytes on the pane writer,
// which delivers them in order on its own goroutine. A detach or switch chord
// deactivates the router, so pane bytes that preceded it in the same read are
// queued for delivery before the writer is closed, and bytes after it are
// discarded.
func (r *SessionInputRouter) routeEmbeddedLocked(final bool) (toDashboard []byte) {
	var child bytes.Buffer
	var dashboard bytes.Buffer
	data := r.rawBuf
	i := 0

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
			r.enqueuePaneLocked(child.Bytes())
			child.Reset()
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
			r.enqueuePaneLocked(child.Bytes())
			child.Reset()
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
	r.enqueuePaneLocked(child.Bytes())
	return dashboard.Bytes()
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
