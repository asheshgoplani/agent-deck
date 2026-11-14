package send

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ASK-95: while Claude holds input behind a running turn its composer shows
// "Press up to edit queued messages". That hint is Claude's, not an operator
// draft, so it must not refuse a further send to the busy session.

func queuedHintPane(composerLine string) string {
	div := strings.Repeat("─", 60)
	return "⏺ Running the lanes.\n\n✽ Thundering… (34s · ↓ 135 tokens)\n\n" +
		div + "\n" + composerLine + "\n" + div + "\n  ⏵⏵ bypass permissions on (shift+tab to cycle)\n"
}

func TestComposerDraftTreatsQueuedHintAsEmpty(t *testing.T) {
	for name, line := range map[string]string{
		"plain":         "❯ Press up to edit queued messages",
		"dim":           "❯ \x1b[2mPress up to edit queued messages\x1b[0m",
		"cursor on P":   "❯ \x1b[7mP\x1b[0mress up to edit queued messages",
		"nbsp padding":  "❯ Press up to edit queued messages ",
		"grey 256 tone": "\x1b[39m❯ \x1b[38;5;246mPress up to edit queued messages\x1b[39m",
	} {
		t.Run(name, func(t *testing.T) {
			draft, visible := ComposerDraft(queuedHintPane(line), tmux.StripANSI)
			if !visible || draft != "" {
				t.Fatalf("draft=%q visible=%v, want an empty visible composer", draft, visible)
			}
			if !ComposerShowsQueuedHint(queuedHintPane(line), tmux.StripANSI) {
				t.Fatal("queued hint not recognized")
			}
		})
	}
}

func TestComposerDraftKeepsRealDraftsNextToQueuedHint(t *testing.T) {
	for _, body := range []string{
		"Press up to edit queued messages and also fix the bug",
		"please press up",
		"edit queued messages",
	} {
		draft, visible := ComposerDraft(queuedHintPane("❯ "+body), tmux.StripANSI)
		if !visible || draft != body {
			t.Fatalf("draft=%q visible=%v, want the operator draft %q", draft, visible, body)
		}
		if ComposerShowsQueuedHint(queuedHintPane("❯ "+body), tmux.StripANSI) {
			t.Fatalf("draft %q read as the queued hint", body)
		}
	}
}

type fixedPane struct {
	raw      string
	captures int
}

func (p *fixedPane) CapturePaneFresh() (string, error) { p.captures++; return p.raw, nil }

func TestGuardComposerDraftLetsSendThroughQueuedHint(t *testing.T) {
	pane := &fixedPane{raw: queuedHintPane("❯ Press up to edit queued messages")}
	res := GuardComposerDraft(pane, ComposerGuardOptions{HoldWait: 2 * time.Second, PollInterval: 10 * time.Millisecond, Strip: tmux.StripANSI})
	if res.Refused {
		t.Fatalf("guard refused a busy composer showing only the queued hint (held %v)", res.Held)
	}
	if pane.captures != 1 {
		t.Fatalf("guard held for %d captures, want a go-ahead on the first", pane.captures)
	}
}

// Counting the hint as an empty composer must not open the bare-Enter
// nudges: our message already sits in Claude's queue behind it.
func TestBareEnterBlockedAtQueuedHint(t *testing.T) {
	a := EnterAttribution{Message: "second message while the turn runs"}
	if !a.BareEnterBlocked(Captured(queuedHintPane("❯ Press up to edit queued messages")), tmux.StripANSI) {
		t.Fatal("bare Enter allowed into a composer showing the queued hint")
	}
	if a.BareEnterBlocked(Captured(queuedHintPane("❯ ")), tmux.StripANSI) {
		t.Fatal("bare Enter blocked at a plain empty composer")
	}
}
