package costs

import (
	"encoding/json"
	"testing"
)

// TestIssue1101_RemoteCostSummary_JSONRoundtrip pins the wire format that
// `agent-deck costs summary --json` emits and that the local TUI's SSH
// fetcher decodes back into a RemoteCostSummary. The field names are an
// API contract — renaming any of them silently breaks remote cost
// aggregation across an agent-deck-on-each-side upgrade.
func TestIssue1101_RemoteCostSummary_JSONRoundtrip(t *testing.T) {
	original := RemoteCostSummary{
		CostTodayMicrodollars:     1_500_000, // $1.50
		CostYesterdayMicrodollars: 2_000_000,
		CostThisWeekMicrodollars:  10_000_000,
		CostLastWeekMicrodollars:  9_000_000,
		CostThisMonthMicrodollars: 25_000_000,
		CostLastMonthMicrodollars: 23_000_000,
		CostProjectedMicrodollars: 30_000_000,
		EventsToday:               5,
		EventsThisWeek:            42,
		EventsThisMonth:           180,
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Spot-check the JSON keys — these are what `costs summary --json`
	// writes and what SSHRunner.FetchCostSummary reads.
	wantSubstrings := []string{
		`"cost_today_microdollars":1500000`,
		`"cost_this_week_microdollars":10000000`,
		`"cost_projected_microdollars":30000000`,
		`"events_today":5`,
		`"events_this_month":180`,
	}
	got := string(raw)
	for _, want := range wantSubstrings {
		if !contains(got, want) {
			t.Fatalf("missing %q in JSON output: %s", want, got)
		}
	}

	var decoded RemoteCostSummary
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded != original {
		t.Fatalf("roundtrip mismatch:\nwant %+v\ngot  %+v", original, decoded)
	}
}

// TestIssue1101_MergeRemoteCostSummaries_AggregatesTotals asserts that the
// TUI's per-host summary aggregator sums every microdollar field across all
// configured remotes. This is the core arithmetic that makes the
// status-line cost segment reflect "local + every remote" instead of
// "local only" (which is what users on v1.9.23 see today for remote
// claude sessions).
func TestIssue1101_MergeRemoteCostSummaries_AggregatesTotals(t *testing.T) {
	summaries := map[string]*RemoteCostSummary{
		"dev": {
			CostTodayMicrodollars:    1_000_000,
			CostThisWeekMicrodollars: 5_000_000,
			EventsToday:              3,
		},
		"prod": {
			CostTodayMicrodollars:    2_500_000,
			CostThisWeekMicrodollars: 12_000_000,
			EventsToday:              7,
		},
		// nil entry simulates a failed fetch (e.g., older remote without
		// `costs summary --json`); aggregator must skip it without panic.
		"broken": nil,
	}

	got := MergeRemoteCostSummaries(summaries)

	if want := int64(3_500_000); got.CostTodayMicrodollars != want {
		t.Fatalf("CostTodayMicrodollars = %d, want %d", got.CostTodayMicrodollars, want)
	}
	if want := int64(17_000_000); got.CostThisWeekMicrodollars != want {
		t.Fatalf("CostThisWeekMicrodollars = %d, want %d", got.CostThisWeekMicrodollars, want)
	}
	if want := 10; got.EventsToday != want {
		t.Fatalf("EventsToday = %d, want %d", got.EventsToday, want)
	}
}

// TestIssue1101_MergeRemoteCostSummaries_EmptyInput is a safety guard for
// the cold-start case (no remotes configured, or first fetch hasn't
// completed). The renderer calls Merge on every paint; it must return a
// zero value cleanly, not panic on nil/empty maps.
func TestIssue1101_MergeRemoteCostSummaries_EmptyInput(t *testing.T) {
	if got := MergeRemoteCostSummaries(nil); got != (RemoteCostSummary{}) {
		t.Fatalf("nil input: got %+v, want zero value", got)
	}
	if got := MergeRemoteCostSummaries(map[string]*RemoteCostSummary{}); got != (RemoteCostSummary{}) {
		t.Fatalf("empty map: got %+v, want zero value", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
