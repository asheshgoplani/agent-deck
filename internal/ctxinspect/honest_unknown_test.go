package ctxinspect

import (
	"strings"
	"testing"
)

// These tests pin the three shapes an honest unknown has to keep on its way to
// the screen: a category nobody read, an item whose absence was never
// established, and a row whose cost is already counted somewhere else. Each of
// them used to be published as a confident zero.

// TestCategoryUnobserved_RendersAsUnknownNotZero covers the phantom-zero row.
// An adapter that never saw a session's MCP block must not produce the same
// screen as one that established the session has no MCP servers.
func TestCategoryUnobserved_RendersAsUnknownNotZero(t *testing.T) {
	unread := Category{Name: "mcp", Title: "MCP instructions", Unobserved: true}
	empty := Category{Name: "mcp", Title: "MCP instructions"}

	if _, complete := unread.DisplayTotal(); complete {
		t.Error("an unobserved category reports a complete total: its 0 will print as a measured zero")
	}
	if _, complete := empty.DisplayTotal(); !complete {
		t.Error("an observed-empty category reports an incomplete total: an established absence is a measurement and must print as one")
	}
	if _, known := unread.DisplayItemCount(); known {
		t.Error("an unobserved category reports an item count: '(0 items)' is a claim about the user's configuration")
	}
	if n, known := empty.DisplayItemCount(); !known || n != 0 {
		t.Errorf("DisplayItemCount() = %d, %v for an observed-empty category; want 0, true", n, known)
	}
	if _, ok := unread.DisplayBadge(); ok {
		t.Error("an unobserved category offers a badge: Badge()'s empty fallback says ABSENT, and 'absent' is what an unread transcript cannot establish")
	}
	if _, ok := empty.DisplayBadge(); !ok {
		t.Error("an observed-empty category withholds its badge")
	}
}

// TestValidate_AvailableMayCostAnExplicitUnknown covers the item whose absence
// was never established. Demanding a certain zero here is what forced a
// summable false certainty onto content nobody looked at.
func TestValidate_AvailableMayCostAnExplicitUnknown(t *testing.T) {
	item := Item{
		ID:      "agents-md:absent:/x/AGENTS.md",
		Label:   "/x/AGENTS.md",
		Content: AbsentContent("this file could not be read, so whether it is in the injected block was never tested"),
		Load:    AvailableUnknownCost("the file could not be read, so its cost is not knowable", Estimated(100, "chars/4")),
		Origin:  OriginProject,
		Lever:   FileLever("/x/AGENTS.md", "on the search path"),
	}
	if problems := validateItem("agents-md", item); len(problems) != 0 {
		t.Errorf("an available item priced at an explicit unknown fails validation: %v", problems)
	}

	loaded := item
	loaded.Load = Load{State: Available, Actual: Measured(42, "test")}
	problems := validateItem("agents-md", loaded)
	if len(problems) != 1 || !strings.Contains(problems[0], "positive cost") {
		t.Errorf("validateItem on an available item reporting 42 tokens = %v; want exactly the positive-cost breach", problems)
	}
}

// TestInformationalItemsAreShownButNotCounted covers the row whose cost is
// already inside another row. Counting it would double the tokens; pricing it
// zero would be a false certainty; so it is excluded from the arithmetic and
// left visible.
func TestInformationalItemsAreShownButNotCounted(t *testing.T) {
	cat := Category{
		Name: "skills",
		Items: []Item{
			{
				ID:    "skills:catalogue",
				Label: "skill catalogue (unsplit)",
				Load:  LoadedNowCost(Measured(900, "captured block")),
			},
			{
				ID:            "skill:alpha",
				Label:         "alpha",
				Load:          OnDemandCost(UnknownTokens("its share of the unsplit block is not knowable"), Estimated(400, "chars/4")),
				Informational: true,
			},
		},
	}
	total, complete := cat.Total()
	if total != 900 {
		t.Errorf("Total() = %d, want 900: an informational row must not add its own cost", total)
	}
	if !complete {
		t.Error("Total() is incomplete: an informational row's unknown must not turn a whole total into a lower bound")
	}
	if len(cat.Sorted()) != 2 {
		t.Error("an informational row was dropped from the display: it is excluded from the arithmetic, not from the screen")
	}
}

// TestAnchorUpperBound_IsAskable is the flag every renderer, the --json
// consumer and verify.Compare act on. A nil anchor is no measurement at all,
// which is not the same as a measurement that overstates.
func TestAnchorUpperBound_IsAskable(t *testing.T) {
	var none *Anchor
	if none.IsUpperBound() {
		t.Error("a nil anchor claims to be an upper bound")
	}
	exact := &Anchor{Tokens: Measured(1000, "first turn")}
	if exact.IsUpperBound() {
		t.Error("an unqualified anchor claims to be an upper bound")
	}
	over := &Anchor{Tokens: Measured(1000, "first turn"), UpperBound: true, UpperBoundReason: "it also covers an earlier turn"}
	if !over.IsUpperBound() {
		t.Error("an anchor marked as an upper bound does not report itself as one, so every renderer will badge it as exact")
	}
}
