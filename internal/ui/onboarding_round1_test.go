package ui

// ASK-211 onboarding, review round 1 (plan and prototype by Sharjeel):
// regressions for the findings of the first review walk.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// profileDB creates profile's state.db in the test HOME and returns it
// open; the caller's writes are what another profile recorded.
func profileDB(t *testing.T, profile string) *statedb.StateDB {
	t.Helper()
	path, err := session.GetDBPathForProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A decline of the Claude hooks in one profile holds in a new profile: the
// hooks go into Claude's shared settings, so the new profile's first form
// must not tick them back on.
func TestOnboardingRound1_HooksDeclineInAnotherProfileCounts(t *testing.T) {
	newOnboardingEnv(t, "claude")
	if got := claudeHooksPrompted(""); got != "" {
		t.Fatalf("no profile answered yet, got %q", got)
	}
	work := profileDB(t, "work")
	if err := work.SetMeta("hooks_prompted", "accepted"); err != nil {
		t.Fatal(err)
	}
	if got := claudeHooksPrompted(""); got != "accepted" {
		t.Fatalf("an accept in another profile: got %q", got)
	}
	def := profileDB(t, "default")
	if err := def.SetMeta("hooks_prompted", "declined"); err != nil {
		t.Fatal(err)
	}
	if got := claudeHooksPrompted(""); got != "declined" {
		t.Fatalf("a decline in another profile must win: got %q", got)
	}
	if d := claudeHooksStartupDecision(false, false, claudeHooksPrompted("")); d.prompt || d.reinstall {
		t.Fatalf("declined elsewhere still asks or installs: %+v", d)
	}
	if got := claudeHooksPrompted("accepted"); got != "accepted" {
		t.Fatalf("this profile's own answer comes first: got %q", got)
	}
}

// Sign-in runs the tool's login in the terminal: no session is created,
// the check page says nothing about coming back, re-checks on return, and
// Enter still reaches the welcome.
func TestOnboardingRound1_SignInLeavesNoSession(t *testing.T) {
	e := newOnboardingEnv(t, "claude")
	h := startDeck(t)
	e.settle(h, 2)
	h.Update(runeKey('l'))
	e.mu.Lock()
	n := len(e.logins)
	e.mu.Unlock()
	if n != 1 {
		t.Fatalf("l ran %d logins, want 1", n)
	}
	if h.hasAnySession() {
		t.Fatal("sign-in added a session to the list")
	}
	if screen := onboardingScreen(h); strings.Contains(screen, "Come back") || strings.Contains(screen, "claude-login") {
		t.Fatalf("check page still talks about a login session:\n%s", screen)
	}
	// The login exits without signing in: the page says so.
	h.Update(onboardingLoginDoneMsg{agent: "claude"})
	e.settle(h, 2)
	if screen := onboardingScreen(h); !strings.Contains(screen, "not signed in yet") {
		t.Fatalf("no result after a login that did not sign in:\n%s", screen)
	}
	// Signed in the second time.
	e.setStatus("claude", 0)
	h.Update(runeKey('l'))
	h.Update(onboardingLoginDoneMsg{agent: "claude"})
	e.settle(h, 2)
	if screen := onboardingScreen(h); !strings.Contains(screen, "is signed in") {
		t.Fatalf("no confirmation after the login:\n%s", screen)
	}
	h.Update(keyEnter)
	if !h.welcomeActive() {
		t.Fatalf("Enter after sign-in skipped the welcome:\n%s", onboardingScreen(h))
	}
}

// Quit on the welcome (first run already finished), start again, create
// the first session: the hint bar still starts at step 1.
func TestOnboardingRound1_QuitOnWelcomeThenHintsOnNextStart(t *testing.T) {
	newOnboardingEnv(t, "claude")
	h := startDeck(t)
	h.Update(keyEnter) // check page -> welcome; writes config.toml
	if !h.welcomeActive() {
		t.Fatal("precondition: the welcome is on screen")
	}
	// Quit here; the next start finds config.toml and no session.
	h2 := startDeck(t)
	if checkVisible(h2) {
		t.Fatal("the check page came back with nothing wrong")
	}
	if !h2.hints.armed {
		t.Fatal("the hint bar was not armed on the start after quitting on the welcome")
	}
	h2.Update(sessionCreatedMsg{instance: session.NewInstanceWithTool("my-app", onboardingFolder(), "claude")})
	if line := hintBarLine(h2); !strings.Contains(line, "Step 1 of 4") {
		t.Fatalf("first session after a relaunch: hint bar %q, want step 1", line)
	}
	// Quit at step 1 (round 3): the next start resumes the tour; once Esc
	// hides it, a later start does not arm it again.
	h3 := startDeck(t, session.NewInstanceWithTool("my-app", onboardingFolder(), "claude"))
	if line := hintBarLine(h3); !strings.Contains(line, "Step 1 of 4") {
		t.Fatalf("restart at step 1: hint bar %q, want step 1", line)
	}
	h3.handleMainKey(keyEsc)
	h4 := startDeck(t)
	if h4.hints.armed {
		t.Fatal("the hint bar armed again after Esc hid it")
	}
}

// welcomeImportSeams replaces the tmux lookups of the welcome's Import.
func welcomeImportSeams(t *testing.T, names []string, namesErr error, discovered []*session.Instance) {
	t.Helper()
	prevNames, prevDiscover := welcomeTmuxSessionNames, welcomeDiscoverSessions
	t.Cleanup(func() { welcomeTmuxSessionNames, welcomeDiscoverSessions = prevNames, prevDiscover })
	welcomeTmuxSessionNames = func() ([]string, error) { return names, namesErr }
	welcomeDiscoverSessions = func([]*session.Instance) ([]*session.Instance, error) {
		if namesErr != nil {
			return nil, namesErr
		}
		return discovered, nil
	}
}

// The welcome's Import counts what it would bring in and never takes a
// tmux session another profile already owns.
func TestOnboardingRound1_WelcomeImportSkipsOtherProfilesSessions(t *testing.T) {
	newOnboardingEnv(t, "claude")
	mine := session.NewInstanceWithTool("my-app", "/w/my-app", "claude")
	theirs := session.NewInstanceWithTool("scratch", "/w/scratch", "shell")
	owned := mine.GetTmuxSession().Name
	other := profileDB(t, "empty2")
	if err := other.SaveInstance(&statedb.InstanceRow{ID: "x1", Title: "my-app", ProjectPath: "/w/my-app", GroupPath: "my-sessions", Tool: "claude", TmuxSession: owned}); err != nil {
		t.Fatal(err)
	}
	welcomeImportSeams(t, []string{owned, theirs.GetTmuxSession().Name}, nil, []*session.Instance{mine, theirs})

	h := startDeck(t)
	h.Update(keyEnter)
	if !h.welcomeActive() {
		t.Fatal("precondition: the welcome is on screen")
	}
	if screen := stripAnsi(h.renderWelcome(120, 40)); !strings.Contains(screen, "Import your tmux sessions (1 found)") {
		t.Fatalf("welcome does not count the importable sessions:\n%s", screen)
	}
	_, cmd := h.Update(runeKey('i'))
	if cmd == nil {
		t.Fatal("i on the welcome did nothing")
	}
	if _, ok := cmd().(importReloadMsg); !ok {
		t.Fatal("import did not reload")
	}
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()
	if len(h.instances) != 1 || h.instances[0].GetTmuxSession().Name == owned {
		var got []string
		for _, i := range h.instances {
			got = append(got, i.Title)
		}
		t.Fatalf("imported %v, want only the session no profile owns", got)
	}
}

// No tmux server (a new machine): the row says none found, and i answers
// with a notice instead of failing silently.
func TestOnboardingRound1_WelcomeImportWithoutTmuxServer(t *testing.T) {
	newOnboardingEnv(t, "claude")
	welcomeImportSeams(t, nil, errors.New("failed to list sessions: exit status 1"), nil)
	h := startDeck(t)
	h.Update(keyEnter)
	if screen := stripAnsi(h.renderWelcome(120, 40)); !strings.Contains(screen, "(none found)") {
		t.Fatalf("welcome does not say none found:\n%s", screen)
	}
	_, cmd := h.Update(runeKey('i'))
	if cmd == nil {
		t.Fatal("i on the welcome did nothing")
	}
	msg := cmd()
	if _, ok := msg.(welcomeNoticeMsg); !ok {
		t.Fatalf("i without a tmux server returned %T, want the welcome notice", msg)
	}
	h.Update(msg)
	if screen := stripAnsi(h.renderWelcome(120, 40)); !strings.Contains(screen, "No tmux sessions to import") {
		t.Fatalf("no notice on the welcome:\n%s", screen)
	}
}

// A user whose only agent is a custom tool is offered that tool, not told
// that no agent is installed.
func TestOnboardingRound1_WelcomeOffersInstalledCustomTool(t *testing.T) {
	e := newOnboardingEnv(t)
	cfg := &session.UserConfig{Tools: map[string]session.ToolDef{"mybot": {Command: "mybot --fast"}}}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	e.setFound("mybot")
	h := startDeck(t)
	h.refreshWelcomeAgents()
	if got := h.welcomeTool(); got != "mybot" {
		t.Fatalf("welcome tool = %q, want the installed custom tool", got)
	}
	screen := stripAnsi(h.renderWelcome(120, 40))
	if strings.Contains(screen, "No agent found") || strings.Contains(screen, "Plain shell") {
		t.Fatalf("welcome sends a custom-tool user to a plain shell:\n%s", screen)
	}
}

// The first session's form is short (prototype): Name, Agent, Path, the
// hooks line and More options. Model and the tool's options wait there.
func TestOnboardingRound1_FirstSessionFormIsShort(t *testing.T) {
	newOnboardingEnv(t, "claude")
	h := startDeck(t)
	h.pendingHooksPrompt = true
	h.Update(keyEnter) // check page -> welcome
	h.Update(keyEnter) // welcome -> filled form
	if !h.newDialog.IsVisible() {
		t.Fatal("precondition: the form is open")
	}
	h.newDialog.SetSize(80, 24)
	form := stripAnsi(h.newDialog.View())
	for _, hidden := range []string{"Model ID", "Reasoning effort", "Claude Options", "Skip permissions"} {
		if strings.Contains(form, hidden) {
			t.Fatalf("first-session form shows %q before More options:\n%s", hidden, form)
		}
	}
	// At 80x24 everything up to More options is on screen.
	for _, want := range []string{"Name:", "Agent:", "Path:", "Show me when Claude", "More options", "←→ agent"} {
		if !strings.Contains(form, want) {
			t.Fatalf("first-session form lacks %q:\n%s", want, form)
		}
	}
	d := h.newDialog
	d.focusIndex = d.indexOf(focusMore)
	d.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	d.SetSize(120, 60)
	open := stripAnsi(d.View())
	for _, want := range []string{"Model ID", "Claude Options", "Create in worktree"} {
		if !strings.Contains(open, want) {
			t.Fatalf("More options does not show %q:\n%s", want, open)
		}
	}
	if d.indexOf(focusModel) < d.indexOf(focusMore) {
		t.Fatal("the model row comes before More options in the Tab order")
	}
}

// A profile that has sessions keeps the full form.
func TestOnboardingRound1_FormWithSessionsKeepsModelRow(t *testing.T) {
	newOnboardingEnv(t, "claude")
	h := startDeck(t, session.NewInstanceWithTool("old", "/w/old", "claude"))
	h.openLocalNewSessionDialog()
	h.newDialog.SetDefaultTool("claude")
	h.newDialog.SetSize(120, 60)
	if form := stripAnsi(h.newDialog.View()); !strings.Contains(form, "Model ID") {
		t.Fatalf("a profile with sessions lost the model row:\n%s", form)
	}
}

// The onboarding boxes keep the box colour behind every cell: no strip of
// terminal background behind a name, a detail or a footer.
func TestOnboardingRound1_BoxesHaveNoBackgroundHoles(t *testing.T) {
	forceTrueColorProfile()
	for _, theme := range []string{"dark", "light"} {
		t.Run(theme, func(t *testing.T) {
			useThemeForTest(t, theme)
			e := newOnboardingEnv(t, "claude")
			welcomeImportSeams(t, nil, nil, nil)
			h := startDeck(t)
			e.settle(h, 2)
			views := map[string]string{"check": h.onboarding.View()}
			h.Update(keyEnter)
			views["welcome"] = h.renderWelcome(120, 40)
			h.Update(runeKey('?'))
			views["help-5-keys"] = h.helpOverlay.View()
			for name, view := range views {
				for i, row := range strings.Split(view, "\n") {
					if holes := backgroundHoles(row); len(holes) > 0 {
						t.Errorf("%s row %d shows the terminal background at %q\nplain=%q", name, i, holes, stripAnsi(row))
					}
				}
			}
		})
	}
}

// The login process runs the command as written, a quoted config-dir
// prefix included, in the folder agent-deck started from.
func TestOnboardingRound1_LoginCommandKeepsConfigDirPrefix(t *testing.T) {
	dir := t.TempDir()
	cmd := onboardingLoginCommand(onboardingLoginRequest{
		Agent: "claude", Path: dir,
		Command: "CLAUDE_CONFIG_DIR=" + shellQuoteOnboarding("/tmp/a b") + " sh -c 'printf %s \"$CLAUDE_CONFIG_DIR\"; pwd'",
	})
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := "/tmp/a b" + dir + "\n"; string(out) != want {
		t.Fatalf("login ran with %q, want %q", out, want)
	}
}
