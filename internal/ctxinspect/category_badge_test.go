package ctxinspect

import "testing"

// A category is only as trustworthy as its worst member: a single absent or
// unpriced item must not hide behind a majority of good ones.
func TestCategoryBadgeTakesTheWeakestAxisIndependently(t *testing.T) {
	tests := []struct {
		name  string
		items []Item
		want  Badge
	}{
		{
			name: "all captured and measured",
			items: []Item{
				{Content: CapturedContent(KindListing, "a"), Load: LoadedNowCost(Measured(1, "usage"))},
				{Content: CapturedContent(KindListing, "b"), Load: LoadedNowCost(Measured(2, "usage"))},
			},
			want: Badge{Text: TextCaptured, Token: TokenProviderMeasured},
		},
		{
			name: "one reconstructed drags the text axis only",
			items: []Item{
				{Content: CapturedContent(KindListing, "a"), Load: LoadedNowCost(Measured(1, "usage"))},
				{Content: ReconstructedContent(KindMarkdown, "b", "rebuilt"), Load: LoadedNowCost(Measured(2, "usage"))},
			},
			want: Badge{Text: TextReconstructed, Token: TokenProviderMeasured},
		},
		{
			name: "one estimate drags the token axis only",
			items: []Item{
				{Content: CapturedContent(KindListing, "a"), Load: LoadedNowCost(Measured(1, "usage"))},
				{Content: CapturedContent(KindListing, "b"), Load: LoadedNowCost(Estimated(2, "chars/4"))},
			},
			want: Badge{Text: TextCaptured, Token: TokenEstimated},
		},
		{
			name: "one unknown drags the token axis all the way down",
			items: []Item{
				{Content: CapturedContent(KindListing, "a"), Load: LoadedNowCost(Estimated(1, "chars/4"))},
				{Content: CapturedContent(KindListing, "b"), Load: Load{State: LoadedNow, Actual: UnknownTokens("no accounting")}},
			},
			want: Badge{Text: TextCaptured, Token: TokenUnknown},
		},
		{
			name: "absent text and a residual number",
			items: []Item{
				{Content: AbsentContent("never written to disk"), Load: LoadedNowCost(ResidualTokens(5, "anchor − Σ"))},
			},
			want: Badge{Text: TextAbsent, Token: TokenResidual},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Category{Name: "c", Items: tc.items}.Badge()
			if got != tc.want {
				t.Fatalf("Badge() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// An empty category asserts nothing. Reporting it as captured and measured
// would be a claim about content that does not exist.
func TestCategoryBadgeOnAnEmptyCategoryClaimsNothing(t *testing.T) {
	got := Category{Name: "empty"}.Badge()
	want := Badge{Text: TextAbsent, Token: TokenUnknown}
	if got != want {
		t.Fatalf("Badge() = %+v, want %+v", got, want)
	}
	if got.Confidence() != ConfidenceLow {
		t.Fatalf("Confidence() = %v, want low", got.Confidence())
	}
}

// A rollup parent restates its children's cost. Letting it vote would let a
// group header outweigh the members it merely summarises.
func TestCategoryBadgeSkipsRollupParents(t *testing.T) {
	cat := Category{
		Name: "c",
		Items: []Item{{
			Label:   "group",
			Content: CapturedContent(KindListing, "group"),
			Load:    LoadedNowCost(Measured(3, "sum")),
			Rollup:  true,
			Children: []Item{
				{Content: ReconstructedContent(KindMarkdown, "a", "rebuilt"), Load: LoadedNowCost(Estimated(1, "chars/4"))},
				{Content: ReconstructedContent(KindMarkdown, "b", "rebuilt"), Load: LoadedNowCost(Estimated(2, "chars/4"))},
			},
		}},
	}
	got := cat.Badge()
	want := Badge{Text: TextReconstructed, Token: TokenEstimated}
	if got != want {
		t.Fatalf("Badge() = %+v, want %+v", got, want)
	}
}

func TestWeakestTextProv(t *testing.T) {
	tests := []struct {
		a, b, want TextProv
	}{
		{TextCaptured, TextCaptured, TextCaptured},
		{TextCaptured, TextReconstructed, TextReconstructed},
		{TextReconstructed, TextCaptured, TextReconstructed},
		{TextReconstructed, TextAbsent, TextAbsent},
		{TextAbsent, TextCaptured, TextAbsent},
	}
	for _, tc := range tests {
		if got := weakestTextProv(tc.a, tc.b); got != tc.want {
			t.Fatalf("weakestTextProv(%v, %v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
