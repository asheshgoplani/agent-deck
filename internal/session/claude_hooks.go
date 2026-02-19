package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// agentDeckHookCommand is the marker command used to identify agent-deck hooks in settings.json.
const agentDeckHookCommand = "agent-deck hook-handler"

// claudeHookEntry represents a single hook entry in Claude Code settings.
type claudeHookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Async   bool   `json:"async,omitempty"`
}

// claudeHookMatcher represents a matcher block (with optional matcher pattern) in settings.
type claudeHookMatcher struct {
	Matcher string            `json:"matcher,omitempty"`
	Hooks   []claudeHookEntry `json:"hooks"`
}

// agentDeckHook returns the standard agent-deck hook entry.
func agentDeckHook(async bool) claudeHookEntry {
	return claudeHookEntry{
		Type:    "command",
		Command: agentDeckHookCommand,
		Async:   async,
	}
}

// hookEventConfigs defines which Claude Code events we subscribe to and their matcher patterns.
var hookEventConfigs = []struct {
	Event   string
	Matcher string // empty = no matcher
	Async   bool   // false = synchronous (blocks via exit code)
}{
	{Event: "SessionStart", Async: true},
	{Event: "UserPromptSubmit", Async: true},
	{Event: "Stop", Async: true},
	{Event: "PermissionRequest", Async: true},
	{Event: "Notification", Matcher: "permission_prompt|elicitation_dialog", Async: true},
	{Event: "SessionEnd", Async: true},
	{Event: "PreCompact", Async: false},
}

// InjectClaudeHooks injects agent-deck hook entries into Claude Code's settings.json.
// Uses read-preserve-modify-write pattern to preserve all existing settings and user hooks.
// Returns true if hooks were newly installed, false if already present.
func InjectClaudeHooks(configDir string) (bool, error) {
	settingsPath := filepath.Join(configDir, "settings.json")

	// Read existing settings (or start fresh)
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

	// Parse existing hooks section
	var existingHooks map[string]json.RawMessage
	if raw, ok := rawSettings["hooks"]; ok {
		if err := json.Unmarshal(raw, &existingHooks); err != nil {
			// hooks key exists but isn't a valid object; start fresh for hooks
			existingHooks = make(map[string]json.RawMessage)
		}
	} else {
		existingHooks = make(map[string]json.RawMessage)
	}

	// Check if already installed (all events present with our hook command)
	if hooksAlreadyInstalled(existingHooks) {
		return false, nil
	}

	// Inject our hook entries for each event
	for _, cfg := range hookEventConfigs {
		existingHooks[cfg.Event] = mergeHookEvent(existingHooks[cfg.Event], cfg.Matcher, cfg.Async)
	}

	// Marshal hooks back into raw settings
	hooksRaw, err := json.Marshal(existingHooks)
	if err != nil {
		return false, fmt.Errorf("marshal hooks: %w", err)
	}
	rawSettings["hooks"] = hooksRaw

	// Atomic write
	finalData, err := json.MarshalIndent(rawSettings, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal settings: %w", err)
	}

	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}

	tmpPath := settingsPath + ".tmp"
	if err := os.WriteFile(tmpPath, finalData, 0644); err != nil {
		return false, fmt.Errorf("write settings.json.tmp: %w", err)
	}
	if err := os.Rename(tmpPath, settingsPath); err != nil {
		os.Remove(tmpPath)
		return false, fmt.Errorf("rename settings.json: %w", err)
	}

	sessionLog.Info("claude_hooks_installed", slog.String("config_dir", configDir))
	return true, nil
}

// RemoveClaudeHooks removes agent-deck hook entries from Claude Code's settings.json.
// Returns true if hooks were removed, false if none found.
func RemoveClaudeHooks(configDir string) (bool, error) {
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
	for _, cfg := range hookEventConfigs {
		if raw, ok := existingHooks[cfg.Event]; ok {
			cleaned, didRemove := removeAgentDeckFromEvent(raw)
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

	// If hooks map is empty, remove the key entirely
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
		os.Remove(tmpPath)
		return false, fmt.Errorf("rename settings.json: %w", err)
	}

	sessionLog.Info("claude_hooks_removed", slog.String("config_dir", configDir))
	return true, nil
}

// CheckClaudeHooksInstalled checks if agent-deck hooks are present in settings.json.
func CheckClaudeHooksInstalled(configDir string) bool {
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

	return hooksAlreadyInstalled(existingHooks)
}

// hooksAlreadyInstalled checks if all required agent-deck hooks are present.
func hooksAlreadyInstalled(hooks map[string]json.RawMessage) bool {
	for _, cfg := range hookEventConfigs {
		raw, ok := hooks[cfg.Event]
		if !ok {
			return false
		}
		if !eventHasAgentDeckHook(raw) {
			return false
		}
	}
	return true
}

// eventHasAgentDeckHook checks if a hook event's matcher array contains our hook.
func eventHasAgentDeckHook(raw json.RawMessage) bool {
	var matchers []claudeHookMatcher
	if err := json.Unmarshal(raw, &matchers); err != nil {
		return false
	}
	for _, m := range matchers {
		for _, h := range m.Hooks {
			if strings.Contains(h.Command, agentDeckHookCommand) {
				return true
			}
		}
	}
	return false
}

// mergeHookEvent adds agent-deck's hook to an existing event's matcher array.
// Preserves all existing matchers and hooks.
func mergeHookEvent(existing json.RawMessage, matcher string, async bool) json.RawMessage {
	var matchers []claudeHookMatcher

	if existing != nil {
		if err := json.Unmarshal(existing, &matchers); err != nil {
			matchers = nil
		}
	}

	// Check if we already have a matcher entry with our hook
	for i, m := range matchers {
		if m.Matcher == matcher {
			// Check if our hook is already in this matcher
			for _, h := range m.Hooks {
				if strings.Contains(h.Command, agentDeckHookCommand) {
					// Already present
					result, _ := json.Marshal(matchers)
					return result
				}
			}
			// Append our hook to existing matcher
			matchers[i].Hooks = append(matchers[i].Hooks, agentDeckHook(async))
			result, _ := json.Marshal(matchers)
			return result
		}
	}

	// No matching matcher found; add a new one
	newMatcher := claudeHookMatcher{
		Matcher: matcher,
		Hooks:   []claudeHookEntry{agentDeckHook(async)},
	}
	matchers = append(matchers, newMatcher)
	result, _ := json.Marshal(matchers)
	return result
}

// removeAgentDeckFromEvent removes agent-deck hook entries from an event's matcher array.
// Returns cleaned JSON and whether any removal happened. Returns nil JSON if the array is empty.
func removeAgentDeckFromEvent(raw json.RawMessage) (json.RawMessage, bool) {
	var matchers []claudeHookMatcher
	if err := json.Unmarshal(raw, &matchers); err != nil {
		return raw, false
	}

	removed := false
	var cleaned []claudeHookMatcher

	for _, m := range matchers {
		var hooks []claudeHookEntry
		for _, h := range m.Hooks {
			if strings.Contains(h.Command, agentDeckHookCommand) {
				removed = true
				continue
			}
			hooks = append(hooks, h)
		}
		if len(hooks) > 0 {
			m.Hooks = hooks
			cleaned = append(cleaned, m)
		} else if m.Matcher != "" && len(m.Hooks) == 0 {
			// Matcher had only our hooks; drop it entirely
			removed = true
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
