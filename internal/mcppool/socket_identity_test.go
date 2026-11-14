package mcppool

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// #2966: two config homes of one user share the pool socket dir. When both
// pool the same MCP name with a different definition (here: a different env,
// as with a work and a personal GitHub token), home A must start its own
// process on its own socket and never reuse home B's.
func TestPoolStart_DifferentDefinitionGetsOwnSocketAndProcess(t *testing.T) {
	isolateDataDir(t)
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")

	poolB, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	t.Cleanup(func() { _ = poolB.Shutdown() })
	if err := poolB.Start("gh", "cat", nil, map[string]string{"TOKEN": "work"}); err != nil {
		t.Fatalf("home B Start: %v", err)
	}
	waitRunning(t, poolB, "gh")

	poolA, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	t.Cleanup(func() { _ = poolA.Shutdown() })
	if err := poolA.Start("gh", "cat", nil, map[string]string{"TOKEN": "personal"}); err != nil {
		t.Fatalf("home A Start: %v", err)
	}
	waitRunning(t, poolA, "gh")

	if a, b := poolA.GetSocketPath("gh"), poolB.GetSocketPath("gh"); a == b {
		t.Fatalf("home A reuses home B's socket %s for a different definition", b)
	}
	poolA.mu.RLock()
	own := poolA.proxies["gh"].mcpProcess != nil
	poolA.mu.RUnlock()
	if !own {
		t.Fatal("home A did not start its own process for its own definition")
	}
}

// The same definition in two homes still shares one process and socket.
func TestPoolStart_SameDefinitionReusesSocket(t *testing.T) {
	isolateDataDir(t)
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")
	env := map[string]string{"TOKEN": "same"}

	poolB, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	t.Cleanup(func() { _ = poolB.Shutdown() })
	if err := poolB.Start("gh", "cat", []string{"-u"}, env); err != nil {
		t.Fatalf("home B Start: %v", err)
	}
	waitRunning(t, poolB, "gh")

	poolA, _ := NewPool(context.Background(), &PoolConfig{Enabled: true, PoolAll: true})
	t.Cleanup(func() { _ = poolA.Shutdown() })
	if err := poolA.Start("gh", "cat", []string{"-u"}, map[string]string{"TOKEN": "same"}); err != nil {
		t.Fatalf("home A Start: %v", err)
	}
	if a, b := poolA.GetSocketPath("gh"), poolB.GetSocketPath("gh"); a != b {
		t.Fatalf("same definition got a second socket: A %s, B %s", a, b)
	}
	poolA.mu.RLock()
	own := poolA.proxies["gh"].mcpProcess != nil
	poolA.mu.RUnlock()
	if own {
		t.Fatal("same definition started a duplicate process")
	}
}

// TUI startup: discovery adopts another home's live socket only when it
// serves this home's definition of that name.
func TestDiscoverExistingSockets_OnlyMatchingDefinition(t *testing.T) {
	isolateDataDir(t)
	dir := mcpSocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	work := SocketSpec{Command: "gh-mcp", Env: map[string]string{"TOKEN": "work"}}
	listenUnixT(t, filepath.Join(dir, SocketFileName("gh", work.Command, work.Args, work.Env)))

	poolA, _ := NewPool(context.Background(), &PoolConfig{Enabled: true})
	personal := SocketSpec{Command: "gh-mcp", Env: map[string]string{"TOKEN": "personal"}}
	if n := poolA.DiscoverExistingSockets(map[string]SocketSpec{"gh": personal}); n != 0 {
		t.Fatalf("discovery adopted another home's socket for a different definition (n=%d)", n)
	}
	if poolA.IsRunning("gh") {
		t.Fatal("gh counts as running in home A, so home A would never start its own")
	}

	if n := poolA.DiscoverExistingSockets(map[string]SocketSpec{"gh": work}); n != 1 {
		t.Fatalf("discovery = %d for the same definition, want 1", n)
	}
}

// Upgrade path: a live pre-#2966 name-only socket ("mcp-<name>.sock") is not
// adopted (its definition is unknown) and not removed; this instance starts
// its own socket beside it.
func TestLegacyNameOnlySocket_NotAdoptedNotRemoved(t *testing.T) {
	isolateDataDir(t)
	dir := mcpSocketDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(dir, "mcp-gh.sock")
	listenUnixT(t, legacy)

	pool, _ := NewPool(context.Background(), &PoolConfig{Enabled: true})
	if n := pool.DiscoverExistingSockets(map[string]SocketSpec{"gh": {Command: "cat"}}); n != 0 {
		t.Fatalf("discovery adopted a name-only legacy socket (n=%d)", n)
	}
	p, err := NewSocketProxy(context.Background(), "gh", "cat", nil, nil)
	if err != nil {
		t.Fatalf("NewSocketProxy: %v", err)
	}
	defer func() { _ = p.Stop() }()
	if p.GetStatus() == StatusRunning || p.GetSocketPath() == legacy {
		t.Fatalf("legacy socket reused: status %v path %s", p.GetStatus(), p.GetSocketPath())
	}
	if !isSocketAlive(legacy) {
		t.Fatal("live legacy socket was removed from under its process")
	}
}

func TestSocketIdentity(t *testing.T) {
	base := socketIdentity("npx", []string{"-y", "srv"}, map[string]string{"A": "1", "B": "2"})
	if got := socketIdentity("npx", []string{"-y", "srv"}, map[string]string{"B": "2", "A": "1"}); got != base {
		t.Fatalf("identity depends on env map order: %s vs %s", got, base)
	}
	differs := map[string]string{
		"command":    socketIdentity("node", []string{"-y", "srv"}, map[string]string{"A": "1", "B": "2"}),
		"args":       socketIdentity("npx", []string{"-y srv"}, map[string]string{"A": "1", "B": "2"}),
		"env value":  socketIdentity("npx", []string{"-y", "srv"}, map[string]string{"A": "1", "B": "3"}),
		"env key":    socketIdentity("npx", []string{"-y", "srv"}, map[string]string{"A": "1", "C": "2"}),
		"env absent": socketIdentity("npx", []string{"-y", "srv"}, map[string]string{"A": "1"}),
	}
	for what, id := range differs {
		if id == base {
			t.Errorf("identity unchanged when %s changes", what)
		}
	}
	if len(base) != 12 {
		t.Errorf("identity length = %d, want 12", len(base))
	}
}

func waitRunning(t *testing.T, p *Pool, name string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !p.IsRunning(name) {
		if time.Now().After(deadline) {
			t.Fatalf("%s not running within 5s", name)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
