package session

// UX top-3 change #1: Enter-advances becomes the DEFAULT for the new-session
// dialog. PR #1295 shipped `[ui].new_session_enter_advances` as opt-IN (default
// false). 12 user signals (issues #23/#1344/#1259/#1353, PR #1295 body, Feedback
// Hub "counter-intuitive entry stuff", maintainer "hard to navigate") all point
// at the same trap: pressing Enter right after typing the name silently creates a
// wrong session. So Enter-advances is now ON by default and the config key flips
// to an opt-OUT: `new_session_enter_advances = false` restores the old behavior.
//
// To distinguish "key absent" (→ default ON) from "explicitly false" (→ opt out),
// the field is a *bool (nil == unset), mirroring the established default-getter
// pattern used by ShellSettings / PreviewSettings elsewhere in this file.

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// Default: key absent → Enter advances (true). This is the behavior flip.
func TestNewSessionEnterAdvances_DefaultsOnWhenUnset(t *testing.T) {
	var ui UISettings // zero value: key never set in config.toml.
	if !ui.GetNewSessionEnterAdvances() {
		t.Fatalf("GetNewSessionEnterAdvances() on unset config = false, want true (Enter-advances is now the default)")
	}
}

// Opt-OUT: explicit `= false` restores the legacy Enter-submits behavior.
func TestNewSessionEnterAdvances_ExplicitFalseOptsOut(t *testing.T) {
	const doc = "[ui]\nnew_session_enter_advances = false\n"
	var cfg UserConfig
	if _, err := toml.Decode(doc, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.UI.GetNewSessionEnterAdvances() {
		t.Fatalf("explicit new_session_enter_advances=false did not opt out; got advance=true")
	}
}

// Explicit `= true` keeps Enter-advances on (unchanged from opt-in days).
func TestNewSessionEnterAdvances_ExplicitTrueStaysOn(t *testing.T) {
	const doc = "[ui]\nnew_session_enter_advances = true\n"
	var cfg UserConfig
	if _, err := toml.Decode(doc, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !cfg.UI.GetNewSessionEnterAdvances() {
		t.Fatalf("explicit new_session_enter_advances=true reported advance=false")
	}
}
