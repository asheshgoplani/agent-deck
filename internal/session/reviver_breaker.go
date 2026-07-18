package session

import (
	"log/slog"
	"sync"
	"time"
)

// ReviveBreaker bounds futile control-pipe reconnect storms on a wedged tmux
// server (#1579).
//
// The failure mode it guards: when the tmux SERVER wedges (stops serving
// control pipes for existing sessions while `has-session` still answers yes),
// every 60s reviver sweep classifies the affected sessions ClassErrored and
// "successfully" reconnects their pipes — which die again immediately. Because
// pm.Connect returns nil each time, there is no failure signal, so the reviver
// respawns the same dead pipes forever (observed: 37 respawns of ~7 sessions in
// 9 minutes). Pressing R in the TUI just triggers another doomed attach. Only a
// full `pkill tmux` + relaunch recovers, and nothing tells the user that.
//
// The breaker detects futility by transition, not by action return code: a
// revive is futile when the session is ClassErrored AGAIN on a later sweep
// despite a prior "successful" revive, or when the revive action itself errors.
// After FutilityThreshold consecutive futile revives it opens the session's
// circuit — revives are skipped and throttled to one probe per exponential
// cooldown (InitialCooldown → MaxCooldown). A single ClassAlive classification
// fully resets the entry (real recovery). When several circuits are open at
// once the shape is server-wide, not session-specific, so it emits one loud,
// rate-limited wedge warning naming the manual remedy. It NEVER auto-kills tmux.
type ReviveBreaker struct {
	mu      sync.Mutex
	entries map[string]*breakerEntry
	log     *slog.Logger
	now     func() time.Time

	lastWedgeWarn time.Time
}

type breakerEntry struct {
	futileCount   int
	pendingVerify bool // a revive ran last sweep; next ClassErrored ⇒ futile
	circuitOpen   bool
	cooldown      time.Duration // current backoff window
	cooldownUntil time.Time     // no probe until this instant
	lastSeen      time.Time     // for TTL pruning
}

// Breaker tuning. Kept in one block so review can adjust N / backoff / TTL
// without hunting through the logic.
const (
	// FutilityThreshold is how many consecutive futile revives trip the circuit.
	FutilityThreshold = 3
	// InitialCooldown is the first throttle window after the circuit opens.
	InitialCooldown = 2 * time.Minute
	// MaxCooldown caps the exponential backoff.
	MaxCooldown = 30 * time.Minute
	// WedgeMinOpenCircuits is the number of simultaneously-open circuits that
	// looks server-wide (a wedge) rather than session-specific.
	WedgeMinOpenCircuits = 2
	// WedgeWarnInterval rate-limits the loud wedge warning.
	WedgeWarnInterval = 10 * time.Minute
	// EntryTTL prunes stale per-session state (a session removed, renamed, or
	// long-healthy leaves no residue).
	EntryTTL = 1 * time.Hour
)

// globalReviveBreaker is the process-wide breaker wired by NewReviver(). The
// TUI constructs a fresh Reviver each 60s sweep, so the breaker state has to
// outlive the Reviver to observe a storm across sweeps.
var globalReviveBreaker = NewReviveBreaker(sessionLog)

// NewReviveBreaker returns an empty breaker. log may be nil (no logging).
func NewReviveBreaker(log *slog.Logger) *ReviveBreaker {
	return &ReviveBreaker{
		entries: make(map[string]*breakerEntry),
		log:     log,
		now:     time.Now,
	}
}

// OnClassify records this sweep's classification for an instance and returns
// whether a revive should be attempted (only meaningful for ClassErrored).
//
//   - ClassAlive  → full reset (the session actually recovered). Returns true
//     but the caller ignores it for non-errored classes.
//   - ClassDead   → no attempt, counters untouched (a gone server is a
//     different condition; an errored↔dead flap must not lose futility state).
//   - ClassErrored → if a revive from a prior sweep is pending verification and
//     the session is still errored, that revive was futile. Then: if the
//     circuit is open and still cooling down, returns false (skip); otherwise
//     returns true (attempt, including the single probe at cooldown expiry).
func (b *ReviveBreaker) OnClassify(id, title string, class RevivalClass) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	e := b.entries[id]
	if e == nil {
		e = &breakerEntry{}
		b.entries[id] = e
	}
	e.lastSeen = now

	switch class {
	case ClassAlive:
		// Genuine recovery — wipe all breaker state for this session.
		delete(b.entries, id)
		return true
	case ClassDead:
		// Never revived; hold state without counting a futile revive.
		e.pendingVerify = false
		return false
	}

	// class == ClassErrored
	if e.pendingVerify {
		// Prior sweep's revive did not stabilize the session.
		e.pendingVerify = false
		b.recordFutileLocked(e, id, title, now)
	}

	if e.circuitOpen && now.Before(e.cooldownUntil) {
		if b.log != nil {
			b.log.Info("reviver_circuit_skip",
				slog.String("title", title),
				slog.String("instance_id", id),
				slog.Int("futile_count", e.futileCount),
				slog.Duration("cooldown_remaining", e.cooldownUntil.Sub(now).Round(time.Second)))
		}
		return false
	}
	// Circuit closed, or open but cooldown expired → allow one (probe) attempt.
	return true
}

// AfterRevive records that a revive action ran this sweep. actionErr != nil is
// immediately futile; a nil error arms pendingVerify so the next OnClassify can
// judge whether the session actually stabilized.
func (b *ReviveBreaker) AfterRevive(id, title string, actionErr error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	e := b.entries[id]
	if e == nil {
		e = &breakerEntry{}
		b.entries[id] = e
	}
	e.lastSeen = b.now()

	if actionErr != nil {
		e.pendingVerify = false
		b.recordFutileLocked(e, id, title, b.now())
		return
	}
	e.pendingVerify = true
}

// recordFutileLocked increments the futile counter and, at/after the threshold,
// opens (or re-arms) the circuit with exponential backoff. Caller holds b.mu.
func (b *ReviveBreaker) recordFutileLocked(e *breakerEntry, id, title string, now time.Time) {
	e.futileCount++

	switch {
	case !e.circuitOpen && e.futileCount >= FutilityThreshold:
		e.circuitOpen = true
		e.cooldown = InitialCooldown
	case e.circuitOpen:
		// A probe was itself futile — back off harder.
		e.cooldown *= 2
		if e.cooldown > MaxCooldown {
			e.cooldown = MaxCooldown
		}
	default:
		// Below threshold, circuit still closed — nothing to arm yet.
		return
	}

	e.cooldownUntil = now.Add(e.cooldown)
	if b.log != nil {
		b.log.Warn("reviver_circuit_open",
			slog.String("title", title),
			slog.String("instance_id", id),
			slog.Int("futile_count", e.futileCount),
			slog.Duration("cooldown", e.cooldown))
	}
	b.maybeWarnWedgeLocked(now)
}

// maybeWarnWedgeLocked emits one rate-limited wedge warning when enough circuits
// are open simultaneously that the cause is server-wide. Caller holds b.mu.
func (b *ReviveBreaker) maybeWarnWedgeLocked(now time.Time) {
	open := 0
	for _, e := range b.entries {
		if e.circuitOpen {
			open++
		}
	}
	if open < WedgeMinOpenCircuits {
		return
	}
	if !b.lastWedgeWarn.IsZero() && now.Sub(b.lastWedgeWarn) < WedgeWarnInterval {
		return
	}
	b.lastWedgeWarn = now
	if b.log != nil {
		b.log.Warn("reviver_tmux_wedge_suspected",
			slog.Int("open_circuits", open),
			slog.String("remedy", "tmux server likely wedged; reconnect/R cannot recover it — run `pkill tmux` then relaunch agent-deck"))
	}
}

// Prune drops entries not seen within EntryTTL. Called once per ReviveAll batch.
func (b *ReviveBreaker) Prune() {
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := b.now().Add(-EntryTTL)
	for id, e := range b.entries {
		if e.lastSeen.Before(cutoff) {
			delete(b.entries, id)
		}
	}
}

// openCircuits reports how many circuits are currently open. Test helper.
func (b *ReviveBreaker) openCircuits() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	open := 0
	for _, e := range b.entries {
		if e.circuitOpen {
			open++
		}
	}
	return open
}
