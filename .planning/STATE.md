---
gsd_state_version: 1.0
milestone: v1.5.4
milestone_name: milestone
status: planning
last_updated: "2026-04-15T12:42:32.095Z"
last_activity: 2026-04-15
progress:
  total_phases: 3
  completed_phases: 1
  total_plans: 1
  completed_plans: 1
  percent: 100
---

# Project State — v1.5.4

## Project Reference

**Project:** Agent Deck
**Repository:** /home/ashesh-goplani/agent-deck
**Worktree:** `/home/ashesh-goplani/agent-deck/.worktrees/per-group-claude-config`
**Branch:** `fix/per-group-claude-config-v154`
**Starting point:** v1.5.3 (`ee7f29e` on `fix/feedback-closeout`)
**Base:** `fa9971e` (upstream PR #578 by @alec-pinson)
**Target version:** v1.5.4

See `.planning/PROJECT.md` for full project context.
See `.planning/ROADMAP.md` for the v1.5.4 phase plan.
See `.planning/REQUIREMENTS.md` for CFG-01..07 and phase mapping.
See `docs/PER-GROUP-CLAUDE-CONFIG-SPEC.md` for the source spec.

## Milestone: v1.5.4 — Per-group Claude Config

**Goal:** Accept PR #578's config schema + lookup as base, close adoption gaps for the user's conductor use case (custom-command injection, env_file sourcing), ship 6 regression tests + a visual harness + docs, with attribution to @alec-pinson.

**Estimated duration:** 60–90 minutes across 3 phases.

## Current Position

Phase: 2
Plan: Not started
Status: Phase 01 complete; awaiting Phase 02 planning
Last activity: 2026-04-15

## Phase Progress

| # | Phase | Status | Requirements | Plans |
|---|-------|--------|--------------|-------|
| 1 | Custom-command injection + core regression tests | Complete | CFG-01, CFG-02, CFG-04 (tests 1, 2, 3, 6) | 1/1 (01-01) |
| 2 | env_file source semantics + observability + conductor E2E | Pending | CFG-03, CFG-04 (tests 4, 5), CFG-07 | — |
| 3 | Visual harness + documentation + attribution commit | Pending | CFG-05, CFG-06 | — |

## Phase 01 commits (since base 3e402e2)

| Hash | Type | Subject |
|------|------|---------|
| 4730aa5 | docs | docs(planning): plan phase 01 — custom-command injection + core regression tests |
| 40f4f04 | test | test(session): add per-group Claude config regression tests (CFG-04 tests 1/2/3/6) |
| b39bbf3 | fix | fix(session): export CLAUDE_CONFIG_DIR for custom-command sessions (CFG-02) |

## Hard rules in force (carried from CLAUDE.md + spec)

- No `git push`, `git tag`, `gh release`, `gh pr create`, `gh pr merge`.
- No `rm` — use `trash`.
- No `--no-verify` (v1.5.3 mandate at repo-root `CLAUDE.md`).
- No Claude attribution in commits. Sign: "Committed by Ashesh Goplani".
- TDD: test before fix; test must fail without the fix.
- Additive only vs PR #578 — do not revert or refactor its existing code.
- At least one commit must carry: "Base implementation by @alec-pinson in PR #578."

## Next action (from conductor)

The user instructed: **stop after bootstrapping the roadmap. Do NOT auto-plan.** The conductor will spawn `gsd-v154-plan-1` to plan Phase 1.

When that happens, the phase-1 planner should:

1. Read `.planning/PROJECT.md`, `.planning/ROADMAP.md`, `.planning/REQUIREMENTS.md`, `docs/PER-GROUP-CLAUDE-CONFIG-SPEC.md`.
2. Run `/gsd-plan-phase 1` to produce `.planning/phases/01-custom-command-injection/PLAN.md`.
3. Honor the scope list in REQUIREMENTS.md — any touch outside is escalation.

## Accumulated Context

Prior milestones on main (not relevant to this branch's scope but preserved for context): v1.5.0 premium web app polish, v1.5.1/1.5.2/1.5.3 patch work, v1.6.0 Watcher Framework in progress on main.

v1.6.0 phase directories (`.planning/phases/13-*`, `14-*`, `15-*`) are leakage from main's `.planning/` into this worktree. They are left untouched. This milestone's phase dirs will be `01-*`, `02-*`, `03-*`.
