# Self-heal auto-resume: transport errors and usage limits

Status: approved 2026-08-07 · implemented on `feature/selfheal-auto-resume`
Implementation plan: `docs/plans/2026-08-07-selfheal-resume-plan.md`
Scope: `internal/tmux`, `internal/selfheal`, `internal/session`

## 1. Motivation

Two failure modes end a Claude turn and leave the session sitting there
indefinitely. Both are recoverable by delivering a single continuation prompt.
Neither is recognised by anything in agent-deck today.

### 1.1 Transport error

Field evidence, 2026-08-07: a DNS failure wedged 3 of 32 live sessions for 16,
18 and 39 minutes. Each pane ended on:

```
⏺ API Error: Unable to connect to API (ENOTFOUND)
✻ Sautéed for 39m 27s
```

The panes were not frozen — they repainted and accepted keystrokes. The network
recovered long before anyone noticed. A single continuation prompt resumed all
three on the first attempt (`delivery: submitted`, 3/3).

`internal/tmux/detector.go` matches `API Error: 401`, `API Error (401`,
`Please run /login` and `socket connection closed`. A transport error matches
none of them, so all three sessions classified as `idle-at-empty-prompt`.

### 1.2 Usage limit

Field evidence, same day, from the operator's transcripts:

```json
{"type":"assistant","isSidechain":false,
 "content":[{"type":"text","text":"You've hit your session limit · resets 6:10pm (Europe/Skopje)"}],
 "error":"rate_limit","apiErrorStatus":429,"timestamp":"2026-08-07T14:23:13.812Z"}
```

This is already detected. `internal/session/usagelimit.go` keys on exactly the
`apiErrorStatus: 429` + `error: "rate_limit"` pair, and `isSidechain: false`
places the record in the main conversation where the backward scanner looks. The
substate `usage-limit` is published today and **has no consumer** —
`internal/selfheal/selfheal.go` records that `usage_limit` is deliberately absent
from the actionable set.

Note the shape: `Agent terminated early due to an API error: …` means a
*subagent* hit the limit. Resuming the parent does not restore the subagent's
work; the parent must re-dispatch it.

### 1.3 What already exists

`internal/selfheal/` is a complete supervision engine: stuck predicate,
cause-specific dwell windows, two-read confirm, per-session and global caps,
circuit breaker, flicker quarantine, NDJSON audit sink, per-session and
per-group opt-out. `internal/session/selfheal_pass.go` drives it from the
transition daemon's existing poll loop.

It ships disabled (`[selfheal] enabled` defaults false) and, when enabled, is
Stage 1 observe-only: `ActionExecutor` has **zero implementations**, and
`ModeSingleAction` / `ModeFull` return `ErrActionInGuardedMode` pending the
owner's re-approval.

This design supplies the first executor and one narrow mode to authorise it.

## 2. Decisions

### D1 — New substate `api-error`

Add `SubstateAPIError = "api-error"` to `internal/tmux/substate.go`, classified
from the rendered banner. Markers: `Unable to connect to API`, `ENOTFOUND`,
`ECONNREFUSED`, `ConnectionRefused`.

Refactor `hasClaudeErrorBanner` to take a marker set, and drive both the
existing `auth-401` check and the new one through it. This is load-bearing, not
tidiness — the real banner renders on a `⏺` assistant line, which is precisely
the line class `claudeAssistantLinePrefix` adjudicates, and a conductor quoting a
child's banner via `session output` renders behind `⎿`, which
`claudeQuotedLinePrefixes` already excludes. A copy-pasted matcher would lose
both guards.

**Ordering in `ClassifySubstate`: after the busy check, before
`model-unavailable`.**

```
1. auth-401            (terminal credential failure — outranks a stale busy cue)
2. busy indicator      → running
3. api-error           (NEW)
4. model-unavailable
5. idle-at-empty-prompt
6. none
```

`auth-401` sits ahead of busy because a credential failure is terminal. A
transport error is not: the session may already have recovered, in which case a
live spinner is the truth. The cost of a false `api-error` is injecting a prompt
into a working session, and that asymmetry sets the ordering. The check is
scoped to the recent tail (same as `hasModelUnavailableNoop`) so a banner
scrolled up into history stops matching.

### D2 — Dwell 60s, anchored on `StatusChangedAt`

```go
stuckDwellThresholds[SubstateAPIError] = 60 * time.Second
```

Anchored on `StatusChangedAt`, **not** `LastSentAt`. The `LastSentAt` anchor is
correct for `idle-at-empty-prompt`, where the only evidence of stuckness is *we
sent something and nothing happened*; a session nobody sent anything to is
legitimately idle. Here the banner is direct positive evidence, so a session
whose last prompt a human typed by hand is equally eligible. Without this, every
hand-driven root session stays unhealed.

### D3 — `NotBefore` gate, for `usage-limit`

Transport recovery is *dwell, then act*. A usage limit is *wait until T, then
act*, where T is hours away. Dwell cannot express that.

Add to `Candidate`:

```go
// NotBefore blocks action until a known-future moment. Zero means no gate.
NotBefore time.Time
```

and one decision value, `DecisionSkipNotBefore`. The predicate refuses to act
while `now < NotBefore`.

`usage-limit` therefore enters the actionable set with dwell 0 and a `NotBefore`
supplied by the caller.

### D4 — Parse to schedule, probe to confirm

`usagelimit.go` states the constraint directly: *"prefer a revalidation signal
over parsing the prose"*, deferred until *"a consumer starts acting on the
verdict"*. This design is that consumer.

Blind probing alone is structurally unworkable: the per-session cap is 2 per 6
hours, so a session would get two attempts across a five-hour window and then
sit until the cap rolled. The caps force a scheduled wake.

So the reset string sets *when to try*, and the observed outcome remains the
authority:

```
record 2026-08-07T14:23:13Z + "resets 6:10pm (Europe/Skopje)"
  → NotBefore = 16:10Z          (next occurrence of that wall time in that zone,
                                 strictly after the record timestamp)
  → at 16:10Z: send the resume prompt
       turn completes           → healed, substate clears itself
       fresh 429 record appears → NotBefore += 20m, rearm
```

Correctness never depends on the parse. If the string is absent, unparseable, or
the wording drifts, `NotBefore` falls back to `record.timestamp + 20m` and the
session recovers by retry — slower, never wrong. The zone is an IANA name
(`Europe/Skopje`), so `time.LoadLocation` resolves it; a zone that fails to load
takes the same fallback.

`latestAssistantTurnIsRateLimited` currently returns a bare bool and discards
both the text and the record timestamp. It must return them so the caller can
compute `NotBefore`.

**Secondary benefit:** a parsed reset also bounds belief better than
`usageLimitMaxAge`. That constant is documented as clearing early for
longer-than-5h (weekly) limits, reporting such a session as unremarkable. With a
parsed reset in hand, belief can extend to it, closing the case where a weekly
limit would never be resumed.

### D5 — One action, one executor

```go
ActionResume Action = "resume"   // params: {"reason": "transport" | "usage_limit"}
```

Not `ActionResend` — that name means "replay the last intent", which can redo
work when a turn partially completed and has nothing to replay when
`LastSentAt` is zero. Not two actions either: both triggers deliver one
continuation prompt through one send path, and splitting them would duplicate
the executor.

`ActionResend` itself is untouched: it remains the observe-mode `would_have` for
`idle-at-empty-prompt` and stays unexecutable.

The executor is the first real `ActionExecutor`. It **calls the existing verified
send path that backs `session nudge`** (`sendWithRetryTarget`,
`cmd/agent-deck/session_cmd.go`) — preconditions, delivery verification, and the
Escape+Enter escalation that is the only thing that ungates a wedged composer. It
does not reimplement send. It records the real `delivery` value (`submitted` /
`typed_not_submitted` / `no_evidence`) into the audit event, so a resume that was
typed but never submitted is visible as a failure rather than a success.

Prompt text differs by reason. The `usage_limit` prompt must state that a
subagent may have been terminated by the limit and needs re-dispatching —
otherwise a parent silently loses a child's work.

### D6 — Empty composer is a precondition

If the composer holds a draft, the verdict downgrades to `ActionEscalate` and
self-heal does not act.

`stall.go` states the reason — *"submitting someone else's text is not a
decision a status probe gets to make"* — and it was confirmed the hard way on
2026-08-07: recovering `conductor2-testfix` required `session nudge --force`,
and the force path **consumed** the operator's draft (`target release-6.18.1`)
rather than restoring it. Autonomous code may not do that to text a human typed.

### D7 — New mode `resume`, off by default

```go
ModeResume Mode = "resume"
```

`executeIfAuthorized` permits execution only for the pair
(`ModeResume`, `ActionResume`). Every other mode and every other action keeps
returning `ErrActionInGuardedMode`. `ModeSingleAction` and `ModeFull` are not
touched.

The PR therefore asks the owner to approve one narrow new path, not to reopen a
gate they closed pending unfixed gaps.

### D8 — No new timer

The transition daemon already polls every 1–3s (`notifyPollFast` = 1s,
`notifyPollMedium` = 2s, `notifyPollSlow` = 3s,
`internal/session/transition_daemon.go:16`), and `selfheal_pass.go` carries an
explicit constraint: *"F3: no new watchdog layer — the existing poll loop drives
it"*. The 60s dwell and the `NotBefore` gate supply the patience. No cron, no
goroutine, no launchd unit.

## 3. Architecture

```
internal/tmux/detector.go     hasClaudeErrorBanner(content, markers)   ← refactor
                              apiErrorBannerSubstrings                 ← new
internal/tmux/substate.go     SubstateAPIError                         ← new
                              ClassifySubstate: 401 → busy → api-error → …

internal/selfheal/selfheal.go stuckDwellThresholds[api-error]  = 60s   ← new
                              stuckDwellThresholds[usage-limit] = 0    ← new
                              actionForSubstate[both] = ActionResume   ← new
                              ModeResume                               ← new
internal/selfheal/candidate.go NotBefore                               ← new
internal/selfheal/policy.go   DecisionSkipNotBefore                    ← new
internal/selfheal/engine.go   executeIfAuthorized: allow (ModeResume × ActionResume)

internal/session/usagelimit.go latestAssistantTurnIsRateLimited returns
                               (text, recordTS) so NotBefore is computable
internal/session/selfheal_pass.go  wire executor; empty-composer precondition;
                                   populate NotBefore
internal/session/<new>.go          ActionExecutor impl → sendWithRetryTarget
```

Inherited unchanged: two-read confirm, per-session cap (2/6h), global cap
(5/hour), circuit breaker, flicker quarantine, opt-out, NDJSON audit.

## 4. Configuration

```toml
[selfheal]
enabled = true
mode    = "resume"
global_per_hour = 30
```

No new dial. `global_per_hour` already exists, but its default of 5 is wrong for
this workload: a transport outage is correlated and wedges every session at once,
so 5 would heal 5 of 30. The cap was sized for restarts; a resume is a single
delivered message and is far cheaper. 30 is the recommended operator setting, not
a new default — the shipped default stays 5.

## 5. Out of scope

- **Wiring `SubstateStalled` in.** Its 10-minute dwell requires text already
  sitting in the composer, which D6 refuses to submit. Wiring it in would produce
  a detector that can only ever escalate. The drafted-composer wedge stays manual.
- **`model-unavailable` and `auth-401` recovery.** Stages 2–3 remain guarded.
- **Restart actions.** This design only ever sends a message.
- **New notification or TUI surface**, beyond the one glyph a new substate needs
  in order to render (`cmd/agent-deck/cli_utils.go`,
  `internal/ui/connection_status.go`).
- **Changing the shipped defaults.** `enabled` stays false; `mode` stays
  `observe`.

## 6. Verification

**Detector (unit).** The captured 2026-08-07 pane classifies `api-error`. A
conductor quoting the banner behind `⎿` does not match. Assistant-line prose
mentioning the banner does not match. A recovered pane carrying both the banner
and a live spinner classifies `running`, not `api-error`. A banner scrolled
beyond the recent tail stops matching. `API Error: 401` still classifies
`auth-401`, not `api-error`.

**Policy (unit).** `api-error` dwells on `StatusChangedAt` and is actionable with
`LastSentAt` zero. `NotBefore` in the future yields `DecisionSkipNotBefore`;
`NotBefore` in the past does not block. A drafted composer yields
`ActionEscalate`. Caps, breaker and flicker gates still fire ahead of the action.

**Reset parsing (unit, table).** `"resets 6:10pm (Europe/Skopje)"` against a
14:23Z record yields 16:10Z. Day rollover: a wall time earlier than the record's
resolves to the next day. Unknown zone, absent string, and drifted wording each
fall back to `recordTS + 20m`. A fresh 429 after an attempt rearms `NotBefore`.

**Engine (unit).** `(ModeResume, ActionResume)` executes. Every other
(mode, action) pair returns `ErrActionInGuardedMode`. `ModeObserve` still
constructs no executor.

**Executor (integration).** A fake send path asserts exactly one delivery per
confirmed candidate, that the audit event carries the real `delivery` value, and
that `typed_not_submitted` is recorded as a failure.

**Manual.** Enable locally; on the next transport blip confirm the audit NDJSON
records a real `action: resume` with `delivery: submitted`, and that the pane
resumed.

## 7. Deployment note (operator machine)

Self-heal runs inside the transition daemon, so **the running daemon must be the
new binary** or the feature is silently absent. Two known local hazards:

- the `com.agentdeck.menubar` LaunchAgent has previously kept a stale pre-fix
  build alive across a rebuild;
- the transition-notifier launchd agent needs `bootout` + `bootstrap` after a
  `make install` — `kickstart` does not pick up the new binary.

Confirm which binary the live daemon is running before trusting the feature.
