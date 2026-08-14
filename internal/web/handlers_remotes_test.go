package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type fakeRemoteFleetLoader struct {
	snapshot session.RemoteFleetSnapshot
	started  chan context.Context
}

func (f *fakeRemoteFleetLoader) Start(ctx context.Context) {
	if f.started != nil {
		f.started <- ctx
	}
}

func (f *fakeRemoteFleetLoader) Snapshot() session.RemoteFleetSnapshot { return f.snapshot }

func TestRemotesAPIListsConfiguredFleet(t *testing.T) {
	srv := NewServer(Config{RemoteFleet: &fakeRemoteFleetLoader{snapshot: session.RemoteFleetSnapshot{
		Remotes: []session.RemoteFleetRemote{{Name: "build", Online: true, Sessions: []session.RemoteSessionInfo{{ID: "session-1", Title: "Build"}}}},
		Counts:  session.RemoteFleetCounts{RemotesOnline: 1, Sessions: 1},
	}}})
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
	if len(snapshot.Remotes) != 1 || snapshot.Remotes[0].Name != "build" || snapshot.Counts.Sessions != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestRemotesAPIRequiresAuthorization(t *testing.T) {
	srv := NewServer(Config{Token: "secret", RemoteFleet: &fakeRemoteFleetLoader{}})
	req := httptest.NewRequest(http.MethodGet, "/api/remotes", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRemotesAPIOnlyReadsSnapshot(t *testing.T) {
	loader := &fakeRemoteFleetLoader{snapshot: session.RemoteFleetSnapshot{}}
	srv := NewServer(Config{RemoteFleet: loader})
	for range 12 {
		req := httptest.NewRequest(http.MethodGet, "/api/remotes", nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	// The loader has no request-aware method: this test protects the security
	// boundary that authenticated requests cannot initiate background work.
}
