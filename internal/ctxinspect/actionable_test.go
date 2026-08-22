package ctxinspect

import "testing"

// The cold-eye reviewer's sharpest structural finding was that the pitch and
// the product did not match: "see what is loaded so you can clean it up", and
// then a default screen with no verb, no action and no saving on it. The number
// that fixes that is the one below, and it is a number, so it has to obey the
// same rule as every other number here — never confident when it is not certain.

func TestActionableTotalCountsOnlyItemsWithALever(t *testing.T) {
	locked := pricedItem("locked", 700)
	locked.Lever = ImmovableLever("harness internals")

	r := anchoredReport(2000, pricedItem("a", 300), pricedItem("b", 200), locked)

	n, tokens, complete := r.ActionableTotal()
	if !complete {
		t.Fatal("every contributing item is priced, so the sum is a total, not a floor")
	}
	if n != 2 {
		t.Fatalf("n = %d, want 2: the immovable item is not something the user can act on", n)
	}
	if tokens != 500 {
		t.Fatalf("tokens = %d, want 500: the immovable item's 700 is not a saving", tokens)
	}
}

func TestActionableTotalIsAFloorWhenACostIsUnknown(t *testing.T) {
	unpriced := pricedItem("b", 0)
	unpriced.Load = LoadedNowCost(UnknownTokens("no per-item accounting"))

	r := anchoredReport(2000, pricedItem("a", 300), unpriced)

	n, tokens, complete := r.ActionableTotal()
	if complete {
		t.Fatal("an unknown cost cannot be summed away: the caller must render this as a lower bound")
	}
	if n != 2 {
		t.Fatalf("n = %d, want 2: an item whose cost is unknown is still an item you can act on", n)
	}
	if tokens != 300 {
		t.Fatalf("tokens = %d, want 300 (the known part only)", tokens)
	}
}

// TestActionableTotalCountsDeferredItemsAtWhatTheyCostToday pins the honest
// half of the payoff line. A deferred skill's 9,000-token body is what it would
// cost if it loaded; today it costs the listing line. Quoting the body as a
// saving would be the single most flattering lie this screen could tell.
func TestActionableTotalCountsDeferredItemsAtWhatTheyCostToday(t *testing.T) {
	deferred := Item{
		ID:      "skill:dataviz",
		Label:   "dataviz",
		Content: CapturedContent(KindListing, "dataviz — charts and graphs"),
		Load:    OnDemandCost(Estimated(100, "chars/4"), Estimated(9000, "chars/4")),
		Origin:  OriginUserConfig,
		Lever:   DirLever("/home/u/.claude/skills/dataviz", "installed skill"),
	}

	r := anchoredReport(2000, deferred)

	n, tokens, complete := r.ActionableTotal()
	if n != 1 || !complete {
		t.Fatalf("n = %d, complete = %v, want 1 and true", n, complete)
	}
	if tokens != 100 {
		t.Fatalf("tokens = %d, want 100: the 9000-token potential is not a saving available today", tokens)
	}
}

// TestActionableTotalDoesNotDoubleCountARollup mirrors the rule every other
// total here obeys: a group header and its members are one cost, not two.
func TestActionableTotalDoesNotDoubleCountARollup(t *testing.T) {
	rollup := Item{
		ID:      "memory",
		Label:   "memory (2 files)",
		Content: AbsentContent("group header"),
		Load:    LoadedNowCost(Estimated(999, "chars/4")),
		Origin:  OriginProject,
		Lever:   ImmovableLever("group header"),
		Rollup:  true,
		Children: []Item{
			pricedItem("memory/a", 100),
			pricedItem("memory/b", 200),
		},
	}

	r := anchoredReport(2000, rollup)

	n, tokens, complete := r.ActionableTotal()
	if !complete {
		t.Fatal("both children are priced, so the sum is complete")
	}
	if n != 2 {
		t.Fatalf("n = %d, want 2: the children are the items, the header is not one", n)
	}
	if tokens != 300 {
		t.Fatalf("tokens = %d, want 300: the rollup header's own 999 must not be added", tokens)
	}
}
