package session

import (
	"sort"
	"strings"
)

// OrchestrateToolPolicy is the resolved tool-selection input consumed by the
// orchestrate workflow.
type OrchestrateToolPolicy struct {
	Strategy       string   `json:"strategy"`
	FallbackTool   string   `json:"fallback_tool"`
	AvailableTools []string `json:"available_tools"`
}

// ResolveOrchestrateToolPolicy resolves orchestration settings against the
// locally installed, non-hidden tool registry. An empty strategy deliberately
// remains empty so existing workflows can preserve their legacy behavior.
func (c *UserConfig) ResolveOrchestrateToolPolicy() OrchestrateToolPolicy {
	if c == nil {
		return OrchestrateToolPolicy{}
	}

	policy := OrchestrateToolPolicy{
		Strategy:     strings.TrimSpace(c.Orchestrate.ToolStrategy),
		FallbackTool: strings.TrimSpace(c.DefaultTool),
	}
	if policy.Strategy != "auto" {
		return policy
	}

	registry := InitFiltered(c.Tools, true, c.UI.HiddenTools)
	for _, name := range registry.order {
		if name != "shell" && registry.installed[name] && !registry.userHidden[name] {
			policy.AvailableTools = append(policy.AvailableTools, name)
		}
	}
	custom := make([]string, 0, len(registry.custom))
	for name := range registry.custom {
		if registry.installed[name] && !registry.userHidden[name] {
			custom = append(custom, name)
		}
	}
	sort.Strings(custom)
	policy.AvailableTools = append(policy.AvailableTools, custom...)
	return policy
}
