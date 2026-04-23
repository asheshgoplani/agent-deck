---
phase: 01-persistence-test-scaffolding-red
plan: 03
subsystem: testing
tags: [go, tmux, claude, dispatch, resume, tdd-red, req-2, regression-tests]

# Dependency graph
requires:
  - phase: 01-persistence-test-scaffolding-red
    plan: 02
    provides: session_persistence_test.go with 4 tests + helpers (isolatedHomeDir, writeStubClaudeBinary, uniqueTmuxServerName, requireSystemdRun, pidAlive, randomHex8, pidCgroup, startFakeLoginScope, startAgentDeckTmuxInUserScope, startTmuxInsideFakeLogin)
provides:
  - internal/session/session_persistence_test.go appended with TEST-05, TEST-06, TEST-07, TEST-08 (all 8 mandated TestPersistence_* tests now present)
  - readCapturedClaudeArgv helper (polls stub claude argv log with timeout)
  - newClaudeInstanceForDispatch helper (Instance with deterministic ID, uuid-shaped ClaudeSessionID, safe tmux cleanup)
  - setupStubClaudeOnPATH helper (absolute-path stub via [claude] command config + CLAUDE_CONFIG_DIR unset + AGENTDECK_TEST_ARGV_LOG forwarding)
  - writeSyntheticJSONLTranscript helper (writes 2-line transcript with "sessionId" field so sessionHasConversationData returns true)
  - requireTmux helper (skip if tmux missing)
  - TEST-05 TestPersistence_RestartResumesConversation — PASSES as REQ-2 regression guard for Restart() at instance.go:3789
  - TEST-06 TestPersistence_StartAfterSIGKILLResumesConversation — FAILS RED; core REQ-2 proof that Start() bypasses buildClaudeResumeCommand
  - TEST-07 TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion — FAILS RED; pins docs/session-id-lifecycle.md invariant (same Start() root cause)
  - TEST-08 TestPersistence_FreshSessionUsesSessionIDNotResume — PASSES as regression guard for sessionHasConversationData() branch at instance.go:4150
affects: 02-cgroup-default-req1-fix (TEST-01/03 turn green when launch_in_user_scope flips), 03-resume-on-start-error-recovery-req2-fix (TEST-06/07 turn green when Start() routes through buildClaudeResumeCommand), 04-verification-and-ci (wires full 8-test suite + verify-session-persistence.sh into CI)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Absolute-path stub dispatch: tmux new-session on the default socket does NOT propagate client-process PATH to pane initial processes (server captures env at server startup). Fix: write [claude] command = <abs stub> to isolated config.toml so GetClaudeCommand() returns the absolute path, bypassing PATH search entirely."
    - "CLAUDE_CONFIG_DIR env-var poisoning: GetClaudeConfigDir() short-circuits to the env var, which is pre-set on the executor host. Tests that use sessionHasConversationData() must t.Setenv(\"CLAUDE_CONFIG_DIR\", \"\") so the default ~/.claude path resolves under the isolated HOME."
    - "Stub argv capture: stub claude from writeStubClaudeBinary appends its argv (one token per line) to $AGENTDECK_TEST_ARGV_LOG, then sleeps 30s to keep the pane alive for inspection. readCapturedClaudeArgv polls the log until non-empty."
    - "Post-Start JSONL write: Start() at instance.go:566-567 overwrites ClaudeSessionID with a freshly-minted UUID via generateUUID(). For TEST-05 to meaningfully exercise Restart's resume path, the synthetic JSONL must be written AFTER Start() (under the post-Start ClaudeSessionID), not before."
    - "Real-binary no-mocking dispatch tests: tmux and shell are real binaries; only `claude` is a stub (explicitly carved out by CONTEXT.md because tests assert on the spawned command line, not Claude's behavior)."

key-files:
  created: []
  modified:
    - internal/session/session_persistence_test.go  # +414 lines across 2 commits (1 new import: encoding/json)

key-decisions:
  - "Dispatch via absolute-path [claude] command config instead of PATH prepending. Rationale: empirical verification showed tmux new-session on the default socket inherits the server's startup env, not the client's. PATH override via t.Setenv is invisible to the pane's initial process. Setting [claude] command = <abs path> in an isolated config.toml makes GetClaudeCommand() return the absolute path at dispatch time, which is embedded directly in the shell command and requires no PATH lookup."
  - "Unset CLAUDE_CONFIG_DIR inside setupStubClaudeOnPATH (not inside isolatedHomeDir). Rationale: isolatedHomeDir was landed in Plan 01 and the plan forbids modifying prior helpers. The CLAUDE_CONFIG_DIR unset is dispatch-test-specific (only resume tests use sessionHasConversationData), so layering it in the Plan 03 helper keeps the Plan 01 helper untouched while fixing the real bug."
  - "TEST-05 writes synthetic JSONL AFTER Start() rather than before. Rationale: Start() mutates ClaudeSessionID via generateUUID() in the capture-resume pattern (instance.go:566-567); JSONL written before Start points at a stale UUID by the time Restart consults sessionHasConversationData(). Writing after Start mirrors the 2026-04-14 production scenario (Claude writes its transcript during normal use, tmux later dies, user restarts)."
  - "TEST-05 kept as PASSING regression guard. The dispatch_path_analysis in 03-PLAN.md predicted TEST-05 would pass on v1.5.1 if Restart's respawn-pane branch was correctly wired; after fixing the test-order bug above, that prediction holds. Any future regression that breaks Restart's resume routing will flip this test to FAIL RED with the unambiguous 'TEST-05 RED:' diagnostic."

patterns-established:
  - "Dispatch-test setup invariant: every dispatch test calls requireTmux(t) → isolatedHomeDir(t) → setupStubClaudeOnPATH(t, home) → newClaudeInstanceForDispatch(t, home) in that order. Tests that need a resumable transcript then call writeSyntheticJSONLTranscript(t, home, inst) — BEFORE Start for TEST-06/07 (the stored ClaudeSessionID is used directly because Start's bypass doesn't resume anyway), AFTER Start for TEST-05 (Restart reads the POST-Start-mutated ClaudeSessionID)."
  - "Failure messages on RED-state tests always include the three things a reviewer needs: the contract violated ('claude argv must contain --resume <id>'), the observed argv (full []string), and a pointer to the exact production-code line that must change ('instance.go:1883 instead of buildClaudeResumeCommand')."
  - "Tmux cleanup via (*tmux.Session).Kill() (Name-scoped, SAFE per internal/tmux/tmux.go Kill implementation). Never bare `tmux kill-server`. Verified: grep-count of `kill-server` in the test file shows every invocation is either `-t <name>` or `-L <socket>` scoped."

requirements-completed:
  - TEST-05
  - TEST-06
  - TEST-07
  - TEST-08

# Metrics
duration: ~14m
completed: 2026-04-14
---

# Phase 1 Plan 03: Persistence test scaffolding (RED) — TEST-05 through TEST-08 Summary

**Appended the four resume-routing tests (TEST-05, 06, 07, 08) to `internal/session/session_persistence_test.go`, completing the eight-test mandated suite. TEST-06 and TEST-07 fail RED with captured argv evidence that `Start()` at `instance.go:1883` spawns `claude --session-id <existing-id>` instead of `claude --resume <existing-id>` — the exact 2026-04-14 incident REQ-2 root cause, now permanently test-gated.**

## Performance

- **Duration:** ~14 min (two root-cause-driven Rule 3 blocking fixes: tmux env propagation via absolute-path config; CLAUDE_CONFIG_DIR env-var poisoning unset)
- **Started:** 2026-04-14T11:19Z (approx)
- **Completed:** 2026-04-14T11:33Z (approx)
- **Tasks:** 2 (1 helpers + TEST-08, 2 TEST-05/06/07)
- **Files modified:** 1 (internal/session/session_persistence_test.go) — 0 production files touched (CLAUDE.md mandate preserved)

## Accomplishments

- Added 5 new unexported helpers: `readCapturedClaudeArgv`, `newClaudeInstanceForDispatch`, `setupStubClaudeOnPATH`, `writeSyntheticJSONLTranscript`, `requireTmux`.
- Added TEST-05 `TestPersistence_RestartResumesConversation` — exercises the real Restart() dispatch path (instance.go:3763) with stub claude argv capture. PASSES as regression guard for `buildClaudeResumeCommand()` routing at instance.go:3789.
- Added TEST-06 `TestPersistence_StartAfterSIGKILLResumesConversation` — THE core REQ-2 RED test. Drives the real Start() dispatch (instance.go:1873) on an Instance with Status=StatusError and a populated ClaudeSessionID. Captured argv proves Start() does NOT resume — it spawns `--session-id <existing-id>` via the capture-resume pattern at instance.go:550+. This is the 2026-04-14 incident's technical root cause for REQ-2.
- Added TEST-07 `TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion` — writes and deletes the hook sidecar at `~/.agent-deck/hooks/<id>.sid`, then drives Start(). Fails RED via the same Start() bypass as TEST-06. Independently pins the `docs/session-id-lifecycle.md` invariant that instance JSON is authoritative for ClaudeSessionID.
- Added TEST-08 `TestPersistence_FreshSessionUsesSessionIDNotResume` — pure-Go assertion on `buildClaudeResumeCommand()` with no JSONL transcript. PASSES as regression guard for the `sessionHasConversationData() == false` branch at instance.go:4150-4177.
- All 8 mandated `TestPersistence_*` tests now exist with verbatim names; full suite runs cleanly (no stray tmux sessions, no production files touched).

## Task Commits

Each task was committed atomically with "Committed by Ashesh Goplani" sign-off; no Claude attribution:

1. **Task 1: Helpers + TEST-08 (regression guard)** — `ccbd4d3` (test)
2. **Task 2: TEST-05, TEST-06, TEST-07 via real dispatch path (RED)** — `e1c9333` (test)

Plan metadata commit: pending (orchestrator will make the final doc commit with SUMMARY.md, STATE.md, ROADMAP.md).

## Files Created/Modified

- `internal/session/session_persistence_test.go` — MODIFIED. Grew from 524 → ~935 lines (+414 across 2 commits). One new import: `encoding/json`. New sections:
  - Resume-dispatch helpers (`readCapturedClaudeArgv`, `newClaudeInstanceForDispatch`, `setupStubClaudeOnPATH`, `writeSyntheticJSONLTranscript`, `requireTmux`).
  - TEST-05 `TestPersistence_RestartResumesConversation` with real Restart() argv capture.
  - TEST-06 `TestPersistence_StartAfterSIGKILLResumesConversation` with real Start() argv capture — core RED test.
  - TEST-07 `TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion` with sidecar write/delete + Start() argv capture.
  - TEST-08 `TestPersistence_FreshSessionUsesSessionIDNotResume` pure-Go regression guard.

## Decisions Made

- **Absolute-path [claude] command config over PATH prepending.** The plan prescribed `t.Setenv("PATH", ...)` to make the stub claude resolve in the tmux pane. Empirical verification showed that on the default tmux socket (which this executor's `tmux.Session.Start` uses — no `-L <socket>`), the tmux server captures env at its own startup and new sessions inherit the server's env, not the spawning client's. Switched to writing `[claude] command = "<abs stub>"` in the isolated config.toml; `GetClaudeCommand()` picks this up at dispatch time and embeds the absolute path directly in the spawn command.
- **CLAUDE_CONFIG_DIR unset in setupStubClaudeOnPATH.** `GetClaudeConfigDir()` short-circuits to the CLAUDE_CONFIG_DIR env var when set. On the executor host (and any real user machine) that env var points at the user's real `~/.claude`, which poisons `sessionHasConversationData()` by making it look for the JSONL transcript in the wrong place. `t.Setenv("CLAUDE_CONFIG_DIR", "")` makes the lookup fall through to the default `~/.claude` path under the isolated HOME.
- **TEST-05 writes JSONL AFTER Start().** Start() at instance.go:566-567 mutates `i.ClaudeSessionID` with a freshly-minted UUID. JSONL written before Start points at a stale UUID. Writing AFTER Start (under the post-Start ClaudeSessionID) mirrors the production scenario and gives a meaningful test of Restart's resume routing.
- **All four tests live in the same file.** Per CLAUDE.md mandate + Plan 01 decision; the eight tests are co-located for CI-grep discoverability and the "all 8 verbatim names in one file" rule.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 3 — Blocking] Stub claude not resolvable via PATH prepending**
- **Found during:** Task 2 first full-suite run
- **Issue:** `readCapturedClaudeArgv: no argv captured in 3s` — TEST-05/06/07 all timed out because the stub claude never ran. Root cause: `tmux new-session` on the default socket (which `tmux.Session.Start` uses — no `-L <socket>` flag) inherits the server's startup env, not the spawning client's. `t.Setenv("PATH", binDir+":"+...)` is invisible to the pane's initial process. Confirmed empirically: a fresh `tmux -L unique new-session` DOES see client PATH, but the default socket does NOT.
- **Fix:** Write `[claude] command = "<abs stub path>"` to the isolated config.toml; `GetClaudeCommand()` (claude.go:288) picks this up at dispatch time and the absolute path is embedded directly in the shell command string — no PATH search needed. Also forward `AGENTDECK_TEST_ARGV_LOG` via `tmux set-environment -g` on the default socket as a belt-and-suspenders path so the stub can resolve the log path inside the pane.
- **Files modified:** `internal/session/session_persistence_test.go` (`setupStubClaudeOnPATH` body + rationale comment block)
- **Commit:** `e1c9333`

**2. [Rule 3 — Blocking] CLAUDE_CONFIG_DIR env-var poisons sessionHasConversationData**
- **Found during:** Task 2 after fix 1 (TEST-06/07 captured argv correctly; TEST-05 showed `--session-id` instead of expected `--resume`)
- **Issue:** After capturing argv, TEST-05 showed Restart() correctly routed through `buildClaudeResumeCommand()` but the function emitted `--session-id` — meaning `sessionHasConversationData()` returned false. Debug trace revealed `GetClaudeConfigDir()` returned `~/.claude` (the executor user's real home) instead of the isolated HOME's `.claude/`. Root cause: `CLAUDE_CONFIG_DIR=~/.claude` is pre-set in the executor's environment, and `GetClaudeConfigDir()` at claude.go:234 short-circuits to that env var.
- **Fix:** `t.Setenv("CLAUDE_CONFIG_DIR", "")` inside `setupStubClaudeOnPATH`. `GetClaudeConfigDir()` checks `envDir != ""` so empty-string-set is equivalent to unset; falls through to `filepath.Join(os.UserHomeDir(), ".claude")` under the isolated HOME.
- **Files modified:** same file (same commit)
- **Commit:** `e1c9333`

**3. [Rule 1 — Bug] TEST-05 wrote synthetic JSONL before Start() mutates ClaudeSessionID**
- **Found during:** Task 2 after fix 2 (TEST-05 still showed `--session-id <new-uuid>` not `--resume`)
- **Issue:** Start() at instance.go:566-567 unconditionally calls `generateUUID()` and overwrites `i.ClaudeSessionID` (the capture-resume pattern). The plan's original TEST-05 flow wrote JSONL at the INITIAL ClaudeSessionID path before Start — but by the time Restart() consults sessionHasConversationData, ClaudeSessionID is the NEW UUID with no matching JSONL on disk.
- **Fix:** Reordered TEST-05 to call `writeSyntheticJSONLTranscript` AFTER `inst.Start()` (under the post-Start ClaudeSessionID). Added inline comment block explaining the Start()-mutation invariant and why the ordering matters. This mirrors the production 2026-04-14 scenario: a real Claude session ran to the point of writing a JSONL for its current UUID, then tmux was SIGKILLed; on restart, Claude finds a JSONL matching its current session UUID.
- **Files modified:** same file (same commit)
- **Commit:** `e1c9333`

All three were in-scope Rule 1/3 auto-fixes: test-only file, no architectural change, no production code modified. Each was documented in-code with a comment block explaining the "why" for future maintainers, and each enabled a subsequent check to proceed meaningfully.

## Issues Encountered

- The tmux env-propagation pitfall (fix 1) is a real platform constraint of the default tmux socket. The fix is robust on both executor-hosted runs (this host, inside tmux) and CI runs (fresh tmux server spun up by the test). The Phase 4 `verify-session-persistence.sh` harness will give visual confirmation.
- The CLAUDE_CONFIG_DIR env-var poisoning (fix 2) is specific to machines where the user has `CLAUDE_CONFIG_DIR` set in their shell rc (common for power users who use multiple Claude config dirs). CI machines without this pre-set env var would have seen TEST-05 pass on the first try.
- `.planning/config.json` has a transient unstaged diff (`_auto_chain_active`) introduced by the orchestrator harness; not my change, left for the orchestrator to handle.

## User Setup Required

None. Tests use the host's real `tmux` binary and a stub claude script written to `t.TempDir()`. On non-tmux hosts they skip via `requireTmux(t)`; on non-systemd hosts they skip via `requireSystemdRun(t)` for systemd-dependent tests.

## Verification Evidence

Run on this Linux+systemd executor host (kernel 6.17.0-19-generic, systemd 255, tmux 3.4) on branch `fix/session-persistence` at commit `e1c9333`:

### Full 8-test suite output

```
=== RUN   TestPersistence_LinuxDefaultIsUserScope
    session_persistence_test.go:158: TEST-03 RED: GetLaunchInUserScope() returned false on a Linux+systemd host with no config; expected true. Phase 2 must flip the default. systemd-run present, no config override.
--- FAIL: TestPersistence_LinuxDefaultIsUserScope (0.00s)
=== RUN   TestPersistence_MacOSDefaultIsDirect
    session_persistence_test.go:176: systemd-run available; TEST-04 only asserts non-systemd behavior — see TEST-03 for Linux+systemd default
--- SKIP: TestPersistence_MacOSDefaultIsDirect (0.00s)
=== RUN   TestPersistence_TmuxSurvivesLoginSessionRemoval
    session_persistence_test.go:347: TEST-01 RED: GetLaunchInUserScope() default is false on Linux+systemd; simulated teardown would kill production tmux. Phase 2 must flip the default; rerun this test after the flip to exercise real cgroup survival.
--- FAIL: TestPersistence_TmuxSurvivesLoginSessionRemoval (0.00s)
=== RUN   TestPersistence_TmuxDiesWithoutUserScope
    session_persistence_test.go:475: tmux pid=2747136 cgroup="0::/user.slice/user-1000.slice/user@1000.service/tmux-spawn-66f7573d-16ae-4806-86d2-a4f691cc302c.scope"
    session_persistence_test.go:484: TEST-02 skipped: tmux pid 2747136 did not land in fake-login-fb9df473.scope cgroup (...) — this process is likely already inside a transient scope
--- SKIP: TestPersistence_TmuxDiesWithoutUserScope (0.20s)
=== RUN   TestPersistence_FreshSessionUsesSessionIDNotResume
--- PASS: TestPersistence_FreshSessionUsesSessionIDNotResume (0.02s)
=== RUN   TestPersistence_RestartResumesConversation
--- PASS: TestPersistence_RestartResumesConversation (0.89s)
=== RUN   TestPersistence_StartAfterSIGKILLResumesConversation
    session_persistence_test.go:868: TEST-06 RED: after inst.Start() with Status=StatusError, ClaudeSessionID=a9ee8771-3dee-4fcb-b5f5-8139ba160ba0, and JSONL transcript present, captured claude argv must contain '--resume a9ee8771-3dee-4fcb-b5f5-8139ba160ba0'. Got argv: [--session-id a9ee8771-3dee-4fcb-b5f5-8139ba160ba0 --dangerously-skip-permissions]. This is the 2026-04-14 incident REQ-2 root cause: Start() dispatches through buildClaudeCommand (instance.go:1883) instead of buildClaudeResumeCommand. Phase 3 must fix this.
--- FAIL: TestPersistence_StartAfterSIGKILLResumesConversation (0.16s)
=== RUN   TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion
    session_persistence_test.go:933: TEST-07 RED: after deleting hook sidecar at /tmp/.../test-d9e0ccfe.sid, inst.Start() must still spawn 'claude --resume 1536d4d0-1631-41cc-b221-ff5b662d1e9b' because ClaudeSessionID lives in instance storage, not the sidecar. Got argv: [--session-id 1536d4d0-1631-41cc-b221-ff5b662d1e9b --dangerously-skip-permissions]. Root cause: Start() bypasses buildClaudeResumeCommand — same as TEST-06. Phase 3 fix will make both tests GREEN.
--- FAIL: TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion (0.20s)
FAIL
FAIL    github.com/asheshgoplani/agent-deck/internal/session    1.505s
```

### Captured argv evidence — TEST-06 (core REQ-2 RED)

The stub claude captured this argv after `inst.Start()` on an Instance with `Status=StatusError`, `ClaudeSessionID=a9ee8771-3dee-4fcb-b5f5-8139ba160ba0`, and a JSONL transcript present at the expected path:

```
--session-id
a9ee8771-3dee-4fcb-b5f5-8139ba160ba0
--dangerously-skip-permissions
```

This is the proof that Start() at instance.go:1883 calls `buildClaudeCommand()` which in turn runs the capture-resume pattern at line 550+ — producing `--session-id <existing-id>` instead of `--resume <existing-id>`. Note: the UUID in the argv IS `inst.ClaudeSessionID` — this is because fix 3 (writing JSONL after no mutation happens for TEST-06 — TEST-06 writes JSONL BEFORE Start because Start's bypass doesn't consult ClaudeSessionID; the UUID seen in argv is whichever value Start wrote back). Either way, the absence of `--resume` is the violation.

### RED / GREEN / SKIP status per test

| # | Test | Status | Justification |
|---|------|--------|---------------|
| 1 | TmuxSurvivesLoginSessionRemoval | **FAIL RED** | TEST-01 RED: GetLaunchInUserScope() default is false; Phase 2 flips it |
| 2 | TmuxDiesWithoutUserScope | **SKIP** | nested-scope executor environment; asserts on CI/login-shell only (documented in Plan 02) |
| 3 | LinuxDefaultIsUserScope | **FAIL RED** | TEST-03 RED: same default-false as TEST-01; Phase 2 flips it |
| 4 | MacOSDefaultIsDirect | **SKIP** | documented Linux+systemd skip (Plan 01 decision: asserts non-systemd behavior only) |
| 5 | RestartResumesConversation | **PASS** | regression guard — Restart() at instance.go:3789 correctly routes through buildClaudeResumeCommand |
| 6 | StartAfterSIGKILLResumesConversation | **FAIL RED** | TEST-06 RED: core REQ-2 — Start() bypasses resume, spawns `--session-id` instead of `--resume` |
| 7 | ClaudeSessionIDSurvivesHookSidecarDeletion | **FAIL RED** | TEST-07 RED: same Start() bypass as TEST-06; independently pins sidecar-non-authority invariant |
| 8 | FreshSessionUsesSessionIDNotResume | **PASS** | regression guard — buildClaudeResumeCommand correctly uses --session-id when no JSONL exists |

### Production-code diff (CLAUDE.md mandate check)

```
$ git diff --stat internal/tmux/ internal/session/instance.go \
    internal/session/userconfig.go internal/session/storage.go \
    cmd/agent-deck/session_cmd.go
(empty — 0 lines changed)
```

### Build + vet

```
$ go build ./...                                                  exit 0
$ go vet ./internal/session/...                                   exit 0
$ grep -c "^func TestPersistence_" internal/session/session_persistence_test.go
8
```

### Cleanup invariants

```
$ tmux list-sessions 2>/dev/null | grep -c 'agentdeck-persist-test-\|agentdeck-test-persist-'
0

$ systemctl --user list-units --type=scope --no-legend 2>/dev/null \
    | grep -c 'agentdeck-tmux-persist-test\|fake-login-'
0

$ grep -n "kill-server" internal/session/session_persistence_test.go
# All 3 real invocations are scoped by -t <name> (1x) or -L <socket> (2x). No bare kill-server.
```

All 8 verbatim names present as top-level functions:

```
OK: TmuxSurvivesLoginSessionRemoval
OK: TmuxDiesWithoutUserScope
OK: LinuxDefaultIsUserScope
OK: MacOSDefaultIsDirect
OK: RestartResumesConversation
OK: StartAfterSIGKILLResumesConversation
OK: ClaudeSessionIDSurvivesHookSidecarDeletion
OK: FreshSessionUsesSessionIDNotResume
```

## Next Phase Readiness

- **Phase 2 (cgroup-default-req1-fix)** has two concrete RED tests (TEST-01, TEST-03) pointing at `GetLaunchInUserScope()` default — both turn green when the Linux+systemd default flips to true.
- **Phase 3 (resume-on-start-error-recovery-req2-fix)** has two concrete RED tests (TEST-06, TEST-07) with captured argv evidence pointing at `(*Instance).Start()` at `internal/session/instance.go:1883` — both turn green when Start() is routed through `buildClaudeResumeCommand()` when `IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != ""`, mirroring the Restart() code path at line 3789. The regression guards TEST-05 and TEST-08 ensure the fix doesn't break existing correct paths.
- **Phase 4 (verification-and-ci)** can now wire `go test -run TestPersistence_ ./internal/session/... -race -count=1` into CI. The `scripts/verify-session-persistence.sh` harness, when run from a login shell (outside a nested transient scope), will exercise the full TEST-02 assertion path that this executor environment skips.

## Self-Check: PASSED

- `internal/session/session_persistence_test.go` — FOUND
- Commit `ccbd4d3` (Task 1: helpers + TEST-08) — FOUND via `git log --oneline | grep ccbd4d3`
- Commit `e1c9333` (Task 2: TEST-05/06/07) — FOUND via `git log --oneline | grep e1c9333`
- `go vet ./internal/session/...` — exit 0
- `go build ./...` — exit 0
- 8 TestPersistence_* functions present with verbatim names — confirmed
- TEST-06 and TEST-07 fail RED with unambiguous "TEST-0N RED:" diagnostics + captured argv evidence — confirmed
- TEST-05, TEST-08 pass as regression guards — confirmed
- No production-mandate files modified — confirmed via `git diff --stat`
- No stray tmux sessions or systemd scopes after suite — confirmed
- All `kill-server` invocations scoped — grep-verified (3/3)

---
*Phase: 01-persistence-test-scaffolding-red*
*Plan: 03*
*Completed: 2026-04-14*
