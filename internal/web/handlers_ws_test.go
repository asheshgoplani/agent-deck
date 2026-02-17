package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
