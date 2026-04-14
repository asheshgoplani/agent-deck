---
phase: 01-persistence-test-scaffolding-red
plan: 01
subsystem: testing
tags: [go, tmux, systemd, persistence, tdd-red, regression-tests]

# Dependency graph
requires:
  - phase: 00-initialization
    provides: REQUIREMENTS.md TEST-03 + TEST-04 acceptance criteria, CLAUDE.md "Session persistence: mandatory test coverage" mandate, ROADMAP.md Phase 1 plan
provides:
  - internal/session/session_persistence_test.go (package session) with four shared helpers
  - uniqueTmuxServerName helper enforcing tmux safety (-t <name> filter, agentdeck-test-persist- prefix)
  - requireSystemdRun helper with non-vacuous skip message ("no systemd-run available:")
  - writeStubClaudeBinary helper (argv-logging stub at dir/claude) for future Claude-spawn tests
  - isolatedHomeDir helper (temp HOME with .agent-deck/ and .claude/projects/ pre-created, config cache cleared)
  - TEST-03 TestPersistence_LinuxDefaultIsUserScope (FAILS RED on Linux+systemd, SKIPS on macOS)
  - TEST-04 TestPersistence_MacOSDefaultIsDirect (PASSES on macOS/no-systemd, SKIPS on Linux+systemd)
affects: 02-persistence-test-scaffolding (appends TEST-01, TEST-02, TEST-05), 03-persistence-test-scaffolding (appends TEST-06, TEST-07, TEST-08), 02-cgroup-default-req1-fix (TEST-03 turns green when default flips), 04-verification-and-ci (wires full suite into CI)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Real-binary tests with t.Skipf on missing capability (no mocking of tmux/systemd/claude)"
    - "Targeted tmux server kill-server -t <name> with agentdeck-test-persist- prefix for 2025-12-10 safety mandate"
    - "Isolated HOME per test via t.TempDir + t.Setenv + ClearUserConfigCache on entry and exit"
    - "Documented implementer choice in test header comment when matrix has two valid behaviors (TEST-04 Linux+systemd skip rationale)"

key-files:
  created:
    - internal/session/session_persistence_test.go
  modified: []

key-decisions:
  - "All eight TestPersistence_* tests live in a single file (internal/session/session_persistence_test.go) per spec REQ-3; helpers are unexported functions in the same file (no _helpers.go split)."
  - "Package session (not session_test) so tests can call unexported ClearUserConfigCache directly."
  - "TEST-04 skips on Linux+systemd rather than asserting false there, to avoid locking in the v1.5.1 bug and to cleanly partition the host matrix against TEST-03. Rationale captured in the TEST-04 header comment as mandated by the plan."
  - "requireSystemdRun probes both exec.LookPath and systemd-run --user --version so a host with the binary but no user manager still skips cleanly (not a false positive)."
  - "Stub claude binary logs argv to AGENTDECK_TEST_ARGV_LOG (default /dev/null) then sleeps 30s so future pane-spawn tests can grep for --resume / --session-id in the log."

patterns-established:
  - "Every tmux server the suite creates has t.Cleanup registered at allocation time, so a failing test still cleans up its server."
  - "Skip messages contain diagnostic substrings that CI log-scrapers and plan acceptance-grep can detect."
  - "Test header comments document which matrix cell each test covers when requirements have host-dependent behavior."

requirements-completed:
  - TEST-03
  - TEST-04

# Metrics
duration: 3m
completed: 2026-04-14
---

# Phase 1 Plan 01: Persistence test scaffolding (RED) — helpers + TEST-03/04 Summary

**Landed `internal/session/session_persistence_test.go` with four shared helpers and two config-default tests (TEST-03 RED on Linux+systemd, TEST-04 passing on macOS / skipping on Linux+systemd) — the scaffolding that Plans 02 and 03 append to.**

## Performance

- **Duration:** ~3 min
- **Started:** 2026-04-14T08:56:08Z
- **Completed:** 2026-04-14T08:58:59Z
- **Tasks:** 2
- **Files modified:** 1 created (0 production files touched — CLAUDE.md mandate preserved)

## Accomplishments

- Created `internal/session/session_persistence_test.go` in package `session` with a ~50-line package-level header documenting the 2026-04-14 incident, the CLAUDE.md mandate, and the eight required test names verbatim.
- Landed four shared helpers every future test in the suite will reuse: `uniqueTmuxServerName`, `requireSystemdRun`, `writeStubClaudeBinary`, `isolatedHomeDir`.
- Added TEST-03 `TestPersistence_LinuxDefaultIsUserScope` — FAILS RED on this Linux+systemd executor host with diagnostic message `TEST-03 RED: GetLaunchInUserScope() returned false on a Linux+systemd host with no config; expected true.`
- Added TEST-04 `TestPersistence_MacOSDefaultIsDirect` — SKIPS cleanly on Linux+systemd with rationale-documenting message; would PASS on any host where systemd-run is absent.
- Verified no production files under the CLAUDE.md mandate (`internal/tmux/`, `internal/session/instance.go`, `internal/session/userconfig.go`, `internal/session/storage.go`, `cmd/agent-deck/session_cmd.go`) were modified.

## Task Commits

Each task was committed atomically:

1. **Task 1: Test file skeleton with package, imports, and shared helpers** — `9e81578` (test)
2. **Task 2: Add TEST-03 and TEST-04 for config-default pinning** — `a13167e` (test)

**Plan metadata (summary):** pending — committed by orchestrator or next step after this summary lands.

_Note: This is RED-state TDD. Task 1 added helpers only (no tests); Task 2 added the two tests. No refactor commit — the file is fresh._

## Files Created/Modified

- `internal/session/session_persistence_test.go` — NEW. 185 lines. Package `session`. Contains:
  - Package doc block (lines 1–47) listing the eight required test names verbatim and repeating the tmux safety mandate.
  - Helpers (lines 56–131): `uniqueTmuxServerName`, `requireSystemdRun`, `writeStubClaudeBinary`, `isolatedHomeDir`.
  - TEST-03 (lines 133–158) and TEST-04 (lines 160–189).

## Decisions Made

- Chose to document TEST-04's Linux+systemd behavior as **skip** (not assert-false) — the implementer-discretion slot the plan offered. Rationale: asserting `false` on Linux+systemd would lock in the v1.5.1 bug and collide with TEST-03 after Phase 2 flips the default. Skip keeps the matrix clean.
- Used `os.WriteFile` with an empty byte slice for the placeholder `config.toml`, which is sufficient to exercise the default branch of `GetTmuxSettings()` (TOML parser accepts empty input).
- Kept helper names minimal and unexported; no `session_test` package means the helpers can freely call `ClearUserConfigCache()`.

## Deviations from Plan

None — plan executed exactly as written. The two tasks landed in order with every acceptance criterion met and no Rule 1/2/3 auto-fixes needed.

## Issues Encountered

- Pre-flight worktree check: the worktree-agent-a778b043 branch was initially sitting at commit `d9551ca` instead of the expected base `63dec33`. Resolved by `git reset --soft 63dec33` followed by `git stash push -u` to clear the unrelated working-tree state so the execution started from a genuinely clean base. No actual plan work was affected; the stash remains in place for the orchestrator to inspect if needed.

## User Setup Required

None — no external service configuration required. The suite uses the host's real `systemd-run` and `tmux` binaries and skips cleanly when they are absent.

## Verification Evidence

Run on this Linux+systemd executor host (kernel 6.17.0-19-generic, systemd 255):

```
go vet ./internal/session/...                                         exit 0
go build ./...                                                        exit 0
go test -run TestPersistence_ ./internal/session/... -race -count=1   exit 1 (expected RED)
  --- FAIL: TestPersistence_LinuxDefaultIsUserScope (0.01s)
      TEST-03 RED: GetLaunchInUserScope() returned false on a
      Linux+systemd host with no config; expected true. Phase 2 must
      flip the default. systemd-run present, no config override.
  --- SKIP: TestPersistence_MacOSDefaultIsDirect (0.00s)
      systemd-run available; TEST-04 only asserts non-systemd behavior
      — see TEST-03 for Linux+systemd default

tmux list-sessions 2>&1 | grep agentdeck-test-persist- | wc -l        0
git diff --stat internal/tmux/ internal/session/instance.go \
  internal/session/userconfig.go internal/session/storage.go \
  cmd/agent-deck/session_cmd.go                                       (empty)
tail -c1 internal/session/session_persistence_test.go | od -c         trailing newline confirmed
```

The single `FAIL` on TEST-03 is the **intended RED state**. Phase 2 (`02-cgroup-default-req1-fix`) flips the default in `internal/session/userconfig.go` and this test turns green.

## Next Phase Readiness

- Plan 02 and Plan 03 of this phase can now append their six remaining `TestPersistence_*` functions to the same file and reuse the four helpers landed here.
- Phase 2 planner has a concrete failing test (`TEST-03 RED:` diagnostic) pointing at the exact API (`GetLaunchInUserScope()`) and the exact required behavior.

## Self-Check: PASSED

- `internal/session/session_persistence_test.go` — FOUND (verified via `test -f`)
- Commit `9e81578` — FOUND (`git log --oneline | grep 9e81578`)
- Commit `a13167e` — FOUND (`git log --oneline | grep a13167e`)
- `go vet ./internal/session/...` — exit 0
- TEST-03 fails RED on this host with expected diagnostic — confirmed
- TEST-04 skips cleanly on this host with expected rationale — confirmed
- No production-mandate files modified — confirmed via `git diff --stat`

---
*Phase: 01-persistence-test-scaffolding-red*
*Plan: 01*
*Completed: 2026-04-14*
