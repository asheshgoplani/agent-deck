package session

import (
	"testing"
	"time"
)

func TestConfiguredStatusIntervalBounds(t *testing.T) {
	for _, tc := range []struct{ configured, expected int }{
		{0, 2}, {-1, 1}, {1, 1}, {5, 5}, {10, 10}, {100, 10},
	} {
		cfg := &UserConfig{Performance: PerformanceSettings{StatusIntervalSeconds: tc.configured}}
		if got := cfg.StatusInterval(); got != time.Duration(tc.expected)*time.Second {
			t.Fatalf("configured=%d got=%s expected=%ds", tc.configured, got, tc.expected)
		}
	}
	var cfg *UserConfig
	if cfg.StatusInterval() != 2*time.Second {
		t.Fatal("nil config must retain the existing cadence")
	}
}
