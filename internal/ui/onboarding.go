package ui

// First-run onboarding (ASK-131): a Welcome page and a "What you can do"
// page in one overlay.
//
//   - Welcome: what agent-deck is, which coding agents are installed and
//     signed in, an install hint for each missing one, and a guided sign-in
//     that runs the tool's own login flow in a session. Enter opens the
//     ordinary New Session dialog (the `n` path) with the highlighted agent
//     and this folder filled in, so the next Enter creates it.
//   - What you can do: the five things that matter, each with its real key.
//     It opens once after the first session made from the Welcome, and any
//     time with the show_around hotkey (default alt+w, "Show me around").
//
// The Welcome opens by itself only on a fresh install: no config.toml (the
// condition that used to open the setup wizard), no sessions and no
// onboarding marker. It replaces the setup wizard there; an install with
// sessions but no config.toml still gets the wizard exactly as before.
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
	// onboardingStatusRunner runs a tool's own sign-in status command and
	// returns its exit code.
	onboardingStatusRunner = runOnboardingStatus
	// onboardingStatusTimeout bounds one status command.
	onboardingStatusTimeout = 5 * time.Second
	// onboardingRecheckEvery is how often sign-in is re-checked after a
	// login session was started, while the Welcome is open.
	onboardingRecheckEvery = 3 * time.Second
	// onboardingRecheckFor bounds that re-checking.
	onboardingRecheckFor = 10 * time.Minute
	// onboardingStartLogin creates the login session.
	onboardingStartLogin = func(h *Home, req onboardingLoginRequest) tea.Cmd { return h.createOnboardingLoginSession(req) }
	// onboardingCmdObserver, when set (tests), receives the commands the
	// overlay queues instead of the Bubble Tea runtime.
	onboardingCmdObserver func(tea.Cmd)
)

type onboardingPage int

const (
	onboardingPageWelcome onboardingPage = iota
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

// onboardingLoginRequest is the session the Log in action starts.
type onboardingLoginRequest struct {
	Agent   string
	Title   string
	Path    string
	Tool    string // always "shell": the tool's own login command runs in it
	Command string // the login command, pinned to the account a new session would use
}

type onboardingStatusMsg struct {
	agent string
	auth  onboardingAuth
}

type onboardingRecheckMsg struct{}

// onboardingKeys are the real, resolved keys the pages name.
type onboardingKeys struct {
	newSession, quickCreate, detach, altSession, prompt, queue, report, tour, help string
}

// OnboardingOverlay is the Welcome / What you can do overlay.
type OnboardingOverlay struct {
	visible       bool
	page          onboardingPage
	firstRun      bool // opened by a fresh install; dismissing it finishes first run
	agents        []onboardingAgent
	cursor        int // index into choices()
	folder        string
	width, height int
	keys          onboardingKeys

	loginAgent    string    // agent whose login session was started
	loginDeadline time.Time // stop re-checking after this
	notice        string    // confirmation line ("Claude Code is signed in.")
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

// choices are the selectable rows: every installed agent, then a plain shell.
func (o *OnboardingOverlay) choices() []string {
	var out []string
	for _, a := range o.agents {
		if a.Found {
			out = append(out, a.Name)
		}
	}
	return append(out, "shell")
}

// Selected is the highlighted choice ("shell" for a plain shell).
func (o *OnboardingOverlay) Selected() string {
	c := o.choices()
	if o.cursor < 0 || o.cursor >= len(c) {
		return c[0]
	}
	return c[o.cursor]
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
	o.agents = detectOnboardingAgents()
	o.cursor = 0
}

// hide closes the overlay and stops any sign-in re-checking.
func (o *OnboardingOverlay) hide() {
	o.visible = false
	o.firstRun = false
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
	if msg.agent == o.loginAgent && msg.auth == authSignedIn {
		o.loginAgent = ""
		o.notice = a.Display + " is signed in. You can start it now."
		return true
	}
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

// loginRequest builds the session the Log in action starts for agent: a
// shell session that runs the tool's own login command (`claude auth login`,
// `codex login`), on the config dir a new session would use. A shell, not a
// Claude session, so a Claude that was never set up does not show its own
// first-run screens before the login.
func (o *OnboardingOverlay) loginRequest(agent string, instances []*session.Instance) (onboardingLoginRequest, bool) {
	a := o.agent(agent)
	if a == nil || !a.canLogIn() {
		return onboardingLoginRequest{}, false
	}
	req := onboardingLoginRequest{
		Agent: agent,
		Title: ensureUniqueSessionTitle(agent+"-login", instances),
		Path:  o.folder,
	}
	req.Tool, req.Command = "shell", a.Login
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

// onboardingSeen reports whether the Welcome was already shown. An
// unresolvable path counts as seen (never nag).
func onboardingSeen() bool {
	path := onboardingSeenPath()
	if path == "" {
		return true
	}
	_, err := os.Stat(path)
	return err == nil
}

// onboardingMarkerExists reports whether the marker is positively there
// (an unresolvable path is false here, unlike onboardingSeen).
func onboardingMarkerExists() bool {
	path := onboardingSeenPath()
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func markOnboardingSeen() {
	path := onboardingSeenPath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
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
	if page == onboardingPageWelcome {
		h.queueOnboarding(h.onboarding.statusChecks()...)
	}
}

// maybeStartOnboarding runs on the first session load. On a fresh install
// (the setup wizard is up on its first step because there is no config.toml,
// there are no sessions, and the Welcome was never shown) the Welcome takes
// the wizard's place. The marker is written as it opens, so it opens by
// itself at most once.
func (h *Home) maybeStartOnboarding(noSessions bool) {
	if !noSessions || !h.setupWizard.IsVisible() || h.setupWizard.currentStep != stepWelcome || onboardingSeen() {
		return
	}
	h.setupWizard.Hide()
	markOnboardingSeen()
	h.openOnboarding(onboardingPageWelcome, true)
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
	h.saveFirstRunConfig(w.GetConfig())
}

// saveFirstRunConfig saves a first-run config merged onto disk, so fields the
// wizard does not manage survive (#1067), and reloads what depends on it.
func (h *Home) saveFirstRunConfig(config *session.UserConfig) {
	merged, mergeErr := session.MergePanelConfigOntoDisk(config)
	if mergeErr == nil && merged != nil {
		config = merged
	}
	if err := session.SaveUserConfig(config); err != nil {
		h.err = err
		h.errTime = time.Now()
	}
	_, _ = session.ReloadUserConfig()
	h.reloadHotkeysFromConfig()
	if defaultTool := session.GetDefaultTool(); defaultTool != "" {
		h.newDialog.SetDefaultTool(defaultTool)
	}
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
			o.page = onboardingPageWelcome
			o.agents = detectOnboardingAgents()
			o.cursor = 0
			h.queueOnboarding(o.statusChecks()...)
		}
		return h, nil
	}
	switch key {
	case "esc":
		h.closeOnboarding()
	case "up", "k":
		if o.cursor > 0 {
			o.cursor--
		}
	case "down", "j":
		if o.cursor < len(o.choices())-1 {
			o.cursor++
		}
	case "tab":
		o.page = onboardingPageTour
		o.loginAgent = ""
	case "r":
		h.queueOnboarding(o.statusChecks()...)
	case "l":
		h.startOnboardingLogin(o.Selected())
	case "enter":
		tool := o.Selected()
		firstRun := o.firstRun
		h.closeOnboarding()
		// Local dialog on this folder, as the Welcome says: not the remote
		// dialog a remote cursor row would route `n` to, and not the cursor
		// group's default path.
		cmd := h.openLocalNewSessionDialog()
		if h.newDialog.IsVisible() {
			h.newDialog.SetPath(o.folder)
			if tool == "shell" {
				h.newDialog.SetDefaultTool("")
			} else {
				h.newDialog.SetDefaultTool(tool)
			}
			h.onboardingTourAfterCreate = firstRun
		}
		return h, cmd
	}
	return h, nil
}

// startOnboardingLogin starts agent's own login flow in a new session and
// re-checks its sign-in until it reports signed in.
func (h *Home) startOnboardingLogin(agent string) {
	h.instancesMu.RLock()
	req, ok := h.onboarding.loginRequest(agent, h.instances)
	h.instancesMu.RUnlock()
	if !ok {
		return
	}
	h.onboarding.loginAgent = agent
	h.onboarding.loginDeadline = time.Now().Add(onboardingRecheckFor)
	h.onboarding.notice = "Signing in to " + h.onboarding.agent(agent).Display + " in session " + req.Title + ". Come back with " + h.onboarding.keys.detach + "."
	h.onboardingLoginTempID = "onboarding-login-" + agent
	h.queueOnboarding(onboardingStartLogin(h, req), onboardingRecheckTick())
}

func onboardingRecheckTick() tea.Cmd {
	return tea.Tick(onboardingRecheckEvery, func(time.Time) tea.Msg { return onboardingRecheckMsg{} })
}

// createOnboardingLoginSession creates the login session through the normal
// create path; sessionCreatedMsg attaches to it (onboardingLoginTempID).
func (h *Home) createOnboardingLoginSession(req onboardingLoginRequest) tea.Cmd {
	return h.createSessionInGroupWithWorktreeAndOptions(
		req.Title, req.Path, req.Tool, req.Command,
		session.DefaultGroupPath,
		"", "", "", // no worktree
		false, false, nil,
		nil, // no extra claude args
		"",  // no startup query
		"",  // no Claude account: the command pins the config dir
		"",
		false, nil,
		"", "",
		h.onboardingLoginTempID,
		false,
	)
}

// handleOnboardingMsg handles the overlay's own messages. ok is false for any
// other message.
func (h *Home) handleOnboardingMsg(msg tea.Msg) (handled bool) {
	switch m := msg.(type) {
	case onboardingStatusMsg:
		h.onboarding.applyStatus(m)
		return true
	case onboardingRecheckMsg:
		o := h.onboarding
		if !o.IsVisible() || o.page != onboardingPageWelcome || o.loginAgent == "" || time.Now().After(o.loginDeadline) {
			return true
		}
		if a := o.agent(o.loginAgent); a != nil && a.Auth == authSignedIn {
			o.applyStatus(onboardingStatusMsg{agent: o.loginAgent, auth: authSignedIn})
			return true
		}
		h.queueOnboarding(o.statusCheck(o.loginAgent), onboardingRecheckTick())
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
		s = o.welcomeSections(inner)
	}
	return lipgloss.Place(o.width, o.height, lipgloss.Center, lipgloss.Center, renderFittedDialog(style, o.height, s))
}

func onboardingWrap(width int, text string) string {
	return lipgloss.NewStyle().Width(width).Render(text)
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

func (o *OnboardingOverlay) welcomeSections(width int) dialogSections {
	title := lipgloss.NewStyle().Bold(true).Foreground(ColorCyan)
	text := lipgloss.NewStyle().Foreground(ColorText)
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	ok := lipgloss.NewStyle().Foreground(ColorGreen)
	warn := lipgloss.NewStyle().Foreground(ColorYellow)
	sel := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	help := lipgloss.NewStyle().Foreground(ColorComment)

	var s dialogSections
	s.head = []string{
		title.Render("Welcome to Agent Deck"),
		"",
		text.Render(onboardingWrap(width, "Run many AI coding agents at once, each in its own session. Switch between them with one key. Works on any machine, this one or a remote.")),
		"",
		title.Render("Your agents"),
	}

	selected := o.Selected()
	found := 0
	focus := 0
	for _, a := range o.agents {
		marker := "  "
		nameStyle := text
		if a.Found && a.Name == selected {
			marker = sel.Render("> ")
			nameStyle = sel
			focus = len(s.body)
		}
		name := nameStyle.Render(padRight(a.Display, 12))
		var line string
		if !a.Found {
			line = marker + warn.Render("x ") + name + dim.Render(" install: "+a.Install)
			s.body = append(s.body, line)
			continue
		}
		found++
		state := a.Auth.label()
		stateStyle := dim
		switch a.Auth {
		case authSignedIn:
			stateStyle = ok
		case authSignedOut, authTimedOut:
			stateStyle = warn
		}
		line = marker + ok.Render("✓ ") + name + dim.Render(" installed · ") + stateStyle.Render(state)
		if a.canLogIn() && a.Name == selected {
			line += help.Render("  l: log in")
		}
		s.body = append(s.body, line)
	}
	shellMarker, shellStyle := "  ", text
	if selected == "shell" {
		shellMarker, shellStyle = sel.Render("> "), sel
		focus = len(s.body)
	}
	shellNote := " just a terminal, no agent"
	if found == 0 {
		shellNote = " no agent found: start a plain terminal"
	}
	s.body = append(s.body, shellMarker+"  "+shellStyle.Render(padRight("Plain shell", 12))+dim.Render(shellNote))
	s.focus = focus

	start := "Enter: new session with " + o.displayOf(selected) + " in " + displayFolder(o.folder) + ", Enter again starts it."
	s.foot = []string{"", text.Render(onboardingWrap(width, start))}
	if o.notice != "" {
		s.foot = append(s.foot, "", ok.Render(onboardingWrap(width, o.notice)))
	}
	if o.keys.report != "" {
		s.foot = append(s.foot, "", dim.Render(onboardingWrap(width, "Found a bug or have an idea? Press "+o.keys.report+" on the main screen to report it.")))
	}
	items := []string{"↑↓ pick", "Enter start", "l log in", "r re-check", "Tab what you can do", "Esc skip"}
	s.foot = append(s.foot, "", help.Render(renderDialogFooterRows(width, 2, " · ", items)))
	return s
}

func (o *OnboardingOverlay) displayOf(choice string) string {
	if choice == "shell" {
		return "a plain shell"
	}
	if a := o.agent(choice); a != nil {
		return a.Display
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
		more = append(more, k.tour+" shows this again")
	}
	if k.report != "" {
		more = append(more, k.report+" reports a problem")
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
