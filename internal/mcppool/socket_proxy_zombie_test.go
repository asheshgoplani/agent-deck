//go:build integration

package mcppool

// Zombie-reap regression for issue #677 — SocketProxy MCP process exits.
//
// Before v1.7.43, broadcastResponses() detected stdout EOF (MCP process
// died) and marked the proxy as Failed, but never called mcpProcess.Wait().
// The zombie lingered until Stop()/Restart() was invoked, which for idle
// or rarely-triggered MCPs may be never. Observed: 10+ `npm exec` / `uv`
// zombies on a long-lived TUI conductor.

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

func countZombieChildrenOfPid(t *testing.T, ppid int) int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skipf("cannot read /proc (non-Linux?): %v", err)
		return 0
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err != nil {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		var (
			parent int
			zombie bool
		)
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			if bytes.HasPrefix(line, []byte("PPid:")) {
				_, _ = fmt.Sscanf(string(line), "PPid:\t%d", &parent)
			} else if bytes.HasPrefix(line, []byte("State:")) && bytes.Contains(line, []byte("zombie")) {
				zombie = true
			}
		}
		if zombie && parent == ppid {
			count++
		}
	}
	return count
}

// TestSocketProxy_NoZombie_OnProcessExit launches a proxy whose child
// process exits quickly on its own. broadcastResponses must reap before
// Stop() is ever called; otherwise a zombie remains.
func TestSocketProxy_NoZombie_OnProcessExit(t *testing.T) {
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("/proc unavailable — non-Linux")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	baseline := countZombieChildrenOfPid(t, os.Getpid())

	// `sh -c "exit 0"` stands in for an MCP server that fails to boot —
	// same leak class as an npm/uv MCP that dies after startup (#677).
	const cycles = 15
	for i := 0; i < cycles; i++ {
		name := fmt.Sprintf("zombietest-%d", i)
		proxy, err := NewSocketProxy(ctx, name, "sh", []string{"-c", "exit 0"}, nil)
		require.NoError(t, err)
		// Start will launch the subprocess; it exits ~immediately, which
		// trips broadcastResponses EOF and must reap without waiting for
		// Stop.
		_ = proxy.Start()

		// Wait for scanner to see EOF and reaper to run.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if proxy.GetStatus() == StatusFailed {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		// Intentionally do NOT call proxy.Stop() — the point is that the
		// EOF-path reaper works without explicit Stop().
	}

	// Allow any trailing reap goroutines to complete.
	time.Sleep(300 * time.Millisecond)

	got := countZombieChildrenOfPid(t, os.Getpid()) - baseline
	assert.LessOrEqual(t, got, 0, "zombie children grew by %d after %d proxy cycles (baseline=%d)", got, cycles, baseline)
}
