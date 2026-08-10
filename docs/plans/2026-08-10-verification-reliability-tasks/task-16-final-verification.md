# Task 16 — Final cross-repository verification

**tier: mid**
**Parallelism:** last, after tasks 01–15 and final commits.

## Approved design extract (verbatim)

> - Follow red/green/refactor for every behavior change.
> - Add queue persistence, FIFO, exactly-once removal, capacity, restart, archive, stop, and unsupported-target tests.
> - Add archive-race tests across transition, done, and Stop-hook notification paths.
> - Cover active-only and `--include-archived` child snapshots and follow-mode transitions.
> - Test the real linked-worktree inheritance path in a subprocess.
> - Add structural and rendering tests for the verification entrance, prompt templates, artifact rules, retry bound, and no-change terminal state.
> - Run focused tests, the sandboxed full Go suite, prompt/shell checks, and relevant integration tests.
> - Review each repository diff independently and rerun verification on the final `main` commits.

## Change

Do not implement features. Record final SHAs; independently inspect each repo with status, log, and base-to-head diff. Resolve every task-record concern.

In Agent Deck run these commands directly (never pipe a status-bearing command):

```sh
go test ./internal/session -run 'RuntimeQueue|DrainForStopHook|Archived' -race -count=1 -v
go test ./cmd/agent-deck -run 'QueueIfBusy|Children|WorktreeGroup|OrchestrateVerification' -count=1 -v
bash -n skills/orchestrate/references/poll.sh
test "$(rg -c -F 'select((.archived // false) | not)' skills/orchestrate/references/poll.sh)" -eq 3
make test
```

Render recon/arm/report to a temp directory with every task-13 key and assert no unresolved placeholder. Run separately defined integration tests for session send, Stop hook, child follow, and launch. Run lint/CI only if declared tools exist and record absence.

In uc-cli final `main`, run focused provenance tests and `go test ./internal/cli -count=1`; inspect final labels/digest code/docs. Both tested SHAs must match reported SHAs and both statuses must be clean.

Potential sandbox failures requiring proof rather than automatic waiver: internal/session JSONL+python3, internal/ui zoxide, internal/tmux `Device not configured`. Reproduce against merge base or demonstrate missing dependency. Never judge success from `cmd | tail`.

## Acceptance criteria

- Final-SHA evidence covers every approved bullet.
- Both diffs are independently reviewed and clean.
- Environmental gaps are explicitly evidenced, never hidden.

## Verification

Expected: all available commands exit 0, render has zero placeholders, shell filter count is 3, and both statuses are empty. A baseline-reproduced sandbox failure must be reported as a gap rather than a passed suite.

## Interfaces

consumes:
- all task 01–15 outputs, final Agent Deck and uc-cli commits, `make test`, integration scripts

produces:
- evidence keyed to exact SHAs, two independent diff reviews, explicit environmental gaps

## Record (append-only)
