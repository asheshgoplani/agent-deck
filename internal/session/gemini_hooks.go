package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const agentDeckGeminiHookCommand = "agent-deck hook-handler"

type geminiHookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

type geminiHookMatcher struct {
	Matcher string            `json:"matcher,omitempty"`
	Hooks   []geminiHookEntry `json:"hooks"`
}

func geminiAgentDeckHook() geminiHookEntry {
	return geminiHookEntry{
		Type:    "command",
		Command: agentDeckGeminiHookCommand,
	}
}

var geminiHookEventConfigs = []struct {
	Event   string
	Matcher string
}{
	// SessionStart/SessionEnd bracket lifecycle.
	// BeforeAgent/AfterAgent provide stable running/waiting transitions.
	// We intentionally keep this set narrow to avoid mapping noisy/auxiliary events.
	{Event: "SessionStart"},
	{Event: "BeforeAgent"},
	{Event: "AfterAgent"},
	{Event: "SessionEnd"},
}

// InjectGeminiHooks injects agent-deck hook entries into Gemini CLI settings.json.
// Uses read-preserve-modify-write pattern to preserve all existing settings and user hooks.
// Returns true if hooks were newly installed, false if already present.
//
// Known limitation: this path does not currently use file locking, so concurrent
// writers to the same settings.json can race (last-writer-wins).
func InjectGeminiHooks(configDir string) (bool, error) {
	settingsPath := filepath.Join(configDir, "settings.json")

	var rawSettings map[string]json.RawMessage
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read settings.json: %w", err)
		}
		rawSettings = make(map[string]json.RawMessage)
	} else {
		if err := json.Unmarshal(data, &rawSettings); err != nil {
			return false, fmt.Errorf("parse settings.json: %w", err)
		}
	}

	var existingHooks map[string]json.RawMessage
	if raw, ok := rawSettings["hooks"]; ok {
		if err := json.Unmarshal(raw, &existingHooks); err != nil {
			existingHooks = make(map[string]json.RawMessage)
		}
	} else {
		existingHooks = make(map[string]json.RawMessage)
	}

	if geminiHooksAlreadyInstalled(existingHooks) {
		return false, nil
	}

	for _, cfg := range geminiHookEventConfigs {
		existingHooks[cfg.Event] = mergeGeminiHookEvent(existingHooks[cfg.Event], cfg.Matcher)
	}

	hooksRaw, err := json.Marshal(existingHooks)
	if err != nil {
		return false, fmt.Errorf("marshal hooks: %w", err)
	}
	rawSettings["hooks"] = hooksRaw

	finalData, err := json.MarshalIndent(rawSettings, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}

	tmpPath := settingsPath + ".tmp"
	if err := os.WriteFile(tmpPath, finalData, 0644); err != nil {
		return false, fmt.Errorf("write settings.json.tmp: %w", err)
	}
	if err := os.Rename(tmpPath, settingsPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("rename settings.json: %w", err)
	}

	sessionLog.Info("gemini_hooks_installed", slog.String("config_dir", configDir))
	return true, nil
}

// RemoveGeminiHooks removes agent-deck hook entries from Gemini CLI settings.json.
// Returns true if hooks were removed, false if none found.
func RemoveGeminiHooks(configDir string) (bool, error) {
	settingsPath := filepath.Join(configDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read settings.json: %w", err)
	}

	var rawSettings map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawSettings); err != nil {
		return false, fmt.Errorf("parse settings.json: %w", err)
	}

	hooksRaw, ok := rawSettings["hooks"]
	if !ok {
		return false, nil
	}

	var existingHooks map[string]json.RawMessage
	if err := json.Unmarshal(hooksRaw, &existingHooks); err != nil {
		return false, nil
	}

	removed := false
	for _, cfg := range geminiHookEventConfigs {
		if raw, ok := existingHooks[cfg.Event]; ok {
			cleaned, didRemove := removeAgentDeckFromGeminiEvent(raw)
			if didRemove {
				removed = true
				if cleaned == nil {
					delete(existingHooks, cfg.Event)
				} else {
					existingHooks[cfg.Event] = cleaned
				}
			}
		}
	}

	if !removed {
		return false, nil
	}

	if len(existingHooks) == 0 {
		delete(rawSettings, "hooks")
	} else {
		hooksData, _ := json.Marshal(existingHooks)
		rawSettings["hooks"] = hooksData
	}

	finalData, err := json.MarshalIndent(rawSettings, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}

	tmpPath := settingsPath + ".tmp"
	if err := os.WriteFile(tmpPath, finalData, 0644); err != nil {
		return false, fmt.Errorf("write settings.json.tmp: %w", err)
	}
	if err := os.Rename(tmpPath, settingsPath); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("rename settings.json: %w", err)
	}

	sessionLog.Info("gemini_hooks_removed", slog.String("config_dir", configDir))
	return true, nil
}

// CheckGeminiHooksInstalled checks whether required agent-deck Gemini hooks are installed.
func CheckGeminiHooksInstalled(configDir string) bool {
	settingsPath := filepath.Join(configDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return false
	}

	var rawSettings map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawSettings); err != nil {
		return false
	}

	hooksRaw, ok := rawSettings["hooks"]
	if !ok {
		return false
	}

	var existingHooks map[string]json.RawMessage
	if err := json.Unmarshal(hooksRaw, &existingHooks); err != nil {
		return false
	}

	return geminiHooksAlreadyInstalled(existingHooks)
}

func geminiHooksAlreadyInstalled(hooks map[string]json.RawMessage) bool {
	for _, cfg := range geminiHookEventConfigs {
		raw, ok := hooks[cfg.Event]
		if !ok {
			return false
		}
		if !geminiEventHasAgentDeckHook(raw) {
			return false
		}
	}
	return true
}

func geminiEventHasAgentDeckHook(raw json.RawMessage) bool {
	var matchers []geminiHookMatcher
	if err := json.Unmarshal(raw, &matchers); err != nil {
		return false
	}
	for _, m := range matchers {
		for _, h := range m.Hooks {
			if strings.Contains(h.Command, agentDeckGeminiHookCommand) {
				return true
			}
		}
	}
	return false
}

func mergeGeminiHookEvent(existing json.RawMessage, matcher string) json.RawMessage {
	var matchers []geminiHookMatcher
	if existing != nil {
		if err := json.Unmarshal(existing, &matchers); err != nil {
			matchers = nil
		}
	}

	for i, m := range matchers {
		if m.Matcher != matcher {
			continue
		}
		for _, h := range m.Hooks {
			if strings.Contains(h.Command, agentDeckGeminiHookCommand) {
				return existing
			}
		}
		matchers[i].Hooks = append(matchers[i].Hooks, geminiAgentDeckHook())
		result, _ := json.Marshal(matchers)
		return result
	}

	matchers = append(matchers, geminiHookMatcher{
		Matcher: matcher,
		Hooks:   []geminiHookEntry{geminiAgentDeckHook()},
	})
	result, _ := json.Marshal(matchers)
	return result
}

func removeAgentDeckFromGeminiEvent(raw json.RawMessage) (json.RawMessage, bool) {
	var matchers []geminiHookMatcher
	if err := json.Unmarshal(raw, &matchers); err != nil {
		return raw, false
	}

	removed := false
	var cleaned []geminiHookMatcher

	for _, m := range matchers {
		var hooks []geminiHookEntry
		for _, h := range m.Hooks {
			if strings.Contains(h.Command, agentDeckGeminiHookCommand) {
				removed = true
				continue
			}
			hooks = append(hooks, h)
		}
		if len(hooks) > 0 {
			m.Hooks = hooks
			cleaned = append(cleaned, m)
		}
	}

	if !removed {
		return raw, false
	}
	if len(cleaned) == 0 {
		return nil, true
	}
	result, _ := json.Marshal(cleaned)
	return result, true
}
