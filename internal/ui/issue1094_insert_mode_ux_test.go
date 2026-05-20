package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #1094 (by @ddorman-dn against v1.9.22): the original insert-mode PR
// (#1076) dropped Backspace, arrow keys, Tab, and control keys, and shelled
// out tmux send-keys for each rune — which made typing visibly slow. These
// tests cover the follow-up fixes: a rune-batching debounce and the missing
// named-key forwarders.

// namedKeyCapture records every dispatchInsertNamedKey call so assertions
// can verify named-key forwarding without running real tmux.
type namedKeyCapture struct {
	calls []namedKeyCall
}

type namedKeyCall struct {
	sessionID string
	key       string
}

func (c *namedKeyCapture) sink(inst *session.Instance, key string) error {
	c.calls = append(c.calls, namedKeyCall{sessionID: inst.ID, key: key})
	return nil
}

// arm1094 returns a Home wired with one focused session, both sinks
// installed, and insert mode already entered (saves boilerplate in every
// test case). Batch duration defaults to sync — individual tests opt into
// batching by setting insertBatchDuration > 0.
func arm1094(t *testing.T) (*Home, *session.Instance, *insertSinkCapture, *namedKeyCapture) {
	t.Helper()
	home, inst, runeCap := armHomeWithOneSession(t)
	namedCap := &namedKeyCapture{}
	home.insertNamedKeySink = namedCap.sink

	// Enter insert mode.
	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)
	if !home.insertMode {
		t.Fatal("test setup: failed to enter insert mode")
	}
	return home, inst, runeCap, namedCap
}

// TestIssue1094_Backspace verifies Backspace forwards "BSpace" to the focused
// session and is NOT swallowed (the v1.9.22 default branch dropped it).
func TestIssue1094_Backspace(t *testing.T) {
	home, inst, _, namedCap := arm1094(t)

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	home = model.(*Home)

	if len(namedCap.calls) != 1 {
		t.Fatalf("Backspace should produce 1 named-key call, got %d", len(namedCap.calls))
	}
	if namedCap.calls[0].key != "BSpace" {
		t.Errorf("Backspace forwarded as %q, want %q", namedCap.calls[0].key, "BSpace")
	}
	if namedCap.calls[0].sessionID != inst.ID {
		t.Errorf("Backspace routed to %q, want %q", namedCap.calls[0].sessionID, inst.ID)
	}
	if !home.insertMode {
		t.Error("Backspace should NOT exit insert mode")
	}
}

// TestIssue1094_ArrowKeys verifies Up/Down/Left/Right forward as the
// corresponding tmux named keys so users can navigate claude pickers.
func TestIssue1094_ArrowKeys(t *testing.T) {
	home, _, _, namedCap := arm1094(t)

	cases := []struct {
		keyType tea.KeyType
		want    string
	}{
		{tea.KeyUp, "Up"},
		{tea.KeyDown, "Down"},
		{tea.KeyLeft, "Left"},
		{tea.KeyRight, "Right"},
	}

	for _, tc := range cases {
		model, _ := home.Update(tea.KeyMsg{Type: tc.keyType})
		home = model.(*Home)
	}

	if len(namedCap.calls) != len(cases) {
		t.Fatalf("expected %d named-key calls, got %d", len(cases), len(namedCap.calls))
	}
	for i, tc := range cases {
		if namedCap.calls[i].key != tc.want {
			t.Errorf("call[%d].key = %q, want %q", i, namedCap.calls[i].key, tc.want)
		}
	}
}

// TestIssue1094_TabAndShiftTab verifies Tab → "Tab" and Shift+Tab → "BTab"
// so users can step through claude's tab-driven picker UIs.
func TestIssue1094_TabAndShiftTab(t *testing.T) {
	home, _, _, namedCap := arm1094(t)

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyTab})
	home = model.(*Home)
	model, _ = home.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	home = model.(*Home)

	if len(namedCap.calls) != 2 {
		t.Fatalf("expected 2 named-key calls (Tab+BTab), got %d", len(namedCap.calls))
	}
	if namedCap.calls[0].key != "Tab" {
		t.Errorf("first call = %q, want Tab", namedCap.calls[0].key)
	}
	if namedCap.calls[1].key != "BTab" {
		t.Errorf("second call = %q, want BTab", namedCap.calls[1].key)
	}
}

// TestIssue1094_CtrlCAndCtrlD verifies the two control keys explicitly
// allow-listed in the spec: C-c (break claude prompt) and C-d (EOF/exit).
func TestIssue1094_CtrlCAndCtrlD(t *testing.T) {
	home, _, _, namedCap := arm1094(t)

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	home = model.(*Home)
	model, _ = home.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	home = model.(*Home)

	if len(namedCap.calls) != 2 {
		t.Fatalf("expected 2 named-key calls (C-c + C-d), got %d", len(namedCap.calls))
	}
	if namedCap.calls[0].key != "C-c" {
		t.Errorf("Ctrl-C forwarded as %q, want %q", namedCap.calls[0].key, "C-c")
	}
	if namedCap.calls[1].key != "C-d" {
		t.Errorf("Ctrl-D forwarded as %q, want %q", namedCap.calls[1].key, "C-d")
	}
	// Ctrl-C in insert mode must NOT exit insert mode — that's Esc's job.
	// (Exiting on Ctrl-C would surprise users who want to break a claude
	// prompt without leaving insert mode.)
	if !home.insertMode {
		t.Error("Ctrl-C in insert mode should NOT exit insert mode")
	}
}

// TestIssue1094_RuneBatching_OneCallPerBurst is the headline latency test:
// rapid typing must coalesce into a single tmux send-keys call (instead of
// one fork+exec per keystroke). We arrange for batching to be active, fire
// the runes that make up "hello", deliver the insertFlushMsg the scheduled
// tea.Tick would have produced, then assert exactly one sink call with the
// full word.
func TestIssue1094_RuneBatching_OneCallPerBurst(t *testing.T) {
	home, _, runeCap, _ := arm1094(t)
	home.insertBatchDuration = 15 * time.Millisecond

	for _, r := range "hello" {
		model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		home = model.(*Home)
	}

	if len(runeCap.calls) != 0 {
		t.Fatalf("with batching enabled, no sink call should fire before flush; got %d", len(runeCap.calls))
	}
	if !home.insertFlushPending {
		t.Error("a flush should be pending after the first buffered rune")
	}

	// Deliver the flush message the way the tea runtime would.
	model, _ := home.Update(insertFlushMsg{})
	home = model.(*Home)

	if len(runeCap.calls) != 1 {
		t.Fatalf("batched flush should produce exactly 1 sink call, got %d", len(runeCap.calls))
	}
	if runeCap.calls[0].text != "hello" {
		t.Errorf("batched text = %q, want %q", runeCap.calls[0].text, "hello")
	}
	if home.insertFlushPending {
		t.Error("insertFlushPending should be cleared after flush")
	}
}

// TestIssue1094_NamedKeyFlushesBufferedRunes ensures keystroke ordering is
// preserved across the batching boundary: if the user types "hi" and then
// hits Backspace, the focused pane must see "hi" before BSpace — not
// the other way around because of late flushing.
func TestIssue1094_NamedKeyFlushesBufferedRunes(t *testing.T) {
	home, _, runeCap, namedCap := arm1094(t)
	home.insertBatchDuration = 15 * time.Millisecond

	for _, r := range "hi" {
		model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		home = model.(*Home)
	}
	if len(runeCap.calls) != 0 {
		t.Fatalf("runes should still be buffered before backspace; got %d", len(runeCap.calls))
	}

	// Backspace forces an immediate flush, then sends BSpace.
	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	home = model.(*Home)

	if len(runeCap.calls) != 1 {
		t.Fatalf("buffered runes should flush exactly once before backspace; got %d", len(runeCap.calls))
	}
	if runeCap.calls[0].text != "hi" {
		t.Errorf("flushed text = %q, want %q", runeCap.calls[0].text, "hi")
	}
	if len(namedCap.calls) != 1 || namedCap.calls[0].key != "BSpace" {
		t.Errorf("BSpace not forwarded after flush; got namedCap=%+v", namedCap.calls)
	}
	if home.insertFlushPending {
		t.Error("flush should have cleared the pending flag")
	}
}

// TestIssue1094_EnterFlushesBufferedRunes is the same ordering invariant
// for Enter — buffered runes must be sent before the Enter key reaches the
// pane, otherwise claude sees an empty submission followed by stray runes.
func TestIssue1094_EnterFlushesBufferedRunes(t *testing.T) {
	home, _, runeCap, _ := arm1094(t)
	home.insertBatchDuration = 15 * time.Millisecond

	for _, r := range "hi" {
		model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		home = model.(*Home)
	}

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyEnter})
	home = model.(*Home)

	if len(runeCap.calls) != 2 {
		t.Fatalf("expected flush (text='hi', sendEnter=false) + enter (text='', sendEnter=true); got %d calls: %+v", len(runeCap.calls), runeCap.calls)
	}
	if runeCap.calls[0].text != "hi" || runeCap.calls[0].sendEnter {
		t.Errorf("first call should be text=hi, sendEnter=false; got %+v", runeCap.calls[0])
	}
	if runeCap.calls[1].text != "" || !runeCap.calls[1].sendEnter {
		t.Errorf("second call should be text='', sendEnter=true; got %+v", runeCap.calls[1])
	}
}

// TestIssue1094_EscFlushesBufferedRunesAndExits ensures Esc also drains the
// buffer (so the user doesn't lose typed text by exiting insert mode) and
// then clears insertMode.
func TestIssue1094_EscFlushesBufferedRunesAndExits(t *testing.T) {
	home, _, runeCap, _ := arm1094(t)
	home.insertBatchDuration = 15 * time.Millisecond

	for _, r := range "ab" {
		model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		home = model.(*Home)
	}

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyEsc})
	home = model.(*Home)

	if len(runeCap.calls) != 1 || runeCap.calls[0].text != "ab" {
		t.Errorf("Esc should flush 'ab' before exiting; got %+v", runeCap.calls)
	}
	if home.insertMode {
		t.Error("Esc should exit insert mode")
	}
}

// TestIssue1094_ScheduleInsertFlushIdempotent verifies that piling up runes
// while a flush is already scheduled does NOT schedule extra ticks. This
// protects against runaway tea.Cmd queues during fast typing.
func TestIssue1094_ScheduleInsertFlushIdempotent(t *testing.T) {
	home, _, _, _ := arm1094(t)
	home.insertBatchDuration = 50 * time.Millisecond

	model, firstCmd := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	home = model.(*Home)
	if firstCmd == nil {
		t.Fatal("first buffered rune should schedule a flush tick")
	}
	if !home.insertFlushPending {
		t.Error("first rune should set insertFlushPending")
	}

	model, secondCmd := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	home = model.(*Home)
	if secondCmd != nil {
		t.Error("second rune should NOT schedule a duplicate tick while a flush is already pending")
	}
}

// TestIssue1094_SyncModeOneCallPerRune verifies that insertBatchDuration<=0
// reverts to the legacy 1-call-per-rune semantics tests rely on. (Existing
// #1069 tests depend on this — armHomeWithOneSession sets it explicitly.)
func TestIssue1094_SyncModeOneCallPerRune(t *testing.T) {
	home, _, runeCap, _ := arm1094(t)
	if home.insertBatchDuration > 0 {
		t.Fatalf("test setup expected sync batching; got %v", home.insertBatchDuration)
	}

	for _, r := range "xyz" {
		model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		home = model.(*Home)
	}

	if len(runeCap.calls) != 3 {
		t.Fatalf("sync mode should produce 1 call per rune; got %d", len(runeCap.calls))
	}
	for i, want := range []string{"x", "y", "z"} {
		if runeCap.calls[i].text != want {
			t.Errorf("call[%d].text = %q, want %q", i, runeCap.calls[i].text, want)
		}
	}
}

// TestIssue1094_NewHomeDefaultsToBatching documents the production default —
// new Home instances batch at defaultInsertBatchDuration so users get the
// latency fix without opt-in.
func TestIssue1094_NewHomeDefaultsToBatching(t *testing.T) {
	h := NewHome()
	if h.insertBatchDuration != defaultInsertBatchDuration {
		t.Errorf("NewHome().insertBatchDuration = %v, want %v", h.insertBatchDuration, defaultInsertBatchDuration)
	}
}
