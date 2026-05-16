// Phase 5 EditSessionDialog tests for the Plugins field.
// RFC: docs/rfc/PLUGIN_ATTACH.md §4.8.

package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// withPluginCatalog redirects HOME and writes a config.toml so
// session.GetAvailablePluginNames returns predictable values.
func withPluginCatalog(t *testing.T, content string) {
	t.Helper()
	temp := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", temp)
	t.Cleanup(func() { os.Setenv("HOME", originalHome) })

	dir := filepath.Join(temp, ".agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// TestEditSessionDialog_PluginsFieldShownForClaudeWithCatalog asserts the
// Plugins field appears for a claude session when the catalog is non-empty.
func TestEditSessionDialog_PluginsFieldShownForClaudeWithCatalog(t *testing.T) {
	withPluginCatalog(t, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	d := &EditSessionDialog{}
	d.Show(&session.Instance{ID: "x", Tool: "claude", Title: "x"})

	found := false
	for _, f := range d.fields {
		if f.key == session.FieldPlugins {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("EditSessionDialog must include FieldPlugins for claude session with non-empty catalog")
	}
}

// TestEditSessionDialog_PluginsFieldHiddenForNonClaude asserts the field
// only renders for claude sessions.
func TestEditSessionDialog_PluginsFieldHiddenForNonClaude(t *testing.T) {
	withPluginCatalog(t, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	d := &EditSessionDialog{}
	d.Show(&session.Instance{ID: "x", Tool: "shell", Title: "x"})

	for _, f := range d.fields {
		if f.key == session.FieldPlugins {
			t.Errorf("FieldPlugins must NOT appear for non-claude session; got fields=%v", d.fields)
		}
	}
}

// TestEditSessionDialog_PluginsFieldHiddenWhenCatalogEmpty asserts the
// field is hidden when the catalog has no entries — clicking on a row
// that resolves to "no available choices" is hostile UX.
func TestEditSessionDialog_PluginsFieldHiddenWhenCatalogEmpty(t *testing.T) {
	withPluginCatalog(t, `
[claude]
config_dir = "~/.claude"
`)
	d := &EditSessionDialog{}
	d.Show(&session.Instance{ID: "x", Tool: "claude", Title: "x"})

	for _, f := range d.fields {
		if f.key == session.FieldPlugins {
			t.Errorf("FieldPlugins must be hidden when catalog is empty; got it visible")
		}
	}
}

// TestEditSessionDialog_PluginsInitialValueFromInstance asserts the field
// is pre-populated with the session's current Plugins as CSV.
func TestEditSessionDialog_PluginsInitialValueFromInstance(t *testing.T) {
	withPluginCatalog(t, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"

[plugins.discord]
name = "discord"
source = "claude-plugins-official"
`)
	d := &EditSessionDialog{}
	inst := &session.Instance{ID: "x", Tool: "claude", Title: "x", Plugins: []string{"octopus", "discord"}}
	d.Show(inst)

	for _, f := range d.fields {
		if f.key == session.FieldPlugins {
			got := strings.TrimSpace(f.input.Value())
			want := "octopus,discord"
			if got != want {
				t.Errorf("FieldPlugins initial value: got %q, want %q", got, want)
			}
			return
		}
	}
	t.Fatal("FieldPlugins field not registered")
}

// TestFieldInitialValue_Plugins asserts the diff baseline for GetChanges
// matches the Show() initial.
func TestFieldInitialValue_Plugins(t *testing.T) {
	inst := &session.Instance{Plugins: []string{"a", "b", "c"}}
	got := fieldInitialValue(inst, session.FieldPlugins)
	want := "a,b,c"
	if got != want {
		t.Errorf("fieldInitialValue: got %q, want %q", got, want)
	}
}
