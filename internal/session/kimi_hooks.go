package session

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

const (
	agentDeckKimiHookCommand        = "agent-deck hook-handler"
	agentDeckKimiPreToolHookCommand = PreToolHookSyncMarkerEnv + "=1 " + agentDeckKimiHookCommand
)

var kimiHookEvents = []string{
	"SessionStart",
	"TurnStarted",
	"UserPromptSubmit",
	"PreToolUse",
	"PostToolUse",
	"PostToolUseFailure",
	"Stop",
	"Interrupt",
	"PermissionRequest",
	"StopFailure",
	"SessionEnd",
}

func KimiHookEventsForInstall() []string {
	return append([]string(nil), kimiHookEvents...)
}

func kimiHookCommandForEvent(event string) string {
	if event == "PreToolUse" {
		return agentDeckKimiPreToolHookCommand
	}
	return agentDeckKimiHookCommand
}

func isAgentDeckKimiHookCommand(command string) bool {
	command = strings.TrimSpace(command)
	return command == agentDeckKimiHookCommand || command == agentDeckKimiPreToolHookCommand
}

func InjectKimiHooks(configDir string) (bool, error) {
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return false, fmt.Errorf("create Kimi config dir: %w", err)
	}
	lock, err := AcquireConfigFileLock(configPath)
	if err != nil {
		return false, fmt.Errorf("acquire Kimi config lock: %w", err)
	}
	defer lock.Release()

	raw, err := readKimiConfig(configPath)
	if err != nil {
		return false, err
	}
	hooks, err := kimiConfigHooks(raw)
	if err != nil {
		return false, err
	}
	seen := make(map[string]int, len(kimiHookEvents))
	updated := make([]any, 0, len(hooks)+len(kimiHookEvents))
	changed := false
	for _, value := range hooks {
		hook, ok := value.(map[string]any)
		if !ok {
			updated = append(updated, value)
			continue
		}
		event, _ := hook["event"].(string)
		command, _ := hook["command"].(string)
		if !isAgentDeckKimiHookCommand(command) || !slices.Contains(kimiHookEvents, event) {
			updated = append(updated, value)
			continue
		}
		if _, duplicate := seen[event]; duplicate {
			changed = true
			continue
		}
		wantCommand := kimiHookCommandForEvent(event)
		if command != wantCommand {
			hook["command"] = wantCommand
			changed = true
		}
		seen[event] = len(updated)
		updated = append(updated, value)
	}
	for _, event := range kimiHookEvents {
		if _, ok := seen[event]; !ok {
			updated = append(updated, map[string]any{
				"event":   event,
				"command": kimiHookCommandForEvent(event),
			})
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	raw["hooks"] = updated
	if err := writeKimiConfig(configPath, raw); err != nil {
		return false, err
	}
	return true, nil
}

func RemoveKimiHooks(configDir string) (bool, error) {
	configPath := filepath.Join(configDir, "config.toml")
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat Kimi config.toml: %w", err)
	}
	lock, err := AcquireConfigFileLock(configPath)
	if err != nil {
		return false, fmt.Errorf("acquire Kimi config lock: %w", err)
	}
	defer lock.Release()

	raw, err := readKimiConfig(configPath)
	if err != nil {
		return false, err
	}
	hooks, err := kimiConfigHooks(raw)
	if err != nil {
		return false, err
	}
	kept := make([]any, 0, len(hooks))
	removed := false
	for _, value := range hooks {
		hook, ok := value.(map[string]any)
		if !ok {
			kept = append(kept, value)
			continue
		}
		command, _ := hook["command"].(string)
		if isAgentDeckKimiHookCommand(command) {
			removed = true
			continue
		}
		kept = append(kept, value)
	}
	if !removed {
		return false, nil
	}
	if len(kept) == 0 {
		delete(raw, "hooks")
	} else {
		raw["hooks"] = kept
	}
	if err := writeKimiConfig(configPath, raw); err != nil {
		return false, err
	}
	return true, nil
}

func GetKimiConfigDir() string {
	configured := ""
	if config, err := LoadUserConfig(); err == nil && config != nil {
		configured = config.Kimi.ConfigDir
	}
	return ResolveKimiConfigDir(configured)
}

func ResolveKimiConfigDir(configured string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		return sandboxedToolDir("Kimi config dir", ExpandPath(configured))
	}
	if ambient := strings.TrimSpace(os.Getenv("KIMI_CODE_HOME")); ambient != "" {
		return sandboxedToolDir("Kimi config dir", ExpandPath(ambient))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".kimi-code")
	}
	return sandboxedToolDir("Kimi config dir", filepath.Join(home, ".kimi-code"))
}

func CheckKimiHooksInstalled(configDir string) bool {
	raw, err := readKimiConfig(filepath.Join(configDir, "config.toml"))
	if err != nil {
		return false
	}
	hooks, err := kimiConfigHooks(raw)
	return err == nil && kimiHooksAlreadyInstalled(hooks)
}

func readKimiConfig(configPath string) (map[string]any, error) {
	raw := make(map[string]any)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return raw, nil
		}
		return nil, fmt.Errorf("read Kimi config.toml: %w", err)
	}
	if len(data) == 0 {
		return raw, nil
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse Kimi config.toml: %w", err)
	}
	return raw, nil
}

func kimiConfigHooks(raw map[string]any) ([]any, error) {
	value, ok := raw["hooks"]
	if !ok || value == nil {
		return nil, nil
	}
	switch hooks := value.(type) {
	case []any:
		return hooks, nil
	case []map[string]any:
		out := make([]any, len(hooks))
		for i := range hooks {
			out[i] = hooks[i]
		}
		return out, nil
	default:
		return nil, fmt.Errorf("parse Kimi config.toml: hooks must be an array of tables")
	}
}

func kimiHooksAlreadyInstalled(hooks []any) bool {
	for _, event := range kimiHookEvents {
		if !kimiHookPresent(hooks, event) {
			return false
		}
	}
	return true
}

func kimiHookPresent(hooks []any, event string) bool {
	for _, value := range hooks {
		hook, ok := value.(map[string]any)
		if !ok {
			continue
		}
		hookEvent, _ := hook["event"].(string)
		command, _ := hook["command"].(string)
		if hookEvent == event && strings.TrimSpace(command) == kimiHookCommandForEvent(event) {
			return true
		}
	}
	return false
}

func writeKimiConfig(configPath string, raw map[string]any) error {
	var out bytes.Buffer
	if err := toml.NewEncoder(&out).Encode(raw); err != nil {
		return fmt.Errorf("marshal Kimi config.toml: %w", err)
	}
	if err := atomicfile.WriteFile(configPath, out.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write Kimi config.toml: %w", err)
	}
	return nil
}
