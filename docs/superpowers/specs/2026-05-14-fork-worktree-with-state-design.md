# Fork-with-State: Carry Parent Working-Tree Contents into a New Worktree

**Status:** Draft — pending implementation plan
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

Add an opt-in mode that creates a new worktree whose working tree exactly mirrors the parent's at the moment of fork — preserving the staged/unstaged split, untracked files, and (optionally) gitignored files. The parent repo must be left byte-identical after the operation.

## Non-goals

- Replacing the existing `-w` behavior. The clean-branch path stays as-is for users who want it.
- Auto-detecting "parent is in a worktree" and forcing the new mode. Behavior is purely opt-in.
- Supporting parent repos in mid-rebase / mid-merge / mid-cherry-pick / mid-bisect. These are refused with actionable errors.
- Submodule recursion. Submodule states are copied as files, not recursed into.
- Quiescing parent's tmux session during materialization. Parent edits during the fork are accepted as staleness.
- Size caps on gitignored copies. If the user typed `--with-state-and-gitignored`, we trust them.

## User-facing surface

### CLI

```
agent-deck session fork <id|title> [existing options] \
    -w, --worktree <branch>          (existing) create a new worktree
    -b, --new-branch                 (existing) create the branch if it doesn't exist
    --with-state                     NEW: carry parent's tracked + staged + untracked state.
                                          Implies --worktree (auto-names branch fork/<session>).
    --with-state-and-gitignored      NEW: also copy gitignored files. Implies --with-state.
```

**Implication chain:** `--with-state-and-gitignored` → `--with-state` → `--worktree`. Each flag self-bootstraps its parents; typing only the most specific flag is sufficient. The parent flags' help text states the implication explicitly.

### TUI (`ForkDialog`)

Under the existing "Create in worktree" checkbox, two new nested checkboxes appear when the worktree checkbox is on:

```
[x] Create in worktree (press w)
    Branch: fork/my-session
    [ ] Carry parent state (press y)
        [ ] Include gitignored files (press i)    ← only visible when "Carry parent state" is on
```

Focus order: name → group → wt-checkbox → branch → with-state-checkbox → with-state-gitignored-checkbox → conductor → options panel.

## Design

### Architecture

| Layer | New | Notes |
|---|---|---|
| CLI handler (`session_cmd.go`) | New flag parsing + implication resolution + cleanup-on-error guard | |
| TUI dialog (`forkdialog.go`) | Two new bool fields, two new checkboxes, two new exported getters | |
| Session options (`session.ClaudeOptions`) | Two new transient fields `WithState`, `IncludeGitignored` | Not persisted to disk; consumed during fork startup only |
| Git plumbing (`internal/git/`) | New file `worktree_with_state.go`; split of `CreateWorktreeWithSetup` into `CreateWorktree` + `RunWorktreeSetup` | |
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

// DetectInProgressOperation returns "rebase", "merge", "cherry-pick", "bisect", or "".
// Used as a pre-flight refusal check.
func DetectInProgressOperation(repoDir string) (kind string, err error)

// HasSubmodules returns true if .gitmodules exists. Used to emit a warning,
// not to refuse the operation.
func HasSubmodules(repoDir string) (bool, error)
```

### Split of `CreateWorktreeWithSetup`

The existing `internal/git/setup.go:CreateWorktreeWithSetup` is currently atomic: it runs `git worktree add` then the setup hook. To slot materialization between these steps, we split it:

```go
// New: just creates the worktree, no setup.
func CreateWorktree(repoDir, worktreePath, branch string) error

// New: runs the user's setup hook against an existing worktree.
func RunWorktreeSetup(worktreePath string, stdout, stderr io.Writer, timeout time.Duration) (setupErr error, err error)

// Existing function becomes a thin wrapper preserving backward compatibility:
func CreateWorktreeWithSetup(...) (setupErr error, err error) {
    if err := CreateWorktree(...); err != nil { return nil, err }
    return RunWorktreeSetup(...)
}
```

The fork-with-state path calls `CreateWorktree` → `MaterializeParentState` → `RunWorktreeSetup` directly. All other existing callers keep using `CreateWorktreeWithSetup`.

**Rationale:** the setup hook (e.g., `npm install`, `uv sync`) is the user's "prepare this worktree for work" script. It needs to see the final file contents — including parent's WIP. If we materialized *after* setup, a parent with a new dependency in `package.json` would yield a worktree with new `package.json` but old `node_modules`.

### Data flow

```
Step 1. Parse + resolve
  - Resolve session id/title → *session.Instance (parent)
  - Apply implication chain: gitignored → with-state → worktree
  - Validate parent is a Claude session and CanFork()

Step 2. Pre-flight on parent's git state
  - DetectInProgressOperation(parent.ProjectPath)
      → if "rebase" | "merge" | "cherry-pick" | "bisect": refuse with actionable error
  - HasSubmodules(parent.ProjectPath)
      → if true: log a warning ("submodules will be copied as-is, not recursed")
  - git -C parent rev-parse HEAD → captured as the worktree start-point

Step 3. Resolve branch + path
  - Apply wtSettings.ApplyBranchPrefix() to user-supplied or auto-named branch
  - Compute worktree path via git.WorktreePath() with wtSettings.Template
  - Reuse-existing-worktree check unchanged

Step 4. Create worktree (no setup yet)
  - git.CreateWorktree(repoRoot, worktreePath, branch) with start-point = parent's HEAD
  - On error: abort, nothing to clean up

Step 5. Materialize parent state                                        NEW
  - git.MaterializeParentState(parent.ProjectPath, worktreePath, opts)
    Internally:
      a. git -C parent diff --cached --binary | git -C newWorktree apply --cached
      b. git -C parent diff --binary          | git -C newWorktree apply
      c. git -C parent ls-files --others --exclude-standard
         → copy each parent/<f> → newWorktree/<f> preserving mode
      d. if IncludeGitignored:
         git -C parent ls-files --others --ignored --exclude-standard
         → copy with mode preserved, no size cap
  - On any failure: return error to caller (cleanup happens in Step 7)

Step 6. Run setup hook                                                  CHANGED
  - git.RunWorktreeSetup(worktreePath, stdout, stderr, timeout)
  - Setup-hook failures stay warnings (matches current session_cmd.go:740)

Step 7. Cleanup-on-error guard                                          NEW
  - If steps 4-6 succeed, no-op.
  - If step 5 fails:
      git -C repoRoot worktree remove --force worktreePath
      git -C repoRoot branch -D <branch>   (only if WE created the branch)
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
| Runtime branch | Auto-named `fork/<sanitized-session>` off parent's HEAD | Matches today's fork dialog default. User can override in TUI/CLI. |
| State scope | tracked-modified + staged + untracked, gitignored opt-in | Most-common need. Gitignored often means node_modules / .env — opt-in protects against accidents. |
| Index fidelity | Preserve staged vs. unstaged split | Two-stage `git apply --cached` then `git apply`. Faithful to parent's exact state for users who pre-stage hunks. |
| In-progress ops | Refuse rebase, merge, cherry-pick, bisect | Cleanest semantics; ship v1 smaller. |
| Submodules | Best-effort copy, no recursion | Warn but don't refuse. |
| LFS | Treat as regular file copy (LFS pointer files self-handle) | Standard LFS behavior. |
| Gitignored size cap | None | User who typed `--with-state-and-gitignored` opted in explicitly. Cap can be added later if needed. |
| Failure handling | Abort + cleanup | Atomic mental model: fork either happens or it doesn't. |
| Parent safety | Read-only on parent (`git diff`, `ls-files`, file reads — no `git stash`, no `git add`) | Parent's index, working tree, stash list, and `.git` must be byte-identical after fork. |
| Race vs. parent writes | Accept staleness, document it | Materialization is not atomic. Fork captures parent state at moment-of-fork. |
| Setup-hook ordering | Materialize BEFORE setup hook | Setup hook sees parent's final state and can react to it (e.g., install new deps from WIP package.json). |

## Errors and cleanup

### Cleanup matrix

| Failure point | Cleanup action |
|---|---|
| Pre-flight (Step 2) | None — early exit |
| `CreateWorktree` (Step 4) | None — git rolls back its own failed `worktree add` |
| Materialization (Step 5) | `git worktree remove --force <path>`, then `git branch -D <branch>` if we created it |
| Setup hook (Step 6) | None — already a non-fatal warning today |
| `Instance.Start` (Step 9) | None — leave worktree for inspection (matches today's behavior) |

### Error message catalog

```
"parent session is mid-rebase; finish or abort the rebase before forking with state (cd <parent-path> && git rebase --abort)"
"parent session is mid-merge; resolve or abort the merge before forking with state"
"parent session is mid-cherry-pick; finish or abort before forking with state"
"parent session is mid-bisect; run 'git bisect reset' in <parent-path> before forking with state"
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
| `TestDetect_Bisect` | active bisect → returns "bisect" |
| `TestDetect_Clean` | normal repo → returns "" |

### Integration tests — `cmd/agent-deck/session_cmd_test.go` extension

| Test | Asserts (via CLI) |
|---|---|
| `TestSessionFork_WithState_CleanParent` | clean parent → clean new worktree, no error |
| `TestSessionFork_WithState_DirtyParent` | parent has staged + unstaged + untracked → fork mirrors all three |
| `TestSessionFork_WithStateAndGitignored` | parent has gitignored content → fork has it |
| `TestSessionFork_WithStateImpliesWorktree` | `--with-state` without `-w` still creates a worktree with auto-named branch |
| `TestSessionFork_WithState_FailsWhenMidRebase` | parent mid-rebase → error, no worktree created |
| `TestSessionFork_WithState_CleansUpOnMaterializeFailure` | injected materialize failure → worktree and branch removed |
| `TestSessionFork_WithState_ParentIsBareRepo` | bare-repo parent project root → works (covers `resolveGitInvocationDir` interaction) |
| `TestSessionFork_WithState_MaterializesBeforeSetupHook` | setup hook sees materialized files (verified via a hook that reads a parent-WIP file) |

### TUI tests — `internal/ui/forkdialog_test.go` extension

| Test |
|---|
| `TestForkDialog_WithStateCheckbox_VisibleWhenWorktreeEnabled` |
| `TestForkDialog_WithStateCheckbox_HiddenWhenWorktreeOff` |
| `TestForkDialog_GitignoredSubCheckbox_NestedUnderWithState` |
| `TestForkDialog_FocusOrder` |

### CLAUDE.md mandate (new section to add)

```
## Fork-with-state: mandatory test coverage

Any PR modifying fork-with-state paths MUST pass:

go test ./internal/git/... -run "Materialize|DetectInProgress" -race -count=1
go test ./cmd/agent-deck/... -run "SessionFork_WithState" -race -count=1
go test ./internal/ui/... -run "ForkDialog_WithState" -race -count=1

Paths under the mandate:
- internal/git/worktree_with_state.go (+ _test.go)
- internal/git/setup.go (split of CreateWorktreeWithSetup)
- cmd/agent-deck/session_cmd.go fork handler
- internal/ui/forkdialog.go
- internal/session/instance.go fork wiring
```

## Documentation impact

- `README.md` — add a `--with-state` example to the fork section
- `cmd/agent-deck/session_cmd.go` fork usage block — add examples and document the implication chain
- `CHANGELOG.md` — note for the version this ships in
- `CLAUDE.md` — add the new test mandate section

## Out of scope (for follow-up tickets)

- Auto-creating a worktree when the parent is already in a worktree (the silent-shared-worktree footgun). Tracked separately.
- Gitignored size caps. Add if real usage shows it's needed.
- Quiescing parent's tmux session for atomic snapshots.
- Submodule recursion.
- Rebase/merge/cherry-pick state replay.
- TUI pre-flight size estimate (`Will copy ~X GB`) for the gitignored checkbox.
