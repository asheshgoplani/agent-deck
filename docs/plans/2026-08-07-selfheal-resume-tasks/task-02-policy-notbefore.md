# Task 02 — selfheal policy surface: `ModeResume`, `ActionResume`, `NotBefore`, dwell map

tier: mid
depends on: task 01 (needs `tmux.SubstateAPIError`)
parallel with: task 04 (disjoint files — task 04 touches only `internal/session/`)
worktree: `/Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` (branch `feature/selfheal-auto-resume`)

Use absolute paths under that worktree for every Read/Edit/Write, and
`git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` for
every git command. Never run `git stash`, `git checkout`, `git switch`, or
`git reset`; never edit the root checkout at `/Users/doozyx/DoozyX/agent-deck`.

**Precondition to check first:** `grep -n 'SubstateAPIError' internal/tmux/substate.go`
must print a line. If it does not, task 01 has not landed — stop and report BLOCKED.

---

## Design extracts (verbatim from the approved design)

> ### D2 — Dwell 60s, anchored on `StatusChangedAt`
>
> ```go
> stuckDwellThresholds[SubstateAPIError] = 60 * time.Second
> ```
>
> Anchored on `StatusChangedAt`, **not** `LastSentAt`. The `LastSentAt` anchor is
> correct for `idle-at-empty-prompt`, where the only evidence of stuckness is *we
> sent something and nothing happened*; a session nobody sent anything to is
> legitimately idle. Here the banner is direct positive evidence, so a session
> whose last prompt a human typed by hand is equally eligible. Without this, every
> hand-driven root session stays unhealed.

> ### D3 — `NotBefore` gate, for `usage-limit`
>
> Transport recovery is *dwell, then act*. A usage limit is *wait until T, then
> act*, where T is hours away. Dwell cannot express that.
>
> Add to `Candidate`:
>
> ```go
> // NotBefore blocks action until a known-future moment. Zero means no gate.
> NotBefore time.Time
> ```
>
> and one decision value, `DecisionSkipNotBefore`. The predicate refuses to act
> while `now < NotBefore`.
>
> `usage-limit` therefore enters the actionable set with dwell 0 and a `NotBefore`
> supplied by the caller.

> ### D5 — One action, one executor
>
> ```go
> ActionResume Action = "resume"   // params: {"reason": "transport" | "usage_limit"}
> ```
>
> Not `ActionResend` — that name means "replay the last intent", which can redo
> work when a turn partially completed and has nothing to replay when
> `LastSentAt` is zero. Not two actions either: both triggers deliver one
> continuation prompt through one send path, and splitting them would duplicate
> the executor.
>
> `ActionResend` itself is untouched: it remains the observe-mode `would_have` for
> `idle-at-empty-prompt` and stays unexecutable.

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
>
> ```go
> ModeResume Mode = "resume"
> ```
>
> `executeIfAuthorized` permits execution only for the pair
> (`ModeResume`, `ActionResume`). Every other mode and every other action keeps
> returning `ErrActionInGuardedMode`. `ModeSingleAction` and `ModeFull` are not
> touched.

> ## 6. Verification
>
> **Policy (unit).** `api-error` dwells on `StatusChangedAt` and is actionable with
> `LastSentAt` zero. `NotBefore` in the future yields `DecisionSkipNotBefore`;
> `NotBefore` in the past does not block. A drafted composer yields
> `ActionEscalate`. Caps, breaker and flicker gates still fire ahead of the action.

Note: the "drafted composer yields `ActionEscalate`" half of that bullet is
implemented in **task 03** (it is an engine-level downgrade). This task only adds
the `Candidate.ComposerDraft` field task 03 reads.

---

## Acceptance criteria

1. `selfheal.ModeResume`, `selfheal.ActionResume`, `selfheal.DecisionSkipNotBefore` exist.
2. `Candidate` has `NotBefore time.Time` and `ComposerDraft bool`.
3. `stuckDwellThresholds` has `SubstateAPIError: 60s` and `SubstateUsageLimit: 0`.
4. `actionForSubstate` maps both to `ActionResume`.
5. `Candidate.dwellAnchor` is unchanged — `api-error` and `usage-limit` both fall
   through to the `StatusChangedAt` branch, so a zero `LastSentAt` does not
   disqualify them.
6. `Evaluate` returns `DecisionSkipNotBefore` when `now.Before(c.NotBefore)`, and
   is unaffected when `NotBefore` is zero or in the past.
7. `ActionResend` still maps from `SubstateIdleAtEmptyPrompt` and nothing else.
8. `go test ./internal/selfheal/ -v` fully green.

## Edits

### 1. `internal/selfheal/selfheal.go`

Add to the `Mode` const block, after `ModeFull`:

```go
	// ModeResume is the ONE authorised acting mode: it permits exactly the pair
	// (ModeResume, ActionResume) and nothing else. It exists so the owner can
	// approve one narrow new path — deliver a single continuation prompt to a
	// session wedged by a transport error or a usage limit — without reopening
	// single_action / full, which stay HELD pending the §9 gap-fixes. Not the
	// default: [selfheal] mode ships as "observe".
	ModeResume Mode = "resume"
```

Add to the `Action` const block, after `ActionResend`:

```go
	// ActionResume — deliver ONE continuation prompt through the verified
	// `session nudge` send path (api-error, usage-limit). Params carry
	// {"reason": "transport"} or {"reason": "usage_limit"}.
	//
	// Deliberately not ActionResend: "resend" means replay the last intent,
	// which can redo work when a turn partially completed and has nothing to
	// replay when LastSentAt is zero. One action rather than two because both
	// triggers deliver one prompt through one send path.
	ActionResume Action = "resume"
```

Add to the `Decision` const block, after `DecisionSkipDwell`:

```go
	DecisionSkipNotBefore Decision = "skip_not_before" // scheduled for a known-future moment
```

Replace the `stuckDwellThresholds` var (lines 81-86) with:

```go
// stuckDwellThresholds are the §1.3 cause-specific dwell windows. A substate not
// present here is not a self-heal-actionable stuck class.
var stuckDwellThresholds = map[tmux.Substate]time.Duration{
	tmux.SubstateModelUnavailable:  90 * time.Second,
	tmux.SubstateAuth401:           60 * time.Second,
	tmux.SubstateIdleAtEmptyPrompt: 5 * time.Minute,
	// A transport banner is DIRECT positive evidence of a wedge, so 60s of it
	// with no busy cue and no output movement is enough. Anchored on
	// StatusChangedAt, not LastSentAt (see Candidate.dwellAnchor): the banner
	// stands on its own, so a session whose last prompt a human typed by hand is
	// equally eligible. Without that, every hand-driven root session stays
	// unhealed.
	tmux.SubstateAPIError: 60 * time.Second,
	// A usage limit does not dwell at all. Waiting is expressed by
	// Candidate.NotBefore — "wait until T, then act", where T is hours away and
	// no dwell window can express it. The two-read confirm, the caps and the
	// breaker all still apply on top.
	tmux.SubstateUsageLimit: 0,
}
```

Replace the `actionForSubstate` var (lines 88-94) with:

```go
// actionForSubstate maps a confirmed stuck substate to the action self-heal
// would take FIRST (§2.4). Observe mode records this as would_have.
var actionForSubstate = map[tmux.Substate]Action{
	tmux.SubstateModelUnavailable:  ActionRestartModelSwitch,
	tmux.SubstateAuth401:           ActionRestartReassertCreds,
	tmux.SubstateIdleAtEmptyPrompt: ActionResend,
	// Both recover the same way: one continuation prompt through the verified
	// send path. Only these two are ever executable, and only under ModeResume.
	tmux.SubstateAPIError:   ActionResume,
	tmux.SubstateUsageLimit: ActionResume,
}
```

### 2. `internal/selfheal/candidate.go`

Add two fields to `Candidate`, after `LastSentAt` (line 59):

```go
	// NotBefore blocks action until a known-future moment. Zero means no gate.
	//
	// It exists because a usage limit is not a dwell problem: the window reopens
	// at a wall-clock time hours away, and no dwell threshold can express "wait
	// until T". The caller derives it from the rejection's own reset string
	// (falling back to record + 20m), so the schedule is a hint and the observed
	// outcome remains the authority — a resume attempted at T either completes
	// the turn or produces a fresh rejection that rearms the gate.
	NotBefore time.Time

	// ComposerDraft is true when the target's composer holds text the operator
	// typed. It is a hard precondition, not a preference: submitting someone
	// else's text is not a decision a status probe gets to make, and the
	// `session nudge --force` path is known to CONSUME an operator draft rather
	// than restore it (2026-08-07, conductor2-testfix, "target release-6.18.1").
	// The engine downgrades ActionResume to ActionEscalate when this is set.
	ComposerDraft bool
```

In `Evaluate`, insert the `NotBefore` gate immediately after the dwell check
(after the `return PredicateResult{Decision: DecisionSkipDwell, Dwell: dwell}`
block closes, before the final `return`):

```go
	// A scheduled wake: the candidate is genuinely stuck and has dwelled, but the
	// condition is known not to have cleared yet (a usage window that reopens at
	// a wall-clock time). Acting early burns one of the two per-session
	// recoveries in the 6h window on an attempt that cannot succeed, which is
	// exactly what the schedule exists to prevent.
	if !c.NotBefore.IsZero() && now.Before(c.NotBefore) {
		return PredicateResult{Decision: DecisionSkipNotBefore, Dwell: dwell}
	}
```

Also extend the `dwellAnchor` doc comment (lines 62-68) so the new classes are
covered — replace the second bullet with:

```go
//   - every other stuck class — including api-error and usage-limit — is anchored
//     on StatusChangedAt (when the banner / stuck state was entered). For
//     api-error that is deliberate: the banner is direct positive evidence, so a
//     session nobody ever sent anything to is still eligible.
```

## Tests

### Append to `internal/selfheal/predicate_test.go`

```go
// §6: api-error dwells on StatusChangedAt and is actionable with LastSentAt zero.
// A hand-driven root session nobody ever sent to must still be healable.
func TestEvaluate_APIError_DwellsOnStatusChangedAt_NoSendNeeded(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := Candidate{
		SessionID:       "s1",
		Substate:        tmux.SubstateAPIError,
		StatusChangedAt: now.Add(-90 * time.Second),
		LastSentAt:      time.Time{}, // never sent — must NOT disqualify
	}
	res := Evaluate(c, now)
	if !res.Candidate || res.Decision != DecisionAct {
		t.Fatalf("api-error with a 90s banner and no send must be a candidate, got %+v", res)
	}
}

// Below the 60s window it is not yet a candidate.
func TestEvaluate_APIError_UnderDwell_SkipsDwell(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := Candidate{
		SessionID:       "s1",
		Substate:        tmux.SubstateAPIError,
		StatusChangedAt: now.Add(-59 * time.Second),
	}
	if res := Evaluate(c, now); res.Candidate || res.Decision != DecisionSkipDwell {
		t.Fatalf("59s of banner is under the 60s dwell, got %+v", res)
	}
}

// §6: NotBefore in the future yields DecisionSkipNotBefore.
func TestEvaluate_NotBeforeInFuture_Skips(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := Candidate{
		SessionID:       "s1",
		Substate:        tmux.SubstateUsageLimit,
		StatusChangedAt: now.Add(-time.Minute),
		NotBefore:       now.Add(2 * time.Hour),
	}
	res := Evaluate(c, now)
	if res.Candidate {
		t.Fatal("a scheduled candidate must not act before NotBefore")
	}
	if res.Decision != DecisionSkipNotBefore {
		t.Fatalf("want %q, got %q", DecisionSkipNotBefore, res.Decision)
	}
}

// §6: NotBefore in the past does not block.
func TestEvaluate_NotBeforeInPast_DoesNotBlock(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := Candidate{
		SessionID:       "s1",
		Substate:        tmux.SubstateUsageLimit,
		StatusChangedAt: now.Add(-time.Minute),
		NotBefore:       now.Add(-time.Second),
	}
	if res := Evaluate(c, now); !res.Candidate || res.Decision != DecisionAct {
		t.Fatalf("a passed NotBefore must not block, got %+v", res)
	}
}

// A zero NotBefore is "no gate", not "block forever".
func TestEvaluate_ZeroNotBefore_NoGate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := Candidate{
		SessionID:       "s1",
		Substate:        tmux.SubstateUsageLimit,
		StatusChangedAt: now.Add(-time.Minute),
	}
	if res := Evaluate(c, now); !res.Candidate {
		t.Fatalf("zero NotBefore must not gate, got %+v", res)
	}
}

// usage-limit has dwell 0: it qualifies as soon as the anchor exists.
func TestEvaluate_UsageLimit_ZeroDwell(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := Candidate{
		SessionID:       "s1",
		Substate:        tmux.SubstateUsageLimit,
		StatusChangedAt: now,
	}
	if res := Evaluate(c, now); !res.Candidate {
		t.Fatalf("usage-limit dwells 0 and must qualify immediately, got %+v", res)
	}
}

// The authoritative disqualifiers still outrank the new gate: a busy session is
// never a candidate no matter what NotBefore says.
func TestEvaluate_BusyOutranksNotBefore(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	c := Candidate{
		SessionID:       "s1",
		Substate:        tmux.SubstateAPIError,
		StatusChangedAt: now.Add(-10 * time.Minute),
		Busy:            true,
	}
	if res := Evaluate(c, now); res.Decision != DecisionSkipBusy {
		t.Fatalf("busy must win, got %q", res.Decision)
	}
}
```

If `predicate_test.go` does not already import `time` and
`github.com/asheshgoplani/agent-deck/internal/tmux`, add them.

### Append to `internal/selfheal/policy_test.go`

```go
// The two new stuck classes are registered with the intended dwell windows.
func TestStuckSubstates_APIErrorAndUsageLimit(t *testing.T) {
	if !IsStuckSubstate(tmux.SubstateAPIError) {
		t.Fatal("api-error must be a self-heal-actionable stuck class")
	}
	if !IsStuckSubstate(tmux.SubstateUsageLimit) {
		t.Fatal("usage-limit must be a self-heal-actionable stuck class")
	}
	if d, _ := DwellThreshold(tmux.SubstateAPIError); d != 60*time.Second {
		t.Fatalf("api-error dwell: want 60s, got %s", d)
	}
	if d, ok := DwellThreshold(tmux.SubstateUsageLimit); !ok || d != 0 {
		t.Fatalf("usage-limit dwell: want 0/ok, got %s/%v", d, ok)
	}
}

// Both new classes resolve to the one executable action.
func TestWouldHaveAction_ResumeClasses(t *testing.T) {
	for _, s := range []tmux.Substate{tmux.SubstateAPIError, tmux.SubstateUsageLimit} {
		if got := WouldHaveAction(s); got != ActionResume {
			t.Fatalf("%q: want %q, got %q", s, ActionResume, got)
		}
	}
}

// ActionResend is untouched: it stays the idle-at-empty-prompt would_have and is
// never produced for the resume classes.
func TestWouldHaveAction_ResendUnchanged(t *testing.T) {
	if got := WouldHaveAction(tmux.SubstateIdleAtEmptyPrompt); got != ActionResend {
		t.Fatalf("idle-at-empty-prompt must still map to %q, got %q", ActionResend, got)
	}
}

// §6: caps still fire ahead of the action for a resume-class candidate. The
// per-session window is 2 in 6h for every non-auth class.
func TestGate_PerSessionCap_AppliesToAPIError(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	p := NewPolicyMachine(DefaultCaps())
	c := Candidate{SessionID: "s1", Substate: tmux.SubstateAPIError}
	for i := 0; i < 2; i++ {
		if d, _ := p.Gate(c, now); d != DecisionAct {
			t.Fatalf("attempt %d should be allowed, got %q", i, d)
		}
		p.RecordAttempt(c, now)
	}
	if d, _ := p.Gate(c, now); d != DecisionCapHit {
		t.Fatalf("third attempt in the 6h window must be %q, got %q", DecisionCapHit, d)
	}
}

// Flicker quarantine still fires ahead of the action.
func TestGate_Flicker_BlocksAPIError(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	p := NewPolicyMachine(DefaultCaps())
	p.SetFlickering("s1", true)
	c := Candidate{SessionID: "s1", Substate: tmux.SubstateAPIError}
	if d, st := p.Gate(c, now); d != DecisionBreakerOpen || !st.BreakerOpen {
		t.Fatalf("a flapping session must be quarantine-equivalent, got %q/%+v", d, st)
	}
}
```

If `policy_test.go` does not already import `time` and the tmux package, add them.

## Verification

```sh
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume
gofmt -l internal/selfheal/
```
Expected: **nothing** (empty).

```sh
go build ./... && go vet ./internal/selfheal/
```
Expected: no output, exit 0.

```sh
go test ./internal/selfheal/ -v
```
Expected: `ok  	github.com/asheshgoplani/agent-deck/internal/selfheal`. Every
pre-existing test still PASSes, plus the twelve new ones. The run-specific
sentinel is `TestEvaluate_NotBeforeInFuture_Skips` — it must appear as
`--- PASS`. `internal/selfheal` is a pure package with no filesystem or tmux
dependency, so there is no known sandbox flake here: any failure is yours.

```sh
go test ./internal/selfheal/ -run 'NotBefore|APIError|UsageLimit|Resume|Resend' -count=1 -v
```
Expected: all PASS, `ok` line.

## Commit

```sh
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume add internal/selfheal/
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume commit -m "feat(selfheal): add the resume action, mode and NotBefore schedule gate

Transport recovery is dwell-then-act; a usage limit is wait-until-T-then-act,
where T is hours away and no dwell window can express it. Candidate.NotBefore
carries that schedule and the predicate refuses to act before it, recording
skip_not_before so the audit shows a session waiting rather than one nobody
looked at.

api-error joins the actionable set at 60s anchored on StatusChangedAt, so a
session whose last prompt a human typed by hand is eligible; usage-limit joins at
dwell 0, gated entirely by NotBefore. Both map to the single new ActionResume.

ActionResend is untouched and stays unexecutable. Nothing here can act on its
own: ModeResume is defined but the engine chokepoint is a separate change."
```

## Interfaces

### consumes
- `internal/tmux`: `tmux.Substate`, `tmux.SubstateAPIError` (**from task 01**), `tmux.SubstateUsageLimit`, `tmux.SubstateAuth401`, `tmux.SubstateModelUnavailable`, `tmux.SubstateIdleAtEmptyPrompt`
- `internal/selfheal/selfheal.go`: `Mode`, `Action`, `Decision`, `ModeObserve`, `ModeSingleAction`, `ModeFull`, `ActionResend`, `ActionEscalate`, `stuckDwellThresholds`, `actionForSubstate`, `IsStuckSubstate`, `DwellThreshold`, `WouldHaveAction`
- `internal/selfheal/candidate.go`: `Candidate`, `PredicateResult`, `Evaluate(c Candidate, now time.Time) PredicateResult`, `(Candidate).dwellAnchor()`, `(Candidate).Dwell(now)`
- `internal/selfheal/policy.go`: `NewPolicyMachine(Caps)`, `DefaultCaps()`, `(*PolicyMachine).Gate(Candidate, time.Time) (Decision, CapsState)`, `(*PolicyMachine).RecordAttempt`, `(*PolicyMachine).SetFlickering`

### produces
- `internal/selfheal/selfheal.go`: `const ModeResume Mode = "resume"`
- `internal/selfheal/selfheal.go`: `const ActionResume Action = "resume"`
- `internal/selfheal/selfheal.go`: `const DecisionSkipNotBefore Decision = "skip_not_before"`
- `internal/selfheal/selfheal.go`: `stuckDwellThresholds[tmux.SubstateAPIError] = 60 * time.Second`; `stuckDwellThresholds[tmux.SubstateUsageLimit] = 0`
- `internal/selfheal/selfheal.go`: `actionForSubstate[tmux.SubstateAPIError] = ActionResume`; `actionForSubstate[tmux.SubstateUsageLimit] = ActionResume` — so `WouldHaveAction` returns `ActionResume` for both
- `internal/selfheal/candidate.go`: `Candidate.NotBefore time.Time` (zero = no gate)
- `internal/selfheal/candidate.go`: `Candidate.ComposerDraft bool` (read by task 03's engine downgrade; **this task only adds the field, it does not act on it**)
- `internal/selfheal/candidate.go`: `Evaluate` returns `PredicateResult{Decision: DecisionSkipNotBefore}` when `!c.NotBefore.IsZero() && now.Before(c.NotBefore)`

## Record (append-only)

### 2026-08-07 — implemented

- Files touched: `internal/selfheal/selfheal.go`, `internal/selfheal/candidate.go`,
  `internal/selfheal/predicate_test.go`, `internal/selfheal/policy_test.go`.
- Implemented exactly as written; no deviations.
- Precondition checked: `grep -n 'SubstateAPIError' internal/tmux/substate.go` →
  lines 41/61/156 (task 01 landed as `d07176c4`).
- Verification: `gofmt -l internal/selfheal/` → empty. `go build ./...` → exit 0.
  `go vet ./internal/selfheal/` → exit 0.
  `go test ./internal/selfheal/ -count=1 -v` → EXIT=0, `ok`, 48 `--- PASS`, 0 FAIL;
  sentinel `TestEvaluate_NotBeforeInFuture_Skips` PASS.
  `go test ./internal/selfheal/ -run 'NotBefore|APIError|UsageLimit|Resume|Resend'
  -count=1 -v` → all 12 new tests PASS, `ok`.
- No concerns.

### 2026-08-08 — amended by review round 1 (commit `225ff78f`)

The Record above said "implemented exactly as written; no deviations". That was
true when written and is no longer. Round 1 changed one thing this task
specified, and the spec text above was not amended with it:

- **AC 2 and Interfaces → produces** both specify
  `Candidate.ComposerDraft bool`. The shipped field is
  `Candidate.ComposerDraft func() bool` — a DEFERRED lookup, not a pre-read
  value.
- Why: as a bool, the daemon adapter had to resolve it while ASSEMBLING every
  candidate, which is a fresh `tmux capture-pane` (3s timeout) per wedged
  session per 1-3s poll, inside the daemon's serial instance loop — including
  for the reads that can only ever return `skip_dwell` / `skip_confirm` and
  never consult it. A transport outage wedges sessions in correlated batches, so
  that is the multi-second-freeze class this repo has hit before. Deferring it
  moves the cost to the one branch that actually reads it.
- What did NOT change: the field is still task 03's to act on, this task still
  only adds it, and every other AC in this task is unaffected. `NotBefore`,
  the dwell thresholds, `actionForSubstate` and `Evaluate` are all as written.

### 2026-08-08 — amended by review round 2 (commit `d51925e1`)

- No AC changed. The `Candidate` TYPE comment was corrected: it still claimed
  "the policy never reaches back into tmux or the DB; everything it needs to
  decide is in here", which stopped being true the moment `ComposerDraft` became
  a callback. It now states the accurate invariant — every field except
  `ComposerDraft` is a value read before evaluation, which is what keeps
  `Evaluate` pure, and `ComposerDraft` is resolved by the engine outside it.
