package ui

// First-run onboarding (ASK-131): Welcome with agent detection and sign-in,
// first session in two keys, and the "What you can do" page. Every test runs
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

	prevLook, prevRun, prevStart, prevObs, prevEvery := onboardingLookPath, onboardingStatusRunner, onboardingStartLogin, onboardingCmdObserver, onboardingRecheckEvery
	t.Cleanup(func() {
		onboardingLookPath, onboardingStatusRunner, onboardingStartLogin, onboardingCmdObserver, onboardingRecheckEvery = prevLook, prevRun, prevStart, prevObs, prevEvery
	})
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
	onboardingStartLogin = func(h *Home, req onboardingLoginRequest) tea.Cmd {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.logins = append(e.logins, req)
		return nil
	}
	onboardingRecheckEvery = time.Millisecond
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
	case onboardingStatusMsg, onboardingRecheckMsg:
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

func welcomeVisible(h *Home) bool {
	return h.onboarding.IsVisible() && h.onboarding.Page() == onboardingPageWelcome
}

func tourVisible(h *Home) bool {
	return h.onboarding.IsVisible() && h.onboarding.Page() == onboardingPageTour
}

func TestOnboarding_FreshInstallShowsWelcomeExactlyOnce(t *testing.T) {
	newOnboardingEnv(t, "claude")
	h := startDeck(t)
	if !welcomeVisible(h) {
		t.Fatal("fresh install: the Welcome is not shown")
	}
	if h.setupWizard.IsVisible() {
		t.Fatal("fresh install: the old setup wizard is still on screen with the Welcome")
	}
	screen := onboardingScreen(h)
	for _, want := range []string{"Welcome to Agent Deck", "each in its own session", "Your agents", "Claude Code"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("Welcome lacks %q:\n%s", want, screen)
		}
	}

	// Second start on the same install, without touching anything.
	h2 := startDeck(t)
	if h2.onboarding.IsVisible() || h2.setupWizard.IsVisible() {
		t.Fatalf("second start: welcome=%v wizard=%v, want neither", h2.onboarding.IsVisible(), h2.setupWizard.IsVisible())
	}
}

func TestOnboarding_EscSkipsAndPersists(t *testing.T) {
	newOnboardingEnv(t, "codex")
	h := startDeck(t)
	if !welcomeVisible(h) {
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
	if !welcomeVisible(h) {
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
			if !welcomeVisible(h) {
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
				hint := "install: " + a.Install
				if isFound == strings.Contains(screen, hint) {
					t.Fatalf("%s found=%v but install hint shown=%v:\n%s", a.Name, isFound, !isFound, screen)
				}
			}
			if len(tc.found) == 0 && !strings.Contains(screen, "no agent found: start a plain terminal") {
				t.Fatalf("no agent found, but no plain-shell offer:\n%s", screen)
			}
		})
	}
}

func TestOnboarding_EnterOpensPrefilledDialogAndSecondEnterCreates(t *testing.T) {
	newOnboardingEnv(t, "claude", "codex")
	h := startDeck(t)
	h.Update(keyDown) // highlight codex
	if got := h.onboarding.Selected(); got != "codex" {
		t.Fatalf("after ↓ the highlight is %q, want codex", got)
	}
	keys := 0
	h.Update(keyEnter)
	keys++
	if h.onboarding.IsVisible() || !h.newDialog.IsVisible() {
		t.Fatal("Enter on the Welcome did not open the New Session dialog")
	}
	if got := h.newDialog.GetSelectedCommand(); got != "codex" {
		t.Fatalf("dialog tool = %q, want the highlighted codex", got)
	}
	if got := h.newDialog.pathInput.Value(); got != h.onboarding.folder {
		t.Fatalf("dialog path = %q, want this folder %q", got, h.onboarding.folder)
	}
	_, cmd := h.Update(keyEnter)
	keys++
	if !submitted(h, cmd) {
		t.Fatalf("second Enter did not create (error %q)", h.newDialog.validationErr)
	}
	if keys != 2 {
		t.Fatalf("Welcome to created session took %d keys, want 2", keys)
	}
	if got := h.newDialog.nameInput.Value(); got != "billing-api" {
		t.Fatalf("session name = %q, want the folder name", got)
	}
	if got := session.GetDefaultTool(); got != "codex" {
		t.Fatalf("default tool after the Welcome = %q, want codex", got)
	}
}

// Reopened later ("Show me around", Tab), the Welcome's pick wins over the
// configured default tool: Enter opens the dialog on the highlighted agent.
func TestOnboarding_ReopenedEnterPrefillsHighlightedAgent(t *testing.T) {
	newOnboardingEnv(t, "claude", "codex")
	cfg := &session.UserConfig{DefaultTool: "claude"}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	h := startDeck(t)
	if h.onboarding.IsVisible() {
		t.Fatal("precondition: no first-run Welcome with a config.toml")
	}
	h.Update(keyShowMe)
	h.Update(keyTab)
	h.Update(keyDown) // codex
	if got := h.onboarding.Selected(); got != "codex" {
		t.Fatalf("highlight = %q, want codex", got)
	}
	h.Update(keyEnter)
	if !h.newDialog.IsVisible() {
		t.Fatal("Enter on the reopened Welcome did not open the dialog")
	}
	if got := h.newDialog.GetSelectedCommand(); got != "codex" {
		t.Fatalf("dialog tool = %q, want the highlighted codex over default_tool claude", got)
	}
	if got := session.GetDefaultTool(); got != "claude" {
		t.Fatalf("reopened Welcome changed default_tool to %q", got)
	}
}

// The Welcome promises "new session with <agent> in <this folder>". Reopened
// with the list cursor on a remote session, Enter must still open the local
// dialog on that agent and folder, not the remote dialog (which drops the
// agent and creates on another machine).
func TestOnboarding_WelcomeEnterOnRemoteCursorStaysLocal(t *testing.T) {
	newOnboardingEnv(t, "claude")
	if err := session.SaveUserConfig(&session.UserConfig{}); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	h := startDeck(t)
	remote := session.RemoteSessionInfo{ID: "r1", Title: "remote-one", Status: "idle"}
	h.flatItems = []session.Item{{Type: session.ItemTypeRemoteSession, RemoteSession: &remote, RemoteName: "myserver"}}
	h.cursor = 0
	h.Update(keyShowMe)
	h.Update(keyTab)
	if !welcomeVisible(h) {
		t.Fatal("precondition: Tab did not reach the Welcome")
	}
	h.Update(keyEnter)
	if !h.newDialog.IsVisible() {
		t.Fatal("Enter on the Welcome did not open the dialog")
	}
	if h.pendingRemoteName != "" {
		t.Fatalf("Enter on the Welcome routed the create to remote %q", h.pendingRemoteName)
	}
	if got := h.newDialog.GetSelectedCommand(); got != "claude" {
		t.Fatalf("dialog tool = %q, want the highlighted claude", got)
	}
	if got := h.newDialog.pathInput.Value(); got != h.onboarding.folder {
		t.Fatalf("dialog path = %q, want the Welcome's folder %q", got, h.onboarding.folder)
	}
}

// Reopened with the cursor in a group whose default path is another folder,
// the dialog still uses the folder the Welcome shows.
func TestOnboarding_ReopenedEnterUsesWelcomeFolder(t *testing.T) {
	e := newOnboardingEnv(t, "claude")
	if err := session.SaveUserConfig(&session.UserConfig{}); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()
	other := filepath.Join(e.home, "other-repo")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	inst := session.NewInstanceWithTool("other", other, "claude")
	h := startDeck(t, inst)
	for i, item := range h.flatItems {
		if item.Type == session.ItemTypeSession && item.Session != nil && item.Session.ID == inst.ID {
			h.cursor = i
		}
	}
	if got := h.getDefaultPathForGroup(inst.GroupPath); got != other {
		t.Fatalf("precondition: group default path = %q, want %q", got, other)
	}
	h.Update(keyShowMe)
	h.Update(keyTab)
	h.Update(keyEnter)
	if !h.newDialog.IsVisible() {
		t.Fatal("Enter on the reopened Welcome did not open the dialog")
	}
	if got := h.newDialog.pathInput.Value(); got != h.onboarding.folder {
		t.Fatalf("dialog path = %q, want the Welcome's folder %q", got, h.onboarding.folder)
	}
}

func TestOnboarding_NoAgentStartsPlainShell(t *testing.T) {
	newOnboardingEnv(t)
	h := startDeck(t)
	h.Update(keyEnter)
	if !h.newDialog.IsVisible() {
		t.Fatal("Enter did not open the dialog")
	}
	if got := h.newDialog.GetSelectedCommand(); got != "" {
		t.Fatalf("no agent found: dialog tool = %q, want the plain shell", got)
	}
}

func TestOnboarding_TourShownOnceAfterFirstCreate(t *testing.T) {
	newOnboardingEnv(t, "claude")
	h := startDeck(t)
	h.Update(keyEnter)
	h.Update(keyEnter)
	if h.onboarding.IsVisible() {
		t.Fatal("What you can do opened before the session exists")
	}
	inst := session.NewInstanceWithTool("billing-api", h.onboarding.folder, "claude")
	h.Update(sessionCreatedMsg{instance: inst})
	if !tourVisible(h) {
		t.Fatal("What you can do did not open after the first session was created")
	}
	screen := onboardingScreen(h)
	for _, want := range []string{"What you can do", "Sessions", "Switch", "Send", "Conductor", "Remotes",
		"agent-deck conductor setup", "agent-deck remote add", "Ctrl+Q", "alt+w"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("What you can do lacks %q:\n%s", want, screen)
		}
	}
	h.Update(keyEsc)
	inst2 := session.NewInstanceWithTool("second", h.onboarding.folder, "claude")
	h.Update(sessionCreatedMsg{instance: inst2})
	if h.onboarding.IsVisible() {
		t.Fatal("What you can do opened again on a later create")
	}
}

// The first create can land while a reload runs (the early-return branch of
// sessionCreatedMsg). The tour belongs to that create: it opens then, and a
// later create does not open it.
func TestOnboarding_TourAfterCreateDuringReload(t *testing.T) {
	newOnboardingEnv(t, "claude")
	h := startDeck(t)
	h.Update(keyEnter)
	h.Update(keyEnter)
	h.reloadMu.Lock()
	h.isReloading = true
	h.reloadMu.Unlock()
	inst := session.NewInstanceWithTool("billing-api", h.onboarding.folder, "claude")
	h.Update(sessionCreatedMsg{instance: inst})
	h.reloadMu.Lock()
	h.isReloading = false
	h.reloadMu.Unlock()
	if !tourVisible(h) {
		t.Fatal("first create landed during a reload and What you can do did not open")
	}
	h.Update(keyEsc)
	inst2 := session.NewInstanceWithTool("second", h.onboarding.folder, "claude")
	h.Update(sessionCreatedMsg{instance: inst2})
	if h.onboarding.IsVisible() {
		t.Fatal("What you can do opened again on a later create")
	}
}

// A failed first create consumes the tour too: it must not pop on some
// unrelated later create.
func TestOnboarding_TourNotShownAfterFailedFirstCreate(t *testing.T) {
	newOnboardingEnv(t, "claude")
	h := startDeck(t)
	h.Update(keyEnter)
	h.Update(keyEnter)
	h.Update(sessionCreatedMsg{err: errors.New("tmux failed")})
	if h.onboarding.IsVisible() {
		t.Fatal("What you can do opened for a failed create")
	}
	inst := session.NewInstanceWithTool("later", h.onboarding.folder, "claude")
	h.Update(sessionCreatedMsg{instance: inst})
	if h.onboarding.IsVisible() {
		t.Fatal("failed first create, yet a later create opened What you can do")
	}
}

func TestOnboarding_TourNotShownWhenDialogCancelled(t *testing.T) {
	newOnboardingEnv(t, "claude")
	h := startDeck(t)
	h.Update(keyEnter)
	h.Update(keyEsc) // cancel the dialog
	inst := session.NewInstanceWithTool("later", h.onboarding.folder, "claude")
	h.Update(sessionCreatedMsg{instance: inst})
	if h.onboarding.IsVisible() {
		t.Fatal("cancelled first session, yet a later create opened What you can do")
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
	for _, w := range []string{"signed in", "not signed in", "sign in inside the tool"} {
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
		agent, tool, command string
		down                 int
	}{
		{"claude", "shell", "claude auth login", 0},
		{"codex", "shell", "codex login", 1},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			e := newOnboardingEnv(t, "claude", "codex")
			h := startDeck(t)
			e.settle(h, 2)
			for i := 0; i < tc.down; i++ {
				h.Update(keyDown)
			}
			if a := h.onboarding.agent(tc.agent); a.Auth != authSignedOut {
				t.Fatalf("precondition: %s not signed in, got %v", tc.agent, a.Auth)
			}
			h.Update(runeKey('l'))
			e.mu.Lock()
			logins := append([]onboardingLoginRequest(nil), e.logins...)
			e.mu.Unlock()
			if len(logins) != 1 {
				t.Fatalf("Log in started %d sessions, want 1", len(logins))
			}
			req := logins[0]
			if req.Tool != tc.tool || req.Command != tc.command {
				t.Fatalf("login session = %+v, want tool %q command %q", req, tc.tool, tc.command)
			}
			if req.Title != tc.agent+"-login" || req.Path != h.onboarding.folder {
				t.Fatalf("login session title/path = %q/%q", req.Title, req.Path)
			}
			// The tool's login finished: the next re-check flips the row.
			e.setStatus(tc.agent, 0)
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
	h.Update(runeKey('l'))
	h.Update(keyDown) // gemini: no status command
	h.Update(runeKey('l'))
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
	overlay.Show()
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
		o.show(onboardingPageWelcome, true, "/work/billing-api", keys)
		o.SetSize(w, hgt)
		o.agent("claude").Auth = authSignedIn
		o.agent("codex").Auth = authSignedOut
		assertOnboardingGolden(t, fmt.Sprintf("welcome_%dx%d", w, hgt), o.View())
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
