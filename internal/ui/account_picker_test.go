package ui

// #924 follow-up — picking a Claude account from the TUI.
//
// The CLI could already create a session under a named account
// (`launch --account`) and move one between accounts
// (`session switch-account`), but the TUI could only DISPLAY the slot on the
// session card. These tests lock the two selection surfaces: the account row
// in the New Session dialog's Claude options panel, and the account row in the
// Edit Session dialog.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	tea "github.com/charmbracelet/bubbletea"
)

// withAccountsConfig points the config loader at a temp HOME whose config.toml
// declares the named accounts, and returns the loaded config.
func withAccountsConfig(t *testing.T, accounts ...string) *session.UserConfig {
	t.Helper()
	tempDir := t.TempDir()
	t.Setenv("HOME", tempDir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tempDir, ".config"))

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	if err := os.MkdirAll(filepath.Join(tempDir, ".agent-deck"), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	cfg := &session.UserConfig{Profiles: map[string]session.ProfileSettings{}}
	for _, name := range accounts {
		cfg.Profiles[name] = session.ProfileSettings{
			Claude: session.ProfileClaudeSettings{ConfigDir: filepath.Join(tempDir, ".claude-"+name)},
		}
	}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	session.ClearUserConfigCache()

	loaded, err := session.LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	return loaded
}

// A machine with a single login has nothing to choose between, so the row must
// not exist at all — no dead control, and no shifted focus indices for the
// options that were there before.
func TestClaudeOptionsAccountRowHiddenWithoutAccounts(t *testing.T) {
	p := NewClaudeOptionsPanel()
	before := p.getFocusCount()

	p.SetAccounts(nil)
	if p.hasAccountRow() {
		t.Fatal("no configured accounts must mean no account row")
	}
	if got := p.getFocusCount(); got != before {
		t.Errorf("focus count = %d, want %d unchanged when the row is hidden", got, before)
	}
	if strings.Contains(p.View(), "Account:") {
		t.Error("the panel must not render an account row it cannot populate")
	}
	if p.GetAccount() != "" {
		t.Errorf("GetAccount() = %q, want empty", p.GetAccount())
	}
}

// The row defaults to "inherit" (no per-session slot) so opening the dialog
// and pressing Enter keeps today's behaviour, and cycles through every
// configured account.
func TestClaudeOptionsAccountRowCycles(t *testing.T) {
	p := NewClaudeOptionsPanel()
	p.SetAccounts([]string{"personal", "work"})

	if !p.hasAccountRow() {
		t.Fatal("configured accounts must produce an account row")
	}
	if p.GetAccount() != "" {
		t.Errorf("default selection = %q, want inherit (empty)", p.GetAccount())
	}

	// Focus the row: it is the last focusable control in the panel.
	p.focusIndex = p.getFocusCount() - 1
	if got := p.getFocusType(); got != "account" {
		t.Fatalf("focus type at last index = %q, want \"account\"", got)
	}

	p.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := p.GetAccount(); got != "personal" {
		t.Errorf("after one right: %q, want \"personal\"", got)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := p.GetAccount(); got != "work" {
		t.Errorf("after two rights: %q, want \"work\"", got)
	}
	// Wraps back to inherit rather than sticking on the last account.
	p.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := p.GetAccount(); got != "" {
		t.Errorf("after wrapping: %q, want inherit (empty)", got)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if got := p.GetAccount(); got != "work" {
		t.Errorf("left from inherit: %q, want \"work\" (wrap backwards)", got)
	}

	view := p.View()
	for _, want := range []string{"Account:", "inherit", "personal", "work"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() missing %q; got:\n%s", want, view)
		}
	}
}

// Space toggles every other control in this panel; the account row must follow
// the same convention rather than being keyboard-reachable but inert.
func TestClaudeOptionsAccountRowSpaceCycles(t *testing.T) {
	p := NewClaudeOptionsPanel()
	p.SetAccounts([]string{"work"})
	p.focusIndex = p.getFocusCount() - 1

	p.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	if got := p.GetAccount(); got != "work" {
		t.Errorf("space on the account row: %q, want \"work\"", got)
	}
}

// SetDefaults is the panel's one config hook, so the New Session dialog picks
// up configured accounts without any extra wiring at the call site.
func TestClaudeOptionsSetDefaultsLoadsAccounts(t *testing.T) {
	cfg := withAccountsConfig(t, "work", "personal")

	p := NewClaudeOptionsPanel()
	p.SetDefaults(cfg)

	if !p.hasAccountRow() {
		t.Fatal("SetDefaults must populate the account row from [profiles.*.claude].config_dir")
	}
	p.SetAccount("work")
	if got := p.GetAccount(); got != "work" {
		t.Errorf("SetAccount/GetAccount round trip = %q, want \"work\"", got)
	}
	// An account that is no longer configured falls back to inherit rather
	// than selecting some unrelated neighbour.
	p.SetAccount("deleted-account")
	if got := p.GetAccount(); got != "" {
		t.Errorf("unknown account = %q, want inherit (empty)", got)
	}
}

// Fork inherits the parent session's slot, so the fork panel must stay as it
// was — no extra row, no shifted focus indices.
func TestClaudeOptionsAccountRowAbsentInForkMode(t *testing.T) {
	p := NewClaudeOptionsPanelForFork()
	p.SetAccounts([]string{"work"})
	if p.hasAccountRow() {
		t.Error("fork mode must not offer an account row")
	}
	if strings.Contains(p.View(), "Account:") {
		t.Error("fork view must not render an account row")
	}
}

// The Edit dialog is the only place a RUNNING session's account can be
// changed. The row must appear for claude sessions once accounts exist, and
// report the change through GetChanges so the commit path can route it into
// session.SwitchAccount.
func TestEditSessionDialogAccountRow(t *testing.T) {
	withAccountsConfig(t, "work", "personal")

	inst := sampleInstance()
	inst.Account = "personal"

	d := NewEditSessionDialog()
	d.Show(inst)

	idx := -1
	for i, f := range d.fields {
		if f.key == session.FieldAccount {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("a claude session on a machine with configured accounts must get an account row")
	}
	field := d.fields[idx]
	if got := field.pillOptions[field.pillCursor]; got != "personal" {
		t.Errorf("preselected account = %q, want the session's stored slot \"personal\"", got)
	}
	if got := field.pillLabels[0]; got != "inherit" {
		t.Errorf("first pill label = %q, want \"inherit\"", got)
	}

	// No edit → no change (a save-without-touching must not rewrite the slot).
	for _, c := range d.GetChanges(inst) {
		if c.Field == session.FieldAccount {
			t.Fatalf("untouched account row produced a change to %q", c.Value)
		}
	}

	// Move the pill to "work" and confirm it surfaces as a restart-required
	// change — the running process keeps the login it launched with.
	d.focusIndex = idx
	d.Update(tea.KeyMsg{Type: tea.KeyRight})
	var found *Change
	for i, c := range d.GetChanges(inst) {
		if c.Field == session.FieldAccount {
			found = &d.GetChanges(inst)[i]
		}
	}
	if found == nil {
		t.Fatal("moving the account pill must produce an account change")
	}
	if found.Value != "work" {
		t.Errorf("changed account = %q, want \"work\"", found.Value)
	}
	if found.IsLive {
		t.Error("an account change needs a restart; it must not be reported as live")
	}
}

// Without configured accounts the Edit dialog stays exactly as it was.
func TestEditSessionDialogAccountRowHiddenWithoutAccounts(t *testing.T) {
	withAccountsConfig(t)

	d := NewEditSessionDialog()
	d.Show(sampleInstance())
	for _, f := range d.fields {
		if f.key == session.FieldAccount {
			t.Fatal("no configured accounts must mean no account row")
		}
	}
}

// The conversation migration only makes sense for `claude --resume`, so a
// non-claude session must not be offered the row (SwitchAccount would refuse
// it at commit time).
func TestEditSessionDialogAccountRowClaudeOnly(t *testing.T) {
	withAccountsConfig(t, "work")

	inst := sampleInstance()
	inst.Tool = "codex"
	inst.Command = "codex"

	d := NewEditSessionDialog()
	d.Show(inst)
	for _, f := range d.fields {
		if f.key == session.FieldAccount {
			t.Fatal("a non-claude session must not get the account row")
		}
	}
}

// The Edit dialog's commit path must NOT write the account through the
// generic SetField loop: a bare field write leaves the conversation in the old
// account's config dir and the restarted `claude --resume` finds nothing. This
// locks the routing — the submit produces a command that runs the full
// session.SwitchAccount flow and reports back as an accountSwitchedMsg.
func TestEditSessionDialogCommitRoutesAccountThroughSwitch(t *testing.T) {
	withAccountsConfig(t, "work", "personal")

	home := NewHome()
	home.width, home.height = 120, 40

	inst := session.NewInstanceWithTool("acct-commit", t.TempDir(), "claude")
	inst.Account = "personal"
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	home.editSessionDialog.SetSize(home.width, home.height)
	home.editSessionDialog.Show(inst)

	idx := -1
	for i, f := range home.editSessionDialog.fields {
		if f.key == session.FieldAccount {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("expected an account row on a claude session with configured accounts")
	}
	home.editSessionDialog.focusIndex = idx
	home.editSessionDialog.Update(tea.KeyMsg{Type: tea.KeyRight}) // personal -> work

	_, cmd := home.handleEditSessionDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("committing an account change must return the switch command")
	}
	// The switch flow captures the SOURCE config dir from the pre-switch
	// account. If the generic SetField loop wrote the field first, that capture
	// would read the already-switched account, decide there was nothing to
	// migrate, and resume with no conversation.
	if inst.Account != "personal" {
		t.Fatalf("account = %q before the switch runs; the ordered SetField loop must not write it", inst.Account)
	}
	if _, ok := home.resumingSessions[inst.ID]; !ok {
		t.Error("commit must mark the session resuming so the spinner runs during the switch")
	}
	if home.editSessionDialog.IsVisible() {
		t.Error("the dialog must close on commit")
	}

	msg, ok := cmd().(accountSwitchedMsg)
	if !ok {
		t.Fatalf("commit produced %T, want accountSwitchedMsg", cmd())
	}
	if msg.err != nil {
		t.Fatalf("switch failed: %v", msg.err)
	}
	if !msg.committed {
		t.Error("a successful switch must report committed so the caller persists it")
	}
	if msg.account != "work" {
		t.Errorf("switched to %q, want \"work\"", msg.account)
	}
	if inst.Account != "work" {
		t.Errorf("instance account = %q, want \"work\" (the switch flow owns the write)", inst.Account)
	}
}

// Clearing the slot back to "inherit" is a supported mutation everywhere else
// (SetField treats "" as "drop the override"), so the TUI must be able to undo
// an account assignment. It is NOT a switch: there is no target config dir to
// migrate into, and routing it through SwitchAccount would fail with
// "unknown account slot".
func TestEditSessionDialogClearingAccountIsNotASwitch(t *testing.T) {
	withAccountsConfig(t, "work", "personal")

	home := NewHome()
	home.width, home.height = 120, 40

	inst := session.NewInstanceWithTool("acct-clear", t.TempDir(), "claude")
	inst.Account = "work"
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	home.editSessionDialog.SetSize(home.width, home.height)
	home.editSessionDialog.Show(inst)
	home.editSessionDialog.focusIndex = accountFieldIndex(t, home.editSessionDialog)
	// options are ["", personal, work]; cursor sits on "work" (index 2).
	home.editSessionDialog.Update(tea.KeyMsg{Type: tea.KeyRight}) // wrap to "" (inherit)

	_, cmd := home.handleEditSessionDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	if inst.Account != "" {
		t.Fatalf("account = %q after clearing to inherit, want empty", inst.Account)
	}
	if cmd != nil {
		if _, isSwitch := cmd().(accountSwitchedMsg); isSwitch {
			t.Error("clearing the slot must not run the switch flow — there is no target dir to migrate into")
		}
	}
}

// A stored slot whose profile was removed from config.toml must stay
// selectable, so opening the dialog for an unrelated edit and saving does not
// silently rewrite the account (and does not swallow the restart the other
// edits needed).
func TestEditSessionDialogKeepsUnconfiguredAccountPill(t *testing.T) {
	withAccountsConfig(t, "work", "personal")

	inst := sampleInstance()
	inst.Account = "removed-slot"

	d := NewEditSessionDialog()
	d.Show(inst)

	idx := accountFieldIndex(t, d)
	field := d.fields[idx]
	if got := field.pillOptions[field.pillCursor]; got != "removed-slot" {
		t.Errorf("selected pill = %q, want the stored slot \"removed-slot\"", got)
	}
	if !strings.Contains(field.pillLabels[field.pillCursor], "not configured") {
		t.Errorf("label = %q, want it to mark the slot as no longer configured", field.pillLabels[field.pillCursor])
	}
	for _, c := range d.GetChanges(inst) {
		if c.Field == session.FieldAccount {
			t.Fatalf("save-without-editing rewrote the account to %q", c.Value)
		}
	}
}

// Switching account and changing tool in one submit cannot both apply: the
// switch is claude-only. Refuse the pair before anything is written rather
// than persisting the tool and then failing the switch.
func TestEditSessionDialogRefusesAccountSwitchWithToolChange(t *testing.T) {
	withAccountsConfig(t, "work", "personal")

	home := NewHome()
	home.width, home.height = 120, 40

	inst := session.NewInstanceWithTool("acct-tool", t.TempDir(), "claude")
	inst.Account = "personal"
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID[inst.ID] = inst
	home.instancesMu.Unlock()
	home.groupTree = session.NewGroupTree(home.instances)
	home.rebuildFlatItems()

	home.editSessionDialog.SetSize(home.width, home.height)
	home.editSessionDialog.Show(inst)

	// Move the account row to "work" and the tool row to shell.
	home.editSessionDialog.focusIndex = accountFieldIndex(t, home.editSessionDialog)
	home.editSessionDialog.Update(tea.KeyMsg{Type: tea.KeyRight})
	for i, f := range home.editSessionDialog.fields {
		if f.key == session.FieldTool {
			home.editSessionDialog.focusIndex = i
			home.editSessionDialog.fields[i].pillCursor = 0 // shell
		}
	}

	_, cmd := home.handleEditSessionDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("the conflicting submit must not start a switch")
	}
	if !home.editSessionDialog.IsVisible() {
		t.Error("the dialog must stay open so the user can resolve the conflict")
	}
	if inst.Account != "personal" || inst.Tool != "claude" {
		t.Errorf("nothing must be written: account=%q tool=%q", inst.Account, inst.Tool)
	}
}

// The account pick is per-session, like the start query (#741): a session
// created on "work" must not silently put the NEXT one on "work" too.
func TestNewDialogAccountDoesNotLeakAcrossOpenings(t *testing.T) {
	withAccountsConfig(t, "work", "personal")

	d := NewNewDialog()
	d.SetDefaultTool("claude")
	d.SetSize(120, 50)
	d.ShowInGroup("projects", "Projects", "/tmp", nil, "")
	d.claudeOptions.SetAccount("work")
	if got := d.GetClaudeAccount(); got != "work" {
		t.Fatalf("GetClaudeAccount() = %q, want \"work\"", got)
	}

	d.Hide()
	d.ShowInGroup("projects", "Projects", "/tmp", nil, "")
	if got := d.GetClaudeAccount(); got != "" {
		t.Errorf("reopened dialog carries account %q; each opening must start at inherit", got)
	}
}

// accountFieldIndex returns the index of the account row, failing the test
// when it is absent.
func accountFieldIndex(t *testing.T, d *EditSessionDialog) int {
	t.Helper()
	for i, f := range d.fields {
		if f.key == session.FieldAccount {
			return i
		}
	}
	t.Fatal("expected an account row")
	return -1
}
