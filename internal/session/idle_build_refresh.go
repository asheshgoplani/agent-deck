package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// Idle build refresh (ASK-14): when a newer Claude Code or Codex CLI build is
// installed, a session that is genuinely idle restarts onto it through the
// ordinary `session restart` path, which resumes its conversation. Without
// this, long-lived sessions and conductors run a stale build for days.
//
// "Genuinely idle" is deliberately narrow. A session is left alone while it is
// running, sits at a question or menu, has background work in flight, holds an
// unsent composer draft, has a client attached, carries an auth hold, or has
// no conversation id to resume. A conductor must also have been idle for
// idleRefreshConductorDwell, any other session for idleRefreshDwell.
//
// A build is "newer" when the file the tool command resolves to was installed
// (its ctime: installers create the file, and npm tarballs pin mtime to 1985)
// after the session last started. Each session gets at most one attempt per
// build (keyed by path, inode, size and mtime, so a chmod is not a new build),
// remembered in runtime/idle-build-refresh.json, and every attempt writes one
// line to logs/idle-build-refresh.log.

const (
	idleRefreshDwell          = 5 * time.Minute
	idleRefreshConductorDwell = 10 * time.Minute
	// idleRefreshRecheck spaces the pane captures of a stale-build session
	// while its dwell runs, so a fleet-wide install costs one capture per
	// session every 30 s instead of one per poll.
	idleRefreshRecheck = 30 * time.Second
	// idleRefreshSpacing is the gap between two refresh restarts in a
	// profile: one boot at a time, never a fleet-wide storm.
	idleRefreshSpacing = 30 * time.Second
	idleRefreshTimeout = 2 * time.Minute
)

// toolBuild identifies an installed tool binary.
type toolBuild struct {
	Path        string
	InstalledAt time.Time
	ID          string // file identity, see fileBuildStat
}

func (b toolBuild) key() string {
	return b.Path + "@" + b.ID
}

// Seams, replaced in tests.
var (
	resolveToolBuild      = defaultResolveToolBuild
	idleRefreshPane       = defaultIdleRefreshPane
	restartForIdleRefresh = defaultRestartForIdleRefresh
	idleRefreshSuppressed = update.AutoUpdateSuppressed
	idleRefreshNow        = time.Now
)

// idleRefreshCandidate is everything the cheap gate needs, read without
// touching tmux.
type idleRefreshCandidate struct {
	Tool            string
	Status          string
	AuthHeld        bool
	HasConversation bool
	LastStartedAt   time.Time
	Build           toolBuild
	BuildKnown      bool
	AlreadyTried    bool
}

// idleRefreshCheapBlocker returns why a session cannot be refreshed before
// its pane is looked at, or "" when the pane check may run.
func idleRefreshCheapBlocker(c idleRefreshCandidate) string {
	switch {
	case c.Tool != "claude" && c.Tool != "codex":
		return "tool"
	case c.Status != string(StatusWaiting) && c.Status != string(StatusIdle):
		return "status " + c.Status
	case c.AuthHeld:
		return "auth_hold"
	case !c.HasConversation:
		return "no conversation id"
	case c.LastStartedAt.IsZero():
		return "start time unknown"
	case !c.BuildKnown:
		return "build unknown"
	case !c.Build.InstalledAt.After(c.LastStartedAt):
		return "current build"
	case c.AlreadyTried:
		return "already tried this build"
	}
	return ""
}

// idleRefreshPaneView is what the pane check reads from tmux.
type idleRefreshPaneView struct {
	Substate           Substate
	BackgroundInFlight bool
	Raw                string
	Attached           bool
}

// idleRefreshPaneBlocker returns why the pane is not idle at an empty
// prompt, or "" when it is.
func idleRefreshPaneBlocker(tool string, v idleRefreshPaneView) string {
	if v.Attached {
		return "client attached"
	}
	if v.BackgroundInFlight {
		return "background work"
	}
	if tool != "codex" && tmux.ParseClaudeBackgroundWork(tmux.StripANSI(v.Raw)).InFlight() {
		// Includes the footer's shell counter, which the substate does not
		// count as pane-proven work: a restart would kill those shells.
		return "background work"
	}
	var draft string
	var visible bool
	if tool == "codex" {
		// Codex has no idle-at-empty-prompt substate; any refinement at all
		// (menu, usage limit, auth banner) means it is not plainly idle.
		if v.Substate != SubstateNone {
			return "substate " + string(v.Substate)
		}
		draft, visible = send.CodexComposerDraft(v.Raw, tmux.StripANSI)
	} else {
		if v.Substate != SubstateIdleAtEmptyPrompt {
			return "substate " + string(v.Substate)
		}
		if send.DialogOpen(tmux.StripANSI(v.Raw)) {
			return "dialog open"
		}
		draft, visible = send.ComposerDraft(v.Raw, tmux.StripANSI)
	}
	if !visible {
		return "no composer"
	}
	if draft != "" {
		return "composer draft"
	}
	return ""
}

func idleRefreshDwellFor(isConductor bool) time.Duration {
	if isConductor {
		return idleRefreshConductorDwell
	}
	return idleRefreshDwell
}

// idleRefreshState is the daemon's per-process bookkeeping. tried is also
// persisted so a daemon recycle cannot retry a build.
type idleRefreshState struct {
	mu          sync.Mutex
	idleSince   map[string]time.Time // profile|id -> first idle pane check
	lastCheck   map[string]time.Time
	lastRestart map[string]time.Time // profile -> last refresh restart
	inFlight    map[string]bool      // profile -> restart running
	tried       map[string]string    // profile|id -> build key
	triedLoaded bool
	wg          sync.WaitGroup
}

func newIdleRefreshState() *idleRefreshState {
	return &idleRefreshState{
		idleSince:   map[string]time.Time{},
		lastCheck:   map[string]time.Time{},
		lastRestart: map[string]time.Time{},
		inFlight:    map[string]bool{},
		tried:       map[string]string{},
	}
}

// runIdleBuildRefreshPass restarts at most one idle, stale-build session of
// profile onto its tool's installed build. The restart runs off the poll loop.
func (d *TransitionDaemon) runIdleBuildRefreshPass(profile string, instances []*Instance, statuses map[string]string) {
	if cfg, err := LoadUserConfig(); err != nil || !cfg.AutoRefreshIdleBuildsEnabled(profile) {
		return // an unreadable config must not override an opt-out
	}
	if idleRefreshSuppressed() != "" {
		return
	}
	if d.idleRefresh == nil {
		d.idleRefresh = newIdleRefreshState()
	}
	st := d.idleRefresh
	st.mu.Lock()
	defer st.mu.Unlock()
	st.loadTried()
	now := idleRefreshNow()
	// The gate below still runs while a restart is in flight or spaced, so
	// activity in the meantime resets every other session's dwell.
	held := st.inFlight[profile] || now.Sub(st.lastRestart[profile]) < idleRefreshSpacing
	builds := map[string]toolBuild{} // resolved once per pass per command
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		k := profile + "|" + inst.ID
		c := idleRefreshCandidate{
			Tool:            inst.Tool,
			Status:          statuses[inst.ID],
			HasConversation: idleRefreshConversationID(inst) != "",
			LastStartedAt:   inst.LastStartedAt,
		}
		if c.Tool == "claude" || c.Tool == "codex" {
			cmd := idleRefreshCommand(inst)
			b, ok := builds[cmd]
			if !ok {
				b, _ = resolveToolBuild(cmd)
				builds[cmd] = b
			}
			c.Build, c.BuildKnown = b, b.Path != ""
			c.AlreadyTried = c.BuildKnown && st.tried[k] == b.key()
		}
		if idleRefreshCheapBlocker(c) == "" {
			// A file read per session, so only once the rest says stale.
			c.AuthHeld = inst.AuthHold() != nil
		}
		if idleRefreshCheapBlocker(c) != "" {
			delete(st.idleSince, k)
			delete(st.lastCheck, k)
			continue
		}
		if last, ok := st.lastCheck[k]; ok && now.Sub(last) < idleRefreshRecheck {
			continue
		}
		st.lastCheck[k] = now
		view, err := idleRefreshPane(inst)
		if err != nil || idleRefreshPaneBlocker(inst.Tool, view) != "" {
			delete(st.idleSince, k)
			continue
		}
		since, ok := st.idleSince[k]
		if !ok {
			st.idleSince[k] = now
			continue
		}
		if held || now.Sub(since) < idleRefreshDwellFor(inst.IsConductor || inst.ResolveConductorName() != "") {
			continue
		}
		delete(st.idleSince, k)
		delete(st.lastCheck, k)
		st.tried[k] = c.Build.key()
		st.saveTried()
		st.inFlight[profile] = true
		st.wg.Add(1)
		go d.idleRefreshRestart(profile, inst.ID, inst.Title, inst.Tool, c.Build, now.Sub(since))
		return // one restart per pass
	}
}

func (d *TransitionDaemon) idleRefreshRestart(profile, id, title, tool string, b toolBuild, idle time.Duration) {
	st := d.idleRefresh
	defer st.wg.Done()
	skipped, err := restartForIdleRefresh(profile, id)
	appendIdleRefreshLog(profile, id, title, tool, b, idle, skipped, err)
	st.mu.Lock()
	st.inFlight[profile] = false
	st.lastRestart[profile] = idleRefreshNow()
	st.mu.Unlock()
}

func idleRefreshConversationID(inst *Instance) string {
	if inst.Tool == "codex" {
		return inst.CodexSessionID
	}
	return inst.ClaudeSessionID
}

// idleRefreshCommand is the tool command a restart of inst would launch.
func idleRefreshCommand(inst *Instance) string {
	if cmd := strings.TrimSpace(inst.Command); cmd != "" && cmd != inst.Tool {
		return cmd
	}
	if inst.Tool == "claude" {
		return GetClaudeCommandForInstance(inst)
	}
	return GetToolCommand(inst.Tool)
}

// idleRefreshBinDirs are tried after PATH: a launchd or systemd daemon often
// runs with a PATH that lacks the user's install directories.
func idleRefreshBinDirs() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, ".claude", "local"),
		filepath.Join(home, ".npm-global", "bin"),
		filepath.Join(home, ".bun", "bin"),
		"/opt/homebrew/bin",
		"/usr/local/bin",
	}
}

// defaultResolveToolBuild resolves the first word of cmd to the real file it
// runs and when that file was installed. A command that is not a plain
// executable (an alias, an env assignment, a missing binary) is unknown, so
// it is never refreshed.
func defaultResolveToolBuild(cmd string) (toolBuild, bool) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 || strings.Contains(fields[0], "=") {
		return toolBuild{}, false
	}
	path, err := exec.LookPath(fields[0])
	if err != nil && !strings.ContainsRune(fields[0], os.PathSeparator) {
		for _, dir := range idleRefreshBinDirs() {
			if p, lerr := exec.LookPath(filepath.Join(dir, fields[0])); lerr == nil {
				path, err = p, nil
				break
			}
		}
	}
	if err != nil {
		return toolBuild{}, false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return toolBuild{}, false
	}
	at, id, err := fileBuildStat(real)
	if err != nil {
		return toolBuild{}, false
	}
	return toolBuild{Path: real, InstalledAt: at, ID: id}, true
}

func defaultIdleRefreshPane(inst *Instance) (idleRefreshPaneView, error) {
	ts := inst.GetTmuxSession()
	if ts == nil || !inst.Exists() {
		return idleRefreshPaneView{}, fmt.Errorf("no tmux session")
	}
	attached, err := ts.ClientAttached()
	if err != nil {
		return idleRefreshPaneView{}, err
	}
	raw, err := ts.CapturePaneFresh()
	if err != nil {
		return idleRefreshPaneView{}, err
	}
	return idleRefreshPaneView{
		Substate:           inst.Substate(),
		BackgroundInFlight: inst.BackgroundWork().InFlight(),
		Raw:                raw,
		Attached:           attached,
	}, nil
}

// defaultRestartForIdleRefresh runs `agent-deck -p <profile> session restart
// <id>`: the same path a person uses, with its spawn lock, verification,
// conversation resume and save. skipped reports that its freshness or auth
// guard kept the session as it was.
func defaultRestartForIdleRefresh(profile, id string) (skipped bool, err error) {
	bin := FindAgentDeck()
	if bin == "" {
		return false, fmt.Errorf("agent-deck binary not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), idleRefreshTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-p", profile, "session", "restart", "--json", id)
	detachIdleRefreshChild(cmd)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			out = append(out, exitErr.Stderr...)
		}
		return false, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	var res struct {
		Skipped bool `json:"skipped"`
	}
	_ = json.Unmarshal(out, &res)
	return res.Skipped, nil
}

func appendIdleRefreshLog(profile, id, title, tool string, b toolBuild, idle time.Duration, skipped bool, restartErr error) {
	path, err := logDataPath("idle-build-refresh.log")
	if err != nil {
		return
	}
	entry := map[string]any{
		"ts":           idleRefreshNow().Format(time.RFC3339),
		"profile":      profile,
		"session":      id,
		"title":        title,
		"tool":         tool,
		"build":        b.Path,
		"installed_at": b.InstalledAt.Format(time.RFC3339),
		"idle_for":     idle.Round(time.Second).String(),
		"result":       "restarted",
	}
	if skipped {
		entry["result"] = "skipped"
	}
	if restartErr != nil {
		entry["result"] = "failed"
		entry["error"] = restartErr.Error()
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = appendRotatingLogLine(path, line, transitionLogRotation)
}

func idleRefreshTriedPath() (string, error) {
	return runtimeDataPath("idle-build-refresh.json")
}

// loadTried reads the persisted attempts once per daemon process. Caller
// holds st.mu.
func (st *idleRefreshState) loadTried() {
	if st.triedLoaded {
		return
	}
	st.triedLoaded = true
	path, err := idleRefreshTriedPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &st.tried)
	if st.tried == nil {
		st.tried = map[string]string{}
	}
}

// saveTried persists the attempts. Caller holds st.mu.
func (st *idleRefreshState) saveTried() {
	path, err := idleRefreshTriedPath()
	if err != nil {
		return
	}
	data, err := json.Marshal(st.tried)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
