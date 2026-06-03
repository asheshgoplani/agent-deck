# PR-A: fork-with-state hardening — correctness gaps + test coverage on top of #1030

**Related:** closes (partially) #1029, follows up #1030
**Branch:** `feature/fork-worktree-with-state` → `upstream/main`
**Spec:** [`docs/superpowers/specs/2026-05-18-fork-with-state-followup-design.md`](../specs/2026-05-18-fork-with-state-followup-design.md)
**Plan:** [`docs/superpowers/plans/2026-05-18-fork-with-state-followup.md`](../plans/2026-05-18-fork-with-state-followup.md)
**Manual verification:** [`fork-worktree-with-state-testing.md`](fork-worktree-with-state-testing.md)
**Post-merge gap analysis:** [`docs/superpowers/discussions/2026-05-18-post-merge-gap-analysis.md`](2026-05-18-post-merge-gap-analysis.md)

## Summary

`agent-deck session fork --with-state` shipped in #1030 but has four user-facing correctness gaps and several missing test guards. This PR (PR-A) closes the CLI-side of those gaps without modifying upstream's `MaterializeWipFromParent` or `CreateWorktreeWithStateAndSetup`. A follow-up (PR-B) will close the TUI side.

The framing below uses **user-feature capability gaps** — what would a real user try, what does the spec promise, what do they actually get today, and where does this PR land. Eight gaps were identified. **PR-A closes 6 fully (CLI surface), 1 partially (gap 5: file half), 1 deferred (gap 4: TUI → PR-B).** The remaining items are listed under "Not in this PR" with their disposition.

---

## Acknowledgement

Big thanks to @asheshgoplani for landing #1030 quickly. The diff-based, parent-read-only approach is exactly right, and `CreateWorktreeWithStateAndSetup` is a clean orchestration shape. This PR adds the hardening that the original issue (#1029) implicitly asked for — its phrasing around "Refuse unsafe parent states with actionable errors," "Existing destination branches/worktrees are refused," and "Fork starts from the parent session's actual `HEAD`" describes correctness contracts that the merge addressed in spirit but not in every corner.

---

## What this PR closes

### Gap 1 — Linked-worktree parents anchored at the wrong commit ✅ closed (CLI)

**User experience pre-PR:** You're in `~/myproject-worktrees/feature-x` with WIP, run `session fork --with-state -w fork/explore`. Spec promised the new worktree forks from `feature-x`'s HEAD. **Actually got:** the new worktree forks from the main repo's HEAD (probably `main`, not `feature-x`) and carries the main worktree's WIP, not yours. The fork looks plausibly successful — no error — but you're now in a parallel agent against the wrong baseline.

**Fix:** New `git.HeadCommit(repoDir)` resolves the parent session's HEAD; new `git.CreateWorktreeAtStartPoint(repoDir, worktreePath, branch, startPoint)` creates the new worktree branched off that explicit start point. The CLI handler captures parent's HEAD and threads it through. `HeadCommit` captures stdout only and includes stderr only on the error path, so ambient Git warnings cannot be prepended to the commit hash. Integration tests pin the linked-worktree contract, and a focused unit regression pins the stdout-only behavior.

**Files:** `internal/git/git.go`, `internal/git/git_test.go`, `internal/git/fork_with_state_integration_test.go`, `cmd/agent-deck/session_cmd.go`.

### Gap 2 — Destination collision is a silent footgun ✅ closed (CLI)

**User experience pre-PR:** Run `--with-state -w fork/explore` after a previous fork with the same name, or against a branch a teammate already has a worktree for. Spec promised a refusal. **Actually got:** a "Reusing existing worktree at …" log line, materialization **doesn't run at all**, and the forked session starts in the pre-existing worktree — sharing disk with whatever was already there. Strictly worse than the silent-shared-worktree footgun this feature was supposed to fix.

**Fix:** New shared helper `git.ValidateForkWithStateDestination(repoRoot, branch)` returns a typed `*DestinationCollisionError` with exported `Kind` constants (`CollisionWorktreeExists` / `CollisionBranchExists`). The CLI handler calls it immediately after branch-prefix resolution, before the legacy branch-existence gate, existing-worktree reuse, or destination-directory creation. In `--with-state` mode, `-w` is the fresh destination branch and `-b` is not required; existing branches/worktrees are always refused rather than reused. Refusal messages include the conflicting path so the user can `cd` to it. A precedence test pins that worktree-existence wins over branch-existence when both conditions are true, and eval coverage drives both the no-`-b` happy path and the existing-worktree refusal through the real binary.

**Files:** `internal/git/fork_with_state_destination.go` (new), `internal/git/fork_with_state_destination_test.go` (new), `cmd/agent-deck/session_cmd.go`.

### Gap 3 — Failed forks leave junk on disk ✅ closed (CLI)

**User experience pre-PR:** Materialization fails mid-flight (mid-rebase parent, weird patch, etc.). Spec promised: "fork either happens or it doesn't" — partially-created worktree removed, branch deleted. **Actually got:** the new worktree directory and the new branch are left orphaned. You have to `git worktree remove --force` and `git branch -D` by hand. And if you don't notice, you've set yourself up to trip over gap 2 on the next try.

**Fix:** The CLI handler now wraps the `MaterializeWipFromParent` call. On error: `git worktree remove --force <path>`, then `git branch -D <branch>` only if `CreateWorktreeAtStartPoint` returned `createdBranch=true` (proof-based cleanup — never deletes a pre-existing branch). The error message gets `; new worktree cleaned up` appended so the user knows the state.

**Files:** `cmd/agent-deck/session_cmd.go`.

### Gap 5 (file half) ✅ closed; conversation half NOT verified by this PR

**User experience pre-PR:** No automated test pins that a real `agent-deck session fork --with-state` against a real binary produces the right files on disk. The spec carved out a `TestEval_SessionForkWithState_CreatesMaterializedWorktree` eval that was never written.

**Fix (file half):** New `tests/eval/session/fork_with_state_test.go` (`//go:build eval_smoke`) drives the compiled `agent-deck` binary against a scratch HOME with a real git repo. Seeds staged + unstaged + untracked + gitignored WIP, runs `agent-deck session fork --with-state-and-gitignored -w fork/eval-state`, and asserts:
- Parent's `git status --porcelain` is byte-identical before/after (read-only invariant)
- Destination is on the requested branch
- Destination's porcelain mirrors parent's
- Gitignored file is present at destination (the `-and-gitignored` discriminator)

**What's still NOT verified:** the conversation-continuity half — that `claude --resume <parent-id> --fork-session` actually composes with the materialization to produce a parallel agent that knows the parent's history. The eval test stubs `claude` out, so the Claude session flow is structurally exercised (`Start()` is called, options propagated) but not end-to-end. See "Not in this PR" below.

**Files:** `tests/eval/session/fork_with_state_test.go` (new).

### Gap 6 ✅ closed (both destination collision and mid-op portions)

**User experience pre-PR:** Refusal messages aren't actionable. For destination collision, you get git's native refusal passed through. For mid-rebase, you get "parent is in mid-rebase; resolve it before forking with --with-state" — no copy-pasteable `cd && git rebase --abort` hint.

**Fix (destination collision):** `DestinationCollisionError` formatting in the CLI handler now produces the spec's wording verbatim:
- `branch 'fork/X' already has a worktree at <path>; choose a new destination branch for --with-state`
- `branch 'fork/X' already exists; choose a new destination branch for --with-state`

**Fix (mid-op refusal):** New exported `git.DetectInProgressOperation(repoDir)` helper mirrors upstream's internal `refuseUnsafeParentState` check table. The CLI handler calls it BEFORE worktree creation, so mid-op refusals happen without first creating a worktree. The actionable message now includes the parent path and the right abort command per kind:
- `parent session is mid-rebase; finish or abort the rebase before forking with state (cd <parent-path> && git rebase --abort)`
- ... and equivalents for `merge`, `cherry-pick`, `revert`, `bisect`.

A structural-guard test pins the new flow so future refactors can't silently drop a kind. Upstream's `refuseUnsafeParentState` remains as a backstop.

**Files:** `internal/git/fork_with_state_destination.go` (+ `_test.go`), `cmd/agent-deck/session_cmd.go`, `cmd/agent-deck/session_cmd_fork_state_test.go`, `tests/eval/session/fork_with_state_test.go`.

### Gap 7 — Submodule warning ✅ closed

**User experience pre-PR:** Repo with `.gitmodules` forks silently. Submodule directory contents get copied as plain files (the materialization contract), but you get no signal that the submodule's internal `.git` state isn't recursed. If you then run something inside the fork that touches the submodule, you'll discover the lossy copy at the worst moment.

**Fix:** New exported `git.HasSubmodules(repoDir)` helper checks for a regular `.gitmodules` file. CLI handler emits the spec-promised stderr warning before doing any fork work:

> `Warning: submodules detected — copied as files, not recursed (parent's submodule states preserved)`

PR-B (TUI) will reuse the same helper for the TUI submit path. Pinned by an eval test (`TestEval_SessionForkWithState_SubmoduleWarning`) that drives the real binary against a `.gitmodules`-bearing repo.

**Files:** `internal/git/fork_with_state_destination.go` (+ `_test.go`), `cmd/agent-deck/session_cmd.go`, `tests/eval/session/fork_with_state_test.go`.

### Gap 8 — Setup hook ordering is now protected ✅ closed

**User experience pre-PR:** Setup hook fires after materialize today (good), but no test pinned this. A future refactor swapping the order would silently regress; the only signal would be "my new dependency from parent's WIP isn't installed in the fork."

**Fix:** New `TestForkWithState_SetupHookObservesMaterializedState` installs a setup script that reads parent's WIP file and writes a fingerprint to a marker outside the worktree. Asserts the marker contains the materialized content. Mutation-tested by the implementer (swapping the order in production code correctly flips the test to `NO_WIP_OBSERVED`).

**Files:** `internal/git/fork_with_state_integration_test.go` (extended with the new test).

---

## Other test coverage added

Beyond the user-feature gaps above, this PR adds five test-coverage gaps from the gap analysis:

- **`TestMaterializeWipFromParent_ParentUntouched`** — asserts parent's `git status`, both diffs, stash list, and raw `.git/index` bytes are identical after materialization. Catches future regressions where someone changes materialization to use `git stash`, `git add`, `git update-index`, or other parent-mutating operations.
- **Four mid-op refusal regressions** — upstream only tested mid-merge. Added `_Rebase`, `_CherryPick`, `_Revert`, `_Bisect` mirroring the same pattern; these will fail if any of the five `refuseUnsafeParentState` sentinels stops being checked.
- **CLI contract tests** via a new `sessionForkBeforeStartHook` test seam — verifies explicit-destination refusal, both collision error messages, and that the resolved `git.WorktreeStateOptions` + `MaterializeWipFromParent(..., *withStateGitignored)` wiring fires in the right order before `forkedInst.Start()`. The real-binary eval suite also covers the contracts source-grep tests previously missed: `--with-state -w <fresh-branch>` does not require `-b`, existing branches are refused, and existing worktrees are refused before session start.
- **`TestForkWithState_BareRepoLayoutLinkedParentWorktree`** — the bare-repo + linked-parent layout integration test (also pins gap 1).
- **`TestForkWithState_SetupHookObservesMaterializedState`** — the setup-hook ordering test (gap 8).

---

## Not in this PR (with disposition)

### Gap 4 — TUI users have no access to `--with-state` → PR-B

The `ForkDialog` is unchanged. TUI users still have to drop to the CLI. Full sub-checkbox integration (`Carry parent state (press y)` / `Include gitignored files (press i)` under the existing "Create in worktree" checkbox), focus order, behavioral eval — all live in PR-B. Will land as a follow-up PR after this one.

### Gap 5 conversation half — verification of `claude --resume --fork-session` continuity

The eval test stubs `claude`, so we exercise the spawn path structurally but don't prove the new session shares conversation history with the parent. Closing this would require either (a) a richer fake-claude that stores/echoes conversation state, or (b) running against a real Claude install in a CI lane. Not blocked on PR-A; worth a small follow-up.

### Gap 11 (from the 11-gap analysis) — Shared `PreflightForkWithState` extraction → deferred PR-C, RFC-required

Upstream's `refuseUnsafeParentState` is internal (lowercase) to `materialize_wip.go`. Promoting it to an exported `PreflightForkWithState` with typed `InProgressOperationError` would let CLI and TUI share a preflight gate with surface-specific error rendering (and would also enable cleaner gap 6 wording). This refactors upstream's just-merged file; it deserves an RFC discussion. Not in PR-A or PR-B; would land as a separate PR-C.

---

## File changes

| File | Change | Lines |
|---|---|---|
| `internal/git/git.go` | + `HeadCommit`, `CreateWorktreeAtStartPoint` | +38 |
| `internal/git/git_test.go` | + 2 tests | +62 |
| `internal/git/fork_with_state_destination.go` | new: `ValidateForkWithStateDestination`, `DestinationCollisionError`, `HasSubmodules`, `DetectInProgressOperation` + helper | +120 |
| `internal/git/fork_with_state_destination_test.go` | new: 11 tests (3 validator + 2 submodule + 6 in-progress detection) | +220 |
| `internal/git/materialize_wip_invariant_test.go` | new | +73 |
| `internal/git/fork_with_state_integration_test.go` | new | +266 |
| `internal/git/issue1029_edge_test.go` | + 4 refusal tests | +101 |
| `internal/git/setup.go` | + `RunWorktreeSetupAfterCreate`; wrapper tail deduped to delegate | +10 |
| `cmd/agent-deck/session_cmd.go` | with-state branch: collision validation → mid-op refusal (actionable) → submodule warning → parent-HEAD + CreateWorktreeAtStartPoint → materialize → cleanup-on-error (with honest failure-reporting); before-start hook seam | +135 |
| `cmd/agent-deck/session_cmd_fork_state_test.go` | new: 7 source-introspection contract tests incl. mid-op structural guard | +260 |
| `tests/eval/session/fork_with_state_test.go` | new (eval_smoke tagged): happy path + 3 error-path subprocess tests | +430 |
| `docs/superpowers/specs/2026-05-18-fork-with-state-followup-design.md` | new (followup design) | +160 |
| `docs/superpowers/plans/2026-05-18-fork-with-state-followup.md` | new (followup plan) | +700 |
| `docs/superpowers/discussions/2026-05-18-post-merge-gap-analysis.md` | new (gap analysis) | +180 |

Total: ~2800 lines (about half are tests + docs).

---

## Test plan

### Mandate suite (must pass)

```bash
GOTOOLCHAIN=go1.24.0 go test ./internal/git/... -run "Materialize|RefuseUnsafeParentState|ValidateForkWithStateDestination|HasSubmodules|DetectInProgressOperation|CreateWorktreeAtStartPoint|HeadCommit|ForkWithState|Issue1029" -race -count=1

GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/... -run "SessionFork_WithState" -race -count=1

GOTOOLCHAIN=go1.24.0 go test -tags eval_smoke ./tests/eval/session/... -run "TestEval_SessionForkWithState" -race -count=1
```

### Regression check (upstream's #1030 tests still pass)

```bash
GOTOOLCHAIN=go1.24.0 go test ./internal/git/... -run "RegressionFor1029|Issue1029" -race -count=1
```

### Local verification done

- [x] `make fmt` clean (one upstream file reformatted by gofmt; committed as `chore: gofmt run`)
- [x] `make lint` clean — no PR-A-introduced warnings
- [x] Mandate suite: 19 + 5 + 1 tests matched, all pass
- [x] Regression suite: 8 tests pass
- [x] Full `go test ./...` — PR-A-touched packages clean; remaining failures are environmental (tmux/zoxide/Linux-systemd suites; pre-existing)
- [x] Review-fix focused checks:
  - `GOTOOLCHAIN=go1.24.0 go test ./internal/git -run TestHeadCommit_IgnoresGitWarningsOnStderr -count=1`
  - `GOTOOLCHAIN=go1.24.0 go test -tags eval_smoke ./tests/eval/session -run 'TestEval_SessionForkWithState_(RealBinary|RejectsExistingWorktree|RejectsExistingBranch)' -count=1 -timeout 120s`
  - `GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck -run 'TestSessionFork_WithState(HookCapturesResolvedStateBeforeStart|OptionsPropagatedBeforeStart)|TestSessionForkBeforeStartHook_NilInProduction' -count=1`
  - `GOTOOLCHAIN=go1.24.0 go test ./internal/git -run 'TestValidateForkWithStateDestination_(PropagatesWorktreeCheckError|Clean|BranchExists|WorktreeExists_TakesPrecedence)' -count=1`
  - `GOTOOLCHAIN=go1.24.0 go test ./internal/git -run 'TestCreateWorktreeWithStateAndSetup_(CleansUpOnMaterializeFailure|WiresMaterialization_RegressionFor1029)' -count=1`
  - `GOTOOLCHAIN=go1.24.0 go test ./internal/git -run TestMaterializeWipFromParent_ParentUntouched -count=1`
  - `GOTOOLCHAIN=go1.24.0 go test ./internal/git/... -run 'Materialize|RefuseUnsafeParentState|ValidateForkWithStateDestination|HasSubmodules|DetectInProgressOperation|CreateWorktreeAtStartPoint|HeadCommit|ForkWithState|Issue1029' -race -count=1`
  - `GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/... -run 'SessionFork_WithState' -race -count=1`
  - `GOTOOLCHAIN=go1.24.0 go test -tags eval_smoke ./tests/eval/session/... -run 'TestEval_SessionForkWithState' -race -count=1 -timeout 180s`
- [x] Full eval-smoke attempted:
  - `GOTOOLCHAIN=go1.24.0 go test -tags eval_smoke ./tests/eval/... ./internal/ui/... -count=1 -timeout 15m`
  - Result: fails outside the fork-with-state path. I reproduced the failing subsets from a detached `main` worktree at `a02a30f3db27d1142b45222a827d0a59ab7dad99`, so these are not introduced by this branch's `handleSessionFork` changes.
  - Real-tmux evals (`TestEval_Session_AttachRestart_SocketIsolation_RealTmux`, `_ITermBadge_RealAttach`, `_InjectStatusLine_RealTmux`) fail before any fork code. With the default macOS temp dir, the harness socket path can exceed the Unix socket path limit (`File name too long`). With `TMPDIR=/private/tmp`, that symptom goes away, but local `tmux 3.6a` still fails during `session start` at the send-keys step (`failed to send command`). Track as tmux/harness environment work, not fork-with-state.
  - `TestEval_Session_StatusUnderBridgedStdio_NoHang` fails on `main` too. The unit WaitDelay contracts pass, but the real-binary eval's lingering-child shim also affects startup tmux probes such as `tmux -V`; those use raw `exec.Command(...).CombinedOutput()` and can consume the test's 10s budget before the status path under test finishes. Track separately as eval/shim coverage drift.
  - `TestEval_SelectFlag_GroupScopeWarning` hangs on `main` too. Re-running with `-timeout 30m -v` confirms the test blocks in `runBinStderrShort` at `cmd.CombinedOutput()` after printing only `=== RUN`; the helper assumes the no-PTY TUI exits on its own, but the current binary stays alive. Track as a select-flag eval harness bug.
  - The three `internal/ui` zoxide picker failures reproduce on `main`. Root cause in this environment: `zoxide` is not installed, and `Show()` checks real availability before using the injected query function, so the tests never populate results. Track as zoxide test-seam/env drift.
- [ ] Manual TUI walkthrough — NOT applicable, no TUI changes in PR-A (gap 4 → PR-B)
- [ ] Manual CLI walkthroughs — see [`fork-worktree-with-state-testing.md`](fork-worktree-with-state-testing.md) for scenario-by-scenario steps mapping to each of the 8 capability gaps

### Reviewer-friendly verification

Cherry-pick scenarios from the testing doc; each closes-by-PR-A scenario is a self-contained `mktemp -d` setup and a small `agent-deck session fork ...` invocation with copy-pasteable assertions.

---

## Reviewer notes

### Polish landed in PR-A (was previously flagged as follow-ups)

1. **Cleanup-error logging now honest.** `_ = exec.Command(...).Run()` replaced with captured errors. When cleanup actually fails (concurrent process holds the path, etc.), the user gets a precise "cleanup also failed (…); manual cleanup required: rm -rf <path> && git -C <repo> branch -D <branch>" message instead of the false "new worktree cleaned up." Commit `a7837cf6`.
2. **Setup-hook tail deduped.** `CreateWorktreeWithStateAndSetup` now delegates to `RunWorktreeSetupAfterCreate` for its setup-hook portion — eliminates the 15-line duplication. Wrapper signature unchanged; upstream's progress-message tests confirm byte-identical stderr output. Commit `9100ef23`.
3. **Subprocess error-path tests added.** Eval-tagged tests drive the real binary against destination-collision, existing-worktree, and mid-rebase parent states, asserting exact user-facing wording. The happy-path eval now intentionally omits `-b` to pin the CLI contract that `--with-state` requires `-w`, not `-b`. Augments the source-introspection contract tests; doesn't replace them. Commit `8646d2a2` plus the latest review-fix commit.
4. **Follow-up bug batch triaged and fixed where narrow.** BUG-01/BUG-08 were already covered by the hoisted destination validator. BUG-02/BUG-05 changed the before-start hook to pass `git.WorktreeStateOptions` by value and added a runtime handler-path test for resolved state propagation. BUG-03 now propagates `GetWorktreeForBranch` failures instead of treating them as "no collision." BUG-06 now cleans up the shared helper's created worktree and branch when materialization fails. BUG-07 extends the parent-read-only invariant to raw index bytes. BUG-09 trims the setup-hook test comment to the scope the test actually proves.

### Design decisions worth a second look

- **`CreateWorktreeAtStartPoint` is a new helper, not an extension of `CreateWorktreeWithStateAndSetup`.** The followup spec discusses two options for anchoring parent's HEAD: (a) new helper + the CLI handler orchestrates Create → Materialize → ProcessWorktreeInclude → Setup manually, or (b) extend the wrapper to accept an optional start point. Picked (a) to avoid touching upstream's just-merged wrapper. Open to revisiting if (b) is preferred.
- **`ValidateForkWithStateDestination` is a shared `internal/git` helper rather than inline checks.** Both CLI and TUI need to call it (TUI is PR-B). Extracting now prevents drift between the two surfaces.
- **`DestinationCollisionError.Kind` is a string with exported constants** (`CollisionWorktreeExists`, `CollisionBranchExists`). Considered using `iota` typed enum; chose strings for `errors.As`-friendliness and zero-ceremony switch arms. Two values today; constants reserved for future expansion.
- **Post-start cleanup remains a product/behavior decision (BUG-04), not a narrow correctness fix in this PR.** The current cleanup guarantee is scoped to materialization failure before the session starts. Extending cleanup across `forkedInst.Start()` or `storage.SaveWithGroups()` failures would change observable behavior and needs a separate design call: after `Start()` succeeds, a tmux process may already be running in the new worktree, so safe cleanup would also need session termination semantics. Current eval coverage intentionally inspects the materialized worktree after a downstream start failure; changing that contract should be done deliberately, with a new test shape.

### Hot takes I'd appreciate guidance on

1. **`DetectInProgressOperation` duplicates upstream's `refuseUnsafeParentState` check table.** The two functions are intentionally kept in sync — comments on both reference each other and ask future contributors to update both. The alternative is promoting `refuseUnsafeParentState` to a shared exported helper, which is gap 11 / PR-C territory and needs an RFC. For now, two sources of truth is the lesser evil. Open to other suggestions.
2. **PR-C (shared preflight extraction) is the natural next architectural step.** Would let CLI and TUI share typed preflight errors instead of having parallel detection helpers (`DetectInProgressOperation` here, `HasSubmodules` here, upstream's `refuseUnsafeParentState`). Worth scoping as a separate issue + RFC rather than slipping into PR-A. Reasonable?
3. **PR-B (TUI) is sized for a separate PR.** ~600 LOC for `ForkDialog` integration + behavioral eval. Will reuse `git.HasSubmodules`, `git.DetectInProgressOperation`, `git.ValidateForkWithStateDestination`, `git.HeadCommit`, `git.CreateWorktreeAtStartPoint`, and the actionable-message formatting patterns from this PR. Reviewable in parallel after this lands.

---

## Out of scope (explicit non-goals)

- Replacing upstream's `MaterializeWipFromParent` or `CreateWorktreeWithStateAndSetup`. They work; we wrap.
- Refactoring upstream's wrapper pattern into a split (`CreateWorktree` + `RunWorktreeSetup`) — the deprecated original spec called for this; the followup adopted the wrapper as base.
- Renaming any upstream-introduced symbols.
- Touching `internal/git/materialize_wip.go` directly. All new helpers live in new files.

---

## Final notes

- This PR was scoped via a comparative analysis against `02b6e5c4` / `6a1645eb` — see [`2026-05-18-post-merge-gap-analysis.md`](2026-05-18-post-merge-gap-analysis.md) for the full deltas walk-through.
- The 8-capability-gap framing in this PR body is the user-experience lens; the 11-gap framing in the analysis doc is the implementation lens. They overlap but aren't 1:1 — gap 11 (shared preflight extraction) is implementation-only and doesn't appear in the 8-gap framing.
- Branch is currently 18 commits ahead of `upstream/main`: 6 docs/spec/plan commits, 9 code/test commits, 2 housekeeping (gofmt + spec-drift fix), 1 wip preserving Items 1-5 of earlier review history. Open to squash-on-merge or rebase-and-merge — both work.
