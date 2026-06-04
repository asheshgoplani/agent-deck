# Fork-with-State Followup: Close Post-#1030 Gaps

**Status:** PR-A merged as [#1263](https://github.com/asheshgoplani/agent-deck/pull/1263) (merge commit `5dc3e912`, 2026-06-03); PR-B (TUI) is the remaining work, being reconciled in `worktree/fork-state-tui-followup`
**Date:** 2026-05-18
**Author:** Steve Morin (steve.morin@gmail.com)
**Supersedes:** [`2026-05-14-fork-worktree-with-state-design.md`](2026-05-14-fork-worktree-with-state-design.md) (deprecated)
**Related code (upstream/main):**
- `internal/git/materialize_wip.go` — `MaterializeWipFromParent` (added by #1030)
- `internal/git/setup.go` — `WorktreeStateOptions`, `CreateWorktreeWithStateAndSetup` (added by #1030)
- `cmd/agent-deck/session_cmd.go` — `--with-state[-and-gitignored]` flag wiring (added by #1030)
- `internal/git/issue1029_with_state_test.go` and `internal/git/issue1029_edge_test.go` — upstream's test coverage
- `internal/vcs`, `internal/vcsbackend`, `internal/jujutsu`, `cmd/agent-deck/vcs_helper.go` — VCS backend abstraction (`vcs.Backend` interface, `vcsbackend.Detect`/`vcsbackend.CreateWorktreeWithSetup`, git backend, `detectAndCreateBackend`) landed on main since this spec was drafted; with-state remains git-only and calls `internal/git` functions directly behind a `backend.Type()==vcs.TypeGit` guard
- `internal/git/materialize_wip.go` — PR [#1277](https://github.com/asheshgoplani/agent-deck/pull/1277) (commit `0ab5b714`) changed `MaterializeWipFromParent` to resolve `GetRepoRoot(parentDir)` internally, so callers may pass any path inside the parent worktree (existing signatures unaffected)
- PR-A merged as [#1263](https://github.com/asheshgoplani/agent-deck/pull/1263) (merge commit `5dc3e912`, 2026-06-03), closing gaps 2, 3, 4-CLI, 5, 6, 7, 8, 9, 10-CLI

## Premise

Upstream merged PR #1030 (commit `6a1645eb`) on 2026-05-18, implementing the core ask of issue #1029: `agent-deck session fork --with-state` and `--with-state-and-gitignored` carry the parent session's staged + unstaged + untracked files (and optionally gitignored files) into a freshly-created worktree on a new branch. The diff-based, parent-read-only approach matches what was designed in the deprecated original spec.

The post-merge gap analysis at [`../discussions/2026-05-18-post-merge-gap-analysis.md`](../discussions/2026-05-18-post-merge-gap-analysis.md) identified 11 deltas between upstream's merged implementation and the deprecated design:
- 4 functional gaps (user-visible behavior the merged code doesn't provide)
- 7 test-coverage gaps (hardening that upstream didn't add)

This followup spec scopes the work to close those 11 gaps as a layer ON TOP of upstream's merged code. Upstream's `MaterializeWipFromParent` and `CreateWorktreeWithStateAndSetup` API stays untouched. The new code in this followup adds capabilities the merged code lacks; it does not refactor or replace what's there.

**Update (2026-06-03):** Since this spec was drafted, main gained a VCS backend abstraction (`internal/vcs`, `internal/vcsbackend`, `internal/jujutsu`, `cmd/agent-deck/vcs_helper.go`). PR-A subsequently merged as [#1263](https://github.com/asheshgoplani/agent-deck/pull/1263) with a backend-reconciled, **decomposed** with-state CLI path (`CreateWorktreeAtStartPoint` + `MaterializeWipFromParent` + `RunWorktreeSetupAfterCreate`) rather than the single-wrapper call originally envisioned, with with-state held git-only behind an early `backend.Type()==vcs.TypeGit` guard. [#1277](https://github.com/asheshgoplani/agent-deck/pull/1277) further refined `MaterializeWipFromParent` to resolve the repo root internally. The remaining functional gaps are only **G1 (TUI integration)** and **G10-TUI**.

## Goal

Close the 11 gaps from the analysis, split across two PRs:

- **PR-A (correctness + test hardening, CLI surface):** parent-HEAD start point for linked-worktree parents, destination collision validation, cleanup-on-error in the CLI fork handler, and the test coverage that verifies those properties.
- **PR-B (TUI surface):** `ForkDialog` integration so users can trigger `--with-state` without dropping to the CLI, plus the TUI submit path's collision check + cleanup + behavioral eval.

## Non-goals

- Replacing upstream's `MaterializeWipFromParent` or `CreateWorktreeWithStateAndSetup`. They remain available; this followup layers around them without changing their API.
- Refactoring upstream's wrapper pattern into the deprecated spec's split pattern (`CreateWorktree` + `RunWorktreeSetup`). **Reality as merged in #1263:** the *with-state* path WAS decomposed into the split pattern (`CreateWorktreeAtStartPoint` + `MaterializeWipFromParent` + `RunWorktreeSetupAfterCreate`) because parent-HEAD start-point capture (G2) and cleanup-on-error (G4) require it. The *non-state* path keeps upstream's `CreateWorktreeWithStateAndSetup` wrapper. So the non-goal stands only for the non-state path: we do not decompose the wrapper where with-state is not requested.
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

**As merged in #1263 (2026-06-03):** the CLI with-state path is NOT a single `CreateWorktreeWithStateAndSetup` wrapper call. It is decomposed and routed through the VCS backend abstraction. As-merged deltas beyond the original design:
- An early guard `if wantState && backend.Type() != vcs.TypeGit { reject "--with-state is only supported for git repositories" }`, placed BEFORE the git-direct collision gate. This early guard — not call routing — is what makes the subsequent `internal/git`-direct calls jujutsu-safe.
- A mutually-exclusive collision gate: `if wantState { ValidateForkWithStateDestination } else if !createNewBranch && !backend.BranchExists(...) { "branch does not exist (use -b)" }`. With-state requires the branch ABSENT; the else-branch requires it PRESENT.
- A configured worktree branch prefix is applied before validation, and an existing destination worktree path is refused before creation.
- Reuse routed through `backend.GetWorktreeForBranch`, with the reuse assignment gated on `!wantState` (with-state never reuses).
- Mid-op refusal surfaces ACTIONABLE abort commands (`git rebase --abort`, etc.) before worktree creation; a submodule warning; `HeadCommit` uses stdout-only (`cmd.Output()` + separate stderr) so git stderr warnings can't contaminate the hash; and cleanup-on-error with a manual-cleanup hint (`branchCleanupHint`).

## Functional gaps to close (4)

### G1 — TUI integration (PR-B)
**Symptom:** TUI users can't trigger `--with-state` from the `ForkDialog`. They must drop to the CLI.
**Fix:** Add two nested checkboxes — `Carry parent state` and `Include gitignored files` — under the existing "Create in worktree" checkbox. Per the 2026-06-04 design decisions (see the plan's PR-B "DESIGN DECISIONS" block): the checkboxes are real focus targets toggled with `Space`/`Enter` (with `y`/`i` as shortcuts); a static hint `↳ creates a NEW branch at parent HEAD` renders under "Carry parent state" (no dialog-side git check), and an existing-branch collision is refused at submit. Wire the TUI submit handler to mirror PR #1263's decomposed with-state path: keep the non-state path backend-routed, but when with-state is requested require a git backend and run `ValidateForkWithStateDestination` → `HeadCommit` → `CreateWorktreeAtStartPoint` → `MaterializeWipFromParent` → `ProcessWorktreeInclude` → `RunWorktreeSetupAfterCreate` with cleanup-on-materialize failure.
**Lives in:** `internal/ui/forkdialog.go`, `internal/ui/home.go`.

### G2 — Parent-HEAD start point for linked parent worktrees (PR-A)
**Symptom:** When the parent session lives in a linked worktree whose HEAD differs from the invocation repo's HEAD (i.e., from main worktree's HEAD), upstream's `CreateWorktree(repoDir, ...)` creates the new fork worktree at the WRONG commit. Materialization then applies parent's diffs onto the wrong base, producing files that don't match what the parent session sees.
**Fix:** Add `HeadCommit(repoDir)` and `CreateWorktreeAtStartPoint(repoDir, worktreePath, branch, startPoint)` helpers. In the CLI fork handler, when `--with-state` is set, capture the parent session's HEAD and pass it as the start point. PR-A chose the decomposed path: `CreateWorktreeAtStartPoint` then `MaterializeWipFromParent` then `RunWorktreeSetupAfterCreate`, rather than extending `CreateWorktreeWithStateAndSetup`.
**Lives in:** `internal/git/git.go`, `cmd/agent-deck/session_cmd.go`.
**As merged in #1263:** CLOSED. The handler captures the parent's HEAD via `HeadCommit` (stdout-only, so git stderr warnings can't contaminate the hash) and calls `CreateWorktreeAtStartPoint` directly — the with-state path is decomposed (`CreateWorktreeAtStartPoint` + `MaterializeWipFromParent` + `RunWorktreeSetupAfterCreate`), not a single wrapper call. These `internal/git`-direct calls are reached only after the early `backend.Type()==vcs.TypeGit` guard, keeping the path jujutsu-safe.

### G3 — Destination collision validation (PR-A; also used by PR-B)
**Symptom:** If a user passes `-w <existing-branch> --with-state`, upstream has no early refusal. Either git refuses worktree-add cryptically deep in the stack, or it succeeds and creates a second worktree on the existing branch, polluting it.
**Fix:** Add a shared `ValidateForkWithStateDestination(repoRoot, branch)` in `internal/git/fork_with_state_destination.go` (new file, separate from upstream's `materialize_wip.go`). Returns typed `DestinationCollisionError{Kind: CollisionWorktreeExists|CollisionBranchExists, Branch, Path}`. CLI handler calls it before its decomposed with-state create path; TUI submit handler must do the same before `CreateWorktreeAtStartPoint`. Both surfaces format the typed error their own way.
**Lives in:** `internal/git/fork_with_state_destination.go` (new), `cmd/agent-deck/session_cmd.go`, `internal/ui/home.go`.
**As merged in #1263 (CLI):** CLOSED. `ValidateForkWithStateDestination` is invoked from a mutually-exclusive collision gate after the early `backend.Type()==vcs.TypeGit` guard and after applying the configured branch prefix: `if wantState { ValidateForkWithStateDestination } else if !createNewBranch && !backend.BranchExists(...) { "branch does not exist (use -b)" }` — with-state requires the branch ABSENT, the else-branch requires it PRESENT. The validator checks for an existing worktree first, propagates `GetWorktreeForBranch` failures, then checks local branch existence. Backend reuse (`backend.GetWorktreeForBranch`) is gated on `!wantState`, so with-state never reuses an existing worktree. The TUI invocation of this validator remains PR-B work.

### G4 — Cleanup-on-error (PR-A CLI portion + PR-B TUI portion)
**Symptom:** If `MaterializeWipFromParent` errors after the new worktree is created, the partially-created worktree stays on disk. User must `git worktree remove --force` and `git branch -D` manually.
**Fix:** In both surfaces, cleanup wraps the decomposed with-state sequence. After `CreateWorktreeAtStartPoint`, on materialization error run `git worktree remove --force <path>` and `git branch -D <branch>` (only if `CreateWorktreeAtStartPoint` returned proof of branch creation, per the deprecated spec's FWS-003 reasoning). Surface the original error to the user with `; new worktree cleaned up` appended.
**Lives in:** `cmd/agent-deck/session_cmd.go` (CLI), `internal/ui/home.go` (TUI).
**As merged in #1263 (CLI):** the CLI portion is CLOSED. Because the with-state path is decomposed (not a single wrapper call), cleanup-on-error is wired around the decomposed `internal/git`-direct calls and additionally surfaces a manual-cleanup hint (`branchCleanupHint`) plus an actionable mid-op abort message (`git rebase --abort`, etc.) before worktree creation. The TUI cleanup portion remains PR-B work, folded into G1.

Net: **G2, G3, and G4-CLI are CLOSED by #1263.** Only **G1 (TUI integration)** and **G10-TUI** remain.

## Test-coverage hardening to add (7)

| Gap | Test | PR |
|---|---|---|
| G5 | `TestMaterializeWipFromParent_ParentUntouched` — assert parent's `git status --porcelain`, index, diff, and stash list byte-identical after `MaterializeWipFromParent` | **PR-A** |
| G6 | `TestForkWithState_BareRepoLayoutLinkedParentWorktree` — bare-repo project root with linked parent worktree as source; assert the fork is created at the parent's HEAD, not main's HEAD | **PR-A** |
| G7 | `TestForkWithState_SetupHookObservesMaterializedState` — setup script writes a fingerprint of a parent-WIP file; assert the fingerprint is in the marker file (proves materialize ran before setup) | **PR-A** |
| G8 | `TestRefuseUnsafeParentState_Rebase_RegressionForFollowup`, `_CherryPick_RegressionForFollowup`, `_Revert_RegressionForFollowup`, `_Bisect_RegressionForFollowup` — upstream only has merge coverage for the refusal path; add the four missing kinds | **PR-A** |
| G9 | `TestSessionFork_WithStateOptionsPropagatedBeforeStart` and `TestSessionFork_WithStateHookCapturesResolvedStateBeforeStart` — CLI before-start hook captures the prepared fork instance and verifies the with-state flags resolved by the handler match the user's request before `Start()` (the flags flow through `git.WorktreeStateOptions`, not `session.ClaudeOptions` — upstream did not extend `ClaudeOptions`) | **PR-A** |
| G10 CLI | `tests/eval/session/fork_with_state_test.go` — eval-tagged real-binary suite covering happy path (`TestEval_SessionForkWithState_RealBinary`), existing branch refusal, existing worktree refusal, mid-rebase refusal, and submodule warning | **PR-A** |
| G10 TUI | `TestEval_ForkDialog_WithStateVisibleInteraction` — eval-tagged: render `ForkDialog`, drive `w → y → i`, assert visible checkbox text appears; assert getters report submitted values | **PR-B** |

## New code surfaces (file map summary)

| File | Action | Owner PR |
|---|---|---|
| `internal/git/git.go` | Modify — add `HeadCommit`, `CreateWorktreeAtStartPoint` | PR-A |
| `internal/git/git_test.go` | Modify — add `TestCreateWorktreeAtStartPoint_*` and `TestHeadCommit_IgnoresGitWarningsOnStderr` tests | PR-A |
| `internal/git/fork_with_state_destination.go` | Create — `ValidateForkWithStateDestination`, `DestinationCollisionError`, collision kind constants, `HasSubmodules`, `DetectInProgressOperation` | PR-A |
| `internal/git/fork_with_state_destination_test.go` | Create — validator tests | PR-A |
| `internal/git/materialize_wip_invariant_test.go` | Create — `TestMaterializeWipFromParent_ParentUntouched` | PR-A |
| `internal/git/issue1029_edge_test.go` | Modify — add 4 missing mid-op refusal tests | PR-A |
| `internal/git/fork_with_state_integration_test.go` | Create — bare-repo + setup-hook observation tests | PR-A |
| `cmd/agent-deck/session_cmd.go` | Modify — wire parent-HEAD + destination validation + cleanup-on-error + before-start hook | PR-A |
| `cmd/agent-deck/session_cmd_fork_state_test.go` | Create — CLI contract tests plus structural guard for actionable mid-op refusal | PR-A |
| `tests/eval/session/fork_with_state_test.go` | Create — CLI eval suite for real-binary with-state behavior and refusal/warning paths | PR-A |
| `internal/ui/forkdialog.go` | Modify — sub-checkboxes, focus order, getters | PR-B |
| `internal/ui/forkdialog_test.go` | Modify — state-machine tests | PR-B |
| `internal/ui/forkdialog_eval_test.go` | Create — TUI behavioral eval | PR-B |
| `internal/ui/home.go` | Modify — TUI submit wires the git-only decomposed with-state path with collision check + cleanup, while keeping non-state creation backend-routed | PR-B |
| Session options (`session.ClaudeOptions`) | Not modified — upstream wired the with-state flags directly through `git.WorktreeStateOptions`, not via `ClaudeOptions`. PR-A's CLI handler builds the `WorktreeStateOptions` from the flag values and passes it straight to the git layer. | n/a |

## Mandatory test coverage

After PR-A and PR-B land, any PR modifying the following paths MUST pass:

```bash
go test ./internal/git/... -run "Materialize|RefuseUnsafeParentState|ValidateForkWithStateDestination|CreateWorktreeAtStartPoint|HeadCommit|ForkWithState|Issue1029" -race -count=1
go test ./cmd/agent-deck/... -run "SessionFork_WithState" -race -count=1
go test ./internal/ui/... -run "ForkDialog_(WithState|ToggleWith|ToggleGitignored|ToggleWorktreeOff|Focus_|View_|Space_|Y_Toggles|I_Toggles|I_Typeable|WorktreeControlsVisible)|ForkWithStateWorktree_|ForkSessionCmdWithOptions_(AcceptsForkState|WithStateRejectsNonGit)|ForkDialogSubmitCapturesWithStateBeforeHide" -race -count=1
go test -tags eval_smoke ./tests/eval/session/... ./internal/ui/... -run "TestEval_SessionForkWithState|TestEval_ForkDialog_WithState" -race -count=1
```

**With-state is git-only.** Both the CLI (closed in #1263) and the forthcoming TUI surface must reject with-state on a non-git backend. PR-B's TUI mandate therefore additionally requires a **jujutsu-rejection** test: a TUI with-state submit against a non-git backend (`backend.Type() != vcs.TypeGit`) must be refused with the "--with-state is only supported for git repositories" message, mirroring the CLI early guard.

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

- Replacing the PR-A/PR-B decomposed with-state path with wrapper-based worktree creation, or changing the non-state `CreateWorktreeWithStateAndSetup` wrapper pattern
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
- 2026-06-03: FUS-003 — Reconciliation audit after PR-A merged as #1263 (merge commit `5dc3e912`). Recorded that a VCS backend abstraction (`internal/vcs`, `internal/vcsbackend`, `internal/jujutsu`, `cmd/agent-deck/vcs_helper.go`) and #1277's repo-root `MaterializeWipFromParent` change landed on main. Updated the spec to reflect the as-merged CLI with-state shape: DECOMPOSED (`CreateWorktreeAtStartPoint` + `MaterializeWipFromParent` + `RunWorktreeSetupAfterCreate`) and backend-routed rather than a single-wrapper call, with the with-state path held git-only behind an early `backend.Type()==vcs.TypeGit` guard (this early guard, not call routing, is the jujutsu-safety mechanism). Marked G2, G3, and G4-CLI CLOSED; re-scoped remaining work to PR-B (TUI), G1 + G10-TUI, and added a jujutsu-rejection test to the TUI mandate.
- 2026-06-04: FUS-004 — Reconciled the remaining spec references with the rewritten PR-B plan: TUI with-state creation is now explicitly decomposed and git-only behind a backend guard, not wrapper-based. Updated PR-B status to `worktree/fork-state-tui-followup` and aligned the documented toolchain with `go.mod`'s Go 1.25.11 requirement.
- 2026-06-04: FUS-005 — Reconciled PR-A spec references against the checked-in code. Updated `HeadCommit`/validator descriptions to match stdout-only HEAD resolution, collision kind constants, `GetWorktreeForBranch` error propagation, branch-prefix/path-exists guards, the actual parent-untouched test name, and the expanded CLI eval suite that landed with PR #1263.
- 2026-06-04: FUS-006 — Recorded the four PR-B UI design decisions in G1 (full detail in the plan's PR-B "DESIGN DECISIONS" block): static "creates a NEW branch at parent HEAD" hint + reject-at-submit collision UX; labels `Carry parent state` / `Include gitignored files`; `y`/`i` shortcuts retained; checkboxes as focus targets toggled with `Space`/`Enter` (the B2 focus-target refactor is now required).
- 2026-06-04: FUS-007 — Executed doc-3 Task 5: reconciled the mandate UI regex in the plan + spec to the as-implemented PR-B test names (33 tests) — replaced the never-matching `GitignoredRequires`/`Toggling`/`FocusOrder` alternations with `ToggleWith`/`ToggleGitignored`/`ToggleWorktreeOff`/`Focus_`/`View_`/`Space_`/`Y_Toggles`/`I_Toggles`/`I_Typeable`/`WorktreeControlsVisible`/`ForkWithStateWorktree_`/`ForkSessionCmdWithOptions_(AcceptsForkState|WithStateRejectsNonGit)`/`ForkDialogSubmitCapturesWithStateBeforeHide`. Expanded B4 item 5 to the full implemented sequence. Marked PR-B B1–B5 and doc-3 Tasks 1–5 complete (commits f636a301, 1914a2da, ecb91357, 023ca226, 4f18a4ac).
