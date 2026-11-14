package ui

import (
	"bytes"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/asheshgoplani/agent-deck/internal/ask"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// askFramesWrapper runs the real Home under teatest with a no-op Init
// (seamBTestWrapper pattern) and keeps every frame View produced.
type askFramesWrapper struct {
	home *Home

	mu        sync.Mutex
	frames    []string
	seen      bool
	delivered bool
}

func (w *askFramesWrapper) Init() tea.Cmd { return nil }

func (w *askFramesWrapper) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m, cmd := w.home.Update(msg)
	w.home = m.(*Home)
	w.mu.Lock()
	switch msg.(type) {
	case askSeenMsg:
		w.seen = true
	case askDeliveredMsg:
		w.delivered = true
	}
	w.mu.Unlock()
	return w, cmd
}

func (w *askFramesWrapper) View() string {
	v := w.home.View()
	w.mu.Lock()
	w.frames = append(w.frames, v)
	w.mu.Unlock()
	return v
}

func (w *askFramesWrapper) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		ok := cond()
		w.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (w *askFramesWrapper) anyFrame(sub string) bool {
	for _, f := range w.frames {
		if strings.Contains(plain(f), sub) {
			return true
		}
	}
	return false
}

func askTestHome(t *testing.T, e *askTestEnv) *Home {
	t.Helper()
	h := NewHome()
	h.width, h.height, h.initialLoading = 120, 40, false
	h.updateSizes()
	e.profile = h.profile
	h.askDialog.now = func() time.Time { return time.Now().Add(4 * time.Second) }
	return h
}

// TestAskTUISentinelNeverLeaks types a random sentinel into the TUI modal of
// the real Home under the real Bubble Tea runtime (teatest), with debug
// logging on, through single keys, a multi-rune key read and a bracketed
// paste, presses Save, and then looks for the sentinel in every frame View
// produced, the raw terminal output, the log ring buffer, and every file
// under the test HOMEs (logs, events, ask dir, profile state). The only
// place it may be is the fake vault's store, outside HOME.
func TestAskTUISentinelNeverLeaks(t *testing.T) {
	outerHome := os.Getenv("HOME")
	e := newAskTestEnv(t)
	logDir := filepath.Join(e.home, ".agent-deck", "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	logging.Init(logging.Config{LogDir: logDir, Level: "debug", Debug: true})
	t.Cleanup(func() { logging.Shutdown(); logging.Init(logging.Config{}) })
	uiLog.Debug("ask_leak_test_probe")

	h := askTestHome(t, e)
	rec := e.secret(t, "conductor/GH_TOKEN_DECKBOX")
	poll := h.askDialog.PollCmd(time.Now())
	if poll == nil {
		t.Fatal("no poll command")
	}
	pollMsg := poll()

	w := &askFramesWrapper{home: h}
	tm := teatest.NewTestModel(t, w, teatest.WithInitialTermSize(120, 40))
	tm.Send(tea.WindowSizeMsg{Width: 120, Height: 40})
	tm.Send(pollMsg)
	w.waitFor(t, "the modal with its nonce", func() bool { return w.seen && w.anyFrame("asks for a secret") })

	p1, p2, p3 := randomSentinel(t), randomSentinel(t), randomSentinel(t)
	parts := []string{p1, p2, p3}
	tm.Type(p1)                                                             // one key per rune
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(p2), Paste: true}) // bracketed paste
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(p3)})              // a coalesced read
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
	w.waitFor(t, "deliver", func() bool { return w.delivered })
	w.waitFor(t, "the stored line", func() bool { return w.anyFrame("Stored conductor/GH_TOKEN_DECKBOX") })

	if err := tm.Quit(); err != nil {
		t.Logf("tm.Quit: %v", err)
	}
	final := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*askFramesWrapper)
	out, _ := io.ReadAll(tm.FinalOutput(t, teatest.WithFinalTimeout(5*time.Second)))

	if v := final.home.askDialog.input.Value(); v != "" {
		t.Fatal("the textinput still holds a value after Deliver")
	}
	value := p1 + p2 + p3
	if got, ok := e.stored(rec.Name); !ok || got != value {
		t.Fatalf("fake vault store did not get the typed value (ok=%v, len=%d want %d)", ok, len(got), len(value))
	}

	hits := func(where string, b []byte) {
		for _, p := range parts {
			if bytes.Contains(b, []byte(p)) {
				t.Errorf("sentinel found in %s", where)
				return
			}
		}
	}
	final.mu.Lock()
	if len(final.frames) < 5 {
		t.Errorf("only %d frames captured", len(final.frames))
	}
	for i, f := range final.frames {
		hits("frame "+string(rune('0'+i%10)), []byte(f))
	}
	final.mu.Unlock()
	hits("the terminal output", out)

	dump := filepath.Join(e.home, "ring-dump.log")
	if err := logging.DumpRingBuffer(dump); err != nil {
		t.Fatal(err)
	}
	logBytes, err := os.ReadFile(filepath.Join(logDir, "debug.log"))
	if err != nil || !bytes.Contains(logBytes, []byte("ask_leak_test_probe")) {
		t.Fatalf("debug logging was not live (err=%v)", err)
	}

	var files int
	for _, root := range []string{e.home, outerHome} {
		if root == "" {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() || !de.Type().IsRegular() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			files++
			hits(path, b)
			return nil
		})
	}
	if files == 0 {
		t.Fatal("walked no files")
	}
	askDir, err := ask.Dir(e.profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(askDir, rec.RequestID+".json")); err != nil {
		t.Fatalf("the request record is not where the walk looked: %v", err)
	}
	if !strings.HasPrefix(askDir, e.home+string(os.PathSeparator)) && (outerHome == "" || !strings.HasPrefix(askDir, outerHome+string(os.PathSeparator))) {
		t.Fatalf("ask dir %s is outside the walked HOMEs", askDir)
	}
}

// TestAskHomeModalKeysNeverReachKeypressLog: with the modal open no key is
// logged; once it is closed the keypress log works again (so this test sees
// a regression in the routing).
func TestAskHomeModalKeysNeverReachKeypressLog(t *testing.T) {
	e := newAskTestEnv(t)
	var buf bytes.Buffer
	prev := uiLog
	uiLog = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { uiLog = prev })

	h := askTestHome(t, e)
	e.secret(t, "conductor/GH_TOKEN_DECKBOX")
	h.Update(h.askDialog.PollCmd(time.Now())())
	if !h.askDialog.IsVisible() {
		t.Fatal("modal not shown")
	}
	for _, r := range "Qz7" {
		h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if strings.Contains(buf.String(), "keypress") || strings.Contains(buf.String(), "Qz7") {
		t.Fatalf("modal keys reached the log:\n%s", buf.String())
	}
	if h.askDialog.input.Value() != "Qz7" {
		t.Fatal("keys did not reach the modal's field")
	}
	h.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.askDialog.IsVisible() {
		t.Fatal("Esc did not close the modal")
	}
	h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if !strings.Contains(buf.String(), "keypress") {
		t.Fatal("control: a dashboard key should be logged once the modal is closed")
	}
}

func TestAskHomeTickPolls(t *testing.T) {
	e := newAskTestEnv(t)
	h := askTestHome(t, e)
	now := time.Now()
	h.lastLogCheck, h.lastCachePrune = now, now
	h.agentsLoaded, h.agentsLastRefresh = true, now
	h.uiStateSaveTicks = 0
	h.Update(tickMsg(now))
	if !h.askDialog.pollInFlight {
		t.Fatal("the tick did not start an ask poll")
	}
	if h.askDialog.PollCmd(now.Add(5*time.Second)) != nil {
		t.Fatal("a second poll started while one is in flight")
	}
	h.Update(askPollMsg{})
	if h.askDialog.PollCmd(now.Add(500*time.Millisecond)) != nil {
		t.Fatal("polls must be at least a second apart")
	}
}

func TestAskHomeCanShowOnlyInTheDashboard(t *testing.T) {
	e := newAskTestEnv(t)
	h := askTestHome(t, e)
	now := time.Now()
	if !h.canShowAsk(now) {
		t.Fatal("baseline: an idle dashboard can show the modal")
	}
	for _, tc := range []struct {
		name  string
		set   func()
		reset func()
	}{
		{"insert mode", func() { h.insertMode = true }, func() { h.insertMode = false }},
		{"embedded session", func() { h.embeddedMode = true }, func() { h.embeddedMode = false }},
		{"attached", func() { h.isAttaching.Store(true) }, func() { h.isAttaching.Store(false) }},
		{"another modal", func() { h.confirmDialog.ShowNotice("x", "y") }, func() { h.confirmDialog.Hide() }},
		{"typing", func() { h.lastUserInputTime = now.Add(-200 * time.Millisecond) }, func() { h.lastUserInputTime = time.Time{} }},
		{"tiny terminal", func() { h.width = 20 }, func() { h.width = 120 }},
	} {
		tc.set()
		if h.canShowAsk(now) {
			t.Errorf("%s: modal allowed", tc.name)
		}
		tc.reset()
	}
	e.secret(t, "conductor/GH_TOKEN_DECKBOX")
	h.insertMode = true
	h.Update(h.askDialog.PollCmd(now)())
	if h.askDialog.IsVisible() {
		t.Fatal("modal opened in insert mode")
	}
}

func TestAskHomeMouseSwallowedWhileOpen(t *testing.T) {
	e := newAskTestEnv(t)
	h := askTestHome(t, e)
	at := time.Now()
	locals := []*session.Instance{clockLocal("a", at), clockLocal("b", at), clockLocal("c", at)}
	h.instances = locals
	h.groupTree = session.NewGroupTree(locals)
	h.rebuildFlatItems()
	e.secret(t, "conductor/GH_TOKEN_DECKBOX")
	h.Update(h.askDialog.PollCmd(time.Now())())
	if !h.askDialog.IsVisible() {
		t.Fatal("modal not shown")
	}
	wheel := tea.MouseMsg{X: 5, Y: 6, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}
	before := h.cursor
	_, cmd := h.Update(wheel)
	if cmd != nil || h.cursor != before || !h.askDialog.IsVisible() {
		t.Fatalf("a mouse event acted while the ask modal was open (cursor %d -> %d)", before, h.cursor)
	}
	if v := plain(h.View()); !strings.Contains(v, "asks for a secret") {
		t.Fatalf("Home view does not show the modal:\n%s", v)
	}
	// Control: the same wheel event moves the list once the modal is closed.
	h.Update(tea.KeyMsg{Type: tea.KeyEsc})
	h.Update(wheel)
	h.Update(wheel)
	if h.cursor == before {
		t.Fatal("control: the wheel did not move the list with the modal closed, so this test proves nothing")
	}
}
