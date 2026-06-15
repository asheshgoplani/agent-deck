package session

import (
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/selfheal"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// selfHealRegistry holds one observe-only self-heal Engine per profile, lazily
// created. The Engine must persist across poll cycles so the two-read confirm
// and the caps/backoff/breaker windows accumulate. It is owned by the transition
// daemon and never spawns its own goroutine (F3: no new watchdog layer — the
// existing poll loop drives it). Safe for concurrent use.
type selfHealRegistry struct {
	mu      sync.Mutex
	engines map[string]*selfheal.Engine
	sinks   map[string]selfheal.EventSink
}

func newSelfHealRegistry() *selfHealRegistry {
	return &selfHealRegistry{
		engines: map[string]*selfheal.Engine{},
		sinks:   map[string]selfheal.EventSink{},
	}
}

// engineFor returns the observe-only engine for a profile, creating it on first
// use with the configured caps + the durable NDJSON audit sink. Returns nil when
// the audit sink can't be opened (self-heal then stands down for that profile,
// rather than failing the whole poll — observe-only must never destabilize the
// daemon).
func (r *selfHealRegistry) engineFor(profile string, caps selfheal.Caps) *selfheal.Engine {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.engines[profile]; ok {
		return e
	}
	path, err := SelfHealAuditPath(profile)
	if err != nil {
		return nil
	}
	sink, err := selfheal.NewNDJSONSink(path)
	if err != nil {
		return nil
	}
	e := selfheal.NewObserveEngine(caps, sink)
	r.engines[profile] = e
	r.sinks[profile] = sink
	return e
}

// capsFromSettings maps the config dials onto selfheal.Caps, falling back to the
// trusted defaults for any unset field.
func capsFromSettings(s SelfHealSettings) selfheal.Caps {
	caps := selfheal.DefaultCaps()
	if s.PerSessionPerWindow > 0 {
		caps.PerSession6h = s.PerSessionPerWindow
	}
	if s.GlobalPerHour > 0 {
		caps.GlobalPerHour = s.GlobalPerHour
	}
	return caps
}

// buildSelfHealCandidate assembles the pure Candidate snapshot for one instance
// from data the daemon already read this cycle. It does NO new tmux capture and
// NO DB mutation — it reuses the cached substate, the canonical status, the hook
// freshness, and the content signal the transition path already computes. This
// is what keeps the observe pass cheap and side-effect-free.
//
// hs is the instance's hook status (may be nil). lastSentAt is the last_sent_at
// clock read from the DB (zero if never sent). optedOut folds the per-session and
// group opt-out config.
func buildSelfHealCandidate(inst *Instance, status string, hs *HookStatus, lastSentAt time.Time, optedOut bool) selfheal.Candidate {
	sub := inst.CachedSubstate()
	busy := sub == SubstateRunning
	hookRunningFresh := hs != nil &&
		normalizeStatusString(hs.Status) == "running" &&
		(hs.UpdatedAt.IsZero() || time.Since(hs.UpdatedAt) <= hookFreshWindow)

	return selfheal.Candidate{
		SessionID:        inst.ID,
		Title:            inst.Title,
		Group:            inst.GroupPath,
		Profile:          "", // stamped by the caller (it knows the profile)
		Account:          inst.Account,
		Status:           status,
		Substate:         sub,
		Busy:             busy,
		HookRunningFresh: hookRunningFresh,
		OutputSig:        transitionEventOutputHash(inst),
		Stopped:          normalizeStatusString(status) == "stopped",
		OptedOut:         optedOut,
		StatusChangedAt:  inst.GetWaitingSince(), // best-available durable dwell anchor
		LastSentAt:       lastSentAt,
	}
}

// runSelfHealObservePass evaluates every instance through the profile's observe
// engine, emitting one audit record per detection. It takes ZERO action — the
// engine is observe-only by construction (no executor). It is called from the
// daemon's syncProfile AFTER statuses are computed, reusing the already-loaded
// instances/hookStatuses (no extra poll). Disabled-by-config → no-op.
//
// now is date-u anchored by the caller (time.Now().UTC()). db is the profile's
// state DB for the read-only last_sent_at lookup.
func (d *TransitionDaemon) runSelfHealObservePass(profile string, instances []*Instance, statuses map[string]string, hookStatuses map[string]*HookStatus, db *statedb.StateDB, now time.Time) {
	settings := GetSelfHealSettings()
	if !settings.Enabled {
		return // global kill switch (default): self-heal does nothing.
	}
	if d.selfheal == nil {
		d.selfheal = newSelfHealRegistry()
	}
	engine := d.selfheal.engineFor(profile, capsFromSettings(settings))
	if engine == nil {
		return // audit sink unavailable — stand down for this profile.
	}

	// Subscribe to the global FlickerDetector (the same one the TUI feeds): a
	// flapping session is by definition not safely healable, so self-heal treats
	// it as quarantine-equivalent (SELF-HEAL-DESIGN.md §3.4). We update the policy
	// machine's flicker view each pass so the gate reflects current flapping.
	flicker := GlobalFlickerDetector()

	for _, inst := range instances {
		if inst == nil {
			continue
		}
		var lastSent time.Time
		if db != nil {
			if ts, err := db.ReadLastSentAt(inst.ID); err == nil && ts > 0 {
				lastSent = time.Unix(ts, 0)
			}
		}
		optedOut := settings.IsSessionOptedOut(inst.ID, inst.Title) ||
			settings.IsGroupOptedOut(inst.GroupPath)

		engine.Policy().SetFlickering(inst.ID, flicker.IsFlickering(inst.ID))

		c := buildSelfHealCandidate(inst, statuses[inst.ID], hookStatuses[inst.ID], lastSent, optedOut)
		c.Profile = profile
		// Observe-only: ProcessRead logs would_have and returns having taken NO
		// action (the engine holds no executor). We deliberately ignore the
		// returned event here — the audit sink already persisted it.
		_ = engine.ProcessRead(c, now)
	}
}
