package quota

import (
	"testing"
	"time"
)

// TestWindowExpired pins that a window stops applying once the provider's own
// reset time has passed, and that a window without a reported reset never
// expires on a guess.
func TestWindowExpired(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(sec int64) *int64 { return &sec }
	cases := []struct {
		name     string
		resetsAt *int64
		want     bool
	}{
		{"no reset reported", nil, false},
		{"resets in the future", at(now.Unix() + 3600), false},
		{"resets exactly now", at(now.Unix()), true},
		{"reset in the past", at(now.Unix() - 1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := Window{Kind: WindowSevenDay, Label: "7d", UsedPercentage: 100, ResetsAt: tc.resetsAt}
			if got := w.Expired(now); got != tc.want {
				t.Fatalf("Expired() = %v, want %v", got, tc.want)
			}
		})
	}
}
