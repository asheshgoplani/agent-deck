package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type fakeRemoteFleetLoader struct {
	snapshot session.RemoteFleetSnapshot
	err      error
}

func (f fakeRemoteFleetLoader) Scan(context.Context) (session.RemoteFleetSnapshot, error) {
	return f.snapshot, f.err
}

func TestRemotesAPIListsConfiguredFleet(t *testing.T) {
	srv := NewServer(Config{RemoteFleet: fakeRemoteFleetLoader{
		snapshot: session.RemoteFleetSnapshot{
			Remotes: []session.RemoteFleetRemote{{
				Name: "build", Online: true,
				Sessions: []session.RemoteSessionInfo{{ID: "session-1", Title: "Build"}},
			}},
			Counts: session.RemoteFleetCounts{RemotesOnline: 1, Sessions: 1},
		},
	}})
	req := httptest.NewRequest(http.MethodGet, "/api/remotes", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var snapshot session.RemoteFleetSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(snapshot.Remotes) != 1 || snapshot.Remotes[0].Name != "build" ||
		snapshot.Counts.Sessions != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestRemotesAPIFailsClosed(t *testing.T) {
	t.Run("authorization", func(t *testing.T) {
		srv := NewServer(Config{
			Token: "secret", RemoteFleet: fakeRemoteFleetLoader{},
		})
		req := httptest.NewRequest(http.MethodGet, "/api/remotes", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("scanner error", func(t *testing.T) {
		srv := NewServer(Config{RemoteFleet: fakeRemoteFleetLoader{
			err: errors.New("raw ssh detail must not leak"),
		}})
		req := httptest.NewRequest(http.MethodGet, "/api/remotes", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		if body := rec.Body.String(); body == "" || strings.Contains(body, "raw ssh detail") {
			t.Fatalf("unsafe error response = %q", body)
		}
	})
}

type blockingRemoteFleetLoader struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

func (f *blockingRemoteFleetLoader) Scan(context.Context) (session.RemoteFleetSnapshot, error) {
	f.mu.Lock()
	f.calls++
	if f.calls == 1 {
		close(f.started)
	}
	f.mu.Unlock()
	<-f.release
	return session.RemoteFleetSnapshot{Counts: session.RemoteFleetCounts{RemotesOnline: 1}}, nil
}

func TestRemoteFleetCacheCoalescesAndReusesScans(t *testing.T) {
	loader := &blockingRemoteFleetLoader{
		started: make(chan struct{}), release: make(chan struct{}),
	}
	cache := newRemoteFleetCache(loader)
	cache.ttl = time.Minute

	const callers = 12
	results := make(chan error, callers)
	go func() {
		_, err := cache.Scan(context.Background())
		results <- err
	}()
	<-loader.started
	for range callers - 1 {
		go func() {
			_, err := cache.Scan(context.Background())
			results <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for {
		cache.mu.Lock()
		waiters := cache.inFlight.waiters
		cache.mu.Unlock()
		if waiters == callers-1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("coalesced waiters = %d, want %d", waiters, callers-1)
		}
		time.Sleep(time.Millisecond)
	}
	close(loader.release)
	for range callers {
		if err := <-results; err != nil {
			t.Fatalf("Scan: %v", err)
		}
	}
	if _, err := cache.Scan(context.Background()); err != nil {
		t.Fatalf("cached Scan: %v", err)
	}
	loader.mu.Lock()
	calls := loader.calls
	loader.mu.Unlock()
	if calls != 1 {
		t.Fatalf("loader calls = %d, want 1", calls)
	}
}
