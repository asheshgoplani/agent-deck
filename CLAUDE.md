# agent-deck — Repo Instructions for Claude Code

This file is read by Claude Code when working inside the `agent-deck` repo. It lists hard rules for any AI or human contributor.

## Go Toolchain

Pin to Go 1.24.0 for all builds and tests. Go 1.25 silently breaks macOS TUI rendering.

```bash
export GOTOOLCHAIN=go1.24.0
```

## Session persistence: mandatory test coverage

Agent-deck has a recurring production failure where a single SSH logout on a Linux+systemd host destroys **every** managed tmux session. **As of v1.5.2, this class of bug is permanently test-gated.**

### The eight required tests

Any PR modifying session lifecycle paths MUST run `go test -run TestPersistence_ ./internal/session/... -race -count=1`. In addition, `bash scripts/verify-session-persistence.sh` MUST run end-to-end on a Linux+systemd host.

### Paths under the mandate

- `internal/tmux/**`, `internal/session/instance.go`, `internal/session/userconfig.go`, `internal/session/storage*.go`
- `cmd/session_cmd.go`, `scripts/verify-session-persistence.sh`, this `CLAUDE.md` section

### Forbidden changes without an RFC

- Flipping `launch_in_user_scope` default back to `false` on Linux
- Removing any of the eight `TestPersistence_*` tests
- Adding a code path that starts a Claude session and ignores `Instance.ClaudeSessionID`

## Feedback feature: mandatory test coverage

The in-product feedback feature is covered by 23 tests. All must pass before any PR touching the feedback surface is merged.

```
go test ./internal/feedback/... ./internal/ui/... ./cmd/agent-deck/... -run "Feedback|Sender_" -race -count=1
```

Reintroducing `D_PLACEHOLDER` as `feedback.DiscussionNodeID` is a **blocker**. `TestSender_DiscussionNodeID_IsReal` catches this automatically.

## Per-group config: mandatory test coverage

Per-group config dir applies to custom-command sessions too; `TestPerGroupConfig_*` suite enforces this.

## Watcher framework: mandatory test coverage

Any commit touching watcher source code MUST pass:

```bash
go test ./internal/watcher/... -race -count=1 -timeout 120s
go test ./cmd/agent-deck/... -run "Watcher" -race -count=1
```

### Watcher paths under the mandate

- `internal/watcher/**` (engine, adapters, health bridge, layout, state, event log, router)
- `cmd/agent-deck/watcher_cmd*.go` (CLI surface)
- `internal/ui/watcher_panel.go` (TUI watcher panel)
- `internal/statedb/statedb.go` (watcher rows in SQLite)
- `cmd/agent-deck/assets/skills/watcher-creator/` (embedded skill)
- `internal/session/watcher_meta.go` (watcher directory helpers)

### Watcher structural changes requiring RFC

- Removing or weakening the health bridge (`internal/watcher/health_bridge.go`)
- Disabling SQLite dedup (INSERT OR IGNORE on `watcher_events`)
- Weakening HMAC-SHA256 verification on the GitHub adapter
- Changing the `~/.agent-deck/watcher/` folder layout (REQ-WF-6)

### Skills + docs sync (REQ-WF-7)

Any commit modifying `internal/watcher/layout.go` or `internal/session/watcher_meta.go` MUST also update embedded skills, README, and CHANGELOG. `TestSkillDriftCheck_WatcherCreator` enforces this at build time.

### Integration harness

```bash
bash scripts/verify-watcher-framework.sh
```

## Performance regression: mandatory test coverage

Agent-deck has a recurring complaint that lifecycle operations (cold start, group create/delete) drift slower release-over-release. **As of v1.7.x, hot-path walltime is permanently test-gated.**

### Required tests

Any PR modifying performance-sensitive lifecycle paths MUST run:

```bash
GOTOOLCHAIN=go1.24.0 PERF_BUDGET_MULTIPLIER=2.0 \
  go test -run '^TestPerf_' -race -count=1 -timeout 120s \
  ./cmd/agent-deck/...
```

CI runs this as `.github/workflows/perf-smoke.yml`. Either red blocks the PR.

### Paths under the mandate

- `cmd/agent-deck/main.go`
- `internal/testutil/perfbudget.go`
- `**/*_perf_test.go`
- `.github/workflows/perf-smoke.yml`

### Cold vs warm classification (REQUIRED for new TestPerf_* tests)

Every `TestPerf_*` test classifies its work as COLD or WARM based on whether it crosses a process or syscall boundary. The classification picks both the budget formula and the measurement helper.

| Class | When to use | Budget formula | Helper |
|------|------------|----------------|--------|
| **COLD** | Cold-start exec, real-disk fsync, child-process spawn, network | `max(base × 5, 1ms) × multiplier` | `testutil.ColdBudget(t, base)` + `testutil.TrimmedMean(fn)` |
| **WARM** | Pure in-process Go work measurable under controlled GC | `max(base × 3, 1ms) × multiplier` | `testutil.WarmBudget(t, base)` + `testutil.TrimmedMeanWarm(fn)` |

`base` MUST cite the last observed local median (under `-race`, multiplier=1.0) in a comment next to the constant. CI sets `PERF_BUDGET_MULTIPLIER=2.0`; effective CI gate is therefore 10× local for COLD, 6× local for WARM.

The 1 ms floor (`testutil.PerfBudgetFloor`) caps minimum budgets — anything faster is either "just fast" (sub-ms timing dominated by clock resolution / scheduler jitter) or signals the unit under test is too small to be a meaningful regression target. Move such tests to `Benchmark*` (Track A) instead.

### Measurement: n=11 trimmed mean

`TrimmedMean` runs 11 timed iterations (plus 1 warm-up), drops the top 2 + bottom 2, averages the middle 7. Picked because:
- Odd n: median is well-defined as a fallback diagnostic.
- ~110 ms per test (11 × ~10 ms typical) — well under the 120 s suite timeout.
- Drop 2/2 absorbs one GC pause + one scheduler hiccup.
- Middle 7 → variance scales as 1/√7 ≈ 0.38 (~2× noise reduction vs single sample).

Larger n (21, 51) was rejected: ~30% marginal variance reduction at 2–5× test cost.

### Tier 1 vs Tier 2 (for future I/O-touching tests)

Disk-touching tests fall into one of two tiers. **This PR adds neither tier; the convention is documented for the next contributor.**

- **Tier 1 — tmpfs walltime** for code-side regressions in TUI-touching or persistence-touching paths. Run the test against a tmpfs-backed scratch dir (`/dev/shm` on Linux; `t.TempDir()` is already tmpfs on most CI Linux distros). fsync is effectively free, so the measured cost is CPU + Go-runtime cost only. Catches "we added 200 ms of CPU work in the save path" but NOT "we now do 5 fsyncs instead of 1". Classify as WARM.

- **Tier 2 — real-disk count assertions** for I/O-pattern regressions. Disk walltime varies 100× across SSDs / HDDs / cloud volumes — un-normalizable. Instead instrument the storage layer (`*sql.DB` and `*os.File` wrappers exposing fsync count, transaction count, rows written) and assert on counts: "create one group = exactly 1 transaction + 1 fsync". No walltime budget. Classify as COLD because fsync count is on the syscall side.

When adding a TUI-flow or persistence-touching test, write the Tier 1 walltime gate AND, if the test exercises a save path, the Tier 2 count gate. Both prevent different bug classes.

### Budget changes require an RFC

- Loosening any `TestPerf_*` budget (i.e. raising the `base`) by more than 25% requires an RFC at `docs/rfc/PERF_BUDGETS.md` documenting the cause and the upper bound.
- Removing a `TestPerf_*` test is forbidden without an RFC.
- Adding a budget MUST cite the local median and use the matching ColdBudget/WarmBudget helper.

### Track A vs Track B

`Benchmark*` functions are advisory (no `-race`, run via `make bench`). `TestPerf_*` are hard-gated walltime regressions that never spawn real tmux. Real-tmux benches live in Track A only.

## Behavioral evaluator harness: mandatory for user-observable changes

The evaluator harness at `tests/eval/` catches the class of bugs where a Go
unit test passes but the user sees the wrong thing (v1.7.35 CLI disclosure
buffered behind stdin, v1.7.37 TUI missing disclosure, #687 inject_status_line
unit-test green + real tmux broken). RFC: `docs/rfc/EVALUATOR_HARNESS.md`.

Any PR that adds or changes an interactive prompt, a tmux state mutation,
a disclosure step, or any user-facing behavior that pure Go tests cannot
structurally express MUST add an eval case. The `eval_smoke` suite runs on
every such PR (`.github/workflows/eval-smoke.yml`); a full tier runs at the
release gate (`.github/workflows/release.yml`).

### Eval paths under the mandate

- `tests/eval/**` (harness, cases, testdata)
- `cmd/agent-deck/feedback_cmd.go`, `internal/ui/feedback_dialog*.go` (feedback surfaces)
- `internal/tmux/tmux.go` (status bar injection, session start)
- `.github/workflows/eval-smoke.yml`, `.github/workflows/release.yml`

### Running locally

```bash
GOTOOLCHAIN=go1.24.0 go test -tags eval_smoke \
  ./tests/eval/... ./internal/ui/...
```

See `tests/eval/README.md` for how to add a case.

## --no-verify mandate

**`git commit --no-verify` is FORBIDDEN on source-modifying commits.** Metadata-only commits (`.planning/**`, `docs/**`, non-source `*.md`) MAY use `--no-verify` when hooks would no-op.

## General rules

- **Never `rm`** — use `trash`.
- **Never commit with Claude attribution** — no "Generated with Claude Code" or "Co-Authored-By: Claude" lines.
- **Never `git push`, `git tag`, `gh release`, `gh pr create/merge`** without explicit user approval.
- **TDD always** — the regression test for a bug lands BEFORE the fix.
- **Simplicity first** — every change minimal, targeted, no speculative refactoring.
