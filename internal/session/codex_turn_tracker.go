package session

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Issue #2900: some Codex turn ends produced no running -> waiting record, so
// the parent was never woken. Codex has no Claude-style transcript, so the
// daemon identifies a Codex turn only by the hook writer's completion
// generation (codexTurnSignal). When that evidence does not advance for a
// real turn (the notify was dropped, written late, judged foreign under the
// wrong Codex home, or masked as consumed), the turn's record carried the
// PREVIOUS turn's signal (or none) and the notifier's dedup swallowed it: an
// identical signal is a duplicate for two hours (defaultOutputHashDedupTTL),
// an empty one for 90 seconds (shortWindowDedupSeconds).
//
// The daemon now keeps its own evidence of a Codex turn: every time it sees
// the child go running it opens a new run (epoch). A run that ends at a
// terminal status is decided exactly once:
//
//   - when the hook evidence is fresh, the record carries it as before;
//   - when it is stale (equal to the signal the previous decided turn
//     carried, or to the last notified one) or empty, the record carries a
//     signal minted from the run instead, so no dedup can mistake it for the
//     previous turn;
//   - a hook completion stamped within codexLateHookSlack of a run-signal
//     record for the same run is that turn's late evidence, absorbed instead
//     of a second record; one stamped later is a new turn;
//   - a run nobody decided (the snapshot edge never fired or was refused)
//     is emitted by emitCodexTurnFallbacks once the pane has sat at its
//     composer for codexTurnFallbackAfter.

const (
	// codexTurnFallbackAfter is how long a Codex child must have been off
	// running, at a terminal status, before the fallback emits its turn.
	codexTurnFallbackAfter = 8 * time.Second
	// codexLateHookSlack bounds how much later than a run-signal record a
	// hook completion may be stamped and still be that turn's late notify:
	// the notify writer's lock budget (3 s), one slow poll interval, and one
	// second for the hook file's whole-second timestamp. A completion stamped
	// later is a new turn, even when no poll saw it running (Codex's legacy
	// notify sends no turn start, and a fast follow-up can fit in one poll
	// gap). The same bound tells, after a daemon restart, whether the hook
	// file still holds the evidence of the turn last recorded under a run
	// signal.
	codexLateHookSlack = 3*time.Second + notifyPollSlow + time.Second
)

// codexTurnTrack is the daemon's per-child memory of observed Codex runs.
type codexTurnTrack struct {
	epoch       uint64    // runs observed: incremented on each rising running edge
	running     bool      // the last pass saw the child running
	runStart    time.Time // when the current run was first seen
	lastRunning time.Time // the last pass that saw the child running

	decidedEpoch uint64    // the run a terminal record was last decided for
	decidedHook  string    // the hook signal current at that decision
	decidedRun   bool      // that decision carried a run signal (stale hook)
	decidedAt    time.Time // when it was decided
	absorbedHook string    // a late hook completion absorbed into that run
}

func (d *TransitionDaemon) codexTrack(profile, id string, create bool) *codexTurnTrack {
	if d.codexTurns == nil {
		if !create {
			return nil
		}
		d.codexTurns = map[string]map[string]*codexTurnTrack{}
	}
	byID := d.codexTurns[profile]
	if byID == nil {
		if !create {
			return nil
		}
		byID = map[string]*codexTurnTrack{}
		d.codexTurns[profile] = byID
	}
	tr := byID[id]
	if tr == nil && create {
		tr = &codexTurnTrack{}
		byID[id] = tr
	}
	return tr
}

// observeCodexRuns opens a run for every Codex child this pass sees running
// and forgets children that left the profile. Runs before any emission path
// of the pass, so a turn end seen in the same pass belongs to the run.
func (d *TransitionDaemon) observeCodexRuns(profile string, byID map[string]*Instance, statuses map[string]string, now time.Time) {
	for id, status := range statuses {
		inst := byID[id]
		if inst == nil || !IsCodexCompatible(inst.Tool) {
			continue
		}
		tr := d.codexTrack(profile, id, true)
		if normalizeStatusString(status) == string(StatusRunning) {
			if !tr.running {
				tr.epoch++
				tr.runStart = now
			}
			tr.running = true
			tr.lastRunning = now
			continue
		}
		tr.running = false
	}
	if d.codexTurns != nil {
		for id := range d.codexTurns[profile] {
			if _, ok := statuses[id]; !ok {
				delete(d.codexTurns[profile], id)
			}
		}
	}
}

// codexRunSignal is the turn signal minted for a run whose hook evidence is
// stale or missing: unique per child and per observed run.
func codexRunSignal(childID string, runStart time.Time) string {
	return fmt.Sprintf("codex-run:%s:%d", childID, runStart.UnixNano())
}

// codexTurnGate applies the run rules above to a Codex record about to go to
// the notifier. absorb reports a late hook completion of a run already
// decided under a run signal; the caller drops it. Otherwise event's signal
// may be replaced by a run signal, and commit must be called with the
// notifier's result.
func (d *TransitionDaemon) codexTurnGate(profile string, inst *Instance, event *TransitionNotificationEvent, now time.Time) (absorb bool, commit func(TransitionNotificationEvent)) {
	noop := func(TransitionNotificationEvent) {}
	if inst == nil || !IsCodexCompatible(inst.Tool) || !isRecordableTurnStatus(event.ToStatus) {
		return false, noop
	}
	hook := strings.TrimSpace(event.LastOutputHash)
	tr := d.codexTrack(profile, inst.ID, false)
	if tr == nil || tr.epoch == 0 {
		// No run observed since this daemon started. After a restart the
		// last record may carry a run signal no one can rebuild; the hook
		// evidence it covered is not a new turn.
		if hook != "" && d.codexRunRecordCovers(inst, event.ToStatus) {
			return true, noop
		}
		return false, noop // as before
	}
	if tr.decidedEpoch == tr.epoch {
		if !tr.decidedRun {
			return false, noop // same run, decided on its hook signal: the notifier's dedup applies
		}
		switch {
		case hook == "" || hook == tr.decidedHook:
			// The same stale evidence again (a re-scan, a waiting -> idle
			// flip): give it the run's signal so the notifier collapses it.
			event.LastOutputHash = codexRunSignal(inst.ID, tr.runStart)
			return false, noop
		case hook == tr.absorbedHook:
			return true, noop // the late hook already absorbed, re-observed
		case !codexHookStampedAfter(inst.ID, tr.decidedAt.Add(codexLateHookSlack)):
			tr.absorbedHook = hook
			sessionLog.Debug("codex_turn_late_hook_absorbed",
				slog.String("instance", inst.ID), slog.Uint64("run", tr.epoch))
			return true, noop
		}
		// A completion stamped after the decided run could have reported is
		// a new turn that ran between two polls: open a run for it.
		tr.epoch++
		tr.runStart = now
		sessionLog.Debug("codex_turn_unobserved_run",
			slog.String("instance", inst.ID), slog.Uint64("run", tr.epoch))
	}
	if tr.running {
		// The pane still shows the run working. A hook candidate now is
		// either this run's notify landing first (a new signal: recorded,
		// and the run is decided) or the previous turn's, still fresh for
		// 45 s (its old signal: the notifier collapses it, and the run stays
		// open). Never mint a run signal for a run the pane has not ended.
		// The previous run's late hook, absorbed there, is its evidence too:
		// the notifier's record carries that run's minted signal, so only
		// this check can tell.
		if hook != "" && (hook == tr.absorbedHook || hook == tr.decidedHook) {
			return true, noop
		}
		epoch := tr.epoch
		return false, func(result TransitionNotificationEvent) {
			if result.DeliveryResult == transitionDeliveryCommitted {
				tr.decidedEpoch, tr.decidedHook, tr.decidedRun, tr.decidedAt = epoch, hook, false, now
				tr.absorbedHook = ""
			}
		}
	}
	stale := hook == "" || hook == tr.decidedHook || hook == tr.absorbedHook
	if !stale && d.notifier != nil {
		// The last record carrying this signal means the hook did not move.
		// A last record carrying NO signal makes the notifier's 90-second
		// window collapse anything after it, a fresh hook signal included
		// (found by the fake-Codex e2e: a turn ending within 90 s of the
		// child's first sighting was dropped). Either way the run's own
		// signal is the only identity that cannot collide.
		if _, last, ok := d.notifier.lastNotifiedTurn(inst.ID); ok && (last == hook || strings.TrimSpace(last) == "") {
			stale = true
		}
	}
	if stale {
		event.LastOutputHash = codexRunSignal(inst.ID, tr.runStart)
	}
	epoch := tr.epoch
	return false, func(result TransitionNotificationEvent) {
		if result.DeliveryResult == transitionDeliveryFailed {
			return // transient: the next poll or the fallback retries
		}
		tr.decidedEpoch, tr.decidedHook, tr.decidedRun, tr.decidedAt = epoch, hook, stale, now
		tr.absorbedHook = ""
	}
}

// noteCodexTurnDecided marks the current run of a Codex child decided by an
// emitTurn outcome that never reached the notifier (a restart judgement, a
// self-conductor drop): it is not a lost turn for the fallback to replay.
func (d *TransitionDaemon) noteCodexTurnDecided(profile string, inst *Instance, to string, now time.Time) {
	if inst == nil || !IsCodexCompatible(inst.Tool) || !isRecordableTurnStatus(to) {
		return
	}
	// A run the pane still shows working is not ended by anything emitTurn
	// said about it (codexTurnGate decides a running run only on a record).
	if tr := d.codexTrack(profile, inst.ID, false); tr != nil && !tr.running && tr.decidedEpoch != tr.epoch {
		tr.decidedEpoch, tr.decidedAt = tr.epoch, now
		tr.decidedRun, tr.decidedHook, tr.absorbedHook = false, "", ""
	}
}

// emitCodexTurnFallbacks emits the turn of every Codex child whose observed
// run ended without a decided record: off running for codexTurnFallbackAfter,
// at a terminal status, with its pane idle at the composer. emitTurn's gate
// gives the record a run signal when the hook evidence is stale, and marks the
// run decided, so it fires once per run and never next to the normal path.
func (d *TransitionDaemon) emitCodexTurnFallbacks(profile string, byID map[string]*Instance, statuses map[string]string, notifyEnabled bool, now time.Time) {
	if !notifyEnabled || d.codexTurns == nil {
		return
	}
	after := d.codexFallbackAfter
	if after <= 0 {
		after = codexTurnFallbackAfter
	}
	for id, tr := range d.codexTurns[profile] {
		if tr.epoch == 0 || tr.decidedEpoch == tr.epoch || tr.running ||
			now.Sub(tr.lastRunning) < after {
			continue
		}
		to := normalizeStatusString(statuses[id])
		inst := byID[id]
		if inst == nil || !isRecordableTurnStatus(to) || !instanceAcceptsTransitionEvents(inst) {
			continue
		}
		if CompletionRecordExists(profile, id) {
			continue // the completion wrapper owns a task worker's terminal signal
		}
		if d.turnLiveCheck != nil && !d.turnLiveCheck(inst) {
			continue
		}
		probe := d.codexComposerIdle
		if probe == nil {
			probe = codexPaneAtComposer
		}
		if !probe(inst) {
			continue
		}
		sessionLog.Info("codex_turn_end_fallback",
			slog.String("instance", id), slog.String("status", to), slog.Uint64("run", tr.epoch))
		_, _ = d.emitTurn(profile, inst, byID, string(StatusRunning), to, now, true)
	}
}

// codexPaneAtComposer reports whether a Codex pane sits idle at its composer:
// the prompt marker is drawn, no turn is working and no menu is open.
func codexPaneAtComposer(inst *Instance) bool {
	s := inst.GetTmuxSession()
	if s == nil {
		return false
	}
	raw, err := s.CapturePane()
	if err != nil {
		return false
	}
	return codexContentAtComposer(inst.Tool, tmux.StripANSI(raw))
}

func codexContentAtComposer(tool, content string) bool {
	det := tmux.NewPromptDetector(PromptDetectorTool(tool))
	if !det.HasPrompt(content) {
		return false
	}
	switch det.ClassifySubstate(content) {
	case tmux.SubstateRunning, tmux.SubstateInteractiveMenu:
		return false
	}
	return true
}

// codexHookStampedAfter reports whether the child's hook status file holds a
// completion stamped after t. No readable file reads as not after.
func codexHookStampedAfter(instanceID string, t time.Time) bool {
	hs := readHookStatusForPass(instanceID)
	return hs != nil && !hs.UpdatedAt.IsZero() && hs.UpdatedAt.After(t)
}

// codexRunRecordCovers reports whether the child's last record carries a
// Codex run signal for status to and the hook file holds nothing newer than
// that record could have absorbed. The run signal lives only in the daemon
// that minted it, so after a restart nothing can rebuild it and every
// comparison against the hook signal fails; without this, a recycled daemon
// woke the parent again for a turn it had already handled (issue #2900
// review). A hook completion stamped later is a turn that ended while the
// daemon was down, and is still recorded.
func (d *TransitionDaemon) codexRunRecordCovers(inst *Instance, to string) bool {
	if d.notifier == nil || inst == nil || !IsCodexCompatible(inst.Tool) {
		return false
	}
	lastTo, lastHash, at, ok := d.notifier.lastNotifiedRecord(inst.ID)
	if !ok || !strings.HasPrefix(lastHash, "codex-run:") ||
		attentionClass(lastTo) != attentionClass(normalizeStatusString(to)) {
		return false
	}
	return !codexHookStampedAfter(inst.ID, at.Add(codexLateHookSlack))
}
