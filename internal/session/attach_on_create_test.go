package session

// [ui].attach_on_create controls whether the TUI attaches to a newly created
// session immediately (instead of only selecting it). It is opt-IN: default
// false preserves today's select-only behavior so existing users see no change.

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// Default: key absent → select-only (false).
func TestAttachOnCreate_DefaultsOffWhenUnset(t *testing.T) {
	var ui UISettings // zero value: key never set in config.toml.
	if ui.GetAttachOnCreate() {
		t.Fatalf("GetAttachOnCreate() on unset config = true, want false (attach-on-create is opt-in)")
	}
}

// Opt-IN: explicit `= true` enables attach-on-create.
func TestAttachOnCreate_ExplicitTrueEnables(t *testing.T) {
	const doc = "[ui]\nattach_on_create = true\n"
	var cfg UserConfig
	if _, err := toml.Decode(doc, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cfg.UI.GetAttachOnCreate() {
		t.Fatalf("explicit attach_on_create=true reported GetAttachOnCreate()=false")
	}
}

// Explicit `= false` stays select-only.
func TestAttachOnCreate_ExplicitFalseStaysOff(t *testing.T) {
	const doc = "[ui]\nattach_on_create = false\n"
	var cfg UserConfig
	if _, err := toml.Decode(doc, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.UI.GetAttachOnCreate() {
		t.Fatalf("explicit attach_on_create=false reported GetAttachOnCreate()=true")
	}
}
