package session

import (
	"encoding/json"
)

// ToolOptions is the interface for tool-specific launch options
// Each AI tool (claude, codex, gemini, etc.) can have its own options struct
// that implements this interface
type ToolOptions interface {
	// ToolName returns the name of the tool (e.g., "claude", "codex")
	ToolName() string
	// ToArgs returns command-line arguments for the tool
	ToArgs() []string
}

// ClaudeOptions holds launch options for Claude Code sessions
type ClaudeOptions struct {
	// SessionMode: "new" (default), "continue" (-c), or "resume" (-r)
	SessionMode string `json:"session_mode,omitempty"`
	// ResumeSessionID is the session ID for -r flag (only when SessionMode="resume")
	ResumeSessionID string `json:"resume_session_id,omitempty"`
	// SkipPermissions adds --dangerously-skip-permissions flag
	SkipPermissions bool `json:"skip_permissions,omitempty"`
	// UseChrome adds --chrome flag
	UseChrome bool `json:"use_chrome,omitempty"`

	// Transient fields for worktree fork (not persisted)
	WorkDir          string `json:"-"`
	WorktreePath     string `json:"-"`
	WorktreeRepoRoot string `json:"-"`
	WorktreeBranch   string `json:"-"`
}

// ToolName returns "claude"
func (o *ClaudeOptions) ToolName() string {
	return "claude"
}

// ToArgs returns command-line arguments based on options
func (o *ClaudeOptions) ToArgs() []string {
	var args []string

	// Session mode flags (mutually exclusive)
	switch o.SessionMode {
	case "continue":
		args = append(args, "-c")
	case "resume":
		if o.ResumeSessionID != "" {
			args = append(args, "--resume", o.ResumeSessionID)
		}
	}
	// "new" or empty = default behavior, no special flag

	// Boolean flags
	if o.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if o.UseChrome {
		args = append(args, "--chrome")
	}

	return args
}

// ToArgsForFork returns arguments suitable for fork resume command
// Fork always uses --resume internally, so session mode flags are not included
func (o *ClaudeOptions) ToArgsForFork() []string {
	var args []string

	if o.SkipPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}
	if o.UseChrome {
		args = append(args, "--chrome")
	}

	return args
}

// NewClaudeOptions creates ClaudeOptions with defaults from config
func NewClaudeOptions(config *UserConfig) *ClaudeOptions {
	opts := &ClaudeOptions{
		SessionMode: "new",
	}
	if config != nil {
		opts.SkipPermissions = config.Claude.GetDangerousMode()
	}
	return opts
}

// CodexOptions holds launch options for Codex CLI sessions
type CodexOptions struct {
	// YoloMode enables --yolo flag (bypass approvals and sandbox)
	// nil = inherit from global config, true/false = explicit override
	YoloMode *bool `json:"yolo_mode,omitempty"`
}

// ToolName returns "codex"
func (o *CodexOptions) ToolName() string {
	return "codex"
}

// ToArgs returns command-line arguments based on options
func (o *CodexOptions) ToArgs() []string {
	var args []string
	if o.YoloMode != nil && *o.YoloMode {
		args = append(args, "--yolo")
	}
	return args
}

// NewCodexOptions creates CodexOptions with defaults from global config
func NewCodexOptions(config *UserConfig) *CodexOptions {
	opts := &CodexOptions{}
	if config != nil && config.Codex.YoloMode {
		yolo := true
		opts.YoloMode = &yolo
	}
	return opts
}

// UnmarshalCodexOptions deserializes CodexOptions from JSON wrapper
func UnmarshalCodexOptions(data json.RawMessage) (*CodexOptions, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var wrapper ToolOptionsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}

	if wrapper.Tool != "codex" {
		return nil, nil
	}

	var opts CodexOptions
	if err := json.Unmarshal(wrapper.Options, &opts); err != nil {
		return nil, err
	}

	return &opts, nil
}

// ToolOptionsWrapper wraps tool options for JSON serialization
// JSON structure: {"tool": "claude", "options": {...}}
type ToolOptionsWrapper struct {
	Tool    string          `json:"tool"`
	Options json.RawMessage `json:"options"`
}

// MarshalToolOptions serializes tool options to JSON
func MarshalToolOptions(opts ToolOptions) (json.RawMessage, error) {
	if opts == nil {
		return nil, nil
	}

	optBytes, err := json.Marshal(opts)
	if err != nil {
		return nil, err
	}

	wrapper := ToolOptionsWrapper{
		Tool:    opts.ToolName(),
		Options: optBytes,
	}

	return json.Marshal(wrapper)
}

// UnmarshalClaudeOptions deserializes ClaudeOptions from JSON wrapper
func UnmarshalClaudeOptions(data json.RawMessage) (*ClaudeOptions, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var wrapper ToolOptionsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}

	if wrapper.Tool != "claude" {
		return nil, nil
	}

	var opts ClaudeOptions
	if err := json.Unmarshal(wrapper.Options, &opts); err != nil {
		return nil, err
	}

	return &opts, nil
}
