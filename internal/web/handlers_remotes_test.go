package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
