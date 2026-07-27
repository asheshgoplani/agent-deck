package session

import "testing"

// The [ui] adaptive_refresh_max_skips knob (issue #1753, PR #1765) is resolved
// through GetAdaptiveRefreshMaxSkips so a config value can never push the
// staleness ceiling past MaxAdaptiveRefreshMaxSkips: before the clamp, a
// config value of 100000 was accepted verbatim, which would delay every
// time-based transition on an off-screen row by ~55 hours of sweeps.
func TestUISettings_GetAdaptiveRefreshMaxSkips(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"unset falls back to default", 0, DefaultAdaptiveRefreshMaxSkips},
		{"explicit value passes through", 5, 5},
		{"max is allowed", MaxAdaptiveRefreshMaxSkips, MaxAdaptiveRefreshMaxSkips},
		{"above max clamps", MaxAdaptiveRefreshMaxSkips + 1, MaxAdaptiveRefreshMaxSkips},
		{"absurd value clamps", 100000, MaxAdaptiveRefreshMaxSkips},
		{"negative is the kill switch", -1, 0},
		{"very negative is still the kill switch", -100000, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := UISettings{AdaptiveRefreshMaxSkips: tc.input}
			if got := u.GetAdaptiveRefreshMaxSkips(); got != tc.want {
				t.Errorf("GetAdaptiveRefreshMaxSkips(%d) = %d, want %d", tc.input, got, tc.want)
			}
		})
	}
}
