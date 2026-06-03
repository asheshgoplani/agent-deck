# Fork-with-State PR-A — Manual Verification & Testing

**Date:** 2026-05-19
**Branch:** `feature/fork-worktree-with-state` (pre-push)
**Last commit:** `35b815af`
**Spec:** [`../specs/2026-05-18-fork-with-state-followup-design.md`](../specs/2026-05-18-fork-with-state-followup-design.md)
**Plan:** [`../plans/2026-05-18-fork-with-state-followup.md`](../plans/2026-05-18-fork-with-state-followup.md)
**Capability-gap framing:** the 8 user-feature gaps captured in [`fork-worktree-with-state-pr-body.md`](fork-worktree-with-state-pr-body.md)

## Test environment

Required:
- `GOTOOLCHAIN=go1.24.0` exported in shell
- `git` 2.30+
- `make`
- For eval suite: `bash`, `sh`
- For full sweep: `tmux` (some env tests will skip without it; PR-A code doesn't depend on tmux)

```bash
export GOTOOLCHAIN=go1.24.0
cd /Users/stevemorin/c/agent-deck
git status   # should show clean working tree on feature/fork-worktree-with-state
```

## Quick smoke (5 minutes) — run the mandate suite

This is the contract PR-A's followup spec asserts. All four commands must pass with no "no tests to run" output:

```bash
go test ./internal/git/... -run "Materialize|RefuseUnsafeParentState|ValidateForkWithStateDestination|HasSubmodules|DetectInProgressOperation|CreateWorktreeAtStartPoint|HeadCommit|ForkWithState|Issue1029" -race -count=1

go test ./cmd/agent-deck/... -run "SessionFork_WithState" -race -count=1

go test -tags eval_smoke ./tests/eval/session/... -run "TestEval_SessionForkWithState" -race -count=1

go test ./internal/git/... -run "RegressionFor1029|Issue1029" -race -count=1   # upstream regression check
```

Expected: 19 + 5 + 1 + 8 tests pass.

---

## Manual verification scenarios

Each scenario maps to one of the 8 capability gaps from the PR body. For each: setup steps, the command to run, the expected outcome, and whether PR-A actually closes the gap.

### Scenario 1 — Linked-worktree parent forks from the right commit (gap 1, CLOSED by PR-A CLI)

This is the headline regression — pre-PR-A, the fork silently anchors at the main worktree's HEAD instead of your parent worktree's HEAD.

```bash
# Setup: scratch project with a linked parent worktree
cd $(mktemp -d) && PROJ=$(pwd)
git init --bare .bare
git -C .bare worktree add -b main seed-wt
git -C seed-wt config user.email t@t && git -C seed-wt config user.name t
echo "seed" > seed-wt/README.md
git -C seed-wt add . && git -C seed-wt commit -m seed

git -C .bare worktree add -b parent-branch parent-wt
git -C parent-wt config user.email t@t && git -C parent-wt config user.name t
echo "parent change" > parent-wt/README.md
git -C parent-wt commit -am "parent commit"

# Sanity: seed HEAD ≠ parent HEAD
diff <(git -C seed-wt rev-parse HEAD) <(git -C parent-wt rev-parse HEAD) || echo "OK: HEADs differ"

# Dirty parent's WIP
echo "wip-content" > parent-wt/wip.txt

# Build the binary and seed a fake agent-deck session pointing at parent-wt
make build
export PATH=$PWD/build:$PATH
export HOME=$PROJ/home && mkdir -p $HOME
agent-deck session add -c claude -t parent-session "$PROJ/parent-wt"
agent-deck session set parent-session claude-session-id "fake-uuid-12345"

# The thing under test
agent-deck session fork parent-session --with-state -w fork/linked-test
```

**Expected (PR-A behavior):**
- Fork worktree created as a sibling of `parent-wt` (path computed per your worktree settings)
- Fork worktree's HEAD == parent-wt's HEAD (NOT seed-wt's HEAD)
- `wip.txt` present in the fork worktree with content `wip-content`

```bash
# Verify
FORK_PATH=$(git -C "$PROJ/.bare" worktree list --porcelain | awk '/^worktree/{p=$2} /^branch refs\/heads\/fork\/linked-test$/{print p}')
git -C "$FORK_PATH" rev-parse HEAD                   # should match parent-wt's HEAD
git -C "$PROJ/parent-wt" rev-parse HEAD              # parent's HEAD
diff <(git -C "$FORK_PATH" rev-parse HEAD) <(git -C "$PROJ/parent-wt" rev-parse HEAD) && echo "PASS: fork anchored at parent HEAD"
cat "$FORK_PATH/wip.txt"                             # should print wip-content
```

**Failure (the bug this gap describes):** fork's HEAD matches `seed-wt`'s, not `parent-wt`'s. Pre-PR-A code does this silently.

### Scenario 2 — Destination collision is refused, not silently reused (gap 2, CLOSED by PR-A CLI)

```bash
# Setup (assumes the same $PROJ + seeded session from Scenario 1)
# Pre-create the destination branch
git -C "$PROJ/.bare" branch fork/already-here main

# The thing under test
agent-deck session fork parent-session --with-state -w fork/already-here
# OR alternatively pre-create a worktree:
# git -C "$PROJ/.bare" worktree add -b fork/another-used "$PROJ/another-used-wt"
# agent-deck session fork parent-session --with-state -w fork/another-used
```

**Expected (PR-A behavior):** non-zero exit. Error message exactly:

> `branch 'fork/already-here' already exists; choose a new destination branch for --with-state`

For the worktree-collision variant:

> `branch 'fork/another-used' already has a worktree at <path>; choose a new destination branch for --with-state`

The message must include the conflicting path so you can `cd` to it.

**Failure (pre-PR-A):** "Reusing existing worktree at … for branch fork/already-here" is printed and the fork proceeds into the pre-existing worktree without running materialization. Strictly worse than the silent-shared-worktree footgun this feature was supposed to fix.

### Scenario 3 — Failed forks clean up (gap 3, CLOSED by PR-A CLI)

The hardest scenario to trigger by hand because materialization rarely fails on a normal repo. Two practical ways:

**Method A: Corrupt the parent's HEAD ref after worktree creation.** This requires injecting a fault between `CreateWorktreeAtStartPoint` and `MaterializeWipFromParent`. Not externally observable; covered by `TestSessionFork_WithState_RejectsExistingDestinationBranch` indirectly.

**Method B: Trigger a clean materialize failure via mid-rebase parent.**

```bash
# In parent-wt, induce a rebase conflict (will leave .git/rebase-merge state)
cd "$PROJ/parent-wt"
git checkout -b conflict-branch
echo "first" > README.md && git commit -am "first"
git checkout parent-branch
echo "second" > README.md && git commit -am "second"
git rebase conflict-branch || true   # expected to leave conflict state

agent-deck session fork parent-session --with-state -w fork/cleanup-test
```

**Expected (PR-A behavior):**
- Exits non-zero
- Error message mentions `mid-rebase` (note: not the actionable-text version — see gap 6)
- After the error, `git -C "$PROJ/.bare" worktree list` does NOT contain `fork/cleanup-test`
- `git -C "$PROJ/.bare" branch --list fork/cleanup-test` returns empty (branch deleted because `createdBranch` proof said this operation created it)

**Failure (pre-PR-A):** the half-baked worktree and branch stay on disk, requiring manual `git worktree remove --force` + `git branch -D`.

### Scenario 4 — TUI dialog (gap 4, NOT CLOSED by PR-A — flagged here so testers don't miss it)

```bash
# Launch agent-deck TUI
agent-deck

# Press 'f' on a Claude session to open Fork dialog
# Press 'w' to enable "Create in worktree"
# Look for: a nested checkbox "Carry parent state (press y)"
```

**Expected (PR-A behavior):** NO such checkbox appears. The TUI dialog is unchanged from upstream — TUI users still have to drop to the CLI.

This is the entire scope of **PR-B**. Re-test this scenario after PR-B lands.

### Scenario 5 — Full dirty-state materialization (gap 5 file half, CLOSED by PR-A; conversation half NOT verified)

```bash
# In a fresh parent-wt
cd "$PROJ/parent-wt"
echo "*.env" > .gitignore
git add .gitignore && git commit -m gitignore

# Staged change
echo "staged" > README.md
git add README.md

# Unstaged change on top of staged
echo "staged-then-unstaged" > README.md

# Untracked file
echo "new-untracked" > new.txt

# Gitignored file
echo "API_KEY=secret" > local.env

agent-deck session fork parent-session --with-state-and-gitignored -w fork/full-test
```

**Expected (PR-A behavior):**
- Fork worktree has `README.md` showing the staged-then-unstaged content
- `git -C <fork-path> diff --cached` matches parent's `git diff --cached`
- `git -C <fork-path> diff` matches parent's `git diff`
- `new.txt` present in fork with `new-untracked`
- `local.env` present in fork with `API_KEY=secret` (this is the `-and-gitignored` discriminator)

**Without `-and-gitignored`:** `local.env` should NOT be in the fork.

**Conversation continuity (NOT VERIFIED by PR-A):** the new Claude Code session is started via `claude --resume <parent-id> --fork-session`. PR-A's eval test stubs out `claude` so this codepath is not actually exercised end-to-end. To manually test: install a real fake-claude or run against a real Claude install and visually confirm the new session shares conversation history with the parent.

### Scenario 6 — Error message quality (gap 6, CLOSED by PR-A)

Both destination collision AND mid-op refusal messages now include actionable hints.

```bash
# From Scenario 3 mid-rebase state, observe the exact error:
agent-deck session fork parent-session --with-state -w fork/midrebase-msg
```

**Expected (PR-A behavior):** the error message exactly matches:

> `parent session is mid-rebase; finish or abort the rebase before forking with state (cd <parent-path> && git rebase --abort)`

Equivalents for the other four kinds (replace `rebase` and `git rebase --abort`):
- `merge` → `git merge --abort`
- `cherry-pick` → `git cherry-pick --abort`
- `revert` → `git revert --abort`
- `bisect` → `git bisect reset`

Refusal now happens BEFORE worktree creation — no orphan worktree to clean up on this path. Pinned by `TestEval_SessionForkWithState_RefusesMidRebaseParent` against the real binary.

### Scenario 7 — Submodule warning (gap 7, CLOSED by PR-A)

```bash
# In parent-wt, create a fake submodule entry
cd "$PROJ/parent-wt"
cat > .gitmodules <<'EOF'
[submodule "lib"]
  path = lib
  url = https://example.invalid/lib.git
EOF
git add .gitmodules && git commit -m submodule

agent-deck session fork parent-session --with-state -w fork/submodule-test 2>&1 | head
```

**Expected (PR-A behavior):** stderr contains the line:

> `Warning: submodules detected — copied as files, not recursed (parent's submodule states preserved)`

Emitted exactly once, before any worktree creation. Doesn't block the operation — fork proceeds normally; submodule files are copied as plain files (the submodule's internal `.git` state isn't recursed, as the warning says). Pinned by `TestEval_SessionForkWithState_SubmoduleWarning` against the real binary.

### Scenario 8 — Setup hook ordering (gap 8, CLOSED by PR-A)

```bash
# In parent-wt, install a setup hook that reads a WIP file
mkdir -p "$PROJ/.agent-deck"
cat > "$PROJ/.agent-deck/worktree-setup.sh" <<'EOF'
#!/bin/sh
set -e
if [ -f wip.txt ]; then
  echo "OBSERVED:$(cat wip.txt)" > /tmp/fork-setup-marker.txt
else
  echo "NO_WIP" > /tmp/fork-setup-marker.txt
fi
EOF
chmod +x "$PROJ/.agent-deck/worktree-setup.sh"

# Dirty parent with a WIP file
echo "wip-after-pra" > "$PROJ/parent-wt/wip.txt"

rm -f /tmp/fork-setup-marker.txt
agent-deck session fork parent-session --with-state -w fork/hook-order
cat /tmp/fork-setup-marker.txt
```

**Expected:** `/tmp/fork-setup-marker.txt` contains `OBSERVED:wip-after-pra`.

**Failure (pre-fix):** `NO_WIP`, proving the setup hook ran before materialization.

PR-A's automated test (`TestForkWithState_SetupHookObservesMaterializedState`) pins this; this manual scenario is for sanity.

---

## Cleanup after testing

```bash
# Remove any worktrees/branches you created during testing
cd "$PROJ/.bare" && git worktree list
# git worktree remove --force <path>
# git branch -D fork/<name>

# Remove scratch projects
rm -rf "$PROJ"

# Remove the marker file from Scenario 8
rm -f /tmp/fork-setup-marker.txt
```

## Stale-tmux-session caveat

The eval test `TestEval_SessionForkWithState_RealBinary` can leave `agentdeck_fork-eval_*` sessions on your default tmux socket. If you see `TestOuterTmuxGuard` failing after running PR-A tests, kill those stragglers:

```bash
tmux ls 2>/dev/null | grep agentdeck_fork-eval | awk -F: '{print $1}' | xargs -I{} tmux kill-session -t {} 2>/dev/null
```

## What this doc does NOT cover

- TUI integration — PR-B
- Conversation-continuity end-to-end (gap 5 conversation half) — needs a real `claude` binary or richer fake
- Shared `PreflightForkWithState` typed-error extraction (gap 11) — deferred to PR-C with RFC
