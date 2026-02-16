//go:build !windows

package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSEndpointTmuxBridgeIntegration(t *testing.T) {
	requireTmuxForWebIntegration(t)

	sessionName := fmt.Sprintf("agentdeck_web_it_%d", time.Now().UnixNano())
	if output, err := exec.Command("tmux", "new-session", "-d", "-s", sessionName).CombinedOutput(); err != nil {
		t.Skipf("tmux new-session unavailable: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	defer func() {
		_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	}()

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
						ID:          "sess-it",
						TmuxSession: sessionName,
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-it"), nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial failed with status %d: %v", resp.StatusCode, err)
		}
		t.Fatalf("dial failed: %v", err)
	}
	defer func() {
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(200*time.Millisecond),
		)
		_ = conn.Close()
	}()

	waitForStatusOrSkipOnAttachFailure(t, conn, "terminal_attached")

	marker := fmt.Sprintf("ADWEB_IT_%d", time.Now().UnixNano())
	command := fmt.Sprintf("printf '%s\\n'\r", marker)
	if err := conn.WriteJSON(wsClientMessage{
		Type: "input",
		Data: command,
	}); err != nil {
		t.Fatalf("failed to send input message: %v", err)
	}

	received, err := readBinaryUntilContains(conn, marker, 8*time.Second)
	if err != nil {
		t.Fatalf("did not observe marker in ws stream: %v\nstream_excerpt=%q", err, trimForError(received, 350))
	}
}

func requireTmuxForWebIntegration(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not found in PATH")
	}
	if output, err := exec.Command("tmux", "-V").CombinedOutput(); err != nil {
		t.Skipf("tmux not available: %v (%s)", err, strings.TrimSpace(string(output)))
	}
}

func waitForStatusOrSkipOnAttachFailure(t *testing.T, conn *websocket.Conn, targetEvent string) {
	t.Helper()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(900 * time.Millisecond))
		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			if isTimeout(err) {
				continue
			}
			t.Fatalf("failed to read websocket message: %v", err)
		}

		if msgType == websocket.BinaryMessage {
			continue
		}

		var msg wsServerMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			continue
		}

		if msg.Type == "error" && (msg.Code == "TERMINAL_ATTACH_FAILED" || msg.Code == "TMUX_SESSION_NOT_FOUND") {
			t.Skipf("tmux attach not supported in this environment: code=%s msg=%s", msg.Code, msg.Message)
		}
		if msg.Type == "status" && msg.Event == targetEvent {
			return
		}
	}

	t.Fatalf("timed out waiting for status event %q", targetEvent)
}

func readBinaryUntilContains(conn *websocket.Conn, marker string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	needle := []byte(marker)
	combined := make([]byte, 0, 8192)

	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(900 * time.Millisecond))
		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			if isTimeout(err) {
				continue
			}
			return string(combined), err
		}

		switch msgType {
		case websocket.BinaryMessage:
			combined = append(combined, payload...)
			if len(combined) > 1_000_000 {
				combined = combined[len(combined)-1_000_000:]
			}
			if bytes.Contains(combined, needle) {
				return string(combined), nil
			}
		case websocket.TextMessage:
			var msg wsServerMessage
			if err := json.Unmarshal(payload, &msg); err == nil && msg.Type == "error" {
				return string(combined), fmt.Errorf("received ws error code=%s message=%s", msg.Code, msg.Message)
			}
		}
	}

	return string(combined), fmt.Errorf("timeout waiting for marker %q", marker)
}

func isTimeout(err error) bool {
	var netErr net.Error
	return err != nil && (strings.Contains(err.Error(), "i/o timeout") || (errors.As(err, &netErr) && netErr.Timeout()))
}

func trimForError(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}
