package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// writeBadgeConfig points the config loader at a temp HOME whose config.toml
// holds body verbatim, so the option is exercised through LoadUserConfig the
// way a user's hand-edited file is.
func writeBadgeConfig(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	path, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	session.ClearUserConfigCache()
}

// newBadgeHome builds a Home from the on-disk config and pins the classic
// (non-embedded) list so the tool badge renderer is the one under test.
func newBadgeHome(t *testing.T) *Home {
	t.Helper()
	h := NewHome()
	h.width, h.height = 240, 40
	h.embeddedLayout = false
	h.compactSidebar = false
	return h
}

func localBadgeRow(h *Home, tool string) string {
	inst := &session.Instance{ID: "row-" + tool, Title: "row-title", Tool: tool, Status: session.StatusIdle}
	var b strings.Builder
	h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, false, nil, 240)
	return ansi.Strip(b.String())
}

func remoteBadgeRow(h *Home, tool string) string {
	rs := session.RemoteSessionInfo{ID: "remote-" + tool, Title: "remote-title", Tool: tool, Status: "idle", RemoteName: "dev"}
	var b strings.Builder
	h.renderRemoteSessionItemAtWidth(&b, session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &rs, RemoteName: "dev", Level: 1, IsLastInGroup: true}, false, 240)
	return ansi.Strip(b.String())
}

// Most configs never set default_tool; every launch path then starts claude.
// hide_default_tool_badge = true on its own must therefore hide the claude
// badge, not silently do nothing.
func TestHideDefaultToolBadge_UnsetDefaultToolMeansClaude(t *testing.T) {
	writeBadgeConfig(t, "[display]\nhide_default_tool_badge = true\n")
	h := newBadgeHome(t)

	if got := localBadgeRow(h, "claude"); strings.Contains(got, "claude") {
		t.Errorf("default_tool unset resolves to claude: its badge must be hidden, row = %q", got)
	}
	if got := localBadgeRow(h, "codex"); !strings.Contains(got, "codex") {
		t.Errorf("non-default tool must keep its badge, row = %q", got)
	}
}

func TestHideDefaultToolBadge_ExplicitDefaultTool(t *testing.T) {
	writeBadgeConfig(t, "default_tool = \"codex\"\n\n[display]\nhide_default_tool_badge = true\n")
	h := newBadgeHome(t)

	if got := localBadgeRow(h, "codex"); strings.Contains(got, "codex") {
		t.Errorf("explicit default_tool badge must be hidden, row = %q", got)
	}
	if got := localBadgeRow(h, "claude"); !strings.Contains(got, "claude") {
		t.Errorf("claude is not the default here and must keep its badge, row = %q", got)
	}
}

func TestHideDefaultToolBadge_OffByDefault(t *testing.T) {
	writeBadgeConfig(t, "[display]\nshow_pane_titles = false\n")
	h := newBadgeHome(t)

	if got := localBadgeRow(h, "claude"); !strings.Contains(got, "claude") {
		t.Errorf("option absent: local badge must render, row = %q", got)
	}
	if got := remoteBadgeRow(h, "claude"); !strings.Contains(got, "claude") {
		t.Errorf("option absent: remote badge must render, row = %q", got)
	}
}

// Changing default_tool in the Settings panel must move the hidden badge
// without a restart, like the sibling [display] settings the save reloads.
func TestHideDefaultToolBadge_SettingsSaveRecomputes(t *testing.T) {
	writeBadgeConfig(t, "[display]\nhide_default_tool_badge = true\n")
	h := newBadgeHome(t)
	if got := localBadgeRow(h, "claude"); strings.Contains(got, "claude") {
		t.Fatalf("precondition: claude badge hidden at startup, row = %q", got)
	}

	h.settingsPanel.Show()
	h.settingsPanel.cursor = int(SettingDefaultTool)
	codexIdx := -1
	for i, v := range h.settingsPanel.toolValues {
		if v == "codex" {
			codexIdx = i
		}
	}
	if codexIdx < 1 {
		t.Fatalf("codex not selectable in settings panel: %v", h.settingsPanel.toolValues)
	}
	h.settingsPanel.selectedTool = codexIdx - 1
	h.Update(tea.KeyMsg{Type: tea.KeyRight})

	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultTool != "codex" {
		t.Fatalf("settings save did not persist default_tool: %q", cfg.DefaultTool)
	}
	if got := localBadgeRow(h, "codex"); strings.Contains(got, "codex") {
		t.Errorf("new default codex must lose its badge after save, row = %q", got)
	}
	if got := localBadgeRow(h, "claude"); !strings.Contains(got, "claude") {
		t.Errorf("claude is no longer default and must regain its badge, row = %q", got)
	}
}

// Classic remote rows carry the same tool badge as local rows, so the option
// applies there too, compared against this deck's default_tool.
func TestHideDefaultToolBadge_RemoteRows(t *testing.T) {
	writeBadgeConfig(t, "[display]\nhide_default_tool_badge = true\n")
	h := newBadgeHome(t)

	if got := remoteBadgeRow(h, "claude"); strings.Contains(got, "claude") {
		t.Errorf("remote row on the default tool must hide its badge, row = %q", got)
	}
	if got := remoteBadgeRow(h, "codex"); !strings.Contains(got, "codex") {
		t.Errorf("remote row on another tool must keep its badge, row = %q", got)
	}
	if got := remoteBadgeRow(h, "Claude"); strings.Contains(strings.ToLower(got), "claude") {
		t.Errorf("remote tool names compare case-insensitively, row = %q", got)
	}
}
