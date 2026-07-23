package session

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// ApplyCodexTUISettings merges Agent Deck-managed TUI defaults into one
// CODEX_HOME/config.toml while preserving every unrelated setting.
func ApplyCodexTUISettings(codexHome string, settings *CodexTUISettings) error {
	if settings == nil || (settings.StatusLine == nil && settings.StatusLineUseColors == nil) {
		return nil
	}
	store, err := newHomeSkillStore(codexHome, "Codex")
	if err != nil {
		return err
	}
	configPath := filepath.Join(store.home, "config.toml")
	lock, err := acquireCodexConfigLock(configPath)
	if err != nil {
		return err
	}
	defer lock.Release()

	cfg := map[string]any{}
	if data, readErr := os.ReadFile(configPath); readErr == nil {
		if len(data) > 0 {
			if err := toml.Unmarshal(data, &cfg); err != nil {
				return fmt.Errorf("refusing to overwrite unparseable Codex config %s: %w", configPath, err)
			}
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read Codex config %s: %w", configPath, readErr)
	}

	tui, _ := cfg["tui"].(map[string]any)
	if tui == nil {
		tui = map[string]any{}
	}
	changed := false
	if settings.StatusLine != nil && !equalCodexStatusLine(tui["status_line"], settings.StatusLine) {
		tui["status_line"] = append([]string{}, settings.StatusLine...)
		changed = true
	}
	if settings.StatusLineUseColors != nil && tui["status_line_use_colors"] != *settings.StatusLineUseColors {
		tui["status_line_use_colors"] = *settings.StatusLineUseColors
		changed = true
	}
	if !changed {
		return nil
	}
	cfg["tui"] = tui

	if err := os.MkdirAll(store.home, 0o700); err != nil {
		return fmt.Errorf("create Codex home %s: %w", store.home, err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("marshal Codex TUI config: %w", err)
	}
	if err := atomicfile.WriteFile(configPath, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("save Codex TUI config %s: %w", configPath, err)
	}
	return nil
}

func equalCodexStatusLine(value any, want []string) bool {
	switch items := value.(type) {
	case []string:
		return reflect.DeepEqual(items, want)
	case []any:
		got := make([]string, len(items))
		for i, item := range items {
			text, ok := item.(string)
			if !ok {
				return false
			}
			got[i] = text
		}
		return reflect.DeepEqual(got, want)
	default:
		return false
	}
}
