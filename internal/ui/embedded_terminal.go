package ui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/clipboard"
	deckterminal "github.com/asheshgoplani/agent-deck/internal/terminal"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// embeddedTerminalSize is expressed in terminal cells, not pixels.
type embeddedTerminalSize struct {
	Cols int
	Rows int
}

// embeddedCursorState is the subset of DEC cursor state the outer renderer
// needs to place the real terminal cursor over the embedded cell grid.
type embeddedCursorState struct {
	X, Y    int
	Visible bool
	Style   vt.CursorStyle
	Steady  bool
}

type embeddedClipboardFunc func(string)

var (
	embeddedClipboardOnce     sync.Once
	embeddedClipboardRequests chan string
	embeddedClipboardQueueMu  sync.Mutex
)

// newEmbeddedTerminalEmulator installs the host-side half of OSC 52 clipboard
// handling. The embedded tmux client cannot write escape sequences directly to
// the user's terminal: Charm VT consumes them while building its cell grid.
// Decoding the request here preserves the behavior a native terminal would
// provide and lets selections from shells and TUIs reach the system clipboard.
func newEmbeddedTerminalEmulator(
	size embeddedTerminalSize,
	copyToClipboard embeddedClipboardFunc,
) *vt.SafeEmulator {
	emulator := vt.NewSafeEmulator(size.Cols, size.Rows)
	if copyToClipboard == nil {
		return emulator
	}
	emulator.RegisterOscHandler(52, func(data []byte) bool {
		parts := strings.SplitN(string(data), ";", 3)
		if len(parts) != 3 || parts[0] != "52" || parts[2] == "" || parts[2] == "?" {
			return true
		}
		decoded, err := base64.StdEncoding.DecodeString(parts[2])
		if err != nil {
			return true
		}
		copyToClipboard(string(decoded))
		return true
	})
	return emulator
}

func copyEmbeddedTerminalSelection(text string) {
	embeddedClipboardOnce.Do(func() {
		embeddedClipboardRequests = make(chan string, 1)
		go func() {
			for selection := range embeddedClipboardRequests {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_, _ = clipboard.CopyContext(ctx, selection, false)
				cancel()
			}
		}()
	})

	// Clipboard state is last-write-wins. Keep at most the active copy and the
	// newest pending selection so a slow desktop service cannot create an
	// unbounded queue or let concurrent pbcopy processes finish out of order.
	enqueueLatestClipboardSelection(embeddedClipboardRequests, text)
}

func enqueueLatestClipboardSelection(requests chan string, text string) {
	embeddedClipboardQueueMu.Lock()
	defer embeddedClipboardQueueMu.Unlock()

	select {
	case requests <- text:
	default:
		select {
		case <-requests:
		default:
		}
		select {
		case requests <- text:
		default:
		}
	}
}

// embeddedTerminal is a real tmux attach client running in a child PTY. tmux
// remains the owner of the session; this object is only one disposable client.
// Charm VT consumes the client's output and produces terminal replies on its
// input pipe, exactly as a native terminal would.
type embeddedTerminal struct {
	emulator *vt.SafeEmulator
	ptmx     *os.File
	cmd      *exec.Cmd
	cancel   context.CancelFunc

	dirty      chan struct{}
	exited     chan struct{}
	outputDone chan struct{}
	replyDone  chan struct{}

	cursorMu sync.RWMutex
	cursor   embeddedCursorState
	exitMu   sync.RWMutex
	exitErr  error

	// emulatorFrameMu makes the rendered cells and callback-owned cursor state
	// one snapshot. VT invokes cursor callbacks while Write is still parsing a
	// tmux repaint (which commonly hides the cursor, paints, then shows it).
	// Without this frame lock the UI can observe that temporary hidden state and
	// flash the outer hardware cursor at the session's output rate.
	emulatorFrameMu sync.RWMutex
	writeMu         sync.Mutex
	closeOnce       sync.Once
}

func startEmbeddedTerminal(
	parent context.Context,
	req deckterminal.AttachRequest,
	size embeddedTerminalSize,
) (*embeddedTerminal, error) {
	return startEmbeddedTerminalWithClipboard(parent, req, size, copyEmbeddedTerminalSelection)
}

func startEmbeddedTerminalWithClipboard(
	parent context.Context,
	req deckterminal.AttachRequest,
	size embeddedTerminalSize,
	copyToClipboard embeddedClipboardFunc,
) (*embeddedTerminal, error) {
	command := deckterminal.BuildAttachCommand(req)
	if command == "" {
		return nil, errors.New("embedded terminal: invalid attach target")
	}
	if size.Cols < 1 || size.Rows < 1 {
		return nil, fmt.Errorf("embedded terminal: invalid size %dx%d", size.Cols, size.Rows)
	}
	size = clampEmbeddedTerminalSize(size)

	ctx, cancel := context.WithCancel(parent)
	// #nosec G204 -- BuildAttachCommand single-quotes every dynamic local and
	// remote operand; the only unquoted tokens are fixed tmux/ssh flags.
	cmd := exec.CommandContext(ctx, "sh", "-c", "exec "+command)
	cmd.Env = embeddedTerminalEnv(childenv.ForLaunch(""))

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: uint16(size.Cols), // #nosec G115 -- dimensions are clamped above and by the UI viewport
		Rows: uint16(size.Rows), // #nosec G115 -- dimensions are clamped above and by the UI viewport
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("embedded terminal: start attach PTY: %w", err)
	}
	if _, err := term.MakeRaw(int(ptmx.Fd())); err != nil {
		_ = ptmx.Close()
		cancel()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return nil, fmt.Errorf("embedded terminal: make PTY raw: %w", err)
	}

	t := &embeddedTerminal{
		emulator:   newEmbeddedTerminalEmulator(size, copyToClipboard),
		ptmx:       ptmx,
		cmd:        cmd,
		cancel:     cancel,
		dirty:      make(chan struct{}, 1),
		exited:     make(chan struct{}),
		outputDone: make(chan struct{}),
		replyDone:  make(chan struct{}),
		cursor: embeddedCursorState{
			Visible: true,
			Style:   vt.CursorBlock,
		},
	}
	t.emulator.SetCallbacks(t.emulatorCallbacks())

	go t.copyPTYOutput()
	go t.copyTerminalReplies()
	go t.waitProcess()
	return t, nil
}

func (t *embeddedTerminal) emulatorCallbacks() vt.Callbacks {
	return vt.Callbacks{
		CursorVisibility: func(visible bool) {
			t.cursorMu.Lock()
			t.cursor.Visible = visible
			t.cursorMu.Unlock()
		},
		// Charm currently passes the cursor's "steady" bit as the second
		// argument despite the callback field's historical blink naming.
		CursorStyle: func(style vt.CursorStyle, steady bool) {
			t.cursorMu.Lock()
			t.cursor.Style, t.cursor.Steady = style, steady
			t.cursorMu.Unlock()
		},
	}
}

func clampEmbeddedTerminalSize(size embeddedTerminalSize) embeddedTerminalSize {
	const maxPTYDimension = int(^uint16(0))
	size.Cols = min(max(size.Cols, 1), maxPTYDimension)
	size.Rows = min(max(size.Rows, 1), maxPTYDimension)
	return size
}

func renderTerminalSnapshot(
	content string,
	size embeddedTerminalSize,
	scrollOffset int,
) (string, int) {
	size = clampEmbeddedTerminalSize(size)
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	maxOffset := max(0, len(lines)-size.Rows)
	scrollOffset = min(max(0, scrollOffset), maxOffset)
	end := len(lines) - scrollOffset
	start := max(0, end-size.Rows)
	lines = lines[start:end]

	emulator := vt.NewSafeEmulator(size.Cols, size.Rows)
	defer func() { _ = emulator.Close() }()
	_, _ = emulator.WriteString(strings.Join(lines, "\r\n"))
	rendered := emulator.Render()
	return ensureExactWidth(ensureExactHeight(rendered, size.Rows), size.Cols), scrollOffset
}

func embeddedTerminalEnv(env []string) []string {
	filtered := make([]string, 0, len(env)+2)
	for _, entry := range env {
		if strings.HasPrefix(entry, "TERM=") || strings.HasPrefix(entry, "COLORTERM=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	// Advertise the emulator we actually implement, not the outer terminal.
	// In particular, inheriting TERM=xterm-ghostty would invite tmux to emit
	// graphics/private protocols that a cell-based Charm VT cannot represent.
	return append(filtered, "TERM=xterm-256color", "COLORTERM=truecolor")
}

func (t *embeddedTerminal) copyPTYOutput() {
	defer close(t.outputDone)
	buf := make([]byte, 32*1024)
	for {
		n, err := t.ptmx.Read(buf)
		if n > 0 {
			t.applyEmulatorOutput(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (t *embeddedTerminal) applyEmulatorOutput(p []byte) {
	t.emulatorFrameMu.Lock()
	_, _ = t.emulator.Write(p)
	t.emulatorFrameMu.Unlock()
	// Cursor callbacks run inside emulator.Write. Publish only after the whole
	// PTY chunk has committed so Bubble Tea cannot render a half-painted frame.
	t.markDirty()
}

func (t *embeddedTerminal) copyTerminalReplies() {
	defer close(t.replyDone)
	// Device attributes, cursor reports, mode reports, and similar replies
	// generated by Charm must flow back to the tmux client, not to Bubble Tea.
	_, _ = io.Copy(t, t.emulator)
}

func (t *embeddedTerminal) waitProcess() {
	err := t.cmd.Wait()
	t.exitMu.Lock()
	if err != nil && !errors.Is(err, context.Canceled) {
		t.exitErr = err
	}
	t.exitMu.Unlock()
	close(t.exited)
	t.markDirty()
}

func (t *embeddedTerminal) markDirty() {
	select {
	case t.dirty <- struct{}{}:
	default:
	}
}

func (t *embeddedTerminal) Wait() (bool, error) {
	select {
	case <-t.dirty:
		select {
		case <-t.exited:
			return false, t.ExitError()
		default:
			return true, nil
		}
	case <-t.exited:
		return false, t.ExitError()
	}
}

func (t *embeddedTerminal) ExitError() error {
	t.exitMu.RLock()
	defer t.exitMu.RUnlock()
	return t.exitErr
}

func (t *embeddedTerminal) Write(p []byte) (int, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.ptmx.Write(p)
}

func (t *embeddedTerminal) Resize(size embeddedTerminalSize) error {
	if size.Cols < 1 || size.Rows < 1 {
		return nil
	}
	size = clampEmbeddedTerminalSize(size)
	if err := pty.Setsize(t.ptmx, &pty.Winsize{
		Cols: uint16(size.Cols), // #nosec G115 -- bounded by viewport
		Rows: uint16(size.Rows), // #nosec G115 -- bounded by viewport
	}); err != nil {
		return fmt.Errorf("resize attach PTY: %w", err)
	}
	t.emulatorFrameMu.Lock()
	t.emulator.Resize(size.Cols, size.Rows)
	t.emulatorFrameMu.Unlock()
	t.markDirty()
	return nil
}

func (t *embeddedTerminal) Render() string {
	t.emulatorFrameMu.RLock()
	defer t.emulatorFrameMu.RUnlock()
	return t.emulator.Render()
}

func (t *embeddedTerminal) Cursor() embeddedCursorState {
	t.emulatorFrameMu.RLock()
	defer t.emulatorFrameMu.RUnlock()
	pos := t.emulator.CursorPosition()
	t.cursorMu.RLock()
	cursor := t.cursor
	t.cursorMu.RUnlock()
	cursor.X, cursor.Y = pos.X, pos.Y
	return cursor
}

func (t *embeddedTerminal) Close() error {
	var closeErr error
	t.closeOnce.Do(func() {
		t.cancel()
		closeErr = t.ptmx.Close()
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		<-t.outputDone
		// SafeEmulator.Read delegates directly to Emulator.Read, whose closed
		// flag is not synchronized with Emulator.Close. Close the input pipe
		// first so the reply reader reaches EOF without racing that flag, then
		// close the otherwise-idle emulator.
		if input, ok := t.emulator.InputPipe().(io.Closer); ok {
			_ = input.Close()
		}
		<-t.replyDone
		_ = t.emulator.Close()
		// Reap the attach process before returning so it cannot keep using the
		// caller's HOME after an evaluator or application shutdown.
		<-t.exited
	})
	return closeErr
}
