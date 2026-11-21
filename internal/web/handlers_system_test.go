package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestHandleSystemStats_OK(t *testing.T) {
	s := &Server{cfg: Config{}}
	req := httptest.NewRequest(http.MethodGet, "/api/system/stats", nil)
	w := httptest.NewRecorder()

	s.handleSystemStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// On a real system, at least memory should be available
	if _, ok := resp["memory"]; !ok {
		t.Log("warning: memory not available in test environment")
	}
}

func TestHandleSystemStats_MethodNotAllowed(t *testing.T) {
	s := &Server{cfg: Config{}}
	req := httptest.NewRequest(http.MethodPost, "/api/system/stats", nil)
	w := httptest.NewRecorder()

	s.handleSystemStats(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleSystemStats_Unauthorized(t *testing.T) {
	s := &Server{cfg: Config{Token: "secret"}}
	req := httptest.NewRequest(http.MethodGet, "/api/system/stats", nil)
	w := httptest.NewRecorder()

	s.handleSystemStats(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// Every open tab polls this endpoint. With a slow GPU tool, a probe per
// request piled up processes and requests, so polls must share one collector.
func TestHandleSystemStats_ConcurrentRequestsShareOneProbe(t *testing.T) {
	dir := t.TempDir()
	probeLog := filepath.Join(dir, "gpu.log")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GPU_PROBE_LOG", probeLog)
	fake := "#!/bin/sh\nprintf 'gpu\\n' >> \"$GPU_PROBE_LOG\"\nsleep 0.2\nprintf '37, Fake GPU\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "nvidia-smi"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}

	server := NewServer(Config{MenuData: &fakeMenuDataLoader{}})
	var requests sync.WaitGroup
	for range 12 {
		requests.Go(func() {
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, newLocalRequest(http.MethodGet, "/api/system/stats", nil))
			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
			}
		})
	}
	requests.Wait()
	// Shutdown waits for the collection in progress.
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	log, err := os.ReadFile(probeLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(log), "gpu\n"); got != 1 {
		t.Errorf("GPU probes for 12 concurrent requests = %d, want 1", got)
	}
}
