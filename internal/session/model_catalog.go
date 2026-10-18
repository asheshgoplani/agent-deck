package session

import (
	"slices"
	"strings"
)

// KnownModelIDsForTool returns model suggestions for the tool configured on
// this host: `[models.<tool>] extra`, then the installed CLI's own list when
// it can be probed (#2388), then the static catalog entries neither
// mentioned. A non-empty `[models.<tool>] list` replaces all of that (#2664).
// The list is a suggestion source, not an allowlist.
func KnownModelIDsForTool(tool string) []string {
	tool = CanonicalToolName(tool)
	override := modelListOverride(tool)
	if pinned := cleanModelIDs(override.List); len(pinned) > 0 {
		return pinned
	}
	models := staticModelIDsForTool(tool)
	if probed := probedModelCatalog(modelProbeKind(tool)); probed != nil {
		models = mergeOrdered(probed.Models, models)
	}
	if extra := cleanModelIDs(override.Extra); len(extra) > 0 {
		models = mergeOrdered(extra, models)
	}
	return models
}

// modelListOverride returns the [models.<tool>] table for a tool; a custom
// tool wrapping Claude or Codex uses that tool's table.
func modelListOverride(tool string) ModelListSettings {
	cfg, _ := LoadUserConfig()
	if cfg == nil {
		return ModelListSettings{}
	}
	kind := modelProbeKind(tool)
	if IsClaudeCompatible(tool) {
		kind = "claude"
	}
	return cfg.Models.ForTool(kind)
}

// cleanModelIDs trims configured IDs and drops blanks and duplicates.
func cleanModelIDs(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// modelProbeKind maps a tool name to the prober that answers for it, so a
// custom tool wrapping Codex shares the Codex probe.
func modelProbeKind(tool string) string {
	if IsCodexCompatible(tool) {
		return "codex"
	}
	return tool
}

// staticModelIDsForTool is the built-in catalog: the fallback when a probe is
// unavailable, and the floor merged under a successful probe.
func staticModelIDsForTool(tool string) []string {
	switch {
	case IsClaudeCompatible(tool):
		return []string{
			"claude-opus-5-5",
			"claude-opus-5",
			"claude-sonnet-5",
			"claude-fable-5-1",
			"claude-fable-5",
			"claude-sonnet-4-6",
			"claude-opus-4-8",
			"claude-opus-4-7",
			"claude-haiku-4-5",
			"claude-haiku-4-5-20251001",
		}
	case tool == "gemini":
		return []string{
			"gemini-3.1-pro-preview",
			"gemini-3.1-pro-preview-customtools",
			"gemini-3-flash-preview",
			"gemini-3.1-flash-lite",
			"gemini-3.1-flash-lite-preview",
			"gemini-2.5-pro",
			"gemini-2.5-flash",
			"gemini-2.5-flash-lite",
		}
	case tool == "opencode":
		return []string{
			"openai/gpt-5.5",
			"openai/gpt-5.5-pro",
			"openai/gpt-5.4",
			"openai/gpt-5.4-pro",
			"openai/gpt-5.4-mini",
			"openai/gpt-5.3-codex",
			"openai/gpt-5",
			"openai/o3",
			"anthropic/claude-opus-5-5",
			"anthropic/claude-opus-5",
			"anthropic/claude-sonnet-5",
			"anthropic/claude-fable-5-1",
			"anthropic/claude-fable-5",
			"anthropic/claude-sonnet-4-6",
			"anthropic/claude-opus-4-8",
			"anthropic/claude-opus-4-7",
			"anthropic/claude-haiku-4-5",
		}
	case IsCodexCompatible(tool):
		return []string{
			"gpt-6-astra",
			"gpt-6-sol",
			"gpt-6-luna",
			"gpt-5.6-sol",
			"gpt-5.6-terra",
			"gpt-5.6-luna",
			"gpt-5.5",
			"gpt-5.5-pro",
			"gpt-5.4",
			"gpt-5.4-pro",
			"gpt-5.4-mini",
			"gpt-5.4-nano",
			"gpt-5.3-codex",
			"gpt-5.2",
			"gpt-5.2-pro",
			"gpt-5.1",
			"gpt-5-pro",
			"gpt-5",
			"gpt-5-mini",
			"gpt-5-nano",
			"gpt-4.1",
			"gpt-4.1-mini",
			"gpt-4o",
			"gpt-4o-mini",
			"o3-pro",
			"o3",
		}
	default:
		return nil
	}
}
