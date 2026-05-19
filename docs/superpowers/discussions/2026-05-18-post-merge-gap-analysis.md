# Post-Merge Gap Analysis vs Our Spec

**Date:** 2026-05-18
**Compared:** upstream/main commit `6a1645eb` (PR #1030, closes #1029) ↔ `docs/superpowers/specs/2026-05-14-fork-worktree-with-state-design.md` (now deprecated)
**Author:** Track A session (post-FWS-017 + FWS-018)
**Now planned in:** [`../plans/2026-05-18-fork-with-state-followup.md`](../plans/2026-05-18-fork-with-state-followup.md) — the 11 gaps are mapped to PR-A (correctness + CLI test hardening) and PR-B (TUI integration). Active spec is [`../specs/2026-05-18-fork-with-state-followup-design.md`](../specs/2026-05-18-fork-with-state-followup-design.md).

## Sanity check first

- Track B analyzed snapshot `02b6e5c4`.
- Upstream merged version is `6a1645eb`.
- `git diff --stat 02b6e5c4 6a1645eb` → empty. Trees are identical.
- Track B's FWS-017 findings are still 100% accurate against the merged state.
- Two post-merge commits touch `internal/ui/home.go` (#1034 emacs nav, #1035 reorder keys) but neither touches the fork submit handler or `forkdialog.go`. Implementation-time line-number drift only.

All four FWS-017 corrections are present in upstream:
1. ✓ `revert` in refusal list (`REVERT_HEAD` check)
2. ✓ NUL-safe listing (`runListZ` with `-z`)
3. ✓ Two-pass union for gitignored (non-ignored + ignored)
4. ✓ Materialize before ProcessWorktreeInclude before setup hook

## What upstream merged (architecture)

### Files added
- `internal/git/materialize_wip.go` (224 lines, single file)
- `internal/git/issue1029_edge_test.go` (233 lines)
- `internal/git/issue1029_with_state_test.go` (133 lines)

### Files modified
- `internal/git/setup.go` (+26 lines): adds `WorktreeStateOptions` struct + `CreateWorktreeWithStateAndSetup` wrapper; `CreateWorktreeWithSetup` becomes a thin pass-through.
- `cmd/agent-deck/session_cmd.go` (+16 lines): two flags, validation, wires to new wrapper.

### Public API
```go
// internal/git/materialize_wip.go
func MaterializeWipFromParent(parentDir, childDir string, includeIgnored bool) error

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

// Original signature preserved as thin wrapper:
func CreateWorktreeWithSetup(...) // calls CreateWorktreeWithStateAndSetup with empty state
```

### Architectural pattern
Upstream uses a **wrapper-and-recompose** pattern: `CreateWorktreeWithStateAndSetup` orchestrates Create → Materialize → ProcessWorktreeInclude → Setup all inside one helper. Caller (CLI) just calls the wrapper with options.

Our spec uses a **split-and-recompose** pattern: `CreateWorktree` + `RunWorktreeSetup` exposed separately so callers can interleave materialization between them. Each surface (CLI, TUI) does its own orchestration.

Both are valid. Upstream's is simpler caller code; ours allows surface-specific variation (which we use for TUI's different error handling). Going forward, the right move is to **adopt upstream's wrapper pattern as the base** and add our extras on top (typed errors, parent-HEAD start point, destination validator, TUI integration).

### Internal helpers (not exported)
- `refuseUnsafeParentState(parentDir)` — preflight, returns plain `error` formatted with parent kind. Not typed; no shared CLI/TUI use.
- `gitDirOf(dir)`
- `applyDiffFromParent(parentDir, childDir, cached bool)` — uses `git apply --index` (not `--cached`)
- `copyUntrackedFromParent(parentDir, childDir, includeIgnored bool)` — two-pass for gitignored
- `runListZ(args...)` — NUL-safe ls-files
- `copyEachFile`, `copyOneFile` — symlink-aware, mode-preserving

### Tests in upstream
Per the merged commit message:
1. canonical staged+unstaged+untracked
2. empty WIP (no-op)
3. binary file
4. symlink
5. ignored opt-in
6. mid-merge refusal
7. deleted-in-parent tracked file
8. `CreateWorktreeWithStateAndSetup` wiring

## Gap table (severity-sorted)

| # | Gap | Severity | Status in upstream | Effort to fix |
|---|---|---|---|---|
| **1** | **TUI integration** — no `ForkDialog` sub-checkboxes, no `internal/ui/` changes | High — user-facing parity | Missing | Medium (Tasks 15, 15A, 16, 17, 17A) |
| **2** | **Parent-HEAD start point for linked parent worktrees** — upstream uses `CreateWorktree(repoDir, ...)`, which creates from invocation dir's HEAD, not the parent session's HEAD. When parent is in a linked worktree whose HEAD differs from main, fork starts from wrong commit and materialization produces wrong-base diffs | High — silent correctness bug for linked-worktree parents | Missing | Small (Task 4A — new `HeadCommit` + `CreateWorktreeAtStartPoint`) |
| **3** | **Destination collision validation** — upstream has no check before materialization. If `-w <existing-branch>` is passed with `--with-state`, behavior is undefined: either git refuses worktree add (if branch already has a worktree elsewhere) or silently creates a new worktree on the existing branch, then materializes on top. No "branch already has a worktree at X" error. No "branch already exists" error | High — silent reuse / cryptic error | Missing | Small (Task 4C — new `ValidateForkWithStateDestination`) |
| **4** | **Cleanup-on-error** — if `MaterializeWipFromParent` fails, upstream leaves the partially-created worktree on disk. The user must manually `git worktree remove` and `git branch -D` to recover | Medium — leaves orphaned state | Missing | Small (a `defer` cleanup block in the CLI handler or wrapper) |
| **5** | **Parent-untouched invariant test** — no test asserts that parent's `git status --porcelain`, index, and stash list are byte-identical after materialize | Medium — would catch regressions to parent-read-only contract | Missing | Small (one test) |
| **6** | **Bare-repo + linked parent worktree test** — covers the layout where the project root is `.bare/` and the parent session is in a linked worktree | Medium — common agent-deck layout | Missing | Small (one test) |
| **7** | **Setup-hook ordering test with parent-WIP observation** — upstream has a "wiring" test but it doesn't explicitly assert the setup hook OBSERVES the materialized WIP (e.g., by writing a fingerprint of a parent's WIP file into a marker) | Medium — ordering contract | Wiring test exists; observation not verified | Small (one test, refine existing) |
| **8** | **Mid-op refusal tests for all five kinds** — upstream tests only mid-merge refusal. Mid-rebase, mid-cherry-pick, mid-revert, mid-bisect refusals are implemented but not regression-tested | Low | Partially tested (1 of 5) | Trivial (4 tests) |
| **9** | **CLI contract test via before-start hook** — upstream has no test that runs the actual CLI command path with a hook to inspect option propagation (e.g., that `ClaudeOptions.WithState` is true before `Start()`) | Low | Missing | Small (Task 12A) |
| **10** | **Behavioral eval smoke (CLI + TUI)** — no eval-tagged tests for the user-visible CLI fork or the TUI ForkDialog visible interaction | Medium — CLAUDE.md eval-harness mandate class | Missing | Small (Task 17A: 2 eval tests) |
| **11** | **Shared `PreflightForkWithState` typed-error helper** — upstream's `refuseUnsafeParentState` is internal, lowercase, returns formatted `error`. CLI and TUI can't share it as a separate preflight gate with typed `InProgressOperationError` | Medium — prevents CLI/TUI drift; lets surfaces customize error rendering | Equivalent inline behavior exists; not extracted/shared | Medium (Task 4B, RFC-required structural change per our spec) |

## Things upstream has that our spec did NOT anticipate

| What | Worth absorbing into our spec? |
|---|---|
| `CreateWorktreeWithStateAndSetup` wrapper pattern (vs our split pattern) | Yes — adopt as the base for our follow-up, simplifies caller code |
| `WorktreeStateOptions{WithState, WithIgnored}` options struct | Yes — cleaner than passing two booleans |
| Internal `applyDiffFromParent` factoring (passes cached as a bool parameter, single function for both staged + unstaged) | Yes — simpler than our spec's two-step inline approach |
| `gitDirOf` helper (vs our `resolveGitDir`) | Equivalent; pick one name |
| `copyEachFile` / `copyOneFile` split | Yes — readable |

## Implications for our plan

Our plan was written assuming a from-scratch implementation. With upstream's PR merged, large chunks of the plan are now either **duplicate** or **rebase-target** rather than **net-new work**.

### Tasks that mostly duplicate upstream
- **Task 5-11** (MaterializeParentState + all scenarios) — upstream's `MaterializeWipFromParent` covers most of this. Repurpose these as test-additions (parent-untouched invariant, partial-staged-with-deletion, etc.) rather than implementation.
- **Task 2, 3** (DetectInProgressOperation, HasSubmodules) — upstream has equivalent inline `refuseUnsafeParentState`. Our extraction is part of the FWS-009 shared-preflight goal (still valuable but RFC-gated per our spec).
- **Task 4** (split `CreateWorktreeWithSetup`) — superseded by upstream's `CreateWorktreeWithStateAndSetup` wrapper. Our split would be a structural change requiring an RFC per upstream's design.

### Tasks that remain net-new work (real follow-up scope)
- **Task 4A** (parent-HEAD start point) — fixes gap 2. **High priority.**
- **Task 4C** (destination collision validator) — fixes gap 3. **High priority.**
- **Task 12A, 13 partial** (CLI before-start hook, refusal-message refinement) — fixes gap 9.
- **Tasks 14 partial** (parent-untouched, bare-repo, setup-hook-observation) — fixes gaps 5, 6, 7.
- **Tasks 15, 15A, 16, 17** (TUI integration) — fixes gap 1. **High priority.**
- **Task 17A** (behavioral eval smoke) — fixes gap 10.

### Tasks that need re-anchoring to upstream's API
- **Task 13** (CLI wiring) — already done by upstream's commit. Our follow-up changes the CLI handler to add cleanup-on-error, the parent-HEAD start point, the destination validator call. ~10-20 lines, not the 150+ in our current Task 13.
- **Task 17** (TUI submit handler) — wraps upstream's `CreateWorktreeWithStateAndSetup` instead of orchestrating Create/Materialize/Setup ourselves.

## Recommended follow-up PR shape

Three small PRs, in priority order:

### PR-A: Correctness fixes (gaps 1, 2, 3, 4)
- TUI integration (gap 1)
- Parent-HEAD start point via `CreateWorktreeAtStartPoint` (gap 2)
- Destination collision validation via `ValidateForkWithStateDestination` (gap 3)
- Cleanup-on-error guard in the CLI handler and TUI submit (gap 4)
- Wraps upstream's `CreateWorktreeWithStateAndSetup`; does not replace it

Estimated size: ~600 lines of code (mostly TUI), ~12 tests.

### PR-B: Test coverage (gaps 5, 6, 7, 8, 9, 10)
- Parent-untouched invariant
- Bare-repo + linked parent worktree
- Setup-hook observation
- All four missing mid-op refusal tests (rebase, cherry-pick, revert, bisect)
- CLI before-start hook
- Behavioral eval smoke (CLI + TUI)

Estimated size: ~300 lines of tests + 1 small helper.

### PR-C: Shared preflight helpers (gap 11) — RFC required
- Extract `PreflightForkWithState` and `ValidateForkWithStateDestination` as public API
- Add typed `InProgressOperationError` and `DestinationCollisionError`
- Promote `refuseUnsafeParentState` to `DetectInProgressOperation` + `PreflightForkWithState`
- Migrate CLI and TUI to use the shared helpers
- Per our spec's "Structural changes requiring RFC", this needs upstream RFC approval

PR-C is the most invasive — it changes upstream's just-merged internal API. Best discussed via RFC issue first.

## What to do with our current spec/plan

Our spec and plan stay valuable as the **target architecture** (what fork-with-state SHOULD look like long-term). They're not the implementation blueprint anymore. Two paths:

1. **Keep them as-is** and treat them as the architectural reference. PR-A, PR-B, PR-C cite the spec for design rationale. The plan's tasks get reorganized into the three PRs (some dropped, some kept).

2. **Refactor the plan** into PR-shaped task lists. More work upfront but easier execution.

Recommend (1) for now, refactor only if we commit to the PR-A scope.

## Quick action items if user picks PR-A

1. Squash our current 5-commit `feature/fork-worktree-with-state` branch into ONE commit: "design and plan for fork-with-state follow-up work" (a docs-only commit).
2. Create a new branch off `upstream/main` for PR-A.
3. Cherry-pick relevant plan tasks (4A, 4C, 12A partial, 13 partial, 14 partial, 15, 15A, 16, 17, 17A) into the new branch — but write the code on top of upstream's `MaterializeWipFromParent` / `CreateWorktreeWithStateAndSetup` rather than the from-scratch versions in the plan.
4. Push to `origin` (smorin/agent-deck).
5. Open PR-A against `upstream/main` referencing #1029, #1030, and the spec/plan.

## Note on FWS-017's claims

Our spec's FWS-017 review log entry says the corrections were "folded back from upstream's implementation." That phrasing is accurate. The spec is now consistent with the merged state on those four dimensions (revert, NUL-safe, two-pass ignored, materialize-before-include).
