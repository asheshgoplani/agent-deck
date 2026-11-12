//go:build !windows

package web

import (
	"fmt"
	"io"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

func TestPhoneTerminalDoesNotResizeDesktop(t *testing.T) {
	requireTmuxForWebIntegration(t)
	name := fmt.Sprintf("phone_size_%d", time.Now().UnixNano())
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", name, "-x", "180", "-y", "50").CombinedOutput(); err != nil {
		t.Fatalf("create tmux: %v %s", err, out)
	}
	defer func() { _ = exec.Command("tmux", "kill-session", "-t", name).Run() }()
	if out, err := exec.Command("tmux", "set-window-option", "-t", name, "window-size", "latest").CombinedOutput(); err != nil {
		t.Fatalf("set size policy: %v %s", err, out)
	}
	desktop := tmuxAttachCommand(name, "")
	terminal, err := pty.StartWithSize(desktop, &pty.Winsize{Cols: 180, Rows: 50})
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, terminal); close(drained) }()
	defer func() { _ = desktop.Process.Kill(); _ = desktop.Wait(); _ = terminal.Close(); <-drained }()
	size := func() string {
		out, err := exec.Command("tmux", "display-message", "-p", "-t", name, "#{window_width}x#{window_height}").CombinedOutput()
		if err != nil {
			t.Fatalf("get size: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.HasPrefix(size(), "180x") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	before := size()
	if !strings.HasPrefix(before, "180x") {
		t.Fatalf("desktop did not attach: %s", before)
	}
	srv := NewServer(Config{ListenAddr: "127.0.0.1:0"})
	srv.menuData = &fakeMenuDataLoader{snapshot: &MenuSnapshot{Items: []MenuItem{{Type: MenuItemTypeSession, Session: &MenuSession{ID: "phone", TmuxSession: name}}}}}
	server := httptest.NewServer(srv.Handler())
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL(server.URL, "/ws/session/phone?ignore-size=1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitForStatusOrSkipOnAttachFailure(t, conn, "terminal_attached")
	if err := conn.WriteJSON(wsClientMessage{Type: "resize", Cols: 39, Rows: 15}); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(wsClientMessage{Type: "input", Data: "printf 'PHONE_%s\\n' SIZE_OK\r"}); err != nil {
		t.Fatal(err)
	}
	// An ignore-size client sees only part of a larger window, so look for the
	// command's output in the pane rather than in the phone's stream.
	ran := false
	for deadline := time.Now().Add(5 * time.Second); !ran && time.Now().Before(deadline); {
		out, _ := exec.Command("tmux", "capture-pane", "-p", "-t", name).CombinedOutput()
		ran = strings.Contains(string(out), "PHONE_SIZE_OK")
		time.Sleep(20 * time.Millisecond)
	}
	if !ran {
		t.Fatal("phone input never ran in the pane")
	}
	for i := 0; i < 15; i++ {
		if after := size(); after != before {
			t.Fatalf("phone resized desktop from %s to %s", before, after)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("desktop geometry retained at %s after phone attach, resize and input", before)
}
