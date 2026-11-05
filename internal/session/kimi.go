package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"al.essio.dev/pkg/shellescape"
)

const (
	KimiPermissionDefault = ""
	KimiPermissionYolo    = "yolo"
	KimiPermissionAuto    = "auto"
)

func ValidateKimiPermissionMode(mode string) error {
	switch mode {
	case KimiPermissionDefault, KimiPermissionYolo, KimiPermissionAuto:
		return nil
	default:
		return fmt.Errorf("invalid Kimi permission mode %q (valid values: yolo, auto, or empty for Kimi default)", mode)
	}
}

// KimiOptions holds per-session launch options for Kimi Code CLI.
type KimiOptions struct {
	Model          string `json:"model,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
}

func (o *KimiOptions) ToolName() string { return "kimi" }

func (o *KimiOptions) ToArgs() []string {
	var args []string
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	switch o.PermissionMode {
	case KimiPermissionYolo:
		args = append(args, "--yolo")
	case KimiPermissionAuto:
		args = append(args, "--auto")
	}
	return args
}

func NewKimiOptions(config *UserConfig) *KimiOptions {
	opts := &KimiOptions{}
	if config != nil {
		opts.Model = config.Kimi.DefaultModel
		opts.PermissionMode = config.Kimi.PermissionMode
	}
	return opts
}

func UnmarshalKimiOptions(data json.RawMessage) (*KimiOptions, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var wrapper ToolOptionsWrapper
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, err
	}
	if wrapper.Tool != "kimi" {
		return nil, nil
	}
	var opts KimiOptions
	if err := json.Unmarshal(wrapper.Options, &opts); err != nil {
		return nil, err
	}
	return &opts, nil
}

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

	opts := i.GetKimiOptions()
	if i.KimiSessionID != "" {
		cmd += " --session " + shellescape.Quote(i.KimiSessionID)
	} else {
		model := ""
		if config != nil {
			model = config.Kimi.DefaultModel
		}
		if opts != nil && strings.TrimSpace(opts.Model) != "" {
			model = opts.Model
		}
		if model = strings.TrimSpace(model); model != "" {
			cmd += " --model " + shellescape.Quote(model)
		}
	}
	permissionMode := KimiPermissionDefault
	if config != nil {
		permissionMode = config.Kimi.PermissionMode
	}
	if opts != nil {
		permissionMode = opts.PermissionMode
	}
	switch permissionMode {
	case KimiPermissionYolo:
		cmd += " --yolo"
	case KimiPermissionAuto:
		cmd += " --auto"
	}
	return envPrefix + cmd
}
