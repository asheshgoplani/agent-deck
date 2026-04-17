package web

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

var ErrTmuxSessionNotFound = errors.New("tmux session not found")

type wsConnWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func newWSConnWriter(conn *websocket.Conn) *wsConnWriter {
	return &wsConnWriter{conn: conn}
}

func (w *wsConnWriter) WriteJSON(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return w.conn.WriteJSON(v)
}

func (w *wsConnWriter) WriteBinary(data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return w.conn.WriteMessage(websocket.BinaryMessage, data)
}

type tmuxPTYBridge struct {
	tmuxSession string
	sessionID   string
	writer      *wsConnWriter

	cmd *exec.Cmd

	// ptmxMu guards ptmx against a concurrent Close/Resize race. Close
	// closes the PTY file and nils the pointer under the write lock;
	// Resize reads under the read lock so Setsize cannot hit a freshly
	// closed fd. Observed as an intermittent TestTmuxPTYBridgeResize
	// -race failure on CI (v1.7.4, v1.7.5 release workflows).
	ptmxMu sync.RWMutex
	ptmx   *os.File

	closeOnce sync.Once
	done      chan struct{}
}

func newTmuxPTYBridge(tmuxSession, sessionID string, writer *wsConnWriter) (*tmuxPTYBridge, error) {
	if tmuxSession == "" {
		return nil, fmt.Errorf("tmux session name is required")
	}
	if writer == nil {
		return nil, fmt.Errorf("writer is required")
	}
	exists, err := tmuxSessionExists(tmuxSession)
	if err != nil {
		return nil, fmt.Errorf("check tmux session %q: %w", tmuxSession, err)
	}
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrTmuxSessionNotFound, tmuxSession)
	}

	cmd := tmuxAttachCommand(tmuxSession)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start tmux pty: %w", err)
	}

	b := &tmuxPTYBridge{
		tmuxSession: tmuxSession,
		sessionID:   sessionID,
		writer:      writer,
		cmd:         cmd,
		ptmx:        ptmx,
		done:        make(chan struct{}),
	}

	go b.streamOutput()
	return b, nil
}

func (b *tmuxPTYBridge) streamOutput() {
	defer close(b.done)

	buf := make([]byte, 4096)
	for {
		n, err := b.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if writeErr := b.writer.WriteBinary(chunk); writeErr != nil {
				b.Close()
				return
			}
		}

		if err != nil {
			if !errors.Is(err, io.EOF) {
				_ = b.writer.WriteJSON(wsServerMessage{
					Type:      "status",
					Event:     "session_closed",
					SessionID: b.sessionID,
					Time:      time.Now().UTC(),
				})
			}
			b.Close()
			return
		}
	}
}

func (b *tmuxPTYBridge) WriteInput(data string) error {
	if b == nil || b.ptmx == nil {
		return fmt.Errorf("bridge not initialized")
	}
	if data == "" {
		return nil
	}
	_, err := b.ptmx.Write([]byte(data))
	return err
}

func (b *tmuxPTYBridge) Resize(cols, rows int) error {
	if b == nil {
		return fmt.Errorf("bridge not initialized")
	}
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("invalid dimensions: cols=%d rows=%d", cols, rows)
	}

	b.ptmxMu.RLock()
	defer b.ptmxMu.RUnlock()
	if b.ptmx == nil {
		return fmt.Errorf("bridge not initialized")
	}

	var firstErr error

	// Step 1: Resize the local PTY master (per D-02: pty.Setsize first).
	// This sends SIGWINCH to the tmux attach process. With ignore-size on the
	// attach client, the tmux server will not auto-resize from this signal,
	// but the PTY master's own TIOCGWINSZ is updated so xterm.js cell layout
	// calculations are correct.
	if err := pty.Setsize(b.ptmx, &pty.Winsize{
		Rows: uint16(rows),
		Cols: uint16(cols),
	}); err != nil {
		firstErr = fmt.Errorf("resize pty: %w", err)
	}

	// Step 2: Tell the tmux server the new window dimensions (per D-01).
	// Required because ignore-size prevents the server from adopting the
	// attach client's PTY size automatically.
	args := []string{
		"resize-window", "-t", b.tmuxSession,
		"-x", strconv.Itoa(cols),
		"-y", strconv.Itoa(rows),
	}
	if output, err := tmuxCommand(args...).CombinedOutput(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("tmux resize-window: %w (output: %s)", err, strings.TrimSpace(string(output)))
	}

	return firstErr
}

func (b *tmuxPTYBridge) Close() {
	if b == nil {
		return
	}
	b.closeOnce.Do(func() {
		b.ptmxMu.Lock()
		if b.ptmx != nil {
			_ = b.ptmx.Close()
			b.ptmx = nil
		}
		b.ptmxMu.Unlock()
		if b.cmd != nil && b.cmd.Process != nil {
			pgid, err := syscall.Getpgid(b.cmd.Process.Pid)
			if err == nil {
				_ = syscall.Kill(-pgid, syscall.SIGTERM)
			} else {
				_ = b.cmd.Process.Kill()
			}
		}
		if b.cmd != nil {
			_ = b.cmd.Wait()
		}
	})
}

func tmuxSessionExists(name string) (bool, error) {
	cmd := tmuxCommand("has-session", "-t", name)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}

	msg := strings.TrimSpace(string(output))
	if msg == "" {
		msg = err.Error()
	}
	return false, fmt.Errorf("tmux has-session failed: %s", msg)
}

func tmuxCommand(args ...string) *exec.Cmd {
	socketPath, hasSocket := tmuxSocketFromEnv()

	finalArgs := args
	if hasSocket {
		finalArgs = append([]string{"-S", socketPath}, args...)
	}

	cmd := exec.Command("tmux", finalArgs...)
	if hasSocket {
		cmd.Env = environWithoutTMUX(os.Environ())
	}
	return cmd
}

func tmuxAttachCommand(sessionName string) *exec.Cmd {
	// Keep this web client from influencing other attached client sizes (for example, the local TUI).
	return tmuxCommand("attach-session", "-f", "ignore-size", "-t", sessionName)
}

func tmuxSocketFromEnv() (string, bool) {
	raw := strings.TrimSpace(os.Getenv("TMUX"))
	if raw == "" {
		return "", false
	}

	socketPart := raw
	if strings.Contains(raw, ",") {
		socketPart = strings.SplitN(raw, ",", 2)[0]
	}

	socketPart = strings.TrimSpace(socketPart)
	if socketPart == "" {
		return "", false
	}
	return socketPart, true
}

func environWithoutTMUX(env []string) []string {
	filtered := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "TMUX=") {
			continue
		}
		filtered = append(filtered, kv)
	}
	return filtered
}
