// Phase 6 tests for the Plugins ↔ Channels auto-link.
// RFC: docs/rfc/PLUGIN_ATTACH.md §4.7.

package session

import (
	"reflect"
	"testing"
)

// TestSyncPluginChannels_AddsEmitsChannelEntries asserts that adding a
// plugin with EmitsChannel=true produces the matching channel id.
func TestSyncPluginChannels_AddsEmitsChannelEntries(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.discord]
name = "discord"
source = "claude-plugins-official"
emits_channel = true

[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
emits_channel = false
`)
	inst := &Instance{
		Tool:    "claude",
		Plugins: []string{"discord", "octopus"},
	}
	syncPluginChannels(inst)

	// Discord gets a channel; octopus does not.
	want := []string{"plugin:discord@claude-plugins-official"}
	if !reflect.DeepEqual(inst.Channels, want) {
		t.Errorf("Channels: got %v, want %v", inst.Channels, want)
	}
}

// TestSyncPluginChannels_PreservesUserChannels asserts that channels
// not produced by any catalog entry are preserved verbatim — they are
// user-added, not autolink-managed.
func TestSyncPluginChannels_PreservesUserChannels(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.discord]
name = "discord"
source = "claude-plugins-official"
emits_channel = true
`)
	inst := &Instance{
		Tool:     "claude",
		Plugins:  []string{"discord"},
		Channels: []string{"plugin:custom@my-org/my-fork"},
	}
	syncPluginChannels(inst)

	want := []string{
		"plugin:custom@my-org/my-fork",           // user channel — preserved
		"plugin:discord@claude-plugins-official", // auto-linked
	}
	if !reflect.DeepEqual(inst.Channels, want) {
		t.Errorf("Channels: got %v, want %v", inst.Channels, want)
	}
}

// TestSyncPluginChannels_RemovesDroppedAutolinks asserts that removing
// a plugin from inst.Plugins drops its previously-auto-linked channel.
// Uses inst.AutoLinkedChannels to track ownership (G4/C2 fix).
func TestSyncPluginChannels_RemovesDroppedAutolinks(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.discord]
name = "discord"
source = "claude-plugins-official"
emits_channel = true

[plugins.slack]
name = "slack"
source = "claude-plugins-official"
emits_channel = true
`)
	inst := &Instance{
		Tool:    "claude",
		Plugins: []string{"slack"}, // discord removed
		Channels: []string{
			"plugin:discord@claude-plugins-official", // stale autolink
			"plugin:slack@claude-plugins-official",   // current autolink
			"plugin:custom@user/fork",                // user channel
		},
		// Persisted ownership: both discord and slack were auto-linked
		// previously. After plugins drops discord, syncPluginChannels
		// must remove its channel.
		AutoLinkedChannels: []string{
			"plugin:discord@claude-plugins-official",
			"plugin:slack@claude-plugins-official",
		},
	}
	syncPluginChannels(inst)

	// Order: user-managed channels first (preserved verbatim), then
	// re-added auto-links appended at the end. This matches the strip-
	// then-append flow in syncPluginChannels.
	want := []string{
		"plugin:custom@user/fork",
		"plugin:slack@claude-plugins-official",
	}
	if !reflect.DeepEqual(inst.Channels, want) {
		t.Errorf("Channels: got %v, want %v", inst.Channels, want)
	}
	// After sync: AutoLinkedChannels reflects the new ownership.
	wantOwn := []string{"plugin:slack@claude-plugins-official"}
	if !reflect.DeepEqual(inst.AutoLinkedChannels, wantOwn) {
		t.Errorf("AutoLinkedChannels: got %v, want %v", inst.AutoLinkedChannels, wantOwn)
	}
}

// TestSyncPluginChannels_DroppedFromCatalog asserts G4: a plugin
// removed from config.toml entirely (so GetPluginDef returns nil) still
// has its auto-linked channel cleaned up — because we use the persisted
// AutoLinkedChannels to know what we own, NOT a re-derivation from the
// current catalog.
func TestSyncPluginChannels_DroppedFromCatalog(t *testing.T) {
	home := withTempHome(t)
	// Catalog now has only slack; discord was removed by the user.
	writeConfig(t, home, `
[plugins.slack]
name = "slack"
source = "claude-plugins-official"
emits_channel = true
`)
	inst := &Instance{
		Tool:    "claude",
		Plugins: []string{"slack"},
		Channels: []string{
			"plugin:discord@claude-plugins-official", // owned but plugin gone
			"plugin:slack@claude-plugins-official",
		},
		AutoLinkedChannels: []string{
			"plugin:discord@claude-plugins-official",
			"plugin:slack@claude-plugins-official",
		},
	}
	syncPluginChannels(inst)

	want := []string{"plugin:slack@claude-plugins-official"}
	if !reflect.DeepEqual(inst.Channels, want) {
		t.Errorf("Channels (G4 fix): got %v, want %v", inst.Channels, want)
	}
}

// TestSyncPluginChannels_OptOutCleansUpStale asserts C2: when the
// session was previously auto-linking and then the user toggles
// PluginChannelLinkDisabled, the auto-linked channels must STILL be
// cleaned up (otherwise stale subscriptions persist forever).
func TestSyncPluginChannels_OptOutCleansUpStale(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.discord]
name = "discord"
source = "claude-plugins-official"
emits_channel = true
`)
	inst := &Instance{
		Tool:                      "claude",
		Plugins:                   []string{"discord"},
		PluginChannelLinkDisabled: true, // user opted out
		Channels: []string{
			"plugin:discord@claude-plugins-official", // previously auto-linked
			"plugin:custom@user/fork",                // user channel
		},
		AutoLinkedChannels: []string{
			"plugin:discord@claude-plugins-official",
		},
	}
	syncPluginChannels(inst)

	want := []string{"plugin:custom@user/fork"}
	if !reflect.DeepEqual(inst.Channels, want) {
		t.Errorf("Channels (C2 opt-out cleanup): got %v, want %v", inst.Channels, want)
	}
	if len(inst.AutoLinkedChannels) != 0 {
		t.Errorf("AutoLinkedChannels must be empty after opt-out; got %v", inst.AutoLinkedChannels)
	}
}

// TestSetField_Plugins_SkipsAutolinkWhenDisabled asserts the user-flag
// downstream effect: opt-out via PluginChannelLinkDisabled means no
// auto-add of channels, but stale removals still happen (covered by
// TestSyncPluginChannels_OptOutCleansUpStale).
func TestSetField_Plugins_SkipsAutolinkWhenDisabled(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.discord]
name = "discord"
source = "claude-plugins-official"
emits_channel = true
`)
	inst := &Instance{
		Tool:                      "claude",
		PluginChannelLinkDisabled: true,
	}
	if _, _, err := SetField(inst, FieldPlugins, "discord", nil); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if len(inst.Channels) != 0 {
		t.Errorf("PluginChannelLinkDisabled must suppress auto-link; got channels=%v", inst.Channels)
	}
}

// TestSetField_Plugins_AutolinksByDefault asserts the SetField path
// invokes syncPluginChannels for non-disabled instances.
func TestSetField_Plugins_AutolinksByDefault(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.discord]
name = "discord"
source = "claude-plugins-official"
emits_channel = true
`)
	inst := &Instance{Tool: "claude"}
	if _, _, err := SetField(inst, FieldPlugins, "discord", nil); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	want := []string{"plugin:discord@claude-plugins-official"}
	if !reflect.DeepEqual(inst.Channels, want) {
		t.Errorf("expected auto-linked channel; got %v", inst.Channels)
	}
}

// TestSyncPluginChannels_Idempotent asserts repeated calls produce the
// same result.
func TestSyncPluginChannels_Idempotent(t *testing.T) {
	home := withTempHome(t)
	writeConfig(t, home, `
[plugins.discord]
name = "discord"
source = "claude-plugins-official"
emits_channel = true
`)
	inst := &Instance{
		Tool:    "claude",
		Plugins: []string{"discord"},
	}
	syncPluginChannels(inst)
	first := append([]string(nil), inst.Channels...)
	syncPluginChannels(inst)
	syncPluginChannels(inst)

	if !reflect.DeepEqual(inst.Channels, first) {
		t.Errorf("syncPluginChannels not idempotent: first=%v, after-3-calls=%v", first, inst.Channels)
	}
}
