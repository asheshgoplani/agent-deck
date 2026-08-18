package session

import (
	"testing"
	"time"
)

// backgroundSweepInterval mirrors the TUI's backgroundStatusUpdate cadence.
// Every count in these tests is "per minute of that sweep".
const backgroundSweepInterval = 2 * time.Second

// countFullProbes drives a settled gate over `window` of sweeps and returns how
// many times it demanded the full (subprocess-spawning) path. activityAt/ackAt
// let a test inject a real signal at a chosen tick.
func countFullProbes(window time.Duration, signal func(tick int) (int64, bool)) int {
	var g settledPollState
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	full := 0
	for tick := 0; time.Duration(tick)*backgroundSweepInterval < window; tick++ {
		activity, acked := signal(tick)
		if !g.shouldSkip(activity, acked, now) {
			full++
		}
		now = now.Add(backgroundSweepInterval)
	}
	return full
}

// A settled session — idle or waiting, no new pane output, acknowledgment
// unchanged — used to pay the full UpdateStatus tail on every 2s sweep: a
// CapturePane classification plus a session-metadata sync (tmux env read +
// transcript tail read), measured together at 91% of UpdateStatus time and
// 190-570ms per session on a live profile. Thirty of those per minute, forever,
// per settled session, is the bug.
func TestSettledPoll_SettledSessionStopsProbingEveryTick(t *testing.T) {
	const window = 5 * time.Minute
	unsweptTicks := int(window / backgroundSweepInterval)

	full := countFullProbes(window, func(int) (int64, bool) { return 1000, false })

	if full >= unsweptTicks {
		t.Fatalf("settled session took the full path %d times in %v — no better than the %d ticks",
			full, window, unsweptTicks)
	}
	// 10s, 20s, 40s, then once a minute: 8 probes in 5 minutes.
	if full > 10 {
		t.Fatalf("settled session took the full path %d times in %v, want <= 10 (was %d)",
			full, window, unsweptTicks)
	}
	t.Logf("full probes over %v: before=%d after=%d (%.0fx fewer)",
		window, unsweptTicks, full, float64(unsweptTicks)/float64(full))
}

// The gate must be signal-driven, not a timer: new pane output has to open it on
// the very next call, or an active session's status goes stale.
func TestSettledPoll_NewOutputOpensTheGateImmediately(t *testing.T) {
	var g settledPollState
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if g.shouldSkip(1000, false, now) {
		t.Fatal("the first call must always take the full path")
	}
	now = now.Add(backgroundSweepInterval)
	if !g.shouldSkip(1000, false, now) {
		t.Fatal("an unchanged settled session must skip")
	}
	now = now.Add(backgroundSweepInterval)
	if g.shouldSkip(1001, false, now) {
		t.Fatal("new window activity must force the full path on the very next call")
	}
}

// Attaching to a session flips its acknowledged flag, which is what turns
// waiting (orange) into idle (grey). That transition must not wait out the
// escalated interval.
func TestSettledPoll_AcknowledgmentChangeOpensTheGate(t *testing.T) {
	var g settledPollState
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	g.shouldSkip(1000, false, now)
	now = now.Add(time.Minute) // long settled: the interval has escalated
	g.shouldSkip(1000, false, now)
	now = now.Add(backgroundSweepInterval)
	if !g.shouldSkip(1000, false, now) {
		t.Fatal("still-settled session should skip")
	}
	now = now.Add(backgroundSweepInterval)
	if g.shouldSkip(1000, true, now) {
		t.Fatal("an acknowledgment flip must force the full path on the very next call")
	}
}

// A session that keeps producing output must never be throttled at all.
func TestSettledPoll_ActiveSessionIsNeverThrottled(t *testing.T) {
	const window = time.Minute
	ticks := int(window / backgroundSweepInterval)
	full := countFullProbes(window, func(tick int) (int64, bool) { return int64(1000 + tick), false })
	if full != ticks {
		t.Fatalf("active session took the full path %d/%d times; an active session must never be throttled", full, ticks)
	}
}

func TestSettledPollInterval_Escalation(t *testing.T) {
	want := []time.Duration{10, 20, 40, 60, 60}
	for i, w := range want {
		if got := settledPollInterval(i); got != w*time.Second {
			t.Errorf("settledPollInterval(%d) = %v, want %v", i, got, w*time.Second)
		}
	}
}

// reset must put a session back on the fast cadence — a session that leaves the
// settled states and comes back must not inherit a 60s interval.
func TestSettledPoll_ResetRestoresTheFastCadence(t *testing.T) {
	var g settledPollState
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		g.shouldSkip(1000, false, now)
		now = now.Add(settledPollMaxInterval)
	}
	g.reset()
	if g.shouldSkip(1000, false, now) {
		t.Fatal("a reset gate must take the full path on its next call")
	}
	if g.skipStreak != 0 {
		t.Fatalf("skipStreak = %d after reset, want 0", g.skipStreak)
	}
}

// ---- session-metadata sync cadence (the 78% phase) ----

// The sync ran unconditionally every 2s — exactly the sweep period — so every
// running/waiting session paid a tmux env read plus a transcript tail read on
// every single tick. Measured at 2195ms of 2826ms total across 43 live sessions.
func TestSessionMetaSyncDue_SettledSessionSyncsOnTheIdleFloor(t *testing.T) {
	const window = time.Minute
	ticks := int(window / backgroundSweepInterval)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	last := base
	syncs := 0
	for tick := 1; tick <= ticks; tick++ {
		now := base.Add(time.Duration(tick) * backgroundSweepInterval)
		if sessionMetaSyncDue(last, now, 2*time.Second, false, 1000, 1000) {
			syncs++
			last = now
		}
	}
	if syncs > 2 {
		t.Fatalf("settled session synced metadata %d times in %v, want <= 2 (was %d)", syncs, window, ticks)
	}
	t.Logf("meta syncs over %v: before=%d after=%d", window, ticks, syncs)
}

// New pane output means the tmux environment or the transcript tail may have
// moved, so the sync must run at the normal 2s cadence.
func TestSessionMetaSyncDue_NewActivitySyncsAtFullCadence(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !sessionMetaSyncDue(base, base.Add(2*time.Second), 2*time.Second, false, 1001, 1000) {
		t.Fatal("new window activity must sync at the normal interval")
	}
	if sessionMetaSyncDue(base, base.Add(time.Second), 2*time.Second, false, 1001, 1000) {
		t.Fatal("the interval floor must still apply inside 2s")
	}
}

// A session still resolving its tool session id has no activity to correlate
// against, so it must keep the 500ms bootstrap cadence.
func TestSessionMetaSyncDue_BootstrapIgnoresTheActivityGate(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !sessionMetaSyncDue(base, base.Add(500*time.Millisecond), 500*time.Millisecond, true, 1000, 1000) {
		t.Fatal("a bootstrapping session must sync on its interval even with no new activity")
	}
}

// A zero activity sample means the pane-activity cache had no entry. That is
// "unknown", not "nothing happened", and must not throttle the sync.
func TestSessionMetaSyncDue_MissingActivityEvidenceDoesNotThrottle(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !sessionMetaSyncDue(base, base.Add(2*time.Second), 2*time.Second, false, 0, 0) {
		t.Fatal("a cold activity cache must fall back to the interval-only cadence")
	}
}

// ---- dead-session recheck (defect 4) ----

// Eleven sessions absent from tmux each paid a 50-164ms full probe on EVERY 2s
// sweep for the whole 50-minute observation window. A confirmed-dead session
// must reach a cheap steady state instead.
func TestDeadRecheckDelay_ConfirmedDeadSessionBacksOff(t *testing.T) {
	const window = time.Hour
	sweeps := int(window / backgroundSweepInterval)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	last := base
	streak := 0
	probes := 0
	for tick := 1; tick <= sweeps; tick++ {
		now := base.Add(time.Duration(tick) * backgroundSweepInterval)
		if now.Sub(last) < deadRecheckDelay(streak) {
			continue
		}
		probes++
		streak++
		last = now
	}
	// 30s + 1m + 2m + 4m then 5m each: ~13 probes in an hour.
	if probes > 15 {
		t.Fatalf("dead session probed %d times in %v, want <= 15", probes, window)
	}
	unfixed := int(window / errorRecheckInterval)
	t.Logf("full probes over %v: every-sweep=%d flat-30s=%d escalating=%d", window, sweeps, unfixed, probes)
}

func TestDeadRecheckDelay_Escalation(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		if got := deadRecheckDelay(i); got != w {
			t.Errorf("deadRecheckDelay(%d) = %v, want %v", i, got, w)
		}
	}
	if deadRecheckDelay(0) != errorRecheckInterval {
		t.Fatalf("deadRecheckDelay(0) = %v, want the historical errorRecheckInterval %v",
			deadRecheckDelay(0), errorRecheckInterval)
	}
}

// The archived+error bypass exists so live tmux state can normalize an archived
// error to stopped. That reconciliation is owed once per archive event — before
// this, it re-armed on every single sweep and defeated the recheck cache
// entirely for every archived+error session.
func TestArchivedErrorNormalization_IsOwedOncePerArchiveEvent(t *testing.T) {
	archivedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	i := &Instance{Status: StatusError, ArchivedAt: archivedAt}

	if !i.archivedErrorNormalizationDueLocked() {
		t.Fatal("a freshly archived error session must be due for normalization")
	}
	// The probe runs and stamps the archive it reconciled.
	i.archivedErrorNormalizedAt = i.ArchivedAt
	if i.archivedErrorNormalizationDueLocked() {
		t.Fatal("normalization must not be owed again for the same archive event")
	}
	// Unarchive then re-archive: a new event, owed once more.
	i.ArchivedAt = time.Time{}
	if i.archivedErrorNormalizationDueLocked() {
		t.Fatal("an unarchived session is never owed archived-error normalization")
	}
	i.ArchivedAt = archivedAt.Add(time.Hour)
	if !i.archivedErrorNormalizationDueLocked() {
		t.Fatal("re-archiving must re-arm the one-time normalization")
	}
}

// A non-archived error session must never trip the archived bypass, or it would
// full-probe on every sweep.
func TestArchivedErrorNormalization_NotDueForActiveSessions(t *testing.T) {
	i := &Instance{Status: StatusError}
	if i.archivedErrorNormalizationDueLocked() {
		t.Fatal("a non-archived error session must not be due for archived normalization")
	}
	i.ArchivedAt = time.Now()
	i.Status = StatusStopped
	if i.archivedErrorNormalizationDueLocked() {
		t.Fatal("an archived STOPPED session is already normalized; it must not bypass the cache")
	}
}
