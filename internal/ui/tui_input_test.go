package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"
)

// #2692: a user's TUI held 2,822 kqueue descriptors after days of attaching.
// Every tea.Exec restores the terminal with a new input reader and Bubble Tea
// v1.3.10 never closes the old one; for a file-like input (one with Name())
// that reader owns a kqueue (macOS) or epoll (Linux) descriptor. These tests
// run the TUI's own input (NewTUIInput) on a pty through release/restore
// cycles, which is exactly what each attach does.

type keyModel struct {
	started chan struct{}
	mu      *sync.Mutex
	keys    *[]string
}

func (m keyModel) Init() tea.Cmd { return func() tea.Msg { close(m.started); return nil } }
func (m keyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		m.mu.Lock()
		*m.keys = append(*m.keys, k.String())
		m.mu.Unlock()
	}
	return m, nil
}
func (m keyModel) View() string { return "" }

type tuiInputRig struct {
	ptmx, tty *os.File
	p         *tea.Program
	mu        sync.Mutex
	keys      []string
	done      chan struct{}
}

func newTUIInputRig(t *testing.T, input func(*os.File) any) *tuiInputRig {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("counts epoll instances in /proc/self/fd")
	}
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	r := &tuiInputRig{ptmx: ptmx, tty: tty, done: make(chan struct{})}
	m := keyModel{started: make(chan struct{}), mu: &r.mu, keys: &r.keys}
	in := input(tty)
	r.p = tea.NewProgram(m, tea.WithInput(in.(interface{ Read([]byte) (int, error) })), tea.WithOutput(tty), tea.WithoutSignalHandler())
	go func() { _, _ = r.p.Run(); close(r.done) }()
	go func() { // drain the program's output
		buf := make([]byte, 4096)
		for {
			if _, err := ptmx.Read(buf); err != nil {
				return
			}
		}
	}()
	select {
	case <-m.started:
	case <-time.After(10 * time.Second):
		t.Fatal("program did not start")
	}
	t.Cleanup(func() {
		r.p.Quit()
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
		}
		_ = ptmx.Close()
		_ = tty.Close()
	})
	return r
}

func (r *tuiInputRig) sawKey(k string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, got := range r.keys {
		if got == k {
			return true
		}
	}
	return false
}

func tuiWaitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func tuiEpollCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name())); err == nil && target == "anon_inode:[eventpoll]" {
			n++
		}
	}
	return n
}

func tuiInputOnPTY(tty *os.File) any { return NewTUIInput(tty) }

// Each attach must not leave a poll descriptor behind.
func TestTUIInputAttachCyclesDoNotLeakPollDescriptors(t *testing.T) {
	r := newTUIInputRig(t, tuiInputOnPTY)
	before := tuiEpollCount(t)
	const attaches = 20
	for i := 0; i < attaches; i++ {
		if err := r.p.ReleaseTerminal(); err != nil {
			t.Fatalf("release: %v", err)
		}
		if err := r.p.RestoreTerminal(); err != nil {
			t.Fatalf("restore: %v", err)
		}
	}
	runtime.GC()
	if after := tuiEpollCount(t); after-before > 1 {
		t.Fatalf("%d attach cycles left %d more poll descriptors open (%d -> %d)", attaches, after-before, before, after)
	}
}

// While the terminal is released (an attached session owns it), the TUI must
// not read stdin: what the user types belongs to the attached program.
func TestTUIInputReleasedTerminalLeavesInputAlone(t *testing.T) {
	r := newTUIInputRig(t, tuiInputOnPTY)
	if _, err := r.ptmx.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	tuiWaitFor(t, "the TUI to get a key before the release", func() bool { return r.sawKey("a") })

	start := time.Now()
	if err := r.p.ReleaseTerminal(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Fatalf("release waited %s for the input reader to stop", took)
	}
	// Typed into the attached session. Release put the tty back in line mode,
	// so a line reaches a reader only with its newline.
	if _, err := r.ptmx.Write([]byte("zq\n")); err != nil {
		t.Fatal(err)
	}
	// The attached program reads the tty. A read blocks, so it runs aside and
	// whatever arrived within 2 s counts.
	var gotMu sync.Mutex
	got := make([]byte, 0, 2)
	go func() {
		buf := make([]byte, 8)
		for {
			n, err := r.tty.Read(buf)
			if err != nil {
				return
			}
			gotMu.Lock()
			got = append(got, buf[:n]...)
			full := len(got) >= 2
			gotMu.Unlock()
			if full {
				return
			}
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		gotMu.Lock()
		full := len(got) >= 2
		gotMu.Unlock()
		if full {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	gotMu.Lock()
	defer gotMu.Unlock()
	if !strings.HasPrefix(string(got), "zq") {
		t.Fatalf("the attached program got %q of what was typed, want \"zq\" (the TUI took the rest)", got)
	}

	if err := r.p.RestoreTerminal(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := r.ptmx.Write([]byte("b")); err != nil {
		t.Fatal(err)
	}
	tuiWaitFor(t, "the TUI to get keys again after the restore", func() bool { return r.sawKey("b") })
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.Contains(strings.Join(r.keys, ""), "z") {
		t.Fatalf("the TUI read input typed while released: %v", r.keys)
	}
}
