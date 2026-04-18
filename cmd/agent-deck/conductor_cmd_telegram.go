package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Conductor telegram-topology CLI glue (fix v1.7.22, issue #658).

// telegramPluginKey is the settings.json key Claude Code uses for this
// plugin. Matches skills/agent-deck/SKILL.md "Telegram conductor topology".
const telegramPluginKey = "telegram@claude-plugins-official"

// readTelegramGloballyEnabled inspects settings.json in the given Claude
// Code profile directory (e.g. ~/.claude or ~/.claude-work) and reports
// whether the telegram plugin is globally enabled. Missing file and missing
// key both map to (false, nil) — absence is the safe baseline.
func readTelegramGloballyEnabled(configDir string) (bool, error) {
	path := filepath.Join(configDir, "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var parsed struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	return parsed.EnabledPlugins[telegramPluginKey], nil
}

// emitTelegramWarnings runs the validator and writes human-facing warnings
// to w. Silent on a clean configuration.
func emitTelegramWarnings(w io.Writer, in session.TelegramValidatorInput) {
	warnings := session.ValidateTelegramTopology(in)
	for _, warn := range warnings {
		fmt.Fprintf(w, "⚠  %s: %s\n", warn.Code, warn.Message)
	}
}

// disableTelegramGlobally is the v1.7.25 auto-remediation for issue #666's
// mechanism 1: the v1.7.22 validator only warned, so legacy
// enabledPlugins."telegram@claude-plugins-official" = true kept causing
// silent child-session crashes. This flips the flag to false and preserves
// all other keys in settings.json.
//
// Returns (changed, err) where changed=true only when the file was
// actually rewritten (the flag was true before this call). Missing file,
// missing block, missing key, or already-false are all no-op cases that
// return (false, nil) — safe to call on every `conductor setup`.
//
// We deliberately do NOT remove the key when setting to false. Keeping
// the explicit false preserves a signal to future diagnostics that the
// flag was touched, and prevents Claude Code from re-enabling by default
// if a later plugin flow treats absence as permission.
func disableTelegramGlobally(configDir string) (bool, error) {
	path := filepath.Join(configDir, "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	// Parse into a generic map so we round-trip unknown keys verbatim
	// (unlike the reader, which only needs enabledPlugins).
	var top map[string]any
	if err := json.Unmarshal(data, &top); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}

	plugins, _ := top["enabledPlugins"].(map[string]any)
	if plugins == nil {
		return false, nil
	}
	cur, exists := plugins[telegramPluginKey]
	if !exists {
		return false, nil
	}
	curBool, _ := cur.(bool)
	if !curBool {
		return false, nil
	}

	plugins[telegramPluginKey] = false
	top["enabledPlugins"] = plugins

	// Indent to match Claude Code's own style — two spaces, trailing
	// newline so git diffs stay clean.
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return false, fmt.Errorf("re-marshal %s: %w", path, err)
	}
	out = append(out, '\n')
	if err := os.WriteFile(path, out, 0644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}
