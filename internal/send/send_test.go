package send

import (
	"strings"
	"testing"
)

func TestHasUnsentPastedPrompt(t *testing.T) {
	if !HasUnsentPastedPrompt("❯ [Pasted text #1 +89 lines]") {
		t.Fatal("expected pasted prompt marker to be detected")
	}
	if HasUnsentPastedPrompt("normal terminal output") {
		t.Fatal("did not expect normal output to be detected as pasted prompt")
	}
	// Case-insensitive detection
	if !HasUnsentPastedPrompt("[PASTED TEXT #2 +10 lines]") {
		t.Fatal("expected case-insensitive pasted prompt detection")
	}
}

func TestHasUnsentComposerPrompt(t *testing.T) {
	content := "────────────────\n❯\u00a0Write one line: LAUNCH_OK\n[Opus 4.6] Context: 0%"
	if !HasUnsentComposerPrompt(content, "Write one line: LAUNCH_OK") {
		t.Fatal("expected unsent composer prompt to be detected")
	}
	if HasUnsentComposerPrompt(content, "Different text") {
		t.Fatal("did not expect mismatched composer text to be detected")
	}

	// Wrapped current composer lines only expose a prefix of the message.
	wrappedContent := "────────────────\n❯\u00a0Read these 3 files and produce a summary for DIAGTOKEN_123. Keep\n  under 80 lines and include one verdict line.\n────────────────\n[Opus 4.6] Context: 0%"
	wrappedMessage := "Read these 3 files and produce a summary for DIAGTOKEN_123. Keep under 80 lines and include one verdict line."
	if !HasUnsentComposerPrompt(wrappedContent, wrappedMessage) {
		t.Fatal("expected wrapped unsent composer prompt to be detected")
	}

	// Claude hint suggestions should not be treated as unsent input for a
	// different message.
	suggestion := "────────────────\n❯\u00a0Try \"write a test for <filepath>\"\n────────────────\n[Opus 4.6] Context: 0%"
	if HasUnsentComposerPrompt(suggestion, wrappedMessage) {
		t.Fatal("did not expect suggestion placeholder to be treated as unsent composer input")
	}
}

// TestHasUnsentComposerPrompt_ColdLaunchMultilineShortFirstLine covers the
// cold-first-launch send/enter race: on a fresh start the trailing Enter can be
// swallowed while the child's TUI is still mounting, so the initial message is
// typed into the composer but never submitted and the session sits in "waiting".
// The send-verify loop only recovers if it can detect the message is still in
// the composer. A multi-line message renders with only its first physical line
// visible (the composer parser truncates at the first blank line), which is the
// shape of every `-m` prompt once the appended completion sentinel — beginning
// "\n\n## Final step …" — is present. When that first line is short, detection
// must still fire. Synthetic pane + message; no tmux, no timing.
func TestHasUnsentComposerPrompt_ColdLaunchMultilineShortFirstLine(t *testing.T) {
	message := "Summarize\n\n## Final step — assert completion\n" +
		"When the task is fully done, print exactly this as the last line: DONE"
	composer := strings.Join([]string{
		"────────────────",
		"❯ Summarize",
		"",
		"## Final step — assert completion",
		"When the task is fully done, print exactly this as the last line: DONE",
		"────────────────",
		"[Opus 4.6] Context: 0%",
	}, "\n")
	if !HasUnsentComposerPrompt(composer, message) {
		t.Fatal("expected a still-in-composer multi-line message with a short first line to be detected as unsent")
	}

	// A composer holding an unrelated short first line must NOT match this
	// multi-line message — the first-line branch stays type-specific.
	other := strings.Join([]string{
		"────────────────",
		"❯ ls -la",
		"────────────────",
		"[Opus 4.6] Context: 0%",
	}, "\n")
	if HasUnsentComposerPrompt(other, message) {
		t.Fatal("did not expect an unrelated composer line to match a different multi-line message")
	}

	// Once submitted, the composer is empty again — must read as consumed even
	// though the multi-line first-line branch is now active.
	consumed := strings.Join([]string{
		"❯ Summarize",
		"✳ Tempering…",
		"────────────────",
		"❯",
		"────────────────",
		"[Opus 4.6] Context: 0%",
	}, "\n")
	if HasUnsentComposerPrompt(consumed, message) {
		t.Fatal("did not expect a submitted (empty-composer) pane to be treated as unsent")
	}
}

// TestHasUnsentComposerPrompt_SameFirstLineDifferentBody guards against the
// first-line recovery match submitting an unrelated draft. Two multi-line
// messages can open with an identical short first line while their bodies
// differ. When the composer holds a draft for one of them, a recovery check for
// the *other* must reject it — otherwise the send-verify loop would fire Enter
// and submit the wrong draft. Detection therefore requires corroborating
// message-specific content (a fragment of the tail past the first line) to be
// visible in the pane. Synthetic pane + messages; no tmux, no timing.
func TestHasUnsentComposerPrompt_SameFirstLineDifferentBody(t *testing.T) {
	present := "Run\n\nDeploy the billing service and report the run id.\n\n" +
		"## Final step\nprint DONE"
	unrelated := "Run\n\nRoll back the auth migration and confirm it.\n\n" +
		"## Final step\nprint DONE"

	// Composer holds the draft for `present`.
	composer := strings.Join([]string{
		"────────────────",
		"❯ Run",
		"",
		"Deploy the billing service and report the run id.",
		"",
		"## Final step",
		"print DONE",
		"────────────────",
		"[Opus 4.6] Context: 0%",
	}, "\n")

	if !HasUnsentComposerPrompt(composer, present) {
		t.Fatal("expected the message whose body is in the composer to be detected as unsent")
	}
	// The unrelated message shares the first line ("Run") but its body is not in
	// the pane — recovery must NOT treat this draft as the unrelated message.
	if HasUnsentComposerPrompt(composer, unrelated) {
		t.Fatal("did not expect a draft sharing only the first line to match an unrelated multi-line message")
	}
}

func TestHasUnsentComposerPrompt_SubmittedHistory(t *testing.T) {
	// Submitted messages can appear in history; only current composer should count.
	submitted := "❯ Write one line: LAUNCH_OK\n✳ Tempering…\n────────────────\n❯\n────────────────\n[Opus 4.6] Context: 0%"
	if HasUnsentComposerPrompt(submitted, "Write one line: LAUNCH_OK") {
		t.Fatal("did not expect submitted history line to be treated as unsent composer input")
	}
}

func TestCurrentComposerPrompt_UsesBottomComposerBlock(t *testing.T) {
	content := strings.Join([]string{
		"> quoted output line from earlier response",
		"Some other output",
		"────────────────",
		"❯\u00a0Read these 3 files and produce a summary for DIAGTOKEN_123. Keep",
		"  under 80 lines and include one verdict line.",
		"────────────────",
		"[Opus 4.6] Context: 0%",
	}, "\n")

	got, ok := CurrentComposerPrompt(content)
	if !ok {
		t.Fatal("expected current composer prompt to be found")
	}
	want := "Read these 3 files and produce a summary for DIAGTOKEN_123. Keep under 80 lines and include one verdict line."
	if got != want {
		t.Fatalf("unexpected composer prompt.\nwant: %q\ngot:  %q", want, got)
	}
}

func TestNormalizePromptText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", ""},
		{"whitespace only", "   \t  ", ""},
		{"NBSP replaced", "hello\u00a0world", "hello world"},
		{"multi whitespace collapsed", "hello   world  foo", "hello world foo"},
		{"leading/trailing trimmed", "  hello world  ", "hello world"},
		{"mixed NBSP and spaces", "\u00a0hello\u00a0\u00a0world\u00a0", "hello world"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizePromptText(tt.input)
			if got != tt.want {
				t.Errorf("NormalizePromptText(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsComposerDividerLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want bool
	}{
		{"long dash line", "────────────────", true},
		{"long hyphen line", "----------------", true},
		{"long bold dash line", "━━━━━━━━━━━━━━━━", true},
		{"short dash line (9)", "---------", false},
		{"exactly 10 dashes", "----------", true},
		{"empty string", "", false},
		{"whitespace only", "   ", false},
		{"mixed chars", "────abc────", false},
		{"with surrounding whitespace", "  ────────────────  ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsComposerDividerLine(tt.line)
			if got != tt.want {
				t.Errorf("IsComposerDividerLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestHasCurrentComposerPrompt(t *testing.T) {
	content := "────────────────\n❯ hello world\n────────────────\n[Opus 4.6]"
	if !HasCurrentComposerPrompt(content) {
		t.Fatal("expected HasCurrentComposerPrompt to return true")
	}
	if HasCurrentComposerPrompt("no prompt here at all") {
		t.Fatal("expected HasCurrentComposerPrompt to return false for no prompt content")
	}
}

func TestParsePromptFromComposerBlock(t *testing.T) {
	lines := []string{
		"❯ hello world",
	}
	got, ok := ParsePromptFromComposerBlock(lines)
	if !ok {
		t.Fatal("expected ParsePromptFromComposerBlock to find prompt")
	}
	if got != "hello world" {
		t.Fatalf("expected 'hello world', got %q", got)
	}

	// No marker present
	_, ok = ParsePromptFromComposerBlock([]string{"no marker here"})
	if ok {
		t.Fatal("expected ParsePromptFromComposerBlock to return false for no marker")
	}
}
