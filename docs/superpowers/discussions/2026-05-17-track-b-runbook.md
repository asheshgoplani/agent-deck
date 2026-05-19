# Track B Runbook — Parallel Comparative Analysis vs Upstream's #1029 Implementation

**Date:** 2026-05-17
**Status:** Settled — ready for manual execution

## Situation

Issue #1029 (filed by @smorin) requested a fork-with-WIP-state feature. Upstream maintainer @asheshgoplani has implemented a working core on branch `upstream/feat/v1.9.x-1029-fork-carry-wip` (commit `02b6e5c4`, May 18 2026). In parallel, smorin had drafted a much more comprehensive design + plan in this clone (`/Users/stevemorin/c/agent-deck`, branch `feature/fork-worktree-with-state`).

The two implementations have the same flag names (`--with-state`, `--with-state-and-gitignored`), same core approach (diff-based + parent-read-only), and same edge-case philosophy (refuse mid-rebase/merge/cherry-pick/bisect, materialize before setup hook). But smorin's plan appears to be a substantial superset, particularly around TUI integration, linked-worktree parent-HEAD correctness, shared CLI/TUI validators, and test coverage breadth.

This runbook coordinates three parallel tracks of work.

## Tracks

| Track | Where | What | Status |
|---|---|---|---|
| **A** | This clone — `/Users/stevemorin/c/agent-deck`, branch `feature/fork-worktree-with-state` | Smorin's independent design and plan | In hand, 3 commits ahead of upstream/main, NOT pushed yet |
| **B** | New worktree — `/Users/stevemorin/c/agent-deck-1029`, branch `review/feat-v1.9.x-1029-fork-carry-wip` | Comparative analysis of upstream's commit vs Track A | To create + populate via a new Claude Code session |
| **C** | GitHub — Issue #1029 + any upstream PR | Public coordination with @asheshgoplani | Comment goes out **after** Track B's analysis lands |

Tracks A and B share the same `.git` directory via `git worktree add`, so refs / exclude / hooks are common. Each worktree has its own HEAD and working tree.

---

## Section 1 — Create the parallel worktree (Track B)

**Run from this clone:** `/Users/stevemorin/c/agent-deck`

### 1a. Create the worktree

```bash
git worktree add /Users/stevemorin/c/agent-deck-1029 upstream/feat/v1.9.x-1029-fork-carry-wip
```

This:
- Creates `/Users/stevemorin/c/agent-deck-1029` as a sibling directory
- Checks out upstream's branch tip in detached HEAD state
- Leaves Track A (`feature/fork-worktree-with-state` in this clone) unaffected

### 1b. Create the review branch in the new worktree

```bash
cd /Users/stevemorin/c/agent-deck-1029
git switch -c review/feat-v1.9.x-1029-fork-carry-wip
git log --oneline -5
# Expected top line: 02b6e5c4 feat(fork): --with-state and --with-state-and-gitignored ...
```

### 1c. Make per-developer files available

The `.git/info/exclude` from Track A is shared (since `.git` is shared). `CLAUDE.md`, `CLAUDE.local.md`, `AGENTS.md` are already ignored.

Symlink the actual files in so Claude Code / Codex find their guidance:

```bash
ln -s /Users/stevemorin/c/agent-deck/CLAUDE.local.md CLAUDE.local.md
ln -s /Users/stevemorin/c/agent-deck/AGENTS.md AGENTS.md
ls -la CLAUDE.local.md AGENTS.md
```

### 1d. Sanity check

```bash
git worktree list
# Expected:
#   /Users/stevemorin/c/agent-deck            <HEAD>  [feature/fork-worktree-with-state]
#   /Users/stevemorin/c/agent-deck-1029       <HEAD>  [review/feat-v1.9.x-1029-fork-carry-wip]
```

---

## Section 2 — Launch the parallel Claude Code session in Track B

Open a fresh terminal tab/window so the existing session in `/Users/stevemorin/c/agent-deck` keeps running independently.

Pick one option below. **Option A is the preferred default** because it gives you a complete parallel environment (conversation + files), not just files. Option B is the alternative if you want a clean session.

### Option A — Fork the parent Claude Code session into the worktree (preferred default)

Dogfood the now-merged `--with-state-and-gitignored` flag to fork BOTH the Claude conversation AND the working-tree state into the parallel worktree. The new session inherits the full conversation history from the parent (so you don't need a long bootstrap prompt), plus all of the parent's tracked + staged + untracked + gitignored files (so `CLAUDE.local.md`, `AGENTS.md`, `.env`, etc. carry over automatically).

**Caveat:** `--with-state` creates the destination worktree branched off the *parent session's HEAD* (i.e., your `feature/fork-worktree-with-state`), not off upstream's `feat/v1.9.x-1029-fork-carry-wip`. For the analysis task, this doesn't matter — the analyzer reads upstream's code via `git show 6a1645eb` or `git diff upstream/main~1..upstream/main` rather than relying on a checkout of upstream's branch.

If you do want upstream's code checked out instead, use Option B.

**From this clone**, find the current session's ID/title and fork it:

```bash
cd /Users/stevemorin/c/agent-deck

# Find the current session — look for the one running this Claude Code instance
agent-deck session list

# Replace <SESSION> with the matching id or title:
agent-deck session fork <SESSION> \
    --with-state-and-gitignored \
    -w review/feat-v1.9.x-1029-fork-carry-wip \
    -t track-b-analysis \
    -g experiments
```

This will:
- Refuse early if parent is mid-rebase/merge/cherry-pick/revert/bisect (shared preflight from upstream's commit)
- Create a new branch `review/feat-v1.9.x-1029-fork-carry-wip` from your current HEAD
- Compute a destination worktree path per your worktree settings (typically a sibling dir; the exact path is logged on success)
- Materialize parent's staged + unstaged + untracked + gitignored files into the new worktree
- Run any setup hook
- Start the forked Claude Code session in the new worktree with the parent's session as resume context

**After the fork succeeds**, switch your terminal to the new worktree, attach to the new session, and paste this short focus prompt (much shorter than the clean-session bootstrap because the new session already has full context):

```
We just forked this session into a parallel worktree to do a comparative
analysis of upstream's #1029 implementation vs my spec/plan.

Your task: produce the comparative analysis described in
docs/superpowers/discussions/2026-05-17-track-b-runbook.md and save it to
docs/superpowers/discussions/2026-05-18-upstream-comparison.md.

Read upstream's commit 6a1645eb files first (now in upstream/main):
    git show 6a1645eb -- cmd/agent-deck/session_cmd.go
    git show 6a1645eb -- internal/git/materialize_wip.go
    git show 6a1645eb -- internal/git/issue1029_with_state_test.go
    git show 6a1645eb -- internal/git/issue1029_edge_test.go
    git show 6a1645eb -- internal/git/setup.go

Then compare against the spec and plan you already know from this
conversation. Cite file:line for every claim. Don't push or open a PR
when done; just commit the analysis doc.
```

### Option B — Clean Claude Code session with full bootstrap prompt (alternative)

Use this if you want upstream's code physically checked out in the worktree (better for IDE-style exploration), or if you don't want to fork the conversation (for a fresh independent context).

This option uses the manual worktree from Section 1.

```bash
cd /Users/stevemorin/c/agent-deck-1029
claude    # or however you launch Claude Code locally
```

### Bootstrap prompt for Option B (paste verbatim into the new session)

```
I'm starting a new Claude Code session in this worktree to do a comparative analysis.

CONTEXT:
- This worktree is /Users/stevemorin/c/agent-deck-1029, checked out to branch
  review/feat-v1.9.x-1029-fork-carry-wip, which is based on upstream's branch
  feat/v1.9.x-1029-fork-carry-wip (commit 02b6e5c4, now merged to upstream/main
  as 6a1645eb in PR #1030 by @asheshgoplani).
- Ashesh has implemented Issue #1029 (fork session with WIP state) with these files:
    cmd/agent-deck/session_cmd.go             |  16 +-
    internal/git/issue1029_edge_test.go       | 233 +
    internal/git/issue1029_with_state_test.go | 133 +
    internal/git/materialize_wip.go           | 224 +
    internal/git/setup.go                     |  26 +
- I (Steve, the issue author) independently designed and planned this feature
  in much more detail in a sibling clone at /Users/stevemorin/c/agent-deck,
  on branch feature/fork-worktree-with-state. My design lives at:
    ../agent-deck/docs/superpowers/specs/2026-05-14-fork-worktree-with-state-design.md
    ../agent-deck/docs/superpowers/plans/2026-05-14-fork-worktree-with-state.md
    ../agent-deck/docs/superpowers/discussions/2026-05-17-item-4-collision-check.html

YOUR TASK:
Produce a comprehensive comparative analysis between Ashesh's implementation
(on this branch) and my design (in ../agent-deck/docs/superpowers/). Save it to:
    docs/superpowers/discussions/2026-05-18-upstream-comparison.md

The analysis should cover, with file:line citations:
1. What Ashesh's commit implements correctly that matches my spec
2. What Ashesh's commit implements differently from my spec (and which is better)
3. What my spec specifies that Ashesh's commit does NOT implement (the gaps)
4. What Ashesh's commit covers that my spec did NOT anticipate
5. Risk assessment per gap: which gaps are correctness bugs vs nice-to-haves
6. Recommended path forward (specific follow-up PR scope, or fold-in suggestions)

Focus areas (from my prior review):
- TUI integration (Ashesh's commit appears to only touch cmd/, not internal/ui/)
- Parent-HEAD start point for linked parent worktrees (my design's FWS-002)
- Shared CLI/TUI validators (PreflightForkWithState, ValidateForkWithStateDestination)
- createdBranch proof for cleanup (FWS-003)
- Test coverage breadth: parent-untouched invariant, bare-repo, setup-hook
  ordering, CLI contract via before-start hook, behavioral eval

GROUND RULES:
- Read ALL of Ashesh's files in full before drawing conclusions:
    cmd/agent-deck/session_cmd.go (focus on the diff vs upstream/main~1)
    internal/git/materialize_wip.go
    internal/git/issue1029_with_state_test.go
    internal/git/issue1029_edge_test.go
    internal/git/setup.go (diff vs upstream/main~1)
- Be specific: cite file:line for every claim.
- Don't write production code in this session — analysis only. Implementation
  happens in a follow-up PR.
- When the analysis doc is ready, commit it on review/feat-v1.9.x-1029-fork-carry-wip
  and STOP. Do not push, do not open a PR.
- The conclusion section of the analysis should be paste-ready for a GitHub
  comment on Issue #1029 (collaborative tone, not adversarial).

Start by:
1. Listing the files you'll read
2. Reading them
3. Reading my spec and plan from ../agent-deck/docs/superpowers/
4. Producing the analysis doc

The output document at docs/superpowers/discussions/2026-05-18-upstream-comparison.md
will be referenced from a GitHub comment posted by me (smorin) after you're
done.
```

The Option B session has no memory of this conversation — the bootstrap is self-contained.

### Which to pick

| Use case | Pick |
|---|---|
| You want the new session to "feel like" the same conversation continuing in a parallel place | **Option A** |
| Per-developer files (`CLAUDE.local.md`, `AGENTS.md`, `.env`) should auto-carry over | **Option A** (the gitignored flag handles it) |
| You want upstream's branch physically checked out in the worktree | **Option B** |
| You want a clean independent context (e.g., the new session shouldn't be biased by prior reasoning) | **Option B** |
| You want to dogfood the merged feature on a real workflow | **Option A** |

---

## Section 3 — GitHub coordination (Track C)

**Run from any worktree once `gh` rate limit resets** (`gh api rate_limit --jq .resources.core` to check).

### 3a. Verify state of upstream issue / PR

```bash
gh issue view 1029 --comments
gh pr list --repo asheshgoplani/agent-deck --search "head:feat/v1.9.x-1029-fork-carry-wip" --state all
```

Note any PR number for cross-posting.

### 3b. Wait for Track B's analysis to land

Comment timing decision: post **after** Track B completes its `docs/superpowers/discussions/2026-05-18-upstream-comparison.md`. Don't post sooner.

### 3c. Post the comment

Template (edit before posting):

```markdown
Hi @asheshgoplani — thanks for the quick turnaround on #1029!

I had been working in parallel on a more detailed design and plan for this feature in my fork. Now that your implementation is on `feat/v1.9.x-1029-fork-carry-wip`, I ran a comparative analysis and wanted to share what I found.

**Common ground (great work):**
- Flag names match exactly: `--with-state`, `--with-state-and-gitignored`
- Diff-based approach with parent-read-only
- Refuses mid-rebase/merge/cherry-pick/bisect (you also added revert — nice catch)
- Materialize before setup hook
- Binary + symlink handling

**Gaps I identified (need confirmation whether intended or follow-up scope):**
- TUI integration — your commit appears to only touch `cmd/`, no changes under `internal/ui/`. My design has matching `ForkDialog` sub-checkboxes (`y` for with-state, `i` for gitignored) with nested-state invariants and a target-based focus order.
- Parent-HEAD start point — when the parent session lives in a linked worktree whose HEAD differs from the main/base worktree, the fork should start from the parent's HEAD, not the invocation repo's HEAD. My design adds `git.CreateWorktreeAtStartPoint` for this.
- Shared CLI validators — my design extracts `PreflightForkWithState` and `ValidateForkWithStateDestination` so CLI and TUI can't drift.
- `createdBranch` proof for cleanup — so future refactors of the early-gate code can't accidentally `git branch -D` a pre-existing branch.
- Test coverage: parent-untouched invariant, mid-rebase refusal with destination-absence assertion, bare-repo-layout with linked parent worktree, setup-hook ordering with parent-WIP-aware script, CLI contract tests via a before-start hook, behavioral eval smoke for the visible TUI flow.

Full design and plan are in my fork:
- Spec: `docs/superpowers/specs/2026-05-14-fork-worktree-with-state-design.md`
- Plan: `docs/superpowers/plans/2026-05-14-fork-worktree-with-state.md`
- Item-4 decision exploration: `docs/superpowers/discussions/2026-05-17-item-4-collision-check.html`
- **Comparative gap analysis (this is the detailed one):** `docs/superpowers/discussions/2026-05-18-upstream-comparison.md`

How would you like to proceed? Options I see:
1. Land your PR as-is; I open follow-up PRs for the gaps (smallest blast radius for your merge)
2. I rebase my work onto your branch and submit a combined PR
3. You pick the top N gaps you want folded in before merge and I push fixes to your branch

Happy to do any of these. Let me know which fits best.
```

Cross-post the same comment to the upstream PR (if one exists).

---

## Section 4 — What to do in the original session (Track A)

While Track B's session runs:

- Do not push `feature/fork-worktree-with-state`. Hold for upstream coordination.
- Wait for Track B to finish.
- When Track B's analysis doc is ready, copy it back into Track A's clone (or just reference it via the shared `.git` — the branch is in the same repo, just check out the file from the analysis branch).
- Use Track A's session to draft and refine the GitHub comment using Track B's findings.

**Status update (2026-05-18):** Track B's analysis landed as [`2026-05-18-post-merge-gap-analysis.md`](2026-05-18-post-merge-gap-analysis.md) (committed to the feature branch). Upstream PR #1030 has since been merged at commit `6a1645eb`. The next move is now: execute the followup plan at [`../plans/2026-05-18-fork-with-state-followup.md`](../plans/2026-05-18-fork-with-state-followup.md), which scopes the gaps into PR-A (correctness + CLI tests) and PR-B (TUI). The active spec is [`../specs/2026-05-18-fork-with-state-followup-design.md`](../specs/2026-05-18-fork-with-state-followup-design.md). The GitHub comment template in Section 3 of this runbook stays valid; post it after PR-A is ready to push.

---

## Recovery scenarios

| What if | Recovery |
|---|---|
| Track B's worktree gets corrupted | `git worktree remove /Users/stevemorin/c/agent-deck-1029 --force` from Track A, then re-run Section 1 |
| Track A's branch needs updating | Track A and Track B's branches are independent; no cross-impact |
| Upstream merges their PR while Track B is running | Track B's analysis is still valuable; the recommendation section pivots from "fold-in" to "follow-up PR scope" |
| `claude` in the new session loses context | Re-paste the bootstrap prompt; the prompt is self-contained |

## Cleanup when Track B is done

After the GitHub comment lands and the analysis is shared:

```bash
# Optionally remove the parallel worktree (preserves the branch in .git):
cd /Users/stevemorin/c/agent-deck
git worktree remove ../agent-deck-1029
# The review/feat-v1.9.x-1029-fork-carry-wip branch still exists; can be pushed
# to origin later if needed for collaborator visibility.
```
