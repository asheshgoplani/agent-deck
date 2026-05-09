package mcppool

// FD-leak regression for V1.9 T5 / critical-hunt #3 + #4.
//
// Before the v1.9 fix, SocketProxy.Start() and HTTPServer.Start() created
// `p.logWriter` via os.Create() and assigned it to the struct field, but
// returned an error before the proxy was registered for Stop(). The caller
// had no Stop() to call, so the log file's FD was leaked. Repeated MCP
// attach failures (a normal pattern when an external MCP is misconfigured)
// would drain the per-process FD budget over hours.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// listOwnLogFDs returns paths of FDs whose readlink target contains the
// given substring. Useful for asserting which FDs leaked when total counts
// drift due to Go runtime activity.
func listOwnLogFDs(t *testing.T, substring string) []string {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot read /proc/self/fd: %v", err)
		return nil
	}
	var out []string
	for _, e := range entries {
		target, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err != nil {
			continue
		}
		if strings.Contains(target, substring) {
			out = append(out, target)
		}
	}
	return out
}

// TestSocketProxy_Start_FailingCommand_NoFDLeak proves SocketProxy.Start()
// does not leak the log file FD when the configured MCP command fails to
// exec. Caller never calls Stop() because Start() returned an error.
// (V1.9 T5, critical-hunt #3.)
func TestSocketProxy_Start_FailingCommand_NoFDLeak(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("/proc/self/fd unavailable — non-Linux")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const cycles = 200
	const logSubstring = ".agent-deck/logs/mcppool/"

	// Prime once so any one-shot Go runtime FDs (proc readers, etc.) settle.
	primer, _ := NewSocketProxy(ctx, "fdleak-prime", "/no/such/binary-prime", nil, nil)
	_ = primer.Start()

	baseline := len(listOwnLogFDs(t, logSubstring))

	for i := 0; i < cycles; i++ {
		name := fmt.Sprintf("fdleak-%d", i)
		proxy, err := NewSocketProxy(ctx, name, "/no/such/binary", nil, nil)
		if err != nil {
			t.Fatalf("NewSocketProxy: %v", err)
		}
		// Expected to fail at process Start; logWriter was created earlier
		// in Start() and must not leak when this returns an error.
		startErr := proxy.Start()
		if startErr == nil {
			t.Fatalf("expected Start to fail for /no/such/binary on cycle %d", i)
		}
		// Intentionally do NOT call Stop() — the contract under test is
		// that a failed Start() leaves no resources for the caller to
		// clean up.
	}
	// Let any trailing goroutines settle.
	time.Sleep(100 * time.Millisecond)

	got := len(listOwnLogFDs(t, logSubstring)) - baseline
	// Allow a small slack for any unrelated activity, but a per-cycle
	// leak would push this to >> cycles/2.
	if got > 5 {
		t.Fatalf("socket-proxy log FDs grew by %d after %d failed Start cycles (baseline=%d); per-cycle leak strongly indicated", got, cycles, baseline)
	}
}

// TestHTTPServer_Start_FailingCommand_NoFDLeak proves HTTPServer.Start()
// does not leak the log file FD when its underlying process fails to
// exec. (V1.9 T5, critical-hunt #4.)
func TestHTTPServer_Start_FailingCommand_NoFDLeak(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("/proc/self/fd unavailable — non-Linux")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const cycles = 200
	const logSubstring = ".agent-deck/logs/http-servers/"

	// Prime to settle any one-shot runtime FDs.
	primer := NewHTTPServer(ctx, "fdleak-http-prime", "http://127.0.0.1:1", "http://127.0.0.1:1", "/no/such/binary-prime", nil, nil, 100*time.Millisecond)
	_ = primer.Start()

	baseline := len(listOwnLogFDs(t, logSubstring))

	for i := 0; i < cycles; i++ {
		name := fmt.Sprintf("fdleak-http-%d", i)
		server := NewHTTPServer(
			ctx, name,
			"http://127.0.0.1:1", "http://127.0.0.1:1",
			"/no/such/binary", nil, nil,
			100*time.Millisecond,
		)
		startErr := server.Start()
		if startErr == nil {
			t.Fatalf("expected Start to fail for /no/such/binary on cycle %d", i)
		}
	}
	time.Sleep(100 * time.Millisecond)

	got := len(listOwnLogFDs(t, logSubstring)) - baseline
	if got > 5 {
		t.Fatalf("http-server log FDs grew by %d after %d failed Start cycles (baseline=%d); per-cycle leak strongly indicated", got, cycles, baseline)
	}
}
