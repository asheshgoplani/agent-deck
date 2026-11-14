package assistant

import (
	"strings"
	"testing"
)

// Printed commands quote the profile, so a profile name with shell
// characters is copied as one word.
func TestPrintedCommandsQuoteProfile(t *testing.T) {
	rec := Record{Name: "bot", Assistant: "hermes", Scopes: DefaultScopes, Profile: "my work;rm -rf x"}
	raise := RaiseScopeCommand(rec, ScopeSend)
	if !strings.HasPrefix(raise, "agent-deck -p 'my work;rm -rf x' connect hermes") {
		t.Errorf("RaiseScopeCommand = %q, want the profile quoted", raise)
	}
	if got := deckCommand(rec.Profile); got != "agent-deck -p 'my work;rm -rf x'" {
		t.Errorf("deckCommand = %q, want the profile quoted", got)
	}
	// Plain names print unchanged.
	if got := deckCommand("work"); got != "agent-deck -p work" {
		t.Errorf("deckCommand(work) = %q", got)
	}
}
