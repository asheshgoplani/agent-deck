package session

import (
	"strings"
)

// Muse adapter.
//
// Muse Code is an interactive terminal coding agent (the `muse` binary).
// The muse CLI is a single TUI binary:
//
//	muse                          # interactive TUI (blocks on a workspace-trust
//	                                prompt in a fresh directory)
//	muse --trust-workspace         # trust this workspace for this run (no prompt)
//	muse --yolo                     # disable approval + sandbox + trust workspace
//	muse resume <uuid> | --last     # resume a previous session
//	muse exec <prompt>              # headless single prompt
//	muse export --session <id>      # export a session transcript to JSON
//
// agent-deck integrates muse at the same level as crush: launch the TUI in
// a tmux pane with optional env_file sourcing, an optional command override
// (e.g. for a provider/model preset), and an optional --yolo flag from
// `[muse].yolo_mode`. Per-session flags flow through MuseOptions
// (ToolOptionsJSON) when wired by the UI.
//
// Status detection: content patterns in internal/tmux/patterns.go, captured
// live from Muse Code 1.0.2: busy `◈ Thinking (Ns · esc to interrupt)`,
// idle prompt `⟩` + "Type @ to search and insert workspace file paths".
// Session discovery lives in muse_discovery.go; restart() re-discovers the
// workspace's newest session and resumes it via buildMuseResumeCommand.

// defaultMuseCommand is the default invocation for Muse sessions.
// Bare `muse` blocks on "Do you trust this workspace?" in a fresh directory
// (captured live in a tmux pane), which would wedge every agent-deck spawn,
// so the default carries --trust-workspace. A [muse].command override
// replaces this default wholesale, flags included.
const defaultMuseCommand = "muse --trust-workspace"

// GetMuseCommand returns the configured muse command.
// Mirrors GetCrushCommand: prefer the user config override, fall back to
// the default invocation.
func GetMuseCommand() string {
	userConfig, _ := LoadUserConfig()
	if userConfig != nil && strings.TrimSpace(userConfig.Muse.Command) != "" {
		return strings.TrimSpace(userConfig.Muse.Command)
	}
	return defaultMuseCommand
}

// museYoloSuffix resolves the --yolo flag for muse launches from one place
// so fresh and resume builders agree. Per-session ToolOptionsJSON takes
// priority over global config when it unmarshals: an explicit YoloMode
// (true or false) is authoritative and the config is ignored; only when no
// usable per-session options exist does [muse].yolo_mode apply.
func museYoloSuffix(opts *MuseOptions, optsOK bool, config *UserConfig) string {
	if optsOK && opts != nil && opts.YoloMode != nil {
		// Explicit per-session override wins both ways: true forces
		// --yolo, false suppresses the global [muse].yolo_mode.
		if *opts.YoloMode {
			return " --yolo"
		}
		return ""
	}
	// No usable per-session value: absent, unparseable, or a serialized
	// default whose YoloMode is nil. Nil is not a choice, so inherit the
	// global config instead of silently dropping it.
	if config != nil && config.Muse.YoloMode {
		return " --yolo"
	}
	return ""
}

// museLaunchOptions unmarshals the instance's per-session muse options.
// The second return reports whether usable options exist (drives the
// config-fallback rule in museYoloSuffix).
func (i *Instance) museLaunchOptions() (*MuseOptions, bool) {
	if len(i.ToolOptionsJSON) == 0 {
		return nil, false
	}
	opts, err := UnmarshalMuseOptions(i.ToolOptionsJSON)
	if err != nil || opts == nil {
		return nil, false
	}
	return opts, true
}

// buildMuseCommand builds the launch command for Muse Code.
// Applies env sourcing, command override, and any per-session flags.
// If baseCommand differs from the bare tool name "muse", it is treated
// as a user-supplied passthrough command and returned without flag
// injection — matching the buildCrushCommand pattern.
func (i *Instance) buildMuseCommand(baseCommand string) string {
	if i.Tool != "muse" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()

	// Passthrough: custom command from CLI (not the bare name)
	trimmed := strings.TrimSpace(baseCommand)
	if trimmed != "" && trimmed != "muse" {
		return envPrefix + trimmed
	}

	cmd := GetMuseCommand()
	opts, optsOK := i.museLaunchOptions()
	config, _ := LoadUserConfig()
	return envPrefix + cmd + museYoloSuffix(opts, optsOK, config)
}

// buildMuseResumeCommand builds the launch command that resumes a known
// muse session: the base command plus the `resume <uuid>` subcommand (muse
// takes resume as a subcommand, not a flag; root options may appear on
// either side of it). An empty session ID falls back to a fresh launch
// rather than emitting a broken `resume` with no target.
//
// The base mirrors buildMuseCommand exactly: the instance's persisted
// command when it is a non-bare custom invocation (wrapper, provider or
// model flags), otherwise the configured default. A custom base is used
// verbatim with no flag injection, same as the fresh passthrough path, so
// a restart never silently drops the operator's explicit invocation.
func (i *Instance) buildMuseResumeCommand(sessionID string) string {
	if i.Tool != "muse" {
		return ""
	}
	if strings.TrimSpace(sessionID) == "" {
		return i.buildMuseCommand(i.Command)
	}
	envPrefix := i.buildEnvSourceCommand()
	base := GetMuseCommand()
	passthrough := false
	if trimmed := strings.TrimSpace(i.Command); trimmed != "" && trimmed != "muse" {
		base, passthrough = trimmed, true
	}
	cmd := base + " resume " + strings.TrimSpace(sessionID)
	if !passthrough {
		opts, optsOK := i.museLaunchOptions()
		config, _ := LoadUserConfig()
		cmd += museYoloSuffix(opts, optsOK, config)
	}
	return envPrefix + cmd
}

// discoverMuseResumeID returns the newest muse session ID bound to the
// instance's working directory, bounded by the instance's last start (so a
// restart cannot rebind to an older unrelated conversation), or "" when
// none qualifies.
func (i *Instance) discoverMuseResumeID() string {
	return FindLatestMuseSession(i.EffectiveWorkingDir(), i.LastStartedAt)
}
