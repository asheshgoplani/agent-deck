# Implementation plan — self-heal auto-resume (transport errors + usage limits)

Design: `docs/plans/2026-08-07-selfheal-auto-resume-design.md` (approved 2026-08-07).
Branch: `feature/selfheal-auto-resume`, based on `e20e60e5`.
Task files: `docs/plans/2026-08-07-selfheal-resume-tasks/task-NN-<name>.md`.

Every task file is self-contained. An implementer reads **only its own task file**
and needs nothing else.

## Two spec gaps this plan resolves (design intent preserved, not reopened)

Both were found by reading the code the design points at. Neither changes a
decision; both are the concrete form D1/D5 leave unstated.

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

## Invariants every task must preserve

- Shipped defaults do not change: `[selfheal] enabled = false`, `mode = "observe"`.
- `ModeSingleAction` / `ModeFull` keep returning `ErrActionInGuardedMode`.
- `ActionResend` stays unexecutable and stays the `idle-at-empty-prompt` `would_have`.
- No new goroutine, timer, cron or launchd unit (design D8).
- Repo gate: `make fmt` and `make lint` clean; conventional commit messages
  (`feat:` / `fix:` / `docs:` / `refactor:`); the contributor self-check at
  `.github/skills/agent-deck-contributor/scripts/self-check.sh` must show no FAIL.

## Task list

| # | Task | Tier | Depends on | Parallel with |
|---|------|------|-----------|---------------|
| 01 | `api-error` detector, substate, ordering, glyph/label | mid | — | — |
| 02 | selfheal policy surface: `ModeResume`, `ActionResume`, `NotBefore`, dwell map | mid | 01 | 04 |
| 03 | engine authorization: `(ModeResume × ActionResume)` chokepoint | strong | 02 | — |
| 04 | usage-limit reset parsing → `NotBefore` | strong | — | 02 |
| 05 | resume executor + send seam | strong | 02, 03 | — |
| 06 | daemon wiring in `selfheal_pass.go` | strong | 01–05 | — |
| 07 | config docs, CHANGELOG, full repo gate | mid | 01–06 | — |

**Parallel-safe pair: 02 and 04.** Disjoint files (`internal/selfheal/*` vs
`internal/session/usagelimit.go` + `internal/session/instance.go`). Nothing else
in this plan is parallel-safe: 01 and 04 both edit `internal/session/instance.go`,
and 03/05/06 each consume the previous task's new names.

### Task 01 — `api-error` detector + substate (tier: mid)

Files: `internal/tmux/detector.go`, `internal/tmux/authfailure.go`,
`internal/tmux/substate.go`, `internal/tmux/apierror_test.go` (new),
`internal/session/instance.go`, `cmd/agent-deck/cli_utils.go`,
`internal/ui/connection_status.go`.

- `scanClaudeBannerLines(content string, patterns, structural []string) bool`
  — new third parameter; both existing call sites pass
  `claudeBannerStructuralMarkers`.
- New `apiErrorBannerSubstrings`, `apiErrorBannerStructuralMarkers`,
  `hasClaudeAPIErrorBanner`.
- New `tmux.SubstateAPIError = "api-error"`, classified in `ClassifySubstate`
  after the busy check and before `model-unavailable`.
- Re-export alias `session.SubstateAPIError`; label in `SubstateLabel`; glyph
  `🌐` in `connection_status.go` under `StatusError`.

Verification (design §6 "Detector (unit)"): `go test ./internal/tmux/ -run 'APIError|Substate|AuthFailure|ErrorBanner' -v`.

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
  `c.ComposerDraft` is true, before the mode check.
- `actionParams` returns `{"reason": "transport"}` / `{"reason": "usage_limit"}`.
- Executed outcome feeds `policy.RecordOutcome`.
- The existing `TestExecuteIfAuthorized_GuardedModes_Refuse` is updated for the
  3-value signature (it asserts a 2-value return today and will not compile).

Verification (design §6 "Engine (unit)"): `go test ./internal/selfheal/ -v`.

Produces: `NewResumeEngine`, the 3-value `executeIfAuthorized`, `ActionExecutor`
contract for task 05.

### Task 04 — usage-limit reset parsing (tier: strong)

Files: `internal/session/usagelimit.go`, `internal/session/instance.go`,
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

Files: `internal/session/selfheal_pass.go`, `internal/session/selfheal_pass_test.go`.

- `selfHealRegistry.engineFor` takes the mode and builds either the observe
  engine or the resume engine.
- `SelfHealSettings.SelfHealMode()` accepts `"resume"`.
- `buildSelfHealCandidate` gains the usage-limit substate lift (the cached
  substate is deliberately usage-limit-blind), the `NotBefore` population, and
  the composer-draft read.

Verification: `go test ./internal/session/ -run SelfHeal -v`.

### Task 07 — docs, CHANGELOG, full gate (tier: mid)

Files: `CHANGELOG.md`, `docs/plans/2026-08-07-selfheal-auto-resume-design.md`
(status line only).

- `## [Unreleased] / ### Added` entry.
- Full gate: `make fmt`, `make lint`, `go build ./...`, `go vet ./...`,
  `go test ./internal/tmux/ ./internal/selfheal/ ./internal/session/ ./cmd/agent-deck/`,
  and `.github/skills/agent-deck-contributor/scripts/self-check.sh`.

## Known-flaky tests in this sandbox

`internal/session` (JSONL + python3 fixtures), `internal/ui` (zoxide) and
`internal/tmux` (`forkpty: Device not configured`) have pre-existing environment
failures unrelated to this change. Every task's verification therefore uses
`-run <pattern>` scoped to the new tests. Reconcile a broad-suite failure against
`git stash`-free baseline (`git -C <wt> worktree` is already isolated; run the
same `-run` pattern on `e20e60e5` if in doubt) before blaming the diff.
