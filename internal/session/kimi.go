package session

import (
	"fmt"
	"strings"

	"al.essio.dev/pkg/shellescape"
)

func (i *Instance) buildKimiCommand(baseCommand string) string {
	if i.Tool != "kimi" {
		return baseCommand
	}

	envPrefix := i.buildEnvSourceCommand()
	if i.HermesHookGeneration != "" {
		envPrefix += "export AGENTDECK_HOOK_GENERATION=" + shellescape.Quote(i.HermesHookGeneration) + "; "
	}
	config, _ := LoadUserConfig()
	if config != nil && strings.TrimSpace(config.Kimi.ConfigDir) != "" {
		envPrefix += "KIMI_CODE_HOME=" + shellescape.Quote(ExpandPath(strings.TrimSpace(config.Kimi.ConfigDir))) + " "
	}
	envPrefix += fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_TITLE=%s AGENTDECK_TOOL=%s AGENTDECK_PROFILE=%s ",
		shellescape.Quote(i.ID), shellescape.Quote(i.Title), i.Tool, shellescape.Quote(sessionProfileEnvValue()))

	cmd := GetToolCommand("kimi")
	if (baseCommand != "kimi" && baseCommand != "") || cmd != "kimi" {
		if baseCommand != "kimi" && baseCommand != "" {
			cmd = baseCommand
		}
		return envPrefix + cmd
	}

	if i.KimiSessionID != "" {
		cmd += " --session " + shellescape.Quote(i.KimiSessionID)
	} else if config != nil && config.Kimi.DefaultModel != "" {
		cmd += " --model " + shellescape.Quote(config.Kimi.DefaultModel)
	}
	if config != nil && config.Kimi.YoloMode {
		cmd += " --yolo"
	}
	return envPrefix + cmd
}
