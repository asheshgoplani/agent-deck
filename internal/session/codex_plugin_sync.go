package session

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// SyncGroupCodexPlugins installs configured native plugins into the group's
// preselected CODEX_HOME. It is explicit: session startup never installs
// plugins or updates marketplaces.
func SyncGroupCodexPlugins(groupPath string) error {
	config, err := LoadUserConfig()
	if err != nil {
		return fmt.Errorf("load config.toml: %w", err)
	}
	if config == nil {
		return fmt.Errorf("config.toml is not configured")
	}
	plugins := config.GetGroupCodexPlugins(groupPath)
	if len(plugins) == 0 {
		return nil
	}
	codexHome := config.GetGroupCodexConfigDir(groupPath)
	if codexHome == "" {
		return fmt.Errorf("group %q has Codex plugins but no config_dir", groupPath)
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
	for _, plugin := range plugins {
		args := append(append([]string{}, argv[1:]...), "plugin", "add", plugin, "--json")
		cmd := exec.Command(argv[0], args...)
		cmd.Env = env
		output, runErr := cmd.CombinedOutput()
		if runErr != nil {
			detail := strings.TrimSpace(string(output))
			if detail != "" {
				return fmt.Errorf("install Codex plugin %q: %w: %s", plugin, runErr, detail)
			}
			return fmt.Errorf("install Codex plugin %q: %w", plugin, runErr)
		}
	}
	return nil
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
