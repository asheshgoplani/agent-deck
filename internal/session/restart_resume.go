package session

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A restart of a child idle at its prompt (the idle build refresh, or a
// `session restart`) kills its process and resumes the conversation in a new
// one. Every path that reports a child's flips saw that as news: the old
// process exiting (Claude's SessionEnd hook, an error) and the new one
// settling at its prompt (SessionStart, a running->waiting flip with the old
// reply), so the parent got an urgent error and its old reply again (#2792).
//
// The restart leaves a token in runtime/restart-resume/<id>.json, decided on
// facts, not on time windows:
//
//   - before the restart: the pane pid it kills, where the transcript ends
//     (or, with no transcript, the turn signal: the Codex completion), and
//     that the child was idle at its prompt. A child in the middle of a turn
//     gets no token, so its parent hears that it stopped, as always.
//   - after the restart: the pane pid of the new process (<id>.spawned.json,
//     written only by the restart, so no read-modify-write can lose it).
//
// The daemon then holds back exactly two things. The exit of the old process,
// and only an exit: an error while the old pid is gone, the hook file holds
// its SessionEnd (stamped no later than a second, the file's resolution, after
// the restart recorded the new process; or, once the new process's
// SessionStart replaced it, the hook event history does), the new process is alive and its
// pane shows no error (that one SessionEnd, however often the daemon sees
// it; the hold lasts per token, so the next restart's token holds its own
// old SessionEnd). And the new process's first arrival at its prompt with no new user
// or assistant record in the transcript (and no
// SessionEnd of its own), which becomes one info-tier line instead and
// spends the token. Any other error after the restart is the new process's
// and goes to the parent at once, urgent, with what is wrong instead of the
// old reply: an error the pane shows (an expired login, an API error, a model
// that is unavailable), a hook reporting it, a resume that exited, or an error
// that cannot be told apart. New turns are delivered as before. An error before the restart has
// recorded its new process waits for it. A new process that does not reach
// its prompt within the boot timeout is reported to the parent as an urgent
// error, and the token is spent.

// RestartReasonIdleBuildRefresh is the restart reason of `session restart
// --if-idle`, the idle build refresh.
const RestartReasonIdleBuildRefresh = "idle build refresh"

// restartResumeMaxScan caps the transcript bytes read past the token; a boot
// appends a few hundred, so more is treated as new activity.
const restartResumeMaxScan = 1 << 20

// restartTokenOrphanAge is when the sweep removes a token of a session no
// profile has any more.
const restartTokenOrphanAge = 24 * time.Hour

// restartBootTimeout is how long the new process has to reach its prompt.
// AGENTDECK_RESTART_BOOT_TIMEOUT overrides it (tests).
func restartBootTimeout() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("AGENTDECK_RESTART_BOOT_TIMEOUT")); err == nil && d > 0 {
		return d
	}
	return 2 * time.Minute
}

type restartToken struct {
	At     time.Time `json:"at"`
	Reason string    `json:"reason,omitempty"`
	Tool   string    `json:"tool,omitempty"`
	// OldPID is the pane pid the restart kills.
	OldPID int `json:"old_pid,omitempty"`
	// Transcript and Offset are the transcript file and its size at the
	// restart; Signal is the turn signal for a tool without one.
	Transcript string `json:"transcript,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	Signal     string `json:"signal,omitempty"`

	// NewPID is the pane pid of the resumed process and SpawnedAt when the
	// restart recorded it, from the spawned file; 0 while the restart runs.
	NewPID    int       `json:"-"`
	SpawnedAt time.Time `json:"-"`
}

type restartSpawned struct {
	NewPID int       `json:"new_pid"`
	At     time.Time `json:"at"`
}

// SetRestartReason names why the next restart happens, for its token.
func (i *Instance) SetRestartReason(reason string) { i.restartReason = reason }

func restartTokenPath(id, suffix string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	return runtimeDataPath(filepath.Join("restart-resume", id+suffix))
}

// noteRestartToken records the token before a restart. Only a child idle at
// its prompt gets one; a restart that cuts a turn short (running, a tool call
// newer than the last text, a reply still flushing) is news, and so is one
// with nothing to compare against (no transcript or signal).
func noteRestartToken(i *Instance, now time.Time) {
	if i == nil {
		return
	}
	clearRestartToken(i.ID)
	switch i.GetStatusThreadSafe() {
	case StatusWaiting, StatusIdle:
	default:
		return
	}
	if facts, ok := instanceTurnFacts(i); ok && (facts.Pending || facts.FinalTextPending) {
		return
	}
	tok := restartToken{At: now, Reason: i.restartReason, Tool: i.Tool, OldPID: instancePanePID(i)}
	if path := i.GetJSONLPath(); path != "" {
		if info, err := os.Stat(path); err == nil {
			tok.Transcript, tok.Offset = path, info.Size()
		}
	}
	if tok.Transcript == "" {
		if tok.Signal = transitionEventOutputHash(i); tok.Signal == "" {
			return
		}
	}
	writeRestartFile(i.ID, ".json", tok)
}

// markRestartSpawned records the resumed process's pane pid after a restart.
// A pid that cannot be read (a tmux call that timed out, or a pane that is
// already gone) is recorded as unknown (-1): nothing is held back on it, and
// the sweep judges such a child by its status alone.
func markRestartSpawned(i *Instance, now time.Time) {
	if _, ok := readRestartToken(i.ID); !ok {
		return
	}
	pid := 0
	for attempt := 0; attempt < 3 && pid <= 0; attempt++ {
		if attempt > 0 {
			time.Sleep(restartPIDRetryDelay)
		}
		pid = instancePanePID(i)
	}
	if pid <= 0 {
		pid = restartPIDUnknown
	}
	writeRestartFile(i.ID, ".spawned.json", restartSpawned{NewPID: pid, At: now})
}

var restartPIDRetryDelay = 200 * time.Millisecond

// restartPIDUnknown is a new pid the restart could not read.
const restartPIDUnknown = -1

// restartPaneSubstate is what the child's pane shows now (a seam for tests).
// It captures the pane, so it is read only for a child holding a token.
var restartPaneSubstate = func(inst *Instance) string { return string(inst.Substate()) }

// restartHookStatus is the child's hook file (a seam for tests).
var restartHookStatus = readHookStatusFile

// hookSessionEnd reports whether the hook file holds a SessionEnd (a
// process that exited); its UpdatedAt says which one.
func hookSessionEnd(hs *HookStatus) bool {
	return hs != nil && (strings.EqualFold(strings.TrimSpace(hs.Event), "SessionEnd") || normalizeStatusString(hs.Status) == "dead")
}

var instancePanePID = func(i *Instance) int {
	ts := i.GetTmuxSession()
	if ts == nil {
		return 0
	}
	pid, err := ts.PanePID()
	if err != nil {
		return 0
	}
	return pid
}

var restartPIDAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	return err == nil && proc.Signal(syscall.Signal(0)) == nil
}

func writeRestartFile(id, suffix string, v any) {
	path, err := restartTokenPath(id, suffix)
	if err != nil {
		return
	}
	data, err := json.Marshal(v)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			tmp := path + ".tmp"
			if err = os.WriteFile(tmp, data, 0o600); err == nil {
				err = os.Rename(tmp, path)
			}
		}
	}
	if err != nil {
		sessionLog.Warn("restart_token_write_failed", slog.String("instance_id", id), slog.String("error", err.Error()))
	}
}

func readRestartToken(id string) (restartToken, bool) {
	var tok restartToken
	path, err := restartTokenPath(id, ".json")
	if err != nil {
		return tok, false
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &tok) != nil || (tok.Signal == "" && tok.Transcript == "") {
		return tok, false
	}
	if sp, err := restartTokenPath(id, ".spawned.json"); err == nil {
		var s restartSpawned
		if b, err := os.ReadFile(sp); err == nil && json.Unmarshal(b, &s) == nil && !s.At.Before(tok.At) && s.NewPID != 0 {
			tok.NewPID, tok.SpawnedAt = s.NewPID, s.At
		}
	}
	return tok, true
}

func clearRestartToken(id string) {
	for _, suffix := range []string{".json", ".spawned.json"} {
		if path, err := restartTokenPath(id, suffix); err == nil {
			_ = os.Remove(path)
		}
	}
}

// restartVerdict is what a flip is against the child's restart token.
type restartVerdict int

const (
	restartNews      restartVerdict = iota // deliver as before
	restartOldExit                         // the old process exiting: hold back
	restartBoot                            // the new process at its prompt: the info line
	restartUnsettled                       // the restart has not recorded its new process yet: retry
	restartNewError                        // an error of the new process: urgent, with its own text
)

// paneErrorText names an error state the pane shows (#1400), "" for none.
func paneErrorText(substate string) string {
	switch substate {
	case string(tmux.SubstateAuth401):
		return "a login or connection error (API Error 401, Please run /login, socket closed)"
	case string(tmux.SubstateModelUnavailable):
		return "the model is unavailable"
	}
	return ""
}

// restartState is the daemon's memory of its tokens: which old exits were
// held back already, and the build an idle refresh restarted onto.
type restartState struct {
	mu sync.Mutex
	// exitHeld is the old process's SessionEnd held back per child, with the
	// token it was held under. The same SessionEnd seen again under the same
	// token (one pass judges a flip on two paths, and a later pass re-reads
	// the light while it is still on file) is the same exit, held again; an
	// error with any other evidence is news. A hold under another token is
	// stale: the token can be removed by paths that never reach
	// spendRestartToken (the notifier, the sweep, the restart process).
	exitHeld map[string]restartExitHold
	build    map[string]string
	// banner holds resumed children whose token the unseen-boot info line
	// spent (#2813): the sweep still checks their pane for an error banner
	// until the boot timeout, as it did while the token lived.
	banner map[string]restartBannerWatch
}

// restartExitHold is one held exit: the token's At (which token) and the
// UpdatedAt of the SessionEnd it held (which exit).
type restartExitHold struct {
	tokenAt time.Time
	stamp   time.Time
}

type restartBannerWatch struct {
	profile string
	tok     restartToken
	until   time.Time
}

func (d *TransitionDaemon) restarts() *restartState {
	d.restartOnce.Do(func() {
		d.restart = &restartState{exitHeld: map[string]restartExitHold{}, build: map[string]string{}, banner: map[string]restartBannerWatch{}}
	})
	return d.restart
}

// judgeRestart decides a flip of child id to `to` at ts against its token.
// transcript is the child's current transcript path, signal its turn signal,
// pane what its pane shows (read only when there is a token). For
// restartNewError, the text says what is wrong.
func (d *TransitionDaemon) judgeRestart(id, transcript, signal string, pane func() string, to string, ts time.Time) (restartToken, restartVerdict, string) {
	tok, ok := readRestartToken(id)
	if !ok || ts.Before(tok.At) {
		return tok, restartNews, ""
	}
	substate := pane()
	isError := normalizeStatusString(to) == string(StatusError)
	if banner := paneErrorText(substate); banner != "" && (isError || normalizeStatusString(to) == string(StatusWaiting) || normalizeStatusString(to) == string(StatusIdle)) {
		// An error state on a live pane is never the old exit, and a pane
		// showing one is not back at its prompt.
		d.spendRestartToken(id)
		return tok, restartNewError, "Restarted, but the resumed session shows " + banner + "."
	}
	if tok.NewPID == 0 {
		return tok, restartUnsettled, "" // the restart is still running; the boot timeout bounds this
	}
	newAlive := restartPIDAlive(tok.NewPID)
	hs := restartHookStatus(id)
	// The restart's kill is what ends the old Claude, so its SessionEnd can be
	// written just after the restart recorded the new process; the hook file
	// stamps whole seconds. A SessionEnd up to its resolution later is the old
	// one's (the other facts below still have to hold).
	oldEndBy := tok.SpawnedAt.Add(time.Second)
	oldEnd := hookSessionEnd(hs) && !hs.UpdatedAt.After(oldEndBy)
	newEnd := hookSessionEnd(hs) && hs.UpdatedAt.After(oldEndBy)
	newStart := hs != nil && strings.EqualFold(strings.TrimSpace(hs.Event), "SessionStart") && !hs.UpdatedAt.Before(tok.SpawnedAt.Truncate(time.Second))
	var endStamp time.Time
	fromHistory := false
	if oldEnd {
		endStamp = hs.UpdatedAt
	} else if newStart {
		// The pass read the light from the old SessionEnd, and the new
		// process's SessionStart replaced it in the hook file before this
		// judgement: that SessionEnd is in the hook event history.
		if at, ok := restartSessionEndIn(id, tok.At, oldEndBy); ok {
			oldEnd, endStamp, fromHistory = true, at.Truncate(time.Second), true
		}
	}
	rs := d.restarts()
	switch normalizeStatusString(to) {
	case string(StatusError):
		// Only the old process's exit event: its pid gone, its own SessionEnd,
		// the new process alive. Anything else is the new process's error.
		// One exit is ours, seen as often as the light shows it: a hold
		// already spent on another SessionEnd does not cover this one.
		rs.mu.Lock()
		ours := newAlive && oldEnd && tok.OldPID > 0 && tok.OldPID != tok.NewPID && !restartPIDAlive(tok.OldPID)
		if held, seen := rs.exitHeld[id]; ours && seen && held.tokenAt.Equal(tok.At) && !held.stamp.Equal(endStamp) {
			// The history line takes its own clock read after the hook
			// file's: the same SessionEnd can read one second later there.
			ours = fromHistory && held.stamp.Equal(endStamp.Add(-time.Second))
		}
		if ours {
			rs.exitHeld[id] = restartExitHold{tokenAt: tok.At, stamp: endStamp}
		}
		rs.mu.Unlock()
		if ours {
			return tok, restartOldExit, ""
		}
		d.spendRestartToken(id)
		if tok.NewPID != restartPIDUnknown && (!newAlive || newEnd) {
			return tok, restartNewError, "Restarted, but the resumed session exited."
		}
		return tok, restartNewError, "Restarted, but the resumed session reports an error."
	case string(StatusWaiting), string(StatusIdle):
		if newAlive && !newEnd && restartTranscriptUnchanged(tok, transcript, signal) {
			d.spendRestartToken(id)
			return tok, restartBoot, ""
		}
	}
	d.spendRestartToken(id)
	return tok, restartNews, ""
}

func (d *TransitionDaemon) spendRestartToken(id string) {
	clearRestartToken(id)
	rs := d.restarts()
	rs.mu.Lock()
	delete(rs.exitHeld, id)
	rs.mu.Unlock()
}

func restartTranscriptUnchanged(tok restartToken, transcript, signal string) bool {
	if tok.Transcript != "" {
		return tok.Transcript == transcript && noTurnSince(tok.Transcript, tok.Offset)
	}
	return signal != "" && signal == tok.Signal
}

// restartBootRecord is the notifier's own check (#2792) for a producer that
// reaches the inbox without emitTurn: the new process's arrival at its
// prompt with an unchanged transcript is dropped and spends the token.
// Errors and finished records are always delivered.
func restartBootRecord(event TransitionNotificationEvent) bool {
	if event.restartJudged || event.ChildSessionID == "" {
		return false
	}
	switch normalizeStatusString(event.ToStatus) {
	case string(StatusWaiting), string(StatusIdle):
	default:
		return false
	}
	tok, ok := readRestartToken(event.ChildSessionID)
	if !ok || event.Timestamp.Before(tok.At) || !restartPIDAlive(tok.NewPID) {
		return false
	}
	if !restartTranscriptUnchanged(tok, tok.Transcript, event.LastOutputHash) {
		return false
	}
	clearRestartToken(event.ChildSessionID)
	return true
}

// noTurnSince reports whether the transcript at path gained no user or
// assistant record after offset.
func noTurnSince(path string, offset int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < offset || info.Size()-offset > restartResumeMaxScan {
		return false
	}
	buf := make([]byte, info.Size()-offset)
	if _, err := f.ReadAt(buf, offset); err != nil && err != io.EOF {
		return false
	}
	for _, line := range strings.Split(string(buf), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec struct {
			Type string `json:"type"`
		}
		if json.Unmarshal([]byte(line), &rec) != nil {
			return false // a partial or unknown line: not provably a boot
		}
		if rec.Type == "user" || rec.Type == "assistant" {
			return false
		}
	}
	return true
}

// emitRestartBoot is emitTurn's result for the new process at its prompt:
// one info line for a parented child, never the old reply again. It goes
// straight to the inbox, past the notifier's dedup state, so the turn the
// child last reported stays the reference for later flips.
func (d *TransitionDaemon) emitRestartBoot(inst *Instance, event TransitionNotificationEvent, tok restartToken, selfConductor bool) TransitionNotificationEvent {
	event.DeliveryResult = transitionDeliveryDropped
	if selfConductor || strings.TrimSpace(inst.ParentSessionID) == "" {
		return event
	}
	tool := tok.Tool
	if tool == "" {
		tool = inst.Tool
	}
	event.Tier = TurnTierInfo
	event.Trigger = TurnTriggerSystem
	event.Text = "Restarted while idle; conversation resumed, no new turn."
	if tok.Reason == RestartReasonIdleBuildRefresh {
		onto := "a newer " + tool + " build"
		rs := d.restarts()
		rs.mu.Lock()
		if b := rs.build[inst.ID]; b != "" {
			onto = tool + " " + filepath.Base(b)
		}
		delete(rs.build, inst.ID)
		rs.mu.Unlock()
		event.Text = "Refreshed onto " + onto + " while idle; conversation resumed, no new turn."
	}
	// Its own identity in the inbox: under the old turn's hash it would fold
	// into the record that turn already sent.
	event.LastOutputHash = "restart:" + tok.At.UTC().Format(time.RFC3339Nano)
	return d.commitRestartRecord(inst, event)
}

func (d *TransitionDaemon) commitRestartRecord(inst *Instance, event TransitionNotificationEvent) TransitionNotificationEvent {
	event.restartJudged = true
	committed, transient, reason := d.notifier.commitEventToInbox(event)
	switch {
	case committed:
		event.DeliveryResult = transitionDeliveryCommitted
		_ = BumpInboxStats(statsParentFor(inst), func(s *InboxStats) {
			if event.Tier == TurnTierUrgent {
				s.RecordsUrgent++
			} else {
				s.RecordsInfo++
			}
		})
	case transient:
		event.DeliveryResult = transitionDeliveryFailed
	default:
		event.DeliveryResult = transitionDeliveryDropped
		event.DeadLetterReason = reason
	}
	publishTransitionEvent("session.transition", event)
	return event
}

// emitRestartError delivers an error of the resumed process at once, urgent,
// with what is wrong instead of the child's old reply. It is its own record:
// under the old turn's hash a later flip of that turn would replace it in the
// parent's inbox before the parent read it.
func (d *TransitionDaemon) emitRestartError(inst *Instance, event TransitionNotificationEvent, tok restartToken, text string) TransitionNotificationEvent {
	event.FromStatus, event.ToStatus = string(StatusRunning), string(StatusError) // whatever the flip read, it is an error
	event.Tier = TurnTierUrgent
	event.Trigger = TurnTriggerSystem
	event.Text = text
	event.LastOutputHash = "restart-error:" + tok.At.UTC().Format(time.RFC3339Nano)
	event.restartJudged = true
	result := d.notifier.NotifyTransition(event)
	if result.DeliveryResult == transitionDeliveryCommitted {
		_ = BumpInboxStats(statsParentFor(inst), func(s *InboxStats) { s.RecordsUrgent++ })
	}
	return result
}

// noteRestartBuild remembers the build an idle refresh restarted id onto.
func (d *TransitionDaemon) noteRestartBuild(id, build string) {
	rs := d.restarts()
	rs.mu.Lock()
	rs.build[id] = build
	rs.mu.Unlock()
}

// restartBootSettle is how long after the spawn the sweep keeps checking the
// pane for an error banner before it sends the info line for an unseen boot.
const restartBootSettle = 5 * time.Second

// restartBootUnseen reports a token holder back at its prompt whose boot flip
// was never seen (#2813): its new process alive for restartBootSettle, its own
// SessionStart after the restart, no new SessionEnd and no new turn in the
// transcript. The hook file keeps only the last event, and the old process's
// SessionEnd can land after the new SessionStart (the kill fires it
// asynchronously); then the file holds that old SessionEnd (judgeRestart's
// oldEndBy rule), the light reads error from it, and the new SessionStart is
// proven from the hook event history. The caller has ruled out an error banner.
func restartBootUnseen(inst *Instance, tok restartToken, status string, now time.Time) bool {
	if tok.NewPID <= 0 || !restartPIDAlive(tok.NewPID) || now.Sub(tok.SpawnedAt) < restartBootSettle {
		return false
	}
	st := normalizeStatusString(status)
	atPrompt := st == string(StatusWaiting) || st == string(StatusIdle)
	hs := restartHookStatus(inst.ID)
	switch {
	case hs == nil:
		return false
	case strings.EqualFold(strings.TrimSpace(hs.Event), "SessionStart"):
		// Whole seconds, the hook file's resolution.
		if !atPrompt || hs.UpdatedAt.Before(tok.SpawnedAt.Truncate(time.Second)) {
			return false
		}
	case hookSessionEnd(hs):
		oldEnd := !hs.UpdatedAt.After(tok.SpawnedAt.Add(time.Second)) && tok.OldPID > 0 && tok.OldPID != tok.NewPID && !restartPIDAlive(tok.OldPID)
		if !oldEnd || !(atPrompt || st == string(StatusError)) || !restartSessionStartSince(inst.ID, tok.At) {
			return false
		}
	default:
		return false
	}
	return restartTranscriptUnchanged(tok, inst.GetJSONLPath(), transitionEventOutputHash(inst))
}

// restartSessionStartSince reports whether the hook event history
// (<id>.events.jsonl, written by the hook handler next to the hook file)
// holds a SessionStart at or after since. With the history off ([health]
// enabled = false) or missing, nothing is proven.
var restartSessionStartSince = func(id string, since time.Time) bool {
	for _, ev := range restartHookHistory(id) {
		if strings.EqualFold(strings.TrimSpace(ev.Event), "SessionStart") && !ev.At.Before(since) {
			return true
		}
	}
	return false
}

// restartHistoryClockLag bounds how much later the hook handler's history
// line reads the clock than the hook file's ts (it loads the config and
// writes the status file first): the same event can cross a second there.
const restartHistoryClockLag = 500 * time.Millisecond

// restartSessionEndIn returns the last SessionEnd in the hook event history
// stamped at or after since and, in whole seconds like the hook file, no
// later than by, allowing for the history's later clock read.
func restartSessionEndIn(id string, since, by time.Time) (time.Time, bool) {
	var at time.Time
	found := false
	for _, ev := range restartHookHistory(id) {
		if strings.EqualFold(strings.TrimSpace(ev.Event), "SessionEnd") && !ev.At.Before(since) && !ev.At.Add(-restartHistoryClockLag).Truncate(time.Second).After(by) {
			at, found = ev.At, true
		}
	}
	return at, found
}

type restartHookEvent struct {
	At    time.Time `json:"at"`
	Event string    `json:"event"`
}

// restartHookHistory reads the tail of child id's hook event history
// (<id>.events.jsonl, written by the hook handler next to the hook file).
func restartHookHistory(id string) []restartHookEvent {
	// Non-blocking and regular files only: judgeRestart reads this inside
	// emitTurn, so a FIFO planted at the path must not stall the daemon.
	f, err := os.OpenFile(filepath.Join(filepath.Dir(hookStatusFilePath(id)), filepath.Base(id)+".events.jsonl"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	// The newest events are at the tail; a cut first line does not parse.
	if info.Size() > maxHookStatusFileSize {
		_, _ = f.Seek(info.Size()-maxHookStatusFileSize, io.SeekStart)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxHookStatusFileSize))
	if err != nil {
		return nil
	}
	var events []restartHookEvent
	for _, line := range strings.Split(string(data), "\n") {
		var ev restartHookEvent
		if json.Unmarshal([]byte(line), &ev) == nil {
			events = append(events, ev)
		}
	}
	return events
}

// sweepRestartTokens reports a resumed child whose pane shows an error banner
// at once, sends the info line for one back at its prompt whose boot flip went
// unseen (restartBootUnseen), and spends every token older than the boot
// timeout. A child whose new process is alive and at its prompt with no
// proof of a clean resume, or already on a new turn, gets nothing. Any other
// (the new process dead, an error, a boot still spinning with no new turn) is
// reported to its parent once, as an urgent error. After the info line,
// sweepRestartBanners keeps the banner check until the boot timeout.
func (d *TransitionDaemon) sweepRestartTokens(profile string, byID map[string]*Instance, statuses map[string]string, now time.Time) {
	defer d.sweepRestartBanners(profile, byID, now)
	dir, err := runtimeDataPath("restart-resume")
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	timeout := restartBootTimeout()
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || strings.HasSuffix(id, ".spawned") {
			continue
		}
		tok, ok := readRestartToken(id)
		if !ok {
			clearRestartToken(id)
			continue
		}
		inst := byID[id]
		if inst == nil {
			if now.Sub(tok.At) > restartTokenOrphanAge {
				clearRestartToken(id) // a session no profile has any more
			}
			continue // another profile's child, or gone
		}
		if tok.NewPID != 0 {
			// A new process: what the parent was told about the one before
			// it (a banner, a timeout) is not news about this one. A token
			// with no pid yet (a retry that may fail before it spawns
			// anything) leaves the mark: the old death is still the news.
			delete(d.idleExitTold[profile], id)
			// A resume that went straight to an error banner flips waiting ->
			// error, which no path reports: tell the parent now, with it.
			if banner := paneErrorText(restartPaneSubstate(inst)); banner != "" {
				d.spendRestartToken(id)
				if strings.TrimSpace(inst.ParentSessionID) != "" && !isSelfSuppressedConductor(inst) {
					if d.emitRestartError(inst, TransitionNotificationEvent{
						ChildSessionID: inst.ID, ChildTitle: inst.Title, Profile: profile,
						FromStatus: string(StatusRunning), ToStatus: string(StatusError), Timestamp: now,
					}, tok, "Restarted, but the resumed session shows "+banner+".").DeliveryResult == transitionDeliveryCommitted {
						d.markIdleExitTold(profile, id) // its later death is the same failure
					}
				}
				continue
			}
		}
		if restartBootUnseen(inst, tok, statuses[id], now) {
			// Issue #2813: back at its prompt with no flip seen (real Claude
			// resumes straight to it): the info line now, without waiting
			// for the boot timeout.
			to := normalizeStatusString(statuses[id])
			if to == string(StatusError) {
				to = string(StatusWaiting) // read from the old process's SessionEnd
				// The light reads error until that SessionEnd ages out, so a
				// death of the resumed process makes no waiting -> error
				// flip: hand it to the idle-death watch (#2806) now.
				d.watchIdleExit(profile, id, string(StatusWaiting))
			}
			d.spendRestartToken(id)
			d.emitRestartBoot(inst, TransitionNotificationEvent{
				ChildSessionID: inst.ID, ChildTitle: inst.Title, Profile: profile,
				FromStatus: string(StatusRunning), ToStatus: to, Timestamp: now,
			}, tok, isSelfSuppressedConductor(inst))
			// An error banner is still reported until the boot timeout.
			rs := d.restarts()
			rs.mu.Lock()
			rs.banner[id] = restartBannerWatch{profile: profile, tok: tok, until: tok.At.Add(timeout)}
			rs.mu.Unlock()
			continue
		}
		if now.Sub(tok.At) <= timeout {
			continue
		}
		d.spendRestartToken(id)
		if tok.NewPID == 0 || tok.NewPID == restartPIDUnknown {
			// No new pid to go on (the restart never recorded one, or could
			// not read it): judge by status.
			switch normalizeStatusString(statuses[id]) {
			case string(StatusWaiting), string(StatusIdle), string(StatusRunning):
				continue
			}
		} else if restartPIDAlive(tok.NewPID) {
			switch normalizeStatusString(statuses[id]) {
			case string(StatusWaiting), string(StatusIdle):
				continue // back at its prompt
			case string(StatusRunning):
				if !restartTranscriptUnchanged(tok, inst.GetJSONLPath(), transitionEventOutputHash(inst)) {
					continue // back, and already on a new turn
				}
			}
		}
		if strings.TrimSpace(inst.ParentSessionID) == "" || isSelfSuppressedConductor(inst) {
			continue
		}
		event := TransitionNotificationEvent{
			ChildSessionID: inst.ID, ChildTitle: inst.Title, Profile: profile,
			FromStatus: string(StatusRunning), ToStatus: string(StatusError), Timestamp: now,
			Tier: TurnTierUrgent, Trigger: TurnTriggerSystem,
			LastOutputHash: "restart-timeout:" + tok.At.UTC().Format(time.RFC3339Nano),
			Text:           fmt.Sprintf("Restarted, but did not come back to its prompt within %s.", timeout),
		}
		if d.commitRestartRecord(inst, event).DeliveryResult == transitionDeliveryCommitted && !d.idleExitPaneAlive(inst) {
			// That record is the death's report; the light may still read
			// waiting and flip to error a pass later (#2806 would say it
			// again).
			d.markIdleExitTold(profile, id)
		}
	}
}

// restartResumeEcho reports whether an unclassified flip of inst to `to` at
// ts is the resumed process at the prompt the unseen-boot info line (#2813)
// already reported: the banner watch is live and the transcript gained no
// user or assistant record since the restart. An error, a banner, a
// permission or elicitation prompt, or anything after the watch expires is
// still news. An observed flip on a transcript the resumed process never
// wrote (it may be writing a new session file the instance does not resolve
// yet, #2184) is news too: noTurnSince cannot see that turn.
func (d *TransitionDaemon) restartResumeEcho(inst *Instance, to string, ts time.Time, observedFlip bool) bool {
	switch normalizeStatusString(to) {
	case string(StatusWaiting), string(StatusIdle):
	default:
		return false
	}
	rs := d.restarts()
	rs.mu.Lock()
	w, ok := rs.banner[inst.ID]
	rs.mu.Unlock()
	if !ok || ts.After(w.until) || ts.Before(w.tok.At) {
		return false
	}
	if w.tok.Transcript == "" || w.tok.Transcript != inst.GetJSONLPath() || !noTurnSince(w.tok.Transcript, w.tok.Offset) {
		return false
	}
	if observedFlip {
		info, err := os.Stat(w.tok.Transcript)
		if err != nil || info.ModTime().Before(w.tok.At) {
			return false
		}
	}
	// A permission request or elicitation dialog after the restart blocks the
	// child on input; its parent must hear it.
	if hs := restartHookStatus(inst.ID); hs != nil && hookEventBlocksTurn(hs.Event) && !hs.UpdatedAt.Before(w.tok.At.Truncate(time.Second)) {
		return false
	}
	sub := restartPaneSubstate(inst)
	return sub != string(tmux.SubstateInteractiveMenu) && paneErrorText(sub) == ""
}

// dropStaleBannerWatch forgets child id's banner watch once a newer restart
// left a token: the watch's own token was spent when the info line went out,
// so a token under another At is the next restart's, which judges its own
// boot (sweepRestartBanners does the same, but only on its own pass).
func (d *TransitionDaemon) dropStaleBannerWatch(id string) {
	rs := d.restarts()
	rs.mu.Lock()
	w, ok := rs.banner[id]
	rs.mu.Unlock()
	if !ok {
		return
	}
	if tok, live := readRestartToken(id); !live || tok.At.Equal(w.tok.At) {
		return
	}
	rs.mu.Lock()
	if cur, ok := rs.banner[id]; ok && cur.tok.At.Equal(w.tok.At) {
		delete(rs.banner, id)
	}
	rs.mu.Unlock()
}

// sweepRestartBanners reports an error banner on a resumed child whose token
// the unseen-boot info line spent, once, until the boot timeout (#2813). The
// info line is not sent again.
func (d *TransitionDaemon) sweepRestartBanners(profile string, byID map[string]*Instance, now time.Time) {
	rs := d.restarts()
	rs.mu.Lock()
	watches := make(map[string]restartBannerWatch, len(rs.banner))
	for id, w := range rs.banner {
		if w.profile == profile {
			watches[id] = w
		}
	}
	rs.mu.Unlock()
	for id, w := range watches {
		inst := byID[id]
		_, restarting := readRestartToken(id) // a new restart judges its own boot
		done := inst == nil || restarting || now.After(w.until)
		var banner string
		if !done {
			banner = paneErrorText(restartPaneSubstate(inst))
			done = banner != ""
		}
		if !done {
			continue
		}
		rs.mu.Lock()
		delete(rs.banner, id)
		rs.mu.Unlock()
		if banner == "" || strings.TrimSpace(inst.ParentSessionID) == "" || isSelfSuppressedConductor(inst) {
			continue
		}
		result := d.emitRestartError(inst, TransitionNotificationEvent{
			ChildSessionID: inst.ID, ChildTitle: inst.Title, Profile: profile,
			FromStatus: string(StatusRunning), ToStatus: string(StatusError), Timestamp: now,
		}, w.tok, "Restarted, but the resumed session shows "+banner+".")
		if result.DeliveryResult == transitionDeliveryCommitted {
			d.markIdleExitTold(profile, id) // its later death is the same failure
		}
	}
}
