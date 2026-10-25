package ui

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// #2692: a user's TUI held 2,822 kqueue descriptors after days of attaching.
// Every tea.Exec restores the terminal with a new input reader and Bubble Tea
// v1.3.10 never closes the old one; for a file-like input (one with Name())
// that reader owns a kqueue (macOS) or epoll (Linux) descriptor. These tests
// run the TUI's own input (NewTUIInput) on a pty through release/restore
// cycles, which is exactly what each attach does.

type keyModel struct {
	started chan struct{}
	sizes   chan struct{}
	mu      *sync.Mutex
	keys    *[]string
	onKey   *func(string) tea.Cmd
}

func (m keyModel) Init() tea.Cmd { return func() tea.Msg { close(m.started); return nil } }
func (m keyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.WindowSizeMsg); ok {
		select {
		case m.sizes <- struct{}{}:
		default:
		}
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		m.mu.Lock()
		*m.keys = append(*m.keys, k.String())
		on := *m.onKey
		m.mu.Unlock()
		if on != nil {
			return m, on(k.String())
		}
	}
	return m, nil
}
func (m keyModel) View() string { return "" }

type tuiInputRig struct {
	ptmx, tty *os.File
	p         *tea.Program
	mu        sync.Mutex
	keys      []string
	onKey     func(string) tea.Cmd
	sizes     chan struct{}
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
	r := &tuiInputRig{ptmx: ptmx, tty: tty, sizes: make(chan struct{}, 64), done: make(chan struct{})}
	m := keyModel{started: make(chan struct{}), sizes: r.sizes, mu: &r.mu, keys: &r.keys, onKey: &r.onKey}
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
	r.waitResizeCheck(t) // the start's own size check
	t.Cleanup(func() {
		r.p.Quit()
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
			t.Error("program did not exit; closing its terminal anyway")
		}
		_ = ptmx.Close()
		_ = tty.Close()
	})
	return r
}

// Bubble Tea checks the terminal size on a goroutine of its own at the start
// and after every RestoreTerminal, and Run does not wait for it. Its
// WindowSizeMsg is the only sign it is done with the tty, so the test waits for
// it before the next restore or the cleanup closes the tty (defect 303: a race
// under -race otherwise).
func (r *tuiInputRig) waitResizeCheck(t *testing.T) {
	t.Helper()
	select {
	case <-r.sizes:
	case <-time.After(5 * time.Second):
		t.Fatal("no size check after the start or restore")
	}
}

func (r *tuiInputRig) restore(t *testing.T) {
	t.Helper()
	if err := r.p.RestoreTerminal(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	r.waitResizeCheck(t)
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
		r.restore(t)
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

	r.restore(t)
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

// ttyReadCmd is an attached program: it reads the terminal until a newline.
type ttyReadCmd struct {
	tty *os.File
	got chan string
}

func (c ttyReadCmd) SetStdin(io.Reader)  {}
func (c ttyReadCmd) SetStdout(io.Writer) {}
func (c ttyReadCmd) SetStderr(io.Writer) {}
func (c ttyReadCmd) Run() error {
	var b []byte
	buf := make([]byte, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !strings.Contains(string(b), "\n") {
			n, err := c.tty.Read(buf)
			if err != nil {
				return
			}
			b = append(b, buf[:n]...)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	c.got <- string(b)
	return nil
}

// Lander review of #2815: a key typed while the release is under way (here 5
// to 40 ms after the attach starts) must reach the attached program. The
// fallback reader drops bytes read across a cancel, so the TUI must not read
// once a release begins.
func TestTUIInputKeysTypedAsTheAttachStartsReachTheAttachedProgram(t *testing.T) {
	r := newTUIInputRig(t, tuiInputOnPTY)
	got := make(chan string, 1)
	r.mu.Lock()
	r.onKey = func(k string) tea.Cmd {
		if k != "x" {
			return nil
		}
		return execHoldingInput(ttyReadCmd{tty: r.tty, got: got}, func(error) tea.Msg { return nil })
	}
	r.mu.Unlock()
	for _, d := range []time.Duration{5, 10, 20, 30, 40, 5, 10, 20, 30, 40} {
		if _, err := r.ptmx.Write([]byte("x")); err != nil { // attach
			t.Fatal(err)
		}
		time.Sleep(d * time.Millisecond)
		if _, err := r.ptmx.Write([]byte("zq\n")); err != nil { // typed into the attached program
			t.Fatal(err)
		}
		select {
		case s := <-got:
			if !strings.HasPrefix(s, "zq") {
				t.Fatalf("typed %d ms after the attach began: the attached program got %q, want \"zq\"", d, s)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("typed %d ms after the attach began: the attached program never finished", d)
		}
		r.waitResizeCheck(t) // the exec's restore
	}
	if tuiInputHeld.Load() != 0 {
		t.Fatalf("the input hold must be released after every exec, held=%d", tuiInputHeld.Load())
	}
}

// Lander review of #2815: an incomplete escape sequence in the translator
// must not leave the read loop in a blocking stdin read when the terminal is
// released, or the release waits 500 ms and the stale read takes the
// attached program's first keystroke.
func TestTUIInputIncompleteSequenceDoesNotBlockTheRelease(t *testing.T) {
	r := newTUIInputRig(t, tuiInputOnPTY)
	if _, err := r.ptmx.Write([]byte("\x1b[1;")); err != nil { // half a CSI sequence
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond) // the reader takes it and waits for the rest
	start := time.Now()
	if err := r.p.ReleaseTerminal(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if took := time.Since(start); took > 400*time.Millisecond {
		t.Fatalf("release waited %s: the reader sat in a blocking stdin read", took)
	}
	got := make(chan string, 1)
	go func() { _ = ttyReadCmd{tty: r.tty, got: got}.Run() }()
	if _, err := r.ptmx.Write([]byte("zq\n")); err != nil {
		t.Fatal(err)
	}
	if s := <-got; !strings.Contains(s, "zq") {
		t.Fatalf("the attached program got %q, want \"zq\"", s)
	}
	r.restore(t)
}

// Every exec of the TUI goes through the input hold (#2692): a bare tea.Exec
// or tea.ExecProcess would let the TUI read, and drop, keys meant for the
// attached program while the terminal is handed over.
func TestTUIExecsAllHoldTheInput(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "tui_input.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, bare := range []string{"tea.Exec(", "tea.ExecProcess("} {
			if strings.Contains(string(src), bare) {
				t.Errorf("%s calls %s directly; use execHoldingInput / execProcessHoldingInput", f, bare)
			}
		}
	}
}

// ASK-92: an attach does synchronous setup (tmux calls) before it returns its
// exec. A key typed during that setup, here 5 ms after the attach key while
// the setup takes 40 ms, must reach the attached program, not the TUI.
func TestTUIInputKeysTypedDuringAttachSetupReachTheAttachedProgram(t *testing.T) {
	r := newTUIInputRig(t, tuiInputOnPTY)
	got := make(chan string, 1)
	r.mu.Lock()
	r.onKey = func(k string) tea.Cmd {
		if k != "x" {
			return nil
		}
		defer holdInputForExec()()
		time.Sleep(40 * time.Millisecond) // the attach's tmux setup
		return execHoldingInput(ttyReadCmd{tty: r.tty, got: got}, func(error) tea.Msg { return nil })
	}
	r.mu.Unlock()
	for round := 0; round < 3; round++ {
		if _, err := r.ptmx.Write([]byte("x")); err != nil { // attach
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
		if _, err := r.ptmx.Write([]byte("zq\n")); err != nil { // typed during the setup
			t.Fatal(err)
		}
		select {
		case s := <-got:
			if !strings.HasPrefix(s, "zq") {
				t.Fatalf("round %d: the attached program got %q, want \"zq\" (the TUI took the rest)", round, s)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: the attached program never finished", round)
		}
		time.Sleep(150 * time.Millisecond) // the restore
	}
	r.mu.Lock()
	keys := strings.Join(r.keys, "")
	r.mu.Unlock()
	if strings.ContainsAny(keys, "zq") {
		t.Fatalf("the TUI read keys typed during the attach setup: %q", keys)
	}
	if n := tuiInputHeld.Load(); n != 0 {
		t.Fatalf("the input hold must be released after every exec, held=%d", n)
	}
}

// ASK-92: a hold taken for an attach that builds no exec (a restart instead,
// an error) is given back at once, nested holds are one hold, and an exec
// that is built but never run gives its hold back after the timeout.
func TestHoldInputForExecAlwaysGivesTheInputBack(t *testing.T) {
	if n := tuiInputHeld.Load(); n != 0 {
		t.Fatalf("held=%d before the test", n)
	}
	func() {
		defer holdInputForExec()()
		defer holdInputForExec()() // nested: attachSelectedLegacy -> attachSession
		if n := tuiInputHeld.Load(); n != 1 {
			t.Fatalf("held=%d while preparing, want 1", n)
		}
	}()
	if n := tuiInputHeld.Load(); n != 0 {
		t.Fatalf("no exec was built: held=%d, want 0", n)
	}

	old := tuiInputPreHoldTimeout
	tuiInputPreHoldTimeout = 100 * time.Millisecond
	t.Cleanup(func() { tuiInputPreHoldTimeout = old })
	func() {
		defer holdInputForExec()()
		_ = execHoldingInput(ttyReadCmd{}, nil) // built, then dropped
	}()
	if n := tuiInputHeld.Load(); n != 1 {
		t.Fatalf("the exec took the hold over: held=%d, want 1", n)
	}
	tuiWaitFor(t, "the dropped exec to give the input back", func() bool { return tuiInputHeld.Load() == 0 })
}

// ASK-92 review round: createRemoteSessionWithOptions loads the user config
// before it builds its exec, so it takes the input hold first and the built
// exec takes that hold over. The input is held as soon as the exec is built,
// before Bubble Tea runs it; without the early hold it would stay free until
// then and keys typed meanwhile would become TUI keys.
func TestRemoteCreateAttachHoldsTheInputDuringSetup(t *testing.T) {
	withTempAgentDeckHome(t, `
[remotes.lab]
host = "alice@lab.example"
agent_deck_path = "/usr/local/bin/agent-deck"
`)
	if n := tuiInputHeld.Load(); n != 0 {
		t.Fatalf("held=%d before the test", n)
	}
	old := tuiInputPreHoldTimeout
	tuiInputPreHoldTimeout = 100 * time.Millisecond
	t.Cleanup(func() { tuiInputPreHoldTimeout = old })

	h := NewHome()
	if cmd := h.createRemoteSessionWithOptions("lab", session.RemoteAddOptions{}); cmd == nil {
		t.Fatal("no command returned")
	}
	if n := tuiInputHeld.Load(); n != 1 {
		t.Fatalf("held=%d once the remote create built its exec, want 1 (hold taken during setup)", n)
	}
	tuiWaitFor(t, "the unrun exec to give the input back", func() bool { return tuiInputHeld.Load() == 0 })
}
