package session

import (
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/selfheal"
	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// selfHealRegistry holds one self-heal Engine per profile, lazily created. The
// Engine must persist across poll cycles so the two-read confirm and the
// caps/backoff/breaker windows accumulate. It is owned by the transition daemon
// and never spawns its own goroutine (F3: no new watchdog layer — the existing
// poll loop drives it). Safe for concurrent use.
type selfHealRegistry struct {
	mu      sync.Mutex
	engines map[string]*selfheal.Engine
	sinks   map[string]selfheal.EventSink
	// execs holds the resume executor per profile, for the modes that have one.
	// It is kept alongside the engine because its instance view has to be
	// refreshed every pass while the engine itself is long-lived.
	execs map[string]*ResumeExecutor
}

func newSelfHealRegistry() *selfHealRegistry {
	return &selfHealRegistry{
		engines: map[string]*selfheal.Engine{},
		sinks:   map[string]selfheal.EventSink{},
		execs:   map[string]*ResumeExecutor{},
	}
}

// engineFor returns the engine for a profile, creating it on first use with the
// configured caps + the durable NDJSON audit sink. Returns nil when the audit
// sink can't be opened (self-heal then stands down for that profile, rather than
// failing the whole poll — supervision must never destabilize the daemon).
//
// mode selects the engine: "resume" builds the ONE acting engine, with a resume
// executor. Every other mode — including the guarded single_action/full — builds
// the observe engine, which holds no executor at all. That keeps "observe takes
// no action" a structural property rather than a runtime check.
//
// The returned executor is nil for a non-acting engine.
//
// CACHING CAVEAT, deliberate: mode is read only on the MISS path. The engine has
// to outlive a poll cycle — the two-read confirm and the cap/backoff/breaker
// windows accumulate inside it, and rebuilding it per pass would silently reset
// every one of them. The consequence is that editing `[selfheal] mode` in config
// changes nothing until the transition daemon is restarted. That is documented
// for operators in docs/self-heal.md (task 07); a rebuild-on-mode-change
// mechanism is out of scope for this PR.
func (r *selfHealRegistry) engineFor(profile string, caps selfheal.Caps, mode string) (*selfheal.Engine, *ResumeExecutor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.engines[profile]; ok {
		return e, r.execs[profile]
	}
	path, err := SelfHealAuditPath(profile)
	if err != nil {
		return nil, nil
	}
	sink, err := selfheal.NewNDJSONSink(path)
	if err != nil {
		return nil, nil
	}
	var e *selfheal.Engine
	var exec *ResumeExecutor
	if mode == string(selfheal.ModeResume) {
		exec = NewResumeExecutor()
		e = selfheal.NewResumeEngine(caps, sink, exec)
	} else {
		e = selfheal.NewObserveEngine(caps, sink)
	}
	r.engines[profile] = e
	r.sinks[profile] = sink
	if exec != nil {
		r.execs[profile] = exec
	}
	return e, exec
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

// selfHealResumeSubstates are the substates that can produce ActionResume. Two
// enrichments are scoped to them because both cost real work: the usage-limit
// verdict is a throttled transcript read, and the composer check is a pane
// capture. Every other session pays nothing.
func isSelfHealResumeSubstate(s Substate) bool {
	return s == SubstateAPIError || s == SubstateUsageLimit || s == SubstateModelUnavailable
}

// buildSelfHealCandidate assembles the pure Candidate snapshot for one instance
// from data the daemon already read this cycle, plus narrowly-scoped extra
// reads (see below). It does NO DB mutation — it normally reuses the cached
// substate, the canonical status, the hook freshness, and the content signal
// the transition path already computes.
//
// hs is the instance's hook status (may be nil). lastSentAt is the last_sent_at
// clock read from the DB (zero if never sent). optedOut folds the per-session and
// group opt-out config.
func buildSelfHealCandidate(inst *Instance, status string, hs *HookStatus, lastSentAt time.Time, optedOut bool) selfheal.Candidate {
	sub := inst.CachedSubstate()
	// When another live TUI owns status polling, the transition daemon reads the
	// coarse status from SQLite into freshly reconstructed Instances. Substate is
	// process-local and is not persisted, so that daemon-side cache is empty even
	// when the TUI saw Codex's capacity banner. Refresh only non-running Codex
	// sessions: that is the tool which emits this exact banner, and the narrow
	// gate avoids pane captures for the active fleet on every daemon pass.
	if sub == SubstateNone && IsCodexCompatible(inst.Tool) {
		switch normalizeStatusString(status) {
		case "idle", "waiting", "error":
			if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil {
				sub = tmuxSess.GetSubstate()
			}
		}
	}
	busy := sub == SubstateRunning
	hookRunningFresh := hs != nil &&
		normalizeStatusString(hs.Status) == "running" &&
		(hs.UpdatedAt.IsZero() || time.Since(hs.UpdatedAt) <= hookFreshWindow)

	var notBefore time.Time
	// CachedSubstate is DELIBERATELY usage-limit-blind: it is the TUI render hot
	// path and must stay filesystem-free, so nothing there ever reports a quota
	// rejection (instance.go says so explicitly). Self-heal runs on the daemon
	// poll, not that path, so it may ask the live detector — which is throttled to
	// one transcript scan per instance per 5s and cannot be outrun by the 1-3s
	// poll. Without this a usage-limited session reaches self-heal labelled
	// idle-at-empty-prompt and is never scheduled.
	//
	// Gated on not-busy so an actively working session never pays for the scan,
	// and because a busy session is disqualified downstream regardless.
	//
	// The verdict and its schedule come from ONE accessor: read apart, a rebind
	// landing between them yields substate usage-limit with a zero NotBefore,
	// which is a candidate with no gate.
	if !busy {
		if limited, nb := inst.usageLimitedWithSchedule(); limited {
			sub = SubstateUsageLimit
			notBefore = nb
		}
	}

	// D6: an operator draft in the composer is a hard precondition. Attached as a
	// DEFERRED lookup rather than resolved here: each resolution forks a
	// `tmux capture-pane` with a 3s timeout, and this runs inside syncProfile's
	// serial instance loop on every 1-3s poll. A transport outage is correlated by
	// construction — the incident this feature exists for wedged 3 of 32 sessions
	// at once — so resolving eagerly would fork one capture per wedged session per
	// poll, including on the reads that can only return skip_dwell / skip_confirm
	// and never consult the answer. The engine resolves it once, on the read where
	// it is deciding to act, still ahead of RecordAttempt (candidate.go).
	//
	// Scoped to the substates that can produce a resume: every other session
	// carries no lookup at all.
	var composerDraft func() bool
	if isSelfHealResumeSubstate(sub) {
		composerDraft = func() bool { return composerDraftLookup(inst) }
	}

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
		NotBefore:        notBefore,
		ComposerDraft:    composerDraft,
	}
}

// composerDraftLookup is the seam the deferred draft check goes through, so a
// test can count resolutions — and prove there are none on the read path —
// without a live tmux server to capture from.
var composerDraftLookup = instanceComposerHasDraft

// instanceComposerHasDraft reports whether the instance's composer holds operator
// text. A capture failure returns TRUE — fail-safe: unable to prove the composer
// is empty means self-heal must not type into it. The raw capture keeps ANSI
// intact because the SGR dim attribute is the only thing separating Claude's
// autosuggestion from real operator input.
func instanceComposerHasDraft(inst *Instance) bool {
	tmuxSess := inst.GetTmuxSession()
	if tmuxSess == nil {
		return false // no pane at all: nothing to protect, and nothing to send to.
	}
	raw, err := tmuxSess.CapturePaneFresh()
	if err != nil {
		return true
	}
	return send.ComposerHasDraft(raw, tmux.StripANSI)
}

// runSelfHealPass evaluates every instance through the profile's engine, emitting
// one audit record per detection. In every mode but "resume" the engine holds no
// executor and the pass takes ZERO action. It is called from the daemon's
// syncProfile AFTER statuses are computed, reusing the already-loaded
// instances/hookStatuses (no extra poll, no new goroutine — F3).
// Disabled-by-config → no-op.
//
// now is date-u anchored by the caller (time.Now().UTC()). db is the profile's
// state DB for the read-only last_sent_at lookup.
func (d *TransitionDaemon) runSelfHealPass(profile string, instances []*Instance, statuses map[string]string, hookStatuses map[string]*HookStatus, db *statedb.StateDB, now time.Time) {
	settings := GetSelfHealSettings()
	if !settings.Enabled {
		return // global kill switch (default): self-heal does nothing.
	}
	if d.selfheal == nil {
		d.selfheal = newSelfHealRegistry()
	}
	engine, exec := d.selfheal.engineFor(profile, capsFromSettings(settings), settings.SelfHealMode())
	if engine == nil {
		return // audit sink unavailable — stand down for this profile.
	}
	// Refresh the executor's view every pass: the engine is long-lived (the
	// confirm and cap windows must accumulate) while the instance slice is rebuilt
	// each poll, so a stale view would resume a session that no longer exists.
	if exec != nil {
		exec.SetInstances(instances)
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
		// A session whose auth hold has ALREADY survived an automatic boot is
		// opted out. Self-heal's planned action for auth-401 is a single
		// creds-reasserting restart, which genuinely fixes the scratch-clobber
		// class — but once a boot has demonstrably died on auth, further restarts
		// are the fleet-death amplifier (auth_hold.go), and only a human can
		// clear the condition. Stage 2 must inherit this guard, so it lives here
		// rather than in the engine.
		optedOut := settings.IsSessionOptedOut(inst.ID, inst.Title) ||
			settings.IsGroupOptedOut(inst.GroupPath) ||
			inst.AuthHoldSurvivedBoot()

		engine.Policy().SetFlickering(inst.ID, flicker.IsFlickering(inst.ID))

		c := buildSelfHealCandidate(inst, statuses[inst.ID], hookStatuses[inst.ID], lastSent, optedOut)
		c.Profile = profile
		// ProcessRead evaluates, logs, and — in resume mode only, for a confirmed
		// api-error/usage-limit candidate with an empty composer — delivers one
		// continuation prompt. We deliberately ignore the returned event here: the
		// audit sink already persisted it.
		//
		// ACCEPTED TRADE-OFF (design F3: "no new watchdog layer, no new
		// goroutine"): that send is SYNCHRONOUS inside this serial per-instance
		// loop, and the send tuning carries a 10s guard hold plus a submit-
		// verification retry budget. A correlated outage — the incident this
		// feature exists for wedged 3 of 32 sessions at once — can therefore add
		// tens of seconds to ONE profile's transition-daemon poll. Moving the send
		// behind a bounded worker was considered and deliberately not taken: it is
		// a design change, not a fix, and the empty-composer precondition makes the
		// guard hold unlikely in the common case. Recorded as an open design
		// question rather than silently absorbed.
		_ = engine.ProcessRead(c, now)
	}
}
