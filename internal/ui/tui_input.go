package ui

import (
	"io"
	"os"
	"runtime"
	"time"
)

// tuiInput is the input the TUI hands Bubble Tea (#2692).
//
// Bubble Tea v1.3.10 builds a new cancelable input reader on every
// RestoreTerminal (each tea.Exec: attach, the editor, the updater) and never
// closes the old one. For an input that looks like a file (it has Name()),
// cancelreader backs the reader with a kqueue (macOS) or epoll (Linux): a raw
// descriptor no finalizer closes, so one leaked per attach.
//
// tuiInput keeps Fd() (Bubble Tea needs it for raw mode) but has no Name(),
// so cancelreader takes its fallback path, which owns no descriptor. The
// fallback cannot interrupt a blocked Read, so Read never blocks for long: it
// returns (0, nil) when no input arrives within tuiInputPoll, which lets the
// fallback notice a cancel and stop before the attached program reads stdin.
type tuiInput struct {
	f *os.File
	r io.Reader
	// ready waits up to the timeout for input; nil when r yields by itself.
	ready func(time.Duration) bool
	// pending reports input r already holds.
	pending func() bool
}

// tuiInputPoll bounds one Read while no input arrives: how long a release
// waits for the read loop to stop.
const tuiInputPoll = 50 * time.Millisecond

func (t *tuiInput) Read(p []byte) (int, error) {
	if t.ready != nil && (t.pending == nil || !t.pending()) && !t.ready(tuiInputPoll) {
		return 0, nil
	}
	return t.r.Read(p)
}

func (t *tuiInput) Write(p []byte) (int, error) { return t.f.Write(p) }
func (t *tuiInput) Close() error                { return t.f.Close() }
func (t *tuiInput) Fd() uintptr                 { return t.f.Fd() }

// NewTUIInput is the TUI's input on stdin: CSI u translation (see
// NewCSIuReader), in a form whose reader owns no poll descriptor.
func NewTUIInput(stdin *os.File) io.Reader {
	if runtime.GOOS == "windows" {
		return NewCSIuReader(stdin) // no kqueue/epoll there; pollFdReady is a stub
	}
	c := newCSIuReader(stdin)
	fd := int(stdin.Fd())
	return &tuiInput{
		f: stdin, r: c,
		ready:   func(d time.Duration) bool { return pollFdReady(fd, d) },
		pending: func() bool { return len(c.outBuf) > 0 || len(c.inBuf) > 0 || c.err != nil },
	}
}

// NewTUIRouterInput is the embedded-terminal input router in the same form;
// the router's Read already returns when stdin is idle.
func NewTUIRouterInput(r *SessionInputRouter) io.Reader {
	if runtime.GOOS == "windows" {
		return r
	}
	return &tuiInput{f: r.File, r: r}
}
