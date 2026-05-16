//go:build hostsensitive && !windows

// Host-sensitive web tests. Built and run only when the `hostsensitive`
// build tag is supplied (e.g. nightly job: `go test -tags hostsensitive`).
// See issue #969 for the categorization rationale.

package web

import (
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTmuxPTYBridgeResize(t *testing.T) {
	requireTmuxForWebIntegration(t)

	// Skip on CI: this asserts a WebSocket resize message propagates through
	// the bridge's attach-client PTY all the way to the tmux session geometry.
	// On CI's headless GitHub Actions runner the attach-client PTY never
	// reaches the requested 120x40 size — pty.Setsize is called locally but
	// tmux's view of the client size stays at 80x24. Verified-working on real
	// PTYs (macOS/Linux desktops, conductor host May 5 2026); the production
	// fix is covered by Session.Start tests in internal/tmux. This test stays
	// valuable as a local-dev smoke test.
	if os.Getenv("CI") == "true" || os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Skip("flaky on CI headless runners: attach-client PTY winsize doesn't propagate to tmux")
	}

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
