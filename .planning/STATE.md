---
gsd_state_version: 1.0
milestone: v1.5.3
milestone_name: milestone
status: executing
last_updated: "2026-04-15T11:42:11.198Z"
last_activity: 2026-04-15
progress:
  total_phases: 3
  completed_phases: 3
  total_plans: 4
  completed_plans: 4
  percent: 100
---

# Project State

## Project Reference

**Project:** Agent Deck
**Repository:** /home/ashesh-goplani/agent-deck
**Branch:** fix/feedback-closeout
**Current version:** v1.5.2
**Target version:** v1.5.3

See `/home/ashesh-goplani/agent-deck/.planning/PROJECT.md` for full project context.
See `/home/ashesh-goplani/agent-deck/.planning/ROADMAP.md` for the v1.5.3 roadmap (pending).
See `/home/ashesh-goplani/agent-deck/.planning/REQUIREMENTS.md` for requirements and phase mappings.

**Core value:** Reliable session management for AI coding agents.
**Current focus:** Phase 03 — docs-and-mandate

## Milestone: v1.5.3 — Feedback Closeout

**Goal:** Replace the `D_PLACEHOLDER` GitHub Discussion node ID in `internal/feedback/sender.go:18`, add a format regression test, document the feature in README, and lock mandatory test coverage via CLAUDE.md.

**Source spec:** `docs/FEEDBACK-CLOSEOUT-SPEC.md`
**Starting point:** v1.5.2 (top commit `9c0295d` on `fix/session-persistence` — REQ-7 custom-command JSONL resume fix)
**Branch:** `fix/feedback-closeout`

## Current Position

Phase: 03
Plan: Not started
Status: Executing Phase 03
Last activity: 2026-04-15

## Accumulated Context

### v1.5.3 Milestone Init (2026-04-15)

- Previous milestone (v1.5.2) shipped the REQ-7 custom-command JSONL resume fix (top commit `9c0295d`).
- v1.5.3 is a hotfix closeout branch off `main` after v1.5.2, not a feature milestone.
- Feedback feature is 95% complete on `main`: CLI (`agent-deck feedback`) + `Ctrl+E` TUI shortcut + FeedbackDialog landed across phases 01-01, 01-02, 02-01, 03-01. Current branch top commits: `775ec29` (docs SKILL.md update), `848539e` (feedbackSender wiring), `691a74c` (CLI subcommand), `82c8cf2` (RED tests), `e190efb` (FeedbackDialog wire).
- 22 feedback tests currently green: 11 in `internal/feedback`, 9 in `internal/ui` FeedbackDialog, 2 in `cmd/agent-deck` feedback handler.
- Sole release blocker: `internal/feedback/sender.go:18` holds `const DiscussionNodeID = "D_PLACEHOLDER"`. GraphQL tier silently fails against this value; fallback tiers (gh clipboard+browser, clipboard-only) work, so no user-visible regression — but the intended path is dead.
- v1.6.0 Watcher Framework work is paused. Old phase directories (13-watcher-engine-core, 14-simple-adapters-webhook-ntfy-github, 15-slack-adapter-and-import) belong to the paused milestone and will be cleared as part of this milestone init.

### Hard rules for v1.5.3

- No `git push`, `git tag`, `gh pr create`, `gh pr merge`.
- No `rm` — use `trash`.
- TDD is non-negotiable: `TestSender_DiscussionNodeID_IsReal` lands RED first, then const change flips it GREEN.
- Sign commits "Committed by Ashesh Goplani". No Claude attribution.
- No `--no-verify`.
- No scope creep beyond the files listed in REQ-FB-4's mandate block.

### Success criteria

1. `go test ./internal/feedback/... ./internal/ui/... ./cmd/agent-deck/... -run "Feedback|Sender_" -race -count=1` returns 23 passing tests (22 existing + 1 new).
2. Manual end-to-end feedback submit creates a Discussion comment in the right repo.
3. `grep -i "feedback" README.md` and `grep "Feedback feature: mandatory test coverage" CLAUDE.md` both match.
4. No push/tag/PR on this branch.
