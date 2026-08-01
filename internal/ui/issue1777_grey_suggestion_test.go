package ui

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Issue #1777: a Claude autosuggestion rendered in bright-black grey (SGR 90
// or 38;5;8) instead of dim defeats a dim-only check, so the watcher-dispatch
// guard would save it as an "operator draft" and restore it after delivery —
// stranding the suggestion in the composer as real, normal-coloured text that
// a later bare Enter could submit. Grey ghosts must pass through the guard
// exactly like dim ones: no Ctrl+C, no save, no restore.
func TestDeliverToConductorPaneGuarded_IgnoresGreySuggestion(t *testing.T) {
	div := strings.Repeat("─", 40)
	greyPane := "some prior output\n" + div +
		"\n\x1b[39m❯ \x1b[90mupgrade agent-deck on carrollton and archive the five error sessions\x1b[0m\n" +
		div + "\n  auto mode on\n"
	p := &guardedFakePane{
		draftPane:        greyPane,
		postSendCaptures: []string{emptyComposer()},
	}
	opts := testConductorGuardOpts()
	opts.Strip = tmux.StripANSI
	if err := deliverToConductorPaneGuarded(p, "[slack] u: hi", opts, 40, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ctrlCCalls != 0 || p.chunkedCalls != 0 {
		t.Fatalf("grey suggestion must not trigger save-clear-restore, got ctrlC=%d chunked=%d",
			p.ctrlCCalls, p.chunkedCalls)
	}
}
