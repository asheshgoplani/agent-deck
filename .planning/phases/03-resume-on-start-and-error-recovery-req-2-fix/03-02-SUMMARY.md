---
phase: 03-resume-on-start-and-error-recovery-req-2-fix
plan: 02
subsystem: internal/session (test-only)
tags: [testing, persistence, red-test, dispatch, PERSIST-07, PERSIST-09]
requirements: [PERSIST-07, PERSIST-09]
dependency_graph:
  requires:
    - "03-01 (TestPersistence_ClaudeSessionIDPreservedThroughStopError landed — adjacent guard)"
  provides:
    - "TestPersistence_SessionIDFallbackWhenJSONLMissing — RED test pinning CONTEXT Decision 5 divergence-prevention contract"
  affects:
    - "internal/session/session_persistence_test.go (additive, no production code)"
tech_stack:
  added: []
  patterns:
    - "Additive RED test reusing Phase 1 helpers (isolatedHomeDir, newClaudeInstanceForDispatch, setupStubClaudeOnPATH, readCapturedClaudeArgv, requireTmux)."
    - "Absence-of-JSONL contract: new test explicitly does NOT call writeSyntheticJSONLTranscript and asserts ENOENT on the stored-ID jsonl path before Start()."
key_files:
  created: []
  modified:
    - "internal/session/session_persistence_test.go (+66 lines: one new function, no other edits)"
decisions:
  - "Test is strictly additive — not one of the eight CLAUDE.md-mandated TestPersistence_* tests. Lands as a locked regression guard for CONTEXT Decision 5."
  - "Test lands in Wave 2 (03-02) so Plan 03-03's dispatch fix can turn its Assertion A red→green as part of a single atomic feat commit."
  - "Stored ID `deadbeef-fake-uuid-0000-000000000001` chosen as an unambiguous, valid-shaped UUID sentinel so failure messages point directly at the 2026-04-14 divergence (f1e103df → b9403638) root cause."
metrics:
  duration_minutes: 3
  tasks_completed: 1
  files_touched: 1
  completed_date: "2026-04-14"
---

# Phase 3 Plan 2: TestPersistence_SessionIDFallbackWhenJSONLMissing (RED) Summary

Lands a single RED test pinning CONTEXT Decision 5: when `Instance.ClaudeSessionID` is populated but NO JSONL transcript exists under `~/.claude/projects/<hash>/`, `inst.Start()` MUST produce `claude --session-id <stored-id>` — NEVER `--resume` and NEVER a freshly minted UUID. Plan 03-03's dispatch fix turns this test GREEN.

## What Landed

- One new test function `TestPersistence_SessionIDFallbackWhenJSONLMissing` appended to `internal/session/session_persistence_test.go`, placed immediately after `TestPersistence_ClaudeSessionIDPreservedThroughStopError` (Plan 03-01) and before `TestPersistence_ExplicitOptOutHonoredOnLinux`.
- Reuses Phase 1 helpers without modification: `requireTmux`, `isolatedHomeDir`, `setupStubClaudeOnPATH`, `newClaudeInstanceForDispatch`, `readCapturedClaudeArgv`, `ConvertToClaudeDirName`.
- Pins the three-part contract from CONTEXT Decision 5:
  - **Assertion A:** captured `argv` MUST contain the stored `deadbeef-fake-uuid-0000-000000000001` — evidence that `Start()` did NOT mint a fresh UUID.
  - **Assertion B:** captured `argv` MUST contain `--session-id <stored-id>` and MUST NOT contain `--resume` — the no-JSONL fallback emits `--session-id`, not `--resume`, which would cause Claude "No conversation found" errors.
  - **Assertion C:** `inst.ClaudeSessionID` MUST still equal the stored value after `Start()` returns — no overwrite of the struct field.
- No production code touched. No modification to any of the eight mandated `TestPersistence_*` tests. No modification to Plan 03-01's `TestPersistence_ClaudeSessionIDPreservedThroughStopError`.

## Test Result On Current Code

The new test is RED at Assertion A — exactly the contract the plan documents:

```
=== RUN   TestPersistence_SessionIDFallbackWhenJSONLMissing
    session_persistence_test.go:1051: SessionIDFallback RED: captured argv does
    not contain stored ClaudeSessionID "deadbeef-fake-uuid-0000-000000000001" —
    the stored ID was discarded / overwritten. Root cause: instance.go:566-567
    mints a fresh UUID. Argv: [--session-id 948a4238-ef65-44d4-8fd1-78e45ca5fd53
    --dangerously-skip-permissions]
--- FAIL: TestPersistence_SessionIDFallbackWhenJSONLMissing (0.23s)
```

**Observation:** The captured argv correctly shows that current code minted a fresh UUID (`948a4238-ef65-44d4-8fd1-78e45ca5fd53`) and dispatched through `--session-id <fresh-UUID>` — the literal 2026-04-14 divergence. The `--dangerously-skip-permissions` trailing flag is the existing `buildClaudeCommand()` surface. Assertion B (which checks for `--session-id <deadbeef>`) would also fire if Assertion A were removed; Assertion C (in-struct overwrite) would also fire. All three fire against current code; Plan 03-03 turns all three GREEN by routing `Start()` through `buildClaudeResumeCommand()` when `ClaudeSessionID != ""`.

Host: Linux with tmux available. `requireTmux(t)` passed — the test was genuinely exercised (not skipped).

## Full TestPersistence_ Suite Status

Run: `go test -run TestPersistence_ ./internal/session/... -race -count=1 -v`

| Test | Result | Notes |
|------|--------|-------|
| TestPersistence_LinuxDefaultIsUserScope | PASS | Unchanged |
| TestPersistence_MacOSDefaultIsDirect | SKIP | Non-macOS host (unchanged) |
| TestPersistence_TmuxSurvivesLoginSessionRemoval | FAIL | Pre-existing environmental; worktree host lacks full systemd-run / login-session scope — unchanged by this plan |
| TestPersistence_TmuxDiesWithoutUserScope | PASS | Unchanged |
| TestPersistence_FreshSessionUsesSessionIDNotResume | PASS | Unchanged |
| TestPersistence_RestartResumesConversation | PASS | Unchanged |
| TestPersistence_StartAfterSIGKILLResumesConversation | FAIL | Pre-existing RED — Plan 03-03 fix target, unchanged by this plan |
| TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion | FAIL | Pre-existing RED — Plan 03-03 fix target, unchanged by this plan |
| TestPersistence_ClaudeSessionIDPreservedThroughStopError | FAIL (Step 4) | Plan 03-01 regression guard — Plan 03-03 fix target, unchanged by this plan |
| **TestPersistence_SessionIDFallbackWhenJSONLMissing** | **FAIL (Assertion A)** | **NEW — RED by design per CONTEXT Decision 5; Plan 03-03 turns it GREEN** |
| TestPersistence_ExplicitOptOutHonoredOnLinux (4 subtests) | PASS | Unchanged |

No previously-passing test became red because of this plan. No previously-red test became green because of this plan. Purely additive.

## Deviations from Plan

**Plan acceptance criterion 2 (literal-count) interpretation:** The plan said "grep -c 'deadbeef-fake-uuid-0000-000000000001' ... returns at least 5 (storedID is referenced in setup + 3 assertions + error messages)." The test body provided verbatim in the plan declares a local `storedID` variable and then references it 9 times (setup, 3 assertions, 4 error-message `%q` substitutions via `storedID`, and the `jsonlPath` construction). The literal string `deadbeef-fake-uuid-0000-000000000001` therefore appears exactly once (the declaration) — all subsequent uses go through the `storedID` identifier. The semantic intent (ID referenced many places, unambiguous in failure messages) is satisfied; the literal-count grep simply measures a different thing than was likely intended. **No code change made** — the plan's test body was used verbatim. This is a documentation mismatch, not a substance deviation.

Otherwise the plan executed exactly as written. Insertion point was the one the plan specified. Helper signatures matched the plan's `<interfaces>` block with zero surprises.

## Commit

- `dc7388f` — `test(03-02): add TestPersistence_SessionIDFallbackWhenJSONLMissing (RED)`
- Single file: `internal/session/session_persistence_test.go` (+66 lines)
- Signed "Committed by Ashesh Goplani"
- No Claude attribution anywhere in the commit
- Committed with `--no-verify` per phase-orchestrator directive

## Acceptance Criteria Results

- `grep -n 'func TestPersistence_SessionIDFallbackWhenJSONLMissing' internal/session/session_persistence_test.go` → 1 match. ✓
- `grep -c 'deadbeef-fake-uuid-0000-000000000001'` → 1 literal; `grep -c 'storedID'` → 9 semantic references (see Deviations note above).
- `grep -n 'writeSyntheticJSONLTranscript'` shows the new test does NOT call the helper — only a comment at line 1033 mentions it ("Explicitly DO NOT call writeSyntheticJSONLTranscript"). ✓
- `go build ./internal/session/...` → exit 0. ✓
- `go test -run TestPersistence_SessionIDFallbackWhenJSONLMissing ./internal/session/... -race -count=1` → exit non-zero (FAIL at Assertion A) on a tmux-available host. ✓ — this is the RED state Plan 03-03 will make GREEN.
- `go test -run TestPersistence_ …` full-suite — all mandated tests' status unchanged; Plan 03-01's RED guard unchanged; new RED test is RED at Assertion A. ✓
- `git diff --stat HEAD~1 HEAD` shows only `internal/session/session_persistence_test.go` changed. ✓ (+66 lines, nothing else)
- Commit message contains no Claude attribution. ✓

## Self-Check: PASSED

- Test function exists at line ~1003 of `<repo-root>/.worktrees/session-persistence/internal/session/session_persistence_test.go` — verified by grep (`func TestPersistence_SessionIDFallbackWhenJSONLMissing` → 1 match).
- Commit `dc7388f` exists on branch `fix/session-persistence` — verified by `git log --oneline -2`.
- Build passes, target test runs with the documented RED failure mode, full suite otherwise unchanged — all verified.

## Threat Flags

None. Plan introduced a test-only, additive function reusing existing helpers under an isolated HOME. No new network surface, no new auth path, no new file-system surface beyond `t.TempDir()`. Threat model T-03-02-01 (config.toml under isolated HOME) and T-03-02-02 (3s polling deadline) accepted as designed.
