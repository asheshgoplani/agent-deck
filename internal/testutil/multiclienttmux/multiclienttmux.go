// Package multiclienttmux boots an isolated tmux server with Agent Deck's
// multi-client size policy (window-size=latest, aggressive-resize=on: the
// window follows the client that last attached, typed or resized), then lets
// a test attach N pty clients at chosen sizes — the harness from
// TEST-PLAN.md §6.1 / TUI-TEST-PLAN.md §6.8 for the "two web clients
// hijacking pane size" regression (J4 / F2).
//
// Every harness instance gets its own socket under a short isolated temp
// dir, never touching the user's real tmux server. Cleanup tears down the server,
// kills all spawned client processes, and removes the socket.
//
// Usage:
//
//	h := multiclienttmux.New(t, "myscratch")
//	h.AddClient(100, 62)
//	h.AddClient(189, 62)
//	h.ResizeClient(0, 88, 71)
//	w, hgt, _ := h.WindowSize() // expect 88x70 (the resized client, minus status row)
package multiclienttmux

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
	"github.com/creack/pty"
)

// Harness is the live test scaffold.
type Harness struct {
	SocketPath  string // -S <path> for every tmux command targeting this server
	SessionName string
	// SocketName is the server's `tmux -L` name when it was booted by
	// NewNamed, "" otherwise.
	SocketName string

	t  *testing.T
	mu sync.Mutex

	clients []*clientProc // pty-attached clients to clean up
}

type clientProc struct {
	cmd *exec.Cmd
	pty *os.File
}

// New boots a fresh tmux server on a per-test isolated socket and creates a
// detached session named sessionName with Agent Deck's multi-client sizing
// defaults.
// The server and all spawned clients are torn down via t.Cleanup.
//
// Skips the test (via t.Skip) if the tmux binary is unavailable.
func New(t *testing.T, sessionName string) *Harness {
	t.Helper()
	skipWithoutTmux(t)

	// Use a short /tmp-based socket path: t.TempDir() on darwin resolves under
	// /var/folders/<hash>/T/<TestName>... and overshoots the sun_path 104-byte
	// limit for long test names ("File name too long").
	socketPath, sockCleanup := testutil.ShortTmuxSocket()
	t.Cleanup(sockCleanup)
	return boot(t, sessionName, []string{"-S", socketPath}, socketPath, "")
}

// NewNamed is New on a server addressed by name (`tmux -L <name>`, under the
// test binary's TMUX_TMPDIR), the way Agent Deck addresses its own servers,
// so a test can point production code (a control pipe, a PipeManager) at the
// harness through SocketName. Every harness method still targets SocketPath.
func NewNamed(t *testing.T, sessionName string) *Harness {
	t.Helper()
	skipWithoutTmux(t)
	name := fmt.Sprintf("mct-%d-%d", os.Getpid(), time.Now().UnixNano())
	return boot(t, sessionName, []string{"-L", name}, "", name)
}

func skipWithoutTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("multiclienttmux: tmux binary not available")
	}
}

// boot creates the session on the server selected by selector and applies
// the sizing policy. socketPath may be empty when the selector is a -L name;
// it is then read back from the server.
func boot(t *testing.T, sessionName string, selector []string, socketPath, socketName string) *Harness {
	t.Helper()

	// Detached new-session on the isolated socket. -x/-y set the initial
	// window size; clients attaching later may shrink it depending on
	// aggressive-resize.
	args := append(append([]string{}, selector...), "new-session", "-d", "-s", sessionName, "-x", "200", "-y", "60")
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("multiclienttmux: new-session: %v\n%s", err, out)
	}
	if socketPath == "" {
		args = append(append([]string{}, selector...), "display-message", "-p", "#{socket_path}")
		out, err := exec.Command("tmux", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("multiclienttmux: read socket path: %v\n%s", err, out)
		}
		socketPath = strings.TrimSpace(string(out))
	}

	// Explicitly mirror Session.Start (internal/tmux sharedview.go) rather
	// than trusting the server default, which is `smallest` on tmux < 3.1.
	if out, err := exec.Command("tmux", "-S", socketPath,
		"set-option", "-w", "-t", sessionName, "window-size", "latest", ";",
		"set-option", "-w", "-t", sessionName, "aggressive-resize", "on",
	).CombinedOutput(); err != nil {
		t.Fatalf("multiclienttmux: set window-size/aggressive-resize: %v\n%s", err, out)
	}

	h := &Harness{
		SocketPath:  socketPath,
		SessionName: sessionName,
		SocketName:  socketName,
		t:           t,
	}
	t.Cleanup(h.cleanup)
	return h
}

// AddClient spawns a pty-backed `tmux attach` client at the requested
// size. The pty stays alive (and the client attached) until cleanup.
func (h *Harness) AddClient(cols, rows int) error {
	cmd := exec.Command("tmux", "-S", h.SocketPath, "attach-session", "-t", h.SessionName)
	// A tmux client needs a terminal type; a headless runner (CI, Docker)
	// may have none, and the client would exit at once with "terminal does
	// not support clear", leaving the window at its birth size.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) // #nosec G115 -- test helper, sizes provided by caller fit uint16
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

// ResizeClient changes an attached client's PTY dimensions and waits briefly
// for tmux to process the resulting SIGWINCH.
func (h *Harness) ResizeClient(index, cols, rows int) error {
	if cols < 1 || cols > math.MaxUint16 || rows < 1 || rows > math.MaxUint16 {
		return fmt.Errorf("multiclienttmux: dimensions out of range: cols=%d rows=%d (want 1..%d)", cols, rows, math.MaxUint16)
	}

	h.mu.Lock()
	if index < 0 || index >= len(h.clients) || h.clients[index] == nil {
		h.mu.Unlock()
		return fmt.Errorf("multiclienttmux: client index %d out of range", index)
	}
	clientPTY := h.clients[index].pty
	h.mu.Unlock()

	if err := pty.Setsize(clientPTY, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		return fmt.Errorf("multiclienttmux: pty.Setsize: %w", err)
	}
	time.Sleep(100 * time.Millisecond)
	return nil
}

// DetachClient ends an attached client (its terminal closes, as when a
// person closes the window) and waits for tmux to drop it.
func (h *Harness) DetachClient(index int) error {
	h.mu.Lock()
	if index < 0 || index >= len(h.clients) || h.clients[index] == nil {
		h.mu.Unlock()
		return fmt.Errorf("multiclienttmux: client index %d out of range", index)
	}
	c := h.clients[index]
	h.clients[index] = nil
	h.mu.Unlock()

	c.close()
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
		if c != nil {
			c.close()
		}
	}

	// Best-effort kill-server. Errors are non-fatal (server may already
	// be gone if a client tore it down).
	_ = exec.Command("tmux", "-S", h.SocketPath, "kill-server").Run()
}

func (c *clientProc) close() {
	_ = c.pty.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_, _ = c.cmd.Process.Wait()
	}
}
