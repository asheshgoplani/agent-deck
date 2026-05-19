# Fork-with-State Followup Meta-Plan

**Date:** 2026-05-18
**Branch:** `feature/fork-worktree-with-state`
**Status:** Complete (2026-05-18)
**Purpose:** Plan the creation of a new followup spec + plan (replacing the deprecated ground-up spec + plan) that scopes the work to close 11 gaps identified after upstream merged #1029 (PR #1030, commit `6a1645eb`).

This document is meta — it's the runbook for creating the followup plan files, not the plan files themselves.

## Background

- Original spec: `docs/superpowers/specs/2026-05-14-fork-worktree-with-state-design.md` (449 lines, written before #1030 merged)
- Original plan: `docs/superpowers/plans/2026-05-14-fork-worktree-with-state.md` (~3700 lines, from-scratch implementation)
- Gap analysis: `docs/superpowers/discussions/2026-05-18-post-merge-gap-analysis.md` (identified 11 gaps after the merge)
- Decision: keep originals as deprecated/archived (banner only, no moves); create new followup spec + plan that build ON TOP of upstream's merged implementation.

## Gap → PR mapping (locked contract)

| Gap | PR | Why |
|---|---|---|
| 2. Parent-HEAD start point | **PR-A** | Correctness for linked-worktree parents |
| 3. Destination collision validation | **PR-A** | Correctness/UX for existing-branch case |
| 4. Cleanup-on-error (CLI) | **PR-A** | CLI surface |
| 4. Cleanup-on-error (TUI) | **PR-B** | TUI surface |
| 5. Parent-untouched invariant test | **PR-A** | Tests materialization contract PR-A relies on |
| 6. Bare-repo + linked parent worktree test | **PR-A** | Tests parent-HEAD path (gap 2) |
| 7. Setup-hook observation test | **PR-A** | Tests orchestration ordering |
| 8. 4 missing mid-op refusal tests | **PR-A** | General hardening |
| 9. CLI before-start hook contract test | **PR-A** | Verifies option propagation |
| 10. Behavioral eval smoke (CLI) | **PR-A** | CLAUDE.md eval mandate (CLI) |
| 1. TUI integration | **PR-B** | TUI surface |
| 10. Behavioral eval smoke (TUI) | **PR-B** | CLAUDE.md eval mandate (TUI) |
| 11. Shared `PreflightForkWithState` extraction | **Deferred PR-C** | RFC-required; refactors upstream's just-merged code |

## Locked decisions

| Decision | Value |
|---|---|
| New spec filename | `docs/superpowers/specs/2026-05-18-fork-with-state-followup-design.md` |
| New plan filename | `docs/superpowers/plans/2026-05-18-fork-with-state-followup.md` |
| Old-file deprecation | Banner at top, no file moves |
| Test scope | All 11 gaps mapped (functional + test coverage) |
| PR shape | 2 PRs: PR-A (correctness + tests, CLI) + PR-B (TUI) |
| `ValidateForkWithStateDestination` scope | Shared `internal/git` helper added in PR-A; used by both surfaces |
| Validator file location | New `internal/git/fork_with_state_destination.go` (avoids touching upstream's just-merged `materialize_wip.go`) |
| Gap 11 documentation | One-paragraph "Out of scope (deferred)" mention pointing at future PR-C |
| Output of this meta-plan | One commit on `feature/fork-worktree-with-state`, no push yet |

## Execution checklist

Update each checkbox as the corresponding work completes. Parallel TaskTool entries (tasks #21-#25) track the same items in the harness's task system.

### Step 1 — Add deprecation banners to old files

- [x] **Step 1 complete** (both banners applied)
  - [x] `docs/superpowers/specs/2026-05-14-fork-worktree-with-state-design.md` — banner added at line 1
  - [x] `docs/superpowers/plans/2026-05-14-fork-worktree-with-state.md` — banner added at line 1

Banner content (identical, with file-specific link):

```markdown
> **⚠ DEPRECATED — superseded by [`<followup-file>`](<relative-path>)**
>
> This document was written as a ground-up design before upstream merged #1029
> (commit 6a1645eb). It remains as historical reference for the design
> reasoning, decision log, and FWS-001 through FWS-018 entries. The active
> design and plan are now in the followup files. See also the post-merge gap
> analysis at [`2026-05-18-post-merge-gap-analysis.md`](../discussions/2026-05-18-post-merge-gap-analysis.md).
```

### Step 2 — Draft the followup spec

- [x] **Step 2 complete** (new spec file created)
  - [x] `docs/superpowers/specs/2026-05-18-fork-with-state-followup-design.md` written

Sections in the new spec:
- Status / date / author / related code
- Premise (upstream's merged state)
- Goal (close 4 functional gaps + 7 test-coverage gaps; layer on top, don't replace)
- Non-goals (no upstream API refactor; no replacement of `MaterializeWipFromParent`)
- What upstream merged (short summary)
- Functional gaps to close (4) — each with PR target
- Test-coverage hardening (7) — each with PR target
- New code surfaces (file map summary)
- Mandate (Option B housing, single tracked section)
- References (original spec, original plan, gap analysis, runbook, upstream commit/PR)
- Out of scope (deferred PR-C for shared preflight helpers)
- Review change log (starts fresh; first entry "FUS-001 spec drafted as followup")

### Step 3 — Draft the followup plan

- [x] **Step 3 complete** (new plan file created)
  - [x] `docs/superpowers/plans/2026-05-18-fork-with-state-followup.md` written

Top-level structure:
- Standard header (goal, architecture, tech stack, spec pointer, pre-flight)
- File map (~10 entries, labeled PR-A / PR-B)
- **PR-A tasks** (Tasks 1-10):
  - T1: `HeadCommit` + `CreateWorktreeAtStartPoint` helpers (gap 2)
  - T2: `ValidateForkWithStateDestination` + `DestinationCollisionError` (gap 3, shared `internal/git`)
  - T3: Wire parent-HEAD + collision check + cleanup into `handleSessionFork` (gaps 2, 3, 4-CLI)
  - T4: CLI before-start hook + contract tests (gaps 8, 9)
  - T5: Parent-untouched invariant test (gap 5)
  - T6: Bare-repo + linked parent worktree test (gap 6)
  - T7: Setup-hook observation test (gap 7)
  - T8: CLI behavioral eval (gap 10-CLI)
  - T9: Verification (`make fmt`/`lint`/`test` + mandate suite)
  - T10: PR-A push + open
- **PR-B tasks** (Tasks 11-17):
  - T11: `ForkDialog` sub-checkbox state + getters (gap 1)
  - T12: `ForkDialog` focus-target refactor (gap 1)
  - T13: `ForkDialog` rendering + key handlers + tests (gap 1)
  - T14: TUI submit handler wires through `CreateWorktreeWithStateAndSetup` + collision check + cleanup (gaps 1, 3-TUI, 4-TUI)
  - T15: TUI behavioral eval (gap 10-TUI)
  - T16: Verification
  - T17: PR-B push + open (depends on PR-A merge)
- Spec coverage check table mapping every gap to its task(s)
- Out of scope: gap 11 (deferred PR-C with RFC)

### Step 4 — Cross-link

- [x] **Step 4 complete** (both cross-links applied)
  - [x] `docs/superpowers/discussions/2026-05-18-post-merge-gap-analysis.md` — "Now planned in" pointer added near the top
  - [x] `docs/superpowers/discussions/2026-05-17-track-b-runbook.md` — Section 4 updated with status note pointing at followup plan

### Step 5 — Commit

- [x] **Step 5 complete** (single commit landed on `feature/fork-worktree-with-state`)

Commit covers:
- Deprecation banners (Step 1)
- Followup spec (Step 2)
- Followup plan (Step 3)
- Cross-links (Step 4)
- This meta-plan doc itself

Commit message template:

```
docs: followup spec + plan for closing post-#1030 functional and test-coverage gaps

- Deprecate the original spec + plan (banners, left in place for history)
- Add followup spec scoped to deltas on top of upstream/main 6a1645eb
- Add followup plan with PR-A (correctness + test hardening, CLI) and
  PR-B (TUI) task lists
- Cross-link the gap analysis and runbook to the new files
- Gap 11 (shared preflight helpers) explicitly deferred to PR-C with RFC

This makes the next move: execute the followup plan to produce PR-A then PR-B.
```

## After this meta-plan executes

Branch state will be 7 commits ahead of upstream/main. The next decision is:
- Squash + push to `origin` and start PR-A code work?
- Or iterate on the followup plan first?

## Explicitly NOT doing in this meta-plan

- Writing PR-A or PR-B code (the followup plan drives that)
- Touching upstream's `MaterializeWipFromParent` or `CreateWorktreeWithStateAndSetup`
- Implementing the shared preflight helpers (gap 11, deferred PR-C)
- Posting the GitHub comment on Issue #1029 (still pending per runbook timing)
