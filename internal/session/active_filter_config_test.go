package session

import (
	"reflect"
	"testing"
)

func TestActiveFilterConfigKeys(t *testing.T) {
	for _, tc := range []struct {
		key, raw string
		want     any
	}{
		{"display.default_filter", "active", "active"},
		{"display.active_filter_excludes", "error,stopped", []string{"error", "stopped"}},
	} {
		k, ok := LookupConfigKey(tc.key)
		if !ok {
			t.Errorf("missing CLI config key %s", tc.key)
			continue
		}
		v, err := k.Parse(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		cfg := &UserConfig{}
		k.Set(cfg, v)
		if got := k.Get(cfg); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s = %v, want %v", tc.key, got, tc.want)
		}
		if _, err := k.Parse("invalid-status"); err == nil {
			t.Errorf("%s accepted invalid status", tc.key)
		}
	}
}
