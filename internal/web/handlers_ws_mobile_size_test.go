//go:build !windows

package web

import (
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// sharedWindow is a detached tmux session with the shared-view policy
// (window-size latest) that several viewers attach to.
type sharedWindow struct {
	t    *testing.T
	name string
}

func newSharedWindow(t *testing.T, prefix string) *sharedWindow {
	t.Helper()
	requireTmuxForWebIntegration(t)
	name := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	// Created at a size no viewer uses, so waitWidth sees a viewer's resize.
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", name, "-x", "200", "-y", "60").CombinedOutput(); err != nil {
		t.Fatalf("create tmux: %v %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() })
	if out, err := exec.Command("tmux", "set-window-option", "-t", name, "window-size", "latest").CombinedOutput(); err != nil {
		t.Fatalf("set size policy: %v %s", err, out)
	}
	return &sharedWindow{t: t, name: name}
}

func (w *sharedWindow) size() string {
	w.t.Helper()
	out, err := exec.Command("tmux", "display-message", "-p", "-t", w.name, "#{window_width}x#{window_height}").CombinedOutput()
	if err != nil {
		w.t.Fatalf("get size: %v %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// waitWidth waits until the window is cols wide and returns its size.
func (w *sharedWindow) waitWidth(cols int, why string) string {
	w.t.Helper()
	prefix := fmt.Sprintf("%dx", cols)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if s := w.size(); strings.HasPrefix(s, prefix) {
			return s
		}
	}
	w.t.Fatalf("%s: window is %s, want %d columns", why, w.size(), cols)
	return ""
}

// holdSize checks that the window stays at want for a while.
func (w *sharedWindow) holdSize(want, why string) {
	w.t.Helper()
	for i := 0; i < 15; i++ {
		if got := w.size(); got != want {
			w.t.Fatalf("%s: window changed from %s to %s", why, want, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitPane waits until marker shows up in the pane. An ignore-size client
// sees only part of a larger window, so input is checked in the pane rather
// than in the client's own stream.
func (w *sharedWindow) waitPane(marker, why string) {
	w.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		out, _ := exec.Command("tmux", "capture-pane", "-p", "-t", w.name).CombinedOutput()
		if strings.Contains(string(out), marker) {
			return
		}
	}
	w.t.Fatalf("%s: %s never showed up in the pane", why, marker)
}

// attachDesktop attaches a native tmux client in a cols x rows terminal.
func (w *sharedWindow) attachDesktop(cols, rows uint16) *os.File {
	w.t.Helper()
	cmd := tmuxAttachCommand(w.name, "")
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		w.t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, terminal); close(drained) }()
	w.t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); _ = terminal.Close(); <-drained })
	return terminal
}

func (w *sharedWindow) webServer() *httptest.Server {
	w.t.Helper()
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{Items: []MenuItem{{Type: MenuItemTypeSession, Session: &MenuSession{ID: "shared", TmuxSession: w.name}}}}}
	server := httptest.NewServer(srv.Handler())
	w.t.Cleanup(server.Close)
	return server
}

// attachWeb opens the session's web terminal, as a phone when phone is true.
func (w *sharedWindow) attachWeb(server *httptest.Server, phone bool) *websocket.Conn {
	w.t.Helper()
	path := "/ws/session/shared"
	if phone {
		path += "?ignore-size=1"
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, path), nil)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { _ = conn.Close() })
	waitForStatusOrSkipOnAttachFailure(w.t, conn, "terminal_attached")
	return conn
}

func sendWeb(t *testing.T, conn *websocket.Conn, msg wsClientMessage) {
	t.Helper()
	if err := conn.WriteJSON(msg); err != nil {
		t.Fatal(err)
	}
}

func TestPhoneTerminalDoesNotResizeDesktop(t *testing.T) {
	w := newSharedWindow(t, "phone_size")
	w.attachDesktop(180, 50)
	before := w.waitWidth(180, "desktop attach")

	phone := w.attachWeb(w.webServer(), true)
	sendWeb(t, phone, wsClientMessage{Type: "resize", Cols: 39, Rows: 15})
	sendWeb(t, phone, wsClientMessage{Type: "input", Data: "printf 'PHONE_%s\\n' SIZE_OK\r"})
	w.waitPane("PHONE_SIZE_OK", "phone input")
	if after := w.size(); after != before {
		t.Fatalf("phone resized desktop from %s to %s", before, after)
	}
	w.holdSize(before, "phone resized desktop")
	t.Logf("desktop geometry retained at %s after phone attach, resize and input", before)
}

// Desktop viewers keep the shared-view policy (#866, #2186, window-size
// latest): with a phone watching, the window still follows whichever desktop
// client attached, resized or typed last, desktop web terminals included.
func TestDesktopViewersStillFollowLatestWithPhoneWatching(t *testing.T) {
	w := newSharedWindow(t, "phone_latest")
	native := w.attachDesktop(180, 50)
	wide := w.waitWidth(180, "native desktop attach")
	server := w.webServer()

	phone := w.attachWeb(server, true)
	sendWeb(t, phone, wsClientMessage{Type: "resize", Cols: 39, Rows: 15})
	w.holdSize(wide, "phone attach and resize")

	desktopWeb := w.attachWeb(server, false)
	sendWeb(t, desktopWeb, wsClientMessage{Type: "resize", Cols: 120, Rows: 40})
	sendWeb(t, desktopWeb, wsClientMessage{Type: "input", Data: "printf 'WEB_%s\\n' DESKTOP\r"})
	w.waitPane("WEB_DESKTOP", "desktop web input")
	narrow := w.waitWidth(120, "desktop web terminal became the latest viewer")

	if _, err := native.WriteString("printf 'NATIVE_%s\\n' DESKTOP\r"); err != nil {
		t.Fatal(err)
	}
	w.waitPane("NATIVE_DESKTOP", "native desktop input")
	wide = w.waitWidth(180, "native desktop typed and became the latest viewer again")

	sendWeb(t, phone, wsClientMessage{Type: "input", Data: "printf 'PHONE_%s\\n' TYPED\r"})
	w.waitPane("PHONE_TYPED", "phone input")
	w.holdSize(wide, "phone typed")
	t.Logf("window followed the desktops (180 -> %s -> %s) and stayed at %s while the phone attached, resized and typed", narrow, wide, wide)
}
