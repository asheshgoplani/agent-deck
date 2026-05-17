package session

import (
	"strings"
	"unicode"
)

// ModelInfo is the normalized status view of a per-session model override.
// ModelID is the exact string passed to the underlying tool; Model and Version
// are best-effort display fields derived from common provider ID formats.
type ModelInfo struct {
	ModelID string
	Model   string
	Version string
}

// LaunchModelID returns the exact per-session model override that will be
// passed to the underlying tool on start/restart. Empty means tool default.
func (i *Instance) LaunchModelID() string {
	if i == nil {
		return ""
	}

	switch {
	case IsClaudeCompatible(i.Tool):
		if opts := i.GetClaudeOptions(); opts != nil {
			return strings.TrimSpace(opts.Model)
		}
	case i.Tool == "gemini":
		return strings.TrimSpace(i.GeminiModel)
	case i.Tool == "opencode":
		if opts := i.GetOpenCodeOptions(); opts != nil {
			return strings.TrimSpace(opts.Model)
		}
	case IsCodexCompatible(i.Tool):
		if opts := i.GetCodexOptions(); opts != nil {
			return strings.TrimSpace(opts.Model)
		}
	}

	return ""
}

// LaunchModelInfo returns normalized status fields for the session's model
// override. Empty ModelID means the tool's configured default is in effect.
func (i *Instance) LaunchModelInfo() ModelInfo {
	return ParseModelID(i.LaunchModelID())
}

// ParseModelID derives display-friendly model and version fields from common
// provider model IDs while preserving the exact ID for command execution.
func ParseModelID(modelID string) ModelInfo {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return ModelInfo{}
	}

	info := ModelInfo{ModelID: modelID}
	provider, base := splitProviderModelID(modelID)

	switch {
	case isClaudeAlias(base):
		info.Model = withProvider(provider, "Claude "+titleWords(base))
		info.Version = "alias"
	case strings.HasPrefix(base, "claude-"):
		info.Model, info.Version = parseClaudeModel(provider, base)
	case strings.HasPrefix(base, "gemini-"):
		info.Model, info.Version = parseGeminiModel(provider, base)
	case strings.HasPrefix(base, "gpt-"):
		info.Model, info.Version = parseGPTModel(provider, base)
	case strings.HasPrefix(base, "chatgpt-"):
		info.Model, info.Version = parseChatGPTModel(provider, base)
	case isOpenAIReasoningModel(base):
		info.Model = withProvider(provider, "OpenAI Reasoning")
		info.Version = base
	default:
		info.Model = withProvider(provider, base)
	}

	return info
}

// Display returns a compact label for human-readable status output.
func (m ModelInfo) Display() string {
	switch {
	case m.ModelID == "":
		return ""
	case m.Model != "" && m.Version != "":
		return m.Model + " " + m.Version
	case m.Model != "":
		return m.Model
	default:
		return m.ModelID
	}
}

func splitProviderModelID(modelID string) (provider, base string) {
	provider, base, ok := strings.Cut(modelID, "/")
	if !ok {
		return "", modelID
	}
	if strings.TrimSpace(base) == "" {
		return "", modelID
	}
	return strings.TrimSpace(provider), strings.TrimSpace(base)
}

func parseClaudeModel(provider, base string) (model, version string) {
	parts := strings.Split(base, "-")
	if len(parts) < 4 {
		return withProvider(provider, base), ""
	}

	model = withProvider(provider, "Claude "+titleWords(parts[1]))
	version = parts[2] + "." + parts[3]
	if len(parts) >= 5 && parts[4] != "" {
		version += " " + parts[4]
	}
	return model, version
}

func parseGeminiModel(provider, base string) (model, version string) {
	rest := strings.TrimPrefix(base, "gemini-")
	parts := strings.Split(rest, "-")
	if len(parts) < 2 {
		return withProvider(provider, "Gemini"), rest
	}

	version = parts[0]
	descriptors := append([]string(nil), parts[1:]...)
	filtered := descriptors[:0]
	for _, descriptor := range descriptors {
		if descriptor == "preview" {
			version += " Preview"
			continue
		}
		filtered = append(filtered, descriptor)
	}
	descriptors = filtered
	if len(descriptors) == 0 {
		model = "Gemini"
	} else {
		model = "Gemini " + titleWords(strings.Join(descriptors, " "))
	}
	return withProvider(provider, model), version
}

func parseGPTModel(provider, base string) (model, version string) {
	rest := strings.TrimPrefix(base, "gpt-")
	parts := strings.Split(rest, "-")
	if len(parts) == 0 || parts[0] == "" {
		return withProvider(provider, "GPT"), ""
	}

	version = parts[0]
	model = "GPT"
	if len(parts) > 1 {
		model += " " + titleWords(strings.Join(parts[1:], " "))
	}
	return withProvider(provider, model), version
}

func parseChatGPTModel(provider, base string) (model, version string) {
	rest := strings.TrimPrefix(base, "chatgpt-")
	if rest == "" {
		return withProvider(provider, "ChatGPT"), ""
	}
	return withProvider(provider, "ChatGPT"), rest
}

func isClaudeAlias(base string) bool {
	return base == "opus" || base == "sonnet" || base == "haiku"
}

func isOpenAIReasoningModel(base string) bool {
	if len(base) < 2 || base[0] != 'o' {
		return false
	}
	return base[1] >= '0' && base[1] <= '9'
}

func withProvider(provider, model string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "":
		return model
	case "anthropic":
		if strings.HasPrefix(model, "Claude ") {
			return "Anthropic " + model
		}
	case "openai":
		if strings.HasPrefix(model, "GPT") || strings.HasPrefix(model, "ChatGPT") || strings.HasPrefix(model, "OpenAI ") {
			return "OpenAI " + strings.TrimPrefix(model, "OpenAI ")
		}
	case "google", "gemini":
		if strings.HasPrefix(model, "Gemini") {
			return "Google " + model
		}
	}
	return model
}

func titleWords(s string) string {
	parts := strings.Fields(strings.ReplaceAll(s, "-", " "))
	for i, part := range parts {
		parts[i] = titleWord(part)
	}
	return strings.Join(parts, " ")
}

func titleWord(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(strings.ToLower(s))
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
