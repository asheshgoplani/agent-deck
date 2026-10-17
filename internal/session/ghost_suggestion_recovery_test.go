package session

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The conductor recovery turn on a Claude composer that shows a 2.1.295
// prompt suggestion as its placeholder renderer paints it for a focused
// composer (first character in inverse video, the rest dim, cursor parked at
// the start of the composer):
// the suggestion is an empty composer, so the recovery turn is typed over it
// and submitted exactly once, and the submitted line holds only our text.
func TestConductorRecoveryGhostSuggestionIsEmptyComposer(t *testing.T) {
	inst, capture := recoveryFixture(t, true)
	b, err := os.ReadFile(inst.Command)
	if err != nil {
		t.Fatal(err)
	}
	const ghost = "Run the release checklist and tag the build now"
	ghostPrompt := `❯ \033[7m` + ghost[:1] + `\033[27m\033[2m` + ghost[1:] + `\033[0m\r\033[2C`
	if err := os.WriteFile(inst.Command, []byte(strings.ReplaceAll(string(b), "❯ ", ghostPrompt)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := inst.Start(); err != nil {
		t.Fatal(err)
	}
	assertRecoveryTurns(t, capture, 1)
	got, _ := os.ReadFile(capture)
	if strings.Contains(string(got), ghost) {
		t.Fatalf("the prompt suggestion reached the agent as input: %q", got)
	}
}

// ghostFrameTarget replays one captured pane frame forever.
type ghostFrameTarget string

func (f ghostFrameTarget) CapturePaneFresh() (string, error) { return string(f), nil }

// The deterministic half of the test above: the exact frame tmux capture-pane
// -e returned for the fake composer, fed to the recovery turn's readiness wait
// and pre-input check without a pane, so no paint timing is involved. The
// suggestion is an empty composer, so the recovery turn may be typed over it.
func TestConductorRecoveryGhostSuggestionFrameIsSafePrompt(t *testing.T) {
	const frame = "❯ \x1b[7mR\x1b[0;2mun the release checklist and tag the build now\x1b[0m"
	if !conductorRecoveryPromptSafe("claude", frame) {
		t.Fatalf("a composer showing only a prompt suggestion must be a safe empty prompt: %q", frame)
	}
	if err := waitForConductorRecoveryPrompt(ghostFrameTarget(frame), "claude", 3*time.Second); err != nil {
		t.Fatalf("readiness wait refused a composer showing only a prompt suggestion: %v", err)
	}
}
