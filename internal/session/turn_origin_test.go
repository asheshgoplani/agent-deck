package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTurnOriginForPrompt(t *testing.T) {
	for prompt, want := range map[string]string{
		"[ORDER watch-ci] its check changed.\nWas: a": "order:watch-ci",
		SendEnvelope("abc") + "\n[ORDER x] its check": "order:x",
		"[HEARTBEAT] [ops] Status: 1 waiting.":        TurnOriginHeartbeat,
		"please watch my PRs":                         "",
		"<channel source=\"discord\">hi</channel>":    "",
		"[ORDER Bad Name] x":                          "",
	} {
		if got := TurnOriginForPrompt(prompt); got != want {
			t.Errorf("%q: got %q want %q", prompt, got, want)
		}
	}
}

func TestTurnOrigin_RecordLoadClear(t *testing.T) {
	setupConductorTest(t)
	const id = "0a1b2c3d-1700000000"
	if err := RecordTurnOrigin(id, "[ORDER w] its check changed."); err != nil {
		t.Fatal(err)
	}
	if got := LoadTurnOrigin(id); got != "order:w" || !IsOrderTurnOrigin(got) {
		t.Fatalf("got %q", got)
	}
	if err := RecordTurnOrigin(id, "owner typed this"); err != nil {
		t.Fatal(err)
	}
	if got := LoadTurnOrigin(id); got != "" {
		t.Fatalf("an attended prompt must clear the origin, got %q", got)
	}
	_ = RecordTurnOrigin(id, "[HEARTBEAT] x")
	ClearTurnOrigin(id)
	if got := LoadTurnOrigin(id); got != "" {
		t.Fatalf("after Stop: %q", got)
	}
	if got := LoadTurnOrigin("../escape"); got != "" {
		t.Fatalf("path traversal id: %q", got)
	}
	if hookArtifactID(id+turnOriginSuffix) != id {
		t.Fatal("turn-origin files must be pruned with the instance's other hook artifacts")
	}
}

// Defect 10 of the proactive audit: HEARTBEAT_RULES.md can never fire on a
// schedule (the tick sends nothing unless a child is waiting or errored or
// the inbox has records). The shipped docs must say so and point at
// ORDERS.md, which is the scheduled path.
func TestHeartbeatRulesDocsPointAtStandingOrders(t *testing.T) {
	if !strings.Contains(conductorSharedClaudeMDTemplate, "## Standing Orders") ||
		!strings.Contains(conductorSharedClaudeMDTemplate, "a heartbeat only fires when a child is waiting or errored") ||
		!strings.Contains(conductorSharedClaudeMDTemplate, "orders add") ||
		!strings.Contains(conductorSharedClaudeMDTemplate, "NO_REPLY") {
		t.Fatal("shared conductor template must document standing orders and NO_REPLY")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "conductor", "HEARTBEAT_RULES.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ORDERS.md") {
		t.Fatal("conductor/HEARTBEAT_RULES.md must say time-based checks belong in ORDERS.md")
	}
}

// A conductor whose CLAUDE.md was generated before the Standing Orders
// section must still be recognised as generated (and migrated).
func TestStandingOrdersSectionIsAMigratableGeneration(t *testing.T) {
	gens := conductorInstructionsGenerations(conductorSharedClaudeMDTemplate)
	if len(gens) == 0 || gens[0] != preStandingOrdersConductorInstructionsTemplate(conductorSharedClaudeMDTemplate) ||
		strings.Contains(gens[0], "## Standing Orders") {
		t.Fatal("the generation before Standing Orders must be the newest predecessor")
	}
}
