package session

import (
	"testing"
	"time"
)

func TestTimeFilterModeMatches(t *testing.T) {
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC) // Monday, mid-afternoon

	tests := []struct {
		name string
		mode TimeFilterMode
		t    time.Time
		want bool
	}{
		{"all matches far past", TimeFilterAll, now.AddDate(-1, 0, 0), true},
		{"all matches future", TimeFilterAll, now.Add(time.Hour), true},

		{"today matches earlier today", TimeFilterToday, now.Add(-2 * time.Hour), true},
		{"today matches exactly midnight", TimeFilterToday, time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), true},
		{"today excludes yesterday 23:59", TimeFilterToday, time.Date(2026, 6, 14, 23, 59, 59, 0, time.UTC), false},
		{"today excludes 3 days ago", TimeFilterToday, now.AddDate(0, 0, -3), false},

		{"3days matches today", TimeFilter3Days, now.Add(-time.Hour), true},
		{"3days matches exactly 3*24h ago", TimeFilter3Days, now.Add(-3 * 24 * time.Hour), true},
		{"3days excludes just past 3*24h ago", TimeFilter3Days, now.Add(-3*24*time.Hour - time.Second), false},
		{"3days excludes 7 days ago", TimeFilter3Days, now.AddDate(0, 0, -7), false},

		{"7days matches 5 days ago", TimeFilter7Days, now.AddDate(0, 0, -5), true},
		{"7days matches exactly 7*24h ago", TimeFilter7Days, now.Add(-7 * 24 * time.Hour), true},
		{"7days excludes just past 7*24h ago", TimeFilter7Days, now.Add(-7*24*time.Hour - time.Second), false},
		{"7days excludes 30 days ago", TimeFilter7Days, now.AddDate(0, 0, -30), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.mode.Matches(tt.t, now); got != tt.want {
				t.Errorf("%v.Matches(%v, %v) = %v, want %v", tt.mode, tt.t, now, got, tt.want)
			}
		})
	}
}

func TestTimeFilterModeLabel(t *testing.T) {
	tests := []struct {
		mode TimeFilterMode
		want string
	}{
		{TimeFilterAll, "All time"},
		{TimeFilterToday, "Today"},
		{TimeFilter3Days, "Last 3 days"},
		{TimeFilter7Days, "Last 7 days"},
	}
	for _, tt := range tests {
		if got := tt.mode.Label(); got != tt.want {
			t.Errorf("%v.Label() = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

// TestTimeFilterModeCycle guards the invariant the TUI hotkey relies on:
// cycling TimeFilterModeCount times returns to the starting (default) mode.
func TestTimeFilterModeCycle(t *testing.T) {
	mode := TimeFilterAll
	seen := []TimeFilterMode{mode}
	for i := 0; i < TimeFilterModeCount-1; i++ {
		mode = TimeFilterMode((int(mode) + 1) % TimeFilterModeCount)
		seen = append(seen, mode)
	}
	mode = TimeFilterMode((int(mode) + 1) % TimeFilterModeCount)
	if mode != TimeFilterAll {
		t.Errorf("cycling %d times should return to TimeFilterAll, got %v", TimeFilterModeCount, mode)
	}
	want := []TimeFilterMode{TimeFilterAll, TimeFilterToday, TimeFilter3Days, TimeFilter7Days}
	if len(seen) != len(want) {
		t.Fatalf("cycle produced %d distinct steps, want %d", len(seen), len(want))
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Errorf("cycle step %d = %v, want %v", i, seen[i], want[i])
		}
	}
}
