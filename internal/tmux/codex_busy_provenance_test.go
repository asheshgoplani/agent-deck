package tmux

import "testing"

// Codex leaves earlier assistant output visible above its live composer. An
// interrupt phrase in that history is prose, not proof that the current frame
// is busy. Both the prompt detector and Session status path must apply the same
// provenance rule so they cannot disagree about the frame.
func TestCodexQuotedInterruptProseDoesNotSuppressPrompt(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "assistant example followed by bare prompt",
			content: "Assistant result: Working (12s · esc to interrupt) was only an example.\n\n›",
		},
		{
			name: "quoted prose with parenthesis followed by composer",
			content: "• The phrase esc to interrupt (shown in docs) is only an example.\n" +
				"› Ask Codex to do anything\n" +
				"  gpt-5.6 · /project · Context 99% left",
		},
		{
			name: "quoted exact phrase in final parentheses",
			content: "• The docs describe cancelling (esc to interrupt)\n" +
				"› Ask Codex to do anything",
		},
		{
			name: "prose with ing word and exact phrase",
			content: "Something in the docs says (esc to interrupt)\n" +
				"›",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewPromptDetector("codex").HasPrompt(tt.content); !got {
				t.Error("PromptDetector.HasPrompt() = false, want true for the live Codex prompt")
			}

			session := &Session{Command: "codex"}
			if got := session.hasBusyIndicator(tt.content); got {
				t.Error("Session.hasBusyIndicator() = true, want false for quoted interrupt prose")
			}
		})
	}
}

func TestCodexLiveInterruptStatusRemainsBusy(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "current working status",
			content: "• Working (3s • esc to interrupt)\n› Ask Codex to do anything",
		},
		{
			name: "current status with inline background terminal message",
			content: "Working (3s • esc to interrupt) · 1 background terminal running · /ps to view · /stop to close\n" +
				"› Ask Codex to do anything",
		},
		{
			name:    "current status with minute duration",
			content: "Reading the terminal output (3m 05s • esc to interrupt)\n› previous suggestion",
		},
		{
			name:    "current status with hour duration",
			content: "Working (1h 02m 03s • esc to interrupt)\n› previous suggestion",
		},
		{
			name:    "older exact interrupt line",
			content: "Working on task\nesc to interrupt\n› previous suggestion",
		},
		{
			name:    "tool execution interrupt line",
			content: "• Running curl https://example.invalid\n  press esc to interrupt\n› previous suggestion",
		},
		{
			name:    "ctrl-c status suffix",
			content: "• Thinking (3s · ctrl+c to interrupt)\n› previous suggestion",
		},
		{
			name:    "spinner-led legacy status suffix",
			content: "⠋ Working (esc to interrupt)\n› previous suggestion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewPromptDetector("codex").HasPrompt(tt.content); got {
				t.Error("PromptDetector.HasPrompt() = true, want false for live Codex work")
			}

			session := &Session{Command: "codex"}
			if got := session.hasBusyIndicator(tt.content); !got {
				t.Error("Session.hasBusyIndicator() = false, want true for live Codex work")
			}
		})
	}
}

func TestNonCodexInterruptStatusBehaviorUnchanged(t *testing.T) {
	content := "────────────────────────────────────────\n" +
		"❯\n" +
		"────────────────────────────────────────\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt"
	session := &Session{Command: "claude"}
	if got := session.hasBusyIndicator(content); !got {
		t.Error("Claude interrupt status stopped matching after Codex-specific provenance change")
	}
}

func BenchmarkCodexBusyProvenance(b *testing.B) {
	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "quoted prose",
			content: "Assistant result: Working (12s · esc to interrupt) was only an example.\n\n›",
		},
		{
			name:    "live status",
			content: "• Working (3s • esc to interrupt)\n› Ask Codex to do anything",
		},
	}

	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			session := &Session{Command: "codex"}
			b.ReportAllocs()
			for b.Loop() {
				_ = session.hasBusyIndicator(tt.content)
			}
		})
	}
}
