# RFC: Behavioral Evaluator Harness

- **Issue:** #37
- **Status:** DRAFT — design review, no implementation yet
- **Author:** conductor session, worktree `feature/design/37-evaluator-harness`
- **Date:** 2026-04-21
- **Scope:** design only; a follow-up RFC (or issue) will propose the first implementation slice

## Motivation

Three bugs shipped or were misdiagnosed in the last 96 hours because unit
tests passed while user-observable behavior was broken. The common thread
is that every affected test asserted on function returns, struct fields,
or buffered strings — not on what a human sitting in a terminal would
actually see, type, and confirm.

### Bug 1 — CLI feedback disclosure was typed-blind (v1.7.35 → v1.7.36 hotfix)

The v1.7.35 fix for issue #679 added an explicit disclosure block and a
`Post this? [y/N]` confirm to the `agent-deck feedback` CLI. The disclosure
was rendered into a `strings.Builder` and only flushed to stdout after
`handleFeedback` returned. Users typed Rating, Comment, and the confirm
answer against a blank cursor; the "your comment will be posted PUBLICLY
under your GitHub account" warning never appeared while consent was being
asked for.

The unit tests in `cmd/agent-deck/feedback_cmd_test.go` (pre-v1.7.36) also
used `strings.Builder`, so the buffered output was flushed synchronously
in the test but not in production. Same symbol, different runtime —
timing bug invisible to the test.

Fix landed in commit `57f6afe` (v1.7.36); see
`cmd/agent-deck/feedback_cmd.go:32-46` and the author's own admission in
the commit body:

> The legacy #679 tests continue to use strings.Builder for convenience
> — that type silently buffers, which is exactly the class of test gap
> that hid this regression.

### Bug 2 — TUI feedback dialog had zero disclosure (v1.7.37)

The v1.7.35 CLI disclosure did not carry over to the TUI feedback popup
at `internal/ui/feedback_dialog.go`. On Enter in the comment box, the TUI
went straight from comment to `sender.Send()`, silently posting to
GitHub Discussion #600 under the user's `gh` account — and on error
falling through Sender's three-tier clipboard+browser fallback.

Fix (`b2a75c1`, v1.7.37) inserted a `stepConfirm` between `stepComment`
and `stepSent` (`internal/ui/feedback_dialog.go:33-35`) and routed the
TUI through `GhCmd` directly to bypass the silent fallback
(`internal/ui/feedback_dialog.go:213-217`).

The CLI and TUI are separate code paths with no cross-path equivalence
test. A behavioral eval that said "given a user triggers feedback from
*either* path, the disclosure text appears and no post happens without
explicit y" would have failed on the TUI *before* v1.7.35 shipped.

### Bug 3 — #687 inject_status_line was misdiagnosed as a regression

A hotfix session received a bug report about `inject_status_line` in the
tmux session. Session ran
`go test ./internal/tmux/... -race`, saw green, closed the report as
"no regression."

The unit tests at `internal/tmux/tmux_test.go:73-98`
(`TestSession_InjectStatusLine_Default`,
`TestSession_SetInjectStatusLine`, `TestSession_InjectStatusLine_ReconnectSession`)
assert on the struct field and the generated args slice. They do not
start a real tmux server, they do not inspect the actual status-line
string inside the tmux window, and they do not verify what the user's
terminal displays. On a subsequent investigation, running agent-deck
against a real tmux socket confirmed current `main` renders the status
line correctly — but the first session could not tell the difference
between "tests pass" and "behavior is correct" because it never checked
behavior.

## 1. What the evaluator is

A **behavioral evaluator harness** is a second test layer that runs
alongside Go's unit tests. It:

1. Builds (or reuses a pre-built) `agent-deck` binary from the current
   tree.
2. Spawns that binary inside a **controlled sandbox**: a scratch
   `HOME` and `XDG_CONFIG_HOME`, a dedicated tmux socket, a stub `gh`
   shim on `PATH`, optional `PTY` allocation for interactive flows.
3. Drives the binary with stdin bytes, tmux keystrokes, or CLI args.
4. Asserts on **user-observable outcomes only**:
   - stdout/stderr **timing** (what bytes arrived before stdin was read).
   - tmux state (`tmux list-sessions`, `tmux capture-pane`,
     `tmux show-options`, inspection of `status-left` / `status-right`).
   - Filesystem mutations under the scratch home
     (`~/.agent-deck/feedback-state.json`, etc.).
   - Subprocess invocations captured by shims (argv of `gh api ...`,
     argv of `claude --resume`, env vars passed down).
5. Refuses access to anything a real user wouldn't have: it cannot
   import private packages, cannot reach into `*Session` internals,
   cannot assert on struct fields.

The harness is **complementary** to unit tests, not a replacement. Unit
tests stay fast, granular, and private. The harness owns one job only:
catching regressions where the function returned fine but the user saw
something wrong.

## 2. How it runs

Three candidate cadences, with tradeoffs:

| Cadence | Cost | Catches bugs at | Pain |
|---|---|---|---|
| **Every PR** | High. Full suite ~2-5 min once built; binary build adds 30-60s. | PR review — cheapest fix point. | PRs touching unrelated code still pay the tax. |
| **Pre-release gate only** | Low per-PR. | Release day — more expensive, context already cold. | Feedback loop is days, not minutes. |
| **Tiered: smoke on every PR, full on release** | Medium. | PR catches most issues, release backstops. | Tiering config drift between jobs. |

**Proposal: tiered.**

- **Per-PR smoke tier** (~30-60s wall time, runs in CI matrix):
  only tests tagged `//go:build eval_smoke`. Covers the highest-
  leverage paths: feedback CLI+TUI, session start/stop, tmux scope
  creation. Opt-in via a PR label (`eval:skip`) when touching pure
  docs; otherwise on by default.

- **Pre-release tier** (~3-5 min, runs in `release.yml` before tag):
  full eval suite including session restart across simulated SSH
  logout, Telegram plugin child-spawn scenarios, config-migration
  upgrade paths. Blocking. A release that fails eval does not get a
  tag.

**External dependencies** are handled by shims, never live calls:

- `gh` — a bash/go shim that logs argv+stdin to a file and returns a
  canned success or canned failure based on the test's pre-written
  script. Lives on `PATH` ahead of the real `gh`.
- `tmux` — use the **real tmux binary**, but against a
  per-test socket at `$TMPDIR/eval-tmux-<testid>.sock`. No shared
  server, no cross-test pollution, no teardown anxiety.
- GitHub API, Telegram API, external MCP servers — shimmed the same
  way as `gh`. The harness explicitly **does not** hit any live
  network endpoint; a failing shim assertion is a test failure, not
  a flake.

This does mean the harness **cannot** catch "but what if GitHub's real
API changes shape?" — that's a monitoring concern, not an eval
concern. See Non-goals (§8).

## 3. What a test case looks like

### Format: Go `testing` + custom helpers under `tests/eval/`

Rationale: agent-deck is a Go project. Every contributor reads and
writes Go. A separate framework (Bats, pytest, expect) adds a language
to the maintenance surface area without buying much — `os/exec` plus
`github.com/creack/pty` plus a small helpers package gets us 90% of
what those frameworks do, with zero language context-switch cost.

Sketch of a case (pseudocode — not committed in this RFC):

```go
func TestEval_FeedbackCLI_DisclosureBeforeStdinBlocks(t *testing.T) {
    env := eval.NewSandbox(t)       // scratch HOME, tmux socket, gh shim
    env.GhShim.ScriptSuccess()       // gh would succeed if called
    env.GhShim.ExpectNotCalled(t)    // but we'll assert it is NOT called

    session := env.Spawn(t, "feedback")  // starts binary under PTY
    defer session.Close()

    // Assert disclosure text hits the PTY output buffer BEFORE we
    // send anything on stdin. Timeout at 2s.
    session.ExpectOutput(t, "posted PUBLICLY", 2*time.Second)
    session.ExpectOutput(t, "[y/N]", 2*time.Second)

    session.Send("5\n")              // rating
    session.Send("great tool\n")     // comment
    session.Send("N\n")              // decline

    session.ExpectExit(t, 0)
    env.GhShim.AssertNotCalled(t)
    env.AssertFile(t, "~/.agent-deck/feedback-state.json",
        jsonContains(`"rating":5`))
}
```

Key properties of the format:

- **PTY, not pipe**: stdin/stdout run through a real pseudo-terminal so
  line-buffered output behaves like production. A `strings.Builder`
  can't mask a buffering bug because there is no `strings.Builder`.
- **Shims log, not stub**: `env.GhShim` records argv/stdin from the
  spawned binary. The test asserts what the binary *tried* to do.
- **Output is a time series**, not a string. `ExpectOutput` waits up
  to a timeout; if the disclosure is buffered behind a stdin read,
  the assertion fails with "waited 2s, never saw 'posted PUBLICLY'."
  This is structurally immune to the Bug 1 class of error.

## 4. Standard coverage

First-pass suite, ordered by leverage:

1. **Feedback flow** — CLI and TUI.
   - Disclosure visible before any stdin read (both paths).
   - Decline does not call `gh`.
   - Accept calls `gh api graphql` with the expected mutation payload
     and the expected body.
   - `gh` failure renders explicit error, never falls back to
     clipboard/browser.
   - Opt-out state persists across runs
     (`~/.agent-deck/feedback-state.json`).

2. **Session lifecycle**.
   - `session start` creates exactly one tmux session with the
     expected name.
   - `session stop` removes it.
   - `session restart` does not destroy the user's scope (see
     `SESSION-PERSISTENCE-SPEC.md` and the existing
     `scripts/verify-session-persistence.sh` — the harness
     *subsumes* that script's assertions).
   - SSH-logout simulation: start session in user scope, simulate
     logout (`loginctl terminate-user`-equivalent under a mock), assert
     tmux session survives.

3. **Telegram plugin child-spawn** (regressed once already — see S8,
   commit `ff6e04b`).
   - Parent conductor session has `TELEGRAM_STATE_DIR` set.
   - Child session spawned via custom-command wrapper must **not**
     inherit `TELEGRAM_STATE_DIR`.
   - Assert via a claude-stub that prints its env on launch.

4. **Config migrations**.
   - v1.5 config file at the scratch `HOME` still launches v1.7.x
     without data loss.
   - Additive fields default correctly.

5. **Tmux scope creation**.
   - Correct socket path.
   - Correct binary invoked (real path, not a wrapper alias).
   - Correct env passed to the child (via env-printing stub in the
     wrapper position).

6. **Status-line injection** (Bug 3).
   - Spawn session with `inject_status_line=true`, capture tmux
     window via `tmux display-message -p '#{status-left}'`,
     assert the injected content is actually there.

This is **not** an exhaustive list and the RFC does not commit to
shipping all six on day one. A reasonable first slice is (1) +
smoke-level (2); the rest grow with feature work.

## 5. Who maintains it

**Rule: eval cases ship with features.** The author adding a new
interactive flow adds an eval case alongside their unit tests. This
is the same pattern as the feedback and watcher test mandates in
`CLAUDE.md` — enforce by code review, not by tooling, in the first
iteration.

**Release gate**: the pre-release tier blocks `release.yml`. If the
suite is failing at release time, the release is held. This matches
the existing "session persistence tests must pass before merge"
policy.

**Flakiness budget**: zero tolerance. A flaky eval test is quarantined
(moved to a `//go:build eval_quarantine` tag, reported on a weekly
issue) within 48 hours of the first flake. The harness is worthless
if people start ignoring red.

## 6. Cost analysis

**Setup complexity: medium-high.** Writing the sandbox
helpers (PTY wrapper, gh shim, tmux-socket isolation, env inspector)
is a real project — realistically 2-4 days of focused work for the
first committer. Once the helpers exist, new test cases are cheap
(50-100 lines each).

**CI time**:

- Smoke tier: +30-60s per PR. Acceptable; within noise of existing
  test runtime.
- Release tier: +3-5 min per release. Negligible — releases are
  ~weekly, not per-commit.

**Maintenance burden**:

- Every CLI prompt change requires the corresponding eval case to
  update — same maintenance shape as existing unit tests.
- Shims need to stay in sync with the real `gh` / `tmux` interfaces
  we depend on. `gh` rarely breaks its CLI; tmux even more rarely.
  Low frequency of shim churn.
- The PTY layer is the scariest dependency — pseudo-terminals are
  OS-level plumbing and behave subtly differently on Linux vs macOS.
  Mitigation: run the harness only on Linux in CI (the release
  target), document a known-good local dev path for macOS, accept
  that a macOS-only eval failure is a yellow flag not a red one.

**Prevention value**: three bugs in 96 hours is a generous data
point. Each shipped a patch release (v1.7.36, v1.7.37), churned
the CHANGELOG, and consumed user trust. A harness that catches two
of three similar future bugs pays for its 2-4 days in the first
quarter.

**Honest tradeoff**: the harness will, in its first 6 months, have
cases that are harder to keep green than they are valuable. The
session-restart suite in particular depends on Linux systemd
semantics and will be painful to debug when it fails on a cold
morning. The mitigation is ruthless quarantine (§5), not heroic
effort. A harness nobody trusts is worse than no harness.

## 7. Examples — three cases that would have caught today's bugs

### Example 1 — Bug 1 (CLI disclosure buffering)

```go
func TestEval_FeedbackCLI_DisclosureBeforeConsent(t *testing.T) {
    env := eval.NewSandbox(t)
    env.GhShim.ScriptSuccess()

    pty := env.SpawnPTY(t, "feedback")
    defer pty.Close()

    // The class-of-bug assertion: user must see the consent
    // language *before* the process starts reading from stdin.
    pty.ExpectOutputBefore(t,
        want:   "posted PUBLICLY",
        before: "Rating (1-5",
        timeout: 2*time.Second)

    // If the disclosure is buffered, ExpectOutputBefore fails with
    // "saw 'Rating' at t=0, never saw 'posted PUBLICLY' before it."
}
```

### Example 2 — Bug 2 (TUI has no disclosure)

```go
func TestEval_FeedbackTUI_DisclosureStepExists(t *testing.T) {
    env := eval.NewSandbox(t)
    env.GhShim.ExpectNotCalled(t)  // decline means no post

    tui := env.SpawnTUI(t)
    defer tui.Close()

    tui.SendKey("ctrl+f")          // open feedback dialog
    tui.ExpectPane(t, "Rating")
    tui.SendKeys("5")
    tui.SendKey("enter")
    tui.ExpectPane(t, "comment")
    tui.SendKeys("great")
    tui.SendKey("enter")

    // The disclosure *must* appear before any post happens.
    tui.ExpectPane(t, "posted PUBLICLY")
    tui.ExpectPane(t, "[y/N]")
    tui.SendKey("n")

    tui.ExpectExitClean(t)
    env.GhShim.AssertNotCalled(t)
}

// Parity case — fails loudly if CLI and TUI diverge again.
func TestEval_FeedbackCLI_and_TUI_HaveEquivalentDisclosure(t *testing.T) {
    cliText := eval.CaptureCLIDisclosure(t)
    tuiText := eval.CaptureTUIDisclosure(t)
    require.Equal(t, normalize(cliText), normalize(tuiText),
        "CLI and TUI disclosure text must match word-for-word")
}
```

### Example 3 — Bug 3 (inject_status_line real tmux)

```go
func TestEval_Session_InjectStatusLine_RealTmux(t *testing.T) {
    env := eval.NewSandbox(t)
    sock := env.TmuxSocket()

    env.Run(t, "add", "-t", "evaltest", "-c", "claude",
        "--inject-status-line", "/tmp/evaltest")
    env.Run(t, "session", "start", "evaltest")

    // Query the *real* tmux for the *real* status-left content.
    statusLeft := env.Tmux(t, sock, "display-message", "-p",
        "-t", "evaltest:0", "#{status-left}")

    require.Contains(t, statusLeft, "evaltest",
        "injected status-line must include the session name")
    require.NotContains(t, statusLeft, "#{",
        "injected status-line must not contain unexpanded tmux vars")
}
```

None of these three cases would have passed against the broken
code. All three are the kind of test that unit tests *cannot*
structurally express, because the broken behavior lives in the
interaction between the binary and its runtime (stdout buffering,
tmux state, cross-path equivalence), not in any single function's
return value.

## 8. Non-goals

The harness is deliberately scoped. It does **not** try to be:

- **Fuzz testing.** No random input generation. A separate issue
  if we ever want it.
- **Property-based testing.** No `gopter`/`quick`-style generators.
  Eval cases are explicit scenarios with explicit assertions.
- **Performance regression detection.** No wall-time SLAs. Budgets
  for test timeouts, yes; perf budgets for the product, no.
- **Unit test replacement.** Unit tests stay. The harness does
  not cover branching logic inside pure functions; those remain
  a Go test concern.
- **Load / concurrency testing.** One user, one terminal, one
  session. Concurrent-session edge cases stay in `internal/session`
  unit tests and the existing `TestPersistence_` suite.
- **Real GitHub / Telegram integration testing.** All external
  services are shimmed. A separate contract-testing layer could
  exist someday but is out of scope here.
- **UI visual regression.** No screenshot diffing. TUI assertions
  are on pane *text*, not pixels.
- **Cross-platform macOS coverage.** Linux-first. macOS dev can
  run locally; CI runs Linux.

If a future need pushes against these boundaries, it earns its own
RFC — the harness stays small by saying no.

## 9. Decision — concrete proposal

**Framework.** Go `testing` with a custom helpers package at
`tests/eval/harness/`. Built on `os/exec`, `github.com/creack/pty`
(permissive license, widely used), real `tmux`, and bash shims for
`gh`. No new language, no new test runner.

**Location.**

```
tests/eval/
├── harness/           # sandbox, PTY wrapper, shims, tmux helpers
│   ├── sandbox.go
│   ├── pty.go
│   ├── gh_shim.go
│   └── tmux.go
├── feedback/          # eval cases for feedback CLI + TUI
│   ├── cli_test.go
│   └── tui_test.go
├── session/
│   └── lifecycle_test.go
└── testdata/
    └── gh_scripts/    # canned gh response scripts
```

**When it runs.**

- CI job `eval-smoke` in `.github/workflows/test.yml` — triggered
  per-PR, runs `go test -tags eval_smoke ./tests/eval/...`.
  Blocking. Opt-out label `eval:skip` for pure-docs PRs.
- CI job `eval-full` in `.github/workflows/release.yml` — runs
  `go test -tags 'eval_smoke eval_full' ./tests/eval/...` before
  any tag is cut. Blocking.

**Growth strategy.**

- **Day 1:** harness skeleton + 3 cases from §7 (the bugs that
  motivated the RFC). Commit the harness and the cases together;
  do not merge a harness-without-cases.
- **Ongoing:** every PR that fixes a "unit tests passed but user
  saw wrong thing" bug must add an eval case as part of the fix.
  Same discipline as the session-persistence mandate already in
  `CLAUDE.md`.
- **Quarterly:** audit the quarantine list. Cases that cannot be
  made reliable in a quarter are deleted, not carried.

**Out-of-band**: a CLAUDE.md section documenting the "eval case
required for interactive flow changes" rule, mirroring the
existing feedback / watcher / session-persistence mandates.

**What the conductor needs from the user before implementation.**

1. Confirmation that the tiered cadence (smoke per-PR + full
   per-release) is acceptable, or a preference for a different mix.
2. Confirmation that Linux-only CI is acceptable, or a hard
   requirement for macOS parity (which changes the cost estimate
   materially).
3. Green light on `tests/eval/` as the location; alternative
   proposals welcome (`test/eval/`, `internal/eval/`, etc.).
4. A decision on whether the first implementation slice is three
   cases (§7) or only the highest-leverage one (Bug 1, CLI). The
   latter de-risks the harness skeleton at the cost of a second
   PR for cases 2 and 3.

Once these four are settled, a follow-up issue can carry the
implementation plan with specific file lists, estimated diff size,
and a target release.
