# Task 06 — daemon wiring: build the resume engine, enrich the candidate

tier: strong
depends on: tasks 01, 02, 03, 04, 05 (all of them)
parallel with: nothing
worktree: `/Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` (branch `feature/selfheal-auto-resume`)

Use absolute paths under that worktree for every Read/Edit/Write, and
`git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` for
every git command. Never run `git stash`, `git checkout`, `git switch`, or
`git reset`; never edit the root checkout at `/Users/doozyx/DoozyX/agent-deck`.

**Precondition to check first:**
```sh
grep -c 'ModeResume' internal/selfheal/selfheal.go
grep -c 'NewResumeEngine' internal/selfheal/engine.go
grep -c 'NewResumeExecutor' internal/session/selfheal_resume.go
grep -c 'UsageLimitNotBefore' internal/session/usagelimit.go
```
All four must print `1` or more. If any prints `0`, its task has not landed —
stop and report BLOCKED naming the missing one.

---

## Design extracts (verbatim from the approved design)

> ### D2 — Dwell 60s, anchored on `StatusChangedAt`
>
> Anchored on `StatusChangedAt`, **not** `LastSentAt`. … Here the banner is direct
> positive evidence, so a session whose last prompt a human typed by hand is
> equally eligible. Without this, every hand-driven root session stays unhealed.

> ### D3 — `NotBefore` gate, for `usage-limit`
>
> `usage-limit` therefore enters the actionable set with dwell 0 and a `NotBefore`
> supplied by the caller.

> ### D6 — Empty composer is a precondition
>
> If the composer holds a draft, the verdict downgrades to `ActionEscalate` and
> self-heal does not act.
>
> `stall.go` states the reason — *"submitting someone else's text is not a
> decision a status probe gets to make"* — and it was confirmed the hard way on
> 2026-08-07: recovering `conductor2-testfix` required `session nudge --force`,
> and the force path **consumed** the operator's draft (`target release-6.18.1`)
> rather than restoring it. Autonomous code may not do that to text a human typed.

> ### D7 — New mode `resume`, off by default

> ### D8 — No new timer
>
> The transition daemon already polls every 1–3s (`notifyPollFast` = 1s,
> `notifyPollMedium` = 2s, `notifyPollSlow` = 3s,
> `internal/session/transition_daemon.go:16`), and `selfheal_pass.go` carries an
> explicit constraint: *"F3: no new watchdog layer — the existing poll loop drives
> it"*. The 60s dwell and the `NotBefore` gate supply the patience. No cron, no
> goroutine, no launchd unit.

> ## 3. Architecture
>
> ```
> internal/session/selfheal_pass.go  wire executor; empty-composer precondition;
>                                    populate NotBefore
> ```
>
> Inherited unchanged: two-read confirm, per-session cap (2/6h), global cap
> (5/hour), circuit breaker, flicker quarantine, opt-out, NDJSON audit.

> ## 4. Configuration
>
> ```toml
> [selfheal]
> enabled = true
> mode    = "resume"
> global_per_hour = 30
> ```
>
> No new dial. `global_per_hour` already exists, but its default of 5 is wrong for
> this workload: a transport outage is correlated and wedges every session at once,
> so 5 would heal 5 of 30. The cap was sized for restarts; a resume is a single
> delivered message and is far cheaper. 30 is the recommended operator setting, not
> a new default — the shipped default stays 5.

> ## 5. Out of scope
>
> - **Changing the shipped defaults.** `enabled` stays false; `mode` stays
>   `observe`.

> ## 6. Verification
>
> **Engine (unit).** … `ModeObserve` still constructs no executor.

---

## The two problems this wiring has to solve

**1. `CachedSubstate()` is deliberately usage-limit-blind.**
`buildSelfHealCandidate` reads `inst.CachedSubstate()`, and
`internal/session/instance.go` documents that path as explicitly *not* wired for
usage-limit ("this path must stay filesystem-free"). So a usage-limited session
would reach self-heal as `idle-at-empty-prompt` and never be resumed. The
self-heal pass runs on the daemon poll, not the TUI render hot path, so it may
ask the live detector — `usageLimited()` is throttled to one transcript scan per
instance per 5 seconds, which the 1–3 s poll cannot outrun. Gate the call on a
non-busy cached substate so a working session pays nothing.

**2. Reading the composer costs a pane capture.**
Only do it for the two substates that can produce `ActionResume`. Every other
session pays nothing, and D8's "no new watchdog layer" stays true because this is
still one extra capture on an already-running poll, for a handful of sessions.

## Acceptance criteria

1. `SelfHealSettings.SelfHealMode()` returns `"resume"` for `mode = "resume"`,
   and still normalizes empty/unknown to `"observe"`.
2. The registry **builds** a resume engine (with an executor) for mode
   `"resume"`, and an observe engine (executor nil) for every other mode. The
   test must exercise the real `engineFor` construction path on a fresh registry
   — pre-injecting `r.engines[profile]` hits the cache-return and asserts on the
   injected value, so it cannot fail.
3. `buildSelfHealCandidate` lifts a usage-limited session to
   `SubstateUsageLimit` and populates `NotBefore` from `UsageLimitNotBefore()`.
4. `buildSelfHealCandidate` sets `ComposerDraft` for the two resume substates
   and leaves it false for every other substate (no pane capture).
5. The executor's instance view is refreshed at the top of every pass.
6. The shipped default is unchanged: with no `[selfheal]` config, the pass is a
   no-op and no engine is built.
7. `engineFor`'s doc comment states that the engine is cached per profile and
   the mode is read only on the miss, so changing `[selfheal] mode` takes effect
   only after a transition-daemon restart. A test pins that behaviour.
8. `go test ./internal/session/ -run SelfHeal -v` green.

## Edits

### 1. `internal/session/userconfig.go` — accept the new mode

Replace `SelfHealMode` (lines 299-309):

```go
// SelfHealMode normalizes the configured mode to a known value. Empty / unknown
// → "observe" (the safe default). Used by the daemon when constructing the
// engine. The string return matches selfheal.Mode values.
//
// "resume" is the ONE acting mode: it authorises exactly (resume mode × resume
// action) — deliver a single continuation prompt to a session wedged by a
// transport error or an exhausted usage window — and nothing else.
// "single_action" / "full" remain DEFINED but GUARDED.
func (s SelfHealSettings) SelfHealMode() string {
	switch s.Mode {
	case "single_action", "full", "resume":
		return s.Mode
	default:
		return "observe"
	}
}
```

Update the `Mode` field's doc comment (lines 269-273):

```go
	// Mode is the authority level:
	//   "observe"       (DEFAULT) — logs would_have, takes no action.
	//   "resume"                  — authorises exactly one path: deliver a single
	//                               continuation prompt to a session wedged by a
	//                               transport error (api-error) or an exhausted
	//                               usage window (usage-limit). Every other action
	//                               still refuses.
	//   "single_action" / "full"  — Stages 2-3, DEFINED but GUARDED, refuse to act.
	// An unknown/empty value is normalized to "observe".
	Mode string `toml:"mode,omitempty"`
```

Note the shipped default is untouched: `SelfHealSettings{}` still has
`Enabled: false` and `Mode: ""` → `"observe"`.

### 2. `internal/session/selfheal_pass.go` — the whole file

Replace the file contents with:

```go
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
	return s == SubstateAPIError || s == SubstateUsageLimit
}

// buildSelfHealCandidate assembles the pure Candidate snapshot for one instance
// from data the daemon already read this cycle, plus two narrowly-scoped extra
// reads (see below). It does NO DB mutation — it reuses the cached substate, the
// canonical status, the hook freshness, and the content signal the transition
// path already computes.
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
	if !busy && inst.usageLimited() {
		sub = SubstateUsageLimit
		notBefore = inst.UsageLimitNotBefore()
	}

	// D6: an operator draft in the composer is a hard precondition. Read only for
	// the substates that can produce a resume — every other session skips the pane
	// capture entirely.
	composerDraft := false
	if isSelfHealResumeSubstate(sub) {
		composerDraft = instanceComposerHasDraft(inst)
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
		_ = engine.ProcessRead(c, now)
	}
}
```

### 3. `internal/session/transition_daemon.go` — rename the call

At line 422-427, replace the comment and call with:

```go
	// Self-heal: evaluate every instance through the profile's engine. In every
	// mode but "resume" this logs what it WOULD do and takes ZERO action; in
	// "resume" it may deliver ONE continuation prompt to a confirmed api-error /
	// usage-limit candidate whose composer is empty. Runs every poll (including
	// the first) so the dwell/confirm clocks start immediately. Reuses the
	// instances/hookStatuses already loaded above — no new goroutine (F3).
	// Disabled-by-config → cheap no-op.
	d.runSelfHealPass(profile, instances, statuses, hookStatuses, db, time.Now().UTC())
```

## Tests — `internal/session/selfheal_pass_test.go`

Replace `TestSelfHealRegistry_ObserveEngineOnly` (lines 104-112) with the tests
below.

**These must construct through the real `engineFor` MISS path — do not
pre-inject `r.engines["p"]`.** Pre-injecting hits the cache-return at the top of
`engineFor`, so the test asserts on the value it just injected and *cannot fail*;
it would pass against an `engineFor` that ignores `mode` entirely, which is
precisely acceptance criterion 2. Nothing needs mocking to reach the real path:
`internal/session`'s `TestMain` (`internal/session/testmain_test.go`) already
calls `testutil.IsolateHome()`, so `SelfHealAuditPath` + `NewNDJSONSink` resolve
and create under the sandboxed HOME, never the operator's data dir.

```go
// The registry must BUILD the right engine for the mode — constructed through
// the real engineFor path, one fresh registry per mode so every call is a cache
// MISS. A pre-injected engine would only re-assert the injected value.
func TestSelfHealRegistry_NonResumeMode_BuildsObserveEngine(t *testing.T) {
	for _, mode := range []string{"observe", "single_action", "full", "", "nonsense"} {
		r := newSelfHealRegistry()
		e, exec := r.engineFor("p", selfheal.DefaultCaps(), mode)
		if e == nil {
			t.Fatalf("mode %q: engineFor returned nil — the audit sink could not be opened. "+
				"internal/session's TestMain calls testutil.IsolateHome(); if that stopped "+
				"happening this test would write to the real data dir", mode)
		}
		if e.Mode() != selfheal.ModeObserve {
			t.Fatalf("mode %q: registry must build an observe engine, got %q", mode, e.Mode())
		}
		if exec != nil {
			t.Fatalf("mode %q: a non-resume engine must hold no executor", mode)
		}
	}
}

// Acceptance criterion 2: mode "resume" BUILDS the acting engine, with an
// executor. Fresh registry, so this exercises the construction branch.
func TestSelfHealRegistry_ResumeMode_BuildsActingEngine(t *testing.T) {
	r := newSelfHealRegistry()
	e, exec := r.engineFor("p", selfheal.DefaultCaps(), "resume")
	if e == nil {
		t.Fatal("engineFor returned nil — the audit sink could not be opened")
	}
	if e.Mode() != selfheal.ModeResume {
		t.Fatalf("want %q, got %q", selfheal.ModeResume, e.Mode())
	}
	if exec == nil {
		t.Fatal("the resume engine must hand back its executor so the pass can refresh the view")
	}
	if r.execs["p"] != exec {
		t.Fatal("the executor must be retained in the registry, not rebuilt per pass")
	}
}

// Pins the documented caching caveat rather than leaving it as a surprise: the
// engine is built once per profile and the mode is read only on that miss, so
// flipping [selfheal] mode in config has NO effect until the transition daemon
// restarts. The engine must outlive the poll — the confirm and cap/breaker
// windows accumulate inside it — so this is the accepted trade, documented for
// operators in docs/self-heal.md. If this test starts failing because the engine
// is rebuilt on a mode change, delete the test AND the doc paragraph together.
func TestSelfHealRegistry_ModeChangeNeedsRestart(t *testing.T) {
	r := newSelfHealRegistry()
	first, _ := r.engineFor("p", selfheal.DefaultCaps(), "observe")
	if first == nil {
		t.Fatal("engineFor returned nil — the audit sink could not be opened")
	}
	second, exec := r.engineFor("p", selfheal.DefaultCaps(), "resume")
	if second != first {
		t.Fatal("the engine must be cached per profile — rebuilding it resets the confirm and cap windows")
	}
	if second.Mode() != selfheal.ModeObserve {
		t.Fatalf("a cached engine keeps its original mode, got %q", second.Mode())
	}
	if exec != nil {
		t.Fatal("no executor appears without a restart either")
	}
}
```

Add to `TestSelfHealMode_Normalizes` (line 88) the resume case, or append:

```go
// "resume" is a recognised mode; the shipped default is still observe.
func TestSelfHealMode_AcceptsResume(t *testing.T) {
	if got := (SelfHealSettings{Mode: "resume"}).SelfHealMode(); got != "resume" {
		t.Fatalf("want resume, got %q", got)
	}
	if got := (SelfHealSettings{}).SelfHealMode(); got != "observe" {
		t.Fatalf("the shipped default must stay observe, got %q", got)
	}
	if (SelfHealSettings{}).Enabled {
		t.Fatal("the shipped default must stay disabled")
	}
}
```

Append the candidate-enrichment tests:

```go
// The composer read is scoped to the two substates that can produce a resume.
func TestIsSelfHealResumeSubstate(t *testing.T) {
	for _, s := range []Substate{SubstateAPIError, SubstateUsageLimit} {
		if !isSelfHealResumeSubstate(s) {
			t.Fatalf("%q must be a resume substate", s)
		}
	}
	for _, s := range []Substate{SubstateNone, SubstateRunning, SubstateIdleAtEmptyPrompt, SubstateAuth401, SubstateModelUnavailable, SubstateStalled} {
		if isSelfHealResumeSubstate(s) {
			t.Fatalf("%q must NOT trigger a composer capture", s)
		}
	}
}

// A candidate with no tmux session takes neither extra read and reports no draft.
func TestBuildSelfHealCandidate_NoPane_NoComposerDraft(t *testing.T) {
	inst := &Instance{ID: "s1", Title: "worker-3"}
	c := buildSelfHealCandidate(inst, "idle", nil, time.Time{}, false)
	if c.ComposerDraft {
		t.Fatal("no pane means nothing to protect")
	}
	if !c.NotBefore.IsZero() {
		t.Fatalf("no usage-limit verdict means no schedule, got %s", c.NotBefore)
	}
}

// instanceComposerHasDraft fails SAFE: a capture that errors must read as
// "there might be a draft", never as "the composer is empty".
func TestInstanceComposerHasDraft_NoPane(t *testing.T) {
	if instanceComposerHasDraft(&Instance{ID: "s1"}) {
		t.Fatal("a nil tmux session has no composer to hold a draft")
	}
}
```

If `selfheal_pass_test.go` does not already import `time`, add it.

## Verification

```sh
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume
gofmt -l internal/session/ internal/selfheal/ cmd/agent-deck/
```
Expected: **nothing** (empty).

```sh
go build ./... && go vet ./internal/session/ ./cmd/agent-deck/
```
Expected: no output, exit 0.

```sh
go test ./internal/session/ -run 'SelfHeal|ComposerHasDraft' -count=1 -v
```
Expected: `ok  	github.com/asheshgoplani/agent-deck/internal/session`. Run-specific
sentinel: `TestSelfHealRegistry_ResumeMode_BuildsActingEngine` must appear as
`--- PASS`.

Confirm the registry tests actually go through the construction path — a
pre-injected engine makes them unfailable:
```sh
grep -n 'r.engines\[' internal/session/selfheal_pass_test.go
```
Expected: **no output**. No test may write `r.engines[...]` directly.

```sh
go test ./internal/selfheal/ -count=1
```
Expected: `ok  	github.com/asheshgoplani/agent-deck/internal/selfheal` (this task
must not regress the engine package).

Structural check that the shipped defaults did not move — this is the invariant
the whole PR turns on:
```sh
grep -n 'Enabled bool' -A2 internal/session/userconfig.go | head -5
go test ./internal/session/ -run TestSelfHealMode_AcceptsResume -count=1 -v
```
Expected: the `Enabled` field carries no default-true anywhere, and the test
PASSes (it asserts `SelfHealSettings{}` is disabled and observe).

Confirm the old function name is fully gone (a leftover call site would compile
against a stale copy):
```sh
grep -rn 'runSelfHealObservePass' --include='*.go' .
```
Expected: **no output**.

## Commit

```sh
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume add \
  internal/session/selfheal_pass.go internal/session/selfheal_pass_test.go \
  internal/session/transition_daemon.go internal/session/userconfig.go
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume commit -m "feat(session): wire the resume engine into the transition daemon poll

The pass builds the acting engine only for mode \"resume\"; every other mode still
gets the observe engine, which holds no executor at all, so \"observe takes no
action\" stays structural. The shipped defaults are untouched: enabled false,
mode observe.

Two candidate enrichments, both scoped so a healthy session pays nothing.
CachedSubstate is deliberately usage-limit-blind because it is the render hot
path and must stay filesystem-free, so a quota-rejected session would have
reached self-heal labelled idle-at-empty-prompt and never been scheduled; the
pass asks the live detector instead, gated on not-busy and throttled to one
transcript scan per 5s. And the composer is read only for the two substates that
can produce a resume, failing safe: a capture that errors reads as \"there might
be a draft\", never as \"the composer is empty\".

The engine stays cached per profile, so the mode is read only when it is first
built: changing [selfheal] mode takes effect on the next daemon restart, not the
next poll. Rebuilding per pass would reset the two-read confirm and every cap and
breaker window, so the caveat is documented rather than engineered away.

No new timer, goroutine or unit — the existing 1-3s poll drives it."
```

## Interfaces

### consumes
- `internal/selfheal`: `selfheal.Engine`, `selfheal.Caps`, `selfheal.DefaultCaps()`, `selfheal.EventSink`, `selfheal.MemorySink`, `selfheal.NewNDJSONSink`, `selfheal.NewObserveEngine(caps, sink)`, `selfheal.NewResumeEngine(caps, sink, exec)` (**task 03**), `selfheal.ModeObserve`, `selfheal.ModeResume` (**task 02**), `selfheal.Candidate` incl. `NotBefore` and `ComposerDraft` (**task 02**), `(*Engine).ProcessRead`, `(*Engine).Policy()`, `(*Engine).Mode()`
- `internal/session/selfheal_resume.go` (**task 05**): `NewResumeExecutor() *ResumeExecutor`, `(*ResumeExecutor).SetInstances([]*Instance)`
- `internal/session/usagelimit.go` (**task 04**): `(*Instance).usageLimited() bool`, `(*Instance).UsageLimitNotBefore() time.Time`
- `internal/session/instance.go`: `SubstateAPIError` (**task 01**), `SubstateUsageLimit`, `SubstateRunning`, `(*Instance).CachedSubstate()`, `(*Instance).GetTmuxSession()`, `(*Instance).GetWaitingSince()`, `(*Instance).AuthHoldSurvivedBoot()`, fields `ID`/`Title`/`GroupPath`/`Account`
- `internal/send`: `send.ComposerHasDraft(raw string, strip func(string) string) bool`
- `internal/tmux`: `tmux.StripANSI`, `(*tmux.Session).CapturePaneFresh() (string, error)`
- `internal/session`: `GetSelfHealSettings()`, `SelfHealSettings`, `SelfHealAuditPath(profile)`, `GlobalFlickerDetector()`, `normalizeStatusString`, `hookFreshWindow`, `transitionEventOutputHash`, `HookStatus`, `(*statedb.StateDB).ReadLastSentAt`
- `internal/session/testmain_test.go`: the package `TestMain` already calls `testutil.IsolateHome()`, which is what makes it safe for a test to drive the real `engineFor` construction path (it opens a real NDJSON sink under the sandboxed HOME). Do not add a second `TestMain`.

### produces
- `internal/session/selfheal_pass.go`: **renamed** `func (d *TransitionDaemon) runSelfHealPass(...)` (was `runSelfHealObservePass`)
- `internal/session/selfheal_pass.go`: **changed signature** `func (r *selfHealRegistry) engineFor(profile string, caps selfheal.Caps, mode string) (*selfheal.Engine, *ResumeExecutor)`
- `internal/session/selfheal_pass.go`: `selfHealRegistry.execs map[string]*ResumeExecutor`
- `internal/session/selfheal_pass.go`: `func isSelfHealResumeSubstate(s Substate) bool`
- `internal/session/selfheal_pass.go`: `func instanceComposerHasDraft(inst *Instance) bool`
- `internal/session/userconfig.go`: `SelfHealSettings.SelfHealMode()` now returns `"resume"` for that configured value

## Record (append-only)

### 2026-08-07 — implemented

- Files touched: `internal/session/selfheal_pass.go`,
  `internal/session/selfheal_pass_test.go`,
  `internal/session/transition_daemon.go`, `internal/session/userconfig.go`.
- Implemented exactly as written; no deviations.
- Preconditions checked: `ModeResume` in `selfheal.go` → 4, `NewResumeEngine` in
  `engine.go` → 2, `NewResumeExecutor` in `selfheal_resume.go` → 2,
  `UsageLimitNotBefore` in `usagelimit.go` → 2. All ≥ 1.
- TDD: the replacement tests were written first and failed to build
  (`assignment mismatch: 2 variables but r.engineFor returns 1 value`,
  `too many arguments in call to r.engineFor`, `r.execs undefined`) before the
  pass rewrite landed.
- Verification:
  `gofmt -l internal/session/ internal/selfheal/ cmd/agent-deck/` → empty.
  `go build ./...` → `BUILD_EXIT=0`.
  `go vet ./internal/session/ ./cmd/agent-deck/` → clean apart from the
  pre-existing `issue1225_wake_nudge_wiring_test.go:217` noted in task 01.
  `go test ./internal/session/ -run 'SelfHeal|ComposerHasDraft' -count=1 -v` →
  `TEST_EXIT=0`, `ok  github.com/asheshgoplani/agent-deck/internal/session 0.462s`,
  **21 `--- PASS`, 0 FAIL**; run-specific sentinel
  `--- PASS: TestSelfHealRegistry_ResumeMode_BuildsActingEngine` present.
  `grep -n 'r.engines\[' internal/session/selfheal_pass_test.go` → no output (the
  registry tests go through the real `engineFor` MISS path).
  `go test ./internal/selfheal/ -count=1` → `ok  …/internal/selfheal 0.187s`.
  `grep -rn 'runSelfHealObservePass' --include='*.go' .` → no output (the old name
  is fully gone).
  `grep -n 'Enabled bool' -A2 internal/session/userconfig.go` →
  `Enabled bool \`toml:"enabled,omitempty"\`` with no default-true anywhere, and
  `TestSelfHealMode_AcceptsResume` PASS pins `SelfHealSettings{}` as disabled +
  observe.
- No concerns.

### 2026-08-08 — amended by review round 2 (commits `225ff78f`, `7187cc75`)

The Record above said "implemented exactly as written; no deviations", and its
precondition list cites `UsageLimitNotBefore in usagelimit.go → 2`. Both have
stopped being true; this amendment records why (round 3, finding 5).

- **AC 3** names `(*Instance).UsageLimitNotBefore()` as the source of the
  candidate's `NotBefore`. That method no longer exists. `buildSelfHealCandidate`
  now reads `(*Instance).usageLimitedWithSchedule()`, which returns the
  usage-limit VERDICT and its schedule together under one `RLock` — see task 04's
  round-2 amendment for the torn-read argument. The precondition check quoted
  above returns **0** today; the equivalent check is
  `grep -c 'usageLimitedWithSchedule' internal/session/usagelimit.go`.
- **The lift itself is unchanged and is still what AC 3 requires**: a
  usage-limited session reaches self-heal as `SubstateUsageLimit` carrying its
  schedule, pinned by
  `TestBuildSelfHealCandidate_UsageLimited_LiftsSubstateAndSchedule` and
  `TestUsageLimitedWithSchedule_ReportsMemo_RebindClearsIt`.
- Everything else in this task is as written: the registry, `engineFor`'s
  observe-by-default construction, the `!settings.Enabled` early return, the
  shipped defaults (`Enabled bool` with no default-true) and the serial
  per-instance pass.

### 2026-08-08 — amended by review round 3 (this round)

- No AC changed; no behaviour changed. Round 3 finding 6 was DECIDED as
  "accepted as designed": the resume send stays SYNCHRONOUS inside
  `Engine.ProcessRead`, driven from this task's serial per-instance loop, because
  design F3 forbids a new watchdog layer or goroutine. A comment at the
  `engine.ProcessRead(c, now)` call site in `selfheal_pass.go` now records the
  accepted trade-off explicitly — a correlated outage can add tens of seconds to
  ONE profile's poll, the empty-composer precondition makes the 10s guard hold
  unlikely in the common case, and the bounded-worker alternative was considered
  and deliberately not taken. Recorded as an open design question for after the
  merge, not absorbed silently.
