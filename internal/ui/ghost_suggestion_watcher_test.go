package ui

import (
	"strings"
	"testing"
)

// A watcher dispatch whose text the next Claude prompt suggestion repeats
// (2.1.295 paints the suggestion with an inverse cursor cell and dim text)
// must not read as "our message stuck in the composer": the retry arm would
// press a bare Enter at the suggestion.
func TestDeliverToConductorPane_SuggestionEchoIsNotRetried(t *testing.T) {
	const msg = "Run the release checklist and tag the build now"
	div := strings.Repeat("─", 40)
	ghost := "some prior output\n" + div + "\n❯ \x1b[7mR\x1b[0;2mun the release checklist and tag the build now\x1b[0m\n" + div + "\n  auto mode on\n"
	p := &fakeConductorPane{captures: []string{ghost}, statuses: []string{"waiting"}}
	if err := deliverToConductorPaneTuned(p, msg, 5, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.enterCalls != 0 {
		t.Fatalf("bare Enter pressed %d time(s) at a prompt suggestion", p.enterCalls)
	}
}
