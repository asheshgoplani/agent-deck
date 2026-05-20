package ui

import (
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Issue #1102 (by @ddorman-dn against v1.9.23): #1096's 15ms rune batching
// didn't actually fix the latency complaint, because at realistic typing
// speeds (>15ms between keystrokes) every keystroke still triggers a
// separate flush which pays the full tmux fork+exec cost. These tests cover
// the follow-up: every flush now goes through a persistent KeySender opened
// at enterInsertMode (one fork+exec for the whole insert session) and torn
// down at exitInsertMode.

// fakeInsertKeySender simulates the persistent client by tracking how many
// dispatches it received. SendKeys is intentionally costed at zero so the
// 100-keys-in-500ms budget reflects in-process work, not the latency of
// the dispatch sink itself.
type fakeInsertKeySender struct {
	sendCount     atomic.Int32
	namedKeyCount atomic.Int32
	enterCount    atomic.Int32
	closeCount    atomic.Int32
	lastText      string
}

func (f *fakeInsertKeySender) SendKeys(text string) error {
	f.sendCount.Add(1)
	f.lastText = text
	return nil
}
func (f *fakeInsertKeySender) SendNamedKey(string) error {
	f.namedKeyCount.Add(1)
	return nil
}
func (f *fakeInsertKeySender) SendEnter() error {
	f.enterCount.Add(1)
	return nil
}
func (f *fakeInsertKeySender) Close() error {
	f.closeCount.Add(1)
	return nil
}

// armInsertModeWithFakeKeySender returns a Home wired with one focused
// session whose insert-mode dispatch path is intercepted by a fake
// persistent sender. The TUI-level insertKeySink/insertNamedKeySink are
// deliberately NOT installed — this verifies the production dispatch
// precedence (sink → sender → fallback) routes to the sender as expected.
func armInsertModeWithFakeKeySender(t *testing.T) (*Home, *fakeInsertKeySender) {
	t.Helper()
	home, _, _ := armHomeWithOneSession(t)
	// Clear the legacy sinks installed by armHomeWithOneSession so the
	// dispatch path falls through to the persistent KeySender.
	home.insertKeySink = nil
	home.insertNamedKeySink = nil

	fake := &fakeInsertKeySender{}
	home.insertOpenKeySender = func(insertTargetRef) (insertKeySender, error) {
		return fake, nil
	}

	// Enter insert mode — this is the moment that opens the sender.
	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)
	if !home.insertMode {
		t.Fatal("test setup: failed to enter insert mode")
	}
	if home.insertKeySender == nil {
		t.Fatal("test setup: KeySender was not installed on enterInsertMode")
	}
	return home, fake
}

// TestIssue1102_PersistentKeySender_Opened verifies that pressing `I` opens
// a persistent KeySender and stores it on Home — proving the production
// dispatch path goes through the new persistent client instead of paying
// per-keystroke fork+exec via the legacy Session.SendKeys path.
func TestIssue1102_PersistentKeySender_Opened(t *testing.T) {
	home, fake := armInsertModeWithFakeKeySender(t)
	if home.insertKeySender == nil {
		t.Fatal("insert mode did not install a persistent KeySender")
	}
	if home.insertKeySender != fake {
		t.Errorf("insertKeySender = %v, want %v (the injected fake)", home.insertKeySender, fake)
	}
}

// TestIssue1102_PersistentKeySender_ClosedOnExit verifies the KeySender's
// Close is invoked when the user leaves insert mode — otherwise the tmux
// -C subprocess (and an SSH ControlMaster slot for remote sessions) leak
// for the lifetime of the TUI.
func TestIssue1102_PersistentKeySender_ClosedOnExit(t *testing.T) {
	home, fake := armInsertModeWithFakeKeySender(t)

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyEsc})
	home = model.(*Home)

	if home.insertMode {
		t.Error("Esc should exit insert mode")
	}
	if home.insertKeySender != nil {
		t.Error("Esc should clear the persistent KeySender reference")
	}
	if got := fake.closeCount.Load(); got != 1 {
		t.Errorf("Close() invoked %d times, want exactly 1", got)
	}
}

// TestIssue1102_PersistentKeySender_TypedRunesGoThroughIt verifies that runes
// typed in insert mode reach the persistent KeySender's SendKeys — proving
// the fast path is used in production (i.e., not silently bypassed by the
// legacy fork+exec fallback).
func TestIssue1102_PersistentKeySender_TypedRunesGoThroughIt(t *testing.T) {
	home, fake := armInsertModeWithFakeKeySender(t)
	home.insertBatchDuration = -1 // sync: 1 sink call per rune for deterministic counting

	for _, r := range "hello" {
		model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		home = model.(*Home)
	}

	if got := fake.sendCount.Load(); got != 5 {
		t.Errorf("SendKeys called %d times, want 5 (one per rune in sync mode)", got)
	}
}

// TestIssue1102_PersistentKeySender_NamedKeysGoThroughIt verifies that
// Backspace/arrows/Tab/Ctrl-C/Ctrl-D continue working in insert mode and
// route through the KeySender's SendNamedKey instead of the legacy
// Session.SendNamedKey fork+exec.
func TestIssue1102_PersistentKeySender_NamedKeysGoThroughIt(t *testing.T) {
	home, fake := armInsertModeWithFakeKeySender(t)

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	home = model.(*Home)

	if got := fake.namedKeyCount.Load(); got != 1 {
		t.Errorf("SendNamedKey called %d times, want 1 (Backspace)", got)
	}
}

// TestIssue1102_PersistentKeySender_EnterGoesThroughIt covers the third
// dispatch verb: Enter must reach the KeySender's SendEnter (which on the
// remote path becomes a separate `agent-deck session send-keys --enter`
// invocation and on the local path uses tmux's bracketed-paste-flush
// SendEnter helper).
func TestIssue1102_PersistentKeySender_EnterGoesThroughIt(t *testing.T) {
	home, fake := armInsertModeWithFakeKeySender(t)

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyEnter})
	home = model.(*Home)

	if got := fake.enterCount.Load(); got != 1 {
		t.Errorf("SendEnter called %d times, want 1", got)
	}
}

// TestIssue1102_InsertModePerf_100KeysUnder500ms is the headline regression
// test for the latency complaint. Pumping 100 individual keystrokes through
// the production insert-mode handler — with sync batching to force one
// dispatch per keystroke (the realistic-typing case where the 15ms batch
// window never coalesces) — must complete in under 500ms (5ms/keystroke
// budget). The OLD implementation would fork+exec tmux per keystroke and
// blow this budget on macOS under load; the NEW implementation goes through
// the persistent KeySender and stays under the budget by orders of
// magnitude.
func TestIssue1102_InsertModePerf_100KeysUnder500ms(t *testing.T) {
	home, fake := armInsertModeWithFakeKeySender(t)
	home.insertBatchDuration = -1 // sync mode: force one dispatch per keystroke

	const n = 100
	start := time.Now()
	for i := 0; i < n; i++ {
		model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune('a' + i%26)}})
		home = model.(*Home)
	}
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("100 keystrokes through insert mode took %v, want <500ms (#1102 perf budget)", elapsed)
	}
	if got := fake.sendCount.Load(); got != int32(n) {
		t.Errorf("expected exactly %d SendKeys calls in sync mode; got %d", n, got)
	}
	t.Logf("100 keystrokes via persistent KeySender path: %v (%.2fms/keystroke)",
		elapsed, float64(elapsed.Microseconds())/1000.0/float64(n))
}

// TestIssue1102_LegacyFallbackUsedWhenSenderUnavailable documents the
// degradation path: when OpenKeySender returns an error (e.g., tmux -C
// unsupported in some container), the dispatch falls back to the per-call
// Session.SendKeys path so the feature still works — just slower. This
// guards against a future regression where a broken sender opener would
// disable insert mode entirely.
func TestIssue1102_LegacyFallbackUsedWhenSenderUnavailable(t *testing.T) {
	home, inst, runeCap := armHomeWithOneSession(t)
	// Force the opener to fail — but the legacy fallback should keep the
	// dispatch alive via insertKeySink (the test's per-call sink), proving
	// no degradation in functionality.
	home.insertOpenKeySender = func(insertTargetRef) (insertKeySender, error) {
		return nil, errInsertNoRemoteConfig
	}
	// Reset opener so enterInsertMode error path fires — but in this case
	// errInsertNoRemoteConfig is a local-session bring-up failure, not a
	// remote one. Let's instead simulate "tmux not supported": return a
	// non-sentinel error to take the silent fallback branch.
	home.insertOpenKeySender = func(insertTargetRef) (insertKeySender, error) {
		// Returning errInsertNoTmuxSession would set error; returning a
		// generic error takes the fallback branch silently.
		return nil, &fakeOpenerErr{}
	}

	model, _ := home.handleMainKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'I'}})
	home = model.(*Home)
	if !home.insertMode {
		t.Fatal("insert mode should still arm even if persistent sender fails to open")
	}
	if home.insertKeySender != nil {
		t.Error("persistent sender should be nil when opener failed")
	}

	// Type one rune — should go through the legacy insertKeySink path
	// (which armHomeWithOneSession installed).
	model, _ = home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	_ = model
	_ = inst
	if len(runeCap.calls) != 1 {
		t.Errorf("legacy fallback dispatch produced %d calls, want 1", len(runeCap.calls))
	}
}

// fakeOpenerErr is a marker error used by the fallback test to take the
// non-sentinel error branch in enterInsertMode (where the UI silently
// degrades to per-call dispatch instead of surfacing the failure).
type fakeOpenerErr struct{}

func (*fakeOpenerErr) Error() string { return "tmux -C not supported in this environment" }
