package codex

import (
	"strings"
	"testing"
)

func TestSpanSetClaimsWithoutOverlapping(t *testing.T) {
	// Two files with identical content must not both claim the same characters:
	// if they did, the block's residue would go negative and the report would
	// attribute more than was injected.
	block := "HEADER\nSAME\nMIDDLE\nSAME\nFOOTER"
	spans := newSpanSet(block)

	first, ok := spans.Claim("SAME")
	if !ok {
		t.Fatal("the first claim must succeed")
	}
	second, ok := spans.Claim("SAME")
	if !ok {
		t.Fatal("the second occurrence must be claimable")
	}
	if first.Start == second.Start {
		t.Fatalf("both claims landed on the same span %v", first)
	}
	if _, ok := spans.Claim("SAME"); ok {
		t.Fatal("a third claim must fail: only two occurrences exist")
	}
	if got, want := spans.Claimed(), 8; got != want {
		t.Fatalf("Claimed() = %d, want %d", got, want)
	}
	if got, want := spans.Residue(), len([]rune(block))-8; got != want {
		t.Fatalf("Residue() = %d, want %d", got, want)
	}
}

func TestSpanSetClaimRejectsUnusableNeedles(t *testing.T) {
	spans := newSpanSet("short")
	if _, ok := spans.Claim(""); ok {
		t.Error("an empty needle must not claim anything")
	}
	if _, ok := spans.Claim("a much longer needle than the block"); ok {
		t.Error("a needle longer than the block must not claim anything")
	}
	if _, ok := spans.Claim("absent"); ok {
		t.Error("an absent needle must not claim anything")
	}
	if spans.Claimed() != 0 {
		t.Fatalf("Claimed() = %d, want 0", spans.Claimed())
	}
	if spans.MatchRate() != 0 {
		t.Fatalf("MatchRate() = %v, want 0", spans.MatchRate())
	}
}

func TestSpanSetHandlesMultiByteContent(t *testing.T) {
	// Offsets are runes, not bytes: an emoji in a CLAUDE.md-style file must not
	// shift every subsequent span.
	block := "🚨 rule one\nMIDDLE\n🚨 rule two"
	spans := newSpanSet(block)
	span, ok := spans.Claim("MIDDLE")
	if !ok {
		t.Fatal("claim failed")
	}
	if got := spans.Text(span); got != "MIDDLE" {
		t.Fatalf("Text(span) = %q, want MIDDLE", got)
	}
	if got, want := spans.Chars(), len([]rune(block)); got != want {
		t.Fatalf("Chars() = %d, want %d", got, want)
	}
}

func TestSpanSetEmptyBlockMatchesFully(t *testing.T) {
	spans := newSpanSet("")
	if spans.MatchRate() != 100 {
		t.Fatalf("MatchRate() = %v, want 100: an empty block has nothing left to explain", spans.MatchRate())
	}
}

func TestJoinUnclaimedReturnsTheGapsInOrder(t *testing.T) {
	spans := newSpanSet("AAA<gap>BBB<end>")
	if _, ok := spans.Claim("BBB"); !ok {
		t.Fatal("claim BBB failed")
	}
	if _, ok := spans.Claim("AAA"); !ok {
		t.Fatal("claim AAA failed")
	}
	// Claims were made out of order; the residue must still read in block order.
	if got, want := joinUnclaimed(spans), "<gap><end>"; got != want {
		t.Fatalf("joinUnclaimed() = %q, want %q", got, want)
	}
}

const sampleCatalogue = "## Skills\n" +
	"A skill is a set of local instructions.\n" +
	"### Skill roots\n" +
	"- `r0` = `/home/u/.codex/skills`\n" +
	"- `r1` = `/home/u/.codex/plugins/cache/vendor`\n" +
	"### Available skills\n" +
	"- dataviz: Charts and plots. (file: /home/u/.codex/skills/dataviz/SKILL.md)\n" +
	"- vendor:thing: A vendor skill. (file: r1/thing/SKILL.md)\n" +
	"- broken-entry-without-a-path\n"

func TestParseSkillCatalogue(t *testing.T) {
	cat := ParseSkillCatalogue(sampleCatalogue)

	if len(cat.Roots) != 2 || cat.Roots["r1"] != "/home/u/.codex/plugins/cache/vendor" {
		t.Fatalf("Roots = %v", cat.Roots)
	}
	if len(cat.Entries) != 2 {
		t.Fatalf("got %d entries, want 2 (the pathless line is not an entry): %+v", len(cat.Entries), cat.Entries)
	}

	first := cat.Entries[0]
	if first.Name != "dataviz" || first.Description != "Charts and plots." {
		t.Errorf("first entry = %+v", first)
	}
	if first.Path != "/home/u/.codex/skills/dataviz/SKILL.md" {
		t.Errorf("first path = %q", first.Path)
	}
	if first.Dir() != "/home/u/.codex/skills/dataviz" {
		t.Errorf("first dir = %q", first.Dir())
	}
	if first.Alias != "" {
		t.Errorf("an absolute path must not report a root alias, got %q", first.Alias)
	}

	second := cat.Entries[1]
	if second.Path != "/home/u/.codex/plugins/cache/vendor/thing/SKILL.md" {
		t.Errorf("root alias was not expanded: %q", second.Path)
	}
	if second.Alias != "r1" {
		t.Errorf("Alias = %q, want r1", second.Alias)
	}

	// Every character of the catalogue is either an entry or preamble; nothing
	// may be lost, because the block's total is what has to add up.
	if got, want := cat.EntryChars()+cat.PreambleChars, len([]rune(sampleCatalogue)); got != want {
		t.Fatalf("entry chars + preamble chars = %d, want %d (the whole block)", got, want)
	}
}

func TestParseSkillCatalogueOnUnknownFormat(t *testing.T) {
	// A format this build does not recognise must yield no entries rather than
	// wrong ones: mis-assigning cost between skills is the failure the feature
	// exists to prevent.
	cat := ParseSkillCatalogue("## Skills\nnothing here looks like an entry\n")
	if len(cat.Entries) != 0 {
		t.Fatalf("Entries = %+v, want none", cat.Entries)
	}
	if cat.PreambleChars == 0 {
		t.Fatal("the whole block must land in the preamble rather than vanish")
	}
}

func TestParseSkillCatalogueEmpty(t *testing.T) {
	cat := ParseSkillCatalogue("   ")
	if len(cat.Entries) != 0 || cat.PreambleChars != 0 || len(cat.Roots) != 0 {
		t.Fatalf("an empty catalogue must parse to nothing, got %+v", cat)
	}
}

func TestExpandRootLeavesUnresolvableAliasesAlone(t *testing.T) {
	roots := map[string]string{"r0": "/root/zero"}
	tests := []struct {
		name      string
		path      string
		wantPath  string
		wantAlias string
	}{
		{name: "absolute is unchanged", path: "/abs/SKILL.md", wantPath: "/abs/SKILL.md"},
		{name: "known alias expands", path: "r0/x/SKILL.md", wantPath: "/root/zero/x/SKILL.md", wantAlias: "r0"},
		{name: "unknown alias is left alone", path: "r9/x/SKILL.md", wantPath: "r9/x/SKILL.md"},
		{name: "no separator is left alone", path: "r0", wantPath: "r0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, gotAlias := expandRoot(tc.path, roots)
			if gotPath != tc.wantPath || gotAlias != tc.wantAlias {
				t.Fatalf("expandRoot(%q) = (%q, %q), want (%q, %q)", tc.path, gotPath, gotAlias, tc.wantPath, tc.wantAlias)
			}
		})
	}
}

func TestFirstLineLabelIsBounded(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := firstLineLabel(long + "\nsecond line")
	if len([]rune(got)) > 64 {
		t.Fatalf("label is %d runes, want at most 64", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("a truncated label must say so, got %q", got)
	}
	if got := firstLineLabel("   "); got != "untagged block" {
		t.Fatalf("firstLineLabel(blank) = %q", got)
	}
}
