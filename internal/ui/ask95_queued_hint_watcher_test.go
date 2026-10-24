package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// ASK-95: a busy conductor that already holds queued input shows Claude's
// "Press up to edit queued messages" hint in its composer. Watcher and alert
// dispatch must deliver through it (Claude queues the message) instead of
// failing "composer is occupied", and must not touch the composer first.
func TestDeliverToConductorPaneGuarded_DeliversThroughQueuedHint(t *testing.T) {
	div := strings.Repeat("─", 40)
	hintPane := "✽ Thundering… (34s · ↓ 135 tokens)\n" + div +
		"\n\x1b[39m❯ Press up to edit queued messages\n" + div + "\n  auto mode on\n"
	p := &guardedFakePane{
		draftPane:        hintPane,
		postSendCaptures: []string{hintPane},
	}
	opts := testConductorGuardOpts()
	opts.Strip = tmux.StripANSI
	if err := deliverToConductorPaneGuarded(p, "[slack] u: hi", opts, 40, 0); err != nil {
		t.Fatalf("busy conductor with queued input refused: %v", err)
	}
	if p.ctrlCCalls != 0 || p.chunkedCalls != 0 {
		t.Fatalf("composer touched before delivery: ctrlC=%d chunked=%d", p.ctrlCCalls, p.chunkedCalls)
	}
}
