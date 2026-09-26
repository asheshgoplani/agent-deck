package session

import "testing"

// TestConfigKeysRoundTrip: every key parses its default back, sets it on a
// config and reads the same value, so get/set/schema cannot drift apart.
func TestConfigKeysRoundTrip(t *testing.T) {
	for _, k := range ConfigKeys() {
		cfg := &UserConfig{}
		var raw string
		switch v := k.Default.(type) {
		case bool:
			raw = map[bool]string{true: "false", false: "true"}[v]
		case int:
			raw = "7"
		case float64:
			raw = "0.3"
		case []string:
			raw = "cpu,load"
		case string:
			raw = v
			if k.Type == "enum" {
				raw = k.Values[len(k.Values)-1]
			}
			if k.Type == "string" {
				raw = "x"
			}
		default:
			t.Fatalf("%s: default of unexpected type %T", k.Key, k.Default)
		}
		val, err := k.Parse(raw)
		if err != nil {
			t.Fatalf("%s: parse %q: %v", k.Key, raw, err)
		}
		k.Set(cfg, val)
		got := k.Get(cfg)
		if g, ok := got.([]string); ok {
			if len(g) != 2 || g[0] != "cpu" || g[1] != "load" {
				t.Errorf("%s: list round-trip %v", k.Key, g)
			}
			continue
		}
		if got != val {
			t.Errorf("%s: set %v, got %v", k.Key, val, got)
		}
	}
	if k, ok := LookupConfigKey("ui.theme"); !ok || k.Key != "theme" {
		t.Fatalf("alias ui.theme: %+v %v", k, ok)
	}
}
