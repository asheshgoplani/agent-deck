package ui

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"
)

func newRoutingTestInput(rect terminalCellRect) (*SessionInputRouter, *bytes.Buffer) {
	child := new(bytes.Buffer)
	router := &SessionInputRouter{
		active: true,
		child:  child,
		rect:   rect,
		rawBuf: make([]byte, 0, 256),
	}
	return router, child
}

func routeTestBytes(t *testing.T, router *SessionInputRouter, data string) []byte {
	t.Helper()
	router.mu.Lock()
	router.rawBuf = append(router.rawBuf, data...)
	dashboard, err := router.routeEmbeddedLocked(false)
	router.mu.Unlock()
	if err != nil {
		t.Fatalf("routeEmbeddedLocked: %v", err)
	}
	return dashboard
}

func TestSessionInputRouterForwardsRawKeyboardBytes(t *testing.T) {
	router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	input := "hello\r\x1b[A\x1b[13;2u\x1b[27;3;120~"
	if got := routeTestBytes(t, router, input); len(got) != 0 {
		t.Fatalf("dashboard received %q, want no bytes", got)
	}
	if got := child.String(); got != input {
		t.Fatalf("child received %q, want exact raw stream %q", got, input)
	}
}

func TestSessionInputRouterCtrlQDetachesButPastePayloadDoesNot(t *testing.T) {
	router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	paste := bracketedPasteStart + "before\x11after" + bracketedPasteEnd
	if got := routeTestBytes(t, router, paste); len(got) != 0 {
		t.Fatalf("dashboard received paste bytes %q", got)
	}
	if child.String() != paste {
		t.Fatalf("paste changed in transit: got %q, want %q", child.String(), paste)
	}
	if !router.active {
		t.Fatal("Ctrl+Q inside bracketed paste detached the session")
	}

	dashboard := routeTestBytes(t, router, "x\x1b[113;5utail")
	if got, want := string(dashboard), "\x11"; got != want {
		t.Fatalf("detach bytes = %q, want %q", got, want)
	}
	if router.active {
		t.Fatal("CSI-u Ctrl+Q did not deactivate routing")
	}
	if got, want := child.String(), paste+"x"; got != want {
		t.Fatalf("child received bytes after detach: got %q, want %q", got, want)
	}
}

func TestSessionInputRouterSwitchKeyOpensDashboardWithoutLeakingToPane(t *testing.T) {
	sequences := []string{
		"\x17",           // legacy Ctrl+W
		"\x1b[119;5u",    // Kitty CSI-u Ctrl+W
		"\x1b[27;5;119~", // xterm modifyOtherKeys Ctrl+W
	}
	for _, sequence := range sequences {
		router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
		router.switchByte = 0x17

		dashboard := routeTestBytes(t, router, "pane"+sequence+"tail")
		if !bytes.Equal(dashboard, []byte{0x17}) {
			t.Errorf("Ctrl+W %q dashboard bytes = %q, want raw Ctrl+W", sequence, dashboard)
		}
		if got := child.String(); got != "pane" {
			t.Errorf("Ctrl+W %q child bytes = %q, want only bytes before switch", sequence, got)
		}
		if router.active {
			t.Errorf("Ctrl+W %q left embedded routing active", sequence)
		}
	}
}

func TestSessionInputRouterSwitchKeyHonorsPasteAndDisabledBinding(t *testing.T) {
	paste := bracketedPasteStart + "before\x17after" + bracketedPasteEnd
	router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	router.switchByte = 0x17
	if got := routeTestBytes(t, router, paste); len(got) != 0 {
		t.Fatalf("dashboard received switch key from paste payload: %q", got)
	}
	if got := child.String(); got != paste {
		t.Fatalf("paste changed in transit: got %q, want %q", got, paste)
	}
	if !router.active {
		t.Fatal("Ctrl+W inside bracketed paste deactivated routing")
	}

	router, child = newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	if got := routeTestBytes(t, router, "\x17"); len(got) != 0 {
		t.Fatalf("disabled switch binding returned bytes to dashboard: %q", got)
	}
	if got := child.String(); got != "\x17" {
		t.Fatalf("disabled switch binding did not forward Ctrl+W: %q", got)
	}
}

func TestSessionInputRouterBuffersSplitSwitchKeySequence(t *testing.T) {
	router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	router.switchByte = 0x17
	if got := routeTestBytes(t, router, "\x1b[119;"); len(got) != 0 || child.Len() != 0 {
		t.Fatalf("partial switch chord leaked: dashboard=%q child=%q", got, child.String())
	}
	if got := routeTestBytes(t, router, "5u"); !bytes.Equal(got, []byte{0x17}) {
		t.Fatalf("split Ctrl+W = %q, want raw Ctrl+W", got)
	}
	if router.active {
		t.Fatal("split Ctrl+W did not deactivate routing")
	}
}

func TestSessionInputRouterCtrlAltBTogglesSidebarWithoutLeakingToPane(t *testing.T) {
	sequences := []string{
		"\x1b\x02",
		"\x1b[98;7u",
		"\x1b[66;8u",
		"\x1b[27;7;98~",
		"\x1b[27;8;66~",
	}
	for _, sequence := range sequences {
		router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
		dashboard := routeTestBytes(t, router, sequence)
		if !bytes.Equal(dashboard, []byte{embeddedSidebarToggleSignal}) {
			t.Errorf("Ctrl+Alt+B %q dashboard bytes = %q, want private toggle signal", sequence, dashboard)
		}
		if child.Len() != 0 {
			t.Errorf("Ctrl+Alt+B %q leaked to pane: %q", sequence, child.String())
		}
		if !router.active {
			t.Errorf("Ctrl+Alt+B %q detached the embedded session", sequence)
		}
	}

	router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	if got := routeTestBytes(t, router, "\x07"); len(got) != 0 {
		t.Fatalf("literal Ctrl+G leaked to dashboard: %q", got)
	}
	if child.String() != "\x07" {
		t.Fatalf("literal Ctrl+G = %q in pane, want unchanged", child.String())
	}
}

func TestSessionInputRouterBuffersSplitSidebarToggleSequence(t *testing.T) {
	router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	if got := routeTestBytes(t, router, "\x1b[98;"); len(got) != 0 || child.Len() != 0 {
		t.Fatalf("partial sidebar chord leaked: dashboard=%q child=%q", got, child.String())
	}
	if got := routeTestBytes(t, router, "7u"); !bytes.Equal(got, []byte{embeddedSidebarToggleSignal}) {
		t.Fatalf("split sidebar chord = %q, want private toggle signal", got)
	}
}

func TestSessionInputRouterTranslatesMouseCoordinates(t *testing.T) {
	router, child := newRoutingTestInput(terminalCellRect{X: 20, Y: 4, Width: 40, Height: 12})
	inside := "\x1b[<0;21;5M" // first cell in the embedded terminal
	outside := "\x1b[<64;2;2M"
	if got := routeTestBytes(t, router, inside+outside); string(got) != outside {
		t.Fatalf("dashboard mouse = %q, want only outside event %q", got, outside)
	}
	if got, want := child.String(), "\x1b[<0;1;1M"; got != want {
		t.Fatalf("translated mouse = %q, want %q", got, want)
	}
}

func TestSessionInputRouterBuffersSplitSpecialSequences(t *testing.T) {
	router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	if got := routeTestBytes(t, router, "\x1b[11"); len(got) != 0 {
		t.Fatalf("partial sequence leaked to dashboard: %q", got)
	}
	if child.Len() != 0 {
		t.Fatalf("partial sequence leaked to child: %q", child.String())
	}
	if got := routeTestBytes(t, router, "3;5u"); string(got) != "\x11" {
		t.Fatalf("split Ctrl+Q = %q, want raw detach byte", got)
	}
}

func TestSessionInputRouterFlushesStandaloneEscape(t *testing.T) {
	router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	router.mu.Lock()
	router.rawBuf = append(router.rawBuf, '\x1b')
	if _, err := router.routeEmbeddedLocked(false); err != nil {
		t.Fatal(err)
	}
	if child.Len() != 0 {
		t.Fatalf("ambiguous Escape forwarded before timeout: %q", child.String())
	}
	if _, err := router.routeEmbeddedLocked(true); err != nil {
		t.Fatal(err)
	}
	router.mu.Unlock()
	if got := child.String(); got != "\x1b" {
		t.Fatalf("standalone Escape = %q, want raw ESC", got)
	}
}

func TestSessionInputRouterActivationDuringBlockedReadPreservesRawBytes(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	defer writeFile.Close()

	router := NewSessionInputRouter(readFile)
	var child bytes.Buffer
	result := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 32)
		n, _ := router.Read(buf)
		result <- append([]byte(nil), buf[:n]...)
	}()

	// Activate after Read has entered its blocking syscall. A dashboard-owned
	// compatibility reader used to consume and rewrite this first sequence.
	time.Sleep(20 * time.Millisecond)
	router.Activate(&child, terminalCellRect{Width: 80, Height: 24}, 0)
	raw := "\x1b[13;2u"
	if _, err := io.WriteString(writeFile, raw+"\x11"); err != nil {
		t.Fatal(err)
	}

	select {
	case dashboard := <-result:
		if !bytes.Equal(dashboard, []byte{0x11}) {
			t.Fatalf("dashboard bytes = %q, want Ctrl+Q", dashboard)
		}
	case <-time.After(time.Second):
		t.Fatal("router did not return detach key")
	}
	if child.String() != raw {
		t.Fatalf("child bytes = %q, want exact raw sequence %q", child.String(), raw)
	}
}
