package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
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

func TestExternalStorageChangeRefreshesMenuAPIAndSSE(t *testing.T) {
	profile := fmt.Sprintf("external-refresh-%d", time.Now().UnixNano())
	writeExternalMenuSession(t, profile, "sess-initial", "Initial")

	menuData := NewMemoryMenuData(NewSessionDataService(profile))
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    profile,
		MenuData:   menuData,
	})
	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	initialAPI := loadMenuSnapshotFromAPI(t, testServer.URL)
	if !menuSnapshotHasSession(initialAPI, "sess-initial") {
		t.Fatalf("initial API snapshot does not contain sess-initial: %+v", initialAPI.Items)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, testServer.URL+"/events/menu", nil)
	if err != nil {
		t.Fatalf("new SSE request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("start SSE request: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	_, initialPayload, err := readSSEEvent(reader)
	if err != nil {
		t.Fatalf("read initial SSE event: %v", err)
	}
	var initialSSE MenuSnapshot
	if err := json.Unmarshal([]byte(initialPayload), &initialSSE); err != nil {
		t.Fatalf("decode initial SSE event: %v", err)
	}
	if !menuSnapshotHasSession(&initialSSE, "sess-initial") {
		t.Fatalf("initial SSE snapshot does not contain sess-initial: %+v", initialSSE.Items)
	}

	writeExternalMenuSession(t, profile, "sess-external", "External")

	type sseResult struct {
		snapshot MenuSnapshot
		err      error
	}
	sseResultCh := make(chan sseResult, 1)
	go func() {
		_, payload, readErr := readSSEEvent(reader)
		if readErr != nil {
			sseResultCh <- sseResult{err: readErr}
			return
		}
		var snapshot MenuSnapshot
		if decodeErr := json.Unmarshal([]byte(payload), &snapshot); decodeErr != nil {
			sseResultCh <- sseResult{err: decodeErr}
			return
		}
		sseResultCh <- sseResult{snapshot: snapshot}
	}()

	apiDeadline := time.Now().Add(4 * time.Second)
	var refreshedAPI *MenuSnapshot
	for time.Now().Before(apiDeadline) {
		candidate := loadMenuSnapshotFromAPI(t, testServer.URL)
		if menuSnapshotHasSession(candidate, "sess-external") {
			refreshedAPI = candidate
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if refreshedAPI == nil {
		t.Error("API menu kept the stale snapshot after an external storage write")
	}

	result := <-sseResultCh
	if result.err != nil {
		t.Errorf("SSE menu did not emit the external storage change: %v", result.err)
	} else if !menuSnapshotHasSession(&result.snapshot, "sess-external") {
		t.Errorf("SSE menu emitted a snapshot without sess-external: %+v", result.snapshot.Items)
	}
}

func writeExternalMenuSession(t *testing.T, profile, id, title string) {
	t.Helper()

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("open external storage: %v", err)
	}
	defer func() {
		if err := storage.Close(); err != nil {
			t.Errorf("close external storage: %v", err)
		}
	}()

	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load external storage: %v", err)
	}
	inst := session.NewInstanceWithGroupAndTool(title, t.TempDir(), "external", "shell")
	inst.ID = id
	instances = append(instances, inst)
	groupTree := session.NewGroupTreeWithGroups(instances, groups)
	groupTree.CreateGroupPath("external")
	if err := storage.SaveWithGroups(instances, groupTree); err != nil {
		t.Fatalf("write external storage: %v", err)
	}
}

func loadMenuSnapshotFromAPI(t *testing.T, serverURL string) *MenuSnapshot {
	t.Helper()

	resp, err := http.Get(serverURL + "/api/menu")
	if err != nil {
		t.Fatalf("load API menu: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("API menu status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var snapshot MenuSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode API menu: %v", err)
	}
	return &snapshot
}

func menuSnapshotHasSession(snapshot *MenuSnapshot, id string) bool {
	if snapshot == nil {
		return false
	}
	for _, item := range snapshot.Items {
		if item.Session != nil && item.Session.ID == id {
			return true
		}
	}
	return false
}

func TestSSEReconnectSnapshot(t *testing.T) {
	// This test verifies that every new SSE connection to /events/menu
	// receives a full "menu" event as the very first event.
	// This is the SYNC-04 reconnect contract: on reconnect, the client
	// receives complete state, not a diff. No separate REST fetch needed.
	testSnapshot := &MenuSnapshot{
		Profile:       "reconnect-test",
		TotalGroups:   1,
		TotalSessions: 2,
		Items: []MenuItem{
			{Index: 0, Type: MenuItemTypeGroup, Group: &MenuGroup{Name: "g1", Path: "g1"}},
			{Index: 1, Type: MenuItemTypeSession, Session: &MenuSession{ID: "s1", Title: "Session 1", Status: "running"}},
		},
	}

	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{snapshot: testSnapshot}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/events/menu")
	if err != nil {
		t.Fatalf("SSE request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream, got %s", ct)
	}

	reader := bufio.NewReader(resp.Body)
	eventType, eventData, err := readSSEEvent(reader)
	if err != nil {
		t.Fatalf("failed to read first SSE event: %v", err)
	}

	if eventType != "menu" {
		t.Fatalf("expected first SSE event type 'menu', got '%s'", eventType)
	}

	var snapshot MenuSnapshot
	if err := json.Unmarshal([]byte(eventData), &snapshot); err != nil {
		t.Fatalf("failed to unmarshal SSE snapshot data: %v", err)
	}
	if snapshot.Profile != "reconnect-test" {
		t.Fatalf("expected profile 'reconnect-test', got '%s'", snapshot.Profile)
	}
	if snapshot.TotalSessions != 2 {
		t.Fatalf("expected 2 sessions in snapshot, got %d", snapshot.TotalSessions)
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
