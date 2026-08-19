package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// accountDialogConfig writes a sandboxed config.toml and returns the temp HOME.
// Nothing here touches the real ~/.agent-deck/config.toml.
func accountDialogConfig(t *testing.T, contents string) string {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("AGENTDECK_PROFILE", "")

	dir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return tmpHome
}

const dialogReferenceConfig = `
[profiles.personal.claude]
config_dir = "~/.agent-accounts/claude/personal"

[profiles.work.claude]
config_dir = "~/.agent-accounts/claude/work"

[profiles.codex-gmail.codex]
codex_home = "~/.agent-accounts/codex/gmail"

[profiles.codex-seminno.codex]
codex_home = "~/.agent-accounts/codex/semanticinnovations"
`

// selectTool points the dialog's tool picker at a named tool.
func selectTool(t *testing.T, d *NewDialog, tool string) {
	t.Helper()
	for i, cmd := range d.presetCommands {
		if cmd == tool {
			d.commandCursor = i
			d.updateToolOptions()
			return
		}
	}
	t.Fatalf("tool %q not in preset commands %v", tool, d.presetCommands)
}

// TestAccountFieldHiddenWithoutConfiguredAccounts is the opt-in-by-presence
// guard for the TUI: a zero-config user must not see an Account field, and it
// must not be in the focus ring either (otherwise Tab lands on an invisible
// element).
func TestAccountFieldHiddenWithoutConfiguredAccounts(t *testing.T) {
	accountDialogConfig(t, "[claude]\ndangerous_mode = true\n")

	d := NewNewDialog()
	d.SetSize(120, 50)
	d.ShowInGroup("default", "default", "/tmp", nil, "")
	selectTool(t, d, "claude")

	if d.hasAccountField() {
		t.Fatal("Account field must not exist with no configured accounts")
	}
	if d.indexOf(focusAccount) != -1 {
		t.Fatal("focusAccount must not be in the focus ring with no configured accounts")
	}
	if strings.Contains(d.View(), "Account:") {
		t.Fatalf("dialog must not render an Account label with no configured accounts:\n%s", d.View())
	}
	if d.SelectedAccount() != "" {
		t.Fatalf("SelectedAccount = %q, want empty", d.SelectedAccount())
	}
}

// TestAccountFieldAppearsForConfiguredTool covers the reference case: the field
// shows for a tool that has accounts and lists exactly those accounts.
func TestAccountFieldAppearsForConfiguredTool(t *testing.T) {
	accountDialogConfig(t, dialogReferenceConfig)

	d := NewNewDialog()
	d.SetSize(120, 50)
	d.ShowInGroup("default", "default", "/tmp", nil, "")
	selectTool(t, d, "claude")

	if !d.hasAccountField() {
		t.Fatal("Account field must appear when the selected tool has accounts")
	}
	if d.indexOf(focusAccount) == -1 {
		t.Fatal("focusAccount must be reachable by Tab")
	}

	view := d.View()
	if !strings.Contains(view, "Account:") {
		t.Fatalf("dialog must render the Account label:\n%s", view)
	}
	for _, want := range []string{"personal", "work", accountDefaultLabel} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog must offer %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "codex-gmail") {
		t.Errorf("a claude session must not be offered a codex account:\n%s", view)
	}
}

// TestAccountFieldIsToolScoped is the core promise of the picker: switching the
// tool rebuilds the list, so codex never sees claude accounts.
func TestAccountFieldIsToolScoped(t *testing.T) {
	accountDialogConfig(t, dialogReferenceConfig)

	d := NewNewDialog()
	d.SetSize(120, 50)
	d.ShowInGroup("default", "default", "/tmp", nil, "")

	selectTool(t, d, "claude")
	got := accountNames(d)
	if strings.Join(got, ",") != "personal,work" {
		t.Fatalf("claude accounts = %v, want [personal work]", got)
	}

	selectTool(t, d, "codex")
	got = accountNames(d)
	if strings.Join(got, ",") != "codex-gmail,codex-seminno" {
		t.Fatalf("codex accounts = %v, want [codex-gmail codex-seminno]", got)
	}

	// A tool with no account family drops the field entirely.
	selectTool(t, d, "gemini")
	if d.hasAccountField() {
		t.Fatalf("gemini has no account family; field must disappear, got %v", accountNames(d))
	}
	if d.indexOf(focusAccount) != -1 {
		t.Fatal("focusAccount must leave the focus ring when the tool has no accounts")
	}
}

// TestAccountFieldDefaultsFromGroupConfig is the config-first half of the
// feature seen from the TUI: an account configured once for the group is
// pre-selected in the dialog.
func TestAccountFieldDefaultsFromGroupConfig(t *testing.T) {
	accountDialogConfig(t, dialogReferenceConfig+`
[groups."work"]
account = "work"
`)

	d := NewNewDialog()
	d.SetSize(120, 50)
	d.ShowInGroup("work", "work", "/tmp", nil, "")
	selectTool(t, d, "claude")

	if got := d.SelectedAccount(); got != "work" {
		t.Fatalf("pre-selected account = %q, want %q (from [groups.\"work\"].account)", got, "work")
	}

	// A group with no configured account falls back to the tool default.
	d2 := NewNewDialog()
	d2.SetSize(120, 50)
	d2.ShowInGroup("other", "other", "/tmp", nil, "")
	selectTool(t, d2, "claude")
	if got := d2.SelectedAccount(); got != "" {
		t.Fatalf("unconfigured group: selected account = %q, want the tool default (empty)", got)
	}
}

// TestAccountFieldNavigation covers the keys a user actually presses.
func TestAccountFieldNavigation(t *testing.T) {
	accountDialogConfig(t, dialogReferenceConfig)

	d := NewNewDialog()
	d.SetSize(120, 50)
	d.ShowInGroup("default", "default", "/tmp", nil, "")
	selectTool(t, d, "claude")

	idx := d.indexOf(focusAccount)
	if idx == -1 {
		t.Fatal("focusAccount not in the ring")
	}
	d.focusIndex = idx
	d.updateFocus()

	press := func(key string) {
		t.Helper()
		var msg tea.KeyMsg
		switch key {
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "left":
			msg = tea.KeyMsg{Type: tea.KeyLeft}
		case "space":
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")}
		default:
			t.Fatalf("unhandled key %q", key)
		}
		updated, _ := d.Update(msg)
		*d = *updated
	}

	if got := d.SelectedAccount(); got != "" {
		t.Fatalf("initial selection = %q, want the tool default", got)
	}
	press("right")
	if got := d.SelectedAccount(); got != "personal" {
		t.Fatalf("after one right = %q, want personal", got)
	}
	press("right")
	if got := d.SelectedAccount(); got != "work" {
		t.Fatalf("after two rights = %q, want work", got)
	}
	// Right clamps at the end rather than wrapping.
	press("right")
	if got := d.SelectedAccount(); got != "work" {
		t.Fatalf("right at the end = %q, want work (clamped)", got)
	}
	press("left")
	press("left")
	if got := d.SelectedAccount(); got != "" {
		t.Fatalf("back at the start = %q, want the tool default", got)
	}
	// Left clamps too.
	press("left")
	if got := d.SelectedAccount(); got != "" {
		t.Fatalf("left at the start = %q, want the tool default (clamped)", got)
	}
	// Space wraps.
	press("space")
	press("space")
	press("space")
	if got := d.SelectedAccount(); got != "" {
		t.Fatalf("space wrapped past the end = %q, want the tool default", got)
	}
}

// TestAccountPickSurvivesToolSwitchWhenSharedName: a name configured for both
// families stays selected across a tool change.
func TestAccountPickSurvivesToolSwitchWhenSharedName(t *testing.T) {
	accountDialogConfig(t, `
[profiles.work.claude]
config_dir = "~/.agent-accounts/claude/work"

[profiles.work.codex]
codex_home = "~/.agent-accounts/codex/work"

[profiles.personal.claude]
config_dir = "~/.agent-accounts/claude/personal"
`)

	d := NewNewDialog()
	d.SetSize(120, 50)
	d.ShowInGroup("default", "default", "/tmp", nil, "")
	selectTool(t, d, "claude")
	d.accountCursor = indexOfAccount(d, "work")

	selectTool(t, d, "codex")
	if got := d.SelectedAccount(); got != "work" {
		t.Fatalf("shared account name should survive the tool switch, got %q", got)
	}

	// A name the new tool does not have is dropped rather than carried over.
	selectTool(t, d, "claude")
	d.accountCursor = indexOfAccount(d, "personal")
	selectTool(t, d, "codex")
	if got := d.SelectedAccount(); got == "personal" {
		t.Fatal("a claude-only account must not survive a switch to codex")
	}
}

// TestAccountHintLineNamesEnvVarAndHome: the detail line is what tells the user
// what the pick actually does, so it must carry the env var and the path.
func TestAccountHintLineNamesEnvVarAndHome(t *testing.T) {
	home := accountDialogConfig(t, dialogReferenceConfig)

	d := NewNewDialog()
	d.SetSize(120, 50)
	d.ShowInGroup("default", "default", "/tmp", nil, "")
	selectTool(t, d, "codex")
	d.accountCursor = indexOfAccount(d, "codex-seminno")

	hint := d.accountHintLine()
	wantHome := filepath.Join(home, ".agent-accounts/codex/semanticinnovations")
	if !strings.Contains(hint, "CODEX_HOME=") || !strings.Contains(hint, wantHome) {
		t.Fatalf("hint = %q, want it to name CODEX_HOME and %s", hint, wantHome)
	}

	d.accountCursor = 0
	if hint := d.accountHintLine(); !strings.Contains(hint, "tool default home") {
		t.Fatalf("default-pick hint = %q, want it to say the tool default home is used", hint)
	}
}

func accountNames(d *NewDialog) []string {
	names := make([]string, 0, len(d.accounts))
	for _, a := range d.accounts {
		names = append(names, a.Name)
	}
	return names
}

func indexOfAccount(d *NewDialog, name string) int {
	for i, a := range d.accounts {
		if a.Name == name {
			return i + 1
		}
	}
	return 0
}
