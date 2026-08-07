# Implementation plan — self-heal auto-resume (transport errors + usage limits)

Design: `docs/plans/2026-08-07-selfheal-auto-resume-design.md` (approved 2026-08-07).
Branch: `feature/selfheal-auto-resume`, based on `e20e60e5`.
Task files: `docs/plans/2026-08-07-selfheal-resume-tasks/task-NN-<name>.md`.

Every task file is self-contained. An implementer reads **only its own task file**
and needs nothing else.

## Three spec gaps this plan resolves (design intent preserved, not reopened)

All three were found by reading the code the design points at. None changes a
decision; each is the concrete form D1/D5 — or design section 5's
`SubstateStalled` boundary — leaves unstated.

1. **D1's assistant-line guard would reject the real banner.**
   `scanClaudeBannerLines` skips a `⏺`-prefixed line unless it also carries a
   marker from `claudeBannerStructuralMarkers` (`" · "` or `{"type":"error"`).
   The captured banner — `⏺ API Error: Unable to connect to API (ENOTFOUND)` —
   carries neither, so inheriting the guard verbatim gives a detector that never
   fires. Resolution: `scanClaudeBannerLines` takes the structural set as a
   parameter, and the api-error scan passes a set that adds the parenthesised
   transport codes. Both §6 detector requirements then hold: the real banner
   matches, and assistant-line prose without a parenthesised code does not.
   (Task 01.)

2. **D5's executor cannot live where §3 places it.**
   `sendWithRetryTarget` / `executeSend` are in `package main`
   (`cmd/agent-deck/session_cmd.go`); `internal/session` cannot import them.
   Resolution: a registration seam — `internal/session` declares the send
   function type and a setter, `cmd/agent-deck` registers a thin wrapper over
   `executeSend` in an `init()`. The executor still calls the one verified send
   path; nothing is reimplemented. (Task 05.)

3. **D1's ordering silently takes `SubstateStalled` off the board.**
   `SubstateStalled` is defined by exactly the banner D1 now claims
   (`internal/tmux/substate.go`: *"a transport failure (\"API Error: Unable to
   connect to API (ConnectionRefused)\")"*, the 2026-07-24 incident). But
   `promoteStalled` (`internal/session/stall.go`) refines only
   `SubstateIdleAtEmptyPrompt`, and D1 puts `api-error` **ahead** of the idle
   verdict — so such a pane could never reach the promotion and `stalled` would
   become unreachable for the panes it was built for. That is not cosmetic:
   `session nudge` refuses to send when the substate is `stalled`
   (`cmd/agent-deck/session_nudge_cmd.go`), and that refusal is what stops a
   send from consuming an operator's in-flight composer draft.
   Resolution: `promoteStalled` refines `SubstateAPIError` too. Banner + empty
   composer stays `api-error` and is resumable; banner + a frozen draft still
   becomes `stalled` after the existing 10-minute `StallDwell`. This does **not**
   wire `SubstateStalled` into self-heal — it stays out of
   `stuckDwellThresholds` and `actionForSubstate`, so design section 5's
   "wiring `SubstateStalled` in" exclusion is untouched. (Task 01.)

## Invariants every task must preserve

- Shipped defaults do not change: `[selfheal] enabled = false`, `mode = "observe"`.
- `ModeSingleAction` / `ModeFull` keep returning `ErrActionInGuardedMode`.
- `ActionResend` stays unexecutable and stays the `idle-at-empty-prompt` `would_have`.
- `SubstateStalled` is never added to `stuckDwellThresholds` or
  `actionForSubstate`. `grep -rn 'SubstateStalled' internal/selfheal/` must stay
  empty (design section 5). Task 01 keeps the substate *reachable*; no task makes
  it *actionable*.
- `CHANGELOG.md` is not edited by any task (`CONTRIBUTING.md` house rule).
- No new goroutine, timer, cron or launchd unit (design D8).
- Repo gate: `make fmt` and `make lint` clean; conventional commit messages
  (`feat:` / `fix:` / `docs:` / `refactor:`); the contributor self-check at
  `.github/skills/agent-deck-contributor/scripts/self-check.sh` must show no FAIL.

## Task list

| # | Task | Tier | Depends on | Parallel with |
|---|------|------|-----------|---------------|
| 01 | `api-error` detector, substate, ordering, glyph/label, `stalled` preservation | mid | — | — |
| 02 | selfheal policy surface: `ModeResume`, `ActionResume`, `NotBefore`, dwell map | mid | 01 | 04 |
| 03 | engine authorization: `(ModeResume × ActionResume)` chokepoint | strong | 02 | — |
| 04 | usage-limit reset parsing → `NotBefore` | strong | — | 02 |
| 05 | resume executor + send seam | strong | 02, 03 | — |
| 06 | daemon wiring in `selfheal_pass.go` | strong | 01–05 | — |
| 07 | operator docs (`docs/self-heal.md`), full repo gate | mid | 01–06 | — |

**Parallel-safe pair: 02 and 04.** Disjoint files (`internal/selfheal/*` vs
`internal/session/usagelimit.go` + `internal/session/instance.go`). Nothing else
in this plan is parallel-safe: 01 and 04 both edit `internal/session/instance.go`,
and 03/05/06 each consume the previous task's new names.

### Task 01 — `api-error` detector + substate + `stalled` preservation (tier: mid)

Files: `internal/tmux/detector.go`, `internal/tmux/authfailure.go`,
`internal/tmux/substate.go`, `internal/tmux/apierror_test.go` (new),
`internal/session/instance.go`, `internal/session/stall.go`,
`internal/session/stall_test.go`, `cmd/agent-deck/cli_utils.go`,
`internal/ui/connection_status.go`, `internal/ui/connection_status_test.go`.

- `scanClaudeBannerLines(content string, patterns, structural []string) bool`
  — new third parameter; both existing call sites pass
  `claudeBannerStructuralMarkers`.
- New `apiErrorBannerSubstrings`, `apiErrorBannerStructuralMarkers`,
  `hasClaudeAPIErrorBanner`.
- New `tmux.SubstateAPIError = "api-error"`, classified in `ClassifySubstate`
  after the busy check and before `model-unavailable`.
- Re-export alias `session.SubstateAPIError`; label in `SubstateLabel`; glyph
  `🌐` in `connection_status.go` gated on `StatusIdle`/`StatusWaiting` —
  **not** `StatusError`, which a transport banner never produces (nothing maps
  the transport markers to the tmux "error" verdict, so a glyph gated there
  would be dead code). Mirrors `SubstateStalled`'s existing gating.
- `promoteStalled` (`internal/session/stall.go`) refines `SubstateAPIError` as
  well as `SubstateIdleAtEmptyPrompt`. Without this, classifying `api-error`
  ahead of the idle verdict makes `SubstateStalled` **unreachable** for the
  panes it was built from — and `session nudge`'s `SubstateStalled` refusal is
  what stops a send from consuming an operator's in-flight draft. Net
  behaviour: banner + empty composer stays `api-error` (self-heal resumes it);
  banner + frozen draft becomes `stalled` after the existing 10-minute
  `StallDwell` (🧊, nudge refuses). `SubstateStalled` is still **not** wired
  into self-heal — design section 5 holds.

Verification (design §6 "Detector (unit)"): `go test ./internal/tmux/ -run 'APIError|Substate|AuthFailure|ErrorBanner' -v`,
plus `go test ./internal/session/ -run PromoteStalled -v` and
`go test ./internal/ui/ -run RowStatusGlyph -v`.

Produces for later tasks: `tmux.SubstateAPIError`, `session.SubstateAPIError`.

### Task 02 — selfheal policy surface (tier: mid)

Files: `internal/selfheal/selfheal.go`, `internal/selfheal/candidate.go`,
`internal/selfheal/policy_test.go`, `internal/selfheal/predicate_test.go`.

- `ModeResume Mode = "resume"`; `ActionResume Action = "resume"`;
  `DecisionSkipNotBefore Decision = "skip_not_before"`.
- `Candidate.NotBefore time.Time` and `Candidate.ComposerDraft bool`.
- `stuckDwellThresholds[SubstateAPIError] = 60 * time.Second`,
  `stuckDwellThresholds[SubstateUsageLimit] = 0`.
- `actionForSubstate[SubstateAPIError] = ActionResume`,
  `actionForSubstate[SubstateUsageLimit] = ActionResume`.
- `Evaluate` gains the `NotBefore` gate after the dwell check.

Verification (design §6 "Policy (unit)"): `go test ./internal/selfheal/ -v`.

Produces: the four new names above + the two `Candidate` fields, all consumed by 03, 05, 06.

### Task 03 — engine authorization chokepoint (tier: strong)

Files: `internal/selfheal/engine.go`, `internal/selfheal/engine_test.go`.

- `NewResumeEngine(caps Caps, sink EventSink, exec ActionExecutor) *Engine`.
- `executeIfAuthorized` returns `(string, Action, error)` and authorizes exactly
  the pair `(ModeResume, ActionResume)`; everything else returns
  `ErrActionInGuardedMode`.
- Composer-draft downgrade (D6): `ActionResume` becomes `ActionEscalate` when
  `c.ComposerDraft` is true — **before `policy.Gate` / `policy.RecordAttempt`**,
  not just before the mode check. After them, a session holding an operator's
  draft burns both of its 2-per-6h recoveries doing nothing and is then
  cap-locked for six hours, so clearing the draft finds no budget left. The
  downgrade carries its own audit outcome `held_composer_draft`.
- `actionParams` returns `{"reason": "transport"}` / `{"reason": "usage_limit"}`.
- Executed outcome feeds `policy.RecordOutcome`.
- The existing `TestExecuteIfAuthorized_GuardedModes_Refuse` is updated for the
  3-value signature (it asserts a 2-value return today and will not compile).

Verification (design §6 "Engine (unit)"): `go test ./internal/selfheal/ -v`.

Produces: `NewResumeEngine`, the 3-value `executeIfAuthorized`, `ActionExecutor`
contract for task 05.

### Task 04 — usage-limit reset parsing (tier: strong)

Files: `internal/session/usagelimit.go`, `internal/session/instance.go`,
`internal/session/usagelimit_test.go` (the seven existing
`latestAssistantTurnIsRateLimited` call sites move to the 4-value signature),
`internal/session/usagelimit_reset_test.go` (new).

- `latestAssistantTurnIsRateLimited` returns
  `(limited bool, text string, recordTS time.Time, ok bool)`.
- `parseUsageLimitReset(text string, recordTS time.Time) (time.Time, bool)`
  handles `resets 6:10pm (Europe/Skopje)`, optional minutes, day rollover,
  unknown zone.
- `usageLimitNotBefore(text string, recordTS, prevNotBefore time.Time) time.Time`
  — parse-to-schedule, with the `recordTS + 20m` fallback and the fresh-429 rearm.
- Instance memo fields `usageLimitNotBeforeCached`, cleared on rebind exactly
  like `usageLimitedCached`; accessor `UsageLimitNotBefore() time.Time`.

Verification (design §6 "Reset parsing (unit, table)"): `go test ./internal/session/ -run UsageLimit -v`.

Produces: `(*Instance).UsageLimitNotBefore()` consumed by task 06.

### Task 05 — resume executor + send seam (tier: strong)

Files: `internal/session/selfheal_resume.go` (new),
`internal/session/selfheal_resume_test.go` (new),
`cmd/agent-deck/selfheal_send.go` (new).

- `type SelfHealSendFunc func(inst *Instance, message string) (delivery string, err error)`
  plus `SetSelfHealSender(fn SelfHealSendFunc)`.
- `resumeExecutor` implementing `selfheal.ActionExecutor`, resolving the instance
  by id, choosing the prompt by `reason`, delegating to the registered sender,
  and returning an outcome string that carries the real `delivery` value.
- `cmd/agent-deck/selfheal_send.go`: `init()` registering a wrapper over
  `executeSend(tmuxSess, inst.Tool, message, false, defaultSendTuning())`.

Verification (design §6 "Executor (integration)"): `go test ./internal/session/ -run SelfHealResume -v`
plus `go build ./...`.

Produces: `NewResumeExecutor`, `SetSelfHealSender`, consumed by task 06.

### Task 06 — daemon wiring (tier: strong)

Files: `internal/session/selfheal_pass.go`, `internal/session/selfheal_pass_test.go`,
`internal/session/userconfig.go`, `internal/session/transition_daemon.go`.

- `selfHealRegistry.engineFor` takes the mode and builds either the observe
  engine or the resume engine. The engine stays cached per profile — it holds
  the two-read confirm and every cap/backoff/breaker window, so rebuilding it
  per pass would reset them. Consequence, documented rather than engineered
  away: changing `mode` in config takes effect only after a transition-daemon
  restart. Task 06 states it in `engineFor`'s doc comment and pins it with a
  test; task 07 states it for operators.
- The registry tests must construct through the real `engineFor` path
  (pre-injecting `r.engines[profile]` hits the cache-return and asserts on the
  injected value, so such a test cannot fail). `internal/session`'s `TestMain`
  already calls `testutil.IsolateHome()`, so the real NDJSON sink lands in the
  sandbox.
- `SelfHealSettings.SelfHealMode()` accepts `"resume"`.
- `buildSelfHealCandidate` gains the usage-limit substate lift (the cached
  substate is deliberately usage-limit-blind), the `NotBefore` population, and
  the composer-draft read.

Verification: `go test ./internal/session/ -run SelfHeal -v`.

### Task 07 — operator docs, full gate (tier: mid)

Files: `docs/self-heal.md` (new), `README.md` (one User-guides row),
`docs/plans/2026-08-07-selfheal-auto-resume-design.md` (status line only).

- **No `CHANGELOG.md` edit.** `CONTRIBUTING.md` forbids it in a PR ("entries are
  added at landing time"), and task 07's own gate would flag it. The release
  note lives in the PR body; task 07 carries the prose verbatim.
- `docs/self-heal.md` is the documentation target that keeps the feature from
  shipping documented nowhere: every `[selfheal]` key with its shipped default,
  what `mode = "resume"` authorises, design section 4's `global_per_hour`
  guidance (default 5, raise to ~30 for a fleet, and why), the audit outcome
  strings, the mode-change-needs-restart caveat, and the deployment note.
  Linked from the README so it is reachable without knowing the filename.
- Full gate: `make fmt`, `make lint`, `go build ./...`, `go vet ./...`,
  `go test ./internal/tmux/ ./internal/selfheal/ ./internal/session/ ./cmd/agent-deck/`,
  and `.github/skills/agent-deck-contributor/scripts/self-check.sh`.

## Verification not owned by any task

Design section 6's **Manual** step — *"Enable locally; on the next transport blip
confirm the audit NDJSON records a real `action: resume` with
`delivery: submitted`, and that the pane resumed"* — is **the operator's,
post-merge, and is owned by no task in this plan**. No task installs, restarts or
bootstraps anything on the operator machine (design section 7 lists why that is
its own hazard). Task 07 carries the step into the PR body so it is not lost.

## Known-flaky tests in this sandbox

`internal/session` (JSONL + python3 fixtures), `internal/ui` (zoxide) and
`internal/tmux` (`forkpty: Device not configured`) have pre-existing environment
failures unrelated to this change. Every task's verification therefore uses
`-run <pattern>` scoped to the new tests. Reconcile a broad-suite failure against
`git stash`-free baseline (`git -C <wt> worktree` is already isolated; run the
same `-run` pattern on `e20e60e5` if in doubt) before blaming the diff.
