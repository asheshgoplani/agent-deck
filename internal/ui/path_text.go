package ui

import "github.com/asheshgoplani/agent-deck/internal/session"

func userConfigPathForDisplay() string {
	path, err := session.GetUserConfigPath()
	if err != nil {
		return "$XDG_CONFIG_HOME/agent-deck/config.toml"
	}
	return path
}

func skillPoolPathForDisplay() string {
	path, err := session.GetSkillPoolPath()
	if err != nil {
		return "$XDG_CONFIG_HOME/agent-deck/skills/pool"
	}
	return path
}
