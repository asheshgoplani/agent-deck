package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// GroupCodexResolution is the resolved Codex configuration for a group.
// Source labels are "group:<path>", "global", "profile", "env", or "default".
type GroupCodexResolution struct {
	ConfigDir       string   `json:"config_dir,omitempty"`
	ConfigDirSource string   `json:"config_dir_source"`
	EnvFile         string   `json:"env_file,omitempty"`
	EnvFileSource   string   `json:"env_file_source,omitempty"`
	Command         string   `json:"command"`
	CommandSource   string   `json:"command_source"`
	Model           string   `json:"model,omitempty"`
	ModelSource     string   `json:"model_source,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	ReasoningSource string   `json:"reasoning_effort_source,omitempty"`
	Skills          []string `json:"skills,omitempty"`
	MCPs            []string `json:"mcps,omitempty"`
	Marketplaces    []string `json:"marketplaces,omitempty"`
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

func (c *UserConfig) GetGroupCodexModel(groupPath string) string {
	v, _ := c.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.Model })
	return v
}

func (c *UserConfig) GetGroupCodexReasoningEffort(groupPath string) string {
	v, _ := c.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.ReasoningEffort })
	return v
}

func (c *UserConfig) GetGroupCodexSkills(groupPath string) []string {
	return c.unionGroupCodexList(groupPath, func(s GroupCodexSettings) []string { return s.Skills })
}

// ResolveGroupCodexHomeSkills returns the declarative skills that can safely
// live in the group's resolved CODEX_HOME. Descendant-only skills require a
// distinct config_dir so they cannot leak into siblings sharing the home.
func ResolveGroupCodexHomeSkills(groupPath string) (string, []string, error) {
	config, err := LoadUserConfig()
	if err != nil {
		return "", nil, fmt.Errorf("load config.toml: %w", err)
	}
	if config == nil {
		return "", nil, nil
	}
	homeValue, owner := config.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.ConfigDir })
	if hasParentPathComponent(homeValue) {
		return "", nil, fmt.Errorf("group %q Codex config_dir %q contains parent traversal", owner, homeValue)
	}
	home := ExpandPath(homeValue)
	effectiveSkills := config.GetGroupCodexSkills(groupPath)
	if len(effectiveSkills) == 0 {
		return home, nil, nil
	}
	if home == "" {
		// Only the global [codex].skills floor contributed here — no group in
		// the chain asked for these skills, so there is no per-group config_dir
		// the user could add to satisfy the requirement. Materializing into the
		// shared default home would leak a floor across every ungrouped
		// session, so skip quietly instead of failing the spawn. A group that
		// declares its own skills still gets the actionable error below.
		if !config.anyGroupInChainDeclaresCodexSkills(groupPath) {
			return "", nil, nil
		}
		return "", nil, fmt.Errorf("group %q has Codex skills but no config_dir", groupPath)
	}

	for path := groupPath; path != "" && path != owner; path = getParentPath(path) {
		if group, ok := config.Groups[path]; ok && len(group.Codex.Skills) > 0 {
			return "", nil, fmt.Errorf("group %q declares Codex skills but inherits config_dir from %q; set [groups.%q.codex].config_dir to isolate them", path, owner, path)
		}
	}
	homeSkills := config.GetGroupCodexSkills(owner)

	paths := make([]string, 0, len(config.Groups))
	for path := range config.Groups {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if path == owner {
			continue
		}
		group := config.Groups[path]
		if group.Codex.ConfigDir == "" || hasParentPathComponent(group.Codex.ConfigDir) || !sameAgentHomePath(group.Codex.ConfigDir, home) {
			continue
		}
		otherSkills := config.GetGroupCodexSkills(path)
		if !sameStringSet(homeSkills, otherSkills) {
			return "", nil, fmt.Errorf("groups %q and %q resolve different Codex skills into shared config_dir %q", owner, path, home)
		}
	}
	return home, homeSkills, nil
}

// ResolveInstanceCodexHome returns the active home selected by the final Codex
// command and verifies it against an explicit group home independently of
// whether that group declares skills.
func ResolveInstanceCodexHome(inst *Instance) (string, error) {
	if inst == nil || !IsCodexCompatible(inst.Tool) {
		return "", nil
	}
	actualHome := inst.getCodexHomeDir()
	if hasParentPathComponent(actualHome) {
		return "", fmt.Errorf("Codex command resolves CODEX_HOME %q with parent traversal", actualHome)
	}
	store, err := newHomeSkillStore(actualHome, "Codex")
	if err != nil {
		return "", err
	}

	config, err := LoadUserConfig()
	if err != nil {
		return "", fmt.Errorf("load config.toml: %w", err)
	}
	if config == nil {
		return store.home, nil
	}
	configuredValue, owner := config.findGroupCodexSetting(inst.GroupPath, func(s GroupCodexSettings) string { return s.ConfigDir })
	if hasParentPathComponent(configuredValue) {
		return "", fmt.Errorf("group %q Codex config_dir %q contains parent traversal", owner, configuredValue)
	}
	configuredHome := ExpandPath(configuredValue)
	if configuredHome != "" && !sameAgentHomePath(store.home, configuredHome) {
		return "", fmt.Errorf("Codex command resolves CODEX_HOME %q but group %q config_dir resolves %q", actualHome, inst.GroupPath, configuredHome)
	}
	return store.home, nil
}

// ResolveInstanceCodexHomeSkills verifies that the home selected by the final
// Codex command matches the declarative group home before provisioning skills.
func ResolveInstanceCodexHomeSkills(inst *Instance) (string, []string, error) {
	actualHome, err := ResolveInstanceCodexHome(inst)
	if err != nil {
		return "", nil, err
	}
	_, skills, err := ResolveGroupCodexHomeSkills(inst.GroupPath)
	if err != nil {
		return "", nil, err
	}
	return actualHome, skills, nil
}

func hasParentPathComponent(path string) bool {
	for _, component := range strings.FieldsFunc(os.ExpandEnv(path), func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if component == ".." {
			return true
		}
	}
	return false
}

func sameAgentHomePath(left, right string) bool {
	leftCanonical := canonicalAgentHomePath(left)
	rightCanonical := canonicalAgentHomePath(right)
	if leftCanonical == rightCanonical {
		return true
	}

	leftCurrent, rightCurrent := leftCanonical, rightCanonical
	var leftSuffix, rightSuffix []string
	for {
		leftInfo, leftErr := os.Stat(leftCurrent)
		rightInfo, rightErr := os.Stat(rightCurrent)
		if leftErr == nil && rightErr == nil {
			if !os.SameFile(leftInfo, rightInfo) {
				return false
			}
			return strings.EqualFold(filepath.Join(leftSuffix...), filepath.Join(rightSuffix...))
		}

		leftParent := filepath.Dir(leftCurrent)
		rightParent := filepath.Dir(rightCurrent)
		if leftParent == leftCurrent || rightParent == rightCurrent {
			return false
		}
		leftSuffix = append([]string{filepath.Base(leftCurrent)}, leftSuffix...)
		rightSuffix = append([]string{filepath.Base(rightCurrent)}, rightSuffix...)
		leftCurrent, rightCurrent = leftParent, rightParent
	}
}

// canonicalAgentHomePath resolves symlinks in the longest existing prefix so
// aliases remain comparable even when the final agent home does not exist yet.
func canonicalAgentHomePath(path string) string {
	clean := filepath.Clean(ExpandPath(path))
	if absolute, err := filepath.Abs(clean); err == nil {
		clean = absolute
	}

	current := clean
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved)
		} else if !os.IsNotExist(err) {
			return clean
		}

		parent := filepath.Dir(current)
		if parent == current {
			return clean
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := values[value]; !ok {
			return false
		}
	}
	return true
}

func (c *UserConfig) GetGroupCodexMCPs(groupPath string) []string {
	return c.unionGroupCodexList(groupPath, func(s GroupCodexSettings) []string { return s.MCPs })
}

func (c *UserConfig) GetGroupCodexMarketplaces(groupPath string) []string {
	return c.unionGroupCodexList(groupPath, func(s GroupCodexSettings) []string { return s.Marketplaces })
}

func (c *UserConfig) GetGroupCodexPlugins(groupPath string) []string {
	return c.unionGroupCodexList(groupPath, func(s GroupCodexSettings) []string { return s.Plugins })
}

// anyGroupInChainDeclaresCodexSkills reports whether groupPath or any ancestor
// declares [groups.X.codex].skills of its own. Distinguishes "this group asked
// for skills" from "only the global [codex].skills floor applies", which the
// no-config_dir path needs to keep its error actionable.
func (c *UserConfig) anyGroupInChainDeclaresCodexSkills(groupPath string) bool {
	if c == nil || c.Groups == nil {
		return false
	}
	for p := groupPath; p != ""; p = getParentPath(p) {
		if group, ok := c.Groups[p]; ok && len(group.Codex.Skills) > 0 {
			return true
		}
	}
	return false
}

func (c *UserConfig) unionGroupCodexList(groupPath string, get func(GroupCodexSettings) []string) []string {
	if c == nil {
		return nil
	}
	var chain [][]string
	if c.Groups != nil {
		for p := groupPath; p != ""; p = getParentPath(p) {
			if groupCfg, ok := c.Groups[p]; ok {
				if entries := get(groupCfg.Codex); len(entries) > 0 {
					chain = append(chain, entries)
				}
			}
		}
	}
	// The global [codex] loadout is the outermost ancestor: appended last so
	// the root-first walk below emits it before any group entry. A session
	// with no group still inherits it — that is the point of a global floor.
	if entries := get(c.Codex.GroupLoadout()); len(entries) > 0 {
		chain = append(chain, entries)
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
	if model, matched := config.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.Model }); model != "" {
		res.Model = model
		res.ModelSource = "group:" + matched
	}
	if effort, matched := config.findGroupCodexSetting(groupPath, func(s GroupCodexSettings) string { return s.ReasoningEffort }); effort != "" {
		res.ReasoningEffort = effort
		res.ReasoningSource = "group:" + matched
	}
	_, skills, skillsErr := ResolveGroupCodexHomeSkills(groupPath)
	if skillsErr != nil {
		res.ConfigError = skillsErr.Error()
	} else {
		res.Skills = skills
	}
	res.MCPs = config.GetGroupCodexMCPs(groupPath)
	res.Marketplaces = config.GetGroupCodexMarketplaces(groupPath)
	res.Plugins = config.GetGroupCodexPlugins(groupPath)
	return res
}
