package ui

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// Between Enter and the tmux client's first byte the PTY does not exist. Keys
// typed in that window used to reach Bubble Tea and be discarded in embedded
// mode; a fast typist or a paste lost the first characters of every session.
// The router now enters session mode on Prepare and holds pane bytes until
// Activate installs the child.

func TestSessionInputRouterHoldsInputUntilTheChildExists(t *testing.T) {
	router := NewSessionInputRouter(nil)
	router.Prepare(terminalCellRect{Width: 80, Height: 24}, 0)

	if got := routeTestBytes(t, router, "ls -la"); len(got) != 0 {
		t.Fatalf("connecting interval leaked keys to the dashboard: %q", got)
	}

	var child bytes.Buffer
	router.Activate(&child, terminalCellRect{Width: 80, Height: 24}, 0)
	if got := child.String(); got != "ls -la" {
		t.Fatalf("child received %q after activation, want the held keystrokes", got)
	}

	if got := routeTestBytes(t, router, "\r"); len(got) != 0 {
		t.Fatalf("live keys leaked to the dashboard: %q", got)
	}
	if got := child.String(); got != "ls -la\r" {
		t.Fatalf("child received %q, want held bytes followed by live bytes", got)
	}
}

func TestSessionInputRouterForwardedKeysPrecedeLiveBytes(t *testing.T) {
	router := NewSessionInputRouter(nil)
	router.Prepare(terminalCellRect{Width: 80, Height: 24}, 0)

	// Home re-encodes the KeyMsgs that shared a stdin read with Enter and
	// forwards them; anything read afterwards is routed raw.
	router.Forward([]byte("ec"))
	_ = routeTestBytes(t, router, "ho hi")

	var child bytes.Buffer
	router.Activate(&child, terminalCellRect{Width: 80, Height: 24}, 0)
	if got := child.String(); got != "echo hi" {
		t.Fatalf("child received %q, want forwarded keys before raw keys", got)
	}

	// Once the client exists, forwarded stragglers go straight through.
	router.Forward([]byte("\r"))
	if got := child.String(); got != "echo hi\r" {
		t.Fatalf("child received %q after live forward", got)
	}
}

func TestSessionInputRouterDetachWhileConnectingDropsHeldInput(t *testing.T) {
	router := NewSessionInputRouter(nil)
	router.Prepare(terminalCellRect{Width: 80, Height: 24}, 0)

	got := routeTestBytes(t, router, "abc\x11def")
	if !bytes.Equal(got, []byte{0x11}) {
		t.Fatalf("dashboard bytes = %q, want the detach chord alone", got)
	}
	if router.active {
		t.Fatal("detach chord while connecting left the router in session mode")
	}
	if len(router.held) != 0 {
		t.Fatalf("held bytes survived the detach: %q", router.held)
	}

	// Home abandons the connect; a late Activate must not replay stale keys.
	var child bytes.Buffer
	router.Activate(&child, terminalCellRect{Width: 80, Height: 24}, 0)
	if child.Len() != 0 {
		t.Fatalf("stale held bytes reached a later client: %q", child.String())
	}
}

func TestSessionInputRouterForwardOutsideSessionModeIsDropped(t *testing.T) {
	router := NewSessionInputRouter(nil)
	router.Forward([]byte("stray"))
	if len(router.held) != 0 {
		t.Fatalf("dashboard-mode forward was held: %q", router.held)
	}
}

// blockingWriter stalls its first Write until released, standing in for a PTY
// whose client has stopped reading.
type blockingWriter struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
	buf     bytes.Buffer
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.buf.Write(p)
}

func TestSessionInputRouterWritesToTheChildOutsideItsLock(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	defer writeFile.Close()

	child := &blockingWriter{release: make(chan struct{}), entered: make(chan struct{})}
	router := NewSessionInputRouter(readFile)
	router.Activate(child, terminalCellRect{Width: 80, Height: 24}, 0)

	readDone := make(chan struct{})
	go func() {
		buf := make([]byte, 32)
		_, _ = router.Read(buf)
		close(readDone)
	}()
	if _, err := io.WriteString(writeFile, "x"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("router never wrote to the child")
	}

	// With the PTY write stalled, Bubble Tea's goroutine must still be able
	// to move the pane, detach, or resize; each takes r.mu.
	done := make(chan struct{})
	go func() {
		router.UpdateRect(terminalCellRect{Width: 40, Height: 12})
		router.Deactivate()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("UpdateRect/Deactivate blocked behind a stalled child write")
	}
	close(child.release)
	if _, err := io.WriteString(writeFile, "\x11"); err != nil {
		t.Fatal(err)
	}
	<-readDone
}

func TestKeyMsgRawBytesReEncodesLegacyKeys(t *testing.T) {
	cases := []struct {
		name string
		msg  tea.KeyMsg
		want string
		ok   bool
	}{
		{"runes", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("héllo")}, "héllo", true},
		{"alt rune", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x"), Alt: true}, "\x1bx", true},
		{"paste", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab"), Paste: true}, bracketedPasteStart + "ab" + bracketedPasteEnd, true},
		{"space", tea.KeyMsg{Type: tea.KeySpace}, " ", true},
		{"enter", tea.KeyMsg{Type: tea.KeyEnter}, "\r", true},
		{"tab", tea.KeyMsg{Type: tea.KeyTab}, "\t", true},
		{"backspace", tea.KeyMsg{Type: tea.KeyBackspace}, "\x7f", true},
		{"escape", tea.KeyMsg{Type: tea.KeyEscape}, "\x1b", true},
		{"ctrl+c", tea.KeyMsg{Type: tea.KeyCtrlC}, "\x03", true},
		{"up", tea.KeyMsg{Type: tea.KeyUp}, "\x1b[A", true},
		{"shift+tab", tea.KeyMsg{Type: tea.KeyShiftTab}, "\x1b[Z", true},
		{"f1 has no legacy form", tea.KeyMsg{Type: tea.KeyF1}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := keyMsgRawBytes(tc.msg)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if string(got) != tc.want {
				t.Fatalf("bytes = %q, want %q", got, tc.want)
			}
		})
	}
}

// End to end through Home: Enter prepares the router, keys typed while the
// PTY connects are held (raw and re-encoded alike), and the install pass
// replays them into the client before anything else.
func TestEmbeddedModeReplaysKeysTypedWhileConnecting(t *testing.T) {
	home, inst, _ := armHomeWithOneSession(t)
	home.embeddedLayout = true
	inst.SetTmuxSessionForTest(tmux.NewSession("focused-session", "/tmp/focused"))
	home.sessionExists = func(*session.Instance) bool { return true }
	router := NewSessionInputRouter(nil)
	home.sessionInput = router

	if !home.enterEmbeddedMode() {
		t.Fatalf("enterEmbeddedMode failed: %v", home.err)
	}
	if !router.active || router.child != nil {
		t.Fatalf("router after Enter: active=%v child=%v, want session mode with no child yet", router.active, router.child)
	}

	// A KeyMsg that shared Enter's stdin read arrives through Bubble Tea.
	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ls")})
	home = model.(*Home)
	// Bytes read after the switch are routed raw by the router itself.
	_ = routeTestBytes(t, router, "\r")

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	// Read the client side raw so the line discipline does not fold the
	// carriage return into a newline before the assertion sees it.
	if _, err := term.MakeRaw(int(tty.Fd())); err != nil {
		t.Skipf("raw mode on pty slave: %v", err)
	}
	emulator := vt.NewSafeEmulator(80, 24)
	defer func() { _ = emulator.Close() }()
	terminal := &embeddedTerminal{ptmx: ptmx, emulator: emulator, dirty: make(chan struct{}, 1)}

	_ = home.installEmbeddedTerminal(embeddedStartMsg{generation: home.embeddedGeneration, terminal: terminal})
	if router.child == nil {
		t.Fatal("install did not activate the router")
	}

	got := make([]byte, 16)
	_ = tty.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := tty.Read(got)
	if err != nil {
		t.Fatalf("read replayed keys from the client side: %v", err)
	}
	if string(got[:n]) != "ls\r" {
		t.Fatalf("client received %q, want the keys typed while connecting, in order", got[:n])
	}
	home.embeddedTerminal = nil // fixture has no child process for Close
}
