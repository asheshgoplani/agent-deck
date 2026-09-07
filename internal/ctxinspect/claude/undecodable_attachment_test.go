package claude

import (
	"strings"
	"testing"
)

// recMCPInstructionsBroken is an mcp_instructions_delta whose payload does not
// fit the record shape: the discriminator reads, so the burst is witnessed, and
// the body does not, so nothing about the session's MCP servers was learnt.
const recMCPInstructionsBroken = `{"type":"attachment","sessionId":"s1","attachment":{"type":"mcp_instructions_delta",` +
	`"addedNames":"not-an-array","addedBlocks":["## telegram"]}}`

// The same rule applied to the one category that never asked the question:
// skills_honest_unknown_test.go. It is the harder case, because a category with
// rows on it looks established whatever it says.

// TestUndecodableAttachmentIsNotAnAbsence is the regression test for the
// absence-note lie.
//
// Witnessing the startup burst and reading one of its records are different
// facts, and only the second licenses "this session recorded no MCP instruction
// blocks". Saying it on the strength of the first turns a gap in agent-deck's
// read into a claim about the user's configuration — and the screen printed a
// summable 0 beside it.
func TestUndecodableAttachmentIsNotAnAbsence(t *testing.T) {
	f := newFixture(t, recSkillListing, recMCPInstructionsBroken, recAssistant)
	rep := inspect(t, f.request())

	cat, ok := rep.Category(CategoryMCP)
	if !ok {
		t.Fatal("want an MCP category: the burst was witnessed, so the category is not absent, only unread")
	}
	if !cat.Unobserved {
		t.Error("the MCP category claims its contents were established: its record was seen and could not be decoded")
	}
	if _, complete := cat.DisplayTotal(); complete {
		t.Error("the MCP category reports a complete total: an unread category's 0 prints as a measured zero")
	}
	if _, known := cat.DisplayItemCount(); known {
		t.Error("the MCP category reports an item count: '(0 items)' reads as 'you have no MCP servers'")
	}
	for _, note := range cat.Notes {
		if strings.Contains(note, "recorded no MCP") {
			t.Errorf("note claims an absence it cannot establish: %q", note)
		}
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("violations: %v", rep.Violations)
	}
}

// TestObservedEmptyCategoryStillReportsACertainZero is the other half of the
// rule. Weakening every empty category to "unknown" would be its own dishonesty:
// when the burst was read in full and carried no MCP block, the session really
// does have none, and that is a measurement worth printing.
func TestObservedEmptyCategoryStillReportsACertainZero(t *testing.T) {
	f := newFixture(t, recSkillListing, recAssistant)
	rep := inspect(t, f.request())

	cat, ok := rep.Category(CategoryMCP)
	if !ok {
		t.Fatal("want an MCP category")
	}
	if cat.Unobserved {
		t.Error("a category whose burst was read in full is marked unobserved: an established absence is a finding, not a gap")
	}
	if _, complete := cat.DisplayTotal(); !complete {
		t.Error("an established absence reports an incomplete total")
	}
	if _, ok := cat.DisplayBadge(); !ok {
		t.Error("an established absence withholds its badge")
	}
	if _, known := cat.DisplayItemCount(); !known {
		t.Error("an established absence withholds its item count")
	}
}
