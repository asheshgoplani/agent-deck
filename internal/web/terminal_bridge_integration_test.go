//go:build !windows

package web

import (
	"fmt"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTmuxPTYBridgeResize(t *testing.T) {
	requireTmuxForWebIntegration(t)

	sessionName := fmt.Sprintf("agentdeck_web_resize_%d", time.Now().UnixNano())
	if output, err := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-x", "80", "-y", "24").CombinedOutput(); err != nil {
		t.Skipf("tmux new-session unavailable: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	defer func() {
		_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	}()

	// Match what Session.Start does in production — without these options,
	// tmux defaults to window-size=latest which doesn't reliably re-arbitrate
	// to the bridge's attach client size on CI's headless tmux. Production
	// session creation always sets these (see internal/tmux/tmux.go); the
	// test's manual `tmux new-session` bypassed that path.
	_ = exec.Command("tmux", "set-option", "-t", sessionName, "window-size", "largest").Run()
	_ = exec.Command("tmux", "set-window-option", "-t", sessionName, "aggressive-resize", "on").Run()

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
						ID:          "sess-resize",
						TmuxSession: sessionName,
					},
				},
			},
		},
	}

	testServer := httptest.NewServer(srv.Handler())
	defer testServer.Close()

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-resize"), nil)
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

	if err := conn.WriteJSON(wsClientMessage{Type: "resize", Cols: 120, Rows: 40}); err != nil {
		t.Fatalf("failed to send resize message: %v", err)
	}

	// The web bridge no longer issues a `tmux resize-window -x N -y M` call
	// (that flipped session window-size to manual and dragged native clients —
	// the dots-in-window bug). Instead the bridge only Setsizes the local PTY
	// to the requested cols x rows, and tmux's window-size policy arbitrates
	// across all attached clients. With one attach client at PTY 120x40 and
	// tmux's default 1-row status bar, the resulting window content size is
	// 120x39 (rows are reduced by the status bar height).
	const wantSize = "120x39"
	var got string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("tmux", "display-message", "-t", sessionName, "-p", "#{window_width}x#{window_height}").Output()
		if err != nil {
			t.Fatalf("tmux display-message failed: %v", err)
		}
		got = strings.TrimSpace(string(out))
		if got == wantSize {
			return // PASS
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("tmux window size after Resize: got %q, want %q", got, wantSize)
}
