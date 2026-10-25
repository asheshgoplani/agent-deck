package mcppool

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// shortTempDir returns a short private directory (unix socket paths are
// limited to ~104 bytes, which t.TempDir() names can exceed).
func shortTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "mp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

// isolateDataDir points HOME and XDG_DATA_HOME at short temp dirs so
// mcpSocketDir resolves inside the test sandbox.
func isolateDataDir(t *testing.T) {
	t.Helper()
	home := shortTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "d"))
}

// isolateNoDataDir makes the data dir unresolvable so code takes its temp
// fallback, and points TMPDIR at a fresh dir. Returns that TMPDIR.
func isolateNoDataDir(t *testing.T) string {
	t.Helper()
	tmp := shortTempDir(t)
	t.Setenv("HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("TMPDIR", tmp)
	return tmp
}

func listenUnixT(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
}

func assertPrivateOwnedDir(t *testing.T, dir string) {
	t.Helper()
	fi, err := os.Lstat(dir)
	if err != nil {
		t.Fatalf("lstat %s: %v", dir, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		t.Fatalf("%s is not a real directory (mode %v)", dir, fi.Mode())
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("%s mode = %v, want 0700", dir, fi.Mode().Perm())
	}
	if st := fi.Sys().(*syscall.Stat_t); int(st.Uid) != os.Getuid() {
		t.Fatalf("%s owned by uid %d, want %d", dir, st.Uid, os.Getuid())
	}
}

// A live socket of our own in the pool's private socket dir (where
// NewSocketProxy creates them) is discovered and registered.
func TestDiscoverExistingSockets_AdoptsOwnSocketInPrivateDir(t *testing.T) {
	isolateDataDir(t)
	dir := mcpSocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "mcp-own.sock")
	listenUnixT(t, sock)

	pool, _ := NewPool(context.Background(), &PoolConfig{Enabled: true})
	if n := pool.DiscoverExistingSockets(); n != 1 {
		t.Fatalf("DiscoverExistingSockets = %d, want 1", n)
	}
	if got := pool.GetSocketPath("own"); got != sock {
		t.Fatalf("GetSocketPath(own) = %q, want %q", got, sock)
	}
}

// Sockets that are symlinks, not sockets, or sit in a group/other-writable
// dir are never adopted, even when they accept connections.
func TestDiscoverExistingSockets_RejectsUnsafeSockets(t *testing.T) {
	isolateDataDir(t)
	dir := mcpSocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	elsewhere := shortTempDir(t)
	live := filepath.Join(elsewhere, "x.sock")
	listenUnixT(t, live)
	if err := os.Symlink(live, filepath.Join(dir, "mcp-link.sock")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp-file.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	pool, _ := NewPool(context.Background(), &PoolConfig{Enabled: true})
	if n := pool.DiscoverExistingSockets(); n != 0 {
		t.Fatalf("DiscoverExistingSockets = %d, want 0 (unsafe sockets adopted)", n)
	}

	// Own live socket, but the directory is world-writable: refused.
	sock := filepath.Join(dir, "mcp-open.sock")
	listenUnixT(t, sock)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if n := pool.DiscoverExistingSockets(); n != 0 {
		t.Fatalf("DiscoverExistingSockets = %d, want 0 in a world-writable dir", n)
	}
}

// With no resolvable data dir, the socket dir falls back to the per-user temp
// dir, which must be a real 0700 directory owned by us: a missing one is
// created private, a loose one is tightened, a planted symlink is refused.
func TestMCPSocketDirFallback_IsPrivateAndOwned(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		isolateNoDataDir(t)
		assertPrivateOwnedDir(t, mcpSocketDir())
	})
	t.Run("tightened", func(t *testing.T) {
		tmp := isolateNoDataDir(t)
		stable := filepath.Join(tmp, fmt.Sprintf("agentdeck-%d", os.Getuid()))
		if err := os.Mkdir(stable, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(stable, 0o777); err != nil {
			t.Fatal(err)
		}
		got := mcpSocketDir()
		if got != stable {
			t.Fatalf("mcpSocketDir = %q, want existing own dir %q kept", got, stable)
		}
		assertPrivateOwnedDir(t, got)
	})
	t.Run("symlink_refused", func(t *testing.T) {
		tmp := isolateNoDataDir(t)
		attacker := shortTempDir(t)
		stable := filepath.Join(tmp, fmt.Sprintf("agentdeck-%d", os.Getuid()))
		if err := os.Symlink(attacker, stable); err != nil {
			t.Fatal(err)
		}
		got := mcpSocketDir()
		if got == stable {
			t.Fatalf("mcpSocketDir adopted planted symlink %q", stable)
		}
		assertPrivateOwnedDir(t, got)
	})
}

// With no resolvable data dir, SocketProxy.Start must not write its log under
// the shared "<TempDir>/agent-deck" (here a symlink another user planted).
func TestSocketProxyStart_LogFallbackNotInSharedTemp(t *testing.T) {
	if _, err := os.Stat("/proc/self/cgroup"); err != nil {
		t.Skip("non-Linux")
	}
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")
	tmp := isolateNoDataDir(t)
	attacker := shortTempDir(t)
	if err := os.Symlink(attacker, filepath.Join(tmp, "agent-deck")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	proxy, err := NewSocketProxy(ctx, "logfb", "sh", []string{"-c", "sleep 30"}, nil)
	if err != nil {
		t.Fatalf("NewSocketProxy: %v", err)
	}
	t.Cleanup(func() { _ = proxy.Stop() })
	if err := proxy.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	resolved, err := filepath.EvalSymlinks(filepath.Dir(proxy.logFile))
	if err != nil {
		t.Fatal(err)
	}
	attackerResolved, _ := filepath.EvalSymlinks(attacker)
	if strings.HasPrefix(resolved+"/", attackerResolved+"/") {
		t.Fatalf("log file %s landed in planted dir %s", proxy.logFile, attacker)
	}
	rel, err := filepath.Rel(tmp, proxy.logFile)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("log file %s not under TMPDIR %s", proxy.logFile, tmp)
	}
	assertPrivateOwnedDir(t, filepath.Join(tmp, strings.Split(rel, string(filepath.Separator))[0]))
}

// A socket adopted by discovery learns its command from the later Start call,
// so RestartProxy can respawn it locally if the serving instance exits.
func TestDiscoveredSocket_StartRecordsCommandForRestart(t *testing.T) {
	isolateDataDir(t)
	dir := mcpSocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	listenUnixT(t, filepath.Join(dir, "mcp-adopt.sock"))

	pool, _ := NewPool(context.Background(), &PoolConfig{Enabled: true})
	if n := pool.DiscoverExistingSockets(); n != 1 {
		t.Fatalf("DiscoverExistingSockets = %d, want 1", n)
	}
	if err := pool.Start("adopt", "sh", []string{"-c", "sleep 30"}, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	pool.mu.RLock()
	proxy := pool.proxies["adopt"]
	pool.mu.RUnlock()
	if proxy.command != "sh" || len(proxy.args) != 2 {
		t.Fatalf("adopted proxy command = %q %v, want sh [-c sleep 30]", proxy.command, proxy.args)
	}
}
