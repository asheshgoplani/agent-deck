package web

import (
	"regexp"
	"strings"
	"testing"
)

// TestTerminalLinkHandler_WiredIntoTerminal_RegressionFor1682 pins the one
// line that makes `[web].trusted_domains` reachable at all.
//
// The trusted-domain policy lives in static/app/terminalLinks.js and is
// covered behaviorally by tests/web/unit/terminalLinks.test.js (matching
// rules) and tests/web/e2e/trusted-domains.spec.js (allowlisted vs not, in a
// real browser against a real server). Neither can observe whether xterm is
// actually handed the handler: an OSC-8 link only exists once a live tmux
// pane emits one, and xterm exposes no back-reference from its DOM element to
// the Terminal instance.
//
// So this is a source pin, in the same spirit as
// TestWebBundle_ChartNotInInitialPayload_RegressionFor1022. Drop the
// `linkHandler` option from TerminalPanel.js and every link silently falls
// back to xterm's built-in "could potentially be dangerous" confirm — the
// exact regression issue #1682 asks us to prevent.
func TestTerminalLinkHandler_WiredIntoTerminal_RegressionFor1682(t *testing.T) {
	panel := readEmbedded(t, "static/app/TerminalPanel.js")

	if !strings.Contains(panel, `from './terminalLinks.js'`) {
		t.Error("TerminalPanel.js does not import ./terminalLinks.js")
	}
	wired := regexp.MustCompile(`linkHandler\s*:\s*createTerminalLinkHandler\(\)`)
	if !wired.MatchString(panel) {
		t.Error("TerminalPanel.js does not pass linkHandler: createTerminalLinkHandler() to new Terminal(...) — " +
			"without it xterm's built-in confirm fires on every link and [web].trusted_domains has no effect")
	}

	// The policy module must keep reading the hydrated signals rather than a
	// snapshot captured at terminal-construction time.
	links := readEmbedded(t, "static/app/terminalLinks.js")
	for _, want := range []string{"trustedDomainsSignal", "confirmLinkOpenSignal"} {
		if !strings.Contains(links, want) {
			t.Errorf("terminalLinks.js no longer references %s — link policy would ignore /api/settings", want)
		}
	}
}
