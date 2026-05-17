package session

import (
	"strings"
)

// Crush adapter (Issue #940).
//
// charmbracelet/crush is an interactive terminal AI assistant
// (github.com/charmbracelet/crush). The crush CLI is a single TUI binary:
//
//	crush                    # interactive TUI
//	crush --yolo             # auto-accept all permissions
//	crush --session <id>     # resume a specific session
//	crush --continue         # resume the most recent session
//	crush --cwd <path>       # set working directory
//	crush --debug            # enable debug logging
//
// agent-deck integrates crush at the same level as copilot/hermes: launch
// the TUI in a tmux pane with optional env_file sourcing, an optional
// command override (e.g., for a wrapper script), and an optional --yolo
// flag from `[crush].yolo_mode`. Per-session resume IDs flow through
// CrushOptions (ToolOptionsJSON) when wired by the UI.
//
// Status detection: process-alive/dead only at this stage. Content-sniffing
// patterns live in internal/tmux/tmux.go and will refine the "running"
// signal once Crush's TUI strings stabilise.

// GetCrushCommand returns the configured crush command/alias.
// Mirrors GetCodexCommand on main: prefer the user config override, fall
// back to the bare binary name.
func GetCrushCommand() string {
	userConfig, _ := LoadUserConfig()
	if userConfig != nil && strings.TrimSpace(userConfig.Crush.Command) != "" {
		return strings.TrimSpace(userConfig.Crush.Command)
	}
	return "crush"
}

// buildCrushCommand builds the launch command for charmbracelet/crush.
// Applies env sourcing, command override, and any per-session flags.
// If baseCommand differs from the bare tool name "crush", it is treated
// as a user-supplied passthrough command and returned without flag
// injection — matching the buildCopilotCommand pattern on main.
func (i *Instance) buildCrushCommand(baseCommand string) string {
	if i.Tool != "crush" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	// Passthrough: custom command from CLI (not the bare name)
	trimmed := strings.TrimSpace(baseCommand)
	if trimmed != "" && trimmed != "crush" {
		return envPrefix + trimmed
	}

	cmd := GetCrushCommand()

	// Per-session flags from ToolOptionsJSON take priority over global
	// config. Falls back to [crush].yolo_mode for --yolo when no per-session
	// override is present.
	if len(i.ToolOptionsJSON) > 0 {
		if opts, err := UnmarshalCrushOptions(i.ToolOptionsJSON); err == nil && opts != nil {
			args := opts.ToArgs()
			if len(args) > 0 {
				cmd += " " + strings.Join(args, " ")
			}
			return envPrefix + cmd
		}
	}

	config, _ := LoadUserConfig()
	if config != nil && config.Crush.YoloMode {
		cmd += " --yolo"
	}

	return envPrefix + cmd
}
