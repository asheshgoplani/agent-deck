package ui

import (
	"testing"
	"time"
)

// Canonical parity table — MUST stay byte-identical to the JS mirror in
// tests/web/unit/timeFmt.test.js. See the design spec.
func TestHumanizeSince_ParityTable(t *testing.T) {
	cases := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "just now"},
		{"45s", 45 * time.Second, "just now"},
		{"60s", 60 * time.Second, "1m ago"},
		{"59m", 59 * time.Minute, "59m ago"},
		{"60m", 60 * time.Minute, "1h ago"},
		{"3h20m", 3*time.Hour + 20*time.Minute, "3h 20m ago"},
		{"23h59m", 23*time.Hour + 59*time.Minute, "23h 59m ago"},
		{"24h", 24 * time.Hour, "1d ago"},
		{"2d5h", 2*24*time.Hour + 5*time.Hour, "2d 5h ago"},
		{"6d23h", 6*24*time.Hour + 23*time.Hour, "6d 23h ago"},
		{"7d", 7 * 24 * time.Hour, "1w ago"},
		{"3w2d", 23 * 24 * time.Hour, "3w 2d ago"},
		{"30d", 30 * 24 * time.Hour, "1mo ago"},
		{"5mo1w", 160 * 24 * time.Hour, "5mo 1w ago"},
		{"365d", 365 * 24 * time.Hour, "1y ago"},
		{"2y3mo", 820 * 24 * time.Hour, "2y 3mo ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := humanizeSince(tc.d); got != tc.want {
				t.Errorf("humanizeSince(%v) = %q, want %q", tc.d, got, tc.want)
			}
		})
	}
}

// Negative/future skew must not produce a weird string.
func TestHumanizeSince_FutureIsJustNow(t *testing.T) {
	if got := humanizeSince(-5 * time.Minute); got != "just now" {
		t.Errorf("humanizeSince(negative) = %q, want %q", got, "just now")
	}
}

// Zero-secondary component is dropped, not shown as "Xh 0m".
func TestHumanizeSince_DropsZeroSecondary(t *testing.T) {
	if got := humanizeSince(5 * time.Hour); got != "5h ago" {
		t.Errorf("humanizeSince(5h) = %q, want %q", got, "5h ago")
	}
}
