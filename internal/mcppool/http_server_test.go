package mcppool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHTTPServer_ExternalServer(t *testing.T) {
	// Start a test HTTP server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Create HTTPServer pointing to external server (no command)
	server := NewHTTPServer(context.Background(), "test", ts.URL, ts.URL, "", nil, nil, time.Second)

	// Start should detect the external server
	if err := server.Start(); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Should be running
	if !server.IsRunning() {
		t.Error("Expected server to be running")
	}

	// Should NOT be started by us
	if server.StartedByUs() {
		t.Error("Expected StartedByUs() to be false for external server")
	}

	// Stop should work
	if err := server.Stop(); err != nil {
		t.Fatalf("Stop() failed: %v", err)
	}

	// Should be stopped
	if server.GetStatus() != StatusStopped {
		t.Errorf("Expected status Stopped, got %v", server.GetStatus())
	}
}

func TestHTTPPool_RegisterExternal(t *testing.T) {
	// Start a test HTTP server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	pool := NewHTTPPool(context.Background())

	// Register external server
	if err := pool.RegisterExternal("test", ts.URL); err != nil {
		t.Fatalf("RegisterExternal() failed: %v", err)
	}

	// Should be running
	if !pool.IsRunning("test") {
		t.Error("Expected server to be running after registration")
	}

	// Get server
	server := pool.GetServer("test")
	if server == nil {
		t.Fatal("GetServer() returned nil")
	}

	// Should NOT be started by us
	if server.StartedByUs() {
		t.Error("Expected StartedByUs() to be false")
	}

	// List servers
	servers := pool.ListServers()
	if len(servers) != 1 {
		t.Errorf("Expected 1 server, got %d", len(servers))
	}

	// Shutdown
	if err := pool.Shutdown(); err != nil {
		t.Fatalf("Shutdown() failed: %v", err)
	}
}

func TestHTTPPool_StartStop(t *testing.T) {
	// Start a test HTTP server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	pool := NewHTTPPool(context.Background())

	// Start with external URL (no command means it will discover external)
	err := pool.Start("test", ts.URL, ts.URL, "", nil, nil, time.Second)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Should be running
	if !pool.IsRunning("test") {
		t.Error("Expected server to be running")
	}

	// Get running count
	if count := pool.GetRunningCount(); count != 1 {
		t.Errorf("Expected 1 running, got %d", count)
	}

	// Shutdown
	if err := pool.Shutdown(); err != nil {
		t.Fatalf("Shutdown() failed: %v", err)
	}
}

// markerHealthServer answers 200 only while marker exists, so the URL is
// unreachable until the fake MCP command (fakeMCPArgs) has spawned.
func markerHealthServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "up")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(marker); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	return ts, marker
}

func fakeMCPArgs(marker string) []string {
	return []string{"-c", `touch "$0"; exec sleep 30`, marker}
}

// TestHTTPPool_StartAfterStop_RespawnsServer: HTTPPool.Stop cancels the
// server's context and HTTPPool.Start reuses the same *HTTPServer, so the
// respawn must not run on the cancelled context ("context canceled").
func TestHTTPPool_StartAfterStop_RespawnsServer(t *testing.T) {
	ts, marker := markerHealthServer(t)

	pool := NewHTTPPool(context.Background())
	defer func() { _ = pool.Shutdown() }()

	for i := 1; i <= 2; i++ {
		if err := pool.Start("fake", ts.URL, ts.URL, "sh", fakeMCPArgs(marker), nil, 5*time.Second); err != nil {
			t.Fatalf("start #%d: %v", i, err)
		}
		server := pool.GetServer("fake")
		if !server.IsRunning() || !server.StartedByUs() {
			t.Fatalf("start #%d: running=%v startedByUs=%v, want both true", i, server.IsRunning(), server.StartedByUs())
		}
		if err := pool.Stop("fake"); err != nil {
			t.Fatalf("stop #%d: %v", i, err)
		}
		if err := os.Remove(marker); err != nil {
			t.Fatalf("remove marker: %v", err)
		}
	}
}

// TestHTTPServer_StartAfterParentCancel_Fails: the per-Start context still
// derives from the parent, so a server whose pool has shut down stays down.
func TestHTTPServer_StartAfterParentCancel_Fails(t *testing.T) {
	ts, marker := markerHealthServer(t)

	parent, cancel := context.WithCancel(context.Background())
	server := NewHTTPServer(parent, "fake", ts.URL, ts.URL, "sh", fakeMCPArgs(marker), nil, 5*time.Second)
	cancel()
	_ = server.Stop()

	if err := server.Start(); err == nil {
		_ = server.Stop()
		t.Fatal("Start after parent cancel succeeded, want error")
	}
}
