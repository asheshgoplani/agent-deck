package ui

// First-run onboarding (ASK-131, reshaped by ASK-211 from Sharjeel's plan
// and prototype): a quick check page and a "What you can do" page in one
// overlay. The welcome itself is the empty main screen (welcome.go).
//
//   - Quick check: tmux (version, or missing with an install line; missing
//     blocks Enter), agent-deck's own version, and each coding agent
//     (installed and signed in, or missing with an install line), plus a
//     guided sign-in (l) that runs the tool's own login flow in a session.
//     No agent found: Enter continues with a Plain shell. r re-checks.
//     Enter continues to the welcome.
//   - What you can do: the five things that matter, each with its real key,
//     any time with the show_around hotkey (default alt+w, "Show me around").
//
// The check page opens by itself once on a fresh install: no config.toml
// (the condition that used to open the setup wizard), no sessions and no
// onboarding marker. It replaces the setup wizard there; an install with
// sessions but no config.toml still gets the wizard exactly as before.
// After that it opens only when something is wrong at start and there are
// no sessions yet: tmux missing, or no agent CLI found.
//
// Sign-in state comes only from each tool's own status command, run with a
// timeout and with its output discarded (only the exit code is read). No
// credentials file is opened and no token is read, stored, printed or passed.

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/harness"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// onboardingSeenName is the marker file (under the effective data dir) whose
// presence means the Welcome has been shown on this install.
const onboardingSeenName = ".onboarding-welcome-seen"

// onboardingAgentNames are the agents the Welcome looks for, in order.
var onboardingAgentNames = []string{"claude", "codex", "gemini", "pi"}

// Seams for tests. Production code never reassigns them.
var (
	// onboardingLookPath finds an agent's binary on PATH.
	onboardingLookPath = exec.LookPath
	// onboardingTmuxCheck reports tmux's version line ("tmux 3.5a") and
	// whether tmux is installed.
	onboardingTmuxCheck = checkTmuxForOnboarding
	// onboardingStatusRunner runs a tool's own sign-in status command and
	// returns its exit code.
	onboardingStatusRunner = runOnboardingStatus
	// onboardingStatusTimeout bounds one status command.
	onboardingStatusTimeout = 5 * time.Second
	// onboardingStartLogin runs the tool's own login in this terminal.
	onboardingStartLogin = runOnboardingLogin
	// onboardingCmdObserver, when set (tests), receives the commands the
	// overlay queues instead of the Bubble Tea runtime.
	onboardingCmdObserver func(tea.Cmd)
)

type onboardingPage int

const (
	onboardingPageCheck onboardingPage = iota
	onboardingPageTour
)

// onboardingAuth is an agent's sign-in state as its status command reports it.
type onboardingAuth int

const (
	authNoStatus  onboardingAuth = iota // the tool has no status command
	authChecking                        // status command running
	authSignedIn                        // exit 0
	authSignedOut                       // non-zero exit
	authTimedOut                        // no answer within onboardingStatusTimeout
	authUnknown                         // the command could not run
)

func (a onboardingAuth) label() string {
	switch a {
	case authChecking:
		return "checking sign-in..."
	case authSignedIn:
		return "signed in"
	case authSignedOut:
		return "not signed in"
	case authTimedOut:
		return "sign-in check timed out"
	case authUnknown:
		return "sign-in unknown"
	}
	return "sign in inside the tool"
}

// onboardingAgent is one row of the Welcome's agent list.
type onboardingAgent struct {
	Name       string
	Display    string
	Binary     string
	Install    string
	Login      string
	StatusArgs []string
	Found      bool
	Auth       onboardingAuth
}

// canLogIn reports whether the Welcome offers "Log in" for this agent.
func (a onboardingAgent) canLogIn() bool {
	return a.Found && len(a.StatusArgs) > 0 && strings.TrimSpace(a.Login) != "" && a.Auth != authSignedIn && a.Auth != authChecking
}

// onboardingLoginRequest is the login the sign-in action runs.
type onboardingLoginRequest struct {
	Agent   string
	Path    string // the folder it runs in
	Command string // the login command, pinned to the account a new session would use
}

type onboardingStatusMsg struct {
	agent string
	auth  onboardingAuth
}

// onboardingLoginDoneMsg says the login command of agent has exited.
type onboardingLoginDoneMsg struct{ agent string }

// onboardingKeys are the real, resolved keys the pages name.
type onboardingKeys struct {
	newSession, quickCreate, detach, altSession, prompt, queue, report, whatsNew, tour, help string
}

// OnboardingOverlay is the quick check / What you can do overlay.
type OnboardingOverlay struct {
	visible       bool
	page          onboardingPage
	firstRun      bool // opened by a fresh install; dismissing it finishes first run
	atStart       bool // opened at start (first run or a problem): q quits agent-deck
	agents        []onboardingAgent
	tmuxFound     bool
	tmuxVersion   string // "tmux 3.5a", or "" when it could not be read
	flash         string // the re-check result, or why Enter is blocked
	folder        string
	width, height int
	keys          onboardingKeys

	loginAgent string // agent whose login just ran, until its re-check answers
	notice     string // the sign-in result ("Claude Code is signed in.")
}

// NewOnboardingOverlay returns a hidden overlay.
func NewOnboardingOverlay() *OnboardingOverlay {
	return &OnboardingOverlay{}
}

func (o *OnboardingOverlay) IsVisible() bool           { return o != nil && o.visible }
func (o *OnboardingOverlay) Page() onboardingPage      { return o.page }
func (o *OnboardingOverlay) SetSize(width, height int) { o.width, o.height = width, height }

// agent returns the row for name, or nil.
func (o *OnboardingOverlay) agent(name string) *onboardingAgent {
	for i := range o.agents {
		if o.agents[i].Name == name {
			return &o.agents[i]
		}
	}
	return nil
}

// choices are the agents a first session can start: every installed agent
// in order, then a plain shell.
func (o *OnboardingOverlay) choices() []string {
	return onboardingChoices(o.agents)
}

func onboardingChoices(agents []onboardingAgent) []string {
	var out []string
	for _, a := range agents {
		if a.Found {
			out = append(out, a.Name)
		}
	}
	return append(out, "shell")
}

// Selected is the agent first run picks as the default tool: the first
// installed agent, or "shell" when none is installed.
func (o *OnboardingOverlay) Selected() string {
	return o.choices()[0]
}

// anyAgentFound reports whether at least one agent CLI is installed.
func (o *OnboardingOverlay) anyAgentFound() bool {
	return o.Selected() != "shell"
}

// loginTarget is the agent l signs in to: the first installed agent that
// offers a sign-in and is not signed in yet, or nil.
func (o *OnboardingOverlay) loginTarget() *onboardingAgent {
	for i := range o.agents {
		if o.agents[i].canLogIn() {
			return &o.agents[i]
		}
	}
	return nil
}

// checkTmuxForOnboarding finds tmux and reads `tmux -V` (no server is
// contacted), bounded to a second.
func checkTmuxForOnboarding() (version string, found bool) {
	return TmuxVersion()
}

// TmuxVersion finds tmux and reads `tmux -V` ("tmux 3.5a"; no server is
// contacted), bounded to a second, with the filtered child environment
// (internal/childenv). It is shared with the printed check page in
// cmd/agent-deck. version is "" when it cannot be read.
func TmuxVersion() (version string, found bool) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-V")
	cmd.Env = childenv.ForLaunch("")
	out, err := cmd.Output()
	if err != nil {
		return "", true
	}
	return strings.TrimSpace(string(out)), true
}

// detect fills the agent rows and the tmux row afresh.
func (o *OnboardingOverlay) detect() {
	o.agents = detectOnboardingAgents()
	o.tmuxVersion, o.tmuxFound = onboardingTmuxCheck()
}

// detectOnboardingAgents fills the agent rows from the harness table (with
// [harnesses.<name>] overrides) and PATH.
func detectOnboardingAgents() []onboardingAgent {
	var overrides map[string]harness.Override
	if cfg, _ := session.LoadUserConfig(); cfg != nil {
		overrides = cfg.Harnesses
	}
	byName := map[string]harness.Entry{}
	for _, e := range harness.All(overrides) {
		byName[e.Name] = e
	}
	agents := make([]onboardingAgent, 0, len(onboardingAgentNames))
	for _, name := range onboardingAgentNames {
		e, ok := byName[name]
		if !ok {
			continue
		}
		a := onboardingAgent{
			Name: name, Display: e.DisplayName, Binary: e.Binary,
			Install: e.InstallCommand, Login: e.LoginCommand, StatusArgs: e.StatusArgs,
		}
		if _, err := onboardingLookPath(e.Binary); err == nil {
			a.Found = true
		}
		agents = append(agents, a)
	}
	return agents
}

// show opens the overlay on page, detecting agents afresh.
func (o *OnboardingOverlay) show(page onboardingPage, firstRun bool, folder string, keys onboardingKeys) {
	o.visible = true
	o.page = page
	o.firstRun = firstRun
	o.folder = folder
	o.keys = keys
	o.notice = ""
	o.flash = ""
	o.detect()
}

// hide closes the overlay and stops any sign-in re-checking.
func (o *OnboardingOverlay) hide() {
	o.visible = false
	o.firstRun = false
	o.atStart = false
	o.loginAgent = ""
}

// statusChecks marks every installed agent with a status command as
// checking and returns one command per agent.
func (o *OnboardingOverlay) statusChecks() []tea.Cmd {
	var cmds []tea.Cmd
	for i := range o.agents {
		a := &o.agents[i]
		if !a.Found || len(a.StatusArgs) == 0 {
			continue
		}
		a.Auth = authChecking
		cmds = append(cmds, onboardingStatusCmd(*a))
	}
	return cmds
}

// statusCheck re-checks one agent.
func (o *OnboardingOverlay) statusCheck(name string) tea.Cmd {
	a := o.agent(name)
	if a == nil || !a.Found || len(a.StatusArgs) == 0 {
		return nil
	}
	a.Auth = authChecking
	return onboardingStatusCmd(*a)
}

// applyStatus records a status result. It returns true when the agent whose
// login session was started is now signed in.
func (o *OnboardingOverlay) applyStatus(msg onboardingStatusMsg) bool {
	a := o.agent(msg.agent)
	if a == nil {
		return false
	}
	a.Auth = msg.auth
	if msg.agent != o.loginAgent {
		return false
	}
	o.loginAgent = ""
	if msg.auth == authSignedIn {
		o.notice = a.Display + " is signed in. You can start it now."
		return true
	}
	o.notice = a.Display + " is not signed in yet. Press l to try again."
	return false
}

func onboardingStatusCmd(a onboardingAgent) tea.Cmd {
	binary, args, env := a.Binary, append([]string(nil), a.StatusArgs...), onboardingStatusEnv(a.Name)
	name := a.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), onboardingStatusTimeout)
		defer cancel()
		code, err := onboardingStatusRunner(ctx, binary, args, env)
		switch {
		case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
			return onboardingStatusMsg{agent: name, auth: authTimedOut}
		case err != nil:
			return onboardingStatusMsg{agent: name, auth: authUnknown}
		case code == 0:
			return onboardingStatusMsg{agent: name, auth: authSignedIn}
		default:
			return onboardingStatusMsg{agent: name, auth: authSignedOut}
		}
	}
}

// runOnboardingStatus runs binary args with env, output discarded, and
// returns the exit code. A non-zero exit is not an error.
func runOnboardingStatus(ctx context.Context, binary string, args, env []string) (int, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// onboardingClaudeDir is the Claude config dir (and its source) a first
// Claude session in the default group would use, default account included.
func onboardingClaudeDir() (dir, source, account string) {
	cfg, _ := session.LoadUserConfig()
	account = session.DefaultAccountForNewSession(cfg, session.DefaultGroupPath, nil)
	probe := &session.Instance{Tool: "claude", GroupPath: session.DefaultGroupPath, Account: account}
	dir, source = session.GetClaudeConfigDirSourceForInstance(probe)
	return dir, source, account
}

// onboardingCodexHome is CODEX_HOME when agent-deck's config moves it off
// ~/.codex and the environment does not already set it, else "".
func onboardingCodexHome() string {
	if strings.TrimSpace(os.Getenv("CODEX_HOME")) != "" {
		return ""
	}
	home, _ := os.UserHomeDir()
	dir := session.GetCodexConfigDir()
	if dir == "" || filepath.Clean(dir) == filepath.Join(home, ".codex") {
		return ""
	}
	return dir
}

// onboardingStatusEnv is the environment a status command runs with: the
// launch environment, pinned to the config dir a new session would use.
func onboardingStatusEnv(agent string) []string {
	env := append(childenv.ForLaunch(""), "NO_COLOR=1")
	switch agent {
	case "claude":
		if dir, source, _ := onboardingClaudeDir(); source != "default" && dir != "" {
			env = append(env, "CLAUDE_CONFIG_DIR="+dir)
		}
	case "codex":
		if dir := onboardingCodexHome(); dir != "" {
			env = append(env, "CODEX_HOME="+dir)
		}
	}
	return env
}

// loginRequest builds the login the sign-in action runs for agent: the
// tool's own login command (`claude auth login`, `codex login`), on the
// config dir a new session would use. It runs in this terminal while the
// TUI waits, not in a session: nothing is added to the session list, and
// the check page re-checks the sign-in as soon as the login exits.
func (o *OnboardingOverlay) loginRequest(agent string) (onboardingLoginRequest, bool) {
	a := o.agent(agent)
	if a == nil || !a.canLogIn() {
		return onboardingLoginRequest{}, false
	}
	req := onboardingLoginRequest{Agent: agent, Path: o.folder, Command: a.Login}
	switch agent {
	case "claude":
		if dir, source, _ := onboardingClaudeDir(); source != "default" && dir != "" {
			req.Command = "CLAUDE_CONFIG_DIR=" + shellQuoteOnboarding(dir) + " " + a.Login
		}
	case "codex":
		if dir := onboardingCodexHome(); dir != "" {
			req.Command = "CODEX_HOME=" + shellQuoteOnboarding(dir) + " " + a.Login
		}
	}
	return req, true
}

func shellQuoteOnboarding(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// onboardingSeenPath is the marker file, or "" when it cannot be resolved.
func onboardingSeenPath() string {
	path, err := agentpaths.EffectiveDataPath(onboardingSeenName, onboardingSeenName)
	if err != nil {
		return ""
	}
	return path
}

// onboardingFirstRunDoneTag starts the marker when onboarding itself
// finished first run on this install (wrote config.toml). Markers from older
// versions hold only a timestamp: those installs predate this onboarding.
const onboardingFirstRunDoneTag = "first-run-done"

// onboardingMarkerExists reports whether the marker is positively there
// (an unresolvable path is false).
func onboardingMarkerExists() bool {
	path := onboardingSeenPath()
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// onboardingFirstRunFinished reports whether onboarding finished first run
// on this install. Only then may the check page come back later.
func onboardingFirstRunFinished() bool {
	path := onboardingSeenPath()
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && strings.HasPrefix(string(data), onboardingFirstRunDoneTag)
}

// markOnboardingFirstRunDone writes the marker as first run finishes (not as
// the page opens, so quitting the check page brings it back next start).
func markOnboardingFirstRunDone() {
	path := onboardingSeenPath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(onboardingFirstRunDoneTag+" "+time.Now().Format(time.RFC3339)+"\n"), 0o644)
}

// onboardingConfigMissing reports whether there is no config.toml yet.
func onboardingConfigMissing() bool {
	configPath, err := session.GetUserConfigPath()
	if err != nil || configPath == "" {
		return false
	}
	_, err = os.Stat(configPath)
	return os.IsNotExist(err)
}

// onboardingAnyAgentInstalled reports whether any agent can start here: one
// of the check page's agents, any installed tool in the New Session picker
// (opencode, copilot, cursor, custom tools...), or the configured
// default_tool. Probes follow configured commands (session.ToolInstalled).
func onboardingAnyAgentInstalled() bool {
	if len(onboardingChoices(detectOnboardingAgents())) > 1 {
		return true
	}
	if newDialogToolInstalled == nil {
		// The probe is off (tests): every tool counts as installed.
		return true
	}
	for _, name := range session.PickerToolNames() {
		if name != "" && name != "shell" && newDialogToolInstalled(name) {
			return true
		}
	}
	if def := session.GetDefaultTool(); def != "" && def != "shell" && newDialogToolInstalled(def) {
		return true
	}
	return false
}

// ---- Home wiring ----

// queueOnboarding hands cmds to the runtime on the way out of Update.
func (h *Home) queueOnboarding(cmds ...tea.Cmd) {
	for _, c := range cmds {
		if c != nil {
			h.onboardingCmds = append(h.onboardingCmds, c)
		}
	}
}

// takeOnboardingCmds drains the queue as one command (nil when empty).
func (h *Home) takeOnboardingCmds() tea.Cmd {
	if len(h.onboardingCmds) == 0 {
		return nil
	}
	cmds := h.onboardingCmds
	h.onboardingCmds = nil
	batch := tea.Batch(cmds...)
	if onboardingCmdObserver != nil {
		onboardingCmdObserver(batch)
		return nil
	}
	return batch
}

func (h *Home) onboardingKeys() onboardingKeys {
	return onboardingKeys{
		newSession:  h.actionKey(hotkeyNewSession),
		quickCreate: h.actionKey(hotkeyQuickCreate),
		detach:      DetachByteLabel(DetachByteFromBinding(h.actionKey(hotkeyDetach))),
		altSession:  h.actionKey(hotkeyAltSession),
		prompt:      h.actionKey(hotkeyPromptSession),
		queue:       h.actionKey(hotkeyQueueMessage),
		report:      h.actionKey(hotkeyReportProblem),
		whatsNew:    h.actionKey(hotkeyWhatsNew),
		tour:        h.actionKey(hotkeyShowAround),
		help:        h.actionKey(hotkeyHelp),
	}
}

func onboardingFolder() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	home, _ := os.UserHomeDir()
	return home
}

// openOnboarding shows the overlay on page. firstRun marks the automatic
// first-run opening.
func (h *Home) openOnboarding(page onboardingPage, firstRun bool) {
	if h.onboarding == nil {
		h.onboarding = NewOnboardingOverlay()
	}
	h.onboarding.show(page, firstRun, onboardingFolder(), h.onboardingKeys())
	h.onboarding.SetSize(h.width, h.height)
	if page == onboardingPageCheck {
		h.queueOnboarding(h.onboarding.statusChecks()...)
	}
}

// maybeStartOnboarding runs on the first session load and reports whether
// this run is a fresh install's first run. On a fresh install (no
// config.toml, no sessions, and onboarding never finished first run here)
// the check page takes the setup wizard's place; it keeps coming back at
// start until first run is finished (Enter, Esc or a pick writes
// config.toml and the marker). Later it opens only on an install whose first
// run onboarding finished, with no sessions, when something is wrong: tmux
// missing or no agent at all. An install whose config predates onboarding
// never sees it.
func (h *Home) maybeStartOnboarding(noSessions bool) (freshInstall bool) {
	if !noSessions {
		return false
	}
	if onboardingConfigMissing() && !onboardingFirstRunFinished() {
		h.setupWizard.Hide()
		h.openOnboarding(onboardingPageCheck, true)
		h.onboarding.atStart = true
		return true
	}
	if h.setupWizard.IsVisible() || !onboardingFirstRunFinished() {
		return false
	}
	if _, found := onboardingTmuxCheck(); found && onboardingAnyAgentInstalled() {
		return false
	}
	h.openOnboarding(onboardingPageCheck, false)
	h.onboarding.atStart = true
	return false
}

// finishFirstRun writes config.toml the way the setup wizard does when it is
// skipped, with tool as the default tool ("shell" for none).
func (h *Home) finishFirstRun(tool string) {
	w := NewSetupWizard()
	for i, t := range w.toolOptions {
		if t == tool {
			w.selectedTool = i
		}
	}
	// The marker records that first run finished, which means config.toml
	// was written: a failed save leaves first run open, so the check page
	// comes back at the next start instead of an install with no config
	// that onboarding never offers again.
	if h.saveFirstRunConfig(w.GetConfig()) == nil {
		markOnboardingFirstRunDone()
	}
}

// saveFirstRunUserConfig writes config.toml for first run; a test seam.
var saveFirstRunUserConfig = session.SaveUserConfig

// saveFirstRunConfig saves a first-run config merged onto disk, so fields the
// wizard does not manage survive (#1067), and reloads what depends on it. A
// failed save is shown and returned.
func (h *Home) saveFirstRunConfig(config *session.UserConfig) error {
	merged, mergeErr := session.MergePanelConfigOntoDisk(config)
	if mergeErr == nil && merged != nil {
		config = merged
	}
	saveErr := saveFirstRunUserConfig(config)
	if saveErr != nil {
		h.err = saveErr
		h.errTime = time.Now()
	}
	_, _ = session.ReloadUserConfig()
	h.reloadHotkeysFromConfig()
	if defaultTool := session.GetDefaultTool(); defaultTool != "" {
		h.newDialog.SetDefaultTool(defaultTool)
	}
	return saveErr
}

// closeOnboarding hides the overlay, finishing first run if it was one.
func (h *Home) closeOnboarding() {
	if h.onboarding.firstRun {
		h.finishFirstRun(h.onboarding.Selected())
	}
	h.onboarding.hide()
}

// handleOnboardingKey owns every key while the overlay is open.
func (h *Home) handleOnboardingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	o := h.onboarding
	key := msg.String()
	if o.page == onboardingPageTour {
		switch key {
		case "esc", "enter", "q":
			h.closeOnboarding()
		case "tab":
			o.page = onboardingPageCheck
			o.flash = ""
			o.detect()
			h.queueOnboarding(o.statusChecks()...)
		}
		return h, nil
	}
	switch key {
	case "enter", "esc":
		if o.atStart && !o.tmuxFound {
			o.flash = "agent-deck needs tmux. Install it, then press r."
			return h, nil
		}
		// Continue to the welcome: the empty main screen (welcome.go).
		h.closeOnboarding()
		h.welcomeSel = 0
		h.refreshWelcomeAgents()
	case "q", "ctrl+c":
		if !o.atStart {
			h.closeOnboarding()
			return h, nil
		}
		o.hide()
		return h.tryQuit()
	case "tab":
		o.page = onboardingPageTour
	case "r":
		h.recheckOnboarding()
	case "l":
		if a := o.loginTarget(); a != nil {
			h.startOnboardingLogin(a.Name)
		}
	}
	return h, nil
}

// recheckOnboarding is r on the check page: tmux and the agents are found
// again, sign-in is re-checked, and one line says what changed.
func (h *Home) recheckOnboarding() {
	o := h.onboarding
	hadTmux, hadAgent := o.tmuxFound, o.anyAgentFound()
	o.detect()
	h.queueOnboarding(o.statusChecks()...)
	switch {
	case !o.tmuxFound:
		o.flash = "Checked again: tmux is still not found."
	case !hadTmux:
		found := "tmux"
		if o.tmuxVersion != "" {
			found = o.tmuxVersion
		}
		o.flash = "Checked again: " + found + " found."
	case !o.anyAgentFound():
		o.flash = "Checked again: still no agent found."
	case !hadAgent:
		var names []string
		for _, a := range o.agents {
			if a.Found {
				names = append(names, a.Display)
			}
		}
		o.flash = "Checked again: " + strings.Join(names, " and ") + " found."
	default:
		o.flash = "Checked again: all good."
	}
}

// startOnboardingLogin runs agent's own login flow in this terminal; the
// check page re-checks its sign-in when the login exits.
func (h *Home) startOnboardingLogin(agent string) {
	req, ok := h.onboarding.loginRequest(agent)
	if !ok {
		return
	}
	h.onboarding.loginAgent = ""
	h.onboarding.notice = ""
	h.queueOnboarding(onboardingStartLogin(req))
}

// runOnboardingLogin hands the terminal to the login command (the TUI
// waits, as for an editor) and reports when it exits. It runs through sh
// so a config-dir prefix (CLAUDE_CONFIG_DIR=... claude auth login) works;
// the environment is the launch environment (internal/childenv).
func runOnboardingLogin(req onboardingLoginRequest) tea.Cmd {
	agent := req.Agent
	return execProcessHoldingInput(onboardingLoginCommand(req), func(error) tea.Msg { return onboardingLoginDoneMsg{agent: agent} })
}

// onboardingLoginCommand is the process runOnboardingLogin runs.
func onboardingLoginCommand(req onboardingLoginRequest) *exec.Cmd {
	cmd := exec.Command("/bin/sh", "-c", req.Command) // #nosec G204 -- the harness table's login command, quoted config dir
	cmd.Dir = req.Path
	cmd.Env = childenv.ForLaunch("")
	return cmd
}

// handleOnboardingMsg handles the overlay's own messages. ok is false for any
// other message.
func (h *Home) handleOnboardingMsg(msg tea.Msg) (handled bool) {
	switch m := msg.(type) {
	case onboardingStatusMsg:
		h.onboarding.applyStatus(m)
		return true
	case onboardingLoginDoneMsg:
		// Back from the login: re-check that agent's sign-in right away.
		o := h.onboarding
		if !o.IsVisible() {
			return true
		}
		if c := o.statusCheck(m.agent); c != nil {
			o.loginAgent = m.agent
			o.notice = "Checking the sign-in..."
			h.queueOnboarding(c)
		}
		return true
	}
	return false
}

// ---- View ----

func (o *OnboardingOverlay) View() string {
	if !o.visible {
		return ""
	}
	dialogWidth := 76
	if o.width > 0 && o.width < dialogWidth+4 {
		dialogWidth = o.width - 4
		if dialogWidth < 40 {
			dialogWidth = 40
		}
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorCyan).
		Background(ColorSurface).
		Padding(1, 2).
		Width(dialogWidth)
	inner := dialogWidth - style.GetHorizontalPadding()

	var s dialogSections
	if o.page == onboardingPageTour {
		s = o.tourSections(inner)
	} else {
		s = o.checkSections(inner)
	}
	// Keep the box colour behind every styled name, detail and footer.
	surface := surfaceBackgroundSGR()
	for _, part := range []*[]string{&s.head, &s.body, &s.foot} {
		for i, l := range *part {
			(*part)[i] = fillSurfaceAfterResets(l, surface)
		}
	}
	return lipgloss.Place(o.width, o.height, lipgloss.Center, lipgloss.Center, renderFittedDialog(style, o.height, s))
}

func onboardingWrap(width int, text string) string {
	return lipgloss.NewStyle().Width(width).Render(text)
}

// onboardingCommandLine puts a shell command on its own line so a copy gives
// the whole command (ASK-211 round 3: wrapped commands broke on copy). It
// keeps indent when the command fits after it, drops the indent when that
// is what makes it fit, and truncates with … only when it cannot fit at all.
func onboardingCommandLine(indent, cmd string, width int) string {
	switch w := lipgloss.Width(cmd); {
	case lipgloss.Width(indent)+w <= width:
		return indent + cmd
	case w <= width:
		return cmd
	}
	return truncateDisplay(cmd, max(1, width), "…")
}

func displayFolder(folder string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if folder == home {
			return "~"
		}
		if strings.HasPrefix(folder, home+string(filepath.Separator)) {
			return "~" + folder[len(home):]
		}
	}
	return folder
}

// TmuxInstallLines are the tmux install commands for goos. The in-TUI check
// page and the printed one (cmd/agent-deck check_text.go) both use them.
func TmuxInstallLines(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"Mac:    brew install tmux"}
	case "linux":
		return []string{
			"Debian/Ubuntu:  sudo apt install tmux",
			"Fedora/RHEL:    sudo dnf install tmux",
			"Arch:           sudo pacman -S tmux",
		}
	}
	return []string{"See https://github.com/tmux/tmux/wiki/Installing"}
}

// onboardingGOOS is the OS the tmux install line is for (a seam for tests).
var onboardingGOOS = runtime.GOOS

func (o *OnboardingOverlay) checkSections(width int) dialogSections {
	title := lipgloss.NewStyle().Bold(true).Foreground(ColorCyan)
	text := lipgloss.NewStyle().Foreground(ColorText)
	name := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	ok := lipgloss.NewStyle().Foreground(ColorGreen)
	warn := lipgloss.NewStyle().Foreground(ColorYellow)
	bad := lipgloss.NewStyle().Foreground(ColorRed)
	help := lipgloss.NewStyle().Foreground(ColorComment)

	const nameW = 13
	row := func(mark string, n lipgloss.Style, label, detail string, detailStyle lipgloss.Style) string {
		avail := width - 2 - nameW - 1
		return mark + " " + n.Render(padRight(label, nameW)) + " " + detailStyle.Render(truncateDisplay(detail, avail, "…"))
	}
	indent := strings.Repeat(" ", 2+nameW+1)

	var s dialogSections
	s.head = []string{title.Render("Agent Deck · quick check"), ""}

	if o.tmuxFound {
		v := o.tmuxVersion
		if v == "" {
			v = "installed"
		}
		s.body = append(s.body, row(ok.Render("✓"), name, "tmux", v, dim))
	} else {
		s.body = append(s.body, row(bad.Render("✗"), bad.Bold(true), "tmux", "not found. Every session runs inside tmux.", bad))
		for _, l := range TmuxInstallLines(onboardingGOOS) {
			s.body = append(s.body, indent+dim.Render(truncateDisplay(l, width-len(indent), "…")))
		}
	}
	s.body = append(s.body, row(ok.Render("✓"), name, "agent-deck", "v"+Version, dim))
	s.body = append(s.body, "", title.Render("Your agents"))

	for _, a := range o.agents {
		if !a.Found {
			if a.Install == "" {
				s.body = append(s.body, row(warn.Render("x"), name, a.Display, "not installed", dim))
				continue
			}
			s.body = append(s.body, row(warn.Render("x"), name, a.Display, "not installed. Install with:", dim))
			s.body = append(s.body, dim.Render(onboardingCommandLine(indent, a.Install, width)))
			continue
		}
		var detail string
		detailStyle := dim
		switch a.Auth {
		case authSignedIn:
			detail, detailStyle = "installed · signed in", ok
		case authSignedOut, authTimedOut:
			detail, detailStyle = "installed · "+a.Auth.label(), warn
		case authChecking, authUnknown:
			detail = "installed · " + a.Auth.label()
		default:
			detail = "installed · you sign in on the first session"
		}
		s.body = append(s.body, row(ok.Render("✓"), name, a.Display, detail, detailStyle))
	}
	s.body = append(s.body, row(" ", name, "Plain shell", "always works", dim))

	s.foot = []string{""}
	if o.flash != "" {
		style := ok
		if !strings.HasPrefix(o.flash, "Checked again: ") || strings.Contains(o.flash, "not found") || strings.Contains(o.flash, "no agent") {
			style = warn
		}
		s.foot = append(s.foot, style.Render(onboardingWrap(width, o.flash)), "")
	}
	if o.notice != "" {
		style := ok
		if strings.Contains(o.notice, "not signed in") {
			style = warn
		}
		s.foot = append(s.foot, style.Render(onboardingWrap(width, o.notice)), "")
	}
	var items []string
	switch {
	case !o.atStart:
		items = []string{"Esc close"}
		if a := o.loginTarget(); a != nil {
			items = append(items, "l sign in to "+a.Display)
		}
		items = append(items, "r re-check", "Tab what you can do")
	case !o.tmuxFound:
		items = []string{"r re-check after you install", "q quit"}
	case !o.anyAgentFound():
		s.foot = append(s.foot, text.Render(onboardingWrap(width, "No agent yet. You can still start with a Plain shell.")), "")
		items = []string{"Enter continue with Plain shell", "r re-check", "q quit"}
	default:
		items = []string{"Enter continue"}
		if a := o.loginTarget(); a != nil {
			items = append(items, "l sign in to "+a.Display)
		}
		items = append(items, "r re-check", "q quit")
	}
	s.foot = append(s.foot, help.Render(renderDialogFooterRows(width, 2, " · ", items)))
	return s
}

// onboardingDisplayOf is choice's display name among agents ("Plain shell"
// for a plain shell).
func onboardingDisplayOf(agents []onboardingAgent, choice string) string {
	if choice == "shell" {
		return "a Plain shell"
	}
	for _, a := range agents {
		if a.Name == choice {
			return a.Display
		}
	}
	return choice
}

func (o *OnboardingOverlay) tourSections(width int) dialogSections {
	title := lipgloss.NewStyle().Bold(true).Foreground(ColorCyan)
	keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	text := lipgloss.NewStyle().Foreground(ColorText)
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	help := lipgloss.NewStyle().Foreground(ColorComment)

	k := o.keys
	join := func(keys ...string) string {
		var out []string
		for _, key := range keys {
			if strings.TrimSpace(key) != "" {
				out = append(out, key)
			}
		}
		return strings.Join(out, " / ")
	}
	rows := []struct{ what, keys, says string }{
		{"Sessions", join(k.newSession, k.quickCreate), "new session (Enter creates) / quick create. This list is all of them."},
		{"Switch", join("Enter", k.detach, k.altSession), "open the selected session / back to this list / the previous one."},
		{"Send", join(k.prompt, k.queue), "message a session without opening it / queue one for after its turn."},
		{"Conductor", "agent-deck conductor setup <name>", "one agent that watches the others and asks you only when needed."},
		{"Remotes", "agent-deck remote add <name> user@host", "plug in any machine; its sessions show up here."},
	}
	s := dialogSections{head: []string{title.Render("What you can do"), ""}}
	for _, r := range rows {
		says := strings.Split(onboardingWrap(width-11, r.says), "\n")
		for i := range says {
			says[i] = strings.Repeat(" ", 11) + strings.TrimRight(says[i], " ")
		}
		s.body = append(s.body,
			text.Render(padRight(r.what, 10))+" "+keyStyle.Render(r.keys),
			dim.Render(strings.Join(says, "\n")))
	}
	more := []string{}
	if k.help != "" {
		more = append(more, k.help+" all keys")
	}
	if k.tour != "" {
		more = append(more, k.tour+" this page")
	}
	if k.report != "" {
		more = append(more, k.report+" report a problem")
	}
	if k.whatsNew != "" {
		more = append(more, k.whatsNew+" what's new")
	}
	s.foot = []string{""}
	if len(more) > 0 {
		s.foot = append(s.foot, dim.Render(onboardingWrap(width, strings.Join(more, " · ")+".")), "")
	}
	s.foot = append(s.foot, help.Render(renderDialogFooterRows(width, 2, " · ", []string{"Tab agents & sign-in", "Esc close"})))
	return s
}

func padRight(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}
