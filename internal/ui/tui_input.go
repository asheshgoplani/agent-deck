package ui

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

// tuiInputHeld stops every tuiInput from reading while a tea.Exec hands the
// terminal to another program (execHoldingInput). The fallback reader drops
// bytes read across a cancel, so nothing may be read once a release begins:
// what is typed then belongs to the attached program.
var tuiInputHeld atomic.Int32

func (t *tuiInput) Read(p []byte) (int, error) {
	if tuiInputHeld.Load() > 0 {
		time.Sleep(10 * time.Millisecond) // let the fallback see the cancel
		return 0, nil
	}
	if t.ready != nil && (t.pending == nil || !t.pending()) && !t.ready(tuiInputPoll) {
		return 0, nil
	}
	if tuiInputHeld.Load() > 0 {
		return 0, nil // a release began while polling: leave the bytes in the kernel
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
	c.yieldEmpty = true // never block in stdin.Read: tuiInput polls
	fd := int(stdin.Fd())
	return &tuiInput{
		f: stdin, r: c,
		ready: func(d time.Duration) bool { return pollFdReady(fd, d) },
		// Only output ready to return: an incomplete sequence in inBuf needs
		// new bytes, which the poll waits for.
		pending: func() bool { return len(c.outBuf) > 0 || c.err != nil },
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

// execHoldingInput is tea.Exec for the TUI: the TUI's input stops reading
// before Bubble Tea releases the terminal, and reads again once the program
// returns (or the exec fails before it runs). Every tea.Exec of the TUI goes
// through it.
func execHoldingInput(c tea.ExecCommand, fn tea.ExecCallback) tea.Cmd {
	return func() tea.Msg {
		tuiInputHeld.Add(1)
		var once atomic.Bool
		release := func() {
			if once.CompareAndSwap(false, true) {
				tuiInputHeld.Add(-1)
			}
		}
		return tea.Exec(heldExec{ExecCommand: c, release: release}, func(err error) tea.Msg {
			release() // also when the release itself failed and Run never ran
			if fn == nil {
				return nil
			}
			return fn(err)
		})()
	}
}

type heldExec struct {
	tea.ExecCommand
	release func()
}

func (h heldExec) Run() error {
	defer h.release()
	return h.ExecCommand.Run()
}

// execProcessHoldingInput is tea.ExecProcess for the TUI. The program reads
// the terminal itself: Bubble Tea would hand it the TUI's (held) input.
func execProcessHoldingInput(cmd *exec.Cmd, fn tea.ExecCallback) tea.Cmd {
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	return execHoldingInput(osExecCommand{cmd}, fn)
}

// osExecCommand mirrors Bubble Tea's wrapper for *exec.Cmd.
type osExecCommand struct{ *exec.Cmd }

func (c osExecCommand) SetStdin(r io.Reader) {
	if c.Stdin == nil {
		c.Stdin = r
	}
}

func (c osExecCommand) SetStdout(w io.Writer) {
	if c.Stdout == nil {
		c.Stdout = w
	}
}

func (c osExecCommand) SetStderr(w io.Writer) {
	if c.Stderr == nil {
		c.Stderr = w
	}
}
