package main

import (
	"bytes"
	"testing"
	"time"
)

// `launch -m … --no-wait` runs the same pre-send probe as the waited launch:
// a shown Claude prompt suggestion turns on the typed text check (exercised
// by TestGuardedSendAtSuggestionUsesTypedTextCheck), so an Enter whose typed
// bytes were lost on a cold start never submits the suggestion.
func TestLaunchNoWaitPromptConfirmsTypedTextAtSuggestion(t *testing.T) {
	pane := &mockSendRetryTarget{panes: []string{ghostPane(ghostRows["focused"])}}
	if opts := launchPromptSendOptions(pane, "claude"); !opts.suggestionShownBeforeSend || !opts.composerPasteFreeBeforeSend {
		t.Fatalf("claude probe: suggestion=%v pasteFree=%v, want true true", opts.suggestionShownBeforeSend, opts.composerPasteFreeBeforeSend)
	}
	if opts := launchPromptSendOptions(pane, "codex"); opts.suggestionShownBeforeSend {
		t.Fatal("the Claude suggestion rule must not apply to a codex launch")
	}
}

// The launch prompt retry applies the Claude prompt-suggestion rule only to a
// Claude target. A Codex composer that mounts with its dim placeholder while
// the retry types must get its Enter, not a typed text check that waits for a
// Claude repaint and never presses.
func TestLaunchPromptRetryKeepsSuggestionRuleToClaude(t *testing.T) {
	codexPlaceholder := "\n› \x1b[2mAsk Codex to do anything\x1b[0m\n\n  gpt-5 · ~/repo\n"
	// maxWait 0: the poll reads the first capture (no composer yet) and the
	// retry reads the second (the placeholder).
	target := &enterRecordingTarget{mockSendRetryTarget: &mockSendRetryTarget{panes: []string{"Starting Codex…", codexPlaceholder}}}
	var warn bytes.Buffer
	verifyPromptConsumedAfterLaunchAttributed(target, "codex", "review the diff", false, 0, time.Millisecond, &warn)
	// A plain SendKeysAndEnter (counted by the mock) or a checked one that
	// passed; a withheld Enter is neither.
	if !target.entered && target.sendKeysCalls != 1 {
		t.Fatalf("codex retry never pressed Enter; warn=%q", warn.String())
	}
}
