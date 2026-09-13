package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestServer_TracksInFlightRequests pins the self-restart gate: a request
// inside a handler makes Idle false until it returns, while an event
// stream (which the browser reconnects on its own) never counts.
func TestServer_TracksInFlightRequests(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	if !srv.Idle() || srv.InFlightRequests() != 0 {
		t.Fatal("a fresh server must be idle")
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	handler := srv.trackInFlight(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))

	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sessions", nil))
	}()
	<-entered
	if srv.Idle() || srv.InFlightRequests() != 1 {
		t.Fatalf("during a request: idle=%v inFlight=%d, want busy/1", srv.Idle(), srv.InFlightRequests())
	}
	close(release)
	<-done
	if !srv.Idle() {
		t.Fatal("after the request returned the server must be idle again")
	}

	// Event streams are excluded.
	streamEntered := make(chan struct{})
	streamRelease := make(chan struct{})
	stream := srv.trackInFlight(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(streamEntered)
		<-streamRelease
	}))
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		stream.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/events/menu", nil))
	}()
	select {
	case <-streamEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("stream handler never ran")
	}
	if !srv.Idle() {
		t.Fatal("an open event stream must not count as in-flight work")
	}
	close(streamRelease)
	<-streamDone

	for path, want := range map[string]bool{
		"/events/command-center": true,
		"/api/costs/stream":      true,
		"/ws/session/abc":        false,
		"/api/sessions":          false,
	} {
		if got := isStreamPath(path); got != want {
			t.Errorf("isStreamPath(%q) = %v, want %v", path, got, want)
		}
	}
}
