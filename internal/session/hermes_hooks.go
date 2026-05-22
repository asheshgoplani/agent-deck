package session

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const agentDeckHermesHookCommand = "agent-deck hook-handler"

// hermesHookEvents are the Hermes lifecycle events we subscribe to.
// pre_tool_call/post_tool_call bracket each tool call (running/waiting).
// on_session_start provides an initial waiting state.
// on_session_end signals the session is dead.
var hermesHookEvents = []string{
	"pre_tool_call",
	"post_tool_call",
	"on_session_start",
	"on_session_end",
}

// GetHermesConfigDir returns the Hermes config directory (~/.hermes).
func GetHermesConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".hermes")
	}
	return filepath.Join(home, ".hermes")
}

// InjectHermesHooks injects agent-deck hook entries into Hermes's config.yaml.
// Uses read-preserve-modify-write to keep all existing config keys intact.
// Returns true if hooks were newly installed, false if already present.
func InjectHermesHooks(configDir string) (bool, error) {
	configPath := filepath.Join(configDir, "config.yaml")

	var raw map[string]interface{}
	data, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read config.yaml: %w", err)
		}
		raw = make(map[string]interface{})
	} else {
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return false, fmt.Errorf("parse config.yaml: %w", err)
		}
		if raw == nil {
			raw = make(map[string]interface{})
		}
	}

	if hermesHooksAlreadyInstalled(raw) {
		return false, nil
	}

	mergeHermesHookEntries(raw)

	out, err := yaml.Marshal(raw)
	if err != nil {
		return false, fmt.Errorf("marshal config.yaml: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}

	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, out, 0644); err != nil {
		return false, fmt.Errorf("write config.yaml.tmp: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("rename config.yaml: %w", err)
	}

	sessionLog.Info("hermes_hooks_installed", slog.String("config_dir", configDir))
	return true, nil
}

// RemoveHermesHooks removes agent-deck hook entries from Hermes's config.yaml.
// Returns true if hooks were removed, false if none found.
func RemoveHermesHooks(configDir string) (bool, error) {
	configPath := filepath.Join(configDir, "config.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read config.yaml: %w", err)
	}

	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false, fmt.Errorf("parse config.yaml: %w", err)
	}
	if raw == nil {
		return false, nil
	}

	hooksSection, _ := raw["hooks"].(map[string]interface{})
	if hooksSection == nil {
		return false, nil
	}

	removed := false
	for _, event := range hermesHookEvents {
		eventHooks, _ := hooksSection[event].([]interface{})
		var kept []interface{}
		for _, h := range eventHooks {
			hm, ok := h.(map[string]interface{})
			if !ok {
				kept = append(kept, h)
				continue
			}
			cmd, _ := hm["command"].(string)
			if strings.Contains(cmd, agentDeckHermesHookCommand) {
				removed = true
				continue
			}
			kept = append(kept, h)
		}
		if removed {
			if len(kept) == 0 {
				delete(hooksSection, event)
			} else {
				hooksSection[event] = kept
			}
		}
	}

	if !removed {
		return false, nil
	}

	if len(hooksSection) == 0 {
		delete(raw, "hooks")
	} else {
		raw["hooks"] = hooksSection
	}

	out, err := yaml.Marshal(raw)
	if err != nil {
		return false, fmt.Errorf("marshal config.yaml: %w", err)
	}

	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, out, 0644); err != nil {
		return false, fmt.Errorf("write config.yaml.tmp: %w", err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("rename config.yaml: %w", err)
	}

	sessionLog.Info("hermes_hooks_removed", slog.String("config_dir", configDir))
	return true, nil
}

// CheckHermesHooksInstalled returns true if all agent-deck hook entries are
// present in Hermes's config.yaml.
func CheckHermesHooksInstalled(configDir string) bool {
	data, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		return false
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return false
	}
	return hermesHooksAlreadyInstalled(raw)
}

// hermesHooksAlreadyInstalled checks that every required event has an
// agent-deck hook entry.
func hermesHooksAlreadyInstalled(raw map[string]interface{}) bool {
	hooksSection, _ := raw["hooks"].(map[string]interface{})
	if hooksSection == nil {
		return false
	}
	for _, event := range hermesHookEvents {
		eventHooks, _ := hooksSection[event].([]interface{})
		found := false
		for _, h := range eventHooks {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			cmd, _ := hm["command"].(string)
			if strings.Contains(cmd, agentDeckHermesHookCommand) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// mergeHermesHookEntries appends agent-deck hook entries for any missing events.
func mergeHermesHookEntries(raw map[string]interface{}) {
	hooksSection, _ := raw["hooks"].(map[string]interface{})
	if hooksSection == nil {
		hooksSection = make(map[string]interface{})
	}

	for _, event := range hermesHookEvents {
		eventHooks, _ := hooksSection[event].([]interface{})
		alreadyPresent := false
		for _, h := range eventHooks {
			hm, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			cmd, _ := hm["command"].(string)
			if strings.Contains(cmd, agentDeckHermesHookCommand) {
				alreadyPresent = true
				break
			}
		}
		if !alreadyPresent {
			eventHooks = append(eventHooks, map[string]interface{}{
				"command": agentDeckHermesHookCommand,
			})
			hooksSection[event] = eventHooks
		}
	}

	raw["hooks"] = hooksSection
}
