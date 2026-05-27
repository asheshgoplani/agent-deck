package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Issue #1131 (by @ddorman-dn): "direct type STILL laggy" on latest, local
// AND remote. Prior perf work (#1102/#1110/#1112) made the keystroke SEND
// path fast (persistent tmux -C / SSH stream, sub-ms per key) and an
// end-to-end tmux benchmark (internal/tmux) confirms the tmux send→render
// layer is ~3ms. So the send path was NOT the bottleneck.
//
// The real bottleneck is the FEEDBACK path: in insert mode the user watches
// agent-deck's preview pane (they are NOT attached to tmux). That preview is
// refreshed only by the 2s background tickMsg, gated by a 2s previewCacheTTL
// — and nothing invalidated it after a keystroke. So a typed character could
// take up to ~2 seconds to echo back into the preview. That is the lag.
//
// The fix: after any insert keystroke, schedule a fast preview refresh that
// bypasses the TTL, so the echo loop is ~60ms instead of ~2000ms.

// TestIssue1131_InsertKeystrokeSchedulesFastPreviewRefresh proves the wiring:
// pressing a rune in insert mode arms a preview refresh, so the user does not
// have to wait for the 2s background tick to see their echo.
func TestIssue1131_InsertKeystrokeSchedulesFastPreviewRefresh(t *testing.T) {
	home, _ := armInsertModeWithFakeKeySender(t)

	if home.insertPreviewRefreshPending {
		t.Fatal("refresh should not be pending before any keystroke")
	}

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	home = model.(*Home)

	if !home.insertPreviewRefreshPending {
		t.Error("typing a rune in insert mode must schedule a fast preview refresh (#1131)")
	}
}

// TestIssue1131_PreviewRefreshDelayBeatsBackgroundTick guards the headline
// number: the insert-mode echo delay must be dramatically smaller than the
// 2s background tick that was previously the only refresh trigger. If a
// future change bumps it back toward tickInterval, perceived lag returns.
func TestIssue1131_PreviewRefreshDelayBeatsBackgroundTick(t *testing.T) {
	if insertPreviewEchoDelay >= tickInterval {
		t.Errorf("insertPreviewEchoDelay=%v must be << tickInterval=%v", insertPreviewEchoDelay, tickInterval)
	}
	// Echo must feel instant: well under the ~100ms human-perceptible bar.
	if insertPreviewEchoDelay > 100*time.Millisecond {
		t.Errorf("insertPreviewEchoDelay=%v exceeds 100ms — echo will feel laggy", insertPreviewEchoDelay)
	}
}

// TestIssue1131_RefreshHandlerBypassesTTL is the core regression fence. The
// old code only refreshed the preview when previewCacheTTL (2s) had elapsed.
// The insert-mode refresh MUST fetch even when the cache is fresh (<2s old),
// otherwise the keystroke echo is still throttled to the 2s cadence.
func TestIssue1131_RefreshHandlerBypassesTTL(t *testing.T) {
	home, _ := armInsertModeWithFakeKeySender(t)

	// Resolve the insert target's preview key and mark its cache FRESH — as
	// if it were captured microseconds ago. Under the old TTL gate this would
	// suppress any refetch for ~2s.
	_, key, _ := home.selectedPreviewTarget()
	if key == "" {
		t.Fatal("test setup: no selected preview target")
	}
	home.previewCacheMu.Lock()
	home.previewCacheTime[key] = time.Now()
	home.previewFetchingID = ""
	home.previewCacheMu.Unlock()

	home.insertPreviewRefreshPending = true // as set by the scheduler
	model, cmd := home.Update(insertPreviewRefreshMsg{})
	home = model.(*Home)

	if home.insertPreviewRefreshPending {
		t.Error("handler should clear the pending flag")
	}
	if cmd == nil {
		t.Fatal("refresh must fetch the preview even with a fresh cache (TTL bypass) — got nil cmd")
	}
	home.previewCacheMu.RLock()
	fetching := home.previewFetchingID
	home.previewCacheMu.RUnlock()
	if fetching != key {
		t.Errorf("previewFetchingID = %q, want %q (refresh did not target the insert session)", fetching, key)
	}
}

// TestIssue1131_RefreshNoOpAfterExit verifies the refresh msg is harmless once
// the user has left insert mode (a tick scheduled just before Esc must not
// kick off a stray fetch).
func TestIssue1131_RefreshNoOpAfterExit(t *testing.T) {
	home, _ := armInsertModeWithFakeKeySender(t)
	home.insertPreviewRefreshPending = true

	model, _ := home.Update(tea.KeyMsg{Type: tea.KeyEsc})
	home = model.(*Home)
	if home.insertMode {
		t.Fatal("Esc should exit insert mode")
	}

	model, cmd := home.Update(insertPreviewRefreshMsg{})
	home = model.(*Home)
	if cmd != nil {
		t.Error("insertPreviewRefreshMsg after exit must be a no-op")
	}
	if home.insertPreviewRefreshPending {
		t.Error("pending flag should be cleared even on the no-op path")
	}
}
