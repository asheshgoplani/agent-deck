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
	// AppendSystemPrompt adds --append-system-prompt flag with value
	AppendSystemPrompt string `json:"append_system_prompt,omitempty"`
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

	// String flags
	if o.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", o.AppendSystemPrompt)
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
	if o.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", o.AppendSystemPrompt)
	}

	return args
}

// NewClaudeOptions creates ClaudeOptions with defaults from config
func NewClaudeOptions(config *UserConfig) *ClaudeOptions {
	opts := &ClaudeOptions{
		SessionMode: "new",
	}
	if config != nil {
		opts.SkipPermissions = config.Claude.DangerousMode
	}
	return opts
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
