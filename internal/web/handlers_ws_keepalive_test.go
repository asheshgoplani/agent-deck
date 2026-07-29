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

// tmuxClientCount returns how many clients are attached to a tmux session on the
// default (env-selected) server. Errors — including "no server" — count as 0.
func tmuxClientCount(session string) int {
	out, err := exec.Command("tmux", "list-clients", "-t", session, "-F", "x").CombinedOutput()
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// waitForClientCount polls until the session has exactly want clients (tmux
// registers/removes attach clients slightly after the process starts/dies).
func waitForClientCount(session string, want int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if tmuxClientCount(session) == want {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return tmuxClientCount(session) == want
}

func newKeepaliveTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	sessionName := fmt.Sprintf("agentdeck_ws_keepalive_%d", time.Now().UnixNano())
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", sessionName).CombinedOutput(); err != nil {
		t.Skipf("tmux new-session unavailable: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", sessionName).Run() })

	srv := NewServer(Config{ListenAddr: "127.0.0.1:0", Profile: "work"})
	srv.menuData = &fakeMenuDataLoader{
		snapshot: &MenuSnapshot{
			Profile: "work",
			Items: []MenuItem{{
				Type:    MenuItemTypeSession,
				Session: &MenuSession{ID: "sess-ka", TmuxSession: sessionName},
			}},
		},
	}
	testServer := httptest.NewServer(srv.Handler())
	t.Cleanup(testServer.Close)
	return testServer, sessionName
}

// TestWSKeepaliveReapsDeadPeer proves the leak fix: a client that stops
// answering pings (a vanished peer — network drop / killed mobile app) is
// detected via the read deadline, so the handler returns and its deferred
// bridge.Close() kills the tmux attach instead of leaking it forever.
func TestWSKeepaliveReapsDeadPeer(t *testing.T) {
	requireTmuxForWebIntegration(t)

	origWait, origPeriod := wsPongWait, wsPingPeriod
	wsPongWait, wsPingPeriod = 700*time.Millisecond, 250*time.Millisecond
	defer func() { wsPongWait, wsPingPeriod = origWait, origPeriod }()

	testServer, sessionName := newKeepaliveTestServer(t)

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-ka"), nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial failed with status %d: %v", resp.StatusCode, err)
		}
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	// Attach while healthy (gorilla auto-pongs during these reads), and confirm
	// the bridge's tmux client is present before we simulate the peer dying.
	waitForStatusOrSkipOnAttachFailure(t, conn, "terminal_attached")
	if !waitForClientCount(sessionName, 1, time.Second) {
		t.Fatalf("expected an attached tmux client after terminal_attached, got %d", tmuxClientCount(sessionName))
	}

	// Now the peer "vanishes": stop answering pings. Keep reading so control
	// frames are processed; expect the server to close once the pong deadline
	// lapses.
	conn.SetPingHandler(func(string) error { return nil })
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatalf("server did not close the dead-peer connection within timeout")
	}

	if !waitForClientCount(sessionName, 0, 2*time.Second) {
		t.Fatalf("tmux attach client leaked: still attached after keepalive teardown")
	}
}

// TestWSKeepaliveHealthyClientSurvives guards against over-aggressive teardown:
// a client that answers pings (gorilla auto-pongs while reading) must stay
// attached well past pongWait.
func TestWSKeepaliveHealthyClientSurvives(t *testing.T) {
	requireTmuxForWebIntegration(t)

	origWait, origPeriod := wsPongWait, wsPingPeriod
	wsPongWait, wsPingPeriod = 300*time.Millisecond, 100*time.Millisecond
	defer func() { wsPongWait, wsPingPeriod = origWait, origPeriod }()

	testServer, sessionName := newKeepaliveTestServer(t)

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL(testServer.URL, "/ws/session/sess-ka"), nil)
	if err != nil {
		if resp != nil {
			t.Fatalf("dial failed with status %d: %v", resp.StatusCode, err)
		}
		t.Fatalf("dial failed: %v", err)
	}
	defer conn.Close()

	waitForStatusOrSkipOnAttachFailure(t, conn, "terminal_attached")

	// Drain frames in the background so gorilla auto-pongs the server's pings.
	// A single blocking ReadMessage services all inbound control frames; the
	// deferred conn.Close() unblocks it at test end. (gorilla forbids re-reading
	// after a read-deadline timeout, so we set one deadline past the test.)
	go func() {
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Several pongWait intervals later the connection should still be attached.
	time.Sleep(5 * wsPongWait)
	if tmuxClientCount(sessionName) == 0 {
		t.Fatalf("healthy ponging client was reaped: tmux attach dropped before it should")
	}
}
