package tmux

import (
	"strings"
	"testing"
)

// Issue #1577: the `codex` preset's busy/prompt patterns never matched the
// codewhale (deepseek-v4-pro) TUI, so a conductor-driven codewhale worker was
// always misread as idle and `session send --wait` timed out. These tests lock
// the reporter-captured footers to the new `codewhale` preset.

// Live-captured pane footers from the issue.
const (
	codewhaleBusyWaiting = "yolo · deepseek-v4-pro · tool checklist_write · 0 active · 1 done · 106s · Alt+V  (waiting for deepseek deepseek-v4-pro, 2s/300s idle timeout)"
	codewhaleBusyWorking = "yolo · deepseek-v4-pro · ... · working (2s)"
	codewhaleIdleFooter  = "yolo · deepseek-v4-pro · 1 done · Alt+V"
	codewhalePlaceholder = "Write a task or use /."
)

func anyBusyStringMatches(raw *RawPatterns, text string) bool {
	res, err := CompilePatterns(raw)
	if err != nil {
		return false
	}
	for _, s := range res.BusyStrings {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

func anyPromptStringMatches(raw *RawPatterns, text string) bool {
	res, err := CompilePatterns(raw)
	if err != nil {
		return false
	}
	for _, s := range res.PromptStrings {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

func TestCodewhalePresetExists(t *testing.T) {
	raw := DefaultRawPatterns("codewhale")
	if raw == nil {
		t.Fatal("expected a non-nil codewhale preset")
	}
	if len(raw.BusyPatterns) == 0 {
		t.Error("codewhale preset should define busy patterns")
	}
	if len(raw.PromptPatterns) == 0 {
		t.Error("codewhale preset should define prompt patterns")
	}
}

func TestCodewhalePresetMatchesCapturedFooters(t *testing.T) {
	raw := DefaultRawPatterns("codewhale")

	if !anyBusyStringMatches(raw, codewhaleBusyWaiting) {
		t.Errorf("codewhale busy patterns must match the 'waiting for deepseek' footer:\n%s", codewhaleBusyWaiting)
	}
	if !anyBusyStringMatches(raw, codewhaleBusyWorking) {
		t.Errorf("codewhale busy patterns must match the 'working (' footer:\n%s", codewhaleBusyWorking)
	}
	if anyBusyStringMatches(raw, codewhaleIdleFooter) {
		t.Errorf("codewhale busy patterns must NOT match the idle footer:\n%s", codewhaleIdleFooter)
	}
	if !anyPromptStringMatches(raw, codewhalePlaceholder) {
		t.Errorf("codewhale prompt patterns must match the composer placeholder:\n%s", codewhalePlaceholder)
	}
}

// TestCodexPresetDoesNotMatchCodewhaleFooters reproduces the pre-fix broken
// state: the codex preset a codewhale tool inherited by default never matched
// the real deepseek footers, so readiness was never detected.
func TestCodexPresetDoesNotMatchCodewhaleFooters(t *testing.T) {
	codex := DefaultRawPatterns("codex")
	if anyBusyStringMatches(codex, codewhaleBusyWaiting) || anyBusyStringMatches(codex, codewhaleBusyWorking) {
		t.Error("codex preset unexpectedly matched a codewhale busy footer — the #1577 repro no longer holds")
	}
}
