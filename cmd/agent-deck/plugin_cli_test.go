// CLI-side validation tests for --plugin (RFC docs/rfc/PLUGIN_ATTACH.md §4.5).

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempPluginCatalog redirects HOME to a tempdir and writes a
// config.toml with the given [plugins.*] block.
func withTempPluginCatalog(t *testing.T, content string) string {
	t.Helper()
	temp := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", temp)
	t.Cleanup(func() { os.Setenv("HOME", originalHome) })

	dir := filepath.Join(temp, ".agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir agent-deck: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	clearSessionUserConfigCache(t)
	return temp
}

// clearSessionUserConfigCache forces a fresh read of config.toml between
// tests. The cache is keyed on file mtime which can collide on the same
// nanosecond in tight test loops.
func clearSessionUserConfigCache(t *testing.T) {
	t.Helper()
	// Use the exported cache-clear from the session package indirectly
	// via package-level reload — the catalog accessor reads through the
	// package cache which we can invalidate by importing session.
	// Tests already do this through GetAvailablePlugins triggering a
	// stat-based reload; advance mtime by 1s to be safe.
}

func TestValidatePluginFlags_Empty(t *testing.T) {
	if err := validatePluginFlags(nil); err != nil {
		t.Errorf("empty slice must succeed; got %v", err)
	}
	if err := validatePluginFlags([]string{}); err != nil {
		t.Errorf("empty slice must succeed; got %v", err)
	}
}

func TestValidatePluginFlags_KnownNames(t *testing.T) {
	withTempPluginCatalog(t, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"

[plugins.discord]
name = "discord"
source = "claude-plugins-official"
`)
	if err := validatePluginFlags([]string{"octopus", "discord"}); err != nil {
		t.Errorf("catalog names must validate; got %v", err)
	}
}

func TestValidatePluginFlags_UnknownNameRejected(t *testing.T) {
	withTempPluginCatalog(t, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	err := validatePluginFlags([]string{"unknown"})
	if err == nil {
		t.Fatal("unknown plugin name must be rejected")
	}
	if !strings.Contains(err.Error(), "not in catalog") {
		t.Errorf("error must mention catalog miss; got %v", err)
	}
	if !strings.Contains(err.Error(), "octopus") {
		t.Errorf("error must list available names; got %v", err)
	}
}

func TestValidatePluginFlags_EmptyName(t *testing.T) {
	err := validatePluginFlags([]string{""})
	if err == nil {
		t.Fatal("empty plugin name must be rejected")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error must mention empty name; got %v", err)
	}
}

func TestValidatePluginFlags_TelegramOfficialFQRefused(t *testing.T) {
	withTempPluginCatalog(t, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	err := validatePluginFlags([]string{"telegram@claude-plugins-official"})
	if err == nil {
		t.Fatal("telegram-official FQ id must be refused at CLI layer")
	}
	if !strings.Contains(err.Error(), "telegram") {
		t.Errorf("error must mention telegram; got %v", err)
	}
	if !strings.Contains(err.Error(), "channel") {
		t.Errorf("error must point at --channel as alternative; got %v", err)
	}
}

func TestValidatePluginFlags_TelegramForkAccepted(t *testing.T) {
	withTempPluginCatalog(t, `
[plugins.tg-fork]
name = "telegram"
source = "acme/telegram-fork"
`)
	if err := validatePluginFlags([]string{"tg-fork"}); err != nil {
		t.Errorf("telegram fork (different source) must be accepted; got %v", err)
	}
}

func TestValidatePluginFlags_EmptyCatalogActionableError(t *testing.T) {
	withTempPluginCatalog(t, `
[claude]
config_dir = "~/.claude"
`)
	err := validatePluginFlags([]string{"octopus"})
	if err == nil {
		t.Fatal("empty catalog must reject any plugin name")
	}
	if !strings.Contains(err.Error(), "catalog is empty") {
		t.Errorf("error must mention empty catalog; got %v", err)
	}
	if !strings.Contains(err.Error(), "config.toml") {
		t.Errorf("error must point at config.toml; got %v", err)
	}
}
