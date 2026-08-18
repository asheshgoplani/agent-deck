package session

import "time"

const (
	// settledPollBaseInterval is how long a settled session (idle or waiting,
	// no new pane output, acknowledgment unchanged) may skip its full tmux
	// probe before the first mandatory refresh. It matches the interval the
	// idle tier has always used, so a settled session is never staler than an
	// idle one already was.
	settledPollBaseInterval = 10 * time.Second

	// settledPollMaxInterval caps the escalation. A session that has stayed
	// settled for minutes is refreshed once a minute rather than 30 times.
	settledPollMaxInterval = 60 * time.Second

	// sessionMetaSyncIdleFloor is how long a session with no new pane output
	// may go without a session-metadata sync (tmux env read + transcript tail
	// read). The old unconditional 2s cadence matched the background sweep
	// period exactly, so every running/waiting session paid that cost on every
	// single tick — measured at 77.7% of all UpdateStatus time across a live
	// 43-session profile, 190-570ms for the worst sessions.
	sessionMetaSyncIdleFloor = 30 * time.Second
)

// settledPollState is the per-instance memory behind the settled-session poll
// gate. It is deliberately signal-driven rather than a blind timer: the two
// inputs that can change a settled session's status — new pane output (tmux
// window activity advances) and the user attaching (the acknowledged flag
// flips) — force the next call down the full path immediately. The escalating
// interval only governs how often a session with NO such signal is re-probed
// anyway as a safety net.
type settledPollState struct {
	lastFull   time.Time
	activity   int64
	acked      bool
	skipStreak int
	primed     bool
}

// settledPollInterval is the safety-net refresh interval after n consecutive
// skips: 10s, 20s, 40s, capped at 60s.
func settledPollInterval(skipStreak int) time.Duration {
	d := settledPollBaseInterval
	for i := 0; i < skipStreak; i++ {
		if d >= settledPollMaxInterval {
			break
		}
		d *= 2
	}
	if d > settledPollMaxInterval {
		d = settledPollMaxInterval
	}
	return d
}

// shouldSkip reports whether the full tmux probe can be skipped this call.
// A false result means the caller is taking the full path, and the sample the
// next decision compares against is recorded here.
//
// The escalation advances once per safety-net probe, not once per skip: a
// session that stays settled is re-probed after 10s, then 20s, then 40s, then
// once a minute. Any real signal resets it to 10s.
func (s *settledPollState) shouldSkip(activity int64, acked bool, now time.Time) bool {
	changed := !s.primed || activity != s.activity || acked != s.acked
	if !changed && now.Sub(s.lastFull) < settledPollInterval(s.skipStreak) {
		return true
	}
	if changed {
		s.skipStreak = 0
	} else {
		// The safety net fired and found nothing new — back off further.
		s.skipStreak++
	}
	s.primed = true
	s.lastFull = now
	s.activity = activity
	s.acked = acked
	return false
}

// reset clears the gate so the next call takes the full path. Called whenever
// something outside the settled path changed the session's state.
func (s *settledPollState) reset() {
	s.primed = false
	s.skipStreak = 0
}

// deadRecheckIntervalMax caps the escalating recheck for a session whose tmux
// session has been confirmed gone. Observed before this: eleven sessions absent
// from tmux each paid a 50-164ms full probe on EVERY 2s sweep, continuously,
// across a 50-minute window.
const deadRecheckIntervalMax = 5 * time.Minute

// deadRecheckDelay is how long to wait before re-probing a session confirmed
// gone `streak` times in a row: 30s, 1m, 2m, 4m, capped at 5m. streak 0 keeps
// the historical errorRecheckInterval, so a freshly dead session is unaffected.
func deadRecheckDelay(streak int) time.Duration {
	d := errorRecheckInterval
	for i := 0; i < streak; i++ {
		if d >= deadRecheckIntervalMax {
			break
		}
		d *= 2
	}
	if d > deadRecheckIntervalMax {
		d = deadRecheckIntervalMax
	}
	return d
}

// archivedErrorNormalizationDueLocked reports whether this session still owes
// the one-time archived+error reconciliation that bypasses the recheck cache.
// It is due once per archive event: archiving stamps a new ArchivedAt, which no
// longer matches the stamp the last reconciliation ran against.
func (i *Instance) archivedErrorNormalizationDueLocked() bool {
	return i.IsArchived() && i.Status == StatusError &&
		!i.archivedErrorNormalizedAt.Equal(i.ArchivedAt)
}

// sessionMetaSyncDue is the pure cadence decision for the session-metadata sync
// (tmux env read + transcript tail read). Separated from the instance so the
// cadence is unit-testable.
//
// A zero activity sample means the pane-activity cache had no entry, which is
// "unknown", not "nothing happened" — those fall through to the old
// interval-only behaviour rather than being throttled on missing evidence.
func sessionMetaSyncDue(last, now time.Time, interval time.Duration, bootstrap bool, activity, lastActivity int64) bool {
	if last.IsZero() {
		return true
	}
	since := now.Sub(last)
	if since < interval {
		return false
	}
	if bootstrap || activity == 0 || activity != lastActivity {
		return true
	}
	// Settled: the pane has produced nothing since the last sync, so neither the
	// tmux environment nor the transcript tail can have moved. Re-read on a slow
	// floor purely as a safety net.
	return since >= sessionMetaSyncIdleFloor
}

// sessionMetaSyncDueLocked applies sessionMetaSyncDue and, when it says yes,
// records the sample the next decision compares against. Caller must hold i.mu.
func (i *Instance) sessionMetaSyncDueLocked(interval time.Duration, bootstrap bool, now time.Time) bool {
	var activity int64
	if i.tmuxSession != nil {
		activity = i.tmuxSession.GetCachedWindowActivity()
	}
	if !sessionMetaSyncDue(i.lastSessionMetaSync, now, interval, bootstrap, activity, i.lastMetaSyncActivity) {
		return false
	}
	i.lastSessionMetaSync = now
	i.lastMetaSyncActivity = activity
	return true
}
