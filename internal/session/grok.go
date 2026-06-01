package session

import (
	"encoding/json"
	"strings"
)

// Grok adapter (xAI Grok Build CLI).
//
// `grok` from xAI (https://docs.x.ai/build/overview) is a Claude-Code-style
// interactive TUI. agent-deck integrates it at the same level as gemini/codex:
// launch the TUI in a tmux pane with optional env_file sourcing, an optional
// command override, a per-session model (`-m <model>`), and an opt-in
// `--always-approve` flag (Grok's auto-approve / yolo equivalent).
//
// Default model: grok-build. Resume (`-r/--resume <id>`) is intentionally NOT
// wired yet — a reliable Grok session-ID extraction path has not been verified
// (deferred; see PR notes). Status detection uses the busy/prompt patterns in
// internal/tmux/patterns.go (DefaultRawPatterns), captured from the real TUI.

// GrokOptions holds launch options for Grok Build CLI sessions.
type GrokOptions struct {
	// Model overrides the Grok model for this session (e.g., "grok-build").
	// Passed as `-m <model>`. Empty means the CLI's own default.
	Model string `json:"model,omitempty"`
	// YoloMode enables `--always-approve` (auto-approve all tool calls).
	// nil = inherit from config, true/false = explicit override.
	YoloMode *bool `json:"yolo_mode,omitempty"`
}

// ToolName returns "grok"
func (o *GrokOptions) ToolName() string {
	return "grok"
}

// ToArgs returns command-line arguments based on options.
func (o *GrokOptions) ToArgs() []string {
	var args []string
	if o.Model != "" {
		args = append(args, "-m", o.Model)
	}
	if o.YoloMode != nil && *o.YoloMode {
		args = append(args, "--always-approve")
	}
	return args
}

// NewGrokOptions creates GrokOptions with defaults from global config.
func NewGrokOptions(config *UserConfig) *GrokOptions {
	opts := &GrokOptions{}
	if config != nil {
		opts.Model = config.Grok.DefaultModel
		if config.Grok.YoloMode {
			yolo := true
			opts.YoloMode = &yolo
		}
	}
	return opts
}

// UnmarshalGrokOptions deserializes GrokOptions from the JSON wrapper.
func UnmarshalGrokOptions(data json.RawMessage) (*GrokOptions, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var wrapper ToolOptionsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}

	if wrapper.Tool != "grok" {
		return nil, nil
	}

	var opts GrokOptions
	if err := json.Unmarshal(wrapper.Options, &opts); err != nil {
		return nil, err
	}

	return &opts, nil
}

// GetGrokOptions returns Grok-specific options, or nil if not set.
func (i *Instance) GetGrokOptions() *GrokOptions {
	if len(i.ToolOptionsJSON) == 0 {
		return nil
	}
	opts, err := UnmarshalGrokOptions(i.ToolOptionsJSON)
	if err != nil {
		return nil
	}
	return opts
}

// SetGrokOptions stores Grok-specific options.
func (i *Instance) SetGrokOptions(opts *GrokOptions) error {
	if opts == nil {
		i.ToolOptionsJSON = nil
		return nil
	}
	data, err := MarshalToolOptions(opts)
	if err != nil {
		return err
	}
	i.ToolOptionsJSON = data
	return nil
}

// buildGrokCommand builds the launch command for the Grok Build CLI.
// Applies env sourcing, command override, and per-session/global flags
// (model, --always-approve). If baseCommand differs from the bare tool name
// "grok", it is treated as a user-supplied passthrough command and returned
// without flag injection — matching the buildGeminiCommand/buildCrushCommand
// pattern.
func (i *Instance) buildGrokCommand(baseCommand string) string {
	if i.Tool != "grok" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	// Passthrough: custom command from CLI (not the bare name).
	trimmed := strings.TrimSpace(baseCommand)
	if trimmed != "" && trimmed != "grok" {
		return envPrefix + trimmed
	}

	cmd := GetToolCommand("grok")

	// Per-session options take priority; any field left unset falls back to
	// the global [grok] config. Filling per-field (rather than only when the
	// whole wrapper is nil) means a partial override — e.g. `--yolo` alone via
	// applyCLIYoloOverride — still inherits default_model, regardless of the
	// order the options were written.
	config, _ := LoadUserConfig()
	defaults := NewGrokOptions(config)
	opts := i.GetGrokOptions()
	if opts == nil {
		opts = defaults
	} else {
		if opts.Model == "" {
			opts.Model = defaults.Model
		}
		if opts.YoloMode == nil {
			opts.YoloMode = defaults.YoloMode
		}
	}
	if args := opts.ToArgs(); len(args) > 0 {
		cmd += " " + strings.Join(args, " ")
	}

	return envPrefix + cmd
}
