package tmux

import "testing"

func TestCommandCodeConfiguredState(t *testing.T) {
	raw := &RawPatterns{BusyPatterns: []string{`re:(?m)^[^\r\n]*…[ \t]+esc to interrupt[ \t]+•[^\r\n]*$`}, PromptPatterns: []string{"❯", "Type something..."}}
	patterns, err := CompilePatterns(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"☆ Razzmatazzing", "⌘ Pondering", "✻ Constructing"} {
		s := NewSession("test", t.TempDir())
		s.SetPatterns(patterns)
		content := word + "…  esc to interrupt • 54s • ↓ 70.0k\n────\n❯ Ask your question...\n────\npermission bypass on\n? for shortcuts\n\n\n"
		if !s.hasBusyIndicator(content) {
			t.Errorf("busy not detected: %s", word)
		}
	}
	for _, content := range []string{"❯ Ask your question...", "5. Type something..."} {
		s := NewSession("test", t.TempDir())
		s.SetPatterns(patterns)
		if s.hasBusyIndicator(content) {
			t.Errorf("idle detected as busy: %s", content)
		}
		if !s.hasPromptIndicator(content) {
			t.Errorf("prompt not detected: %s", content)
		}
	}
}
