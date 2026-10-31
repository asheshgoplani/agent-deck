package mcppool

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// dialRetry dials path the way agent-deck mcp-proxy does: retry until a
// listener answers or the deadline passes.
func dialRetry(t *testing.T, path string, within time.Duration) net.Conn {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		c, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("nothing answers on %s within %v: %v", path, within, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// roundTrip sends one JSON-RPC request through the pool (the MCP is cat,
// which echoes it back as the "response") and checks the reply carries the
// client's own id.
func roundTrip(t *testing.T, c net.Conn, id int) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "ping"})
	if _, err := c.Write(append(req, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("no reply through the pool: %v", err)
	}
	var resp struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(line, &resp); err != nil || resp.ID != id {
		t.Fatalf("reply %q, want id %d", line, id)
	}
}

func proxyOf(t *testing.T, p *Pool, name string) *SocketProxy {
	t.Helper()
	p.mu.RLock()
	defer p.mu.RUnlock()
	proxy := p.proxies[name]
	if proxy == nil {
		t.Fatalf("no proxy for %s", name)
	}
	return proxy
}

// Upgrade from a name-keyed version: a running session's mcp-proxy keeps
// dialing <sockdir>/mcp-<name>.sock. After the old TUI quits (its socket is
// gone, or left dead after a crash) and the new version starts, that path
// must answer again with this home's process, while new sessions get the
// definition-keyed path.
func TestUpgrade_LegacyProxyClientReconnectsToNewPool(t *testing.T) {
	for _, tc := range []struct {
		name     string
		leftDead bool
	}{{"old socket removed", false}, {"old socket left dead", true}} {
		t.Run(tc.name, func(t *testing.T) {
			isolateDataDir(t)
			t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")
			dir := mcpSocketDir()
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			legacy := filepath.Join(dir, "mcp-gh.sock")

			// Old version: pool on the name-only path, a session's
			// mcp-proxy connected to it.
			old, err := net.Listen("unix", legacy)
			if err != nil {
				t.Fatal(err)
			}
			client, err := net.Dial("unix", legacy)
			if err != nil {
				t.Fatal(err)
			}
			if tc.leftDead {
				old.(*net.UnixListener).SetUnlinkOnClose(false)
			}
			_ = old.Close()
			_ = client.Close()

			// New version starts its pool.
			pool, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
			t.Cleanup(func() { _ = pool.Shutdown() })
			if err := pool.Start("gh", "cat", nil, map[string]string{"TOKEN": "x"}); err != nil {
				t.Fatalf("Start: %v", err)
			}
			waitRunning(t, pool, "gh")
			if got := pool.GetSocketPath("gh"); got == legacy {
				t.Fatalf("new sessions get the name-only path %s", got)
			}

			// The session's mcp-proxy reconnects on the old path.
			c := dialRetry(t, legacy, 5*time.Second)
			defer c.Close()
			roundTrip(t, c, 7)

			// And the definition-keyed path serves the same process.
			c2 := dialRetry(t, pool.GetSocketPath("gh"), 5*time.Second)
			defer c2.Close()
			roundTrip(t, c2, 8)
		})
	}
}

// A live name-only socket (an old version still running, or another home
// that took it first) is left to its owner: not served, not removed.
func TestLegacyAlias_LiveOwnerKept(t *testing.T) {
	isolateDataDir(t)
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")
	dir := mcpSocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "mcp-gh.sock")
	listenUnixT(t, legacy)

	pool, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	t.Cleanup(func() { _ = pool.Shutdown() })
	if err := pool.Start("gh", "cat", nil, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitRunning(t, pool, "gh")
	if proxyOf(t, pool, "gh").legacyListener != nil {
		t.Fatal("pool took over a live name-only socket")
	}
	if !isSocketAlive(legacy) {
		t.Fatal("live name-only socket removed from under its owner")
	}
}

// Only the first definition of a name serves the name-only path; a second
// home with another definition keeps to its own definition-keyed socket and
// never takes the alias over.
func TestLegacyAlias_SecondDefinitionDoesNotTakeIt(t *testing.T) {
	isolateDataDir(t)
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")

	poolB, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	t.Cleanup(func() { _ = poolB.Shutdown() })
	if err := poolB.Start("gh", "cat", nil, map[string]string{"TOKEN": "work"}); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, poolB, "gh")
	if proxyOf(t, poolB, "gh").legacyListener == nil {
		t.Fatal("first definition does not serve the name-only path")
	}

	poolA, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	t.Cleanup(func() { _ = poolA.Shutdown() })
	if err := poolA.Start("gh", "cat", nil, map[string]string{"TOKEN": "personal"}); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, poolA, "gh")
	if proxyOf(t, poolA, "gh").legacyListener != nil {
		t.Fatal("second definition took the name-only path")
	}
	if got := ExternalSocketPath("gh", "cat", nil, map[string]string{"TOKEN": "personal"}); got != poolA.GetSocketPath("gh") {
		t.Fatalf("CLI lookup for home A's definition = %q, want %q", got, poolA.GetSocketPath("gh"))
	}
}

// After the MCP is restarted the name-only path answers again, and a stopped
// pool leaves no name-only socket file behind.
func TestLegacyAlias_RestartAndShutdown(t *testing.T) {
	isolateDataDir(t)
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")
	legacy := filepath.Join(mcpSocketDir(), "mcp-gh.sock")

	pool, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	if err := pool.Start("gh", "cat", nil, nil); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, pool, "gh")
	if err := pool.RestartProxy("gh"); err != nil {
		t.Fatalf("RestartProxy: %v", err)
	}
	waitRunning(t, pool, "gh")
	c := dialRetry(t, legacy, 5*time.Second)
	roundTrip(t, c, 3)
	_ = c.Close()

	_ = pool.Shutdown()
	if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
		t.Fatalf("name-only socket left after shutdown: %v", err)
	}
}

// Clients of the main and the name-only listener must not share a client id,
// or one's replies would go to the other.
func TestLegacyAlias_ClientIDsUnique(t *testing.T) {
	isolateDataDir(t)
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")
	pool, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	t.Cleanup(func() { _ = pool.Shutdown() })
	if err := pool.Start("gh", "cat", nil, nil); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, pool, "gh")
	a := dialRetry(t, pool.GetSocketPath("gh"), 5*time.Second)
	defer a.Close()
	b := dialRetry(t, filepath.Join(mcpSocketDir(), "mcp-gh.sock"), 5*time.Second)
	defer b.Close()
	proxy := proxyOf(t, pool, "gh")
	deadline := time.Now().Add(5 * time.Second)
	for proxy.GetClientCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("clients = %d, want 2 (one client replaced the other)", proxy.GetClientCount())
		}
		time.Sleep(20 * time.Millisecond)
	}
	roundTrip(t, a, 1)
	roundTrip(t, b, 2)
}

// macOS limits a unix socket path to 104 bytes. A long MCP name is shortened
// (prefix plus a digest of the full name) so the path fits; short names keep
// the plain "mcp-<name>-<id>.sock" form.
func TestSocketPathIn_LongNameFitsSunPath(t *testing.T) {
	dir := "/" + strings.Repeat("d", 59) // a typical /Users/<u>/.local/share/agent-deck/sockets
	long := strings.Repeat("n", 60)
	p1 := socketPathIn(dir, long+"a", "cat", nil, nil)
	p2 := socketPathIn(dir, long+"b", "cat", nil, nil)
	for _, p := range []string{p1, p2} {
		if len(p) > sunPathBudget {
			t.Fatalf("%s is %d bytes, over %d", p, len(p), sunPathBudget)
		}
		if filepath.Dir(p) != dir {
			t.Fatalf("%s not in %s", p, dir)
		}
	}
	if sunPathBudget != 103 {
		t.Fatalf("sunPathBudget = %d, want 103 (104 bytes with the NUL)", sunPathBudget)
	}
	if p1 == p2 {
		t.Fatalf("two long names share %s", p1)
	}
	if again := socketPathIn(dir, long+"a", "cat", nil, nil); again != p1 {
		t.Fatalf("not deterministic: %s vs %s", again, p1)
	}
	if other := socketPathIn(dir, long+"a", "node", nil, nil); other == p1 {
		t.Fatal("shortened path ignores the definition")
	}
	short := socketPathIn(dir, "gh", "cat", nil, nil)
	if want := filepath.Join(dir, "mcp-gh-"+socketIdentity("cat", nil, nil)+".sock"); short != want {
		t.Fatalf("short name path = %s, want %s", short, want)
	}
}

// A pooled MCP with a long name starts and answers on Linux too.
func TestPoolStart_LongName(t *testing.T) {
	isolateDataDir(t)
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")
	name := strings.Repeat("x", 100)
	pool, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	t.Cleanup(func() { _ = pool.Shutdown() })
	if err := pool.Start(name, "cat", nil, nil); err != nil {
		t.Fatalf("Start with a %d-char name: %v", len(name), err)
	}
	waitRunning(t, pool, name)
	if p := pool.GetSocketPath(name); len(p) > sunPathBudget {
		t.Fatalf("socket path %d bytes", len(p))
	}
}

// When even a shortened name cannot fit, the proxy refuses with a clear
// error before spawning the MCP.
func TestNewSocketProxy_SocketDirTooLong(t *testing.T) {
	home := shortTempDir(t)
	deep := filepath.Join(home, strings.Repeat("p", 90))
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", deep)
	_, err := NewSocketProxy(context.Background(), "gh", "cat", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("err = %v, want a socket path too long error", err)
	}
}

// Two homes running the same name with different definitions get their own
// log file each.
func TestSocketProxy_LogFilePerDefinition(t *testing.T) {
	isolateDataDir(t)
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")
	a, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	t.Cleanup(func() { _ = a.Shutdown() })
	b, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	t.Cleanup(func() { _ = b.Shutdown() })
	if err := a.Start("gh", "cat", nil, map[string]string{"T": "1"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Start("gh", "cat", nil, map[string]string{"T": "2"}); err != nil {
		t.Fatal(err)
	}
	la, lb := proxyOf(t, a, "gh").logFile, proxyOf(t, b, "gh").logFile
	if la == "" || la == lb {
		t.Fatalf("both definitions log to %q", la)
	}
}

// listenFullBacklog makes a unix socket whose accept queue is full: a dial
// fails without ECONNREFUSED, like a live owner too busy to accept in time.
func listenFullBacklog(t *testing.T, path string) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		c, err := net.DialTimeout("unix", path, 200*time.Millisecond)
		if err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) {
				t.Skipf("full backlog reports ECONNREFUSED here: %v", err)
			}
			return
		}
		t.Cleanup(func() { _ = c.Close() })
	}
	t.Skip("could not fill the accept queue")
}

func deadSocketT(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
}

// Discovery removes only dead (connection refused) legacy and own candidate
// sockets; other homes' sockets and a busy live one are left alone.
func TestDiscoverExistingSockets_RemovesOnlyOwnRefused(t *testing.T) {
	isolateDataDir(t)
	dir := mcpSocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := SocketSpec{Command: "cat"}
	ownDead := filepath.Join(dir, SocketFileName("gh", spec.Command, nil, nil))
	legacyDead := filepath.Join(dir, "mcp-gh.sock")
	otherDead := filepath.Join(dir, SocketFileName("other", "cat", nil, nil))
	busy := filepath.Join(dir, SocketFileName("busy", "cat", nil, nil))
	deadSocketT(t, ownDead)
	deadSocketT(t, legacyDead)
	deadSocketT(t, otherDead)
	listenFullBacklog(t, busy)

	pool, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	pool.DiscoverExistingSockets(map[string]SocketSpec{"gh": spec, "busy": {Command: "cat"}})

	for path, wantGone := range map[string]bool{ownDead: true, legacyDead: true, otherDead: false, busy: false} {
		_, err := os.Lstat(path)
		if gone := os.IsNotExist(err); gone != wantGone {
			t.Errorf("%s: removed=%v, want %v", filepath.Base(path), gone, wantGone)
		}
	}
}
