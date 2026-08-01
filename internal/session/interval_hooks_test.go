package session

import "testing"

// boolPtr is defined in gemini_yolo_test.go (same package).

func TestIntervalHookSettings_GetEnabled(t *testing.T) {
	cases := []struct {
		name string
		h    IntervalHookSettings
		want bool
	}{
		{"command set, enabled unset -> true", IntervalHookSettings{Command: "echo hi"}, true},
		{"command empty, enabled unset -> false", IntervalHookSettings{}, false},
		{"command whitespace only -> false", IntervalHookSettings{Command: "   "}, false},
		{"explicit enabled=false pauses", IntervalHookSettings{Command: "echo hi", Enabled: boolPtr(false)}, false},
		{"explicit enabled=true with no command -> true", IntervalHookSettings{Enabled: boolPtr(true)}, true},
	}
	for _, c := range cases {
		if got := c.h.GetEnabled(); got != c.want {
			t.Errorf("%s: GetEnabled() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIntervalHookSettings_GetIntervalSeconds(t *testing.T) {
	cases := []struct {
		in   int
		want int
	}{
		{0, DefaultIntervalHookSeconds},  // unset -> default
		{-5, DefaultIntervalHookSeconds}, // negative -> default
		{1, MinIntervalHookSeconds},      // below floor -> clamp up
		{5, 5},                           // at floor
		{60, 60},                         // normal
		{999999, MaxIntervalHookSeconds}, // above ceiling -> clamp down
	}
	for _, c := range cases {
		h := IntervalHookSettings{IntervalSeconds: c.in}
		if got := h.GetIntervalSeconds(); got != c.want {
			t.Errorf("GetIntervalSeconds(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestIntervalHookSettings_GetTimeoutSeconds(t *testing.T) {
	cases := []struct {
		name            string
		intervalSeconds int
		timeoutSeconds  int
		want            int
	}{
		{"unset -> min(default, interval)", 60, 0, DefaultIntervalHookTimeout},
		{"unset, small interval -> interval", 10, 0, 10},
		{"explicit within bounds", 60, 15, 15},
		{"explicit exceeds interval -> clamp to interval", 30, 120, 30},
		{"negative -> default", 60, -3, DefaultIntervalHookTimeout},
	}
	for _, c := range cases {
		h := IntervalHookSettings{IntervalSeconds: c.intervalSeconds, TimeoutSeconds: c.timeoutSeconds}
		if got := h.GetTimeoutSeconds(); got != c.want {
			t.Errorf("%s: GetTimeoutSeconds() = %d, want %d", c.name, got, c.want)
		}
	}
}
