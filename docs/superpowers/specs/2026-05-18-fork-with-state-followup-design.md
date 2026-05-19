# Fork-with-State Followup: Close Post-#1030 Gaps

**Status:** Draft — pending implementation plan
**Date:** 2026-05-18
**Author:** Steve Morin (steve.morin@gmail.com)
**Supersedes:** [`2026-05-14-fork-worktree-with-state-design.md`](2026-05-14-fork-worktree-with-state-design.md) (deprecated)
**Related code (upstream/main):**
- `internal/git/materialize_wip.go` — `MaterializeWipFromParent` (added by #1030)
- `internal/git/setup.go` — `WorktreeStateOptions`, `CreateWorktreeWithStateAndSetup` (added by #1030)
- `cmd/agent-deck/session_cmd.go` — `--with-state[-and-gitignored]` flag wiring (added by #1030)
- `internal/git/issue1029_with_state_test.go` and `internal/git/issue1029_edge_test.go` — upstream's test coverage

## Premise

Upstream merged PR #1030 (commit `6a1645eb`) on 2026-05-18, implementing the core ask of issue #1029: `agent-deck session fork --with-state` and `--with-state-and-gitignored` carry the parent session's staged + unstaged + untracked files (and optionally gitignored files) into a freshly-created worktree on a new branch. The diff-based, parent-read-only approach matches what was designed in the deprecated original spec.

The post-merge gap analysis at [`../discussions/2026-05-18-post-merge-gap-analysis.md`](../discussions/2026-05-18-post-merge-gap-analysis.md) identified 11 deltas between upstream's merged implementation and the deprecated design:
- 4 functional gaps (user-visible behavior the merged code doesn't provide)
- 7 test-coverage gaps (hardening that upstream didn't add)

This followup spec scopes the work to close those 11 gaps as a layer ON TOP of upstream's merged code. Upstream's `MaterializeWipFromParent` and `CreateWorktreeWithStateAndSetup` API stays untouched. The new code in this followup adds capabilities the merged code lacks; it does not refactor or replace what's there.

## Goal

Close the 11 gaps from the analysis, split across two PRs:

- **PR-A (correctness + test hardening, CLI surface):** parent-HEAD start point for linked-worktree parents, destination collision validation, cleanup-on-error in the CLI fork handler, and the test coverage that verifies those properties.
- **PR-B (TUI surface):** `ForkDialog` integration so users can trigger `--with-state` without dropping to the CLI, plus the TUI submit path's collision check + cleanup + behavioral eval.

## Non-goals

- Replacing upstream's `MaterializeWipFromParent` or `CreateWorktreeWithStateAndSetup`. They work; we wrap.
- Refactoring upstream's wrapper pattern into the deprecated spec's split pattern (`CreateWorktree` + `RunWorktreeSetup`). The wrapper is simpler caller code; we adopt it.
- Extracting `refuseUnsafeParentState` into a shared, exported `PreflightForkWithState` with typed `InProgressOperationError`. This is gap 11 — deferred to a separate PR-C with RFC discussion. PR-A and PR-B inline their refusals against upstream's existing internal helper instead.
- Renaming any upstream-introduced symbols. We add new symbols; we don't rename `MaterializeWipFromParent` to match the deprecated spec's `MaterializeParentState`.
- Touching upstream's `internal/git/materialize_wip.go` file directly. New helpers live in new files.

## What upstream merged (summary)

```go
// internal/git/materialize_wip.go
func MaterializeWipFromParent(parentDir, childDir string, includeIgnored bool) error
// Internal: refuseUnsafeParentState, gitDirOf, applyDiffFromParent (uses --index for staged),
//           copyUntrackedFromParent (two-pass for gitignored), runListZ (NUL-safe ls-files),
//           copyEachFile, copyOneFile (symlink + mode preserving)

// internal/git/setup.go
type WorktreeStateOptions struct {
    WithState   bool
    WithIgnored bool
}
func CreateWorktreeWithStateAndSetup(
    repoDir, worktreePath, branchName string,
    state WorktreeStateOptions,
    stdout, stderr io.Writer, setupTimeout time.Duration,
) (setupErr, err error)
// Orchestrates: CreateWorktree → MaterializeWipFromParent (if state.WithState)
//               → ProcessWorktreeInclude → setup hook.

// CreateWorktreeWithSetup remains as a thin pass-through with empty state.

// cmd/agent-deck/session_cmd.go: --with-state and --with-state-and-gitignored
// flags; validation that --with-state requires -w; wires to CreateWorktreeWithStateAndSetup.
```

Tests in upstream: canonical staged+unstaged+untracked, empty WIP, binary file, symlink, ignored opt-in, mid-merge refusal, deleted-in-parent tracked file, `CreateWorktreeWithStateAndSetup` wiring (8 tests).

## Functional gaps to close (4)

### G1 — TUI integration (PR-B)
**Symptom:** TUI users can't trigger `--with-state` from the `ForkDialog`. They must drop to the CLI.
**Fix:** Add two nested checkboxes ("Carry parent state" press `y`, "Include gitignored files" press `i`) under the existing "Create in worktree" checkbox. Wire the TUI submit handler to call `CreateWorktreeWithStateAndSetup` with the resolved `WorktreeStateOptions`.
**Lives in:** `internal/ui/forkdialog.go`, `internal/ui/home.go`.

### G2 — Parent-HEAD start point for linked parent worktrees (PR-A)
**Symptom:** When the parent session lives in a linked worktree whose HEAD differs from the invocation repo's HEAD (i.e., from main worktree's HEAD), upstream's `CreateWorktree(repoDir, ...)` creates the new fork worktree at the WRONG commit. Materialization then applies parent's diffs onto the wrong base, producing files that don't match what the parent session sees.
**Fix:** Add `HeadCommit(repoDir)` and `CreateWorktreeAtStartPoint(repoDir, worktreePath, branch, startPoint)` helpers. In the CLI fork handler, when `--with-state` is set, capture the parent session's HEAD and pass it as the start point. (This pre-empts upstream's `CreateWorktreeWithStateAndSetup` — we call `CreateWorktreeAtStartPoint` then `MaterializeWipFromParent` then `RunWorktreeSetup` manually, OR we extend the wrapper to accept an optional start point. PR-A picks one — see the followup plan.)
**Lives in:** `internal/git/git.go`, `cmd/agent-deck/session_cmd.go`.

### G3 — Destination collision validation (PR-A; also used by PR-B)
**Symptom:** If a user passes `-w <existing-branch> --with-state`, upstream has no early refusal. Either git refuses worktree-add cryptically deep in the stack, or it succeeds and creates a second worktree on the existing branch, polluting it.
**Fix:** Add a shared `ValidateForkWithStateDestination(repoRoot, branch)` in `internal/git/fork_with_state_destination.go` (new file, separate from upstream's `materialize_wip.go`). Returns typed `DestinationCollisionError{Kind: "worktree_exists"|"branch_exists", Branch, Path}`. CLI handler calls it before invoking the upstream wrapper; TUI submit handler calls it before its wrapper invocation. Both surfaces format the typed error their own way.
**Lives in:** `internal/git/fork_with_state_destination.go` (new), `cmd/agent-deck/session_cmd.go`, `internal/ui/home.go`.

### G4 — Cleanup-on-error (PR-A CLI portion + PR-B TUI portion)
**Symptom:** If `MaterializeWipFromParent` errors inside `CreateWorktreeWithStateAndSetup`, the partially-created worktree stays on disk. User must `git worktree remove --force` and `git branch -D` manually.
**Fix:** In both surfaces, wrap the call to `CreateWorktreeWithStateAndSetup`. On error, `git worktree remove --force <path>` and `git branch -D <branch>` (only if `CreateWorktreeAtStartPoint` returned proof of branch creation, per the deprecated spec's FWS-003 reasoning). Surface the original error to the user with `; new worktree cleaned up` appended.
**Lives in:** `cmd/agent-deck/session_cmd.go` (CLI), `internal/ui/home.go` (TUI).

## Test-coverage hardening to add (7)

| Gap | Test | PR |
|---|---|---|
| G5 | `TestMaterialize_ParentUntouched_RegressionForFollowup` — assert parent's `git status --porcelain`, index, and stash list byte-identical after `MaterializeWipFromParent` | **PR-A** |
| G6 | `TestForkWithState_BareRepoLayoutLinkedParentWorktree` — bare-repo project root with linked parent worktree as source; assert the fork is created at the parent's HEAD, not main's HEAD | **PR-A** |
| G7 | `TestForkWithState_SetupHookObservesMaterializedState` — setup script writes a fingerprint of a parent-WIP file; assert the fingerprint is in the marker file (proves materialize ran before setup) | **PR-A** |
| G8 | `TestRefuseUnsafeParentState_Rebase`, `_CherryPick`, `_Revert`, `_Bisect` — upstream only has `_Merge` for the refusal path; add the four missing kinds | **PR-A** |
| G9 | `TestSessionFork_WithStateOptionsPropagatedBeforeStart` — CLI before-start hook captures the prepared fork instance and verifies the with-state flags resolved by the handler match the user's request before `Start()` (the flags flow through `git.WorktreeStateOptions`, not `session.ClaudeOptions` — upstream did not extend `ClaudeOptions`) | **PR-A** |
| G10 CLI | `TestEval_SessionForkWithState_RealBinary` — eval-tagged smoke test: real `agent-deck` binary, scratch HOME, fake `claude`, real git repo; `session fork --with-state-and-gitignored -w <new-branch>` creates a new destination worktree whose files mirror the parent's WIP | **PR-A** |
| G10 TUI | `TestEval_ForkDialog_WithStateVisibleInteraction` — eval-tagged: render `ForkDialog`, drive `w → y → i`, assert visible checkbox text appears; assert getters report submitted values | **PR-B** |

## New code surfaces (file map summary)

| File | Action | Owner PR |
|---|---|---|
| `internal/git/git.go` | Modify — add `HeadCommit`, `CreateWorktreeAtStartPoint` | PR-A |
| `internal/git/git_test.go` | Modify — add `TestCreateWorktreeAtStartPoint_*` tests | PR-A |
| `internal/git/fork_with_state_destination.go` | Create — `ValidateForkWithStateDestination`, `DestinationCollisionError` | PR-A |
| `internal/git/fork_with_state_destination_test.go` | Create — validator tests | PR-A |
| `internal/git/materialize_wip_invariant_test.go` | Create — `TestMaterialize_ParentUntouched_RegressionForFollowup` | PR-A |
| `internal/git/issue1029_edge_test.go` | Modify — add 4 missing mid-op refusal tests | PR-A |
| `internal/git/fork_with_state_integration_test.go` | Create — bare-repo + setup-hook observation tests | PR-A |
| `cmd/agent-deck/session_cmd.go` | Modify — wire parent-HEAD + destination validation + cleanup-on-error + before-start hook | PR-A |
| `cmd/agent-deck/session_cmd_fork_state_test.go` | Create — CLI contract tests | PR-A |
| `tests/eval/session/fork_with_state_test.go` | Create — eval smoke for CLI | PR-A |
| `internal/ui/forkdialog.go` | Modify — sub-checkboxes, focus order, getters | PR-B |
| `internal/ui/forkdialog_test.go` | Modify — state-machine tests | PR-B |
| `internal/ui/forkdialog_eval_test.go` | Create — TUI behavioral eval | PR-B |
| `internal/ui/home.go` | Modify — TUI submit wires `CreateWorktreeWithStateAndSetup` with collision check + cleanup | PR-B |
| Session options (`session.ClaudeOptions`) | Not modified — upstream wired the with-state flags directly through `git.WorktreeStateOptions`, not via `ClaudeOptions`. PR-A's CLI handler builds the `WorktreeStateOptions` from the flag values and passes it straight to the git layer. | n/a |

## Mandatory test coverage

After PR-A and PR-B land, any PR modifying the following paths MUST pass:

```bash
go test ./internal/git/... -run "Materialize|RefuseUnsafeParentState|ValidateForkWithStateDestination|CreateWorktreeAtStartPoint|HeadCommit|ForkWithState|Issue1029" -race -count=1
go test ./cmd/agent-deck/... -run "SessionFork_WithState" -race -count=1
go test ./internal/ui/... -run "ForkDialog_(WithState|ToggleWithState|GitignoredRequires|Toggling|FocusOrder)" -race -count=1
go test -tags eval_smoke ./tests/eval/session/... ./internal/ui/... -run "TestEval_SessionForkWithState|TestEval_ForkDialog_WithState" -race -count=1
```

### Paths under the mandate

- `internal/git/materialize_wip.go` (upstream-owned; modifications require coordination)
- `internal/git/setup.go` — `WorktreeStateOptions`, `CreateWorktreeWithStateAndSetup` (upstream-owned)
- `internal/git/fork_with_state_destination.go` (new; PR-A)
- `internal/git/fork_with_state_destination_test.go` (new; PR-A)
- `internal/git/materialize_wip_invariant_test.go` (new; PR-A)
- `internal/git/fork_with_state_integration_test.go` (new; PR-A)
- `internal/git/issue1029_edge_test.go` (upstream-owned; extended in PR-A)
- `internal/git/git.go` — `HeadCommit`, `CreateWorktreeAtStartPoint` (PR-A)
- `cmd/agent-deck/session_cmd.go` fork handler (PR-A)
- `cmd/agent-deck/session_cmd_fork_state_test.go` (new; PR-A)
- `internal/ui/forkdialog.go` (PR-B)
- `internal/ui/home.go` TUI submit (PR-B)
- `internal/ui/forkdialog_eval_test.go` (new; PR-B)
- `tests/eval/session/fork_with_state_test.go` (new; PR-A)

### Structural changes requiring RFC

- Reverting upstream's `CreateWorktreeWithStateAndSetup` wrapper pattern back to the split pattern in the deprecated original spec
- Removing or weakening `ValidateForkWithStateDestination` once it lands
- Removing the parent-HEAD start-point capture and reverting to invocation-repo-HEAD behavior
- Removing the cleanup-on-error guards
- Removing `--with-state` from the TUI `ForkDialog` once it lands

## Out of scope (deferred PR-C)

**Gap 11 — Shared `PreflightForkWithState` extraction.** Upstream's `refuseUnsafeParentState` is internal (lowercase) to `materialize_wip.go` and returns a plain formatted `error`. The deprecated original spec called for promoting this to an exported `PreflightForkWithState` that returns a typed `InProgressOperationError`, so CLI and TUI could share a single preflight gate with surface-specific error rendering. This refactor touches upstream's just-merged code in a structurally significant way; it deserves its own RFC discussion with @asheshgoplani before implementation. Tracked as a future PR-C. The followup plan does NOT include implementation tasks for gap 11 — PR-A and PR-B inline their refusals against `refuseUnsafeParentState`'s implicit behavior.

## References

- Deprecated original spec: [`2026-05-14-fork-worktree-with-state-design.md`](2026-05-14-fork-worktree-with-state-design.md) (449 lines, FWS-001 through FWS-018)
- Deprecated original plan: [`../plans/2026-05-14-fork-worktree-with-state.md`](../plans/2026-05-14-fork-worktree-with-state.md) (~3700 lines, 21-task TDD breakdown)
- Followup implementation plan: [`../plans/2026-05-18-fork-with-state-followup.md`](../plans/2026-05-18-fork-with-state-followup.md) — task list for PR-A and PR-B
- Post-merge gap analysis: [`../discussions/2026-05-18-post-merge-gap-analysis.md`](../discussions/2026-05-18-post-merge-gap-analysis.md) — origin of the 11-gap framing
- Track B runbook: [`../discussions/2026-05-17-track-b-runbook.md`](../discussions/2026-05-17-track-b-runbook.md) — parallel-worktree comparative analysis flow
- Upstream PR: <https://github.com/asheshgoplani/agent-deck/pull/1030>
- Upstream merge commit: `6a1645eb` on `upstream/main`
- Issue: <https://github.com/asheshgoplani/agent-deck/issues/1029>

## Review change log

- 2026-05-18: FUS-001 — Spec drafted as followup to the deprecated 2026-05-14 design. Premise: upstream's #1030 is merged; scope this work to closing the 11 gaps identified in the post-merge gap analysis. Two-PR split (PR-A correctness + CLI tests; PR-B TUI). `ValidateForkWithStateDestination` extracted as a shared `internal/git` helper (avoiding upstream's `materialize_wip.go` file). Gap 11 (shared `PreflightForkWithState` extraction) explicitly deferred to PR-C with RFC.
- 2026-05-19: FUS-002 — Removed stale references to ClaudeOptions.WithState/IncludeGitignored fields. Upstream's #1030 chose a different architecture (flags flow through git.WorktreeStateOptions, not ClaudeOptions). Spec corrected to reflect upstream's actual wiring; A4's CLI contract tests already adapted to the real shape.
