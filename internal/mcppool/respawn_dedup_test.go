package mcppool

// Cascade-trigger regression for v1.9 — duplicate MCP-child accumulation.
//
// Background: on 2026-05-08, a conductor scope was killed by systemd-oomd
// after accumulating 43 simultaneous instances of @upstash/context7-mcp at
// 10:48 CEST. Root-cause analysis is in /tmp/worker-root-cause/RESULTS.md.
// This file is the agent-deck-side reproducer: after a session-style
// restart loop, exactly ONE live MCP child must remain per (pool, name).

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveDirectChildrenMatching returns the count of running (non-zombie) processes
// whose PPid is ppid and whose /proc/<pid>/comm matches comm. Linux-only.
func liveDirectChildrenMatching(t *testing.T, ppid int, comm string) []int {
	t.Helper()
	if _, err := os.Stat("/proc"); err != nil {
		t.Skipf("/proc unavailable — non-Linux: %v", err)
	}
	entries, err := os.ReadDir("/proc")
	require.NoError(t, err)
	var pids []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err != nil {
			continue
		}
		statusBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		var (
			parent  int
			isZomb  bool
			procCmd string
		)
		for _, line := range bytes.Split(statusBytes, []byte{'\n'}) {
			switch {
			case bytes.HasPrefix(line, []byte("PPid:")):
				_, _ = fmt.Sscanf(string(line), "PPid:\t%d", &parent)
			case bytes.HasPrefix(line, []byte("Name:")):
				procCmd = string(bytes.TrimSpace(bytes.TrimPrefix(line, []byte("Name:"))))
			case bytes.HasPrefix(line, []byte("State:")) && bytes.Contains(line, []byte("zombie")):
				isZomb = true
			}
		}
		if parent == ppid && !isZomb && procCmd == comm {
			pids = append(pids, pid)
		}
	}
	return pids
}

// TestPool_RestartLoop_ProducesOneChild — five Pool.RestartProxy cycles must
// leave exactly one live SocketProxy child. Hypothesis (a)/(b) from the
// cascade brief: if Pool's lookup or restart cleanup is racy, repeated
// restarts will leak children.
func TestPool_RestartLoop_ProducesOneChild(t *testing.T) {
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("/proc unavailable — non-Linux")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := NewPool(ctx, &PoolConfig{Enabled: true, PoolAll: true, FallbackStdio: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Shutdown() })

	// Long-lived child that idles on stdin — stands in for an MCP server.
	const name = "dedup-test"
	cmd := []string{"-c", "while read line; do echo $line; done"}

	baselineLive := len(liveDirectChildrenMatching(t, os.Getpid(), "sh"))
	require.NoError(t, pool.Start(name, "sh", cmd, nil))

	// Wait until child actually exists.
	require.Eventually(t, func() bool {
		return len(liveDirectChildrenMatching(t, os.Getpid(), "sh"))-baselineLive == 1
	}, 2*time.Second, 20*time.Millisecond, "pool failed to spawn initial child")

	for i := 0; i < 5; i++ {
		require.NoError(t, pool.RestartProxy(name), "restart #%d", i+1)
	}

	// Settle — give any leaked children time to be observable.
	time.Sleep(300 * time.Millisecond)

	live := liveDirectChildrenMatching(t, os.Getpid(), "sh")
	delta := len(live) - baselineLive
	assert.Equal(t, 1, delta,
		"expected exactly 1 live MCP child after 5 restarts (baseline=%d, observed=%v)",
		baselineLive, live)
}

// TestPool_ConcurrentStarts_ProduceOneChild — N concurrent Pool.Start calls
// for the same MCP must coalesce to ONE child. Hypothesis (e): race in the
// attach + start path lets multiple plugin loads spawn duplicates.
func TestPool_ConcurrentStarts_ProduceOneChild(t *testing.T) {
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("/proc unavailable — non-Linux")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := NewPool(ctx, &PoolConfig{Enabled: true, PoolAll: true, FallbackStdio: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Shutdown() })

	const name = "concurrent-start-test"
	cmd := []string{"-c", "while read line; do echo $line; done"}

	baselineLive := len(liveDirectChildrenMatching(t, os.Getpid(), "sh"))

	const N = 8
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() { errs <- pool.Start(name, "sh", cmd, nil) }()
	}
	for i := 0; i < N; i++ {
		require.NoError(t, <-errs)
	}

	time.Sleep(300 * time.Millisecond)
	live := liveDirectChildrenMatching(t, os.Getpid(), "sh")
	delta := len(live) - baselineLive
	assert.Equal(t, 1, delta,
		"expected exactly 1 live MCP child after %d concurrent Starts (baseline=%d, observed=%v)",
		N, baselineLive, live)
}

// TestHTTPPool_ConcurrentStarts_ProduceOneChild — N concurrent HTTPPool.Start
// calls for the same MCP must coalesce to ONE child. The HTTPPool releases
// its mutex BEFORE calling server.Start, and HTTPServer.Start only
// early-returns on StatusRunning — not on StatusStarting. Two concurrent
// callers can both observe StatusStarting and both spawn processes,
// orphaning the first child (s.process is overwritten by the second).
func TestHTTPPool_ConcurrentStarts_ProduceOneChild(t *testing.T) {
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("/proc unavailable — non-Linux")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool := NewHTTPPool(ctx)
	t.Cleanup(func() { _ = pool.Shutdown() })

	const name = "http-concurrent-test"
	// A fake server that listens on a port we'll reach via health-check;
	// using `sleep` is enough — the health probe will fail until timeout,
	// but BOTH concurrent Starts will spawn a process before that happens.
	// Use a unique sentinel so the proc-walker can find our children.
	cmd := "sleep"
	args := []string{"30"}

	baselineLive := len(liveDirectChildrenMatching(t, os.Getpid(), "sleep"))

	const N = 8
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			// 50 ms startup timeout — Start will return failure quickly,
			// but we only care about how many sleep-children are alive.
			errs <- pool.Start(name, "http://127.0.0.1:1/", "http://127.0.0.1:1/",
				cmd, args, nil, 50*time.Millisecond)
		}()
	}
	for i := 0; i < N; i++ {
		<-errs // ignore err — both success and timeout are fine
	}

	// Settle: give any leaked children a moment to be observable.
	time.Sleep(200 * time.Millisecond)

	live := liveDirectChildrenMatching(t, os.Getpid(), "sleep")
	delta := len(live) - baselineLive
	assert.LessOrEqual(t, delta, 1,
		"expected at most 1 live MCP child after %d concurrent HTTPPool Starts (baseline=%d, observed=%v)",
		N, baselineLive, live)
}
