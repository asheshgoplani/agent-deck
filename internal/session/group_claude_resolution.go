package session

// GroupClaudeResolution is the resolved view of the effective Claude
// configuration for a group path — what a session created in that group
// would actually launch with. Built for `agent-deck group show --resolved`
// so a misconfigured stanza is diagnosable: a typo'd group key, a TOML
// parse error, or a missing env_file are otherwise indistinguishable at
// launch (the spawn silently proceeds on defaults).
//
// Source labels: "group:<path>" (the ancestor that matched), "global",
// "env", "profile", "default", or "" when unset.
type GroupClaudeResolution struct {
	ConfigDir       string `json:"config_dir,omitempty"`
	ConfigDirSource string `json:"config_dir_source"`

	EnvFile         string `json:"env_file,omitempty"`
	EnvFileSource   string `json:"env_file_source,omitempty"`
	EnvFileResolved string `json:"env_file_resolved,omitempty"`
	// EnvFileExists is meaningful only when EnvFileResolved is absolute;
	// a relative env_file resolves against each session's working dir.
	EnvFileExists bool `json:"env_file_exists"`

	Command       string `json:"command"`
	CommandSource string `json:"command_source"`

	Model       string `json:"model,omitempty"`
	ModelSource string `json:"model_source,omitempty"`

	Env     map[string]string `json:"env,omitempty"`
	Plugins []string          `json:"plugins,omitempty"`
	MCPs    []string          `json:"mcps,omitempty"`

	// ConfigError carries the config.toml load error verbatim when the
	// file failed to parse — in that state every value above is a default
	// and the stanza the user is debugging is NOT in effect.
	ConfigError string `json:"config_error,omitempty"`
}

// ResolveGroupClaude resolves the effective Claude settings for a group
// path using the same chains the spawn builders use (group ancestor-walk →
// global → default; env beats group for config_dir on the no-instance
// chain). Conductor-level overrides are per-session and therefore not part
// of a group view.
func ResolveGroupClaude(groupPath string) GroupClaudeResolution {
	res := GroupClaudeResolution{}

	config, cfgErr := LoadUserConfig()
	if cfgErr != nil {
		res.ConfigError = cfgErr.Error()
	}

	// config_dir — reuse the canonical resolver (#881 single source of truth).
	configDir, configDirSource := GetClaudeConfigDirSourceForGroup(groupPath)
	res.ConfigDir = configDir
	res.ConfigDirSource = configDirSource
	if configDirSource == "group" && config != nil {
		if _, matched := config.findGroupClaudeSetting(groupPath, func(s GroupClaudeSettings) string { return s.ConfigDir }); matched != "" {
			res.ConfigDirSource = "group:" + matched
		}
	}

	if config == nil {
		res.Command = "claude"
		res.CommandSource = "default"
		return res
	}

	// env_file — group chain then global, matching getToolEnvFile's claude branch.
	if envFile, matched := config.findGroupClaudeSetting(groupPath, func(s GroupClaudeSettings) string { return s.EnvFile }); envFile != "" {
		res.EnvFile = envFile
		res.EnvFileSource = "group:" + matched
	} else if config.Claude.EnvFile != "" {
		res.EnvFile = config.Claude.EnvFile
		res.EnvFileSource = "global"
	}
	if res.EnvFile != "" {
		res.EnvFileResolved = ExpandPath(res.EnvFile)
		// Route the existence probe through statEnvFileProbe — the same
		// fail-closed, boundary-aware, symlink-resolving guard (with the
		// sink-local traversal barrier) as the spawn-time env_file probe.
		// os.Stat follows symlinks, so a lexically in-home env_file that is a
		// symlink to an out-of-root file must not probe outside the home root
		// from this diagnostic path (CodeQL go/path-injection — second sink).
		// There is no session project in a group-level view, so the operator
		// home is the only probe root; an absolute env_file outside it (or
		// escaping via symlink, or never probed) leaves EnvFileExists false.
		_, res.EnvFileExists, _ = statEnvFileProbe(res.EnvFileResolved, "")
	}

	// command — group chain → global [claude].command → "claude".
	if cmd, matched := config.findGroupClaudeSetting(groupPath, func(s GroupClaudeSettings) string { return s.Command }); cmd != "" {
		res.Command = cmd
		res.CommandSource = "group:" + matched
	} else if config.Claude.Command != "" {
		res.Command = config.Claude.Command
		res.CommandSource = "global"
	} else {
		res.Command = "claude"
		res.CommandSource = "default"
	}

	// model — group chain only; the global default_model stays a
	// new-session-dialog prefill (#1172), not a launch default.
	if model, matched := config.findGroupClaudeSetting(groupPath, func(s GroupClaudeSettings) string { return s.Model }); model != "" {
		res.Model = model
		res.ModelSource = "group:" + matched
	}

	res.Env = config.GetGroupClaudeEnv(groupPath)
	res.Plugins = config.GetGroupClaudePlugins(groupPath)
	res.MCPs = config.GetGroupClaudeMCPs(groupPath)

	return res
}
