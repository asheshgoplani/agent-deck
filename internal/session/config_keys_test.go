package session

import (
	"errors"
	"reflect"
	"testing"
)

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
			raw = k.Values[0] + "," + k.Values[1]
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
			if !reflect.DeepEqual(g, val) {
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

	// #2895: `config set claude.default_account work` writes the [claude]
	// key the New Session dialog and `add`/`launch` read, and "" clears it.
	k, ok := LookupConfigKey("claude.default_account")
	if !ok {
		t.Fatal("claude.default_account is not a config key")
	}
	cfg := &UserConfig{}
	k.Set(cfg, "work")
	if cfg.Claude.DefaultAccount != "work" || k.Get(cfg) != "work" {
		t.Fatalf("claude.default_account set work: field %q, get %v", cfg.Claude.DefaultAccount, k.Get(cfg))
	}
	k.Set(cfg, "")
	if cfg.Claude.DefaultAccount != "" {
		t.Fatalf("claude.default_account set \"\": field %q, want cleared", cfg.Claude.DefaultAccount)
	}
}

// TestConfigKeysConsent: only macapp.plugins needs consent, and only to turn
// it on; SetConfigValue refuses it without consent before touching the file.
func TestConfigKeysConsent(t *testing.T) {
	for _, k := range ConfigKeys() {
		if (k.Consent != "") != (k.Key == "macapp.plugins") {
			t.Errorf("%s: consent %q", k.Key, k.Consent)
		}
	}
	k, _ := LookupConfigKey("macapp.plugins")
	if !k.NeedsConsent(true) || k.NeedsConsent(false) {
		t.Fatalf("NeedsConsent: on %v off %v", k.NeedsConsent(true), k.NeedsConsent(false))
	}
	if _, v, err := SetConfigValue("macapp.plugins", "on", false); !errors.Is(err, ErrConsentRequired) || v != true {
		t.Fatalf("SetConfigValue without consent: %v %v", v, err)
	}
}
