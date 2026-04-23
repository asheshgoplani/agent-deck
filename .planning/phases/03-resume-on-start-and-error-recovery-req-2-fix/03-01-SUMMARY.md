---
phase: 03-resume-on-start-and-error-recovery-req-2-fix
plan: 01
subsystem: internal/session (test-only)
tags: [testing, persistence, regression-guard, PERSIST-08]
requirements: [PERSIST-08]
dependency_graph:
  requires: []
  provides:
    - "TestPersistence_ClaudeSessionIDPreservedThroughStopError — regression guard for PERSIST-08"
  affects:
    - "internal/session/session_persistence_test.go (additive, no production code)"
tech_stack:
  added: []
  patterns:
    - "Additive test function reusing Phase 1 helpers (isolatedHomeDir, newClaudeInstanceForDispatch, setupStubClaudeOnPATH, writeSyntheticJSONLTranscript, requireTmux)."
key_files:
  created: []
  modified:
    - "internal/session/session_persistence_test.go (+66 lines: one new function, no other edits)"
decisions:
  - "Test is strictly additive — not among the eight CLAUDE.md-mandated TestPersistence_* tests."
  - "Test lands FIRST (Wave 1) so the later Plan 03-03 dispatch fix is verified against an existing contract (TDD sequencing)."
  - "Step 4 is known-RED on current v1.5.1 code (instance.go:566-567 UUID mint); Plan 03-03 will turn it GREEN by routing Start() through buildClaudeResumeCommand."
metrics:
  duration_minutes: 2
  tasks_completed: 1
  files_touched: 1
  completed_date: "2026-04-14"
---

# Phase 3 Plan 1: Regression Guard for ClaudeSessionID Preservation Summary

Adds `TestPersistence_ClaudeSessionIDPreservedThroughStopError` to pin PERSIST-08: `Instance.ClaudeSessionID` must survive `StatusRunning → StatusStopped → StatusError` transitions and must remain unchanged after `Start()` on an instance that already has a populated ID.

## What Landed

- A single new test function appended to `internal/session/session_persistence_test.go`, placed after `TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion` (line 894) and before `TestPersistence_ExplicitOptOutHonoredOnLinux` (line 1025 after insertion).
- Reuses existing Phase 1 helpers without any helper modifications: `requireTmux`, `isolatedHomeDir`, `setupStubClaudeOnPATH`, `newClaudeInstanceForDispatch`, `writeSyntheticJSONLTranscript`.
- No production code touched. No change to any of the eight mandated `TestPersistence_*` tests.

## Test Result On Current Code

The new test is RED at Step 4 — exactly the outcome the plan documents as acceptable:

```
=== RUN   TestPersistence_ClaudeSessionIDPreservedThroughStopError
    session_persistence_test.go:999: PERSIST-08: Start() overwrote ClaudeSessionID.
    want "7ada7dd9-9f35-82c1-f6ec-7aaf45b32716"
    got  "aebb0719-2600-409e-b1ba-7d6e435fb2a5"
    — this is the 2026-04-14 root cause (instance.go:566-567 mint). Plan 03-03
    routes Start() through buildClaudeResumeCommand when ClaudeSessionID != "",
    which never mints a new UUID.
--- FAIL: TestPersistence_ClaudeSessionIDPreservedThroughStopError (0.32s)
```

Steps 1, 2, and 3 pass (Status-transition assignments never clear the ID). Step 4 fails because `instance.go:566-567` unconditionally mints a new UUID via `buildClaudeCommand`. This is the precise bug Plan 03-03 will fix; once that lands, Step 4 goes GREEN and the invariant is permanently gated.

## Full TestPersistence_ Suite Status

Run: `go test -run TestPersistence_ ./internal/session/... -race -count=1 -v`

| Test | Result | Notes |
|------|--------|-------|
| TestPersistence_LinuxDefaultIsUserScope | PASS | |
| TestPersistence_MacOSDefaultIsDirect | SKIP | Non-macOS host |
| TestPersistence_TmuxSurvivesLoginSessionRemoval | FAIL | Pre-existing — this worktree host lacks full systemd-run / login-session scope — unchanged by this plan |
| TestPersistence_TmuxDiesWithoutUserScope | PASS | |
| TestPersistence_FreshSessionUsesSessionIDNotResume | PASS | |
| TestPersistence_RestartResumesConversation | PASS | |
| TestPersistence_StartAfterSIGKILLResumesConversation | FAIL | Pre-existing RED — Phase 3 fix target, unchanged by this plan |
| TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion | FAIL | Pre-existing RED — Phase 3 fix target, unchanged by this plan |
| **TestPersistence_ClaudeSessionIDPreservedThroughStopError** | **FAIL (Step 4)** | **New regression guard added by this plan; documented RED per CONTEXT Decision 3** |
| TestPersistence_ExplicitOptOutHonoredOnLinux (4 subtests) | PASS | |

No previously-passing test became red because of this plan. No previously-red test became green because of this plan. The plan is purely additive.

## Deviations from Plan

None. The plan executed exactly as written. Insertion point was the one the plan specified (immediately after TEST-07, before `TestPersistence_ExplicitOptOutHonoredOnLinux`). Helper signatures matched the plan's `<interfaces>` block with zero surprises.

## Commit

- `be20eff` — `test(03-01): add TestPersistence_ClaudeSessionIDPreservedThroughStopError`
- Single file: `internal/session/session_persistence_test.go` (+66 lines)
- Signed "Committed by Ashesh Goplani"
- No Claude attribution anywhere in the commit

## Acceptance Criteria Results

- grep for `func TestPersistence_ClaudeSessionIDPreservedThroughStopError` → 1 match (line 958).
- grep count for `inst.ClaudeSessionID != originalID` → 3 matches (after Stopped, after Error, after Start).
- `go build ./internal/session/...` → exit 0.
- `go test -run TestPersistence_ClaudeSessionIDPreservedThroughStopError …` → FAIL at Step 4 with the documented 566-567 mint message (acceptable per plan — the other acceptable outcome was PASS; both documented as acceptable).
- `go test -run TestPersistence_ …` full-suite — all mandated tests' status unchanged.
- `git diff --stat` on the commit shows only `internal/session/session_persistence_test.go`.
- Commit message contains no Claude attribution.

## Self-Check: PASSED

- Test function exists at line 958 of `<repo-root>/.worktrees/session-persistence/internal/session/session_persistence_test.go` — verified by grep.
- Commit `be20eff` exists on branch `fix/session-persistence` — verified by `git log --oneline -1`.
- Build passes, target test runs with the documented failure, full suite unchanged — all verified.

## Threat Flags

None. Plan introduced a test-only, additive function reusing existing helpers under an isolated HOME. No new network surface, no new auth path, no new file-system surface beyond `t.TempDir()`.
