---
phase: 01-custom-command-injection-core-regression-tests
verified: 2026-04-15T13:00:00Z
status: passed
score: 8/8 must-haves verified
overrides_applied: 0
re_verification:
  previous_status: none
  previous_score: N/A
  gaps_closed: []
  gaps_remaining: []
  regressions: []
deferred:
  - truth: "make ci returns zero"
    addressed_in: "Follow-up plan (tmux-env test harness fix)"
    evidence: "deferred-items.md records 6 pre-existing tmux-environment test failures (TestSyncSessionIDsFromTmux_Claude/AllTools/OverwriteWithNew, TestInstance_GetSessionIDFromTmux, TestInstance_UpdateClaudeSession_TmuxFirst/RejectZombie) that reproduce at parent commit 4730aa5 before Phase 1 changes. Accepted as known deviation per phase prompt."
---

# Phase 01: Custom-command injection + core regression tests — Verification Report

**Phase Goal:** Lock the two core behaviors needed for the v1.5.4 conductor use case under regression tests, and close the one gap PR #578 left open — `CLAUDE_CONFIG_DIR` reaching the tmux spawn environment for custom-command (wrapper-script) claude sessions.

**Verified:** 2026-04-15T13:00:00Z
**Status:** passed
**Re-verification:** No — initial verification

## Goal Achievement

### Must-Haves Verification Table

| #   | Must-Have                                                                                                                                 | Status    | Evidence                                                                                                                                                                                                                                                 |
| --- | ----------------------------------------------------------------------------------------------------------------------------------------- | --------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | `pergroupconfig_test.go` exists with exactly 4 `TestPerGroupConfig_*` functions                                                           | ✓ PASSED  | `grep -c "^func TestPerGroupConfig_"` returned **4**. Names match exactly: `CustomCommandGetsGroupConfigDir`, `GroupOverrideBeatsProfile`, `UnknownGroupFallsThroughToProfile`, `CacheInvalidation`.                                                       |
| 2   | `go test ./internal/session/ -run TestPerGroupConfig_ -race -count=1` exits 0                                                             | ✓ PASSED  | Executed in verification run: `ok github.com/asheshgoplani/agent-deck/internal/session 1.046s`. All 4 tests GREEN under race detector.                                                                                                                    |
| 3   | PR #578 regression tests remain GREEN (`GroupWins`, `IsClaudeConfigDirExplicitForGroup`, `BuildClaudeCommand_CustomAlias`)                | ✓ PASSED  | All 3 PASS in verification run. No assertion changes; no regressions.                                                                                                                                                                                   |
| 4   | Surgical patch applied correctly at `instance.go` (`return i.buildBashExportPrefix() + baseCommand`; contains `REQ CFG-02`)                 | ✓ PASSED  | Line 598: `return i.buildBashExportPrefix() + baseCommand`. Line 597 comment: `... in the spawn env before exec'ing the wrapper. (REQ CFG-02)`. Net diff: +4/-2 lines.                                                                                   |
| 5   | RED→GREEN commit ordering in git history (test commit before fix commit)                                                                   | ✓ PASSED  | `git log 3e402e2..HEAD`: order (newest→oldest) is `1c5c81e docs` → `b39bbf3 fix(session)` → `40f4f04 test(session)` → `4730aa5 plan`. Chronologically, `40f4f04` (RED test) lands BEFORE `b39bbf3` (GREEN fix). TDD discipline auditable.                       |
| 6   | Commit trailer compliance: ≥2 `Committed by Ashesh Goplani`, 0 Claude attributions, ≥1 `Builds on PR #578 by @alec-pinson.`                | ✓ PASSED  | `Committed by Ashesh Goplani`: **4** (≥2 ✓). Claude attributions (`🤖`, `Co-Authored-By: Claude`, `Generated with Claude`): **0** (✓). `Builds on PR #578 by @alec-pinson`: **2** (≥1 ✓).                                                                 |
| 7   | Scope discipline — only declared files modified                                                                                            | ✓ PASSED  | `git diff --name-only 3e402e2..HEAD` lists: `internal/session/instance.go`, `internal/session/pergroupconfig_test.go` (the only two code files), plus `.planning/*` artifacts (REQUIREMENTS, ROADMAP, STATE, 01-01-PLAN, 01-01-SUMMARY, 01-RESEARCH, deferred-items). Zero code files outside scope. |
| 8   | No publishing actions (push, tag, gh pr, merge) in reflog                                                                                  | ✓ PASSED  | `git reflog --since="2 hours ago" \| grep -cE "\bpush\b\|\btag\b\|gh pr\|\bmerge\b"` returned **0**.                                                                                                                                                      |

**Score:** 8/8 must-haves verified

### Deferred Items

| # | Item | Addressed In | Evidence |
|---|------|-------------|----------|
| 1 | `make ci` exits zero | Follow-up plan (separate tmux-env investigation) | 6 pre-existing `internal/session` tmux-env test failures (`TestSyncSessionIDsFromTmux_*`, `TestInstance_GetSessionIDFromTmux`, `TestInstance_UpdateClaudeSession_*`) logged with reproduction proof at parent `4730aa5`. Explicit known deviation per phase prompt. Not a Phase-01 regression. |

### Requirements Coverage (Traceability)

| Requirement | Source Plan | Description                                                                 | Status        | Evidence                                                                                                                                                                                                                     |
| ----------- | ----------- | --------------------------------------------------------------------------- | ------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| CFG-01      | 01-01-PLAN  | PR #578 config schema + lookup priority remains correct                     | ✓ SATISFIED   | PR #578 tests still GREEN (no assertion changes); `GroupWins`, `IsClaudeConfigDirExplicitForGroup` PASS. REQUIREMENTS.md traceability table marks CFG-01 Complete (01-01).                                                  |
| CFG-02      | 01-01-PLAN  | Custom-command sessions honor per-group `config_dir` via spawn env          | ✓ SATISFIED   | `instance.go:598` prepends `buildBashExportPrefix()` (which emits `export CLAUDE_CONFIG_DIR=...`) to custom-command return path. Locked by `TestPerGroupConfig_CustomCommandGetsGroupConfigDir` + `GroupOverrideBeatsProfile`. |
| CFG-04 (tests 1, 2, 3, 6) | 01-01-PLAN | Regression tests for custom-command / group-beats-profile / unknown-group / cache invalidation | ✓ SATISFIED   | Four named tests exist in `pergroupconfig_test.go`. All GREEN under `-race -count=1`. Traceability table marks CFG-04 (tests 1, 2, 3, 6) Complete (01-01); remaining tests 4 and 5 mapped to Phase 2.        |

No orphaned requirements — all Phase 1 REQ IDs (CFG-01, CFG-02, CFG-04 subset) mapped correctly in `.planning/REQUIREMENTS.md`.

### Scope Check

Files modified since base `3e402e2`:

- `internal/session/instance.go` (+4/-2) — permitted by plan scope
- `internal/session/pergroupconfig_test.go` (NEW, 235 lines) — permitted by plan scope
- `.planning/*` artifacts (REQUIREMENTS, ROADMAP, STATE, plan docs) — expected planning-document churn

No production Go files outside `internal/session/` touched. No refactors of PR #578's code. Clean scope.

### RED / GREEN Audit

Git history shows canonical TDD ordering:

1. `40f4f04` — `test(session): add per-group Claude config regression tests (CFG-04 tests 1/2/3/6)` — test-only commit (1 file, `pergroupconfig_test.go`).
2. `b39bbf3` — `fix(session): export CLAUDE_CONFIG_DIR for custom-command sessions (CFG-02)` — implementation commit (1 file, `instance.go`). Carries `Builds on PR #578 by @alec-pinson.` attribution.

RED preserved in history (tests 1 and 2 would fail against base because `instance.go:592` previously returned `baseCommand` unchanged; `/tmp/pergroupconfig-red.log` captured the 2 FAIL / 2 PASS split). GREEN verified by running on HEAD: all 4 pass with race detector.

### Anti-Patterns

No TODO/FIXME/PLACEHOLDER markers introduced by this phase's changes. Single comment in `instance.go` cites `REQ CFG-02` (intentional traceability marker, not a stub). Test file uses stdlib only (`os`, `path/filepath`, `strings`, `testing`) — no testify, no network, no hardcoded absolute paths outside `t.TempDir()`.

### Behavioral Spot-Checks

| Behavior                                                | Command                                                                                               | Result                   | Status   |
| ------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- | ------------------------ | -------- |
| All 4 Phase-1 regression tests green under race         | `go test ./internal/session/ -run TestPerGroupConfig_ -race -count=1`                                 | `ok ... 1.046s`          | ✓ PASS   |
| PR #578 tests remain green (no regression)              | `go test ./internal/session/ -run "TestGetClaudeConfigDirForGroup_GroupWins\|...CustomAlias" -race`  | 3/3 PASS                 | ✓ PASS   |
| Surgical patch present in source                        | `grep "return i.buildBashExportPrefix() + baseCommand" internal/session/instance.go`                  | Line 598 match           | ✓ PASS   |
| `REQ CFG-02` traceability marker in source              | `grep "REQ CFG-02" internal/session/instance.go`                                                      | Line 597 match           | ✓ PASS   |

### Human Verification Required

None. Phase 01 is pure Go test authoring + single-line production patch — fully automatable verification. Visual harness (CFG-05) and conductor-host manual proof are deferred to Phase 3.

### Gaps Summary

None. All 8 must-haves verified; all 3 assigned requirements (CFG-01, CFG-02, CFG-04 subset) satisfied; RED→GREEN TDD discipline auditable in git history; commit attribution compliant; scope clean; no publishing side effects. The one known deviation (`make ci` non-zero from 6 pre-existing tmux-env failures) is explicitly accepted per the phase prompt and documented in `deferred-items.md` with reproduction proof at parent commit.

Phase 01 goal achieved: the tmux spawn env for custom-command (wrapper-script) sessions now carries `CLAUDE_CONFIG_DIR` from per-group overrides, and the behavior is locked under four independently-runnable regression tests.

---

_Verified: 2026-04-15T13:00:00Z_
_Verifier: Claude (gsd-verifier)_
