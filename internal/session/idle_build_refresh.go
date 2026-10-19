package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
// unsent composer draft, has a client attached, carries an auth hold, has no
// conversation id to resume, or has an in-Claude timer (cron, scheduled
// wake-up, Monitor) a restart would cancel. SSH, sandbox and wrapper sessions
// do not run the local binary and are never refreshed. A conductor takes part
// only when opted in ([conductors.<name>.sessions]), and only after
// idleRefreshConductorDwell; any other session after idleRefreshDwell. A Codex
// session takes part only on a pass where the daemon probed its status itself,
// since nothing else knows its background state.
//
// The `session restart --if-idle` it runs takes the send lock, recaptures the
// pane and refuses unless the session is still idle with an empty prompt, so a
// message or keystroke in the gap is never lost. Two failed restarts in a row
// in a profile trip a breaker for the rest of the day.
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
	idleRefreshTimers     = defaultIdleRefreshTimers
	idleRefreshSuppressed = update.AutoUpdateSuppressed
	idleRefreshNow        = time.Now
)

// idleRefreshCandidate is everything the cheap gate needs, read without
// touching tmux.
type idleRefreshCandidate struct {
	Tool            string
	Status          string
	NotLocal        bool // SSH, sandbox or wrapper: not the local binary
	Conductor       bool
	ConductorOptIn  bool
	Probed          bool // the daemon refreshed this status itself this pass
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
	case c.NotLocal:
		return "not a local session"
	case c.Conductor && !c.ConductorOptIn:
		return "conductor not opted in"
	case c.Tool == "codex" && !c.Probed:
		return "codex background state unknown"
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
		if strings.Contains(strings.ToLower(tailOf(tmux.StripANSI(v.Raw), 15)), "background terminal") {
			return "background work"
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
	failures    map[string]int       // profile -> consecutive failed restarts
	tripped     map[string]string    // profile -> day the breaker tripped
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
		failures:    map[string]int{},
		tripped:     map[string]string{},
	}
}

// idleRefreshBreakerAfter consecutive failed restarts (a login that broke, a
// pane that dies on boot) stop the profile's refreshes for the day.
const idleRefreshBreakerAfter = 2

func idleRefreshDay(t time.Time) string { return t.Local().Format("2006-01-02") }

// runIdleBuildRefreshPass restarts at most one idle, stale-build session of
// profile onto its tool's installed build. The restart runs off the poll loop.
//
// probed holds the instances whose status this pass refreshed itself (no live
// TUI); only those have a known Codex background state.
func (d *TransitionDaemon) runIdleBuildRefreshPass(profile string, instances []*Instance, statuses map[string]string, probed map[string]string) {
	cfg, err := LoadUserConfig()
	if err != nil || !cfg.AutoRefreshIdleBuildsEnabled(profile) {
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
	if st.tripped[profile] == idleRefreshDay(now) {
		return
	}
	// The gate below still runs while a restart is in flight or spaced, so
	// activity in the meantime resets every other session's dwell.
	held := st.inFlight[profile] || now.Sub(st.lastRestart[profile]) < idleRefreshSpacing
	builds := map[string]toolBuild{} // resolved once per pass per command
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		k := profile + "|" + inst.ID
		_, wasProbed := probed[inst.ID]
		c := idleRefreshCandidate{
			Tool:            inst.Tool,
			Status:          statuses[inst.ID],
			NotLocal:        inst.SSHHost != "" || inst.IsSandboxed() || strings.TrimSpace(inst.Wrapper) != "",
			Conductor:       inst.IsConductor,
			Probed:          wasProbed,
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
		if name := inst.ResolveConductorName(); name != "" {
			c.Conductor = true
			c.ConductorOptIn = cfg.ConductorAutoRefreshIdleBuilds(name)
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
		if held || now.Sub(since) < idleRefreshDwellFor(c.Conductor) {
			continue
		}
		delete(st.idleSince, k)
		delete(st.lastCheck, k)
		if inst.Tool == "claude" {
			// Read once the dwell is over, not every poll: the transcript is
			// the only record of a timer the pane does not show.
			if timer := idleRefreshTimers(inst, now); timer != "" {
				continue // the next dwell looks again
			}
		}
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
	defer st.mu.Unlock()
	now := idleRefreshNow()
	st.inFlight[profile] = false
	st.lastRestart[profile] = now
	switch {
	case err != nil:
		st.failures[profile]++
		if st.failures[profile] >= idleRefreshBreakerAfter {
			st.tripped[profile] = idleRefreshDay(now)
			st.saveTried()
			appendIdleRefreshLog(profile, "", "", "", toolBuild{}, 0, false,
				fmt.Errorf("breaker: %d failed restarts in a row, no more refreshes in this profile today", st.failures[profile]))
		}
	case skipped:
		// Refused (no longer idle, or just restarted): not this build's one
		// attempt, so a later dwell may try again.
		delete(st.tried, profile+"|"+id)
		st.saveTried()
	default:
		st.failures[profile] = 0
	}
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
	cmd := exec.Command(bin, "-p", profile, "session", "restart", "--json", "--if-idle", id)
	detachIdleRefreshChild(cmd)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return false, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(idleRefreshTimeout):
		// Never killed: a restart cut between respawn and save would leave
		// the session unsaved. The goroutine reaps it when it finishes.
		return false, fmt.Errorf("session restart still running after %s", idleRefreshTimeout)
	}
	out := []byte(stdout.String())
	if err != nil {
		out = append(out, stderr.String()...)
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

// idleRefreshPersisted is runtime/idle-build-refresh.json.
type idleRefreshPersisted struct {
	Tried   map[string]string `json:"tried"`
	Tripped map[string]string `json:"breaker_tripped,omitempty"`
}

// loadTried reads the persisted attempts and breaker trips once per daemon
// process. Caller holds st.mu.
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
	var p idleRefreshPersisted
	if json.Unmarshal(data, &p) != nil {
		return
	}
	for k, v := range p.Tried {
		st.tried[k] = v
	}
	for k, v := range p.Tripped {
		st.tripped[k] = v
	}
}

// saveTried persists the attempts and breaker trips. Caller holds st.mu.
func (st *idleRefreshState) saveTried() {
	path, err := idleRefreshTriedPath()
	if err != nil {
		return
	}
	data, err := json.Marshal(idleRefreshPersisted{Tried: st.tried, Tripped: st.tripped})
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

func tailOf(content string, n int) string {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// defaultIdleRefreshTimers reports an in-Claude timer a restart would cancel,
// or "". A transcript that cannot be read counts as one: unknown is not idle.
func defaultIdleRefreshTimers(inst *Instance, now time.Time) string {
	path := inst.GetJSONLPath()
	if path == "" {
		return "transcript unknown"
	}
	f, err := os.Open(path)
	if err != nil {
		return "transcript unreadable"
	}
	defer f.Close()
	return transcriptTimers(f, now)
}

// transcriptTimers scans a Claude transcript for timers still armed at now:
// a CronCreate without a later CronDelete (counted, not matched by id, so it
// errs towards "armed"), a ScheduleWakeup whose delay has not run out, or a
// Monitor that is persistent or within its timeout.
func transcriptTimers(r io.Reader, now time.Time) string {
	names := [][]byte{[]byte(`"CronCreate"`), []byte(`"CronDelete"`), []byte(`"ScheduleWakeup"`), []byte(`"Monitor"`)}
	crons := 0
	var wakeUntil, monitorUntil time.Time
	persistentMonitor := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		hit := false
		for _, n := range names {
			if bytes.Contains(line, n) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		var rec struct {
			Timestamp time.Time `json:"timestamp"`
			Message   struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		var blocks []struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input struct {
				DelaySeconds float64 `json:"delaySeconds"`
				TimeoutMS    float64 `json:"timeout_ms"`
				Persistent   bool    `json:"persistent"`
			} `json:"input"`
		}
		if json.Unmarshal(rec.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_use" {
				continue
			}
			switch b.Name {
			case "CronCreate":
				crons++
			case "CronDelete":
				if crons > 0 {
					crons--
				}
			case "ScheduleWakeup":
				delay := time.Duration(b.Input.DelaySeconds) * time.Second
				if delay <= 0 || delay > time.Hour {
					delay = time.Hour
				}
				wakeUntil = rec.Timestamp.Add(delay)
			case "Monitor":
				if b.Input.Persistent {
					persistentMonitor = true
				}
				timeout := time.Duration(b.Input.TimeoutMS) * time.Millisecond
				if timeout <= 0 || timeout > time.Hour {
					timeout = time.Hour
				}
				if until := rec.Timestamp.Add(timeout); until.After(monitorUntil) {
					monitorUntil = until
				}
			}
		}
	}
	switch {
	case sc.Err() != nil:
		return "transcript unreadable"
	case crons > 0:
		return "cron"
	case wakeUntil.After(now):
		return "scheduled wake-up"
	case persistentMonitor || monitorUntil.After(now):
		return "monitor"
	}
	return ""
}

// SkipConductorRecoveryTurn makes this process's next spawn of a conductor
// send no recovery turn: an idle build refresh resumes the conversation as it
// was, with nothing new to recover from.
func (i *Instance) SkipConductorRecoveryTurn() { i.skipRecoveryTurn = true }

// IdleRefreshRecheck is the last idle check of `session restart --if-idle`,
// run under the send lock right before the restart: "" when the session is
// still idle at an empty prompt, else why not.
func (i *Instance) IdleRefreshRecheck() string {
	switch st := i.GetStatusThreadSafe(); st {
	case StatusWaiting, StatusIdle:
	default:
		return "status " + string(st)
	}
	view, err := defaultIdleRefreshPane(i)
	if err != nil {
		return err.Error()
	}
	return idleRefreshPaneBlocker(i.Tool, view)
}
