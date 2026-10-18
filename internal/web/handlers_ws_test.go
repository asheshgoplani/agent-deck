package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func wsURL(baseURL, path string) string {
	if strings.HasPrefix(baseURL, "https://") {
		return "wss://" + strings.TrimPrefix(baseURL, "https://") + path
	}
	return "ws://" + strings.TrimPrefix(baseURL, "http://") + path
}

func TestWSEndpointUnauthorized(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
		Profile:    "work",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID: "sess-1",
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	_, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-1"), nil)
	if err == nil {
		t.Fatal("expected websocket dial error for unauthorized request")
	}
	if resp == nil {
		t.Fatal("expected HTTP response for unauthorized websocket upgrade")
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, resp.StatusCode)
	}
}

func TestWSEndpointAuthorizedWithQueryToken(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
		Profile:    "work",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID: "sess-1",
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-1?token=secret-token"), nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial failed with status %d: %v", resp.StatusCode, err)
		}
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	expectWSStatusEvent(t, conn, "connected")
}

func TestWSEndpointAuthorizedWithBearerToken(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Token:      "secret-token",
		Profile:    "work",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID: "sess-2",
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer secret-token")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-2"), headers)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial failed with status %d: %v", resp.StatusCode, err)
		}
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	expectWSStatusEvent(t, conn, "connected")
}

func TestWSEndpointRejectsCrossOrigin(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "work",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID: "sess-origin",
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	headers := http.Header{}
	headers.Set("Origin", "https://evil.example")

	_, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-origin"), headers)
	if err == nil {
		t.Fatal("expected websocket dial error for cross-origin request")
	}
	if resp == nil {
		t.Fatal("expected HTTP response for rejected cross-origin websocket upgrade")
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, resp.StatusCode)
	}
}

func TestWSEndpointSessionNotFound(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "work",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID: "sess-existing",
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	_, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-missing"), nil)
	if err == nil {
		t.Fatal("expected websocket dial error for missing session")
	}
	if resp == nil {
		t.Fatal("expected HTTP response for missing session websocket upgrade")
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, resp.StatusCode)
	}
}

func TestWSEndpointConnectAndPing(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "work",
		ReadOnly:   true,
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID: "sess-123",
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-123"), nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial failed with status %d: %v", resp.StatusCode, err)
		}
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	var msg1 wsServerMessage
	if err := conn.ReadJSON(&msg1); err != nil {
		t.Fatalf("failed to read first ws message: %v", err)
	}
	if msg1.Type != "status" || msg1.Event != "connected" || msg1.SessionID != "sess-123" {
		t.Fatalf("unexpected first ws message: %+v", msg1)
	}
	if !msg1.ReadOnly {
		t.Fatalf("expected readOnly=true in connected event, got: %+v", msg1)
	}

	var msg2 wsServerMessage
	if err := conn.ReadJSON(&msg2); err != nil {
		t.Fatalf("failed to read second ws message: %v", err)
	}
	if msg2.Type != "status" || msg2.Event != "ready" {
		t.Fatalf("unexpected second ws message: %+v", msg2)
	}

	if err := conn.WriteJSON(wsClientMessage{Type: "ping"}); err != nil {
		t.Fatalf("failed to write ping message: %v", err)
	}

	var msg3 wsServerMessage
	if err := conn.ReadJSON(&msg3); err != nil {
		t.Fatalf("failed to read pong ws message: %v", err)
	}
	if msg3.Type != "status" || msg3.Event != "pong" || msg3.SessionID != "sess-123" {
		t.Fatalf("unexpected pong message: %+v", msg3)
	}
}

func TestWSEndpointInputWithoutTerminalBridge(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "work",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID: "sess-bridge-missing",
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-bridge-missing"), nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial failed with status %d: %v", resp.StatusCode, err)
		}
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	expectWSStatusEvent(t, conn, "connected")
	expectWSStatusEvent(t, conn, "ready")

	if err := conn.WriteJSON(wsClientMessage{Type: "input", Data: "echo hi\r"}); err != nil {
		t.Fatalf("failed to write input message: %v", err)
	}

	var msg wsServerMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("failed to read ws response: %v", err)
	}
	if msg.Type != "error" || msg.Code != "NO_TERMINAL_BRIDGE" {
		t.Fatalf("unexpected ws response: %+v", msg)
	}
}

func TestWSEndpointReadOnlyBlocksInput(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "work",
		ReadOnly:   true,
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID: "sess-read-only",
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-read-only"), nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial failed with status %d: %v", resp.StatusCode, err)
		}
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	expectWSStatusEvent(t, conn, "connected")
	expectWSStatusEvent(t, conn, "ready")

	if err := conn.WriteJSON(wsClientMessage{Type: "input", Data: "echo blocked\r"}); err != nil {
		t.Fatalf("failed to write input message: %v", err)
	}

	var msg wsServerMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("failed to read ws response: %v", err)
	}
	if msg.Type != "error" || msg.Code != "READ_ONLY" {
		t.Fatalf("unexpected ws response: %+v", msg)
	}
}

func TestWSEndpointResizeWithoutTerminalBridge(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "work",
	})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{
					Type: MenuItemTypeSession,
					Session: &MenuSession{
						ID: "sess-resize-missing",
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-resize-missing"), nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial failed with status %d: %v", resp.StatusCode, err)
		}
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	expectWSStatusEvent(t, conn, "connected")
	expectWSStatusEvent(t, conn, "ready")

	if err := conn.WriteJSON(wsClientMessage{Type: "resize", Cols: 120, Rows: 36}); err != nil {
		t.Fatalf("failed to write resize message: %v", err)
	}

	var msg wsServerMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("failed to read ws response: %v", err)
	}
	if msg.Type != "error" || msg.Code != "NO_TERMINAL_BRIDGE" {
		t.Fatalf("unexpected ws response: %+v", msg)
	}
}

func expectWSStatusEvent(t *testing.T, conn *websocket.Conn, event string) {
	t.Helper()

	var msg wsServerMessage
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("failed to read ws status message: %v", err)
	}
	if msg.Type != "status" || msg.Event != event {
		t.Fatalf("expected status=%q message, got: %+v", event, msg)
	}
}

// TestSessionWSUpgradeDoesNotWaitOnMenuRefresh guards #2663: a terminal open
// must not queue behind an inline menu-data refresh that holds the
// MemoryMenuData lock. The lock is held here to stand in for that refresh.
func TestSessionWSUpgradeDoesNotWaitOnMenuRefresh(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "work",
	})
	menu := NewMemoryMenuData(&fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{Type: MenuItemTypeSession, Session: &MenuSession{ID: "sess-1"}},
			},
		},
	})
	if _, err := menu.LoadMenuSnapshot(); err != nil {
		t.Fatalf("prime snapshot: %v", err)
	}
	srv.menuData = menu

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	menu.mu.Lock()
	unlocked := false
	unlock := func() {
		if !unlocked {
			unlocked = true
			menu.mu.Unlock()
		}
	}
	defer unlock()

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 2 * time.Second
	start := time.Now()
	conn, _, err := dialer.Dial(wsURL(testServer.URL, "/ws/session/sess-1"), nil)
	elapsed := time.Since(start)
	unlock()
	if err != nil {
		t.Fatalf("websocket upgrade blocked behind menu refresh (%s): %v", elapsed, err)
	}
	_ = conn.Close()
	if elapsed > 500*time.Millisecond {
		t.Fatalf("websocket upgrade took %s while menu refresh held the lock", elapsed)
	}
}

func TestMemoryMenuDataCachedSnapshotDoesNotTakeLock(t *testing.T) {
	menu := NewMemoryMenuData(nil)
	if got := menu.CachedMenuSnapshot(); got != nil {
		t.Fatalf("expected nil cached snapshot before publish, got %+v", got)
	}
	menu.SetSnapshot(&MenuSnapshot{Items: []MenuItem{
		{Type: MenuItemTypeSession, Session: &MenuSession{ID: "sess-1"}},
	}})

	menu.mu.Lock()
	defer menu.mu.Unlock()
	done := make(chan *MenuSnapshot, 1)
	go func() { done <- menu.CachedMenuSnapshot() }()
	select {
	case got := <-done:
		if _, ok := snapshotSessionByID(got, "sess-1"); !ok {
			t.Fatalf("cached snapshot missing sess-1: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("CachedMenuSnapshot blocked on the menu-data lock")
	}
}

// TestMemoryMenuDataInvalidateCacheClearsCachedSnapshot guards the #2663
// follow-up: after a mutation the published snapshot may name a tmux session
// that no longer exists, so InvalidateCache must drop it.
func TestMemoryMenuDataInvalidateCacheClearsCachedSnapshot(t *testing.T) {
	menu := NewMemoryMenuData(nil)
	menu.SetSnapshot(&MenuSnapshot{Items: []MenuItem{
		{Type: MenuItemTypeSession, Session: &MenuSession{ID: "sess-1", TmuxSession: "old"}},
	}})
	menu.InvalidateCache()
	if got := menu.CachedMenuSnapshot(); got != nil {
		t.Fatalf("expected InvalidateCache to clear the cached snapshot, got %+v", got)
	}
}

// recordTmuxAttach swaps the websocket bridge constructor for one that records
// each tmux target and fails the attach with errFor(name).
func recordTmuxAttach(t *testing.T, errFor func(name string) error) func() []string {
	t.Helper()
	var (
		mu    sync.Mutex
		names []string
	)
	orig := attachTmuxPTYBridge
	attachTmuxPTYBridge = func(tmuxSession, _ string, _ string, _ *wsConnWriter) (*tmuxPTYBridge, error) {
		mu.Lock()
		names = append(names, tmuxSession)
		mu.Unlock()
		return nil, errFor(tmuxSession)
	}
	t.Cleanup(func() { attachTmuxPTYBridge = orig })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), names...)
	}
}

func dialSessionWSUntilError(t *testing.T, srv *Server, sessionID string) {
	t.Helper()
	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/"+sessionID), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var msg wsServerMessage
		if err := conn.ReadJSON(&msg); err != nil {
			t.Fatalf("read: %v", err)
		}
		if msg.Type == "error" {
			return
		}
	}
}

// TestSessionWSAttachesToRecreatedTmuxAfterInvalidate guards the #2663
// follow-up: restart recreates the tmux session under a new name and then
// invalidates the menu cache. The next terminal open must attach to the new
// name, not the one in the previously published snapshot.
func TestSessionWSAttachesToRecreatedTmuxAfterInvalidate(t *testing.T) {
	loader := &fakeMenuDataLoader{snapshot: &MenuSnapshot{
		Profile: "work",
		Items: []MenuItem{
			{Type: MenuItemTypeSession, Session: &MenuSession{ID: "sess-1", TmuxSession: "agentdeck_old"}},
		},
	}}
	menu := NewMemoryMenuData(loader)
	if _, err := menu.LoadMenuSnapshot(); err != nil {
		t.Fatalf("prime snapshot: %v", err)
	}
	loader.snapshot = &MenuSnapshot{
		Profile: "work",
		Items: []MenuItem{
			{Type: MenuItemTypeSession, Session: &MenuSession{ID: "sess-1", TmuxSession: "agentdeck_new"}},
		},
	}
	menu.InvalidateCache()

	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "work"})
	srv.menuData = menu
	names := recordTmuxAttach(t, func(string) error { return errors.New("stub attach") })

	dialSessionWSUntilError(t, srv, "sess-1")
	if got := names(); len(got) == 0 || got[0] != "agentdeck_new" {
		t.Fatalf("terminal attached to %v, want first attach to agentdeck_new", got)
	}
}

type splitMenuData struct {
	cached *MenuSnapshot
	loaded *MenuSnapshot
}

func (m *splitMenuData) CachedMenuSnapshot() *MenuSnapshot { return m.cached }
func (m *splitMenuData) LoadMenuSnapshot() (*MenuSnapshot, error) {
	return m.loaded, nil
}

// TestSessionWSRetriesFullLoadWhenCachedTmuxIsGone covers recreates the web
// server never saw (CLI/TUI restart): the cached tmux name is dead, so the
// handler must re-resolve through LoadMenuSnapshot and retry once.
func TestSessionWSRetriesFullLoadWhenCachedTmuxIsGone(t *testing.T) {
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "work"})
	srv.menuData = &splitMenuData{
		cached: &MenuSnapshot{Profile: "work", Items: []MenuItem{
			{Type: MenuItemTypeSession, Session: &MenuSession{ID: "sess-1", TmuxSession: "agentdeck_old"}},
		}},
		loaded: &MenuSnapshot{Profile: "work", Items: []MenuItem{
			{Type: MenuItemTypeSession, Session: &MenuSession{ID: "sess-1", TmuxSession: "agentdeck_new"}},
		}},
	}
	names := recordTmuxAttach(t, func(name string) error {
		if name == "agentdeck_old" {
			return fmt.Errorf("%w: %s", ErrTmuxSessionNotFound, name)
		}
		return errors.New("stub attach")
	})

	dialSessionWSUntilError(t, srv, "sess-1")
	if got := strings.Join(names(), ","); got != "agentdeck_old,agentdeck_new" {
		t.Fatalf("attach targets = %q, want agentdeck_old,agentdeck_new", got)
	}
}
