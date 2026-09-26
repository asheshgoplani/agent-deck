package send

import "testing"

func TestCodexReadyPromptUsesCurrentFrame(t *testing.T) {
	tests := []struct {
		name  string
		pane  string
		ready bool
	}{
		{"quoted prose", "• The phrase esc to interrupt was only an example.\n\n› Ask Codex to do anything\n\n  ? for shortcuts            100% context left", true},
		{"quoted status example", "• The docs quote Working (12s • esc to interrupt) here.\n\n› Ask Codex to do anything\n\n  ? for shortcuts            100% context left", true},
		{"working above composer", "• Working (0s • esc to interrupt)\n\n› Ask Codex to do anything\n\n  ? for shortcuts            100% context left", false},
		{"ctrl-c above composer", "• Thinking (3s • ctrl + c to interrupt)\n\n› Ask Codex to do anything\n\n  ? for shortcuts            100% context left", false},
		{"working above queued inputs", "• Working (32m 32s • esc to interrupt)\n\n• Queued follow-up inputs\n  ↳ check the tests\n    before sending\n    shift + ← edit last queued message\n› Ask Codex to do anything\n\n  ? for shortcuts            100% context left", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := &mockReadyChecker{pane: tt.pane}
			if got := paneShowsReadyPrompt(target, "codex", PromptGates{CodexPrompt: true}); got != tt.ready {
				t.Errorf("paneShowsReadyPrompt = %v, want %v", got, tt.ready)
			}
		})
	}
}
