// Plugin ↔ Channel auto-link (RFC docs/rfc/PLUGIN_ATTACH.md §4.7).
//
// Catalog entries with EmitsChannel=true (telegram, discord, slack…)
// auto-add `plugin:<name>@<source>` to Instance.Channels so claude
// registers the inbound notifications/claude/channel handler. Without
// that the plugin silently drops inbound messages.

package session

// SyncPluginChannels is the cmd/-side entry point (mirrors syncPluginChannels).
func SyncPluginChannels(inst *Instance) { syncPluginChannels(inst) }

// syncPluginChannels reconciles Channels + AutoLinkedChannels against
// the current Plugins / PluginChannelLinkDisabled state. Idempotent.
//
//  1. Strip prior AutoLinkedChannels from Channels (G4: works even after
//     the plugin is removed from the catalog; C2: opt-out still cleans up).
//  2. If opt-out: clear AutoLinkedChannels and return.
//  3. Recompute auto-link set from current Plugins ∩ EmitsChannel=true,
//     append new entries to Channels, persist in AutoLinkedChannels.
func syncPluginChannels(inst *Instance) {
	if inst == nil {
		return
	}

	if len(inst.AutoLinkedChannels) > 0 {
		prev := make(map[string]struct{}, len(inst.AutoLinkedChannels))
		for _, ch := range inst.AutoLinkedChannels {
			prev[ch] = struct{}{}
		}
		stripped := make([]string, 0, len(inst.Channels))
		for _, ch := range inst.Channels {
			if _, owned := prev[ch]; !owned {
				stripped = append(stripped, ch)
			}
		}
		inst.Channels = stripped
	}

	if inst.PluginChannelLinkDisabled {
		inst.AutoLinkedChannels = nil
		return
	}

	want := make([]string, 0, len(inst.Plugins))
	wantSet := make(map[string]struct{}, len(inst.Plugins))
	for _, name := range inst.Plugins {
		def := GetPluginDef(name)
		if def == nil || !def.EmitsChannel {
			continue
		}
		ch := def.ChannelID()
		if _, dup := wantSet[ch]; dup {
			continue
		}
		wantSet[ch] = struct{}{}
		want = append(want, ch)
	}

	existing := make(map[string]struct{}, len(inst.Channels))
	for _, ch := range inst.Channels {
		existing[ch] = struct{}{}
	}
	for _, ch := range want {
		if _, dup := existing[ch]; !dup {
			inst.Channels = append(inst.Channels, ch)
		}
	}

	inst.AutoLinkedChannels = want
}
