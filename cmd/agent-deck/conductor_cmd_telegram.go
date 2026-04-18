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
