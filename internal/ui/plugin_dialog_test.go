// Tests for the standalone Plugin Manager dialog (`L` hotkey).
// RFC: docs/rfc/PLUGIN_ATTACH.md.

package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// withCatalog redirects HOME to a tempdir + writes config.toml so the
// session.GetAvailablePlugins accessor returns predictable values.
func withCatalog(t *testing.T, content string) {
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
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
}

func TestPluginDialog_Show_PopulatesItemsAndInitialEnabled(t *testing.T) {
	withCatalog(t, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"

[plugins.discord]
name = "discord"
source = "claude-plugins-official"
emits_channel = true
`)
	d := NewPluginDialog()
	inst := &session.Instance{ID: "x", Tool: "claude", Plugins: []string{"discord"}}
	if err := d.Show(inst); err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !d.IsVisible() {
		t.Fatal("dialog must be visible after Show")
	}
	if len(d.items) != 2 {
		t.Fatalf("items: got %d, want 2", len(d.items))
	}

	// Items are sorted alphabetically — discord first, octopus second.
	if d.items[0].name != "discord" || !d.items[0].enabled {
		t.Errorf("items[0] discord: got %+v, want enabled=true", d.items[0])
	}
	if d.items[1].name != "octopus" || d.items[1].enabled {
		t.Errorf("items[1] octopus: got %+v, want enabled=false", d.items[1])
	}
}

func TestPluginDialog_HasChanged_FalseInitial(t *testing.T) {
	withCatalog(t, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	d := NewPluginDialog()
	d.Show(&session.Instance{ID: "x", Tool: "claude", Plugins: []string{"octopus"}})

	if d.HasChanged() {
		t.Error("HasChanged must be false right after Show with no toggles")
	}
}

func TestPluginDialog_ToggleSetsHasChanged(t *testing.T) {
	withCatalog(t, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"

[plugins.discord]
name = "discord"
source = "claude-plugins-official"
emits_channel = true
`)
	d := NewPluginDialog()
	d.Show(&session.Instance{ID: "x", Tool: "claude"})

	d.cursor = 0
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})

	if !d.HasChanged() {
		t.Error("toggle on first item should produce HasChanged=true")
	}
	selected := d.SelectedPluginNames()
	// First sorted item is discord.
	if !reflect.DeepEqual(selected, []string{"discord"}) {
		t.Errorf("SelectedPluginNames after toggle[0]: got %v, want [discord]", selected)
	}
}

func TestPluginDialog_CursorNavigationWraps(t *testing.T) {
	withCatalog(t, `
[plugins.a]
name = "a"
source = "x/y"

[plugins.b]
name = "b"
source = "x/y"

[plugins.c]
name = "c"
source = "x/y"
`)
	d := NewPluginDialog()
	d.Show(&session.Instance{ID: "x", Tool: "claude"})

	if d.cursor != 0 {
		t.Fatalf("initial cursor: got %d, want 0", d.cursor)
	}

	// Down twice → cursor=2
	d.Update(tea.KeyMsg{Type: tea.KeyDown})
	d.Update(tea.KeyMsg{Type: tea.KeyDown})
	if d.cursor != 2 {
		t.Errorf("cursor after 2 downs: got %d, want 2", d.cursor)
	}
	// Down once more → wrap to 0
	d.Update(tea.KeyMsg{Type: tea.KeyDown})
	if d.cursor != 0 {
		t.Errorf("cursor after wrap-down: got %d, want 0", d.cursor)
	}
	// Up once → wrap to 2
	d.Update(tea.KeyMsg{Type: tea.KeyUp})
	if d.cursor != 2 {
		t.Errorf("cursor after wrap-up: got %d, want 2", d.cursor)
	}
}

func TestPluginDialog_EscHides(t *testing.T) {
	withCatalog(t, `
[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	d := NewPluginDialog()
	d.Show(&session.Instance{ID: "x", Tool: "claude"})
	d.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if d.IsVisible() {
		t.Error("Esc must hide the dialog")
	}
}

func TestPluginDialog_EmptyCatalogStillShowsWithHelp(t *testing.T) {
	withCatalog(t, `
[claude]
config_dir = "~/.claude"
`)
	d := NewPluginDialog()
	if err := d.Show(&session.Instance{ID: "x", Tool: "claude"}); err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !d.IsVisible() {
		t.Error("dialog must show even when catalog is empty (so user can see why)")
	}
	if len(d.items) != 0 {
		t.Errorf("items must be empty for empty catalog; got %v", d.items)
	}
	view := d.View()
	if view == "" {
		t.Error("View must produce help text for empty catalog")
	}
}

func TestPluginDialog_TelegramOfficialFilteredFromCatalog(t *testing.T) {
	withCatalog(t, `
[plugins.tg-official]
name = "telegram"
source = "claude-plugins-official"
emits_channel = true

[plugins.octopus]
name = "octopus"
source = "nyldn/claude-octopus"
`)
	d := NewPluginDialog()
	d.Show(&session.Instance{ID: "x", Tool: "claude"})

	for _, it := range d.items {
		if it.name == "tg-official" || it.id == "telegram@claude-plugins-official" {
			t.Errorf("dialog must not show v1-refused telegram-official entry; got %+v", it)
		}
	}
	if len(d.items) != 1 {
		t.Errorf("only octopus should be visible; got %d items: %+v", len(d.items), d.items)
	}
}

func TestPluginDialog_SelectedPluginNamesPreservesUserToggles(t *testing.T) {
	withCatalog(t, `
[plugins.a]
name = "a"
source = "x/y"

[plugins.b]
name = "b"
source = "x/y"

[plugins.c]
name = "c"
source = "x/y"
`)
	d := NewPluginDialog()
	d.Show(&session.Instance{ID: "x", Tool: "claude", Plugins: []string{"a", "c"}})

	// Disable "a" via toggle, enable "b".
	d.cursor = 0
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}) // toggle a (was on → off)
	d.cursor = 1
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}) // toggle b (was off → on)

	selected := d.SelectedPluginNames()
	want := []string{"b", "c"}
	if !reflect.DeepEqual(selected, want) {
		t.Errorf("SelectedPluginNames: got %v, want %v", selected, want)
	}
}
