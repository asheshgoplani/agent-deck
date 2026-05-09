// Package multiclienttmux boots an isolated tmux server with
// aggressive-resize=on and lets a test attach N pty clients at chosen
// sizes — the harness from TEST-PLAN.md §6.1 / TUI-TEST-PLAN.md §6.8
// for the "two web clients hijacking pane size" regression (J4 / F2).
//
// Every harness instance gets its own socket under t.TempDir(), never
// touching the user's real tmux server. Cleanup tears down the server,
// kills all spawned client processes, and removes the socket.
//
// Usage:
//
//	h := multiclienttmux.New(t, "myscratch")
//	h.AddClient(80, 24)
//	h.AddClient(120, 40)
//	w, hgt, _ := h.WindowSize() // expect 120x40 (largest)
package multiclienttmux

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Harness is the live test scaffold.
type Harness struct {
	SocketPath  string // -S <path> for every tmux command targeting this server
	SessionName string

	t  *testing.T
	mu sync.Mutex

	clients []*clientProc // pty-attached clients to clean up
}

type clientProc struct {
	cmd *exec.Cmd
	pty interface{ Close() error }
}

// New boots a fresh tmux server on a per-test isolated socket and
// creates a detached session named sessionName with aggressive-resize=on.
// The server and all spawned clients are torn down via t.Cleanup.
//
// Skips the test (via t.Skip) if the tmux binary is unavailable.
func New(t *testing.T, sessionName string) *Harness {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("multiclienttmux: tmux binary not available")
	}

	socketDir := t.TempDir()
	socketPath := filepath.Join(socketDir, "sock")

	// Detached new-session on the isolated socket. -x/-y set the initial
	// window size; clients attaching later may shrink it depending on
	// aggressive-resize.
	out, err := exec.Command("tmux", "-S", socketPath,
		"new-session", "-d", "-s", sessionName,
		"-x", "200", "-y", "60",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("multiclienttmux: new-session: %v\n%s", err, out)
	}

	// aggressive-resize=on lets the active window match the smallest
	// attached client *that's looking at it*; for cross-client size
	// regression tests this is what we want.
	if out, err := exec.Command("tmux", "-S", socketPath,
		"set-window-option", "-t", sessionName, "aggressive-resize", "on",
	).CombinedOutput(); err != nil {
		t.Fatalf("multiclienttmux: set aggressive-resize: %v\n%s", err, out)
	}

	h := &Harness{
		SocketPath:  socketPath,
		SessionName: sessionName,
		t:           t,
	}
	t.Cleanup(h.cleanup)
	return h
}

// AddClient spawns a pty-backed `tmux attach` client at the requested
// size. The pty stays alive (and the client attached) until cleanup.
func (h *Harness) AddClient(cols, rows int) error {
	cmd := exec.Command("tmux", "-S", h.SocketPath, "attach-session", "-t", h.SessionName)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return fmt.Errorf("multiclienttmux: pty.Start: %w", err)
	}

	h.mu.Lock()
	h.clients = append(h.clients, &clientProc{cmd: cmd, pty: ptmx})
	h.mu.Unlock()

	// Give tmux a beat to register the client.
	time.Sleep(100 * time.Millisecond)
	return nil
}

// WindowSize returns the active window's dimensions as tmux currently
// reports them (`tmux display -p '#{window_width}x#{window_height}'`).
func (h *Harness) WindowSize() (int, int, error) {
	out, err := exec.Command("tmux", "-S", h.SocketPath,
		"display", "-p", "-t", h.SessionName,
		"#{window_width}x#{window_height}",
	).CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("multiclienttmux: display: %w (%s)", err, out)
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("multiclienttmux: malformed display output %q", out)
	}
	w, err1 := strconv.Atoi(parts[0])
	r, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("multiclienttmux: bad numbers %q", out)
	}
	return w, r, nil
}

// ClientCount returns the number of currently attached clients per tmux.
func (h *Harness) ClientCount() (int, error) {
	out, err := exec.Command("tmux", "-S", h.SocketPath,
		"list-clients", "-t", h.SessionName,
	).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("multiclienttmux: list-clients: %w (%s)", err, out)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return 0, nil
	}
	return strings.Count(string(out), "\n"), nil
}

// cleanup tears down every client pty, then kills the server.
func (h *Harness) cleanup() {
	h.mu.Lock()
	clients := h.clients
	h.clients = nil
	h.mu.Unlock()

	for _, c := range clients {
		_ = c.pty.Close()
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		_, _ = c.cmd.Process.Wait()
	}

	// Best-effort kill-server. Errors are non-fatal (server may already
	// be gone if a client tore it down).
	_ = exec.Command("tmux", "-S", h.SocketPath, "kill-server").Run()
}
