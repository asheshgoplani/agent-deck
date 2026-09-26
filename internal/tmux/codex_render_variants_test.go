package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexLiveRenderVariantsStayBusy(t *testing.T) {
	for _, name := range []string{
		"codex-busy-hollow-bullet",
		"codex-busy-reduced-motion",
		"codex-busy-truncated-hint",
		"codex-busy-remapped-ctrl-c",
	} {
		t.Run(name, func(t *testing.T) {
			frame, err := os.ReadFile(filepath.Join("testdata", "status_corpus", name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			pane := string(frame)
			if NewPromptDetector("codex").HasPrompt(pane) {
				t.Error("busy pane reported ready")
			}
			if !(&Session{Command: "codex"}).hasBusyIndicator(pane) {
				t.Error("busy indicator missed")
			}
			if got := ClassifyPaneFrame("codex", pane); got != FrameActive {
				t.Errorf("status = %s, want active", got)
			}
		})
	}
}
