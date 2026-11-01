package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// A fresh Claude start (`launch`, `session start`) reaches its empty prompt
// before its kickoff is typed. With no transcript yet, that edge took
// emitTurn's legacy branch and reached the parent as urgent: its Stop hook
// blocked on "<child>: idle" and a conductor woke for nothing (#2954). It is
// the twin of a restart's boot (#2792, restart_resume.go).
//
// The start leaves a token in runtime/fresh-start/<id>.json, written just
// before the spawn and removed if the spawn fails, for a command that starts
// a new conversation in the claude binary itself, whose turns this host can
// see: local, unwrapped, hooks on. Any other start removes a stale one. A
// resuming restart leaves none (its boot is the restart token's); RestartFresh
// starts a new conversation through Start and gets one. The daemon then
// turns the child's arrival at its empty prompt with no turn since the start
// (no user or assistant record in the transcript, no hook event but
// SessionStart or SessionEnd) into one info line. The line has its own
// identity in the inbox, so a second sighting, on another path or after a
// daemon restart, folds into it. A pane that shows nothing recognisable yet,
// or a prompt reached before the transcript path or a hook event since the
// start shows that turns would be seen, is judged again next poll, until the
// boot timeout. A menu, a login or model error is delivered as before. An
// error or a turn spends the token silently, and nothing else does before
// restartTokenOrphanAge: a child launched with no kickoff and acknowledged
// later (waiting -> idle at the same prompt) is still at its boot. That long
// life is why the token needs Claude's hooks installed in its config dir:
// with them every turn shows in the hook file's event history. Without that
// history the token ends at the boot timeout, as on main.

// freshStartDir holds the tokens under the runtime data dir.
const freshStartDir = "fresh-start"

type freshStartToken struct {
	At time.Time `json:"at"`
	// SessionID is the conversation the start runs (freshStartTurnSeen).
	SessionID string `json:"session_id,omitempty"`
}

// freshStartObservable reports whether claudeCmd runs the claude binary itself
// (bare or a path), with no alias (#1822 F3, e.g. cdw) or inline env before
// it, which may pick a config dir the transcript lookup and hooks do not see.
func freshStartObservable(claudeCmd string) bool {
	fields := strings.Fields(claudeCmd)
	return len(fields) > 0 && !isShellEnvAssignment(fields[0]) && isClaudeCommand(claudeCmd)
}

// noteFreshStart writes i's token before the spawn when the command just built
// starts a new conversation (launchesFresh, consumed here) whose turns this
// host sees: not over SSH, in a sandbox or behind a wrapper, with Claude's
// hooks on and installed, without drift, in the child's config dir. A config
// that does not load writes none. Otherwise it removes a stale one.
func noteFreshStart(i *Instance, now time.Time) {
	fresh := i.launchesFresh.Swap(false) && IsClaudeCompatible(i.Tool) && !i.notLocal() && !i.hasEffectiveWrapper()
	clearFreshStartToken(i.ID)
	if !fresh {
		return
	}
	if cfg, err := LoadUserConfig(); err != nil || (cfg != nil && !cfg.Claude.GetHooksEnabled()) {
		return
	}
	// The dir the spawn runs under: the ambient one when the builder left
	// CLAUDE_CONFIG_DIR to the command (#1822 F3), else what it exported.
	dir, _ := SpawnClaudeConfigDirForInstance(i)
	if i.configDirLeftToCommand {
		if dir = AmbientClaudeConfigDir(); dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".claude")
		}
	}
	if CheckClaudeHooksInstalled(dir) {
		writeTokenFile(freshStartDir, i.ID, ".json", freshStartToken{At: now, SessionID: i.ClaudeSessionID})
	}
}

func readFreshStartToken(id string) (freshStartToken, bool) {
	var tok freshStartToken
	path, err := runtimeTokenPath(freshStartDir, id, ".json")
	if err != nil {
		return tok, false
	}
	data, err := os.ReadFile(path)
	return tok, err == nil && json.Unmarshal(data, &tok) == nil && !tok.At.IsZero()
}

func clearFreshStartToken(id string) {
	if path, err := runtimeTokenPath(freshStartDir, id, ".json"); err == nil {
		_ = os.Remove(path)
	}
}

// spendFreshStartToken removes tok, and only tok: a start that wrote a newer
// token since it was read keeps its own.
func spendFreshStartToken(id string, tok freshStartToken) {
	if cur, ok := readFreshStartToken(id); ok && cur.At.Equal(tok.At) {
		clearFreshStartToken(id)
	}
}

// hookEventSince reports whether the hook file holds an event stamped at or
// after the start (whole seconds, the file's resolution, when it carries no
// ReceivedAt).
func hookEventSince(hs *HookStatus, tok freshStartToken) bool {
	if hs == nil || strings.TrimSpace(hs.Event) == "" {
		return false
	}
	if !hs.ReceivedAt.IsZero() {
		return !hs.ReceivedAt.Before(tok.At)
	}
	return !hs.UpdatedAt.Before(tok.At.Truncate(time.Second))
}

// freshStartTurnSeen reports a turn since the start: a user or assistant
// record in the transcript, or a hook event since the start other than the
// process's own SessionStart and SessionEnd, in the hook file or its history.
// The hook file keeps only the last event, so a turn then a /clear (or a
// claude -p that inherited the instance id) leaves just a SessionStart and a
// new, empty transcript: an instance saved on another conversation than the
// start's counts too, as does a SessionStart since the start of another
// conversation, or a second one in the history. An id the row does not have
// yet is no other conversation. With no history since the start (off, or the
// hooks never fired) turns may go unseen: past the boot timeout it counts as
// one, so the token is spent as on main.
func freshStartTurnSeen(inst *Instance, tok freshStartToken) bool {
	if tok.SessionID != "" && inst.ClaudeSessionID != "" && inst.ClaudeSessionID != tok.SessionID {
		return true
	}
	if path := inst.GetJSONLPath(); path != "" {
		if _, err := os.Stat(path); err == nil && !noTurnSince(path, 0) {
			return true
		}
	}
	if hs := restartHookStatus(inst.ID); hookEventSince(hs, tok) && !hookSessionEnd(hs) {
		if !strings.EqualFold(strings.TrimSpace(hs.Event), "SessionStart") || (tok.SessionID != "" && hs.SessionID != "" && hs.SessionID != tok.SessionID) {
			return true
		}
	}
	turn, seen := hookHistoryTurnSince(inst.ID, tok.At)
	return turn || (!seen && time.Since(tok.At) > restartBootTimeout())
}

// freshStartVerdict is what a flip is against the child's fresh-start token.
type freshStartVerdict int

const (
	freshStartNews   freshStartVerdict = iota // deliver as before
	freshStartBoot                            // at its empty prompt with no turn: the info line
	freshStartNotYet                          // not provably the boot yet: judged again next poll
)

// judgeFreshStart decides a flip of inst to `to` at ts against its token.
// substate is what the pass cached for the pane; with nothing cached (the TUI
// owns the statuses, or the pane read recognised nothing) the pane is read.
// SessionStart reads as waiting before Claude draws its input box, so a pane
// that still shows nothing is not yet the boot. Nor is the empty prompt until
// a turn would be seen right now: the transcript path resolves, or the hook
// file holds an event since the start (SessionStart, written before the input
// box draws; a SessionEnd may be an older process's). Otherwise a kickoff run
// where neither reaches would come back to the prompt looking like the boot.
// The hook event history's SessionStart since the start is proof too: the old
// process's SessionEnd can land over it in the hook file. The boot timeout
// bounds both waits: past it the empty prompt or an unrecognised pane spends
// the token and is news, so a later prompt adds no line after it.
func judgeFreshStart(inst *Instance, to string, ts time.Time, substate string) (freshStartToken, freshStartVerdict) {
	tok, ok := readFreshStartToken(inst.ID)
	if !ok || ts.Before(tok.At.Truncate(time.Second)) {
		return tok, freshStartNews
	}
	st := normalizeStatusString(to)
	if st == string(StatusError) || freshStartTurnSeen(inst, tok) {
		spendFreshStartToken(inst.ID, tok)
		return tok, freshStartNews
	}
	if st != string(StatusWaiting) && st != string(StatusIdle) {
		return tok, freshStartNews
	}
	if substate == string(tmux.SubstateNone) {
		substate = restartPaneSubstate(inst)
	}
	expired := time.Since(tok.At) > restartBootTimeout()
	switch tmux.Substate(substate) {
	case tmux.SubstateIdleAtEmptyPrompt:
		if hs := restartHookStatus(inst.ID); inst.GetJSONLPath() != "" || (hookEventSince(hs, tok) && !hookSessionEnd(hs)) || restartSessionStartSince(inst.ID, tok.At) {
			return tok, freshStartBoot
		}
		if expired {
			spendFreshStartToken(inst.ID, tok)
			return tok, freshStartNews
		}
		return tok, freshStartNotYet
	case tmux.SubstateNone:
		if expired {
			spendFreshStartToken(inst.ID, tok)
			return tok, freshStartNews
		}
		return tok, freshStartNotYet
	}
	return tok, freshStartNews
}

// emitFreshStartBoot is emitTurn's result for a fresh child at its prompt: one
// info line for a parented child. Like the restart's line it goes past the
// notifier's dedup state, under its own identity in the inbox: the same line
// committed again, pending or consumed, is not written twice.
func (d *TransitionDaemon) emitFreshStartBoot(inst *Instance, event TransitionNotificationEvent, tok freshStartToken, selfConductor bool) TransitionNotificationEvent {
	event.DeliveryResult = transitionDeliveryDropped
	if selfConductor || strings.TrimSpace(inst.ParentSessionID) == "" {
		return event
	}
	event.Tier = TurnTierInfo
	event.Trigger = TurnTriggerSystem
	event.Text = "Started; at its prompt, no turn yet."
	event.LastOutputHash = "fresh-start:" + tok.At.UTC().Format(time.RFC3339Nano)
	return d.commitRestartRecord(inst, event)
}

// sweepFreshStartTokens spends tokens silently: on a turn, and past
// restartTokenOrphanAge, whether or not a profile still has the session.
func (d *TransitionDaemon) sweepFreshStartTokens(byID map[string]*Instance, now time.Time) {
	dir, err := runtimeDataPath(freshStartDir)
	if err != nil {
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		tok, ok := readFreshStartToken(id)
		inst := byID[id]
		switch {
		case !ok:
			clearFreshStartToken(id) // unreadable: nothing to judge by
		case now.Sub(tok.At) > restartTokenOrphanAge, inst != nil && freshStartTurnSeen(inst, tok):
			spendFreshStartToken(id, tok)
		}
	}
}
