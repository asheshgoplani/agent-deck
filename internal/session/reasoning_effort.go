package session

import (
	"fmt"
	"slices"
	"strings"
)

var (
	claudeReasoningEfforts = []string{"low", "medium", "high", "xhigh", "max"}
	codexReasoningEfforts  = []string{"minimal", "low", "medium", "high", "xhigh"}
)

// LaunchReasoningEffortsForTool returns the supported per-session effort
// overrides in display order. An empty selection means the tool default.
func LaunchReasoningEffortsForTool(tool string) []string {
	var efforts []string
	switch {
	case IsClaudeCompatible(tool):
		efforts = claudeReasoningEfforts
	case IsCodexCompatible(tool):
		efforts = codexReasoningEfforts
	}
	return append([]string(nil), efforts...)
}

// SupportsLaunchReasoningEffort reports whether Agent Deck can pass a native
// per-session reasoning/effort override to the selected tool.
func SupportsLaunchReasoningEffort(tool string) bool {
	return len(LaunchReasoningEffortsForTool(tool)) > 0
}

// ValidateLaunchReasoningEffort checks a user-supplied override without
// mutating a session. Empty is always valid and means tool default.
func ValidateLaunchReasoningEffort(tool, effort string) error {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return nil
	}
	valid := LaunchReasoningEffortsForTool(tool)
	if len(valid) == 0 {
		return fmt.Errorf("reasoning effort is not supported for tool %q", tool)
	}
	if !slices.Contains(valid, effort) {
		return fmt.Errorf("invalid reasoning effort %q for %s (expected one of: %s)",
			effort, tool, strings.Join(valid, ", "))
	}
	return nil
}

// LaunchReasoningEffort returns the persisted per-session override. Empty
// means the underlying tool chooses its default.
func (i *Instance) LaunchReasoningEffort() string {
	if i == nil {
		return ""
	}
	switch {
	case IsClaudeCompatible(i.Tool):
		if opts := i.GetClaudeOptions(); opts != nil {
			return strings.TrimSpace(opts.Effort)
		}
	case IsCodexCompatible(i.Tool):
		if opts := i.GetCodexOptions(); opts != nil {
			return strings.TrimSpace(opts.ReasoningEffort)
		}
	}
	return ""
}

// ApplyLaunchReasoningEffort validates and persists a per-session override in
// the tool-specific options store. Empty clears the override.
func (i *Instance) ApplyLaunchReasoningEffort(effort string) error {
	if i == nil {
		return fmt.Errorf("cannot set reasoning effort on a nil session")
	}
	effort = strings.TrimSpace(effort)
	valid := LaunchReasoningEffortsForTool(i.Tool)
	if len(valid) == 0 {
		return fmt.Errorf("reasoning effort is not supported for tool %q", i.Tool)
	}
	if err := ValidateLaunchReasoningEffort(i.Tool, effort); err != nil {
		return err
	}

	switch {
	case IsClaudeCompatible(i.Tool):
		opts := i.GetClaudeOptions()
		if opts == nil {
			userConfig, _ := LoadUserConfig()
			opts = NewClaudeOptions(userConfig)
		}
		opts.Effort = effort
		return i.SetClaudeOptions(opts)
	case IsCodexCompatible(i.Tool):
		opts := i.GetCodexOptions()
		if opts == nil {
			userConfig, _ := LoadUserConfig()
			opts = NewCodexOptions(userConfig)
		}
		opts.ReasoningEffort = effort
		return i.SetCodexOptions(opts)
	default:
		return fmt.Errorf("reasoning effort is not supported for tool %q", i.Tool)
	}
}
