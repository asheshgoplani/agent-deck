package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

const agentDeckCursorHookCommand = "agent-deck hook-handler"

type cursorHookDef struct {
	Command string `json:"command"`
	Matcher string `json:"matcher,omitempty"`
}

// cursorHooksConfig is the read-only view used for the installed check; the
// rewrite goes through raw JSON (cursorHooksRaw) so fields this struct does
// not model survive install and uninstall (ASK-102).
type cursorHooksConfig struct {
	Version int                        `json:"version"`
	Hooks   map[string][]cursorHookDef `json:"hooks"`
}

var cursorHookEventNames = []string{
	"sessionStart",
	"sessionEnd",
	"beforeSubmitPrompt",
	"preToolUse",
	"postToolUse",
	"stop",
}

// InjectCursorHooks injects agent-deck hook entries into ~/.cursor/hooks.json.
// Uses read-preserve-modify-write to keep existing user hooks.
// Returns true if hooks were newly installed, false if already present.
func InjectCursorHooks(configDir string) (bool, error) {
	hooksPath := filepath.Join(configDir, "hooks.json")

	var cfg cursorHooksConfig
	var top jsonObject
	var hooks map[string][]json.RawMessage
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("read hooks.json: %w", err)
		}
	} else {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return false, fmt.Errorf("parse hooks.json: %w", err)
		}
		if top, hooks, err = cursorHooksRaw(data); err != nil {
			return false, fmt.Errorf("parse hooks.json: %w", err)
		}
	}

	if cursorHooksAlreadyInstalled(cfg.Hooks) {
		return false, nil
	}

	if hooks == nil {
		hooks = make(map[string][]json.RawMessage)
	}
	for _, event := range cursorHookEventNames {
		hooks[event] = mergeCursorHookEvent(hooks[event])
	}
	if cfg.Version == 0 {
		top.set("version", json.RawMessage("1"))
	}
	top.set("hooks", mustMarshal(hooks))

	finalData, err := indentSettings(top)
	if err != nil {
		return false, fmt.Errorf("marshal hooks.json: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return false, fmt.Errorf("create config dir: %w", err)
	}
	if err := atomicfile.WriteFile(hooksPath, finalData, 0644); err != nil {
		return false, fmt.Errorf("write hooks.json: %w", err)
	}

	sessionLog.Info("cursor_hooks_installed", slog.String("config_dir", configDir))
	return true, nil
}

// AutoInstallCursorHooks is the TUI-startup entry point for silent Cursor hook
// injection. Unlike InjectCursorHooks (the explicit `cursor-hooks install`
// path), it honors the user's durable opt-out: [cursor] hooks_enabled = false,
// which `cursor-hooks uninstall` persists. Without this gate, TUI startup
// silently re-created the hooks on every launch after an uninstall (issue #1672).
// Returns true if hooks were newly installed.
func AutoInstallCursorHooks(cfg *UserConfig, configDir string) (bool, error) {
	if cfg != nil && !cfg.Cursor.GetHooksEnabled() {
		return false, nil
	}
	if CheckCursorHooksInstalled(configDir) {
		return false, nil
	}
	return InjectCursorHooks(configDir)
}

// SetCursorHooksEnabled persists the Cursor hooks opt-in/opt-out to config.toml.
// enabled=false writes [cursor] hooks_enabled = false (durable opt-out honored
// by AutoInstallCursorHooks); enabled=true removes the key, restoring the
// default. Works on a shallow copy so the LoadUserConfig cache is not mutated
// before the save lands.
func SetCursorHooksEnabled(enabled bool) error {
	cfg, err := LoadUserConfig()
	if err != nil {
		return err
	}
	// No-op when the effective state already matches: skips a full
	// config.toml rewrite (which drops comments/formatting).
	if cfg.Cursor.GetHooksEnabled() == enabled {
		return nil
	}
	updated := *cfg
	if enabled {
		updated.Cursor.HooksEnabled = nil
	} else {
		disabled := false
		updated.Cursor.HooksEnabled = &disabled
	}
	return SaveUserConfig(&updated)
}

// RemoveCursorHooks removes agent-deck hook entries from ~/.cursor/hooks.json.
// Returns true if hooks were removed, false if none found.
func RemoveCursorHooks(configDir string) (bool, error) {
	hooksPath := filepath.Join(configDir, "hooks.json")
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read hooks.json: %w", err)
	}

	top, hooks, err := cursorHooksRaw(data)
	if err != nil {
		return false, fmt.Errorf("parse hooks.json: %w", err)
	}
	if hooks == nil {
		return false, nil
	}

	removed := false
	for _, event := range cursorHookEventNames {
		cleaned, didRemove := removeAgentDeckFromCursorEvent(hooks[event])
		if didRemove {
			removed = true
			if len(cleaned) == 0 {
				delete(hooks, event)
			} else {
				hooks[event] = cleaned
			}
		}
	}

	if !removed {
		return false, nil
	}
	top.set("hooks", mustMarshal(hooks))

	finalData, err := indentSettings(top)
	if err != nil {
		return false, fmt.Errorf("marshal hooks.json: %w", err)
	}
	if err := atomicfile.WriteFile(hooksPath, finalData, 0644); err != nil {
		return false, fmt.Errorf("write hooks.json: %w", err)
	}

	sessionLog.Info("cursor_hooks_removed", slog.String("config_dir", configDir))
	return true, nil
}

// CheckCursorHooksInstalled reports whether required agent-deck Cursor hooks are installed.
func CheckCursorHooksInstalled(configDir string) bool {
	hooksPath := filepath.Join(configDir, "hooks.json")
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		return false
	}

	var cfg cursorHooksConfig
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.Hooks == nil {
		return false
	}
	return cursorHooksAlreadyInstalled(cfg.Hooks)
}

func cursorHooksAlreadyInstalled(hooks map[string][]cursorHookDef) bool {
	for _, event := range cursorHookEventNames {
		if !cursorEventHasAgentDeckHook(hooks[event]) {
			return false
		}
	}
	return true
}

func cursorEventHasAgentDeckHook(defs []cursorHookDef) bool {
	for _, d := range defs {
		if strings.Contains(d.Command, agentDeckCursorHookCommand) {
			return true
		}
	}
	return false
}

// cursorHooksRaw decodes hooks.json keeping the top-level keys and every hook
// entry as raw JSON, so the rewrite changes only agent-deck's own entries. A
// JSON null root reads as an empty object, as the typed decode always did.
func cursorHooksRaw(data []byte) (jsonObject, map[string][]json.RawMessage, error) {
	var top jsonObject
	if string(bytes.TrimSpace(data)) == "null" {
		return top, nil, nil
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, nil, err
	}
	var hooks map[string][]json.RawMessage
	if raw, ok := top.get("hooks"); ok {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, nil, err
		}
	}
	return top, hooks, nil
}

func mergeCursorHookEvent(existing []json.RawMessage) []json.RawMessage {
	for _, d := range existing {
		if rawHookCommandContains(d, agentDeckCursorHookCommand) {
			return existing
		}
	}
	return append(existing, mustMarshal(cursorHookDef{Command: agentDeckCursorHookCommand}))
}

func removeAgentDeckFromCursorEvent(defs []json.RawMessage) ([]json.RawMessage, bool) {
	removed := false
	var cleaned []json.RawMessage
	for _, d := range defs {
		if rawHookCommandContains(d, agentDeckCursorHookCommand) {
			removed = true
			continue
		}
		cleaned = append(cleaned, d)
	}
	return cleaned, removed
}
