package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ResolveGroupClaudeHomeSkills returns the declarative skills that can safely
// live in the Claude home resolved for a group. Every group sharing one
// physical home must resolve the same skill set.
func ResolveGroupClaudeHomeSkills(groupPath string) (string, []string, error) {
	config, err := LoadUserConfig()
	if err != nil {
		return "", nil, fmt.Errorf("load config.toml: %w", err)
	}
	home, source := GetClaudeConfigDirSourceForGroup(groupPath)
	if config == nil {
		return home, nil, nil
	}
	if raw := rawClaudeHomeForGroup(config, groupPath, source); hasParentPathComponent(raw) {
		return "", nil, fmt.Errorf("group %q Claude config_dir %q contains parent traversal", groupPath, raw)
	}

	skills := config.GetGroupClaudeSkills(groupPath)
	if len(skills) > 0 && home == "" {
		return "", nil, fmt.Errorf("group %q has Claude skills but no config_dir", groupPath)
	}

	// A group with no [groups.X] block anywhere in its ancestor chain has made
	// no declaration about this home: it neither installs skills nor opts out
	// of them, so there is nothing for the divergence guard to protect. Only an
	// explicit `skills = []` is a contradiction worth rejecting. Treating the
	// two alike failed resolution for every registry group absent from
	// config.toml — including agent-deck's own "conductor" and "my-sessions" —
	// and failure here discards config.toml wholesale, dropping the session to
	// bare defaults instead of the shared home's configured loadout.
	if !config.hasGroupBlock(groupPath) {
		return home, skills, nil
	}

	paths := make([]string, 0, len(config.Groups))
	for path := range config.Groups {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if path == groupPath {
			continue
		}
		otherSkills := config.GetGroupClaudeSkills(path)
		otherHome, otherSource := GetClaudeConfigDirSourceForGroup(path)
		if raw := rawClaudeHomeForGroup(config, path, otherSource); hasParentPathComponent(raw) {
			continue
		}
		if !sameAgentHomePath(home, otherHome) {
			continue
		}
		if !sameStringSet(skills, otherSkills) {
			return "", nil, fmt.Errorf("groups %q and %q resolve different Claude skills into shared config_dir %q; standardize their skills or assign a distinct config_dir", groupPath, path, home)
		}
	}
	return home, skills, nil
}

// ResolveInstanceClaudeHomeSkills adds conductor declarations and verifies the
// final account/conductor/group/env/profile home selected for this instance.
func ResolveInstanceClaudeHomeSkills(inst *Instance) (string, []string, error) {
	if inst == nil || !IsClaudeCompatible(inst.Tool) {
		return "", nil, nil
	}
	config, err := LoadUserConfig()
	if err != nil {
		return "", nil, fmt.Errorf("load config.toml: %w", err)
	}
	groupHome, groupSkills, err := ResolveGroupClaudeHomeSkills(inst.GroupPath)
	if err != nil || config == nil {
		return groupHome, groupSkills, err
	}

	actualHome, source := GetClaudeConfigDirSourceForInstance(inst)
	if raw := rawClaudeHomeForInstance(config, inst, source); hasParentPathComponent(raw) {
		return "", nil, fmt.Errorf("Claude instance resolves config_dir %q with parent traversal", raw)
	}

	skills := append([]string(nil), groupSkills...)
	if conductorName := conductorNameFromInstance(inst); conductorName != "" {
		conductorSkills := config.GetConductorClaudeSkills(conductorName)
		skills = unionLoadoutEntries(skills, conductorSkills)
		if !sameStringSet(skills, groupSkills) {
			conductorCfg := config.Conductors[conductorName].Claude
			if conductorCfg.ConfigDir == "" {
				return "", nil, fmt.Errorf("conductor %q adds Claude skills while sharing group %q config_dir; set [conductors.%q.claude].config_dir to isolate them", conductorName, inst.GroupPath, conductorName)
			}
			if hasParentPathComponent(conductorCfg.ConfigDir) {
				return "", nil, fmt.Errorf("conductor %q Claude config_dir %q contains parent traversal", conductorName, conductorCfg.ConfigDir)
			}
			if !sameAgentHomePath(actualHome, conductorCfg.ConfigDir) {
				return "", nil, fmt.Errorf("conductor %q skills require config_dir %q but the instance resolves %q from %s", conductorName, ExpandPath(conductorCfg.ConfigDir), actualHome, source)
			}
			if sameAgentHomePath(actualHome, groupHome) {
				return "", nil, fmt.Errorf("conductor %q adds Claude skills but its config_dir %q shares group %q home; assign a physically distinct config_dir", conductorName, actualHome, inst.GroupPath)
			}
		}
	}

	if len(skills) == 0 {
		return actualHome, nil, nil
	}
	if actualHome == "" {
		return "", nil, fmt.Errorf("Claude instance in group %q has skills but no config_dir", inst.GroupPath)
	}
	if !sameAgentHomePath(actualHome, groupHome) && !allGroupClaudeSkillSetsSame(config) {
		return "", nil, fmt.Errorf("Claude instance in group %q resolves config_dir %q from %s instead of group home %q while configured group skill sets diverge", inst.GroupPath, actualHome, source, groupHome)
	}
	return actualHome, skills, nil
}

func allGroupClaudeSkillSetsSame(config *UserConfig) bool {
	var baseline []string
	haveBaseline := false
	for path := range config.Groups {
		skills := config.GetGroupClaudeSkills(path)
		if !haveBaseline {
			baseline = skills
			haveBaseline = true
			continue
		}
		if !sameStringSet(baseline, skills) {
			return false
		}
	}
	return true
}

func rawClaudeHomeForGroup(config *UserConfig, groupPath, source string) string {
	switch source {
	case "group":
		raw, _ := config.findGroupClaudeSetting(groupPath, func(s GroupClaudeSettings) string { return s.ConfigDir })
		return raw
	case "env":
		return os.Getenv("CLAUDE_CONFIG_DIR")
	case "profile":
		if profile, ok := config.Profiles[GetEffectiveProfile("")]; ok {
			return profile.Claude.ConfigDir
		}
	case "global":
		return config.Claude.ConfigDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func rawClaudeHomeForInstance(config *UserConfig, inst *Instance, source string) string {
	switch source {
	case "account":
		if profile, ok := config.Profiles[inst.Account]; ok {
			return profile.Claude.ConfigDir
		}
	case "conductor":
		return config.Conductors[conductorNameFromInstance(inst)].Claude.ConfigDir
	case "group":
		raw, _ := config.findGroupClaudeSetting(inst.GroupPath, func(s GroupClaudeSettings) string { return s.ConfigDir })
		return raw
	case "env":
		return os.Getenv("CLAUDE_CONFIG_DIR")
	case "profile":
		if profile, ok := config.Profiles[GetEffectiveProfile("")]; ok {
			return profile.Claude.ConfigDir
		}
	case "global":
		return config.Claude.ConfigDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}
