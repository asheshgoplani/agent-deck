package ui

// First-run onboarding (ASK-131, ASK-211): the quick check page with tmux,
// agent detection and sign-in, the welcome empty screen (welcome_test.go),
// and the "What you can do" page. Every test runs
// in its own scratch HOME with a fake PATH lookup and a fake status runner;
// no real agent CLI is ever run.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// onboardingEnv is one scratch install: HOME, a fake PATH and a fake status
// runner, plus the commands the overlay queued.
type onboardingEnv struct {
	t      *testing.T
	home   string
	folder string
	found  map[string]bool

	mu       sync.Mutex
	codes    map[string]int   // binary -> exit code of its status command
	errs     map[string]error // binary -> runner error (e.g. a timeout)
	calls    []string         // "binary args..." per status run
	envs     map[string][]string
	queued   []tea.Cmd
	logins   []onboardingLoginRequest
	lookPath []string

	tmuxFound   bool
	tmuxVersion string
}

func newOnboardingEnv(t *testing.T, found ...string) *onboardingEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	folder := filepath.Join(home, "billing-api")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(folder)

	e := &onboardingEnv{t: t, home: home, folder: folder, found: map[string]bool{},
		codes: map[string]int{}, errs: map[string]error{}, envs: map[string][]string{}}
	for _, f := range found {
		e.found[f] = true
	}

	prevLook, prevRun, prevStart, prevObs := onboardingLookPath, onboardingStatusRunner, onboardingStartLogin, onboardingCmdObserver
	prevTmux, prevGOOS, prevVersion := onboardingTmuxCheck, onboardingGOOS, Version
	t.Cleanup(func() {
		onboardingLookPath, onboardingStatusRunner, onboardingStartLogin, onboardingCmdObserver = prevLook, prevRun, prevStart, prevObs
		onboardingTmuxCheck, onboardingGOOS, Version = prevTmux, prevGOOS, prevVersion
	})
	e.tmuxFound, e.tmuxVersion = true, "tmux 3.5a"
	onboardingTmuxCheck = func() (string, bool) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if !e.tmuxFound {
			return "", false
		}
		return e.tmuxVersion, true
	}
	onboardingGOOS = "linux"
	Version = "1.16.36"
	onboardingLookPath = func(bin string) (string, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.lookPath = append(e.lookPath, bin)
		if e.found[bin] {
			return "/fake/bin/" + bin, nil
		}
		return "", errors.New("not found")
	}
	onboardingStatusRunner = func(ctx context.Context, bin string, args, env []string) (int, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.calls = append(e.calls, bin+" "+strings.Join(args, " "))
		e.envs[bin] = env
		if err := e.errs[bin]; err != nil {
			return -1, err
		}
		code, ok := e.codes[bin]
		if !ok {
			return 1, nil
		}
		return code, nil
	}
	onboardingStartLogin = func(req onboardingLoginRequest) tea.Cmd {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.logins = append(e.logins, req)
		return nil
	}
	// The New Session form and the "any agent" check see the same fake PATH.
	prevInstalled := newDialogToolInstalled
	t.Cleanup(func() { newDialogToolInstalled = prevInstalled })
	newDialogToolInstalled = func(name string) bool {
		if name == "" || name == "shell" {
			return true
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.found[name]
	}
	onboardingCmdObserver = func(c tea.Cmd) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.queued = append(e.queued, c)
	}
	return e
}

func (e *onboardingEnv) setStatus(bin string, code int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.codes[bin] = code
}

// settle runs what the overlay queued (status checks, re-check ticks) and
// feeds the results back through Update, a bounded number of rounds.
func (e *onboardingEnv) settle(h *Home, rounds int) {
	e.t.Helper()
	for i := 0; i < rounds; i++ {
		e.mu.Lock()
		cmds := e.queued
		e.queued = nil
		e.mu.Unlock()
		if len(cmds) == 0 {
			return
		}
		for _, c := range cmds {
			for _, msg := range runOnboardingCmd(c) {
				h.Update(msg)
			}
		}
	}
}

func runOnboardingCmd(c tea.Cmd) []tea.Msg {
	if c == nil {
		return nil
	}
	switch m := c().(type) {
	case tea.BatchMsg:
		var out []tea.Msg
		for _, sub := range m {
			out = append(out, runOnboardingCmd(sub)...)
		}
		return out
	case onboardingStatusMsg, onboardingLoginDoneMsg:
		return []tea.Msg{m}
	}
	return nil
}

// startDeck starts agent-deck the way Init and the first session load do.
func startDeck(t *testing.T, instances ...*session.Instance) *Home {
	t.Helper()
	h := NewHome()
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.showSetupWizardIfFirstRun()
	h.Update(loadSessionsMsg{instances: instances})
	return h
}

var (
	keyTab     = tea.KeyMsg{Type: tea.KeyTab}
	keyShowMe  = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}, Alt: true}
	configFile = func(t *testing.T) string {
		p, err := session.GetUserConfigPath()
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
)

func onboardingScreen(h *Home) string { return stripAnsi(h.View()) }

func checkVisible(h *Home) bool {
	return h.onboarding.IsVisible() && h.onboarding.Page() == onboardingPageCheck
}

func (e *onboardingEnv) setTmux(found bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.tmuxFound = found
}

func (e *onboardingEnv) setFound(bins ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, b := range bins {
		e.found[b] = true
	}
}

func tourVisible(h *Home) bool {
	return h.onboarding.IsVisible() && h.onboarding.Page() == onboardingPageTour
}

func TestOnboarding_FreshInstallShowsCheckPageExactlyOnce(t *testing.T) {
	newOnboardingEnv(t, "claude")
	h := startDeck(t)
	if !checkVisible(h) {
		t.Fatal("fresh install: the check page is not shown")
	}
	if h.setupWizard.IsVisible() {
		t.Fatal("fresh install: the old setup wizard is still on screen with the check page")
	}
	screen := onboardingScreen(h)
	for _, want := range []string{"Agent Deck · quick check", "✓ tmux", "tmux 3.5a", "✓ agent-deck", "v1.16.36",
		"Your agents", "✓ Claude Code", "x Codex", "not installed. Install with:", "npm install -g @openai/codex", "Plain shell", "always works", "Enter continue", "r re-check", "q quit"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("Welcome lacks %q:\n%s", want, screen)
		}
	}

	// Second start on the same install, first run not finished (the
	// process ended on the check page): the check page again, not the wizard.
	h2 := startDeck(t)
	if !checkVisible(h2) || !h2.onboarding.firstRun || h2.setupWizard.IsVisible() {
		t.Fatalf("second start before finishing: check=%v firstRun=%v wizard=%v, want the first-run check page", checkVisible(h2), h2.onboarding.firstRun, h2.setupWizard.IsVisible())
	}
	h2.Update(keyEnter) // finish first run
	h3 := startDeck(t)
	if h3.onboarding.IsVisible() || h3.setupWizard.IsVisible() {
		t.Fatalf("start after first run: check=%v wizard=%v, want neither", h3.onboarding.IsVisible(), h3.setupWizard.IsVisible())
	}
}

func TestOnboarding_EscSkipsAndPersists(t *testing.T) {
	newOnboardingEnv(t, "codex")
	h := startDeck(t)
	if !checkVisible(h) {
		t.Fatal("precondition: Welcome shown")
	}
	h.Update(keyEsc)
	if h.onboarding.IsVisible() || h.newDialog.IsVisible() {
		t.Fatal("Esc did not skip the Welcome")
	}
	if _, err := os.Stat(configFile(t)); err != nil {
		t.Fatalf("skip did not finish first run (config.toml): %v", err)
	}
	if got := session.GetDefaultTool(); got != "codex" {
		t.Fatalf("default tool after skip = %q, want the detected codex", got)
	}
	h2 := startDeck(t)
	if h2.onboarding.IsVisible() || h2.setupWizard.IsVisible() {
		t.Fatal("after skip, a new start shows onboarding again")
	}
}

// First run is finished only when config.toml was written: a save that
// fails leaves no first-run marker, so the check page comes back at the next
// start; once a save works, the marker is written and it does not.
func TestOnboarding_FailedConfigSaveKeepsFirstRunOpen(t *testing.T) {
	newOnboardingEnv(t, "claude")
	prevSave := saveFirstRunUserConfig
	t.Cleanup(func() { saveFirstRunUserConfig = prevSave })
	saveFirstRunUserConfig = func(*session.UserConfig) error { return errors.New("disk full") }

	h := startDeck(t)
	if !checkVisible(h) {
		t.Fatal("precondition: fresh install shows the check page")
	}
	h.Update(keyEsc)
	if onboardingFirstRunFinished() || onboardingMarkerExists() {
		t.Fatal("first-run marker written although config.toml failed to save")
	}
	if h.err == nil {
		t.Fatal("the failed save is not shown")
	}
	h2 := startDeck(t)
	if !checkVisible(h2) || !h2.onboarding.firstRun {
		t.Fatal("after a failed save the next start does not bring the first-run check page back")
	}

	saveFirstRunUserConfig = prevSave
	h2.Update(keyEsc)
	if _, err := os.Stat(configFile(t)); err != nil {
		t.Fatalf("config.toml not written: %v", err)
	}
	if !onboardingFirstRunFinished() {
		t.Fatal("first-run marker missing after a successful save")
	}
}

func TestOnboarding_ReopenAfterSkip(t *testing.T) {
	e := newOnboardingEnv(t, "claude")
	h := startDeck(t)
	h.Update(keyEsc)
	if h.onboarding.IsVisible() {
		t.Fatal("precondition: skipped")
	}
	h.Update(keyShowMe)
	if !tourVisible(h) {
		t.Fatalf("%s did not reopen What you can do", keyShowMe.String())
	}
	h.Update(keyTab)
	if !checkVisible(h) {
		t.Fatal("Tab from What you can do did not open the agents page")
	}
	e.setStatus("claude", 0)
	e.settle(h, 2)
	if !strings.Contains(onboardingScreen(h), "signed in") {
		t.Fatalf("reopened Welcome does not check sign-in:\n%s", onboardingScreen(h))
	}
	h.Update(keyEsc)
	if h.onboarding.IsVisible() {
		t.Fatal("Esc did not close the reopened Welcome")
	}
	h.Update(keyShowMe)
	h.Update(keyEsc)
	if h.onboarding.IsVisible() {
		t.Fatal("Esc did not close What you can do")
	}
}

func TestOnboarding_ExistingUserWithSessionsNeverSeesIt(t *testing.T) {
	newOnboardingEnv(t, "claude")
	inst := session.NewInstanceWithTool("existing", t.TempDir(), "claude")
	h := startDeck(t, inst)
	if h.onboarding.IsVisible() {
		t.Fatal("an install with sessions got the Welcome")
	}
	if !h.setupWizard.IsVisible() {
		t.Fatal("an install with sessions but no config.toml must keep the setup wizard as before")
	}
}

func TestOnboarding_ExistingConfigNeverSeesIt(t *testing.T) {
	newOnboardingEnv(t, "claude")
	if err := session.SaveUserConfig(&session.UserConfig{}); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	h := startDeck(t)
	if h.onboarding.IsVisible() || h.setupWizard.IsVisible() {
		t.Fatal("an install with a config.toml got first-run onboarding")
	}
}

func TestOnboarding_AgentDetectionFollowsPath(t *testing.T) {
	cases := []struct {
		name    string
		found   []string
		choices []string
	}{
		{"all", []string{"claude", "codex", "gemini", "pi"}, []string{"claude", "codex", "gemini", "pi", "shell"}},
		{"none", nil, []string{"shell"}},
		{"some", []string{"codex", "pi"}, []string{"codex", "pi", "shell"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newOnboardingEnv(t, tc.found...)
			h := startDeck(t)
			if !checkVisible(h) {
				t.Fatal("Welcome not shown")
			}
			if got := h.onboarding.choices(); strings.Join(got, ",") != strings.Join(tc.choices, ",") {
				t.Fatalf("choices = %v, want %v", got, tc.choices)
			}
			screen := onboardingScreen(h)
			for _, a := range h.onboarding.agents {
				isFound := false
				for _, f := range tc.found {
					isFound = isFound || f == a.Name
				}
				// The command sits on its own line; a long one is cut with
				// … at the end, so look for its start.
				hint := a.Install
				if len(hint) > 40 {
					hint = hint[:40]
				}
				if isFound == strings.Contains(screen, hint) {
					t.Fatalf("%s found=%v but install hint shown=%v:\n%s", a.Name, isFound, !isFound, screen)
				}
			}
			if len(tc.found) == 0 && !strings.Contains(screen, "No agent yet. You can still start with a Plain shell.") {
				t.Fatalf("no agent found, but no plain-shell offer:\n%s", screen)
			}
		})
	}
}

// Check page Enter -> welcome (no modal) -> Enter opens the filled New
// Session form -> Enter creates: the first session is three keys away.
func TestOnboarding_CheckEnterWelcomeEnterEnterCreates(t *testing.T) {
	newOnboardingEnv(t, "claude", "codex")
	h := startDeck(t)
	h.Update(keyEnter)
	if h.onboarding.IsVisible() || h.newDialog.IsVisible() {
		t.Fatal("Enter on the check page did not continue to the welcome")
	}
	if !h.welcomeActive() || !strings.Contains(onboardingScreen(h), "Welcome to Agent Deck") {
		t.Fatalf("after the check page the welcome is not on screen:\n%s", onboardingScreen(h))
	}
	if got := session.GetDefaultTool(); got != "claude" {
		t.Fatalf("default tool after first run = %q, want the first installed claude", got)
	}
	h.Update(keyEnter)
	if !h.newDialog.IsVisible() {
		t.Fatal("Enter on the welcome did not open the New Session dialog")
	}
	if got := h.newDialog.GetSelectedCommand(); got != "claude" {
		t.Fatalf("dialog tool = %q, want the first installed claude", got)
	}
	// The form shows the folder with ~; compare the path it creates in.
	if _, got, _ := h.newDialog.GetValues(); got != onboardingFolder() {
		t.Fatalf("dialog path = %q, want this folder %q", got, onboardingFolder())
	}
	_, cmd := h.Update(keyEnter)
	if !submitted(h, cmd) {
		t.Fatalf("Enter in the filled form did not create (error %q)", h.newDialog.validationErr)
	}
	if got := h.newDialog.nameInput.Value(); got != "billing-api" {
		t.Fatalf("session name = %q, want the folder name", got)
	}
}

func TestOnboarding_NoAgentStartsPlainShell(t *testing.T) {
	newOnboardingEnv(t)
	h := startDeck(t)
	if !checkVisible(h) {
		t.Fatal("precondition: check page")
	}
	screen := onboardingScreen(h)
	for _, want := range []string{"x Claude Code", h.onboarding.agent("claude").Install,
		"No agent yet. You can still start with a Plain shell.", "Enter continue with Plain shell"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("no-agent check page lacks %q:\n%s", want, screen)
		}
	}
	h.Update(keyEnter)
	if h.onboarding.IsVisible() {
		t.Fatal("no agent: Enter must still continue (Plain shell)")
	}
	h.Update(keyEnter)
	if !h.newDialog.IsVisible() {
		t.Fatal("Enter on the welcome did not open the dialog")
	}
	if got := h.newDialog.GetSelectedCommand(); got != "" {
		t.Fatalf("no agent found: dialog tool = %q, want the plain shell", got)
	}
}

// tmux missing: the tmux row says so with the install line, Enter and Esc
// are blocked, r re-checks and, once tmux is there, Enter continues.
func TestOnboarding_TmuxMissingBlocksUntilRecheck(t *testing.T) {
	e := newOnboardingEnv(t, "claude")
	e.setTmux(false)
	h := startDeck(t)
	if !checkVisible(h) {
		t.Fatal("precondition: check page")
	}
	screen := onboardingScreen(h)
	for _, want := range []string{"✗ tmux", "not found. Every session runs inside tmux.", "Debian/Ubuntu:  sudo apt install tmux", "Fedora/RHEL:    sudo dnf install tmux", "Arch:           sudo pacman -S tmux",
		"r re-check after you install", "q quit"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("tmux-missing check page lacks %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "Enter continue") {
		t.Fatalf("tmux missing, yet the footer offers Enter:\n%s", screen)
	}
	for _, k := range []tea.KeyMsg{keyEnter, keyEsc} {
		h.Update(k)
		if !checkVisible(h) {
			t.Fatalf("%s left the check page while tmux is missing", k.String())
		}
	}
	if !strings.Contains(onboardingScreen(h), "agent-deck needs tmux. Install it, then press r.") {
		t.Fatalf("blocked Enter says nothing:\n%s", onboardingScreen(h))
	}
	h.Update(runeKey('r'))
	if !strings.Contains(onboardingScreen(h), "Checked again: tmux is still not found.") {
		t.Fatalf("re-check without tmux:\n%s", onboardingScreen(h))
	}
	e.setTmux(true)
	h.Update(runeKey('r'))
	screen = onboardingScreen(h)
	if !strings.Contains(screen, "Checked again: tmux 3.5a found.") || !strings.Contains(screen, "✓ tmux") {
		t.Fatalf("re-check after installing tmux:\n%s", screen)
	}
	h.Update(keyEnter)
	if h.onboarding.IsVisible() {
		t.Fatal("tmux found again, yet Enter is still blocked")
	}
}

// r re-checks the agents too: one installed meanwhile turns green and the
// line says which.
func TestOnboarding_RecheckFindsNewAgent(t *testing.T) {
	e := newOnboardingEnv(t)
	h := startDeck(t)
	e.setFound("claude")
	h.Update(runeKey('r'))
	screen := onboardingScreen(h)
	for _, want := range []string{"Checked again: Claude Code found.", "✓ Claude Code", "Enter continue"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("re-check lacks %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "No agent yet") {
		t.Fatalf("still says no agent after one was found:\n%s", screen)
	}
	h.Update(runeKey('r'))
	if !strings.Contains(onboardingScreen(h), "Checked again: all good.") {
		t.Fatalf("second re-check:\n%s", onboardingScreen(h))
	}
}

// All green: shown on first run only. Something wrong (no agent CLI) with
// no sessions: shown again at start, not as first run.
func TestOnboarding_ShownAgainOnlyWhenSomethingIsWrong(t *testing.T) {
	e := newOnboardingEnv(t)
	h := startDeck(t)
	h.Update(keyEnter) // finish first run with a plain shell
	h2 := startDeck(t)
	if !checkVisible(h2) || h2.onboarding.firstRun {
		t.Fatalf("no agent at start: check page visible=%v firstRun=%v, want shown, not first run", checkVisible(h2), h2.onboarding.firstRun)
	}
	h2.Update(keyEnter)
	e.setFound("codex")
	h3 := startDeck(t)
	if h3.onboarding.IsVisible() {
		t.Fatal("all green after first run, yet the check page opened again")
	}
	// No agent, but sessions exist: the user is past onboarding.
	newOnboardingEnv(t)
	if err := session.SaveUserConfig(&session.UserConfig{}); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	h4 := startDeck(t, session.NewInstanceWithTool("shell-one", t.TempDir(), "shell"))
	if h4.onboarding.IsVisible() {
		t.Fatal("no agent but a session exists: check page must not nag")
	}
}

// An install whose config.toml predates onboarding (an existing user) never
// gets the check page, even with no sessions and no agent the check page
// knows (ASK-211 review): it is not the install onboarding finished.
func TestOnboarding_ExistingConfigNoSessionsNoKnownAgentNeverSeesIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marker string // "" none, else marker content (older versions: a timestamp)
	}{
		{"no marker", ""},
		{"marker from an older version", "2026-01-02T03:04:05Z\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newOnboardingEnv(t) // nothing on PATH
			if err := session.SaveUserConfig(&session.UserConfig{DefaultTool: "opencode"}); err != nil {
				t.Fatal(err)
			}
			session.ClearUserConfigCache()
			if tc.marker != "" {
				p := onboardingSeenPath()
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(tc.marker), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				h := startDeck(t)
				if h.onboarding.IsVisible() || h.setupWizard.IsVisible() {
					t.Fatalf("start %d: check=%v wizard=%v, want neither for an existing config", i+1, h.onboarding.IsVisible(), h.setupWizard.IsVisible())
				}
			}
		})
	}
}

// On the install onboarding finished, any installed picker tool counts as an
// agent, not only the four the check page lists.
func TestOnboarding_AnyInstalledToolCountsAsAgent(t *testing.T) {
	e := newOnboardingEnv(t)
	h := startDeck(t)
	h.Update(keyEnter) // finish first run with a plain shell
	e.setFound("opencode")
	h2 := startDeck(t)
	if h2.onboarding.IsVisible() {
		t.Fatal("opencode is installed, yet the check page says no agent")
	}
}

// q on the check page at start quits agent-deck; on a page reopened from
// "Show me around" it only closes it.
func TestOnboarding_QuitKey(t *testing.T) {
	newOnboardingEnv(t, "claude")
	h := startDeck(t)
	_, cmd := h.Update(runeKey('q'))
	if h.onboarding.IsVisible() {
		t.Fatal("q left the check page open")
	}
	if cmd == nil && !h.isQuitting {
		t.Fatal("q on the start check page did not quit")
	}
	// q did not finish first run: the next start is the check page again,
	// not the legacy setup wizard and not a config-less main screen.
	if _, err := os.Stat(configFile(t)); err == nil {
		t.Fatal("q on the first-run check page wrote config.toml")
	}
	again := startDeck(t)
	if !checkVisible(again) || !again.onboarding.firstRun || again.setupWizard.IsVisible() {
		t.Fatalf("start after q: check=%v firstRun=%v wizard=%v, want the first-run check page", checkVisible(again), again.onboarding.firstRun, again.setupWizard.IsVisible())
	}

	newOnboardingEnv(t, "claude")
	if err := session.SaveUserConfig(&session.UserConfig{}); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	h2 := startDeck(t)
	h2.Update(keyShowMe)
	h2.Update(keyTab)
	if !checkVisible(h2) {
		t.Fatal("precondition: reopened check page")
	}
	h2.Update(runeKey('q'))
	if h2.onboarding.IsVisible() || h2.isQuitting {
		t.Fatalf("q on a reopened page: visible=%v quitting=%v, want closed, not quitting", h2.onboarding.IsVisible(), h2.isQuitting)
	}
}

// The first session made from the welcome no longer pops "What you can
// do": the hint bar (lane B) teaches the first keys instead.
func TestOnboarding_NoTourAfterFirstCreate(t *testing.T) {
	newOnboardingEnv(t, "claude")
	h := startDeck(t)
	h.Update(keyEnter) // check page -> welcome
	h.Update(keyEnter) // welcome -> form
	h.Update(keyEnter) // create
	inst := session.NewInstanceWithTool("billing-api", onboardingFolder(), "claude")
	h.Update(sessionCreatedMsg{instance: inst})
	if h.onboarding.IsVisible() {
		t.Fatal("What you can do opened after the first create")
	}
}

func TestOnboarding_SignInStatusPerTool(t *testing.T) {
	e := newOnboardingEnv(t, "claude", "codex", "gemini")
	e.setStatus("claude", 0)
	e.setStatus("codex", 1)
	h := startDeck(t)
	if a := h.onboarding.agent("claude"); a.Auth != authChecking {
		t.Fatalf("claude state before the check returns = %v, want checking", a.Auth)
	}
	e.settle(h, 2)
	want := map[string]onboardingAuth{"claude": authSignedIn, "codex": authSignedOut, "gemini": authNoStatus}
	for name, w := range want {
		if got := h.onboarding.agent(name).Auth; got != w {
			t.Fatalf("%s sign-in = %v, want %v", name, got, w)
		}
	}
	e.mu.Lock()
	calls := strings.Join(e.calls, "|")
	e.mu.Unlock()
	if !strings.Contains(calls, "claude auth status") || !strings.Contains(calls, "codex login status") || strings.Contains(calls, "gemini") {
		t.Fatalf("status commands run = %q", calls)
	}
	if strings.Contains(calls, " -p") {
		t.Fatalf("a status check used -p: %q", calls)
	}
	screen := onboardingScreen(h)
	for _, w := range []string{"installed · signed in", "installed · not signed in", "installed · you sign in on the first session"} {
		if !strings.Contains(screen, w) {
			t.Fatalf("screen lacks %q:\n%s", w, screen)
		}
	}
}

func TestOnboarding_SignInTimeoutAndUnknown(t *testing.T) {
	e := newOnboardingEnv(t, "claude", "codex")
	e.errs["claude"] = context.DeadlineExceeded
	e.errs["codex"] = errors.New("exec format error")
	h := startDeck(t)
	e.settle(h, 2)
	if got := h.onboarding.agent("claude").Auth; got != authTimedOut {
		t.Fatalf("claude = %v, want timed out", got)
	}
	if got := h.onboarding.agent("codex").Auth; got != authUnknown {
		t.Fatalf("codex = %v, want unknown", got)
	}
	screen := onboardingScreen(h)
	if !strings.Contains(screen, "sign-in check timed out") || !strings.Contains(screen, "sign-in unknown") {
		t.Fatalf("timeout / unknown not shown:\n%s", screen)
	}
}

// The real runner gives up at the timeout instead of hanging the overlay.
func TestOnboarding_RealRunnerTimesOut(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = runOnboardingStatus(ctx, sleep, []string{"5"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("runner did not stop at the timeout")
	}
}

func TestOnboarding_LogInStartsToolsOwnLoginAndRecheckConfirms(t *testing.T) {
	for _, tc := range []struct {
		agent, command, signedIn string
	}{
		{"claude", "claude auth login", ""},
		{"codex", "codex login", "claude"}, // l signs in to the first agent not signed in
	} {
		t.Run(tc.agent, func(t *testing.T) {
			e := newOnboardingEnv(t, "claude", "codex")
			if tc.signedIn != "" {
				e.setStatus(tc.signedIn, 0)
			}
			h := startDeck(t)
			e.settle(h, 2)
			if !strings.Contains(onboardingScreen(h), "l sign in to "+h.onboarding.agent(tc.agent).Display) {
				t.Fatalf("footer does not offer sign-in to %s:\n%s", tc.agent, onboardingScreen(h))
			}
			if a := h.onboarding.agent(tc.agent); a.Auth != authSignedOut {
				t.Fatalf("precondition: %s not signed in, got %v", tc.agent, a.Auth)
			}
			h.Update(runeKey('l'))
			e.mu.Lock()
			logins := append([]onboardingLoginRequest(nil), e.logins...)
			e.mu.Unlock()
			if len(logins) != 1 {
				t.Fatalf("Log in ran %d logins, want 1", len(logins))
			}
			req := logins[0]
			if req.Agent != tc.agent || req.Command != tc.command || req.Path != h.onboarding.folder {
				t.Fatalf("login = %+v, want agent %q command %q in %q", req, tc.agent, tc.command, h.onboarding.folder)
			}
			// The tool's login finished: the re-check on return flips the row.
			e.setStatus(tc.agent, 0)
			h.Update(onboardingLoginDoneMsg{agent: tc.agent})
			e.settle(h, 4)
			if got := h.onboarding.agent(tc.agent).Auth; got != authSignedIn {
				t.Fatalf("after login the re-check says %v, want signed in", got)
			}
			if !strings.Contains(onboardingScreen(h), "is signed in. You can start it now.") {
				t.Fatalf("no confirmation after login:\n%s", onboardingScreen(h))
			}
		})
	}
}

func TestOnboarding_LogInNotOfferedWhenSignedInOrNoStatus(t *testing.T) {
	e := newOnboardingEnv(t, "claude", "gemini")
	e.setStatus("claude", 0)
	h := startDeck(t)
	e.settle(h, 2)
	h.Update(runeKey('l')) // claude signed in, gemini has no status command
	if strings.Contains(onboardingScreen(h), "l sign in") {
		t.Fatalf("footer offers a sign-in with nothing to sign in to:\n%s", onboardingScreen(h))
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.logins) != 0 {
		t.Fatalf("Log in started %v for a signed-in / status-less agent", e.logins)
	}
}

// The status check and the login session use the config dir the first
// Claude session would use (the default account), and never read it.
func TestOnboarding_StatusUsesDefaultAccountConfigDir(t *testing.T) {
	e := newOnboardingEnv(t, "claude")
	work := filepath.Join(e.home, ".claude-work")
	cfg := &session.UserConfig{
		Profiles: map[string]session.ProfileSettings{"work": {Claude: session.ProfileClaudeSettings{ConfigDir: work}}},
	}
	cfg.Claude.DefaultAccount = "work"
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	h := NewHome()
	h.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	h.Update(keyShowMe)
	h.Update(keyTab)
	e.settle(h, 2)
	e.mu.Lock()
	env := strings.Join(e.envs["claude"], "\n")
	e.mu.Unlock()
	if !strings.Contains(env, "CLAUDE_CONFIG_DIR="+work) {
		t.Fatalf("status env does not pin the default account's dir %q:\n%s", work, env)
	}
	h.Update(runeKey('l'))
	e.mu.Lock()
	defer e.mu.Unlock()
	if want := "CLAUDE_CONFIG_DIR='" + work + "' claude auth login"; len(e.logins) != 1 || e.logins[0].Command != want {
		t.Fatalf("login session = %+v, want command %q", e.logins, want)
	}
}

// "Show me around" is a free key: one main-screen clause, no other action.
func TestShowAroundKeyHasNoMainScreenConflict(t *testing.T) {
	key := defaultHotkeyBindings[hotkeyShowAround]
	if key == "" {
		t.Fatal("show_around has no default key")
	}
	clauses := map[string]bool{}
	for _, b := range mainDispatcherBindings(t) {
		for _, alias := range hotkeyAliases(b.key) {
			if alias == key {
				clauses[b.action] = true
			}
		}
	}
	if len(clauses) != 1 {
		t.Fatalf("main screen dispatches %q in %d clauses, want exactly 1", key, len(clauses))
	}
	for action, bound := range defaultHotkeyBindings {
		if action == hotkeyShowAround {
			continue
		}
		for _, alias := range hotkeyAliases(bound) {
			if alias == key {
				t.Errorf("action %q also reaches %q (via %q)", action, key, bound)
			}
		}
	}
	h := NewHome()
	if got := h.normalizeMainKey(key); got != key {
		t.Fatalf("normalizeMainKey(%q) = %q", key, got)
	}
	if keyShowMe.String() != key {
		t.Fatalf("test key %q is not the bound key %q", keyShowMe.String(), key)
	}
}

func TestShowAroundInHelp(t *testing.T) {
	overlay := NewHelpOverlay()
	overlay.SetSize(200, 400)
	overlay.ShowAll() // ASK-211: ? opens on 5 keys; the row is in the full list
	view := stripAnsi(overlay.View())
	key := defaultHotkeyBindings[hotkeyShowAround]
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, key) && strings.Contains(line, "Show me around") {
			return
		}
	}
	t.Fatalf("help has no %q row for Show me around:\n%s", key, view)
}

// Goldens of both pages at the visualcheck sizes, with fixed agents.
func TestOnboarding_Goldens(t *testing.T) {
	newOnboardingEnv(t, "claude", "codex")
	keys := NewHome().onboardingKeys()
	for _, sz := range [][2]int{{80, 24}, {120, 40}, {200, 50}} {
		w, hgt := sz[0], sz[1]
		o := NewOnboardingOverlay()
		o.show(onboardingPageCheck, true, "/work/billing-api", keys)
		o.atStart = true
		o.SetSize(w, hgt)
		o.agent("claude").Auth = authSignedIn
		o.agent("codex").Auth = authSignedOut
		assertOnboardingGolden(t, fmt.Sprintf("check_%dx%d", w, hgt), o.View())
		o.page = onboardingPageTour
		assertOnboardingGolden(t, fmt.Sprintf("tour_%dx%d", w, hgt), o.View())
	}
}

// onboardingTestdata is resolved at init: the tests chdir into a scratch
// project folder.
var onboardingTestdata, _ = filepath.Abs("testdata")

func assertOnboardingGolden(t *testing.T, name, got string) {
	t.Helper()
	got = strings.TrimRight(stripAnsi(got), "\n") + "\n"
	path := filepath.Join(onboardingTestdata, "onboarding", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	// The frame is printed between markers so a run in a throwaway
	// container (no host mount) can still be reviewed and copied out.
	want, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("read golden %s: %v (UPDATE_GOLDEN=1 to create)\n=== GOLDEN %s ===\n%s=== END GOLDEN ===", path, err, name, got)
		return
	}
	if string(want) != got {
		t.Errorf("golden %s differs.\n--- want\n%s\n=== GOLDEN %s ===\n%s=== END GOLDEN ===", path, want, name, got)
	}
}
