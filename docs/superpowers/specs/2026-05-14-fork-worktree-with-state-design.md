# Fork-with-State: Carry Parent Working-Tree Contents into a New Worktree

> **⚠ DEPRECATED — superseded by [`2026-05-18-fork-with-state-followup-design.md`](2026-05-18-fork-with-state-followup-design.md)**
>
> This document was written as a ground-up design before upstream merged #1029
> (commit 6a1645eb). It remains as historical reference for the design
> reasoning, decision log, and FWS-001 through FWS-018 entries. The active
> design is now in the followup file. See also the post-merge gap analysis at
> [`../discussions/2026-05-18-post-merge-gap-analysis.md`](../discussions/2026-05-18-post-merge-gap-analysis.md).

**Status:** Deprecated 2026-05-18 — historical reference only
**Date:** 2026-05-14
**Author:** Steve Morin (steve.morin@gmail.com)
**Related code:**
- `cmd/agent-deck/session_cmd.go` (fork CLI handler)
- `internal/ui/forkdialog.go` (TUI fork dialog)
- `internal/git/git.go`, `internal/git/setup.go` (worktree plumbing)
- `internal/session/instance.go` (`CreateForkedInstanceWithOptions`)

## Problem

Today `agent-deck session fork` has two modes:

1. **Without `-w`:** the forked Claude session inherits the parent's `ProjectPath` verbatim. Two Claude sessions end up sharing the same working directory — a silent footgun, especially when the parent already lives in a worktree.
2. **With `-w <branch>`:** a new worktree is created at the tip of the named branch (or off the default branch with `-b`). The new worktree is **clean** — none of the parent's uncommitted changes, staged hunks, or untracked files are carried over.

For active development sessions, the second mode misses what users usually want: forking the parent *as it currently looks on disk*, including in-progress edits, staged hunks, and new untracked files Claude just created.

## Goal

Add an opt-in mode that creates a **complete parallel agent environment**: a new destination branch/worktree whose working tree exactly mirrors the parent's at the moment of fork — preserving the staged/unstaged split, untracked files, and (optionally) gitignored files — AND a forked Claude Code session whose conversation history continues from the parent session via the existing `agent-deck session fork` mechanism. The parent repo must be left byte-identical after the operation.

The two pieces compose: file materialization (this feature's new code) sits on top of session forking (the existing `agent-deck session fork` flow that calls `claude --resume <parent-id> --fork-session`). Together they let a user reach an interesting point in a task, then explore multiple agent paths in parallel from the exact same WIP baseline with full conversation continuity — not just file copies started from a blank session.

## Non-goals

- Replacing the existing `-w` behavior. The clean-branch path stays as-is for users who want it.
- Auto-detecting "parent is in a worktree" and forcing the new mode. Behavior is purely opt-in.
- Supporting parent repos in mid-rebase / mid-merge / mid-cherry-pick / mid-revert / mid-bisect. These are refused with actionable errors.
- Submodule recursion. Submodule states are copied as files, not recursed into.
- Quiescing parent's tmux session during materialization. Parent edits during the fork are accepted as staleness.
- Size caps on gitignored copies. If the user typed `--with-state-and-gitignored`, we trust them.

## User-facing surface

### CLI

```
agent-deck session fork <id|title> [existing options] \
    -w, --worktree <new-branch>      (existing) create a worktree; required destination branch for --with-state
    -b, --new-branch                 (existing) create the branch if it doesn't exist; not required for --with-state
    --with-state                     NEW: carry parent's tracked + staged + untracked state.
                                          CLI requires --worktree <new-branch>; no hidden branch suggestion.
    --with-state-and-gitignored      NEW: also copy gitignored files. Implies --with-state.
```

**CLI destination contract:** `--with-state-and-gitignored` → `--with-state`, and `--with-state` requires `-w/--worktree <new-branch>`. The CLI does not auto-name the destination branch. In fork-with-state mode, the `-w` value names a new destination branch from the parent session's HEAD; `-b` is not required and existing destination branches/worktrees are refused.

### TUI (`ForkDialog`)

Under the existing "Create in worktree" checkbox, two new nested checkboxes appear when the worktree checkbox is on:

```
[x] Create in worktree (press w)
    Branch: fork/my-session
    [ ] Carry parent state (press y)
        [ ] Include gitignored files (press i)    ← only visible when "Carry parent state" is on
```

The TUI may suggest/prefill `fork/<sanitized-session>` in the branch field because the user can see and edit it before submit. That field is always the destination branch name, not the source branch.

Focus order: name → group → wt-checkbox → branch → with-state-checkbox → with-state-gitignored-checkbox → conductor → options panel.

## Design

### Architecture

| Layer | New | Notes |
|---|---|---|
| CLI handler (`session_cmd.go`) | New flag parsing + implication resolution + cleanup-on-error guard | |
| TUI dialog (`forkdialog.go`) | Two new bool fields, two new checkboxes, two new exported getters | |
| Session options (`session.ClaudeOptions`) | Two new transient fields `WithState`, `IncludeGitignored` | Not persisted to disk; consumed during fork startup only |
| Git plumbing (`internal/git/`) | New file `worktree_with_state.go` with shared `PreflightForkWithState` and `ValidateForkWithStateDestination` helpers; start-point-aware worktree helper; split of `CreateWorktreeWithSetup` into `CreateWorktree` + `RunWorktreeSetup`; integration with existing `ProcessWorktreeInclude` (PR #890) preserves the materialize → worktreeinclude → setup ordering | |
| `Instance.CreateForkedInstanceWithOptions` | No signature change | Materialization happens in the CLI/TUI handler, before this is called |

### New git helper layer (`internal/git/worktree_with_state.go`)

```go
type StateCopyOptions struct {
    IncludeGitignored bool
}

type StateCopyResult struct {
    TrackedFilesPatched   int
    UntrackedFilesCopied  int
    GitignoredFilesCopied int
}

// MaterializeParentState copies parent's staged + unstaged + untracked
// (and optionally gitignored) into newWorktree. Read-only on parentWorktree.
// Caller is responsible for the worktree already existing at newWorktree.
func MaterializeParentState(parentWorktree, newWorktree string, opts StateCopyOptions) (*StateCopyResult, error)

// DetectInProgressOperation returns "rebase", "merge", "cherry-pick", "revert",
// "bisect", or "". Used as a pre-flight refusal check. Each kind is detected via
// the presence of the corresponding state file in .git: MERGE_HEAD,
// CHERRY_PICK_HEAD, REVERT_HEAD, BISECT_LOG, or the rebase-merge/rebase-apply
// directories.
func DetectInProgressOperation(repoDir string) (kind string, err error)

// HasSubmodules returns true if .gitmodules exists. Used to emit a warning,
// not to refuse the operation.
func HasSubmodules(repoDir string) (bool, error)

type ForkWithStatePreflight struct {
    ParentHead    string
    HasSubmodules bool
}

// InProgressOperationError is returned by PreflightForkWithState when the
// parent is mid-rebase, mid-merge, mid-cherry-pick, mid-revert, or mid-bisect.
// CLI and TUI callers own the final user-facing wording.
type InProgressOperationError struct {
    Kind    string
    RepoDir string
}

func (e *InProgressOperationError) Error() string

// PreflightForkWithState is the shared CLI/TUI fork-with-state gate. It
// refuses unsupported in-progress operations, captures the parent HEAD start
// point, and returns submodule presence as a warning fact for callers to render.
func PreflightForkWithState(parentWorktree string) (*ForkWithStatePreflight, error)

// DestinationCollisionError is returned by ValidateForkWithStateDestination when
// the requested destination branch already exists locally or already has a
// worktree. CLI and TUI callers own the final user-facing wording.
type DestinationCollisionError struct {
    Kind   string // "worktree_exists" or "branch_exists"
    Branch string
    Path   string // populated when Kind == "worktree_exists"
}

func (e *DestinationCollisionError) Error() string

// ValidateForkWithStateDestination is the shared CLI/TUI destination collision
// gate for fork-with-state. It returns a typed DestinationCollisionError when
// the destination branch already has a worktree or already exists as a local
// branch. Both checks are git facts; callers format the error appropriately.
// Order matters: worktree-collision is checked first so the more specific error
// (with path) is surfaced when both conditions are true.
func ValidateForkWithStateDestination(repoRoot, branch string) error
```

### Split of `CreateWorktreeWithSetup`

The existing `internal/git/setup.go:CreateWorktreeWithSetup` is currently atomic: it runs `git worktree add` then the setup hook. To slot materialization between these steps, we split it:

```go
// Existing: creates a worktree from the invocation repo's current HEAD, no setup.
func CreateWorktree(repoDir, worktreePath, branch string) error

// New: creates a worktree from an explicit start point, no setup.
// The fork-with-state path passes the parent session's HEAD here so linked
// parent worktrees fork from the parent worktree commit, not the main worktree.
func CreateWorktreeAtStartPoint(repoDir, worktreePath, branch, startPoint string) (createdBranch bool, err error)

// New: discovers the user's setup hook from repoDir, then runs it against an
// existing worktree. Returns the script's exit error; callers keep treating
// setup failures as warnings.
func RunWorktreeSetup(repoDir, worktreePath string, stdout, stderr io.Writer, timeout time.Duration) error

// Existing function becomes a thin wrapper preserving backward compatibility:
func CreateWorktreeWithSetup(...) (setupErr error, err error) {
    if err := CreateWorktree(...); err != nil { return nil, err }
    return RunWorktreeSetup(repoDir, worktreePath, stdout, stderr, timeout), nil
}
```

The fork-with-state path calls `CreateWorktreeAtStartPoint` → `MaterializeParentState` → `RunWorktreeSetup(repoRoot, worktreePath, ...)` directly. All other existing callers keep using `CreateWorktreeWithSetup`.

**Rationale:** the setup hook (e.g., `npm install`, `uv sync`) is the user's "prepare this worktree for work" script. It needs to see the final file contents — including parent's WIP. If we materialized *after* setup, a parent with a new dependency in `package.json` would yield a worktree with new `package.json` but old `node_modules`. The API keeps both `repoDir` and `worktreePath` because setup scripts are discovered from `<repoDir>/.agent-deck/worktree-setup.sh`, but executed with the new worktree as the current working directory.

### Data flow

```
Step 1. Parse + resolve
  - Resolve session id/title → *session.Instance (parent)
  - Apply implication chain: gitignored → with-state
  - CLI: if with-state is set and no `-w/--worktree <new-branch>` was supplied, refuse with an actionable error
  - TUI: use the visible/editable branch-field suggestion as the destination branch
  - Validate parent is a Claude session and CanFork()

Step 2. Resolve branch + path and run destination collision checks
  - Apply wtSettings.ApplyBranchPrefix() to the destination branch
  - If with-state: call git.ValidateForkWithStateDestination(repoRoot, branch); refuse with a typed DestinationCollisionError if the destination branch already has a worktree or already exists
  - Compute worktree path via git.WorktreePath() with wtSettings.Template
  - Existing worktree reuse remains unchanged for normal, non-with-state `-w` (with-state cannot reach the reuse block because the shared validator refused first)

Step 3. Pre-flight on parent's git state
  - Call git.PreflightForkWithState(parent.ProjectPath)
  - If it returns InProgressOperationError for rebase, merge, cherry-pick, revert, or bisect: refuse with an actionable error
  - If it returns another error: refuse before creating the destination worktree
  - If preflight.HasSubmodules is true: log a warning ("submodules will be copied as-is, not recursed")
  - Use preflight.ParentHead as the worktree start point
  - Step 2 runs before Step 3 because destination collision validation is local-state (cheap) and preflight shells out to git; the structural-guard test only enforces preflight before CreateWorktreeAtStartPoint, not before the collision check

Step 4. Create worktree (no setup yet)
  - git.CreateWorktreeAtStartPoint(repoRoot, worktreePath, branch, parentHead)
  - This is required when the parent session already lives in a linked worktree whose HEAD differs from the main/base worktree.
  - On error: abort, nothing to clean up

Step 5. Materialize parent state                                        NEW
  - git.MaterializeParentState(parent.ProjectPath, worktreePath, opts)
    Internally:
      a. git -C parent diff --cached --binary | git -C newWorktree apply --index
         (updates the new worktree's index and working tree to the parent's staged state)
      b. git -C parent diff --binary          | git -C newWorktree apply
         (applies the parent's unstaged delta on top of the staged working-tree content)
      c. git -C parent ls-files --others -z --exclude-standard
         (NUL-separated to safely handle paths with embedded newlines)
         → copy each parent/<f> → newWorktree/<f> preserving mode
      d. if IncludeGitignored, run a SECOND pass and union with the first:
         git -C parent ls-files --others -z --ignored --exclude-standard
         (Important: `--ignored` is a FILTER on the listing, not additive. With
          `--exclude-standard`, the flag flips the listing to ONLY ignored files
          and excludes non-ignored ones. To capture non-ignored untracked AND
          ignored untracked, two passes must be run and unioned.)
         → copy with mode preserved, no size cap
  - On any failure: return error to caller (cleanup happens in Step 7)

Step 5.5. Run worktreeinclude file copying (existing feature from PR #890)
  - git.ProcessWorktreeInclude(repoRoot, worktreePath, stderr)
  - Reads `.worktreeinclude` (or `.agent-deck/worktreeinclude`) from `repoRoot` and copies the listed files into `worktreePath`.
  - Today this runs unconditionally as part of the worktree-creation flow; the fork-with-state sequence must preserve that ordering: materialize FIRST so worktreeinclude sees the materialized parent state, then setup script sees the result of both.
  - worktreeinclude failures are warnings, not errors (matches existing behavior).

Step 6. Run setup hook                                                  CHANGED
  - git.RunWorktreeSetup(repoRoot, worktreePath, stdout, stderr, timeout)
  - `repoRoot` is used for setup-script discovery; `worktreePath` is the execution cwd
  - Setup-hook failures stay warnings (matches current session_cmd.go:740)

Step 7. Cleanup-on-error guard                                          NEW
  - If steps 4-6 succeed, no-op.
  - If step 5 fails:
      git -C repoRoot worktree remove --force worktreePath
      git -C repoRoot branch -D <branch>   (only if branch creation is proven for this operation)
    Then surface the original error to the user.

Step 8. Construct in-memory forked Instance
  - opts.WorkDir / WorktreePath / WorktreeRepoRoot / WorktreeBranch set as today
  - opts.WithState / opts.IncludeGitignored remain in-memory only
  - inst.CreateForkedInstanceWithOptions(forkTitle, forkGroup, opts)

Step 9. Start + capture session id + persist
  - Unchanged from today (forkedInst.Start + PostStartSync + SaveWithGroups)
```

### Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Default behavior | Opt-in only | Don't change existing fork semantics. Footgun in current code (silent shared worktree) is not addressed by this feature; out of scope. |
| Destination branch | CLI requires explicit `-w/--worktree <new-branch>`; TUI may prefill `fork/<sanitized-session>` | CLI should not create a hidden name. TUI suggestions are visible and editable before submit. |
| Source shape | Parent session may be on an existing branch, a linked worktree, or detached HEAD | Fork-with-state uses the parent session's resolved `HEAD` commit as the start point, independent of source branch/worktree shape. |
| Existing worktree reuse | Refuse for with-state; unchanged for normal `-w` | A with-state fork must create a fresh destination branch/worktree. Reusing an existing worktree can silently skip materialization or overwrite unrelated state. |
| State scope | tracked-modified + staged + untracked, gitignored opt-in | Most-common need. Gitignored often means node_modules / .env — opt-in protects against accidents. |
| Index fidelity | Preserve staged vs. unstaged split | Two-stage `git apply --index` then `git apply`. The staged patch must update both the index and working tree so partially-staged files and staged deletions remain faithful before the unstaged patch is applied. |
| In-progress ops | Refuse rebase, merge, cherry-pick, revert, bisect | Cleanest semantics; ship v1 smaller. Revert leaves `REVERT_HEAD` and conflicted files exactly like merge does, so it gets the same refusal. |
| Submodules | Best-effort copy, no recursion | Warn but don't refuse. |
| LFS | Treat as regular file copy (LFS pointer files self-handle) | Standard LFS behavior. |
| Gitignored size cap | None | User who typed `--with-state-and-gitignored` opted in explicitly. Cap can be added later if needed. |
| Failure handling | Abort + cleanup | Atomic mental model: fork either happens or it doesn't. |
| Branch cleanup proof | Delete branch only when this operation proves it created the branch | The new cleanup path must not key off intent flags alone; an existing branch must never be deleted after a materialization failure. |
| Parent safety | Read-only on parent (`git diff`, `ls-files`, file reads — no `git stash`, no `git add`) | Parent's index, working tree, stash list, and `.git` must be byte-identical after fork. |
| Race vs. parent writes | Accept staleness, document it | Materialization is not atomic. Fork captures parent state at moment-of-fork. |
| Setup-hook ordering | Materialize → worktreeinclude → setup script | Setup hook sees parent's final state and can react to it (e.g., install new deps from WIP package.json). `ProcessWorktreeInclude` from PR #890 also runs after materialization so it can copy auto-included files on top of the materialized state without losing parent WIP. The chain is: `CreateWorktree → MaterializeParentState → ProcessWorktreeInclude → RunWorktreeSetup`. |

## Errors and cleanup

### Cleanup matrix

| Failure point | Cleanup action |
|---|---|
| Destination collision check (Step 2) | None — early exit |
| Pre-flight (Step 3) | None — early exit |
| `CreateWorktree` (Step 4) | None — git rolls back its own failed `worktree add` |
| Materialization (Step 5) | `git worktree remove --force <path>`, then `git branch -D <branch>` only when branch creation is proven for this operation |
| `ProcessWorktreeInclude` (Step 5.5) | None — already a non-fatal warning today (matches existing PR #890 behavior) |
| Setup hook (Step 6) | None — already a non-fatal warning today |
| `Instance.Start` (Step 9) | None — leave worktree for inspection (matches today's behavior) |

### Error message catalog

```
"parent session is mid-rebase; finish or abort the rebase before forking with state (cd <parent-path> && git rebase --abort)"
"parent session is mid-merge; resolve or abort the merge before forking with state"
"parent session is mid-cherry-pick; finish or abort before forking with state"
"parent session is mid-revert; finish or abort the revert before forking with state (cd <parent-path> && git revert --abort)"
"parent session is mid-bisect; run 'git bisect reset' in <parent-path> before forking with state"
"--with-state requires --worktree <new-branch>"
"branch '<branch>' already exists; choose a new destination branch for --with-state"
"branch '<branch>' already has a worktree at <path>; choose a new destination branch for --with-state"
"failed to apply parent's staged changes: <git error>; new worktree cleaned up"
"failed to apply parent's unstaged changes: <git error>; new worktree cleaned up"
"failed to copy untracked file <path>: <error>; new worktree cleaned up"
```

Warnings (non-fatal, written to stderr, fork proceeds):

```
"submodules detected — copied as files, not recursed (parent's submodule states preserved)"
"setup script failed: <error>" (existing behavior)
```

## Testing

Following CLAUDE.md "TDD always" — regression tests for the contract land before implementation.

### Unit tests — `internal/git/worktree_with_state_test.go` (new)

| Test | Asserts |
|---|---|
| `TestMaterialize_TrackedUnstaged` | new worktree has same file contents; `git status` shows unstaged |
| `TestMaterialize_TrackedStaged` | new worktree's index matches; `git diff --cached` reproduces parent's patch |
| `TestMaterialize_PartiallyStaged` | partial staging preserved exactly |
| `TestMaterialize_Untracked` | untracked files present in new worktree with mode preserved (incl. exec bit) |
| `TestMaterialize_UntrackedGitignored_Excluded` | default opts → gitignored files NOT copied |
| `TestMaterialize_UntrackedGitignored_Included` | with `IncludeGitignored: true` → gitignored files copied |
| `TestMaterialize_BinaryFiles` | modified binary file bytes match (verified via sha256) |
| `TestMaterialize_DeletedFromIndex` | staged deletion preserved in new index |
| `TestMaterialize_NoChanges` | clean parent → clean new worktree, no error |
| `TestMaterialize_ParentUntouched` | after materialize, parent's `git status`, index, stash list byte-identical |
| `TestMaterialize_SymlinkInWorkingTree` | symlinks copied as symlinks with correct target |
| `TestMaterialize_FileWithExecBit` | exec bit preserved |
| `TestDetect_Rebase` | mid-rebase parent → returns "rebase" |
| `TestDetect_Merge` | mid-merge parent → returns "merge" |
| `TestDetect_CherryPick` | mid-cherry-pick parent → returns "cherry-pick" |
| `TestDetect_Revert` | mid-revert parent (REVERT_HEAD present) → returns "revert" |
| `TestDetect_Bisect` | active bisect → returns "bisect" |
| `TestDetect_Clean` | normal repo → returns "" |
| `TestPreflightForkWithState_RefusesInProgressOperation` | shared CLI/TUI preflight returns `InProgressOperationError` before a destination worktree can be created |
| `TestPreflightForkWithState_ReturnsParentHeadAndSubmoduleFact` | shared preflight captures parent HEAD and returns submodule presence as a warning fact |
| `TestValidateForkWithStateDestination_Clean` | clean repo + fresh branch name → returns nil |
| `TestValidateForkWithStateDestination_BranchExists` | existing local branch with no worktree → returns `DestinationCollisionError{Kind: "branch_exists"}` |
| `TestValidateForkWithStateDestination_WorktreeExists` | branch already has a worktree → returns `DestinationCollisionError{Kind: "worktree_exists", Path: <worktree-path>}` |

### CLI contract tests — `cmd/agent-deck/session_cmd_fork_state_test.go`

| Test | Asserts |
|---|---|
| `TestSessionFork_WithStateRequiresExplicitDestinationBranch` | CLI `--with-state` without `-w <new-branch>` is refused; no auto-name |
| `TestSessionFork_WithStateAndGitignoredRequiresExplicitDestinationBranch` | CLI `--with-state-and-gitignored` also requires explicit `-w <new-branch>` |
| `TestSessionFork_WithState_RejectsExistingDestinationBranch` | with-state refuses an existing destination branch before materialization |
| `TestSessionFork_WithState_RejectsExistingDestinationWorktree` | with-state refuses destination branches that already have a worktree; normal `-w` reuse is unchanged |
| `TestSessionFork_WithStateAndGitignored_PropagatesOptionsBeforeStart` | actual handler path sets `ClaudeOptions.WithState` and `IncludeGitignored` before `Start()` |

### Git-side integration tests — `internal/git/worktree_with_state_integration_test.go`

| Test | Asserts |
|---|---|
| `TestForkWithStateGitSequence_CleanParent` | clean parent → clean new worktree, no error |
| `TestForkWithStateGitSequence_DirtyParent` | parent has staged + unstaged + untracked → fork mirrors all three |
| `TestForkWithStateGitSequence_WithGitignored` | parent has gitignored content → fork has it |
| `TestForkWithStateGitSequence_FailsWhenMidRebase` | parent mid-rebase → error, no worktree created |
| `TestForkWithStateGitSequence_CleansUpOnMaterializeFailure` | injected materialize failure → worktree and branch removed |
| `TestForkWithStateGitSequence_UsesParentWorktreeHead` | parent session in linked worktree with a different HEAD than main → fork HEAD equals parent HEAD |
| `TestForkWithStateGitSequence_BareRepoLayoutLinkedParentWorktree` | bare-repo layout with a linked parent worktree source → works; the bare repo/project root is repository metadata, not the state source |
| `TestForkWithStateGitSequence_MaterializesBeforeSetupHook` | setup hook sees materialized files (verified via a hook that reads a parent-WIP file) |

### TUI tests — `internal/ui/forkdialog_test.go` extension

State-machine tests (always-on, run via `go test ./internal/ui/...`):

| Test |
|---|
| `TestForkDialog_ToggleWithStateRequiresWorktree` |
| `TestForkDialog_TogglingWorktreeOffClearsWithState` |
| `TestForkDialog_WithStateSuggestsEditableDestinationBranch` |
| `TestForkDialog_FocusOrder` |

Rendering coverage (visible-checkbox, hidden-checkbox, nested-checkbox) is provided by the eval-tagged `TestEval_ForkDialog_WithStateVisibleInteraction` in `internal/ui/forkdialog_eval_test.go` (see "Behavioral eval smoke tests" below). That test drives the dialog through the `w → y → i` keystroke sequence and asserts `View()` output via substring checks against `"Carry parent state"` and `"Include gitignored files"`, covering all three render conditions in a single visible-interaction flow. The eval suite runs on every PR via `.github/workflows/eval-smoke.yml`; local TDD requires `-tags eval_smoke`.

### TUI submit-path tests — `internal/ui/home_fork_state_test.go` (new)

| Test | Asserts |
|---|---|
| `TestForkSessionCmdWithOptions_WithStateUsesSharedPreflight` | TUI fork-with-state submit path calls `git.PreflightForkWithState(source.ProjectPath)` before `CreateWorktreeAtStartPoint`, using a brace-counted structural guard that does not duplicate the lower-level preflight calls inline |

### Behavioral eval smoke tests

These cases satisfy the existing evaluator-harness mandate for user-observable behavior that ordinary Go unit tests cannot fully express.

| File | Test | Asserts |
|---|---|---|
| `tests/eval/session/fork_with_state_test.go` | `TestEval_SessionForkWithState_CreatesMaterializedWorktree` | Real `agent-deck` binary, scratch HOME, fake `claude`, and real git repo: `session fork --with-state-and-gitignored -w <new-branch>` creates a new destination worktree whose files on disk mirror the parent's staged/unstaged/untracked/gitignored state and starts the forked session in that destination cwd. |
| `internal/ui/forkdialog_eval_test.go` | `TestEval_ForkDialog_WithStateVisibleInteraction` | Colocated eval for the TUI-only surface: rendered `ForkDialog` shows the nested with-state and gitignored controls only after the user-visible `w -> y -> i` interaction path, and the getters report the submitted values. |

## Mandatory test coverage

This is the authoritative mandate for fork-with-state. Per CONTRIBUTING.md, `CLAUDE.md` is per-developer and not tracked in the repo, so the mandate is housed here (the tracked spec) and referenced by CI workflows. Test names are stable contracts; any PR that changes a path under the mandate MUST run and pass all four commands below.

```bash
go test ./internal/git/... -run "Materialize|DetectInProgress|HasSubmodules|PreflightForkWithState|ValidateForkWithStateDestination|CreateWorktreeAtStartPoint|HeadCommit|ForkWithStateGitSequence" -race -count=1
go test ./cmd/agent-deck/... -run "SessionFork_WithState|ResolveForkStateFlags" -race -count=1
go test ./internal/ui/... -run "ForkDialog_(WithState|ToggleWithState|GitignoredRequires|Toggling|FocusOrder)|ForkSessionCmdWithOptions_WithStateUsesSharedPreflight" -race -count=1
go test -tags eval_smoke ./tests/eval/session/... ./internal/ui/... -run "TestEval_SessionForkWithState|TestEval_ForkDialog_WithState" -race -count=1
```

### Paths under the mandate

- `internal/git/worktree_with_state.go` (+ `_test.go`)
- `internal/git/worktree_with_state_integration_test.go`
- `internal/git/git.go` — `HeadCommit` and `CreateWorktreeAtStartPoint`
- `internal/git/setup.go` — the `CreateWorktree` / `RunWorktreeSetup` / `CreateWorktreeWithSetup` split
- `cmd/agent-deck/session_cmd.go` — fork handler, `resolveForkStateFlags`
- `internal/ui/forkdialog.go` — with-state sub-checkboxes
- `internal/ui/home.go` — TUI fork submit handler
- `internal/ui/home_fork_state_test.go` — structural guard for shared TUI preflight
- `tests/eval/session/fork_with_state_test.go` — real-binary CLI behavioral eval
- `internal/ui/forkdialog_eval_test.go` — visible TUI ForkDialog behavioral eval
- `internal/session/tooloptions.go` — `WithState` / `IncludeGitignored` fields

### Structural changes requiring RFC

- Re-collapsing `CreateWorktree` + `RunWorktreeSetup` back into the old monolithic `CreateWorktreeWithSetup` (breaks materialization-before-setup ordering)
- Replacing shared `git.PreflightForkWithState` with separate CLI/TUI preflight implementations
- Replacing shared `git.ValidateForkWithStateDestination` with inline CLI/TUI collision checks, or reintroducing a with-state branch into the existing-worktree reuse blocks in `session_cmd.go` / `home.go`
- Mutating parent's index, working tree, or stash list as part of materialization (`git stash`, `git add`, etc.)
- Changing `--with-state-and-gitignored` so it no longer automatically enables `--with-state`
- Making CLI `--with-state` or `--with-state-and-gitignored` automatically create or name a worktree without explicit `-w/--worktree <new-branch>`
- Adding silent fallbacks when materialization fails (must always cleanup + error)
- Decoupling the Claude session fork from file materialization, or shipping `--with-state` without `--fork-session` semantics

## Documentation impact

- `README.md` — expand the `### Fork Sessions` section with the new CLI examples and add a short cross-reference in `### Git Worktrees`
- `cmd/agent-deck/session_cmd.go` fork usage block — add examples and document that CLI `--with-state` requires explicit `-w/--worktree <new-branch>`
- `CHANGELOG.md` — add a `### Added` entry under `## [Unreleased]` using the repo's Keep a Changelog prose style

## Out of scope (for follow-up tickets)

- Changing normal fork behavior to auto-create a worktree when the parent is already in a worktree (the existing silent-shared-worktree footgun). Tracked separately.
- Gitignored size caps. Add if real usage shows it's needed.
- Quiescing parent's tmux session for atomic snapshots.
- Submodule recursion.
- Rebase/merge/cherry-pick state replay.
- TUI pre-flight size estimate (`Will copy ~X GB`) for the gitignored checkbox.

## Review change log

- 2026-05-15: Accepted FWS-001 after clean-context verification. Changed staged patch materialization from `git apply --cached` to `git apply --index` so partially-staged edits and staged deletions are preserved correctly.
- 2026-05-15: Accepted FWS-002 after clean-context verification. Made the fork-with-state worktree creation path explicitly start from the parent session's HEAD so linked parent worktrees do not accidentally fork from the main/base worktree HEAD.
- 2026-05-15: Accepted FWS-003 after clean-context verification as new-code hardening. Cleanup now requires proof that the fork-with-state operation created the branch before running `git branch -D`.
- 2026-05-15: Accepted FWS-004 after clean-context verification. Fork-with-state now has an explicit destination contract: source sessions may have any branch/worktree shape, but the operation always creates a new destination branch and worktree; CLI requires `-w/--worktree <new-branch>` while TUI may show an editable suggested name.
- 2026-05-15: Accepted FWS-005 after clean-context verification. Split real CLI contract tests from git-side integration tests so flag registration, refusal messages, destination validation, and `ClaudeOptions` propagation are covered through the actual CLI/handler path.
- 2026-05-16: Accepted FWS-006 after clean-context verification. Aligned the spec's `RunWorktreeSetup` contract with existing setup-hook behavior: discover scripts from `repoDir`, execute in `worktreePath`, and return a single setup warning error.
- 2026-05-16: Accepted FWS-007 after clean-context verification. Kept the spec-promised clean-parent, bare-repo-layout, and setup-hook ordering integration tests and aligned the plan to implement them.
- 2026-05-16: Accepted FWS-008 after clean-context verification. Confirmed the explicit CLI destination contract remains the spec contract: gitignored may imply with-state, but CLI with-state flags must not auto-create or auto-name worktrees.
- 2026-05-17: Accepted FWS-009 after clean-context verification and option comparison. Fork-with-state preflight is now a shared `internal/git` contract used by both CLI and TUI: git facts are centralized, while each surface keeps responsibility for rendering warnings and errors.
- 2026-05-17: Accepted FWS-010 after clean-context verification. Clarified the TUI nested-state invariant: with-state cannot be enabled unless worktree mode is on, and turning worktree mode off clears both with-state and gitignored state.
- 2026-05-17: Accepted FWS-011 after clean-context verification and codebase-pattern comparison. Clarified that ForkDialog should use the existing `NewDialog` focus-target architecture for dynamic focus order, and added `TestForkDialog_FocusOrder` to the required TUI mandate.
- 2026-05-17: Accepted FWS-012 after clean-context verification and terminology review. Renamed/reworded the bare-layout integration case so it covers a linked parent worktree inside a bare-repo layout, while preserving the global contract that fork-with-state sources must be checked-out working trees.
- 2026-05-17: Accepted FWS-013 after clean-context verification. Confirmed the spec already uses the dedicated `cmd/agent-deck/session_cmd_fork_state_test.go` path; the matching plan file-map row was updated to align with it.
- 2026-05-17: Accepted ADP-007 codebase-pattern audit recommendation. Moved the documented git-side sequence integration tests to `internal/git/worktree_with_state_integration_test.go`, keeping `cmd/agent-deck/session_cmd_fork_state_test.go` focused on CLI/handler contract coverage.
- 2026-05-17: Accepted ADP-008 codebase-pattern audit recommendation. Clarified that the TUI shared-preflight structural guard should use brace-counted function extraction, matching existing source-level guard patterns and avoiding a brittle neighboring-function boundary.
- 2026-05-17: Accepted ADP-010 codebase-pattern audit recommendation. Added bounded behavioral eval smoke coverage for the real CLI fork-with-state flow and the visible TUI ForkDialog interaction path, and included those evals in the fork-with-state mandate.
- 2026-05-17: Accepted ADP-011 codebase-pattern audit recommendation. Documentation impact now calls for expanding the README's dedicated `### Fork Sessions` section, cross-referencing the behavior from `### Git Worktrees`, and writing the changelog entry in the repo's Keep a Changelog style.
- 2026-05-17: Accepted FWS-014 after clean-context verification and option comparison. Reordered data-flow Steps 2 and 3 so destination collision checks run before parent preflight, matching the implementation plan's order and saving git invocations on the common "user reuses a branch name" failure path. The structural-guard test enforces only `PreflightForkWithState` before `CreateWorktreeAtStartPoint`; the relative order of preflight and collision check is now documented as intentionally not enforced. Cleanup matrix updated to reflect the renumbered steps.
- 2026-05-17: Accepted FWS-015 after clean-context verification. Removed the three drift-only TUI test names (`TestForkDialog_WithStateCheckbox_VisibleWhenWorktreeEnabled`, `..._HiddenWhenWorktreeOff`, `TestForkDialog_GitignoredSubCheckbox_NestedUnderWithState`) from the state-machine test inventory and added a pointer to the existing `TestEval_ForkDialog_WithStateVisibleInteraction` eval test that already covers all three render conditions. Local TDD requires `-tags eval_smoke`; the eval suite runs on every PR via the existing eval-smoke workflow.
- 2026-05-17: Accepted FWS-016 after clean-context verification, asymmetry analysis, and option comparison via the Item 4 discussion document. Extracted a shared `git.ValidateForkWithStateDestination(repoRoot, branch)` helper with a typed `DestinationCollisionError` so CLI and TUI both call one validator for destination-collision facts (worktree-exists, branch-exists). Removes CLI's redundant inline collision check from the existing-worktree reuse block, removes TUI's inline `if opts.WithState` branch from its reuse block, and restores both reuse blocks to today's exact behavior. Mirrors the FWS-009 `PreflightForkWithState` architectural pattern: git facts live in `internal/git`; CLI and TUI own surface-appropriate error rendering. Added three helper unit tests (`TestValidateForkWithStateDestination_Clean`, `_BranchExists`, `_WorktreeExists`) and extended the mandate regex to include the new helper.
- 2026-05-18: Accepted FWS-017 after Track B comparative analysis vs upstream's fork-carry-wip branch (commit 02b6e5c4). Four corrections folded back from upstream's implementation: (1) Added revert to the in-progress-ops refusal list — REVERT_HEAD leaves the same half-done state as MERGE_HEAD; updated DetectInProgressOperation docstring, InProgressOperationError docstring, data flow Step 3, Decisions table, error catalog, and added TestDetect_Revert. (2) Data flow Step 5c now uses `git ls-files --others -z` so paths with embedded newlines are handled safely. (3) Data flow Step 5d now does a SECOND pass with `--ignored --exclude-standard` and unions with the first — `--ignored` is a filter, not additive; the prior single-pass version was a latent bug that would have copied only ignored files. (4) Added Step 5.5 for ProcessWorktreeInclude (PR #890) so the materialize → worktreeinclude → setup ordering is documented as a contract. Full analysis at `docs/superpowers/discussions/2026-05-18-upstream-comparison.md` on review/feat-v1.9.x-1029-fork-carry-wip.
- 2026-05-18: Accepted FWS-018 after CONTRIBUTING.md re-read and confirmation that `CLAUDE.md` is intentionally untracked per PR #1002. Promoted the previously-collapsed "CLAUDE.md mandate (new section to add)" subsection into a top-level `## Mandatory test coverage` section in the spec itself (Option B from the post-plan review). Removed the literal text-to-copy-into-CLAUDE.md fence. Updated "Documentation impact" to drop the CLAUDE.md target. Added a new RFC-required structural-change item that protects the coupling between `--with-state` and the Claude session fork. Goal section rewritten to make explicit that `--with-state` delivers a complete parallel agent environment — the Claude Code session conversation history forked via the existing `agent-deck session fork` flow (which invokes `claude --resume <parent-id> --fork-session`) PLUS the materialized working-tree files. The two pieces compose; the new code in this feature is the file half, but the user-visible value is the combined parallel environment.
