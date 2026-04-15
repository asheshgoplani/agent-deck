# Requirements: Agent Deck — v1.5.3 Feedback Closeout

**Defined:** 2026-04-15
**Core Value:** Reliable session management for AI coding agents.
**Source spec:** `docs/FEEDBACK-CLOSEOUT-SPEC.md`

## Milestone v1.5.3 Requirements

Closeout scope for the in-product feedback feature. No new features. Four requirements, each maps to exactly one phase.

### Feedback Closeout

- [ ] **REQ-FB-1** (P0): Real GitHub Discussion node ID replaces `D_PLACEHOLDER` in `internal/feedback/sender.go:18`. Resolution path: run `gh api graphql -f query='{ repository(owner: "asheshgoplani", name: "agent-deck") { discussions(first: 10) { nodes { id title } } } }'` (or the category-scoped variant) and paste the correct `id`.

  **Acceptance:**
  - `DiscussionNodeID != "D_PLACEHOLDER"` — verified by REQ-FB-2 test.
  - Manual end-to-end test: one `agent-deck feedback 4 "closeout smoke"` invocation from a non-headless host successfully creates a comment in the target Discussion (verify via `gh api` or browser).
  - The node ID appears in exactly one place (the const). No duplicate literal.

- [ ] **REQ-FB-2** (P0): Format regression test `TestSender_DiscussionNodeID_IsReal` exists in `internal/feedback/sender_test.go` and asserts:
  1. `DiscussionNodeID != "D_PLACEHOLDER"`.
  2. `DiscussionNodeID` matches regex `^D_[A-Za-z0-9_-]{10,}$` (GitHub GraphQL node ID shape — conservative pattern accepting the current encoding).

  **Acceptance:**
  - Test exists and is RED against current `D_PLACEHOLDER`, then GREEN after REQ-FB-1.
  - Test would fail if anyone reintroduces `D_PLACEHOLDER` or a typoed constant.
  - Independently runnable: `go test -run TestSender_DiscussionNodeID_IsReal ./internal/feedback/`.

- [ ] **REQ-FB-3** (P1): README has a top-level "Feedback" section (or inside an existing Features section) that states:
  - User can press `Ctrl+E` in the TUI, or run `agent-deck feedback`, to send feedback.
  - Feedback posts to a public GitHub Discussion (link the Discussion URL).

  **Acceptance:**
  - `grep -i "ctrl+e" README.md` returns a match.
  - `grep -i "agent-deck feedback" README.md` returns a match.
  - `agent-deck --help` still shows the `feedback` subcommand (no regression).

- [ ] **REQ-FB-4** (P0): CLAUDE.md gains a "Feedback feature: mandatory test coverage" section that:
  - Names the 22 existing tests at suite granularity: `internal/feedback` (11), `internal/ui` FeedbackDialog (9), `cmd/agent-deck` feedback handler (2), plus the new `TestSender_DiscussionNodeID_IsReal`.
  - Declares that any PR modifying files in `internal/feedback/**`, `internal/ui/feedback_dialog.go`, `cmd/agent-deck/feedback_cmd.go`, or `internal/platform/headless.go` must include output of `go test ./internal/feedback/... ./internal/ui/... ./cmd/agent-deck/... -run "Feedback|Sender_" -race -count=1` in the PR description.
  - Declares that reintroducing a placeholder node ID is a blocker, not a warning.

  **Acceptance:** Section is present and references files by path. `grep "Feedback feature: mandatory test coverage" CLAUDE.md` matches.

## v1.6.0 Requirements (Paused — Resumes After v1.5.3)

The full v1.6.0 Watcher Framework requirement catalog is preserved in `.planning/PROJECT.md` under "Paused Milestone: v1.6.0 — Watcher Framework" and resumes after v1.5.3 ships. Not scoped for this milestone.

## Out of Scope (v1.5.3)

| Feature | Reason |
|---------|--------|
| Changing the Sender three-tier fallback logic | Already works; changing it expands blast radius beyond closeout. |
| Changing the FeedbackDialog UI | Feature is UX-complete; out of scope for closeout. |
| Touching `internal/platform/headless.go` beyond maintaining existing `IsHeadless()` behavior | Not a closeout concern. |
| Adding new feedback categories | No new features in a closeout milestone. |
| Any `git push`, `git tag`, `gh pr create`, `gh pr merge` | Hard rule from spec — branch stays local. |

## Traceability

Populated during roadmap creation.

| Requirement | Phase | Status |
|-------------|-------|--------|
| REQ-FB-1 | Phase 2 (Real Discussion Node ID) | Pending |
| REQ-FB-2 | Phase 1 (RED Format Regression Test) | Pending |
| REQ-FB-3 | Phase 3 (Docs and Mandate) | Complete |
| REQ-FB-4 | Phase 3 (Docs and Mandate) | Complete |

**Coverage:**
- v1.5.3 requirements: 4 total
- Mapped to phases: 4
- Unmapped: 0 ✓

---
*Requirements defined: 2026-04-15*
*Last updated: 2026-04-15 after milestone v1.5.3 initialization*
