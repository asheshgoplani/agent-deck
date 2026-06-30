package sysinfo

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestCollectorStart_NonBlocking guards the regression where the initial stats
// collection ran synchronously inside Start(). The TUI calls Start() from
// Home.Init, which Bubble Tea runs before the first paint, so a wedged
// `netstat -ib` (a real macOS pathology with flapping VPN utun* interfaces)
// would freeze the UI on a blank, input-dead screen until netstat returned.
// Start() must spawn its work and return immediately regardless of how slow a
// stat source is.
func TestCollectorStart_NonBlocking(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("network collection only shells out to netstat on darwin")
	}

	// A fake netstat that hangs, placed first on PATH.
	dir := t.TempDir()
	fake := filepath.Join(dir, "netstat")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	c := NewCollector(5, nil)
	defer c.Stop()

	done := make(chan struct{})
	go func() {
		c.Start()
		close(done)
	}()

	select {
	case <-done:
		// Start() returned promptly despite the wedged netstat — correct.
	case <-time.After(3 * time.Second):
		t.Fatal("Collector.Start() blocked on a slow netstat; it must be non-blocking so the TUI can paint")
	}
}

// TestCollectNetworkDarwin_TimesOut verifies that a wedged netstat is bounded
// by the context timeout rather than hanging the collector goroutine forever
// (which also leaked zombie netstat processes across launches).
func TestCollectNetworkDarwin_TimesOut(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("collectNetworkDarwin only runs on darwin")
	}

	dir := t.TempDir()
	fake := filepath.Join(dir, "netstat")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	start := time.Now()
	_ = collectNetworkDarwin() // should return ~timeout, not after 30s
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("collectNetworkDarwin took %s; expected it to be bounded by the ~2s context timeout", elapsed)
	}
}
