package main

import (
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/send"
)

// ghostKeySink is a keySink over a scripted pane: captures come back in
// order (the last repeats) and every key is recorded.
type ghostKeySink struct {
	recordingSink
	captures []string
	calls    int
}

func (g *ghostKeySink) CapturePaneFresh() (string, error) {
	g.calls++
	return g.captures[min(g.calls, len(g.captures))-1], nil
}

func noEscapeSettle(t *testing.T) {
	t.Helper()
	prev := suggestionEscapeSettle
	suggestionEscapeSettle = 0
	t.Cleanup(func() { suggestionEscapeSettle = prev })
}

// `session send-keys --enter` and `--named-key Tab|Right` at a Claude prompt
// suggestion: Escape is pressed once and the key is withheld, because
// Escape does not clear the suggestion on Claude Code 2.1.295.
func TestGuardBareKey_EscapeThenWithholdAtSuggestion(t *testing.T) {
	noEscapeSettle(t)
	for name, row := range ghostRows {
		for _, key := range []string{"Enter", "Tab", "Right"} {
			sink := &ghostKeySink{captures: []string{ghostPane(row)}}
			err := guardBareKey(sink, "claude", key)
			if !errors.Is(err, send.ErrSuggestionShown) {
				t.Errorf("%s/%s: want ErrSuggestionShown, got %v", name, key, err)
			}
			if len(sink.named) != 1 || sink.named[0] != "Escape" || sink.enters != 0 || len(sink.texts) != 0 {
				t.Errorf("%s/%s: want exactly one Escape and nothing else, got named=%v enters=%d texts=%v",
					name, key, sink.named, sink.enters, sink.texts)
			}
		}
	}
}

// No suggestion on screen: no Escape, the key is allowed. A composer holding
// text the caller typed earlier (real text first) is not a suggestion.
func TestGuardBareKey_NoSuggestionNoEscape(t *testing.T) {
	noEscapeSettle(t)
	for name, composer := range map[string]string{
		"empty composer": "❯ ",
		"typed text":     "❯ deploy the staging build",
	} {
		sink := &ghostKeySink{captures: []string{ghostPane(composer)}}
		if err := guardBareKey(sink, "claude", "Enter"); err != nil {
			t.Errorf("%s: unexpected refusal: %v", name, err)
		}
		if len(sink.named) != 0 {
			t.Errorf("%s: Escape sent with no suggestion shown: %v", name, sink.named)
		}
	}
}

// Keys that do not act on a suggestion, and non-Claude tools, are never
// guarded and never read the pane.
func TestGuardBareKey_OnlyClaudeAcceptKeys(t *testing.T) {
	noEscapeSettle(t)
	cases := []struct{ tool, key string }{
		{"claude", "BSpace"},
		{"claude", "C-c"},
		{"claude", "Up"},
		{"codex", "Enter"},
		{"shell", "Tab"},
	}
	for _, tc := range cases {
		sink := &ghostKeySink{captures: []string{ghostPane(ghostRows["focused"])}}
		if err := guardBareKey(sink, tc.tool, tc.key); err != nil {
			t.Errorf("%s/%s: unexpected refusal: %v", tc.tool, tc.key, err)
		}
		if sink.calls != 0 || len(sink.named) != 0 {
			t.Errorf("%s/%s: guard touched the pane (captures=%d named=%v)", tc.tool, tc.key, sink.calls, sink.named)
		}
	}
}
