// CLI-side --plugin flag validation shared by `agent-deck add` and
// `agent-deck launch`. RFC: docs/rfc/PLUGIN_ATTACH.md (§6 telegram refusal).

package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func validatePluginFlags(names []string) error {
	if len(names) == 0 {
		return nil
	}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			return fmt.Errorf("--plugin: empty plugin name not allowed")
		}
		if isTelegramOfficialPluginFlag(name) {
			return fmt.Errorf("--plugin %q refused in v1: telegram@claude-plugins-official cannot be enabled via --plugin. Use --channel plugin:telegram@claude-plugins-official instead. The legacy worker-scratch deny-list (issue #59) hardcodes telegram off; reconciling this with --plugin requires the deferred refactor in docs/rfc/PLUGIN_TELEGRAM_RETROFIT.md", raw)
		}
		if def := session.GetPluginDef(name); def == nil {
			available := session.GetAvailablePluginNames()
			sort.Strings(available)
			if len(available) == 0 {
				return fmt.Errorf("--plugin %q: catalog is empty. Add a [plugins.%s] table to ~/.agent-deck/config.toml", raw, name)
			}
			return fmt.Errorf("--plugin %q: not in catalog. Available: %s. Add new entries via [plugins.<name>] in ~/.agent-deck/config.toml", raw, strings.Join(available, ", "))
		}
	}
	return nil
}

// applyPluginChannelAutolink keeps CLI / mutator / dialog paths in sync
// on AutoLinkedChannels ownership tracking (G4/C2 fix).
func applyPluginChannelAutolink(inst *session.Instance) {
	if inst == nil {
		return
	}
	session.SyncPluginChannels(inst)
}

// Catches power-users who type the FQ id directly; the catalog accessors
// already filter the short-name path (RFC §6).
func isTelegramOfficialPluginFlag(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	return v == "telegram@claude-plugins-official"
}
