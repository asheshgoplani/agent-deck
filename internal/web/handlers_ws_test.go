package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
// MemoryMenuData lock. A fallback whose revision probe blocks stands in for
// that refresh: LoadMenuSnapshot holds the lock while it waits.
func TestSessionWSUpgradeDoesNotWaitOnMenuRefresh(t *testing.T) {
	srv := NewServer(Config{
		ListenAddr: "127.0.0.1:0",
		Profile:    "work",
	})
	loader := &blockingRevisionMenuLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{
				{Type: MenuItemTypeSession, Session: &MenuSession{ID: "sess-1"}},
			},
		},
		release: make(chan struct{}),
		probing: make(chan struct{}, 1),
	}
	menu := NewMemoryMenuData(loader)
	if _, err := menu.LoadMenuSnapshot(); err != nil {
		t.Fatalf("prime snapshot: %v", err)
	}
	srv.menuData = menu

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	// Hold the MemoryMenuData lock the real way: a LoadMenuSnapshot whose
	// revision probe blocks. The probe is only due once the store's revision
	// check interval has passed since the prime, so keep calling until one
	// call reaches it (and then blocks, holding the lock). Bounded by a
	// deadline so a store change cannot hang this goroutine.
	loader.block.Store(true)
	released := false
	release := func() {
		if !released {
			released = true
			close(loader.release)
		}
	}
	defer release()
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			_, _ = menu.LoadMenuSnapshot()
			select {
			case <-loader.release:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	select {
	case <-loader.probing:
	case <-time.After(5 * time.Second):
		t.Fatal("menu refresh never reached the revision probe")
	}

	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = 2 * time.Second
	start := time.Now()
	conn, _, err := dialer.Dial(wsURL(testServer.URL, "/ws/session/sess-1"), nil)
	elapsed := time.Since(start)
	release()
	if err != nil {
		t.Fatalf("websocket upgrade blocked behind menu refresh (%s): %v", elapsed, err)
	}
	_ = conn.Close()
	if elapsed > 500*time.Millisecond {
		t.Fatalf("websocket upgrade took %s while menu refresh held the lock", elapsed)
	}
}

type blockingRevisionMenuLoader struct {
	snapshot *MenuSnapshot
	block    atomic.Bool
	release  chan struct{}
	probing  chan struct{}
}

func (l *blockingRevisionMenuLoader) LoadMenuSnapshot() (*MenuSnapshot, error) {
	return l.snapshot, nil
}

// MenuDataRevision blocks once block is set, until release is closed (or a
// deadline passes, so a broken test cannot hang the package), standing in for
// a slow storage probe made while MemoryMenuData holds its lock.
func (l *blockingRevisionMenuLoader) MenuDataRevision() (int64, error) {
	if l.block.Load() {
		select {
		case l.probing <- struct{}{}:
		default:
		}
		select {
		case <-l.release:
		case <-time.After(10 * time.Second):
		}
	}
	return 1, nil
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
