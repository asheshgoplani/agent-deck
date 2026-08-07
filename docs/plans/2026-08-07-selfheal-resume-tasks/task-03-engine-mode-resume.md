# Task 03 — engine authorization chokepoint: `(ModeResume × ActionResume)`

tier: strong
depends on: task 02 (needs `ModeResume`, `ActionResume`, `Candidate.ComposerDraft`)
parallel with: nothing
worktree: `/Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` (branch `feature/selfheal-auto-resume`)

Use absolute paths under that worktree for every Read/Edit/Write, and
`git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` for
every git command. Never run `git stash`, `git checkout`, `git switch`, or
`git reset`; never edit the root checkout at `/Users/doozyx/DoozyX/agent-deck`.

**Precondition to check first:**
```sh
grep -n 'ModeResume\|ActionResume\|ComposerDraft' internal/selfheal/selfheal.go internal/selfheal/candidate.go
```
must print at least three lines. If not, task 02 has not landed — stop and report BLOCKED.

---

## Design extracts (verbatim from the approved design)

> ### 1.3 What already exists
>
> It ships disabled (`[selfheal] enabled` defaults false) and, when enabled, is
> Stage 1 observe-only: `ActionExecutor` has **zero implementations**, and
> `ModeSingleAction` / `ModeFull` return `ErrActionInGuardedMode` pending the
> owner's re-approval.
>
> This design supplies the first executor and one narrow mode to authorise it.

> ### D5 — One action, one executor
>
> ```go
> ActionResume Action = "resume"   // params: {"reason": "transport" | "usage_limit"}
> ```
>
> The executor is the first real `ActionExecutor`. It **calls the existing verified
> send path that backs `session nudge`** (`sendWithRetryTarget`,
> `cmd/agent-deck/session_cmd.go`) — preconditions, delivery verification, and the
> Escape+Enter escalation that is the only thing that ungates a wedged composer. It
> does not reimplement send. It records the real `delivery` value (`submitted` /
> `typed_not_submitted` / `no_evidence`) into the audit event, so a resume that was
> typed but never submitted is visible as a failure rather than a success.

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
>
> The PR therefore asks the owner to approve one narrow new path, not to reopen a
> gate they closed pending unfixed gaps.

> ## 3. Architecture
>
> Inherited unchanged: two-read confirm, per-session cap (2/6h), global cap
> (5/hour), circuit breaker, flicker quarantine, opt-out, NDJSON audit.

> ## 6. Verification
>
> **Policy (unit).** … A drafted composer yields `ActionEscalate`. Caps, breaker
> and flicker gates still fire ahead of the action.
>
> **Engine (unit).** `(ModeResume, ActionResume)` executes. Every other
> (mode, action) pair returns `ErrActionInGuardedMode`. `ModeObserve` still
> constructs no executor.

---

## Design decisions this task makes (and why)

The design says "every other pair keeps returning `ErrActionInGuardedMode`", but
`executeIfAuthorized` today returns only `(string, Action)` — the sentinel error
is never actually surfaced, the outcome string `"held_stage_2_3"` stands in for
it. §6 asks for a test that asserts the error, so the signature must widen to
`(string, Action, error)`. The outcome string is kept byte-identical for the two
pre-existing guarded modes so nothing reading the audit log has to change.

`policy.RecordOutcome` is currently never called from `ProcessRead` (there was no
outcome to record, Stage 1 having no executor). Now there is one, so it is wired:
a resume whose delivery is not `submitted` counts as a failed recovery and, at
K=2 consecutive, opens the breaker. This is the "circuit breaker inherited
unchanged" clause actually taking effect rather than staying dormant.

## Acceptance criteria

1. `NewResumeEngine(caps Caps, sink EventSink, exec ActionExecutor) *Engine` exists.
2. `executeIfAuthorized(c Candidate, would Action) (string, Action, error)`:
   - `(ModeResume, ActionResume)` with a non-nil executor → calls it.
   - every other `(mode, action)` pair → `(_, ActionNone, ErrActionInGuardedMode)`,
     and the executor is **not** called.
3. `ModeObserve` still returns before ever reaching the chokepoint, and
   `NewObserveEngine` still builds an engine with `exec == nil`.
4. `c.ComposerDraft` downgrades `ActionResume` to `ActionEscalate` before the
   mode check, so a drafted composer can never execute.
5. `actionParams` returns `{"reason": "transport"}` for `SubstateAPIError` and
   `{"reason": "usage_limit"}` for `SubstateUsageLimit`.
6. `Event.Outcome` carries the executor's outcome string verbatim.
7. The pre-existing `TestExecuteIfAuthorized_GuardedModes_Refuse` compiles and
   passes against the new 3-value signature.
8. `go test ./internal/selfheal/ -v` fully green.

## Edits — `internal/selfheal/engine.go`

### 1. New constructor

Add immediately after `NewObserveEngine` (which ends at line 99):

```go
// NewResumeEngine builds the ONE acting engine: mode "resume", authorised for
// exactly the pair (ModeResume, ActionResume) and nothing else. Every other
// action — including ActionResend and the two restart actions — still refuses at
// the chokepoint, so approving this mode approves one narrow path rather than
// reopening single_action / full.
//
// exec must be non-nil; an acting engine with no executor is a mis-wire, and the
// chokepoint reports it as such rather than silently doing nothing.
func NewResumeEngine(caps Caps, sink EventSink, exec ActionExecutor) *Engine {
	return &Engine{
		mode:         ModeResume,
		caps:         caps,
		policy:       NewPolicyMachine(caps),
		sink:         sink,
		exec:         exec,
		prevSig:      map[string]string{},
		confirmed:    map[string]confirmState{},
		substateSeen: map[string]substateEntry{},
	}
}
```

### 2. The composer downgrade + the execute call in `ProcessRead`

Replace the block from `would := WouldHaveAction(c.Substate)` (line 214) to the
end of `ProcessRead` (line 233) with:

```go
	would := WouldHaveAction(c.Substate)

	// D6: an operator draft in the composer is a HARD precondition, checked
	// before authority. Submitting someone else's text is not a decision a status
	// probe gets to make — and the force-send path is known to CONSUME the draft
	// rather than restore it (2026-08-07, conductor2-testfix). Downgrading to
	// escalate here means the audit records a session that needs a human, and no
	// mode can execute past it.
	if would == ActionResume && c.ComposerDraft {
		would = ActionEscalate
	}

	ev.Decision = DecisionAct
	ev.WouldHave = would
	ev.ActionParams = actionParams(c.Substate)

	// OBSERVE: log would_have and STOP. No executor, no action. This is the whole
	// point of Stage 1.
	if e.mode == ModeObserve {
		ev.Outcome = "observe_noop"
		_ = e.sink.Append(ev)
		return ev
	}

	// Every non-observe mode goes through the chokepoint. Only (resume, resume)
	// is authorised; single_action and full stay HELD.
	outcome, action, err := e.executeIfAuthorized(c, would)
	ev.Action = action
	ev.Outcome = outcome
	// Feed the circuit breaker only when a real action ran. A refusal is not a
	// failed recovery — counting it would open the breaker on sessions self-heal
	// never touched.
	if action != ActionNone {
		e.policy.RecordOutcome(c, err == nil && outcomeIsDelivered(outcome))
	}
	_ = e.sink.Append(ev)
	return ev
}

// outcomeDeliveredPrefix marks an executor outcome that reached the agent as an
// accepted turn. The executor formats its outcome as "resumed:<delivery>", where
// delivery is the `session send` contract value — and ONLY "submitted" means the
// agent took the message up. "typed_not_submitted" in particular means the bytes
// are sitting in a composer that is not accepting Enter, which is a failure.
const outcomeDeliveredPrefix = "resumed:submitted"

// outcomeIsDelivered reports whether an executor outcome represents a genuinely
// accepted turn, for the circuit breaker's consecutive-failure count.
func outcomeIsDelivered(outcome string) bool {
	return outcome == outcomeDeliveredPrefix
}
```

### 3. The chokepoint

Replace `executeIfAuthorized` (lines 265-284) with:

```go
// executeIfAuthorized is the single chokepoint where a real action can run. It
// authorises EXACTLY ONE pair — (ModeResume, ActionResume) — and refuses
// everything else with ErrActionInGuardedMode: observe never gets here, and
// single_action / full stay HELD pending re-approval + the three §9 gap-fixes.
//
// The pair check is deliberately a conjunction rather than two nested guards: a
// future mode that forgets to narrow its action set, or a substate that starts
// mapping to a restart, both land on the refusal instead of on a live restart.
//
// Returns the outcome string, the action actually taken (ActionNone when
// refused), and the refusal/execution error.
func (e *Engine) executeIfAuthorized(c Candidate, would Action) (string, Action, error) {
	if e.mode != ModeResume || would != ActionResume {
		// Byte-identical to the pre-resume outcome string so anything reading the
		// audit history for held records keeps matching.
		return "held_stage_2_3", ActionNone, ErrActionInGuardedMode
	}
	if e.exec == nil {
		// An acting engine with no executor is a mis-wire, not a quiet no-op.
		return "no_executor", ActionNone, nil
	}
	outcome, err := e.exec.Execute(c, would)
	if err != nil {
		return "error:" + err.Error(), ActionNone, err
	}
	return outcome, would, nil
}
```

### 4. `actionParams`

Replace `actionParams` (lines 286-296) with:

```go
// actionParams records the would-be action's parameters for the audit (§5).
func actionParams(s tmux.Substate) map[string]any {
	switch s {
	case tmux.SubstateModelUnavailable:
		return map[string]any{"model": "opus", "reissue": true}
	case tmux.SubstateAuth401:
		return map[string]any{"reassert_creds": true}
	case tmux.SubstateAPIError:
		// The reason selects the executor's prompt text. A transport resume just
		// asks the turn to continue.
		return map[string]any{"reason": "transport"}
	case tmux.SubstateUsageLimit:
		// The usage_limit prompt additionally warns that a SUBAGENT may have been
		// terminated by the limit and needs re-dispatching — resuming the parent
		// does not restore a child's work.
		return map[string]any{"reason": "usage_limit"}
	default:
		return nil
	}
}
```

### 5. Update the `ActionExecutor` doc comment

Replace the comment block on `ActionExecutor` (lines 17-26) with:

```go
// ActionExecutor performs a real recovery. It is reachable ONLY through
// Engine.executeIfAuthorized, and only for the pair (ModeResume, ActionResume) —
// observe holds a nil executor and every other mode/action pair is refused with
// ErrActionInGuardedMode.
type ActionExecutor interface {
	// Execute applies the action to the candidate and reports the immediate
	// outcome string. The outcome MUST carry the real delivery verdict (the
	// caller records it verbatim in the audit event), so a resume that was typed
	// but never submitted is visible as a failure rather than a success.
	Execute(c Candidate, a Action) (outcome string, err error)
}
```

Also update `ErrActionInGuardedMode`'s comment (lines 11-15):

```go
// ErrActionInGuardedMode is returned by the execution chokepoint for every
// (mode, action) pair except (ModeResume, ActionResume). single_action / full
// are HELD pending re-approval + the three §9 gap-fixes; observe never reaches
// the chokepoint at all.
var ErrActionInGuardedMode = errors.New("selfheal: actions are HELD (Stages 2-3 not re-approved)")
```

## Tests — `internal/selfheal/engine_test.go`

### 1. Fix the existing test for the widened signature

Replace `TestExecuteIfAuthorized_GuardedModes_Refuse` (lines 175-191) with:

```go
// The chokepoint refuses even when a guarded mode somehow has an executor: Stages
// 2-3 are HELD. This guards against a future mis-wire shipping actions early.
func TestExecuteIfAuthorized_GuardedModes_Refuse(t *testing.T) {
	c := Candidate{SessionID: "s1", Substate: tmux.SubstateModelUnavailable}
	for _, m := range []Mode{ModeSingleAction, ModeFull} {
		spy := &spyExecutor{}
		e := &Engine{mode: m, caps: DefaultCaps(), policy: NewPolicyMachine(DefaultCaps()), sink: &MemorySink{}, exec: spy, prevSig: map[string]string{}, confirmed: map[string]confirmState{}, substateSeen: map[string]substateEntry{}}
		outcome, action, err := e.executeIfAuthorized(c, ActionRestartModelSwitch)
		if action != ActionNone {
			t.Fatalf("mode %q: HELD modes must take no action, got %q", m, action)
		}
		if outcome != "held_stage_2_3" {
			t.Fatalf("mode %q: want held_stage_2_3 outcome, got %q", m, outcome)
		}
		if !errors.Is(err, ErrActionInGuardedMode) {
			t.Fatalf("mode %q: want ErrActionInGuardedMode, got %v", m, err)
		}
		if len(spy.calls) != 0 {
			t.Fatalf("mode %q: executor must NOT be called (Stages 2-3 HELD), got %d calls", m, len(spy.calls))
		}
	}
}
```

Add `"errors"` to the file's import block if it is not already there.

### 2. New tests — append to `engine_test.go`

```go
// resumeSpy records what the executor was asked to do and returns a canned
// outcome, so the chokepoint can be tested without any tmux or send path.
type resumeSpy struct {
	calls   []Action
	outcome string
	err     error
}

func (s *resumeSpy) Execute(c Candidate, a Action) (string, error) {
	s.calls = append(s.calls, a)
	return s.outcome, s.err
}

func resumeEngineFor(exec ActionExecutor, sink EventSink) *Engine {
	return NewResumeEngine(DefaultCaps(), sink, exec)
}

// A dwelled api-error candidate, confirmed over two reads.
func dwelledAPIErrorCand(now time.Time) Candidate {
	return Candidate{
		SessionID:       "s1",
		Title:           "worker-3",
		Substate:        tmux.SubstateAPIError,
		StatusChangedAt: now.Add(-10 * time.Minute),
		OutputSig:       "sig-stable",
	}
}

// §6: (ModeResume, ActionResume) executes.
func TestExecuteIfAuthorized_ResumePair_Executes(t *testing.T) {
	spy := &resumeSpy{outcome: "resumed:submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	outcome, action, err := e.executeIfAuthorized(Candidate{SessionID: "s1", Substate: tmux.SubstateAPIError}, ActionResume)
	if err != nil {
		t.Fatalf("the authorised pair must not error, got %v", err)
	}
	if action != ActionResume {
		t.Fatalf("want %q, got %q", ActionResume, action)
	}
	if outcome != "resumed:submitted" {
		t.Fatalf("outcome must be the executor's verbatim, got %q", outcome)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("executor must be called exactly once, got %d", len(spy.calls))
	}
}

// §6: every other (mode, action) pair returns ErrActionInGuardedMode. This is the
// exhaustive matrix — the whole point of the chokepoint.
func TestExecuteIfAuthorized_EveryOtherPair_Refuses(t *testing.T) {
	modes := []Mode{ModeObserve, ModeSingleAction, ModeFull, ModeResume}
	actions := []Action{ActionRestartModelSwitch, ActionRestart, ActionResend, ActionRestartReassertCreds, ActionEscalate, ActionResume, ActionNone}
	for _, m := range modes {
		for _, a := range actions {
			if m == ModeResume && a == ActionResume {
				continue // the one authorised pair
			}
			spy := &resumeSpy{outcome: "resumed:submitted"}
			e := &Engine{mode: m, caps: DefaultCaps(), policy: NewPolicyMachine(DefaultCaps()), sink: &MemorySink{}, exec: spy, prevSig: map[string]string{}, confirmed: map[string]confirmState{}, substateSeen: map[string]substateEntry{}}
			_, action, err := e.executeIfAuthorized(Candidate{SessionID: "s1"}, a)
			if !errors.Is(err, ErrActionInGuardedMode) {
				t.Fatalf("(%q, %q): want ErrActionInGuardedMode, got %v", m, a, err)
			}
			if action != ActionNone {
				t.Fatalf("(%q, %q): must take no action, got %q", m, a, action)
			}
			if len(spy.calls) != 0 {
				t.Fatalf("(%q, %q): executor must not be called", m, a)
			}
		}
	}
}

// §6: ModeObserve still constructs no executor.
func TestNewObserveEngine_StillHasNoExecutor(t *testing.T) {
	e := NewObserveEngine(DefaultCaps(), &MemorySink{})
	if e.exec != nil {
		t.Fatal("the observe engine must hold no executor — that is the structural guarantee")
	}
	if e.Mode() != ModeObserve {
		t.Fatalf("want %q, got %q", ModeObserve, e.Mode())
	}
}

// End-to-end through ProcessRead: two confirming reads, then exactly one execution.
func TestProcessRead_ResumeMode_ExecutesOncePerConfirmedCandidate(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:submitted"}
	sink := &MemorySink{}
	e := resumeEngineFor(spy, sink)
	c := dwelledAPIErrorCand(now)

	first := e.ProcessRead(c, now)
	if first.Decision != DecisionSkipConfirm {
		t.Fatalf("the first read must only record the confirm, got %q", first.Decision)
	}
	if len(spy.calls) != 0 {
		t.Fatal("one read is never enough — no execution on the first read")
	}

	second := e.ProcessRead(c, now.Add(2*time.Second))
	if second.Decision != DecisionAct {
		t.Fatalf("the second confirming read must act, got %q", second.Decision)
	}
	if second.Action != ActionResume {
		t.Fatalf("want action %q, got %q", ActionResume, second.Action)
	}
	if second.Outcome != "resumed:submitted" {
		t.Fatalf("the audit must carry the real delivery, got %q", second.Outcome)
	}
	if got := second.ActionParams["reason"]; got != "transport" {
		t.Fatalf("api-error params reason: want transport, got %v", got)
	}
	if len(spy.calls) != 1 {
		t.Fatalf("exactly one delivery per confirmed candidate, got %d", len(spy.calls))
	}
}

// §6 (policy bullet): a drafted composer yields ActionEscalate and never executes.
func TestProcessRead_DraftedComposer_EscalatesWithoutActing(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	c := dwelledAPIErrorCand(now)
	c.ComposerDraft = true

	e.ProcessRead(c, now)
	ev := e.ProcessRead(c, now.Add(2*time.Second))
	if ev.WouldHave != ActionEscalate {
		t.Fatalf("a drafted composer must downgrade to %q, got %q", ActionEscalate, ev.WouldHave)
	}
	if ev.Action != ActionNone {
		t.Fatalf("a drafted composer must take no action, got %q", ev.Action)
	}
	if len(spy.calls) != 0 {
		t.Fatal("autonomous code must never submit text a human typed")
	}
}

// A future NotBefore keeps a usage-limit candidate out of the executor entirely.
func TestProcessRead_UsageLimit_NotBefore_DoesNotExecute(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	c := Candidate{
		SessionID:       "s1",
		Substate:        tmux.SubstateUsageLimit,
		StatusChangedAt: now.Add(-time.Minute),
		NotBefore:       now.Add(2 * time.Hour),
		OutputSig:       "sig-stable",
	}
	for i := 0; i < 4; i++ {
		ev := e.ProcessRead(c, now.Add(time.Duration(i)*2*time.Second))
		if ev.Decision != DecisionSkipNotBefore {
			t.Fatalf("read %d: want %q, got %q", i, DecisionSkipNotBefore, ev.Decision)
		}
	}
	if len(spy.calls) != 0 {
		t.Fatalf("a scheduled candidate must not be executed early, got %d calls", len(spy.calls))
	}
}

// usage-limit carries the reason the executor needs to warn about a terminated
// subagent.
func TestProcessRead_UsageLimit_PastNotBefore_ReasonIsUsageLimit(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	c := Candidate{
		SessionID:       "s1",
		Substate:        tmux.SubstateUsageLimit,
		StatusChangedAt: now.Add(-time.Minute),
		NotBefore:       now.Add(-time.Second),
		OutputSig:       "sig-stable",
	}
	e.ProcessRead(c, now)
	ev := e.ProcessRead(c, now.Add(2*time.Second))
	if ev.Action != ActionResume {
		t.Fatalf("want %q, got %q", ActionResume, ev.Action)
	}
	if got := ev.ActionParams["reason"]; got != "usage_limit" {
		t.Fatalf("want reason usage_limit, got %v", got)
	}
}

// A typed-but-unsubmitted delivery is recorded as a FAILURE, not a success, and
// feeds the circuit breaker.
func TestProcessRead_TypedNotSubmitted_CountsAsFailedRecovery(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{outcome: "resumed:typed_not_submitted"}
	e := resumeEngineFor(spy, &MemorySink{})
	c := dwelledAPIErrorCand(now)

	e.ProcessRead(c, now)
	ev := e.ProcessRead(c, now.Add(2*time.Second))
	if ev.Outcome != "resumed:typed_not_submitted" {
		t.Fatalf("the audit must carry the real delivery, got %q", ev.Outcome)
	}
	// K for a non-auth class is 2: one more failed recovery opens the breaker.
	e.ProcessRead(c, now.Add(4*time.Second))
	e.ProcessRead(c, now.Add(6*time.Second))
	if !e.Policy().IsQuarantined("s1") {
		t.Fatal("two consecutive undelivered resumes must open the breaker")
	}
}

// An executor error is surfaced in the audit and takes no action.
func TestProcessRead_ExecutorError_RecordedNoAction(t *testing.T) {
	now := time.Unix(1780000000, 0).UTC()
	spy := &resumeSpy{err: errors.New("pane is gone")}
	e := resumeEngineFor(spy, &MemorySink{})
	c := dwelledAPIErrorCand(now)

	e.ProcessRead(c, now)
	ev := e.ProcessRead(c, now.Add(2*time.Second))
	if ev.Action != ActionNone {
		t.Fatalf("a failed execution takes no action, got %q", ev.Action)
	}
	if ev.Outcome != "error:pane is gone" {
		t.Fatalf("want the error surfaced in the outcome, got %q", ev.Outcome)
	}
}
```

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
go test ./internal/selfheal/ -count=1 -v
```
Expected: `ok  	github.com/asheshgoplani/agent-deck/internal/selfheal`, every test
PASS including every pre-existing one. Run-specific sentinel:
`TestExecuteIfAuthorized_EveryOtherPair_Refuses` must appear as `--- PASS` — it
is the exhaustive 27-pair matrix and its absence means the file did not compile in.

```sh
go test ./internal/selfheal/ -run 'ExecuteIfAuthorized|ProcessRead_Resume|DraftedComposer|NotBefore|TypedNotSubmitted|ObserveEngine_StillHasNoExecutor' -count=1 -v
```
Expected: all PASS.

Structural check that observe mode is still unable to act:
```sh
grep -c 'ModeObserve' internal/selfheal/engine.go
```
Expected: `4` or more (constructor, ProcessRead early return, chokepoint comment,
doc comments). If it drops to 0 the observe guarantee was deleted.

## Commit

```sh
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume add internal/selfheal/
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume commit -m "feat(selfheal): authorise exactly (resume mode x resume action) at the chokepoint

The engine gains its first executable path. executeIfAuthorized now authorises a
single pair and refuses all 27 others with ErrActionInGuardedMode, which the
signature finally surfaces instead of standing in for it with an outcome string.
The held outcome string is byte-identical so existing audit history still matches.

An operator draft in the composer downgrades resume to escalate before the mode
check, so no mode can submit text a human typed. The delivery verdict the
executor reports is recorded verbatim in the audit and feeds the circuit breaker:
a resume typed into a gated composer is a failed recovery, not a success.

observe still constructs no executor and still returns before the chokepoint;
single_action and full are untouched and stay HELD."
```

## Interfaces

### consumes
- `internal/selfheal/selfheal.go`: `Mode`, `ModeObserve`, `ModeSingleAction`, `ModeFull`, `ModeResume` (**task 02**), `Action`, `ActionNone`, `ActionResume` (**task 02**), `ActionResend`, `ActionEscalate`, `ActionRestart`, `ActionRestartModelSwitch`, `ActionRestartReassertCreds`, `Decision`, `DecisionAct`, `DecisionSkipConfirm`, `DecisionSkipNotBefore` (**task 02**), `WouldHaveAction(tmux.Substate) Action`
- `internal/selfheal/candidate.go`: `Candidate` incl. `NotBefore` and `ComposerDraft` (**task 02**), `Evaluate`, `PredicateResult`
- `internal/selfheal/policy.go`: `Caps`, `DefaultCaps()`, `NewPolicyMachine`, `(*PolicyMachine).Gate`, `(*PolicyMachine).RecordAttempt`, `(*PolicyMachine).RecordOutcome(c Candidate, healthy bool)`, `(*PolicyMachine).IsQuarantined`
- `internal/selfheal/event.go`: `Event`, `EventSink`, `MemorySink`, `ReadSig`, `formatTS`
- `internal/tmux`: `tmux.SubstateAPIError` (**task 01**), `tmux.SubstateUsageLimit`, `tmux.SubstateAuth401`, `tmux.SubstateModelUnavailable`

### produces
- `internal/selfheal/engine.go`: `func NewResumeEngine(caps Caps, sink EventSink, exec ActionExecutor) *Engine`
- `internal/selfheal/engine.go`: **changed signature** `func (e *Engine) executeIfAuthorized(c Candidate, would Action) (string, Action, error)`
- `internal/selfheal/engine.go`: `const outcomeDeliveredPrefix = "resumed:submitted"` and `func outcomeIsDelivered(outcome string) bool` — **the executor in task 05 MUST format its outcome as `"resumed:" + delivery`**, where `delivery` is the `session send` contract value (`submitted` / `typed` / `typed_not_submitted` / `unverified` / `no_evidence` / `line_too_long` / `send_failed`). Only `"resumed:submitted"` counts as a healthy recovery.
- `internal/selfheal/engine.go`: `actionParams(tmux.SubstateAPIError) == map[string]any{"reason": "transport"}`; `actionParams(tmux.SubstateUsageLimit) == map[string]any{"reason": "usage_limit"}` — **the executor in task 05 reads `c.Substate`, not these params, to pick its prompt**; the params exist for the audit record.
- `internal/selfheal/engine.go`: `ActionExecutor` interface unchanged in shape — `Execute(c Candidate, a Action) (outcome string, err error)`

## Record (append-only)
