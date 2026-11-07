package ui

// ASK-211 onboarding: the New Session form (plan and prototype by Sharjeel).
// Default tool = first installed agent, missing tools grey with a warning
// and an install line (Create still goes ahead), ~ paths, Path visible at 80x24, "More options",
// and the Claude hooks line that replaced the hooks box.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// stubToolInstalled makes exactly the named tools installed for this test.
func stubToolInstalled(t *testing.T, names ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	prev := newDialogToolInstalled
	newDialogToolInstalled = func(name string) bool { return set[name] }
	t.Cleanup(func() { newDialogToolInstalled = prev })
}

// onboardingDialog is a local dialog opened the way Home opens it: probe,
// pick the initial tool, show.
func onboardingDialog(t *testing.T, configDefault string, w, h int) *NewDialog {
	t.Helper()
	d := NewNewDialog()
	d.SetSize(w, h)
	d.RefreshInstalledTools()
	d.SetDefaultTool(initialNewSessionTool(configDefault, "", d.FirstInstalledAgent()))
	d.ShowInGroup("", "", t.TempDir(), nil, "")
	return d
}

func TestInitialNewSessionTool(t *testing.T) {
	cases := []struct {
		name, config, remembered, first, want string
	}{
		{"nothing configured: first installed agent", "", "", "codex", "codex"},
		{"config wins over first installed", "pi", "", "codex", "pi"},
		{"remembered wins over first installed", "", "gemini", "codex", "gemini"},
		{"no agent installed: shell", "", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := initialNewSessionTool(tc.config, tc.remembered, tc.first); got != tc.want {
				t.Fatalf("initialNewSessionTool(%q,%q,%q) = %q, want %q", tc.config, tc.remembered, tc.first, got, tc.want)
			}
		})
	}
}

func TestNewDialogOnboarding_DefaultIsFirstInstalledAgent(t *testing.T) {
	stubToolInstalled(t, "codex", "gemini")
	d := onboardingDialog(t, "", 120, 40)
	// Existing order is claude, gemini, ..., codex: gemini comes first.
	if got := d.GetSelectedCommand(); got != "gemini" {
		t.Fatalf("selected = %q, want gemini (first installed agent)", got)
	}
	// Installed agents first, then shell, then the missing tools.
	want := []string{"gemini", "codex", "", "claude"}
	for i, w := range want {
		if d.presetCommands[i] != w {
			t.Fatalf("tool row = %q, want it to start %q", d.presetCommands, want)
		}
	}
}

func TestNewDialogOnboarding_ConfiguredDefaultWins(t *testing.T) {
	stubToolInstalled(t, "claude")
	d := onboardingDialog(t, "pi", 120, 40)
	if got := d.GetSelectedCommand(); got != "pi" {
		t.Fatalf("selected = %q, want pi (configured default wins, even when missing)", got)
	}
	shell := onboardingDialog(t, "shell", 120, 40)
	if got := shell.GetSelectedCommand(); got != "" {
		t.Fatalf("default_tool = shell selected %q, want the plain shell", got)
	}
}

func TestNewDialogOnboarding_NoAgentInstalledFallsBackToShell(t *testing.T) {
	stubToolInstalled(t)
	d := onboardingDialog(t, "", 120, 40)
	if got := d.GetSelectedCommand(); got != "" {
		t.Fatalf("selected = %q, want the plain shell", got)
	}
	if d.FirstInstalledAgent() != "" {
		t.Fatalf("FirstInstalledAgent = %q, want none", d.FirstInstalledAgent())
	}
	if msg := d.Validate(); msg != "" {
		t.Fatalf("shell must be creatable, got %q", msg)
	}
}

func TestNewDialogOnboarding_MissingToolGreyWarningCreateAllowed(t *testing.T) {
	forceTrueColorProfile()
	stubToolInstalled(t, "claude")
	d := onboardingDialog(t, "", 120, 50)
	d.focusIndex = d.indexOf(focusCommand)
	d.updateFocus()
	if strings.Contains(stripAnsi(d.View()), "not found here") {
		t.Fatal("an installed pick must not show the missing warning")
	}
	// Claude, shell, then gemini (missing): two steps right.
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRight})
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := d.GetSelectedCommand(); got != "gemini" {
		t.Fatalf("selected = %q, want gemini", got)
	}
	view := stripAnsi(d.View())
	for _, want := range []string{"grey = not installed", "Gemini CLI not found here; Create still tries it.", "Install: npm install -g @google/gemini-cli"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}
	// A warning, not a refusal: the probe cannot see aliases or a PATH set
	// in the user's shell rc files, which the pane may still use.
	d.nameInput.SetValue("demo")
	if msg := d.ShowValidationError(); msg != "" {
		t.Fatalf("Create on a tool not found = %q, want it allowed (warning only)", msg)
	}
	// The grey pill is styled differently from an installed one.
	missing := renderToolPillsMarked([]string{"a", "b"}, 0, []bool{false, true}, "", 0)
	plain := renderToolPillsMarked([]string{"a", "b"}, 0, []bool{false, false}, "", 0)
	if missing == plain {
		t.Fatal("a missing tool renders the same as an installed one")
	}
}

func TestNewDialogOnboarding_RemoteTargetNotProbed(t *testing.T) {
	stubToolInstalled(t)
	d := NewNewDialog()
	d.remoteTarget = true
	d.RefreshInstalledTools()
	if d.toolInstalled != nil {
		t.Fatal("a remote opening must not mark tools by this machine's PATH")
	}
}

func TestNewDialogOnboarding_PathShownWithTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	project := filepath.Join(home, "code", "my-app")
	if got := tildePath(project); got != "~/code/my-app" {
		t.Fatalf("tildePath(%q) = %q", project, got)
	}
	if got := tildePath("/srv/x"); got != "/srv/x" {
		t.Fatalf("tildePath outside home = %q", got)
	}
	d := NewNewDialog()
	d.ShowInGroup("", "", project, nil, "")
	if got := d.pathInput.Value(); got != "~/code/my-app" {
		t.Fatalf("path field = %q, want ~/code/my-app", got)
	}
	_, path, _ := d.GetValues()
	if path != project {
		t.Fatalf("GetValues path = %q, want %q (~ expanded)", path, project)
	}
}

func TestNewDialogOnboarding_PathVisibleAt80x24(t *testing.T) {
	stubToolInstalled(t, "claude", "codex")
	d := onboardingDialog(t, "", 80, 24)
	d.SetClaudeHooksUndecided(true)
	view := stripAnsi(d.View())
	lines := strings.Split(view, "\n")
	if len(lines) > 24 {
		t.Fatalf("dialog is %d rows at 80x24", len(lines))
	}
	found := map[string]bool{}
	for _, l := range lines {
		for _, f := range []string{"Name:  >", "Agent:", "Path:  >"} {
			if strings.Contains(l, f) {
				found[f] = true
			}
		}
	}
	for _, f := range []string{"Name:  >", "Agent:", "Path:  >"} {
		if !found[f] {
			t.Fatalf("80x24 view lacks %q on one line:\n%s", f, view)
		}
	}
}

// The hooks line is what the first Enter agrees to, so it must be on screen
// at 80x24 with nothing scrolled: right under Path, as in the prototype, and
// the next row Tab reaches after Path.
func TestNewDialogOnboarding_HooksLineVisibleAt80x24(t *testing.T) {
	stubToolInstalled(t, "claude")
	d := onboardingDialog(t, "", 80, 24)
	d.SetClaudeHooksUndecided(true)
	if got := d.GetSelectedCommand(); got != "claude" {
		t.Fatalf("precondition: tool = %q, want claude", got)
	}
	view := stripAnsi(d.View())
	if !strings.Contains(view, "Show me when Claude is busy") {
		t.Fatalf("80x24 form hides the hooks line:\n%s", view)
	}
	if strings.Index(view, "Show me when Claude") < strings.Index(view, "Path:  >") {
		t.Fatalf("hooks line is above Path:\n%s", view)
	}
	if model := strings.Index(view, "Model ID:"); model >= 0 && model < strings.Index(view, "Show me when Claude") {
		t.Fatalf("hooks line sits under the model rows:\n%s", view)
	}
	if p, hk := d.indexOf(focusPath), d.indexOf(focusHooks); hk != p+1 {
		t.Fatalf("focus order: Path at %d, hooks at %d; want hooks right after Path", p, hk)
	}
}

func TestNewDialogOnboarding_MoreOptionsToggle(t *testing.T) {
	d := onboardingDialog(t, "", 120, 50)
	for _, target := range []focusTarget{focusWorktree, focusSandbox, focusMultiRepo} {
		if d.indexOf(target) >= 0 {
			t.Fatalf("%v is reachable with More options closed", target)
		}
	}
	view := stripAnsi(d.View())
	if !strings.Contains(view, "▸ More options: worktree, Docker sandbox, multi-repo") || strings.Contains(view, "Create in worktree") {
		t.Fatalf("closed More options view wrong:\n%s", view)
	}
	d.focusIndex = d.indexOf(focusMore)
	d.updateFocus()
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeySpace})
	for _, target := range []focusTarget{focusWorktree, focusSandbox, focusMultiRepo} {
		if d.indexOf(target) < 0 {
			t.Fatalf("%v not reachable after opening More options", target)
		}
	}
	view = stripAnsi(d.View())
	for _, want := range []string{"▾ More options", "Create in worktree", "Run in Docker sandbox", "Multi-repo mode"} {
		if !strings.Contains(view, want) {
			t.Fatalf("open More options lacks %q:\n%s", want, view)
		}
	}
	// Space again closes it.
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeySpace})
	if d.indexOf(focusWorktree) >= 0 {
		t.Fatal("More options did not close")
	}
	// The w shortcut on the tool row (a non-shell tool, so the custom
	// command field does not take the key) still works and shows its rows.
	d.SetDefaultTool("claude")
	d.focusIndex = d.indexOf(focusCommand)
	d.updateFocus()
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if !d.worktreeEnabled || d.indexOf(focusBranch) < 0 {
		t.Fatal("w on the tool row must enable the worktree and show its rows")
	}
}

func TestNewDialogOnboarding_HooksLine(t *testing.T) {
	stubToolInstalled(t, "claude")
	d := onboardingDialog(t, "", 120, 50)
	if d.indexOf(focusHooks) >= 0 {
		t.Fatal("hooks line shown although the question is decided")
	}
	d.SetClaudeHooksUndecided(true)
	idx := d.indexOf(focusHooks)
	if idx < 0 {
		t.Fatal("hooks line missing for Claude with hooks undecided")
	}
	view := stripAnsi(d.View())
	if !strings.Contains(view, "[x] Show me when Claude is busy or needs me") {
		t.Fatalf("hooks line not ticked by default:\n%s", view)
	}
	if asked, install := d.HooksDecision(); !asked || !install {
		t.Fatalf("HooksDecision = %v,%v want asked+install", asked, install)
	}
	d.focusIndex = idx
	d.updateFocus()
	d, _ = d.Update(tea.KeyMsg{Type: tea.KeySpace})
	if asked, install := d.HooksDecision(); !asked || install {
		t.Fatalf("after Space HooksDecision = %v,%v want asked+skip", asked, install)
	}
	if !strings.Contains(stripAnsi(d.View()), "[ ] Show me when Claude") {
		t.Fatal("unticked hooks line not rendered")
	}
	// Not Claude: no line, no decision.
	d.SetDefaultTool("")
	d.updateToolOptions()
	if d.indexOf(focusHooks) >= 0 {
		t.Fatal("hooks line shown for the shell")
	}
	if asked, _ := d.HooksDecision(); asked {
		t.Fatal("HooksDecision asked for the shell")
	}
}

// Creating from the form answers the question exactly as the old box did.
func TestNewDialogOnboarding_HooksLineInstallsOrSkipsOnCreate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		untick  bool
		install bool
	}{{"ticked installs", false, true}, {"unticked skips", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			stubToolInstalled(t, "claude")
			installs := 0
			prev := claudeHooksInstaller
			claudeHooksInstaller = func(string) error { installs++; return nil }
			t.Cleanup(func() { claudeHooksInstaller = prev })

			h := NewHome()
			h.width, h.height = 120, 40
			h.pendingHooksPrompt = true
			h.openLocalNewSessionDialog()
			h.newDialog.SetDefaultTool("claude")
			h.newDialog.updateToolOptions()
			h.newDialog.SetSize(120, 50)
			h.newDialog.nameInput.SetValue("demo")
			h.newDialog.pathInput.SetValue(t.TempDir())
			if tc.untick {
				h.newDialog.hooksTicked = false
			}
			if asked, _ := h.newDialog.HooksDecision(); !asked {
				t.Fatal("precondition: the form asks the hooks question")
			}
			h.handleNewDialogKey(tea.KeyMsg{Type: tea.KeyEnter})
			if h.newDialog.IsVisible() {
				t.Fatalf("form still open: %q", h.newDialog.validationErr)
			}
			if h.pendingHooksPrompt {
				t.Fatal("the hooks question is still open after create")
			}
			if got := installs == 1; got != tc.install {
				t.Fatalf("installs = %d, want install=%v", installs, tc.install)
			}
			if h.confirmDialog.IsVisible() {
				t.Fatal("a hooks box appeared")
			}
		})
	}
}

// The Welcome fills the folder through SetPath; it is shown with ~ too.
func TestNewDialogOnboarding_SetPathUsesTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	d := NewNewDialog()
	d.Show()
	d.SetPath(filepath.Join(home, "code", "x"))
	if got := d.pathInput.Value(); got != "~/code/x" {
		t.Fatalf("SetPath shows %q, want ~/code/x", got)
	}
}
