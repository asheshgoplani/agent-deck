package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
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

	// Dashboard reads stop at a complete event. A matching Home.Update must
	// finish before another read can cross a possible attach transition.
	dashboardBytes   []byte
	dashboardPaste   bool
	dashboardEscape  bool
	barrier          *sessionInputBarrier
	queue            *sessionInputQueue
	failed           chan struct{}
	discardUntilIdle bool
	discardPaste     bool
	discardTail      []byte
}

type sessionInputBarrier struct {
	done  chan struct{}
	key   string
	mouse bool
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

// BeginConnecting reserves input for one immutable attach generation before
// the asynchronous PTY start. Installation supplies its writer without resetting
// paste or partial-token state.
func (r *SessionInputRouter) BeginConnecting(parent context.Context, generation uint64, rect terminalCellRect, switchByte byte) *sessionInputQueue {
	if parent == nil {
		parent = context.Background()
	}
	q := newSessionInputQueue(parent, generation)
	r.mu.Lock()
	r.deactivateLocked()
	r.queue = q
	r.child = q
	r.active = true
	r.rect = rect
	r.switchByte = switchByte
	r.mu.Unlock()
	return q
}

func (r *SessionInputRouter) Install(generation uint64, child io.WriteCloser) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.queue != nil && r.queue.generation == generation && r.queue.install(child)
}

// InputReceipt captures only the outstanding event that this update actually
// handles. Earlier keys, redraws and timers cannot release an Enter barrier.
func (r *SessionInputRouter) InputReceipt(msg tea.Msg) *sessionInputBarrier {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.barrier
	if b == nil {
		return nil
	}
	switch m := msg.(type) {
	case tea.KeyMsg:
		if !b.mouse && m.String() == b.key {
			return b
		}
	case tea.MouseMsg:
		if b.mouse {
			return b
		}
	}
	return nil
}

func (r *SessionInputRouter) FinishInput(b *sessionInputBarrier) {
	if b == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.barrier == b {
		r.releaseBarrierLocked()
	}
}

func (r *SessionInputRouter) releaseBarrierLocked() {
	if r.barrier != nil {
		close(r.barrier.done)
		r.barrier = nil
	}
}

// armDashboardBarrierLocked is intentionally based on bytes already translated
// for the dashboard, never on bytes destined for the connecting/connected pane.
func (r *SessionInputRouter) armDashboardBarrierLocked(p []byte) {
	if string(p) == bracketedPasteStart {
		r.dashboardPaste = true
		return
	}
	if string(p) == bracketedPasteEnd {
		r.dashboardPaste = false
		return
	}
	if r.dashboardPaste || len(p) == 0 {
		return
	}
	b := &sessionInputBarrier{done: make(chan struct{})}
	if bytes.HasPrefix(p, []byte("\x1b[<")) && mouseSequenceEnd(p) == len(p) {
		b.mouse = true
	} else {
		alt := len(p) > 1 && p[0] == 0x1b && p[1] != '[' && p[1] != 'O'
		keyBytes := p
		if alt {
			keyBytes = p[1:]
		}
		if len(keyBytes) == 1 && (keyBytes[0] <= 31 || keyBytes[0] == 127) {
			b.key = (tea.KeyMsg{Type: tea.KeyType(keyBytes[0]), Alt: alt}).String()
		} else {
			// Other terminal sequences retain the compatibility reader's
			// existing behavior. They cannot be a plain Enter or mouse attach.
			return
		}
	}
	r.barrier = b
}

// Activate starts a fresh generation for callers that already own a writer.
// As with Install, the writer must be closable so cancellation can interrupt a
// pending Write. Home uses BeginConnecting/Install to retain pre-start bytes.
func (r *SessionInputRouter) Activate(child io.Writer, rect terminalCellRect, switchByte byte) {
	closer, ok := child.(io.WriteCloser)
	if !ok {
		r.Deactivate()
		return
	}
	q := r.BeginConnecting(context.Background(), 0, rect, switchByte)
	q.install(closer)
}

func (r *SessionInputRouter) Deactivate() {
	r.mu.Lock()
	r.deactivateLocked()
	r.mu.Unlock()
}

func (r *SessionInputRouter) deactivateLocked() {
	if r.inPaste && !r.discardPaste {
		// Cancellation/overflow must not turn the remainder of a paste into
		// dashboard shortcuts. Keep a split terminator prefix across reads.
		r.discardPaste = true
		n := longestSuffixPrefix(r.rawBuf, []byte(bracketedPasteEnd))
		r.discardTail = append(r.discardTail[:0], r.rawBuf[len(r.rawBuf)-n:]...)
	}
	if r.queue != nil {
		r.queue.stop()
		r.queue = nil
	}
	if r.failed != nil {
		close(r.failed)
		r.failed = nil
	}
	r.releaseBarrierLocked()
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
	readUntil := time.Now().Add(25 * time.Millisecond)
	for {
		// A continuous paste must yield too, even when stdin never goes idle.
		if time.Now().After(readUntil) {
			return 0, nil
		}
		r.mu.Lock()
		if len(r.pending) > 0 {
			n := copy(p, r.pending)
			r.pending = r.pending[n:]
			r.mu.Unlock()
			return n, nil
		}
		if failed := r.failed; failed != nil {
			r.mu.Unlock()
			select {
			case <-failed:
				continue
			case <-time.After(25 * time.Millisecond):
				return 0, nil
			}
		}
		if b := r.barrier; b != nil {
			r.mu.Unlock()
			select {
			case <-b.done:
				continue
			case <-time.After(25 * time.Millisecond):
				return 0, nil
			}
		}
		if r.discardUntilIdle {
			r.discardBufferedTailLocked()
			if r.discardUntilIdle {
				r.mu.Unlock()
				return 0, nil
			}
		}
		// Keep suffixes in the kernel in both modes. An out-of-pane mouse
		// event can select another generation just as Enter can attach one.
		// Reading one byte costs syscalls, but avoids prefetching bytes whose
		// destination Home has not decided, and preserves FD readiness.
		const readSize = 1
		r.mu.Unlock()

		// Yield to cancelreader periodically: its cancellation pipe cannot
		// interrupt our internal receipt waits or a second raw read. An empty
		// read retains all parser state and lets its next Read observe Cancel.
		if runtime.GOOS != "windows" && !pollFdReady(int(r.File.Fd()), 25*time.Millisecond) {
			return 0, nil
		}
		buf := make([]byte, readSize)
		n, err := r.File.Read(buf)
		r.mu.Lock()
		if r.discardPaste {
			for _, b := range buf[:n] {
				r.discardTail = append(r.discardTail, b)
				if bytes.HasSuffix(r.discardTail, []byte(bracketedPasteEnd)) {
					r.discardPaste = false
					r.discardTail = nil
					break
				}
				keep := longestSuffixPrefix(r.discardTail, []byte(bracketedPasteEnd))
				r.discardTail = append(r.discardTail[:0], r.discardTail[len(r.discardTail)-keep:]...)
			}
			r.mu.Unlock()
			if err != nil {
				return 0, err
			}
			continue
		}
		if !r.active {
			raw := buf[:n]
			if r.dashboardEscape {
				raw = append([]byte{0x1b}, raw...)
				r.dashboardEscape = false
			} else if n == 1 && raw[0] == 0x1b && err == nil {
				// The reply filter eagerly flushes an unarmed trailing ESC.
				// Keep its introducer together so bytewise reads cannot turn
				// OSC/DCS replies into dashboard text.
				r.dashboardEscape = true
				raw = nil
			}
			r.dashboardBytes = append(r.dashboardBytes, r.normal.consume(raw, err != nil)...)
			standaloneEscape := r.dashboardEscape || (len(r.normal.inBuf) == 1 && r.normal.inBuf[0] == 0x1b)
			if len(r.dashboardBytes) > 0 && completeDashboardRunes(r.dashboardBytes) {
				translated := r.dashboardBytes
				r.dashboardBytes = nil
				r.armDashboardBarrierLocked(translated)
				copied := copy(p, translated)
				r.pending = append(r.pending, translated[copied:]...)
				r.mu.Unlock()
				return copied, nil
			}
			r.mu.Unlock()
			if err != nil {
				return 0, err
			}
			if standaloneEscape && !pollFdReady(int(r.File.Fd()), 50*time.Millisecond) {
				r.mu.Lock()
				var translated []byte
				if r.dashboardEscape {
					r.dashboardEscape = false
					translated = r.normal.consume([]byte{0x1b}, false)
				}
				if len(r.normal.inBuf) == 1 && r.normal.inBuf[0] == 0x1b {
					translated = append(translated, r.normal.consume(nil, true)...)
				}
				r.armDashboardBarrierLocked(translated)
				copied := copy(p, translated)
				r.pending = append(r.pending, translated[copied:]...)
				r.mu.Unlock()
				return copied, nil
			}
			continue
		}
		r.rawBuf = append(r.rawBuf, buf[:n]...)
		if len(r.rawBuf) > maxConnectingInputBytes && r.queue != nil {
			// An unterminated mouse/control token must not evade the queue's
			// finite pending-input policy by growing the parser buffer forever.
			r.queue.mu.Lock()
			r.queue.failLocked(errConnectingInputOverflow)
			r.queue.mu.Unlock()
			r.failed = make(chan struct{})
			r.mu.Unlock()
			continue
		}
		toDashboard, routeErr := r.routeToQueueLocked(err != nil)
		r.discardBufferedTailLocked()
		if routeErr != nil && r.queue != nil {
			// The generation's error command returns the UI to the dashboard.
			// Keep further bytes away from both targets until Home cancels it.
			r.failed = make(chan struct{})
			r.mu.Unlock()
			continue
		}
		if len(toDashboard) > 0 {
			r.armDashboardBarrierLocked(toDashboard)
			copied := copy(p, toDashboard)
			r.pending = append(r.pending, toDashboard[copied:]...)
			r.mu.Unlock()
			return copied, nil
		}
		partial := len(r.rawBuf) == 1 && r.rawBuf[0] == 0x1b && !r.inPaste
		r.mu.Unlock()
		if routeErr != nil {
			return 0, routeErr
		}
		if err != nil {
			return 0, err
		}
		if partial && !pollFdReady(int(r.File.Fd()), 50*time.Millisecond) {
			r.mu.Lock()
			toDashboard, routeErr = r.routeToQueueLocked(true)
			if routeErr != nil && r.queue != nil {
				r.failed = make(chan struct{})
				r.mu.Unlock()
				continue
			}
			if len(toDashboard) > 0 {
				r.armDashboardBarrierLocked(toDashboard)
				copied := copy(p, toDashboard)
				r.pending = append(r.pending, toDashboard[copied:]...)
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

// Discard the bytes already available at cancellation, before handing Ctrl+Q
// back to Home. Waiting until the next readiness notification would instead
// discard a fresh dashboard key. Bound each drain so shutdown can still yield.
func (r *SessionInputRouter) discardBufferedTailLocked() {
	if !r.discardUntilIdle || r.File == nil {
		return
	}
	var buf [256]byte
	for discarded := 0; discarded < maxConnectingInputBytes; {
		if !pollFdReady(int(r.File.Fd()), 0) {
			r.discardUntilIdle = false
			return
		}
		n, err := r.File.Read(buf[:])
		discarded += n
		if err != nil || n == 0 {
			r.discardUntilIdle = false
			return
		}
	}
}

func completeDashboardRunes(p []byte) bool {
	for len(p) > 0 {
		if !utf8.FullRune(p) {
			return false
		}
		_, n := utf8.DecodeRune(p)
		p = p[n:]
	}
	return true
}

// routeToQueueLocked preserves the pure parser seam used by routing tests.
// A cancellation token may deactivate the generation during parsing; in that
// case its unsent prefix is deliberately discarded with the remaining queue.
func (r *SessionInputRouter) routeToQueueLocked(final bool) ([]byte, error) {
	dashboard, child := r.routeEmbeddedLocked(final)
	if !r.active || len(child) == 0 {
		return dashboard, nil
	}
	if r.queue == nil {
		return dashboard, errors.New("embedded input has no generation writer")
	}
	_, err := r.queue.Write(child)
	return dashboard, err
}

// routeEmbeddedLocked consumes complete tokens from rawBuf. It returns bytes
// that should go back through Bubble Tea (detach and out-of-pane mouse) and
// bytes for the pane. Delivery belongs to the generation writer, so parsing
// never blocks on a PTY. A detach cancels unsent bytes; already accepted writes
// cannot be recalled and are never replayed to another generation.
func (r *SessionInputRouter) routeEmbeddedLocked(final bool) (toDashboard, toChild []byte) {
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
			keep := longestSuffixPrefix(data[i:], []byte(bracketedPasteEnd))
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
			r.discardUntilIdle = true
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
			r.discardUntilIdle = true
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

// A connecting generation may retain at most this many input bytes, including
// the write currently in flight. Overflow is an explicit input error, never a
// truncated command or a replay into a different session.
const maxConnectingInputBytes = 256 << 10

var errConnectingInputOverflow = errors.New("embedded session input exceeds 256 KiB pending limit")

type sessionInputQueue struct {
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	chunks     [][]byte
	bytes      int
	installed  bool
	wake       chan struct{}
	err        error
}

func newSessionInputQueue(parent context.Context, generation uint64) *sessionInputQueue {
	ctx, cancel := context.WithCancel(parent)
	return &sessionInputQueue{generation: generation, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
}

func (q *sessionInputQueue) Write(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ctx.Err() != nil {
		return 0, q.ctx.Err()
	}
	if len(p) > maxConnectingInputBytes-q.bytes {
		q.failLocked(errConnectingInputOverflow)
		return 0, errConnectingInputOverflow
	}
	// Coalesce the byte-at-a-time raw reader's pending input into bounded
	// chunks. A chunk in flight has already left this slice and is immutable.
	if last := len(q.chunks) - 1; last >= 0 && len(q.chunks[last])+len(p) <= 4096 {
		q.chunks[last] = append(q.chunks[last], p...)
	} else {
		q.chunks = append(q.chunks, append([]byte(nil), p...))
	}
	q.bytes += len(p)
	select {
	case q.wake <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (q *sessionInputQueue) stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.chunks = nil
	q.cancel()
}

func (q *sessionInputQueue) install(w io.WriteCloser) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.installed || q.ctx.Err() != nil || w == nil {
		return false
	}
	q.installed = true
	// Closing is independent of the writer: a full PTY must never prevent
	// cancellation from reaching its Close. Production Close is idempotent.
	go func() {
		<-q.ctx.Done()
		_ = w.Close()
	}()
	go q.drain(w)
	return true
}

func (q *sessionInputQueue) failLocked(err error) {
	if q.ctx.Err() == nil {
		q.err = err
	}
	q.chunks = nil
	q.cancel()
}

func (q *sessionInputQueue) result() error {
	<-q.ctx.Done()
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.err
}

func (q *sessionInputQueue) drain(w io.Writer) {
	for {
		select {
		case <-q.ctx.Done():
			return
		case <-q.wake:
		}
		for {
			q.mu.Lock()
			if q.ctx.Err() != nil || len(q.chunks) == 0 {
				q.mu.Unlock()
				break
			}
			p := q.chunks[0]
			q.chunks[0] = nil
			q.chunks = q.chunks[1:]
			q.mu.Unlock()
			// Only this goroutine writes. Connecting bytes cannot be
			// overtaken, and a partial write is never retried from its start.
			for len(p) > 0 {
				if q.ctx.Err() != nil {
					return
				}
				n, err := w.Write(p)
				q.mu.Lock()
				if n < 0 || n > len(p) {
					n, err = 0, io.ErrShortWrite
				}
				q.bytes -= n
				if err != nil || n == 0 {
					if err == nil {
						err = io.ErrShortWrite
					}
					q.failLocked(err)
					q.mu.Unlock()
					return
				}
				q.mu.Unlock()
				p = p[n:]
			}
		}
	}
}
