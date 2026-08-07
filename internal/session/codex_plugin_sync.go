package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

var codexPluginSyncMu sync.Mutex

// ReconcileGroupCodexPlugins restores a group's declarative native plugin
// floor when CODEX_HOME/config.toml has lost an enabled plugin or its
// marketplace. Healthy homes are read-only and do not spawn the Codex CLI.
func ReconcileGroupCodexPlugins(groupPath, codexHome string) error {
	config, err := LoadUserConfig()
	if err != nil {
		return fmt.Errorf("load config.toml: %w", err)
	}
	if config == nil || len(config.GetGroupCodexPlugins(groupPath)) == 0 {
		return nil
	}

	codexPluginSyncMu.Lock()
	defer codexPluginSyncMu.Unlock()

	desired := config.GetGroupCodexPlugins(groupPath)
	healthy, err := groupCodexPluginsHealthy(filepath.Join(codexHome, "config.toml"), desired)
	if err != nil {
		return err
	}
	if healthy {
		return nil
	}
	return SyncGroupCodexPlugins(groupPath)
}

func groupCodexPluginsHealthy(configPath string, desired []string) (bool, error) {
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read Codex config %s: %w", configPath, err)
	}
	var cfg map[string]any
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return false, fmt.Errorf("refusing to reconcile unparseable Codex config %s: %w", configPath, err)
	}
	plugins, _ := cfg["plugins"].(map[string]any)
	marketplaces, _ := cfg["marketplaces"].(map[string]any)
	for _, plugin := range desired {
		entry, ok := plugins[plugin].(map[string]any)
		if !ok || entry["enabled"] != true {
			return false, nil
		}
		if at := strings.LastIndex(plugin, "@"); at >= 0 {
			if _, ok := marketplaces[plugin[at+1:]]; !ok {
				return false, nil
			}
		}
	}
	return true, nil
}

// SyncGroupCodexPlugins registers configured marketplaces and installs native
// plugins into the group's preselected CODEX_HOME. Operators can invoke it
// explicitly; startup also invokes it after detecting loadout drift.
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
	if len(marketplaces) == 0 && len(plugins) == 0 && !hasManagedCodexTUISettings(config.Codex.TUI) {
		return nil
	}
	codexHome := config.GetGroupCodexConfigDir(groupPath)
	if codexHome == "" {
		return fmt.Errorf("group %q has Codex provisioning but no config_dir", groupPath)
	}
	store, err := newHomeSkillStore(codexHome, "Codex")
	if err != nil {
		return fmt.Errorf("group %q Codex config_dir: %w", groupPath, err)
	}
	codexHome = store.home
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
