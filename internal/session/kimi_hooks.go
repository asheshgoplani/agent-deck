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

	raw, data, err := readKimiConfig(configPath)
	if err != nil {
		return false, err
	}
	hooks, err := kimiConfigHooks(raw)
	if err != nil {
		return false, err
	}
	seen := make(map[string]bool, len(kimiHookEvents))
	managed := make([]any, 0, len(kimiHookEvents))
	changed := false
	for _, value := range hooks {
		hook, ok := value.(map[string]any)
		if !ok {
			continue
		}
		event, _ := hook["event"].(string)
		command, _ := hook["command"].(string)
		if !isAgentDeckKimiHookCommand(command) || !slices.Contains(kimiHookEvents, event) {
			continue
		}
		if seen[event] {
			changed = true
			continue
		}
		wantCommand := kimiHookCommandForEvent(event)
		if command != wantCommand {
			hook["command"] = wantCommand
			changed = true
		}
		seen[event] = true
		managed = append(managed, value)
	}
	for _, event := range kimiHookEvents {
		if !seen[event] {
			managed = append(managed, map[string]any{
				"event":   event,
				"command": kimiHookCommandForEvent(event),
			})
			changed = true
		}
	}
	if !changed {
		return false, nil
	}
	data, err = replaceAgentDeckKimiHooks(data, managed)
	if err != nil {
		return false, err
	}
	if err := writeKimiConfig(configPath, data); err != nil {
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

	raw, data, err := readKimiConfig(configPath)
	if err != nil {
		return false, err
	}
	if _, err := kimiConfigHooks(raw); err != nil {
		return false, err
	}
	data, removed, err := stripAgentDeckKimiHooks(data, false)
	if err != nil {
		return false, err
	}
	if !removed {
		return false, nil
	}
	if err := writeKimiConfig(configPath, data); err != nil {
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
	raw, _, err := readKimiConfig(filepath.Join(configDir, "config.toml"))
	if err != nil {
		return false
	}
	hooks, err := kimiConfigHooks(raw)
	return err == nil && kimiHooksAlreadyInstalled(hooks)
}

func readKimiConfig(configPath string) (map[string]any, []byte, error) {
	raw := make(map[string]any)
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return raw, nil, nil
		}
		return nil, nil, fmt.Errorf("read Kimi config.toml: %w", err)
	}
	if len(data) == 0 {
		return raw, data, nil
	}
	metadata, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, nil, fmt.Errorf("parse Kimi config.toml: %w", err)
	}
	if metadata.Type("hooks") == "Array" {
		return nil, nil, fmt.Errorf("parse Kimi config.toml: hooks must use [[hooks]] entries, not an inline array")
	}
	return raw, data, nil
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

type kimiHookBlock struct {
	start   int
	end     int
	event   string
	command string
}

func findKimiHookBlocks(data []byte) ([]kimiHookBlock, error) {
	var blocks []kimiHookBlock
	start := -1
	commentStart := -1
	flush := func(boundary int) error {
		if start < 0 {
			return nil
		}
		end := boundary
		if commentStart >= 0 {
			end = commentStart
		}
		var parsed struct {
			Hooks []struct {
				Event   string `toml:"event"`
				Command string `toml:"command"`
			} `toml:"hooks"`
		}
		if err := toml.Unmarshal(data[start:end], &parsed); err != nil {
			return fmt.Errorf("parse Kimi config.toml: %w", err)
		}
		if len(parsed.Hooks) != 1 {
			return fmt.Errorf("parse Kimi config.toml: hooks must be an array of tables")
		}
		blocks = append(blocks, kimiHookBlock{
			start:   start,
			end:     end,
			event:   parsed.Hooks[0].Event,
			command: parsed.Hooks[0].Command,
		})
		start = -1
		commentStart = -1
		return nil
	}

	offset := 0
	for _, line := range strings.SplitAfter(string(data), "\n") {
		lineStart := offset
		offset += len(line)
		if tableName, ok := tomlTableName(line); ok {
			switch {
			case tableName == "hooks":
				if err := flush(lineStart); err != nil {
					return nil, err
				}
				start = lineStart
			case !strings.HasPrefix(tableName, "hooks."):
				if err := flush(lineStart); err != nil {
					return nil, err
				}
			}
		}
		if start < 0 {
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "#"):
			if commentStart < 0 {
				commentStart = lineStart
			}
		case trimmed != "":
			commentStart = -1
		}
	}
	if err := flush(len(data)); err != nil {
		return nil, err
	}
	return blocks, nil
}

func stripAgentDeckKimiHooks(data []byte, knownEventsOnly bool) ([]byte, bool, error) {
	blocks, err := findKimiHookBlocks(data)
	if err != nil {
		return nil, false, err
	}
	var out bytes.Buffer
	out.Grow(len(data))
	last := 0
	removed := false
	for _, block := range blocks {
		if !isAgentDeckKimiHookCommand(block.command) || knownEventsOnly && !slices.Contains(kimiHookEvents, block.event) {
			continue
		}
		start := block.start
		if start > last && start > 0 && data[start-1] == '\n' {
			start--
		}
		out.Write(data[last:start])
		last = block.end
		removed = true
	}
	if !removed {
		return data, false, nil
	}
	out.Write(data[last:])
	return out.Bytes(), true, nil
}

func replaceAgentDeckKimiHooks(data []byte, hooks []any) ([]byte, error) {
	base, _, err := stripAgentDeckKimiHooks(data, true)
	if err != nil {
		return nil, err
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(struct {
		Hooks []any `toml:"hooks"`
	}{Hooks: hooks}); err != nil {
		return nil, fmt.Errorf("marshal Kimi hooks: %w", err)
	}

	out := make([]byte, 0, len(base)+encoded.Len()+1)
	out = append(out, base...)
	if len(base) > 0 {
		out = append(out, '\n')
	}
	out = append(out, encoded.Bytes()...)
	return out, nil
}

func writeKimiConfig(configPath string, data []byte) error {
	if len(data) > 0 {
		var config map[string]any
		if err := toml.Unmarshal(data, &config); err != nil {
			return fmt.Errorf("validate Kimi config.toml: %w", err)
		}
	}
	if err := atomicfile.WriteFile(configPath, data, 0o600); err != nil {
		return fmt.Errorf("write Kimi config.toml: %w", err)
	}
	return nil
}
