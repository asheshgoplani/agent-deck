package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type rotatingMenuDataLoader struct {
	mu        sync.Mutex
	snapshots []*MenuSnapshot
	index     int
}

func (r *rotatingMenuDataLoader) LoadMenuSnapshot() (*MenuSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.snapshots) == 0 {
		return &MenuSnapshot{}, nil
	}

	idx := r.index
	if idx >= len(r.snapshots) {
		idx = len(r.snapshots) - 1
	}
	snapshot := r.snapshots[idx]
	if r.index < len(r.snapshots)-1 {
		r.index++
	}
	return snapshot, nil
}

func TestMenuEventsUnauthorizedWhenTokenEnabled(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{Profile: "default"},
	}

	req := httptest.NewRequest(http.MethodGet, "/events/menu", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"code":"UNAUTHORIZED"`) {
		t.Fatalf("expected UNAUTHORIZED body, got: %s", rr.Body.String())
	}
}

func TestMenuEventsStreamInitialSnapshot(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile:       "work",
			TotalSessions: 1,
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID:    "sess-1",
						Title: "one",
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	req, err := http.NewRequest(http.MethodGet, testServer.URL+"/events/menu", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream content-type, got: %s", ct)
	}

	reader := bufio.NewReader(resp.Body)
	event, payload, err := readSSEEvent(reader)
	if err != nil {
		t.Fatalf("failed to read sse event: %v", err)
	}
	if event != "menu" {
		t.Fatalf("expected event 'menu', got %q", event)
	}

	var snapshot MenuSnapshot
	if err := json.Unmarshal([]byte(payload), &snapshot); err != nil {
		t.Fatalf("invalid snapshot payload: %v", err)
	}
	if snapshot.Profile != "work" {
		t.Fatalf("expected profile work, got %q", snapshot.Profile)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Session == nil || snapshot.Items[0].Session.ID != "sess-1" {
		t.Fatalf("unexpected snapshot payload: %+v", snapshot)
	}
}

func TestMenuEventsStreamPushesChanges(t *testing.T) {
	origInterval := menuEventsPollInterval
	menuEventsPollInterval = 30 * time.Millisecond
	defer func() { menuEventsPollInterval = origInterval }()

	origHeartbeat := menuEventsHeartbeatInterval
	menuEventsHeartbeatInterval = 2 * time.Second
	defer func() { menuEventsHeartbeatInterval = origHeartbeat }()

	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
	})
	srv.menuData = &rotatingMenuDataLoader{
		snapshots: []*MenuSnapshot{
			{
				Profile:       "work",
				TotalSessions: 1,
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:    "sess-1",
							Title: "one",
						},
					},
				},
			},
			{
				Profile:       "work",
				TotalSessions: 1,
				Items: []MenuItem{
					{
						Type: MenuItemTypeSession,
						Session: &MenuSession{
							ID:    "sess-2",
							Title: "two",
						},
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testServer.URL+"/events/menu", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)

	_, payload1, err := readSSEEvent(reader)
	if err != nil {
		t.Fatalf("failed to read first event: %v", err)
	}
	_, payload2, err := readSSEEvent(reader)
	if err != nil {
		t.Fatalf("failed to read second event: %v", err)
	}

	if !strings.Contains(payload1, `"id":"sess-1"`) {
		t.Fatalf("first payload missing sess-1: %s", payload1)
	}
	if !strings.Contains(payload2, `"id":"sess-2"`) {
		t.Fatalf("second payload missing sess-2: %s", payload2)
	}
}

func readSSEEvent(r *bufio.Reader) (string, string, error) {
	var (
		event string
		data  string
	)

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if event != "" || data != "" {
				return event, data, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
}
