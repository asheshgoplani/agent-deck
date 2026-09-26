package tmux

import "testing"

const codexFrameFooter = "› Ask Codex to do anything\n\n  ? for shortcuts            100% context left"

func TestCodexInterruptCurrentFrame(t *testing.T) {
	tests := []struct {
		name    string
		content string
		busy    bool
	}{
		{"quoted prose", "• The phrase esc to interrupt was only an example.\n\n" + codexFrameFooter, false},
		{"quoted status example", "• The docs quote Working (12s • esc to interrupt) here.\n\n" + codexFrameFooter, false},
		{"working above composer", "• Working (0s • esc to interrupt)\n\n" + codexFrameFooter, true},
		{"ctrl-c above composer", "• Thinking (3s • ctrl + c to interrupt)\n\n" + codexFrameFooter, true},
		{"padded working row", "• Working (0s • esc to interrupt)   \n\n" + codexFrameFooter, true},
		{"working above queued inputs", "• Working (32m 32s • esc to interrupt)\n\n" +
			"• Queued follow-up inputs\n" +
			"  ↳ check the tests\n" +
			"    before sending\n" +
			"    shift + ← edit last queued message\n" + codexFrameFooter, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NewPromptDetector("codex").HasPrompt(tt.content); got == tt.busy {
				t.Errorf("HasPrompt = %v, want %v", got, !tt.busy)
			}
			session := &Session{Command: "codex"}
			if got := session.hasBusyIndicator(tt.content); got != tt.busy {
				t.Errorf("hasBusyIndicator = %v, want %v", got, tt.busy)
			}
		})
	}
}
