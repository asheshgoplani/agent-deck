// Phase 3 mutator tests for SetField(FieldPlugins).
// RFC: docs/rfc/PLUGIN_ATTACH.md §4.5, §6.

package session

import (
	"strings"
	"testing"
)

// TestSetField_Plugins_ReplacesList asserts the mutator replaces (not
// appends) the plugins list, matching the FieldChannels contract.
func TestSetField_Plugins_ReplacesList(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"

[plugins.discord]
name = "discord"
source = "claude-plugins-official"
`)
	inst := &Instance{Tool: "claude", Plugins: []string{"discord"}}
	old, _, err := SetField(inst, FieldPlugins, "octopus,discord", nil)
	if err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if old != "discord" {
		t.Errorf("oldValue: got %q, want %q", old, "discord")
	}
	if got := strings.Join(inst.Plugins, ","); got != "octopus,discord" {
		t.Errorf("Plugins after: got %q, want %q", got, "octopus,discord")
	}
}

// TestSetField_Plugins_ClearsWithEmptyValue asserts that passing "" wipes
// the list (mirrors channels semantics).
func TestSetField_Plugins_ClearsWithEmptyValue(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	inst := &Instance{Tool: "claude", Plugins: []string{"octopus"}}
	if _, _, err := SetField(inst, FieldPlugins, "", nil); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if len(inst.Plugins) != 0 {
		t.Errorf("Plugins after empty-clear: got %v, want []", inst.Plugins)
	}
}

// TestSetField_Plugins_RefusesNonClaude asserts the claude-only gate.
func TestSetField_Plugins_RefusesNonClaude(t *testing.T) {
	inst := &Instance{Tool: "shell"}
	_, _, err := SetField(inst, FieldPlugins, "octopus", nil)
	if err == nil {
		t.Fatal("SetField must error on non-claude session")
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error should mention claude requirement; got: %v", err)
	}
}

// TestSetField_Plugins_RejectsUnknown asserts unknown catalog names are
// rejected with the available list.
func TestSetField_Plugins_RejectsUnknown(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	inst := &Instance{Tool: "claude"}
	_, _, err := SetField(inst, FieldPlugins, "octopus,ghost", nil)
	if err == nil {
		t.Fatal("SetField must error on unknown plugin name")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error should mention the unknown name; got: %v", err)
	}
	if !strings.Contains(err.Error(), "octopus") {
		t.Errorf("error should list available names (catalog includes octopus); got: %v", err)
	}
	// Verify state is NOT mutated on error (the partial list of valid
	// names from before the failure must NOT have leaked into Plugins).
	if len(inst.Plugins) != 0 {
		t.Errorf("Plugins must remain unchanged on validation error; got %v", inst.Plugins)
	}
}

// TestSetField_Plugins_RefusesTelegramOfficial asserts the v1 §6 telegram
// refusal at the mutator layer with the FQ id form.
func TestSetField_Plugins_RefusesTelegramOfficial(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	inst := &Instance{Tool: "claude"}
	_, _, err := SetField(inst, FieldPlugins, "telegram@claude-plugins-official", nil)
	if err == nil {
		t.Fatal("SetField must refuse telegram@claude-plugins-official")
	}
	if !strings.Contains(err.Error(), "telegram") || !strings.Contains(err.Error(), "channel") {
		t.Errorf("error should mention telegram and pointer to channels; got: %v", err)
	}
}

// TestSetField_Plugins_EmptyCatalogHasHelpfulError asserts the error
// message is actionable when no [plugins.*] block exists in config.toml.
func TestSetField_Plugins_EmptyCatalogHasHelpfulError(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[claude]
config_dir = "~/.claude"
`)
	inst := &Instance{Tool: "claude"}
	_, _, err := SetField(inst, FieldPlugins, "octopus", nil)
	if err == nil {
		t.Fatal("SetField must error on empty catalog")
	}
	if !strings.Contains(err.Error(), "catalog is empty") {
		t.Errorf("error should mention empty catalog; got: %v", err)
	}
	if !strings.Contains(err.Error(), "config.toml") {
		t.Errorf("error should point at config.toml; got: %v", err)
	}
}

// TestRestartPolicyFor_Plugins asserts plugins are restart-required —
// claude reads enabledPlugins only at process start.
func TestRestartPolicyFor_Plugins(t *testing.T) {
	if got := RestartPolicyFor(FieldPlugins); got != FieldRestartRequired {
		t.Errorf("plugins must be FieldRestartRequired (claude reads enabledPlugins at exec); got %v", got)
	}
}

// TestValidMutableFields_IncludesPlugins asserts the field is in the
// allowlist used by `agent-deck session set` to validate the field arg.
func TestValidMutableFields_IncludesPlugins(t *testing.T) {
	for _, f := range ValidMutableFields {
		if f == FieldPlugins {
			return
		}
	}
	t.Errorf("ValidMutableFields must include FieldPlugins; got %v", ValidMutableFields)
}
