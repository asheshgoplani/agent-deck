package session

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// SyncGroupCodexPlugins registers configured marketplaces and installs native
// plugins into the group's preselected CODEX_HOME. It is explicit: session
// startup never mutates plugins or marketplaces.
func SyncGroupCodexPlugins(groupPath string) error {
	config, err := LoadUserConfig()
	if err != nil {
		return fmt.Errorf("load config.toml: %w", err)
	}
	if config == nil {
		return fmt.Errorf("config.toml is not configured")
	}
	marketplaces := config.GetGroupCodexMarketplaces(groupPath)
	plugins := config.GetGroupCodexPlugins(groupPath)
	if len(marketplaces) == 0 && len(plugins) == 0 && config.Codex.TUI == nil {
		return nil
	}
	codexHome := config.GetGroupCodexConfigDir(groupPath)
	if codexHome == "" {
		return fmt.Errorf("group %q has Codex provisioning but no config_dir", groupPath)
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return fmt.Errorf("create group Codex home %q: %w", codexHome, err)
	}
	if err := ApplyCodexTUISettings(codexHome, config.Codex.TUI); err != nil {
		return fmt.Errorf("apply Codex TUI defaults: %w", err)
	}
	if len(marketplaces) == 0 && len(plugins) == 0 {
		return nil
	}

	command := config.GetGroupCodexCommand(groupPath)
	if command == "" {
		command = GetCodexCommand()
	}
	argv, err := splitCodexPluginCommand(command)
	if err != nil {
		return err
	}
	env := filteredCodexHomeEnv(os.Environ())
	env = append(env, "CODEX_HOME="+codexHome)
	for _, marketplace := range marketplaces {
		if err := runCodexSyncCommand(argv, env, "plugin", "marketplace", "add", marketplace, "--json"); err != nil {
			return fmt.Errorf("register Codex marketplace %q: %w", marketplace, err)
		}
	}
	for _, plugin := range plugins {
		if err := runCodexSyncCommand(argv, env, "plugin", "add", plugin, "--json"); err != nil {
			return fmt.Errorf("install Codex plugin %q: %w", plugin, err)
		}
	}
	return nil
}

func runCodexSyncCommand(argv, env []string, subcommand ...string) error {
	args := append(append([]string{}, argv[1:]...), subcommand...)
	cmd := exec.Command(argv[0], args...)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if detail := strings.TrimSpace(string(output)); detail != "" {
		return fmt.Errorf("%w: %s", err, detail)
	}
	return err
}

func splitCodexPluginCommand(command string) ([]string, error) {
	var argv []string
	rest := strings.TrimSpace(command)
	for rest != "" {
		word, remainder, ok := nextShellWord(rest)
		if !ok || word == "" {
			return nil, fmt.Errorf("Codex plugin sync requires a simple command, got %q", command)
		}
		if isShellEnvAssignment(word) {
			return nil, fmt.Errorf("Codex plugin sync does not accept environment assignments in command %q", command)
		}
		argv = append(argv, word)
		rest = strings.TrimSpace(remainder)
	}
	return argv, nil
}

func filteredCodexHomeEnv(env []string) []string {
	filtered := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, "CODEX_HOME=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
