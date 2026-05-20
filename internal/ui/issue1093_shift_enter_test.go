package ui

import (
	"bytes"
	"io"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/terminal"
)

// Issue #1093 (by @ddorman-dn): Shift+Enter, shipped in #1077, did not actually
// open the focused session in a new iTerm window on darwin arm64 — it fell
// through to the plain-Enter handler and replaced the agent-deck screen.
//
// Root cause: Bubble Tea v1.3.10 has no native Shift+Enter parsing — its KeyMsg
// struct has no Shift modifier and its escape-sequence table contains no entry
// for shift+enter — so a `case "shift+enter":` switch arm was unreachable. The
// repo's own keyboard_compat layer (which IS where the Shift bit lives, on
// terminals using CSI u or xterm modifyOtherKeys) was discarding that bit when
// codepoint==13.
//
// These tests pin the fix at the four layers it touches:
//   1. ParseCSIu preserves Shift on Enter (returns a sentinel distinct from
//      plain KeyEnter).
//   2. ParseModifyOtherKeys preserves Shift on Enter.
//   3. csiuReader translates the on-the-wire bytes into a distinct byte
//      sequence (NOT plain '\r'), so Bubble Tea can decode it and the home
//      switch can see it.
//   4. Home.normalizeMainKey maps that byte sequence to canonical
//      "shift+enter", so the existing dispatch arm fires.
//
// Plain-Enter regression cases are pinned alongside so the fix can't silently
// break the in-pane attach path.

// TestIssue1093_ParseCSIu_PreservesShiftOnEnter pins the parser contract.
func TestIssue1093_ParseCSIu_PreservesShiftOnEnter(t *testing.T) {
	plain := ParseCSIu([]byte("\x1b[13u"))
	shifted := ParseCSIu([]byte("\x1b[13;2u"))

	if plain == nil || shifted == nil {
		t.Fatalf("ParseCSIu returned nil: plain=%v shifted=%v", plain, shifted)
	}
	if plain.Type != tea.KeyEnter {
		t.Fatalf("plain Enter should be KeyEnter, got %v", plain.Type)
	}
	// The fix point: shifted Enter must NOT be indistinguishable from plain
	// KeyEnter. The chosen representation is an implementation detail (PUA
	// rune via KeyRunes), but it must differ from plain KeyEnter so downstream
	// code can route it to the new-window launcher instead of the attach path.
	if shifted.Type == tea.KeyEnter && len(shifted.Runes) == 0 {
		t.Fatalf("Shift+Enter via CSI u was reduced to plain KeyEnter — Shift bit dropped (the #1093 regression)")
	}
}

// TestIssue1093_ParseModifyOtherKeys_PreservesShiftOnEnter mirrors the CSI u
// test for the xterm modifyOtherKeys path. tmux's `extended-keys on` (set on
// every session by internal/tmux/tmux.go ~1948) leaves the outer iTerm2 in
// modifyOtherKeys mode after the first attach — so this is the path the live
// bug report actually hit.
func TestIssue1093_ParseModifyOtherKeys_PreservesShiftOnEnter(t *testing.T) {
	plain := ParseModifyOtherKeys([]byte("\x1b[27;1;13~"))
	shifted := ParseModifyOtherKeys([]byte("\x1b[27;2;13~"))

	if plain == nil || shifted == nil {
		t.Fatalf("ParseModifyOtherKeys returned nil: plain=%v shifted=%v", plain, shifted)
	}
	if plain.Type != tea.KeyEnter {
		t.Fatalf("plain Enter via modifyOtherKeys should be KeyEnter, got %v", plain.Type)
	}
	if shifted.Type == tea.KeyEnter && len(shifted.Runes) == 0 {
		t.Fatalf("Shift+Enter via modifyOtherKeys was reduced to plain KeyEnter — Shift bit dropped (the #1093 regression)")
	}
}

// TestIssue1093_CSIuReader_ShiftEnterEmitsDistinctBytes verifies the reader
// layer translates Shift+Enter sequences into bytes Bubble Tea will NOT decode
// as plain Enter ('\r'). This is the layer the live TUI consumes via
// tea.WithInput(ui.NewCSIuReader(os.Stdin)) in cmd/agent-deck/main.go.
func TestIssue1093_CSIuReader_ShiftEnterEmitsDistinctBytes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"CSI u Shift+Enter", "\x1b[13;2u"},
		{"modifyOtherKeys Shift+Enter", "\x1b[27;2;13~"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewCSIuReader(bytes.NewReader([]byte(tc.input)))
			out, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(out) == "\r" || string(out) == "\n" {
				t.Fatalf("Shift+Enter %q translated to plain Enter (%q) — Bubble Tea will see KeyEnter and fire in-pane attach instead of the launcher",
					tc.input, string(out))
			}
			if len(out) == 0 {
				t.Fatalf("Shift+Enter %q translated to empty bytes — dropped", tc.input)
			}
		})
	}
}

// TestIssue1093_CSIuReader_PlainEnterStillEmitsCR pins the non-regression
// guarantee for plain Enter — every "attach" / "submit" path in the TUI
// depends on this.
func TestIssue1093_CSIuReader_PlainEnterStillEmitsCR(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"CSI u plain Enter", "\x1b[13u", "\r"},
		{"modifyOtherKeys plain Enter", "\x1b[27;1;13~", "\r"},
		{"raw CR (terminal default Enter)", "\r", "\r"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewCSIuReader(bytes.NewReader([]byte(tc.input)))
			out, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			if string(out) != tc.want {
				t.Errorf("plain Enter %q translated to %q, want %q", tc.input, string(out), tc.want)
			}
		})
	}
}

// TestIssue1093_NormalizeMainKey_MarkerMapsToShiftEnter is the final hop: the
// PUA rune that csiuReader emits for Shift+Enter must normalize to canonical
// "shift+enter" so the home.go switch arm fires. Without this mapping the
// rune would fall through unmatched and Shift+Enter would still silently
// do nothing.
func TestIssue1093_NormalizeMainKey_MarkerMapsToShiftEnter(t *testing.T) {
	home := NewHome()
	got := home.normalizeMainKey(string(shiftEnterMarker))
	if got != "shift+enter" {
		t.Fatalf("normalizeMainKey(shift+enter marker) = %q, want %q", got, "shift+enter")
	}
}

// TestIssue1093_HomeDispatch_ShiftEnterCallsLauncher exercises the full
// keypress → launcher path through the public Home.handleMainKey entrypoint.
// Synthetic KeyMsg matches what Bubble Tea produces from the UTF-8 bytes of
// our marker rune. The launcher itself is captured via an injected sink so
// the test does not spawn an actual iTerm2 window.
func TestIssue1093_HomeDispatch_ShiftEnterCallsLauncher(t *testing.T) {
	home, _, _ := armHomeWithOneSession(t)

	var called bool
	var capturedReq terminal.AttachRequest
	home.openInNewWindowSink = func(req terminal.AttachRequest) error {
		called = true
		capturedReq = req
		return nil
	}

	keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{shiftEnterMarker}}
	if got := home.normalizeMainKey(keyMsg.String()); got != "shift+enter" {
		t.Fatalf("precondition: normalizeMainKey of synthetic Shift+Enter KeyMsg = %q, want shift+enter", got)
	}

	_, _ = home.handleMainKey(keyMsg)

	if !called {
		t.Fatal("Shift+Enter dispatch did NOT call the new-window launcher — fell through to in-pane attach (this is the #1093 regression)")
	}
	if capturedReq.Name == "" {
		t.Errorf("launcher called with empty AttachRequest.Name (got %+v) — handler didn't pull the tmux session name", capturedReq)
	}
}
