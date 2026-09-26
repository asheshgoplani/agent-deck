package tmux

import (
	"fmt"
	"strings"
	"testing"
)

// Status-detection audit, review round 2 (2026-09-23).

const codexComposerTail = "\n› Ask Codex to do anything\n\n" +
	"  gpt-6-sol · /private/tmp/x · Context 78% left · Context 22% used · weekly 92% left · 258K window\n"

// P1-1: Codex prints its agent messages and "• Ran <cmd>" headers at column 0
// with "• ", so the "(9m 41s • esc to interrupt)" shape can sit in transcript
// text within the last 25 lines of an idle pane. Only the live slot counts:
// the last • block before the › composer, with nothing but blank and
// "  └ …" lines between.
func TestCodexLiveStatusLine_AnchoredToComposer(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		want  FrameVerdict
	}{
		{"working above composer", "    x := 1\n\n• Working (9m 41s • esc to interrupt)\n\n" + codexComposerTail, FrameActive},
		{"working with bg terminal", "• Ran make\n  └ ok\n\n• Working (37m 57s • esc to interrupt) · 1 background terminal running · /ps to view\n\n" + codexComposerTail, FrameActive},
		{"waiting for bg terminal with └ line", "• Waiting for background terminal (1h 06m 33s • esc to interrupt) · 1 background terminal running\n  └ TAIL=4000 ./run.sh -count=1\n\n" + codexComposerTail, FrameActive},
		{"working above composer with draft", "• Working (12s • esc to interrupt)\n\n› fix the flaky test\n\n  gpt-6-sol · Context 78% left\n", FrameActive},
		{"agent message quotes the status shape", "• While a turn runs Codex draws (9m 41s • esc to interrupt) above the composer; the\n  detector keys on that shape. Nothing is running now.\n\n─ Worked for 12s ──────\n" + codexComposerTail, FrameWaiting},
		{"ran grep for the status shape", "• Ran grep -rnF '(9m 41s • esc to interrupt)' internal/tmux/testdata | head -3\n  └ internal/tmux/testdata/x.txt:98:• Working (9m 41s • esc to interrupt)\n\n• Found it in the corpus fixtures; done.\n\n─ Worked for 8s ──────\n" + codexComposerTail, FrameWaiting},
		{"status-shaped • line directly above the composer but in an agent paragraph", "• Summary: the line reads (9m 41s • esc to interrupt) while busy\n  and nothing else.\n" + codexComposerTail, FrameWaiting},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyPaneFrame("codex", c.frame); got != c.want {
				t.Fatalf("ClassifyPaneFrame = %s, want %s\n%s", got, c.want, c.frame)
			}
		})
	}
}

// P2-4: pi on an OpenAI provider prints no R<n>/CH fields after ↓.
func TestPiPromptStatusLineWithoutCacheField(t *testing.T) {
	rule := strings.Repeat("─", 60)
	for _, status := range []string{
		"↑2.2k ↓198 $0.017 (sub) 0.5%/272k (auto)                gpt-5.5 • thinking off",
		"↑5.9k ↓77 R5.5k CH96.6% $0.034 (sub) 2.1%/272k (auto)     (openai-codex) gpt-5.6-sol • low",
	} {
		frame := " xxx xxxx xxx xxxx\n\n" + rule + "\n\n" + rule + "\n~ • work\n" + status + "\n"
		if got := ClassifyPaneFrame("pi", frame); got != FrameWaiting {
			t.Errorf("pi idle composer with status %q = %s, want waiting", status, got)
		}
	}
}

// P1-2: the roster rows Claude draws under its footer (one per sub-agent)
// push the "Waiting for N background agents to finish" completion line out of
// the 20-line background-work window. That line is what keeps a session that
// handed its turn to background agents green, and the sessions that show it
// are exactly the ones with a long agent roster.
func claudeAwaitedAgentsAboveRoster(rows int) string {
	frame := "⏺ Launched the reviewers. Ending my turn until they report.\n" +
		"\n" +
		"✻ Waiting for 3 background agents to finish\n" +
		"\n" +
		"──────────────────────────────────────────────────────────── work ─\n" +
		"❯ \n" +
		"────────────────────────────────────────────────────────────────────\n" +
		"  [p] u@host:/x | [Fable 5.1] ctx:41% in:408.7k out:1.1k\n" +
		"  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents\n" +
		"  ⏺ main\n"
	for i := 0; i < rows; i++ {
		frame += fmt.Sprintf("  ◯ general-purpose  Task %d: review slice %d\n", i+1, i+1)
	}
	return frame
}

func TestClaudeAwaitedAgentsAboveRosterIsActive(t *testing.T) {
	for _, rows := range []int{3, 16, 30} {
		if got := ClassifyPaneFrame("claude", claudeAwaitedAgentsAboveRoster(rows)); got != FrameActive {
			t.Errorf("%d roster rows: ClassifyPaneFrame = %s, want active (turn awaits background agents)", rows, got)
		}
	}
}
