package ui

import (
	"io"
	"os"
	"testing"
	"time"
)

// Regression tests for stray "C"/"D" typed into an embedded session while an
// arrow key is held. The router reads stdin a byte at a time and forwarded
// every byte it could not match against its own chords. A terminal in
// application cursor mode sends Right as ESC O C, so ESC O reached the pane
// in one write and C in the next; tmux read ESC O as Alt+O and C as text.
// Parameterized CSI keys (Ctrl+Right = ESC [ 1 ; 5 C) split the same way.

func TestSessionInputRouterBuffersSplitSS3Arrow(t *testing.T) {
	router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	routeTestBytes(t, router, "\x1bO")
	if child.Len() != 0 {
		t.Fatalf("partial SS3 arrow forwarded before its final byte: %q", child.String())
	}
	routeTestBytes(t, router, "C")
	if got := child.String(); got != "\x1bOC" {
		t.Fatalf("child received %q, want one Right arrow %q", got, "\x1bOC")
	}
}

func TestSessionInputRouterBuffersSplitParameterizedCSI(t *testing.T) {
	router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	routeTestBytes(t, router, "\x1b[1;5")
	if child.Len() != 0 {
		t.Fatalf("partial Ctrl+Right forwarded before its final byte: %q", child.String())
	}
	routeTestBytes(t, router, "C")
	if got := child.String(); got != "\x1b[1;5C" {
		t.Fatalf("child received %q, want one Ctrl+Right %q", got, "\x1b[1;5C")
	}
}

func TestSessionInputRouterFlushesHeldSS3Prefix(t *testing.T) {
	router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	router.mu.Lock()
	router.rawBuf = append(router.rawBuf, "\x1bO"...)
	_, toChild := router.routeEmbeddedLocked(true)
	router.mu.Unlock()
	_, _ = child.Write(toChild)
	if got := child.String(); got != "\x1bO" {
		t.Fatalf("held Alt+O on timeout = %q, want %q", got, "\x1bO")
	}
}

// A lone Alt+O (ESC O with nothing after it) must still reach the pane once
// stdin goes quiet, not wait for the next keystroke.
func TestSessionInputRouterReleasesLoneAltOAfterQuietPeriod(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	defer writeFile.Close()

	router := NewSessionInputRouter(readFile)
	child := newConnectingCapture()
	router.Activate(child, terminalCellRect{Width: 80, Height: 24}, 0)
	defer router.Deactivate()
	stop := make(chan struct{})
	done := make(chan struct{})
	// Stop the reader before the deferred Deactivate and pipe closes run.
	defer func() {
		close(stop)
		<-done
	}()
	go func() {
		defer close(done)
		buf := make([]byte, 32)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = router.Read(buf)
		}
	}()

	if _, err := io.WriteString(writeFile, "\x1bO"); err != nil {
		t.Fatal(err)
	}
	awaitConnectingBytes(t, child, "\x1bO")
}

func TestSessionInputRouterPlainTextIsNotHeld(t *testing.T) {
	router, child := newRoutingTestInput(terminalCellRect{Width: 80, Height: 24})
	start := time.Now()
	routeTestBytes(t, router, "hello\x1b[Cx")
	if got := child.String(); got != "hello\x1b[Cx" {
		t.Fatalf("child received %q, want %q", got, "hello\x1b[Cx")
	}
	if time.Since(start) > time.Second {
		t.Fatal("routing complete input blocked")
	}
}
