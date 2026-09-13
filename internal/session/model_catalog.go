package session

// KnownModelIDsForTool returns model suggestions for the tool configured on this host.
func KnownModelIDsForTool(tool string) []string {
	switch {
	case IsClaudeCompatible(tool):
		return []string{
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
