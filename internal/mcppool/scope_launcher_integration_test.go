package mcppool

// Integration test: prove the SocketProxy spawn path actually applies
// the per-MCP scope wrapper end-to-end. Wrapper-unit tests (in
// scope_launcher_test.go) only verify the argv shape; this test catches
// regressions where the wrapper exists but is not called from the spawn
// site.

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSocketProxy_SpawnsChildInPerMCPScope verifies that after Start(),
// the MCP child PID is registered under `mcp-pool.slice/mcp-*.scope`.
// This is the regression gate for the cascade-prevention work: if the
// spawn site forgets to call wrapMCPCommand (or someone reverts the
// wrapping), the child lands back in the parent's scope and this test
// fails.
func TestSocketProxy_SpawnsChildInPerMCPScope(t *testing.T) {
	if !systemdRunAvailable() {
		t.Skip("systemd-run / user manager unavailable")
	}
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "1")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// `sh -c 'sleep 30'` stays alive long enough to read its cgroup
	// before it exits. We don't actually speak MCP protocol — the goal
	// is to exercise the spawn site, not the JSON-RPC layer.
	proxy, err := NewSocketProxy(ctx, "scopetest", "sh", []string{"-c", "sleep 30"}, nil)
	if err != nil {
		t.Fatalf("NewSocketProxy: %v", err)
	}
	t.Cleanup(func() { _ = proxy.Stop() })

	if err := proxy.Start(); err != nil {
		t.Fatalf("proxy.Start: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	var (
		cgroupContents string
		childPID       int
	)
	for time.Now().Before(deadline) {
		if proxy.mcpProcess == nil || proxy.mcpProcess.Process == nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		childPID = proxy.mcpProcess.Process.Pid
		data, err := os.ReadFile("/proc/" + strconv.Itoa(childPID) + "/cgroup")
		if err == nil {
			cgroupContents = string(data)
			if strings.Contains(cgroupContents, "mcp-pool.slice/mcp-") &&
				strings.Contains(cgroupContents, ".scope") {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	if childPID == 0 {
		t.Fatalf("child process never started")
	}
	t.Fatalf("child pid=%d cgroup did not enter mcp-pool.slice/mcp-*.scope within 3s\ncgroup contents:\n%s",
		childPID, cgroupContents)
}

// TestSocketProxy_DisabledIsolation_StaysInParentScope is the negative
// case: when AGENT_DECK_MCP_ISOLATION=0, the child must NOT be wrapped.
// The child's /proc/<pid>/comm should be 'sh' (the original command),
// not 'systemd-run'.
func TestSocketProxy_DisabledIsolation_StaysInParentScope(t *testing.T) {
	if _, err := os.Stat("/proc/self/cgroup"); err != nil {
		t.Skip("non-Linux")
	}
	t.Setenv("AGENT_DECK_MCP_ISOLATION", "0")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	proxy, err := NewSocketProxy(ctx, "scopetest-off", "sh", []string{"-c", "sleep 30"}, nil)
	if err != nil {
		t.Fatalf("NewSocketProxy: %v", err)
	}
	t.Cleanup(func() { _ = proxy.Stop() })

	if err := proxy.Start(); err != nil {
		t.Fatalf("proxy.Start: %v", err)
	}

	time.Sleep(200 * time.Millisecond)
	if proxy.mcpProcess == nil || proxy.mcpProcess.Process == nil {
		t.Fatalf("no child process")
	}

	comm, err := os.ReadFile("/proc/" + strconv.Itoa(proxy.mcpProcess.Process.Pid) + "/comm")
	if err != nil {
		t.Fatalf("read /proc/comm: %v", err)
	}
	got := strings.TrimSpace(string(comm))
	if got == "systemd-run" {
		t.Fatalf("isolation disabled but child is systemd-run; wrapper leaked: comm=%q", got)
	}
}
