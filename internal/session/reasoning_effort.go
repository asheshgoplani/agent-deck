package session

import (
	"fmt"
	"slices"
	"strings"
)

var (
	claudeReasoningEfforts = []string{"low", "medium", "high", "xhigh", "max"}
	codexReasoningEfforts  = []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"}
)

// LaunchReasoningEffortsForTool returns the supported per-session effort
// overrides in display order. An empty selection means the tool default. The
// static list comes first; efforts a probed CLI advertises for any of its
// models and the static list lacks are appended (#2388).
func LaunchReasoningEffortsForTool(tool string) []string {
	static := staticReasoningEffortsForTool(tool)
	if len(static) == 0 {
		return nil
	}
	probed := probedModelCatalog(modelProbeKind(tool))
	if probed == nil {
		return static
	}
	efforts := static
	for _, model := range probed.Models {
		efforts = mergeOrdered(efforts, probed.Efforts[model])
	}
	return efforts
}

// LaunchReasoningEffortsForModel narrows the tool's efforts to the ones the
// probed CLI advertises for model. An empty model, a model the probe does not
// describe, or no probe at all yields the tool-wide list.
func LaunchReasoningEffortsForModel(tool, model string) []string {
	efforts := LaunchReasoningEffortsForTool(tool)
	model = strings.TrimSpace(model)
	if len(efforts) == 0 || model == "" {
		return efforts
	}
	if probed := probedModelCatalog(modelProbeKind(tool)); probed != nil {
		if perModel := probed.Efforts[model]; len(perModel) > 0 {
			return append([]string(nil), perModel...)
		}
	}
	return efforts
}

// LaunchModelEffortsForTool returns the per-model effort lists a probe
// reported for the tool's listed models, or nil without a probe. UIs use it
// to narrow the effort choices once a model is picked.
func LaunchModelEffortsForTool(tool string) map[string][]string {
	if len(staticReasoningEffortsForTool(tool)) == 0 {
		return nil
	}
	probed := probedModelCatalog(modelProbeKind(tool))
	if probed == nil {
		return nil
	}
	// Only listed models: hidden ones still validate, but are not advertised.
	out := map[string][]string{}
	for _, model := range probed.Models {
		if efforts := probed.Efforts[model]; len(efforts) > 0 {
			out[model] = append([]string(nil), efforts...)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func staticReasoningEffortsForTool(tool string) []string {
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
	return ValidateLaunchReasoningEffortForModel(tool, "", effort)
}

// ValidateLaunchReasoningEffortForModel is ValidateLaunchReasoningEffort with
// the selected model taken into account: when the installed CLI was probed and
// describes that model, only that model's efforts are accepted.
func ValidateLaunchReasoningEffortForModel(tool, model, effort string) error {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return nil
	}
	valid := LaunchReasoningEffortsForModel(tool, model)
	if len(valid) == 0 {
		return fmt.Errorf("reasoning effort is not supported for tool %q", tool)
	}
	if !slices.Contains(valid, effort) {
		subject := tool
		if model = strings.TrimSpace(model); model != "" && !slices.Equal(valid, LaunchReasoningEffortsForTool(tool)) {
			subject = fmt.Sprintf("%s model %s", tool, model)
		}
		return fmt.Errorf("invalid reasoning effort %q for %s (expected one of: %s)",
			effort, subject, strings.Join(valid, ", "))
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
	if err := ValidateLaunchReasoningEffortForModel(i.Tool, i.LaunchModelID(), effort); err != nil {
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
