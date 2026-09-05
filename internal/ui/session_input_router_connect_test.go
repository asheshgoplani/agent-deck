package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func awaitConnectingBytes(t *testing.T, w *connectingCapture, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for w.snapshot() != want {
		select {
		case <-w.writes:
		case <-deadline:
			t.Fatalf("writer bytes = %q, want %q", w.snapshot(), want)
		}
	}
}

// This writer accepts a short prefix, then blocks until the test permits
// progress or cancellation closes it. Its accepted prefix cannot be recalled.
type connectingBlockedWriter struct {
	*connectingCapture
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	partial bool
}

func newConnectingBlockedWriter(partial bool) *connectingBlockedWriter {
	return &connectingBlockedWriter{connectingCapture: newConnectingCapture(), entered: make(chan struct{}), release: make(chan struct{}), partial: partial}
}

func (w *connectingBlockedWriter) Write(p []byte) (int, error) {
	first := false
	w.once.Do(func() { first = true })
	if first && w.partial && len(p) > 1 {
		return w.connectingCapture.Write(p[:1])
	}
	select {
	case <-w.entered:
	default:
		close(w.entered)
	}
	select {
	case <-w.closed:
		return 0, io.ErrClosedPipe
	case <-w.release:
		return w.connectingCapture.Write(p)
	}
}

func TestSessionInputQueueOrdersConnectingPartialAndLiveWrites(t *testing.T) {
	q := newSessionInputQueue(context.Background(), 1)
	defer q.stop()
	if _, err := q.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	w := newConnectingBlockedWriter(true)
	if !q.install(w) {
		t.Fatal("install rejected")
	}
	awaitConnectingSignal(t, w.entered, "partial write did not resume")
	if got := w.snapshot(); got != "a" {
		t.Fatalf("accepted prefix = %q", got)
	}
	if _, err := q.Write([]byte("界\r")); err != nil {
		t.Fatal(err)
	}
	close(w.release)
	awaitConnectingBytes(t, w.connectingCapture, "abc界\r")
}

func TestSessionInputRouterCtrlQCancelsBlockedWriter(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	defer writeFile.Close()
	router := NewSessionInputRouter(readFile)
	defer router.Deactivate()
	q := router.BeginConnecting(context.Background(), 7, terminalCellRect{Width: 80, Height: 24}, 0)
	w := newConnectingBlockedWriter(false)
	if !router.Install(7, w) {
		t.Fatal("install rejected")
	}
	result := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := router.Read(buf)
			if n > 0 || err != nil {
				result <- append([]byte(nil), buf[:n]...)
				return
			}
		}
	}()
	if _, err := io.WriteString(writeFile, "pending"); err != nil {
		t.Fatal(err)
	}
	awaitConnectingSignal(t, w.entered, "PTY write never blocked")
	if _, err := io.WriteString(writeFile, "\x11"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if string(got) != "\x11" {
			t.Fatalf("dashboard = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl+Q blocked behind PTY Write")
	}
	awaitConnectingSignal(t, w.closed, "cancel did not close the blocked writer")
	if err := q.result(); err != nil {
		t.Fatalf("explicit cancellation became a write error: %v", err)
	}
	if got := w.snapshot(); got != "" {
		t.Fatalf("unsent bytes delivered after cancellation: %q", got)
	}
	fresh := router.BeginConnecting(context.Background(), 8, terminalCellRect{}, 0)
	if router.Install(7, newConnectingCapture()) {
		t.Fatal("stale generation installed")
	}
	if _, err := fresh.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	freshWriter := newConnectingCapture()
	if !router.Install(8, freshWriter) {
		t.Fatal("new generation rejected")
	}
	awaitConnectingBytes(t, freshWriter, "new")
}

type connectingPartialErrorWriter struct{ *connectingCapture }

func (w *connectingPartialErrorWriter) Write(p []byte) (int, error) {
	n, _ := w.connectingCapture.Write(p[:1])
	return n, io.ErrUnexpectedEOF
}

func TestSessionInputQueuePartialErrorDoesNotReplay(t *testing.T) {
	q := newSessionInputQueue(context.Background(), 1)
	defer q.stop()
	if _, err := q.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	w := &connectingPartialErrorWriter{newConnectingCapture()}
	if !q.install(w) {
		t.Fatal("install rejected")
	}
	awaitConnectingSignal(t, w.closed, "write error did not close the generation")
	if err := q.result(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("result = %v", err)
	}
	if _, err := q.Write([]byte("later")); err == nil {
		t.Fatal("failed generation accepted more bytes")
	}
	if got := w.snapshot(); got != "a" {
		t.Fatalf("partial write was replayed: %q", got)
	}
}

func TestSessionInputRouterOverflowQuarantinesSplitPasteTail(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	defer writeFile.Close()
	router := NewSessionInputRouter(readFile)
	defer router.Deactivate()
	q := router.BeginConnecting(context.Background(), 1, terminalCellRect{}, 0)
	// Fill all but one byte. Read must detect overflow on the paste start,
	// then stop consuming until the UI observes the generation error.
	if _, err := q.Write([]byte(strings.Repeat("x", maxConnectingInputBytes-1))); err != nil {
		t.Fatal(err)
	}
	result := make(chan string, 1)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := router.Read(buf)
			if n > 0 || err != nil {
				result <- string(buf[:n])
				return
			}
		}
	}()
	if _, err := io.WriteString(writeFile, bracketedPasteStart+"\x11\r\x1b[20"); err != nil {
		t.Fatal(err)
	}
	awaitConnectingSignal(t, q.ctx.Done(), "paste overflow not reported")
	if err := q.result(); !errors.Is(err, errConnectingInputOverflow) {
		t.Fatalf("result = %v", err)
	}
	router.Deactivate() // Home's error handler abandons this generation.
	if _, err := io.WriteString(writeFile, "1~z"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-result:
		if got != "z" {
			t.Fatalf("paste tail escaped into dashboard: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("quarantine did not release after split paste end")
	}
}

func TestSessionInputRouterInstallRetainsSplitPasteAndControl(t *testing.T) {
	router := NewSessionInputRouter(nil)
	defer router.Deactivate()
	router.BeginConnecting(context.Background(), 1, terminalCellRect{}, 0)
	feed := func(s string) {
		t.Helper()
		router.mu.Lock()
		router.rawBuf = append(router.rawBuf, s...)
		dashboard, err := router.routeToQueueLocked(false)
		router.mu.Unlock()
		if err != nil || len(dashboard) != 0 {
			t.Fatalf("route = %q, %v", dashboard, err)
		}
	}
	feed(bracketedPasteStart + "界\x1b[20")
	w := newConnectingCapture()
	if !router.Install(1, w) {
		t.Fatal("install rejected")
	}
	feed("1~\x1b[13;")
	feed("2u")
	awaitConnectingBytes(t, w, bracketedPasteStart+"界"+bracketedPasteEnd+"\x1b[13;2u")
}

func TestSessionInputRouterActiveMouseDecisionOwnsFollowingRawTail(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	defer writeFile.Close()
	router := NewSessionInputRouter(readFile)
	defer router.Deactivate()
	router.BeginConnecting(context.Background(), 1, terminalCellRect{X: 20, Width: 80, Height: 24}, 0)
	old := newConnectingCapture()
	if !router.Install(1, old) {
		t.Fatal("old install rejected")
	}
	mouse := "\x1b[<0;2;2M"
	want := "界\x1bOP\x1b[13;2u\r"
	if _, err := io.WriteString(writeFile, mouse+want+"\x1b[98;7u"); err != nil {
		t.Fatal(err)
	}
	readEvent := func() string {
		t.Helper()
		buf := make([]byte, 256)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			n, err := router.Read(buf)
			if err != nil {
				t.Fatal(err)
			}
			if n > 0 {
				return string(buf[:n])
			}
		}
		t.Fatal("input event never returned")
		return ""
	}
	if got := readEvent(); got != mouse {
		t.Fatalf("first event = %q", got)
	}
	if got := old.snapshot(); got != "" {
		t.Fatalf("undecided suffix reached old session: %q", got)
	}
	// Even another decoder Read cannot consume the suffix while Home's
	// decision is outstanding. It may yield empty for cancelreader shutdown.
	buf := make([]byte, 256)
	if n, err := router.Read(buf); n != 0 || err != nil {
		t.Fatalf("read before decision = %q, %v", buf[:n], err)
	}
	router.BeginConnecting(context.Background(), 2, terminalCellRect{}, 0)
	fresh := newConnectingCapture()
	if !router.Install(2, fresh) {
		t.Fatal("new install rejected")
	}
	if got := readEvent(); got != string(embeddedSidebarToggleSignal) {
		t.Fatalf("flush marker = %q", got)
	}
	awaitConnectingBytes(t, fresh, want)
	if got := old.snapshot(); got != "" {
		t.Fatalf("suffix crossed generation: %q", got)
	}
}

func TestSessionInputRouterCancelDiscardsBufferedDashboardShortcuts(t *testing.T) {
	readFile, writeFile, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	defer writeFile.Close()
	router := NewSessionInputRouter(readFile)
	defer router.Deactivate()
	router.BeginConnecting(context.Background(), 1, terminalCellRect{}, 0)
	if _, err := io.WriteString(writeFile, "\x11\rdelete"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	if n, err := router.Read(buf); err != nil || string(buf[:n]) != "\x11" {
		t.Fatalf("detach = %q, %v", buf[:n], err)
	}
	router.Deactivate() // Home finishes the detach event.
	if n, err := router.Read(buf); n != 0 || err != nil {
		t.Fatalf("cancelled suffix became input: %q, %v", buf[:n], err)
	}
	// An idle read ends cancellation's buffered-tail quarantine.
	if n, err := router.Read(buf); n != 0 || err != nil {
		t.Fatalf("idle read = %q, %v", buf[:n], err)
	}
	if _, err := io.WriteString(writeFile, "z"); err != nil {
		t.Fatal(err)
	}
	if n, err := router.Read(buf); err != nil || string(buf[:n]) != "z" {
		t.Fatalf("fresh dashboard input = %q, %v", buf[:n], err)
	}
}

func TestSessionInputRouterDashboardStillFiltersEscapeStringReplies(t *testing.T) {
	for _, reply := range []string{"\x1b]0;not-dashboard-input\a", "\x1bP>|terminal-version\x1b\\"} {
		t.Run(reply, func(t *testing.T) {
			readFile, writeFile, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer readFile.Close()
			defer writeFile.Close()
			router := NewSessionInputRouter(readFile)
			defer router.Deactivate()
			if _, err := io.WriteString(writeFile, reply+"z"); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 256)
			if n, err := router.Read(buf); err != nil || string(buf[:n]) != "z" {
				t.Fatalf("reply became dashboard input: %q, %v", buf[:n], err)
			}
		})
	}
}

func TestSessionInputRouterFailedGenerationDiscardsNonPasteTail(t *testing.T) {
	for _, failure := range []string{"overflow", "write error before reader observes failure"} {
		t.Run(failure, func(t *testing.T) {
			readFile, writeFile, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer readFile.Close()
			defer writeFile.Close()
			router := NewSessionInputRouter(readFile)
			defer router.Deactivate()
			q := router.BeginConnecting(context.Background(), 1, terminalCellRect{}, 0)
			wantErr := errConnectingInputOverflow
			if failure == "overflow" {
				if _, err := q.Write([]byte(strings.Repeat("x", maxConnectingInputBytes))); err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(writeFile, "!\rdelete"); err != nil {
					t.Fatal(err)
				}
				// A real read overflows on '!', leaving the apparent dashboard
				// shortcuts unread while the error waits for Home's update.
				buf := make([]byte, 256)
				if n, err := router.Read(buf); n != 0 || err != nil {
					t.Fatalf("failed read = %q, %v", buf[:n], err)
				}
			} else {
				wantErr = io.ErrUnexpectedEOF
				w := &connectingPartialErrorWriter{newConnectingCapture()}
				if _, err := q.Write([]byte("abc")); err != nil {
					t.Fatal(err)
				}
				if !router.Install(1, w) {
					t.Fatal("install rejected")
				}
				awaitConnectingSignal(t, w.closed, "writer did not fail")
				if _, err := io.WriteString(writeFile, "\rdelete"); err != nil {
					t.Fatal(err)
				}
				// No router.Read has set r.failed. Deactivate must inspect the
				// generation error, not depend on the input reader seeing it.
			}
			awaitConnectingSignal(t, q.ctx.Done(), "input failure not reported")
			if err := q.result(); !errors.Is(err, wantErr) {
				t.Fatalf("failure = %v, want %v", err, wantErr)
			}
			router.Deactivate() // Same router action as Home's error handler.
			buf := make([]byte, 256)
			if n, err := router.Read(buf); n != 0 || err != nil {
				t.Fatalf("failed session tail became dashboard input: %q, %v", buf[:n], err)
			}
			if _, err := io.WriteString(writeFile, "z"); err != nil {
				t.Fatal(err)
			}
			if n, err := router.Read(buf); err != nil || string(buf[:n]) != "z" {
				t.Fatalf("fresh dashboard key = %q, %v", buf[:n], err)
			}
			if err := q.result(); !errors.Is(err, wantErr) {
				t.Fatalf("cancellation lost original error: %v", err)
			}
		})
	}
}
