# Verification reliability implementation plan

**Approved design:** `docs/design/2026-08-10-agent-deck-verification-reliability-design.md`
**Execution rule:** each linked task file is self-contained; implementers need read only their assigned file. Follow red/green/refactor for behavior changes. Do not implement from this overview when a task file exists.

## Delivery graph

```text
A: 01 -> 02 -> 03
          \-> 04
   01 -> 05
B: 06 || 07 || (08 -> 09) || 10
C: 11
D: 12 -> 13 -> 14
E: 15 (serial, separate repository)
All groups -> 16
```

Tasks joined by `||`, and Groups A/B/C/D/E, are safe to run in parallel when their dependency arrows are satisfied because their files are disjoint. Task 03 and 04 may run in parallel after 02. Task 05 may run after 01. Tasks 08→09 and 12→13→14 are strictly serial because they edit shared files or consume newly introduced contracts. Task 15 must never run concurrently with any other uc-cli work: its worktree already contains another agent's uncommitted edits. Task 16 is last.

## Ordered tasks

1. [Runtime queue durable store](2026-08-10-verification-reliability-tasks/task-01-runtime-queue-store.md) — **tier: mid**. Add the JSONL record/API, durable append, count/byte limits, pending/peek/discard, and persistence/FIFO/capacity/restart/discard tests in `internal/session`. Produces `RuntimeQueuedMessage`, path helpers, queue constants, `ErrRuntimeQueueFull`, and store APIs used by 02, 03, and 05.
2. [Runtime queue two-phase drain](2026-08-10-verification-reliability-tasks/task-02-runtime-queue-drain.md) — **tier: mid**. Add WAL staging/finalization, crash seam, FIFO formatter, and exactly-once/at-least-once tests. Consumes task 01 APIs and produces drain/format APIs used by 04.
3. [Queue-if-busy CLI](2026-08-10-verification-reliability-tasks/task-03-send-queue-if-busy-cli.md) — **tier: mid**, parallel-safe with 04 after 02. Add the flag, incompatibility and target checks, queue-full code, immediate idle behavior, and JSON receipt tests in `cmd/agent-deck`.
4. [Merged Stop-hook drain](2026-08-10-verification-reliability-tasks/task-04-stophook-merged-drain.md) — **tier: mid**, parallel-safe with 03 after 02. Extend `DrainForStopHook` to consume inbox and runtime records under one durable block budget and return one merged decision; do not edit `hook_handler.go`.
5. [Queue lifecycle discard](2026-08-10-verification-reliability-tasks/task-05-queue-lifecycle-discard.md) — **tier: cheap**, parallel-safe after 01. Call the exact discard API at removal and all four archive sites, with lifecycle tests.
6. [Archived transition suppression](2026-08-10-verification-reliability-tasks/task-06-archived-no-transition-events.md) — **tier: mid**, independent. Reject archived instances centrally and filter them before daemon hook/tmux probes; add race-oriented notification tests.
7. [Archived status remains stopped](2026-08-10-verification-reliability-tasks/task-07-archived-stays-stopped.md) — **tier: mid**, independent. Guard both vanished-pane branches in `UpdateStatus` and test each.
8. [Active-only children snapshots](2026-08-10-verification-reliability-tasks/task-08-children-active-only.md) — **tier: mid**, independent but serial before 09. Add archived metadata, `activeChildren`, active-only defaults, explicit inclusion, follow incompatibility, and fleet-context filtering.
9. [Follow gone event](2026-08-10-verification-reliability-tasks/task-09-follow-gone-event.md) — **tier: mid**, strictly after 08. Emit one archived `gone` event and suppress archived children from all other follow events, summaries, and terminal calculations.
10. [Heartbeat shell archive filter](2026-08-10-verification-reliability-tasks/task-10-pollsh-archived-filter.md) — **tier: cheap**, independent. Add the same defensive jq predicate to all three child pipelines. With an older binary that omits `archived`, `// false` is intentionally inert and cannot hide archived rows; it prevents failure but is not full backward protection.
11. [Linked-worktree group inheritance regression](2026-08-10-verification-reliability-tasks/task-11-worktree-group-inherit-cli-test.md) — **tier: mid**, independent. Add only a subprocess integration test with real Git worktree, isolated tmux, scrubbed-but-consistent profile environment, automatic parenting, and nested group; production implementation remains unchanged.
12. [Verification entrance](2026-08-10-verification-reliability-tasks/task-12-skill-verification-entrance.md) — **tier: strong**, independent but serial before 13. Amend `skills/orchestrate/SKILL.md` with the sixth entrance and complete recon/arms/adjudication/report terminal workflow before PR-specific stages.
13. [Verification prompt templates](2026-08-10-verification-reliability-tasks/task-13-verification-prompt-templates.md) — **tier: mid**, strictly after 12. Add four render-compatible, self-contained verification templates implementing the documented contracts.
14. [Verification docs tests](2026-08-10-verification-reliability-tasks/task-14-verification-docs-tests.md) — **tier: mid**, strictly after 13. Add structural assertions and a real `render.sh` round-trip, skipping only when `python3` is unavailable.
15. [uc-cli release provenance landing](2026-08-10-verification-reliability-tasks/task-15-uc-cli-release-provenance.md) — **tier: mid**, separate repo and **SERIAL / NEVER PARALLEL**. Inspect the existing branch/worktree first, preserve in-flight edits, verify the four approved properties, fill only demonstrated gaps with TDD, then land on uc-cli `main` without conflating repositories.
16. [Final verification](2026-08-10-verification-reliability-tasks/task-16-final-verification.md) — **tier: mid**, last. On final commits, run focused tests, prompt/shell checks, sandboxed full Go suite and integration tests; independently review both repository diffs. Do not infer success from piped command status; reconcile the documented sandbox flakes explicitly.

## Shared verification expectations

Focused commands include `go test ./internal/session/ -run TestRuntimeQueue -race -count=1 -v`, relevant `go test ./cmd/agent-deck -run ... -count=1 -v`, `bash skills/orchestrate/references/prompts/render.sh ...`, and `bash -n skills/orchestrate/references/poll.sh`. Final verification uses `make test` (`go test -race -v ./...` after the hook test), relevant integration tests, and independent `git diff`/`git log` review in Agent Deck and uc-cli. Known sandbox-only failures to classify rather than conceal are internal/session JSONL+python3, internal/ui zoxide, and internal/tmux “Device not configured”.
