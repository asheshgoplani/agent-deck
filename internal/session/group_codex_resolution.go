package session

// GroupCodexResolution is the resolved Codex configuration for a group.
// Source labels are "group:<path>", "global", "profile", "env", or "default".
type GroupCodexResolution struct {
	ConfigDir       string   `json:"config_dir,omitempty"`
	ConfigDirSource string   `json:"config_dir_source"`
	EnvFile         string   `json:"env_file,omitempty"`
	EnvFileSource   string   `json:"env_file_source,omitempty"`
	Command         string   `json:"command"`
	CommandSource   string   `json:"command_source"`
	Skills          []string `json:"skills,omitempty"`
	MCPs            []string `json:"mcps,omitempty"`
	Plugins         []string `json:"plugins,omitempty"`
	ConfigError     string   `json:"config_error,omitempty"`
}

// findGroupCodexSetting walks the group ancestor chain and returns the first
// non-empty scalar setting with the group that supplied it.
func (c *UserConfig) findGroupCodexSetting(groupPath string, get func(GroupCodexSettings) string) (value, matchedGroup string) {
	if c == nil || groupPath == "" || c.Groups == nil {
		return "", ""
	}
	for p := groupPath; p != ""; p = getParentPath(p) {
		if groupCfg, ok := c.Groups[p]; ok {
			if v := get(groupCfg.Codex); v != "" {
				return v, p
			}
		}
	}
	return "", ""
}

func (c *UserConfig) GetGroupCodexConfigDir(groupPath string) string {
	v, _ := c.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.ConfigDir })
	return ExpandPath(v)
}

func (c *UserConfig) GetGroupCodexEnvFile(groupPath string) string {
	v, _ := c.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.EnvFile })
	return v
}

func (c *UserConfig) GetGroupCodexCommand(groupPath string) string {
	v, _ := c.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.Command })
	return v
}

func (c *UserConfig) GetGroupCodexSkills(groupPath string) []string {
	return c.unionGroupCodexList(groupPath, func(s GroupCodexSettings) []string { return s.Skills })
}

func (c *UserConfig) GetGroupCodexMCPs(groupPath string) []string {
	return c.unionGroupCodexList(groupPath, func(s GroupCodexSettings) []string { return s.MCPs })
}

func (c *UserConfig) GetGroupCodexPlugins(groupPath string) []string {
	return c.unionGroupCodexList(groupPath, func(s GroupCodexSettings) []string { return s.Plugins })
}

func (c *UserConfig) unionGroupCodexList(groupPath string, get func(GroupCodexSettings) []string) []string {
	if c == nil || groupPath == "" || c.Groups == nil {
		return nil
	}
	var chain [][]string
	for p := groupPath; p != ""; p = getParentPath(p) {
		if groupCfg, ok := c.Groups[p]; ok {
			if entries := get(groupCfg.Codex); len(entries) > 0 {
				chain = append(chain, entries)
			}
		}
	}
	seen := make(map[string]bool)
	var union []string
	for idx := len(chain) - 1; idx >= 0; idx-- {
		for _, entry := range chain[idx] {
			if entry == "" || seen[entry] {
				continue
			}
			seen[entry] = true
			union = append(union, entry)
		}
	}
	return union
}

// ResolveGroupCodex resolves explicit group configuration. Global/profile/env
// fallbacks are added by the launch resolver, which has the active profile and
// actual process environment available.
func ResolveGroupCodex(groupPath string) GroupCodexResolution {
	res := GroupCodexResolution{Command: "codex", CommandSource: "default"}
	config, cfgErr := LoadUserConfig()
	if cfgErr != nil {
		res.ConfigError = cfgErr.Error()
		return res
	}
	if config == nil {
		return res
	}
	if configDir, matched := config.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.ConfigDir }); configDir != "" {
		res.ConfigDir = ExpandPath(configDir)
		res.ConfigDirSource = "group:" + matched
	}
	if envFile, matched := config.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.EnvFile }); envFile != "" {
		res.EnvFile = envFile
		res.EnvFileSource = "group:" + matched
	}
	if command, matched := config.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.Command }); command != "" {
		res.Command = command
		res.CommandSource = "group:" + matched
	}
	res.Skills = config.GetGroupCodexSkills(groupPath)
	res.MCPs = config.GetGroupCodexMCPs(groupPath)
	res.Plugins = config.GetGroupCodexPlugins(groupPath)
	return res
}
