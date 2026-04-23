---
phase: 01-persistence-test-scaffolding-red
plan: 02
type: execute
wave: 2
depends_on:
  - "01-01"
files_modified:
  - internal/session/session_persistence_test.go
autonomous: true
requirements:
  - TEST-01
  - TEST-02
user_setup: []

must_haves:
  truths:
    - "TestPersistence_TmuxSurvivesLoginSessionRemoval exists, runs on Linux+systemd, and FAILS RED on current v1.5.1 with a message explicitly naming the cgroup default as the cause."
    - "TestPersistence_TmuxDiesWithoutUserScope exists, runs on Linux+systemd, and PASSES immediately (inverse pin — confirms opt-out still dies under simulated teardown)."
    - "Both tests call `requireSystemdRun(t)` first and t.Skipf cleanly on macOS / non-systemd hosts with the literal substring 'no systemd-run available:'."
    - "Both tests use `uniqueTmuxServerName(t)` and `tmux kill-server -t <name>` in t.Cleanup — never bare `tmux kill-server`."
    - "Both tests exercise REAL tmux and REAL `systemd-run --user --scope` — no mocking (per repo CLAUDE.md no-mocking rule for persistence tests)."
    - "After both tests run, `tmux list-sessions 2>/dev/null | grep 'agentdeck-test-persist-' | wc -l` returns 0."
    - "No production code under internal/tmux/, internal/session/instance.go, internal/session/userconfig.go, internal/session/storage.go, or cmd/agent-deck/session_cmd.go is modified."
  artifacts:
    - path: "internal/session/session_persistence_test.go"
      provides: "TEST-01 + TEST-02 appended to the skeleton from Plan 01"
      contains: "func TestPersistence_TmuxSurvivesLoginSessionRemoval, func TestPersistence_TmuxDiesWithoutUserScope"
  key_links:
    - from: "internal/session/session_persistence_test.go::TestPersistence_TmuxSurvivesLoginSessionRemoval"
      to: "systemd-run --user --scope --unit=<fake-login-...> + systemctl --user stop"
      via: "real exec.Command invocations, no shell-out mocking"
      pattern: "systemd-run.*--user.*--scope"
    - from: "internal/session/session_persistence_test.go::TestPersistence_TmuxDiesWithoutUserScope"
      to: "tmux new-session -d -s <unique> (LaunchInUserScope=false path)"
      via: "direct tmux binary invocation WITHOUT systemd-run wrap — mirrors userconfig.Session.startCommandSpec when LaunchInUserScope=false"
      pattern: "exec.Command\\(\"tmux\", \"new-session\""
---

<objective>
Append TEST-01 (`TestPersistence_TmuxSurvivesLoginSessionRemoval`) and TEST-02 (`TestPersistence_TmuxDiesWithoutUserScope`) to the test file created in Plan 01. These are the cgroup-teardown-survival tests that replicate the 2026-04-14 incident root cause.

Purpose: TEST-01 is RED against current v1.5.1 — it spawns tmux inside a throwaway `systemd-run --user --scope` unit (simulating a login-session scope), terminates the scope, and asserts the tmux server survives. On current code the server dies because it's not launched under `user@UID.service`. TEST-02 is the INVERSE PIN — it does the same teardown with `LaunchInUserScope=false` (direct tmux spawn inherits the scope) and asserts the server DIES. TEST-02 must stay green through the entire milestone so a future "fix" that ignores opt-outs is caught.

Output: Same test file, now containing 4 of the 8 tests. `go vet` still passes. No production code modified.
</objective>

<execution_context>
@<repo-root>/.worktrees/session-persistence/.claude/get-shit-done/workflows/execute-plan.md
@<repo-root>/.worktrees/session-persistence/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/PROJECT.md
@.planning/ROADMAP.md
@.planning/REQUIREMENTS.md
@.planning/phases/01-persistence-test-scaffolding-red/01-CONTEXT.md
@.planning/phases/01-persistence-test-scaffolding-red/01-01-SUMMARY.md
@docs/SESSION-PERSISTENCE-SPEC.md
@CLAUDE.md

# Production-code API surface (read-only in this phase)
@internal/tmux/tmux.go
@internal/session/userconfig.go

<interfaces>
<!-- Production-code behavior this plan's tests model manually.
     The tests DO NOT call into internal/tmux/Session; they invoke the real
     `tmux` and `systemd-run` binaries directly to isolate the cgroup question
     from the higher-level Session plumbing. -->

From internal/tmux/tmux.go (lines 814–837) — the production spawn behavior the tests model:
```go
func (s *Session) startCommandSpec(workDir, command string) (string, []string) {
    // … builds `new-session -d -s <name> -c <dir>` …
    if !s.LaunchInUserScope {
        return "tmux", args   // ← TEST-02 models this path
    }
    unitName := "agentdeck-tmux-" + sanitizeSystemdUnitComponent(s.Name)
    scopeArgs := []string{"--user", "--scope", "--quiet", "--collect", "--unit", unitName, "tmux"}
    scopeArgs = append(scopeArgs, args...)
    return "systemd-run", scopeArgs   // ← TEST-01 models this path
}
```

Helpers available from Plan 01 (already in the file):
- `uniqueTmuxServerName(t) string` — returns `agentdeck-test-persist-<hex>` and registers tmux-cleanup in t.Cleanup.
- `requireSystemdRun(t)` — t.Skipf if systemd-run missing.
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Add TEST-01 (TmuxSurvivesLoginSessionRemoval) — simulated login-session teardown survival</name>
  <files>internal/session/session_persistence_test.go</files>
  <read_first>
    - internal/session/session_persistence_test.go (Plan 01 already created it; helpers `uniqueTmuxServerName`, `requireSystemdRun`, `isolatedHomeDir` are defined)
    - internal/tmux/tmux.go lines 765–837 (sanitizeSystemdUnitComponent, startCommandSpec, the systemd-run wrap pattern)
    - docs/SESSION-PERSISTENCE-SPEC.md REQ-3 item 1 (TEST-01 semantics, simulated teardown via `systemd-run --user --scope --unit=fake-login bash -c "exec sleep 1"`)
    - .planning/phases/01-persistence-test-scaffolding-red/01-CONTEXT.md "Login-session teardown simulation" block in <specifics> (explicit 4-step simulation plan)
    - CLAUDE.md "Session persistence: mandatory test coverage" + tmux safety rule
  </read_first>
  <behavior>
    - Function signature: `func TestPersistence_TmuxSurvivesLoginSessionRemoval(t *testing.T)` — verbatim, no abbreviation.
    - First line: `requireSystemdRun(t)`.
    - Test builds TWO real systemd-run scopes to isolate cgroup-inheritance behavior:
      1. The `fake-login-<rand>` scope: simulates the login-session scope that logind tears down. Command `bash -c "exec sleep 300"`.
      2. The agent-deck tmux server: launched under `systemd-run --user --scope --quiet --collect --unit agentdeck-tmux-<name> tmux new-session -d -s <name> bash -c "exec sleep 300"`. This matches the production `LaunchInUserScope=true` path from `startCommandSpec`.
    - Critically: the tmux server is launched under its OWN `agentdeck-tmux-*` scope (mirrors the REQ-1 fix), NOT as a child of the `fake-login-*` scope. The test is asserting that a server launched under `user@UID.service` survives when an UNRELATED login-session scope is torn down.
    - Captures the tmux server PID via `tmux list-sessions -t <name> -F '#{pid}'` or via `pidof`/`pgrep` against the known server name.
    - Tears down the `fake-login-<rand>` scope with `systemctl --user stop fake-login-<rand>.scope` (wait up to 3s for terminate).
    - Asserts the tmux server PID is still alive via `kill -0 <pid>` (via `syscall.Kill` or `exec.Command("kill", "-0", pid).Run()`).
    - Failure message when tmux died: `t.Fatalf("TEST-01 RED: tmux server PID %d died after fake-login scope teardown; expected to survive. Current v1.5.1 default launch_in_user_scope=false inherits the login-session cgroup. Phase 2 must flip the default.")`.
    - IMPORTANT: because TEST-01 is EXERCISING the systemd-run wrap manually (not through Session.Start), it will actually PASS on Linux+systemd when it runs today — the cgroup isolation is achieved by the test's own manual `systemd-run --user --scope` wrap. TO ACHIEVE RED, the test MUST check the default: it first calls `GetTmuxSettings().GetLaunchInUserScope()` and fails with a diagnostic if the default is false — so the RED state on current v1.5.1 triggers before any tmux work even happens. Failure message in that branch: `t.Fatalf("TEST-01 RED: GetLaunchInUserScope() default is false on Linux+systemd; simulated teardown would kill production tmux. Phase 2 must flip the default; rerun this test after the flip to exercise real cgroup survival.")`.
    - If the default is `true` (post-Phase-2), the test proceeds with the full simulation and asserts survival.
    - Scope names MUST be unique per invocation: `fake-login-<hex>` and `agentdeck-tmux-<hex>` using the same unique suffix from `uniqueTmuxServerName` or a separate call to the same helper.
    - t.Cleanup MUST (in order): (a) `systemctl --user stop fake-login-<rand>.scope` (idempotent if already stopped), (b) `tmux kill-server -t <name>`, (c) `systemctl --user stop agentdeck-tmux-<name>.scope` (idempotent).
    - Test tolerates "scope not found" errors during cleanup (treat as already-stopped).
    - Test uses `t.TempDir()` for any file output; no writes outside temp.
    - Current v1.5.1 behavior: TEST-01 FAILS with "TEST-01 RED: GetLaunchInUserScope() default is false" — this is the diagnostic that tells Phase 2 what to fix.
    - Post-Phase-2 behavior (not this plan's concern, but the test must work): the default flips to true, the test proceeds to the full simulation, tmux survives, test passes.
  </behavior>
  <action>
    Append to `internal/session/session_persistence_test.go`:

    1. Add `syscall` and `time` to the import block (alongside existing imports from Plan 01).

    2. Add a small helper (inside the same file, unexported) `pidAlive(pid int) bool`:
       ```go
       func pidAlive(pid int) bool {
           if pid <= 0 {
               return false
           }
           // signal 0 checks for process existence without sending a real signal
           return syscall.Kill(pid, syscall.Signal(0)) == nil
       }
       ```

    3. Add helper `startFakeLoginScope(t *testing.T) string` that:
       - Generates `fakeName := "fake-login-" + <8 hex chars>` (use same random pattern as uniqueTmuxServerName — you can factor a tiny `randomHex8(t)` helper if preferred).
       - Runs `systemd-run --user --scope --quiet --collect --unit=<fakeName> bash -c "exec sleep 300"` in background via `exec.Command(...).Start()`.
       - Registers `t.Cleanup(func() { _ = exec.Command("systemctl", "--user", "stop", fakeName+".scope").Run() })`.
       - Returns `fakeName`.
       - On error, t.Fatalf with "startFakeLoginScope: %v".

    4. Add helper `startAgentDeckTmuxInUserScope(t *testing.T, serverName string) (pid int)` that:
       - Runs `systemd-run --user --scope --quiet --collect --unit=agentdeck-tmux-<serverName> tmux -L <serverName> new-session -d -s persist bash -c "exec sleep 300"` (note: `-L <serverName>` uses a dedicated tmux server socket, so `tmux kill-server -t <serverName>` targets ONLY this server).
       - Waits up to 2s (`time.Sleep(100*time.Millisecond)` loop) for `tmux -L <serverName> list-sessions` to return success.
       - Captures the tmux PID: `out, _ := exec.Command("tmux", "-L", serverName, "display-message", "-p", "-t", "persist", "#{pid}").Output()` — actually to get the server PID use `pgrep -f "tmux -L <serverName>"` or read from `systemctl --user show -p MainPID agentdeck-tmux-<serverName>.scope`. Use the `systemctl show -p MainPID` approach — it returns `MainPID=12345`.
       - Registers `t.Cleanup(func() { _ = exec.Command("tmux", "-L", serverName, "kill-server").Run(); _ = exec.Command("systemctl", "--user", "stop", "agentdeck-tmux-"+serverName+".scope").Run() })`. Note: `tmux -L <serverName> kill-server` ONLY kills the server on socket `<serverName>` — it is SAFE because different `-L` sockets are isolated tmux servers.
       - Returns the PID as int.

    5. Add `TestPersistence_TmuxSurvivesLoginSessionRemoval`:
       ```go
       func TestPersistence_TmuxSurvivesLoginSessionRemoval(t *testing.T) {
           requireSystemdRun(t)

           // RED-state gate: if the default is still false, this test fails with the
           // diagnostic that tells Phase 2 what to fix. This check intentionally
           // runs BEFORE any tmux spawning so the RED message is unambiguous and
           // no tmux server is created to leak.
           home := isolatedHomeDir(t)
           _ = home
           settings := GetTmuxSettings()
           if !settings.GetLaunchInUserScope() {
               t.Fatalf("TEST-01 RED: GetLaunchInUserScope() default is false on Linux+systemd; simulated teardown would kill production tmux. Phase 2 must flip the default; rerun this test after the flip to exercise real cgroup survival.")
           }

           // Post-Phase-2 flow: simulate the 2026-04-14 incident.
           serverName := uniqueTmuxServerName(t)
           fakeLogin := startFakeLoginScope(t)

           pid := startAgentDeckTmuxInUserScope(t, serverName)
           if !pidAlive(pid) {
               t.Fatalf("setup failure: tmux pid %d not alive immediately after spawn", pid)
           }

           // Teardown the fake login scope — simulates logind removing an SSH login session.
           if err := exec.Command("systemctl", "--user", "stop", fakeLogin+".scope").Run(); err != nil {
               // Treat non-existence as acceptable
               t.Logf("systemctl stop %s: %v (continuing)", fakeLogin, err)
           }

           // Give systemd up to 3s to settle the teardown.
           deadline := time.Now().Add(3 * time.Second)
           for time.Now().Before(deadline) {
               time.Sleep(100 * time.Millisecond)
           }

           if !pidAlive(pid) {
               t.Fatalf("TEST-01 RED: tmux server pid %d died after fake-login scope teardown; expected to survive because the server was launched under its own agentdeck-tmux-<name>.scope. The 2026-04-14 incident is recurring.", pid)
           }
       }
       ```

    6. Verification:
       - `go vet ./internal/session/...` exits 0.
       - On Linux+systemd with current v1.5.1: `go test -run TestPersistence_TmuxSurvivesLoginSessionRemoval ./internal/session/... -count=1 -v` exits NON-ZERO with "TEST-01 RED: GetLaunchInUserScope() default is false" in the output.
       - On macOS: same command exits 0 with "--- SKIP" and "no systemd-run available:" in the output.
       - After the test run, `tmux list-sessions 2>/dev/null | grep 'agentdeck-test-persist-' | wc -l` returns 0 (on Linux+systemd; on macOS the test skipped so nothing was created).
       - `systemctl --user list-units --type=scope --no-legend | grep 'agentdeck-tmux-\\|fake-login-' | wc -l` returns 0 after the run (no orphan scopes).
       - `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces no output.
  </action>
  <verify>
    <automated>grep -q "func TestPersistence_TmuxSurvivesLoginSessionRemoval" internal/session/session_persistence_test.go &amp;&amp; grep -q "TEST-01 RED:" internal/session/session_persistence_test.go &amp;&amp; grep -q "pidAlive\\|syscall.Kill" internal/session/session_persistence_test.go &amp;&amp; grep -q "tmux\", \"-L\"" internal/session/session_persistence_test.go &amp;&amp; go vet ./internal/session/... &amp;&amp; go test -run TestPersistence_TmuxSurvivesLoginSessionRemoval ./internal/session/... -count=1 -v 2>&amp;1 | tee /tmp/test01.out; (grep -q "^--- FAIL: TestPersistence_TmuxSurvivesLoginSessionRemoval\\|^--- SKIP: TestPersistence_TmuxSurvivesLoginSessionRemoval" /tmp/test01.out)</automated>
  </verify>
  <acceptance_criteria>
    - `grep -q "func TestPersistence_TmuxSurvivesLoginSessionRemoval" internal/session/session_persistence_test.go` returns 0
    - `grep -q "TEST-01 RED:" internal/session/session_persistence_test.go` returns 0
    - `grep -q "tmux\", \"-L\"" internal/session/session_persistence_test.go` returns 0 (per-test isolated tmux socket)
    - `grep -q "fake-login-" internal/session/session_persistence_test.go` returns 0
    - `grep -c "tmux kill-server" internal/session/session_persistence_test.go` count matches `grep -c "tmux kill-server\\|\"-L\", serverName, \"kill-server\"" internal/session/session_persistence_test.go` count (every kill-server invocation is scoped by -L or -t)
    - `go vet ./internal/session/...` exits 0
    - On Linux+systemd with current v1.5.1 (default=false): `go test -run TestPersistence_TmuxSurvivesLoginSessionRemoval ./internal/session/... -count=1` exits NON-ZERO and output contains "TEST-01 RED:"
    - On macOS/no-systemd: `go test -run TestPersistence_TmuxSurvivesLoginSessionRemoval ./internal/session/... -count=1 -v` output contains "--- SKIP" and "no systemd-run available:"
    - After the test: `tmux list-sessions 2>/dev/null | grep -c 'agentdeck-test-persist-'` returns 0
    - After the test: `systemctl --user list-units --type=scope --no-legend 2>/dev/null | grep -c 'fake-login-\\|agentdeck-tmux-.*test'` returns 0
    - `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces no output
  </acceptance_criteria>
  <done>TEST-01 exists, fails RED on Linux+systemd with diagnostic "TEST-01 RED: GetLaunchInUserScope() default is false", skips cleanly on macOS, leaves no stray tmux servers or systemd scopes, and does not modify production code.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: Add TEST-02 (TmuxDiesWithoutUserScope) — inverse pin, must PASS immediately</name>
  <files>internal/session/session_persistence_test.go</files>
  <read_first>
    - internal/session/session_persistence_test.go (TEST-01 and helpers now present)
    - docs/SESSION-PERSISTENCE-SPEC.md REQ-3 item 2 (TEST-02 semantics: inverse pin)
    - .planning/phases/01-persistence-test-scaffolding-red/01-CONTEXT.md decisions: "TEST-02: PASS (inverse pin — opt-out behavior already broken, this test confirms it)" and "must stay green from the moment the suite lands through the rest of the milestone"
    - internal/tmux/tmux.go lines 814–837 (the `!s.LaunchInUserScope` branch — direct `tmux new-session` without systemd-run wrap)
  </read_first>
  <behavior>
    - Function signature: `func TestPersistence_TmuxDiesWithoutUserScope(t *testing.T)` — verbatim.
    - First line: `requireSystemdRun(t)`.
    - This test is the INVERSE PIN: it proves that WITHOUT cgroup isolation (explicit opt-out `LaunchInUserScope=false`), a tmux server parented inside a login-session scope DOES die when that scope is torn down. This must PASS today and stay green forever — it guards against a future "fix" that changes the default but silently also masks opt-outs.
    - Test flow:
      1. `requireSystemdRun(t)` + `uniqueTmuxServerName(t)` + `isolatedHomeDir(t)`.
      2. Start a fake-login scope: `fakeName := startFakeLoginScope(t)` — same helper as TEST-01.
      3. Start tmux as a CHILD OF the fake-login scope (to mimic the production path when `LaunchInUserScope=false` and the user is inside an SSH session). Do this via `systemd-run --user --scope --quiet --collect --unit=<fakeName>-child bash -c "tmux -L <serverName> new-session -d -s persist bash -c 'exec sleep 300'"`.
         - Actually simpler: use `systemd-run --user --scope --slice=<fakeName>.scope ...` is not portable. The cleanest cgroup-inheritance model is: we run `systemd-run --user --scope --unit=<fakeName>` which creates the scope, and inside that same call we pass the tmux command directly. So revise helper: `startFakeLoginScopeWithTmux(t, serverName) (fakeName string, tmuxPID int)` that runs `systemd-run --user --scope --quiet --collect --unit=<fakeName> bash -c "tmux -L <serverName> new-session -d -s persist bash -c 'exec sleep 300'; exec sleep 300"` — the tmux server is a grandchild of the fake-login scope and inherits its cgroup.
         - Capture the tmux server PID via `tmux -L <serverName> display-message -p -t persist "#{pid}"` after waiting 2s for startup, then walk up with `pgrep -f "tmux -L <serverName>"` to find the SERVER process (display-message returns the pane PID; the server PID is the parent). Simpler: `pgrep -f "^tmux.*-L.*<serverName>"` finds the server.
      4. Assert the tmux server PID IS alive immediately (sanity check, `t.Fatalf("setup failure: tmux not running after start")` on failure).
      5. Tear down the fake-login scope: `systemctl --user stop <fakeName>.scope`.
      6. Wait up to 3s in a polling loop for the PID to become non-alive (`!pidAlive(pid)`).
      7. Assert the tmux server PID is NOT alive. Failure message: `t.Fatalf("TEST-02 INVERSE PIN: tmux server pid %d survived fake-login scope teardown WITHOUT launch_in_user_scope. The opt-out path must remain vulnerable so any future 'fix' that silently masks opt-outs is caught. Expected death.")`.
    - Cleanup:
      - `tmux -L <serverName> kill-server` (idempotent; safe because -L isolates socket).
      - `systemctl --user stop <fakeName>.scope` (idempotent).
      - Both registered as t.Cleanup.
    - On current v1.5.1 this test PASSES because the tmux server inherits the fake-login scope and dies when the scope is stopped.
    - On Phase 2 code (post-fix) this test MUST CONTINUE TO PASS — the test uses an explicit manual scope inheritance, not the production default, so the Phase 2 default flip does not affect it.
  </behavior>
  <action>
    Append to `internal/session/session_persistence_test.go`:

    1. Add helper `startTmuxInsideFakeLogin(t *testing.T, serverName string) (fakeName string, tmuxPID int)`:
       ```go
       func startTmuxInsideFakeLogin(t *testing.T, serverName string) (string, int) {
           t.Helper()
           var b [4]byte
           if _, err := rand.Read(b[:]); err != nil {
               t.Fatalf("rand: %v", err)
           }
           fakeName := "fake-login-" + hex.EncodeToString(b[:])
           // Start tmux as a child of the fake-login scope. The outer `sleep 300`
           // keeps the scope alive until we tear it down.
           cmd := exec.Command("systemd-run", "--user", "--scope", "--quiet",
               "--collect", "--unit="+fakeName,
               "bash", "-c",
               "tmux -L "+serverName+" new-session -d -s persist bash -c 'exec sleep 300'; exec sleep 300")
           if err := cmd.Start(); err != nil {
               t.Fatalf("systemd-run start: %v", err)
           }
           t.Cleanup(func() {
               _ = exec.Command("systemctl", "--user", "stop", fakeName+".scope").Run()
               _ = exec.Command("tmux", "-L", serverName, "kill-server").Run()
           })
           // Poll up to 2s for the tmux server to come up.
           deadline := time.Now().Add(2 * time.Second)
           var pid int
           for time.Now().Before(deadline) {
               time.Sleep(100 * time.Millisecond)
               out, err := exec.Command("pgrep", "-f", "tmux -L "+serverName+" ").Output()
               if err == nil {
                   for _, line := range splitLines(string(out)) {
                       p, perr := parseIntSafe(line)
                       if perr == nil && p > 0 {
                           pid = p
                           break
                       }
                   }
                   if pid > 0 {
                       break
                   }
               }
           }
           if pid == 0 {
               t.Fatalf("startTmuxInsideFakeLogin: could not locate tmux server PID for -L %s", serverName)
           }
           return fakeName, pid
       }
       ```
       Plus two tiny internal helpers `splitLines` and `parseIntSafe` (use `strings.Split`/`strings.TrimSpace` and `strconv.Atoi` — add `strconv`/`strings` to imports if not present).

    2. Add `TestPersistence_TmuxDiesWithoutUserScope`:
       ```go
       // TestPersistence_TmuxDiesWithoutUserScope is the INVERSE PIN. It asserts
       // that when tmux is spawned WITHOUT the systemd-run --user --scope wrap
       // (i.e., launch_in_user_scope=false — the current v1.5.1 default and also
       // the explicit opt-out path after Phase 2), a login-session scope teardown
       // DOES kill the tmux server. This replicates the 2026-04-14 incident root
       // cause and must stay green for the entire milestone. Any future "fix"
       // that silently masks opt-outs will break this test.
       func TestPersistence_TmuxDiesWithoutUserScope(t *testing.T) {
           requireSystemdRun(t)
           _ = isolatedHomeDir(t)
           serverName := uniqueTmuxServerName(t)

           fakeName, pid := startTmuxInsideFakeLogin(t, serverName)
           if !pidAlive(pid) {
               t.Fatalf("setup failure: tmux server pid %d not alive immediately after spawn", pid)
           }

           if err := exec.Command("systemctl", "--user", "stop", fakeName+".scope").Run(); err != nil {
               t.Logf("systemctl stop %s: %v (continuing)", fakeName, err)
           }

           // Poll up to 3s for the pid to die.
           deadline := time.Now().Add(3 * time.Second)
           for time.Now().Before(deadline) {
               if !pidAlive(pid) {
                   return // PASS
               }
               time.Sleep(100 * time.Millisecond)
           }

           t.Fatalf("TEST-02 INVERSE PIN: tmux server pid %d survived fake-login scope teardown WITHOUT launch_in_user_scope. The opt-out path must remain vulnerable so any future 'fix' that silently masks opt-outs is caught. Expected death.", pid)
       }
       ```

    3. Verification:
       - `go vet ./internal/session/...` exits 0.
       - On Linux+systemd: `go test -run TestPersistence_TmuxDiesWithoutUserScope ./internal/session/... -count=1 -v` exits 0 (PASS).
       - On Linux+systemd: `go test -run "TestPersistence_TmuxDiesWithoutUserScope|TestPersistence_TmuxSurvivesLoginSessionRemoval" ./internal/session/... -count=1 -v` — TEST-02 PASS, TEST-01 FAIL (RED). Suite exit code: non-zero (because TEST-01 fails).
       - On macOS: both tests SKIP via `requireSystemdRun`; suite exits 0.
       - After the run, `tmux list-sessions 2>/dev/null | grep -c 'agentdeck-test-persist-'` returns 0.
       - `systemctl --user list-units --type=scope --no-legend 2>/dev/null | grep -c 'fake-login-\\|agentdeck-tmux-.*test'` returns 0.
       - `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces no output.
  </action>
  <verify>
    <automated>grep -q "func TestPersistence_TmuxDiesWithoutUserScope" internal/session/session_persistence_test.go &amp;&amp; grep -q "INVERSE PIN" internal/session/session_persistence_test.go &amp;&amp; grep -q "func startTmuxInsideFakeLogin" internal/session/session_persistence_test.go &amp;&amp; go vet ./internal/session/... &amp;&amp; go test -run TestPersistence_TmuxDiesWithoutUserScope ./internal/session/... -count=1 -v 2>&amp;1 | tee /tmp/test02.out; (grep -q "^--- PASS: TestPersistence_TmuxDiesWithoutUserScope\\|^--- SKIP: TestPersistence_TmuxDiesWithoutUserScope" /tmp/test02.out)</automated>
  </verify>
  <acceptance_criteria>
    - `grep -q "func TestPersistence_TmuxDiesWithoutUserScope" internal/session/session_persistence_test.go` returns 0
    - `grep -q "INVERSE PIN" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func startTmuxInsideFakeLogin" internal/session/session_persistence_test.go` returns 0
    - `go vet ./internal/session/...` exits 0
    - On Linux+systemd: `go test -run TestPersistence_TmuxDiesWithoutUserScope ./internal/session/... -count=1` exits 0 (PASS)
    - On macOS/no-systemd: `go test -run TestPersistence_TmuxDiesWithoutUserScope ./internal/session/... -count=1 -v` output contains "--- SKIP" and "no systemd-run available:"
    - `go test -run "TestPersistence_(TmuxDiesWithoutUserScope|TmuxSurvivesLoginSessionRemoval|LinuxDefaultIsUserScope|MacOSDefaultIsDirect)" ./internal/session/... -count=1 -v` runs exactly 4 tests; on Linux+systemd: 1 PASS (TEST-02), 2 FAIL (TEST-01, TEST-03), 1 SKIP (TEST-04)
    - After the run: `tmux list-sessions 2>/dev/null | grep -c 'agentdeck-test-persist-'` returns 0
    - After the run: `systemctl --user list-units --type=scope --no-legend 2>/dev/null | grep -c 'fake-login-'` returns 0
    - `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces no output
  </acceptance_criteria>
  <done>TEST-02 exists, passes on Linux+systemd (inverse pin green), skips cleanly on macOS, leaves no stray tmux servers or systemd scopes, does not modify production code. The combined TEST-01+TEST-02 pair replicates the 2026-04-14 incident root cause in both directions.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| test process → real tmux binary | Tests invoke `tmux -L <unique-socket> new-session` / `tmux -L <unique-socket> kill-server`. The `-L` flag isolates each test's tmux server on its own socket, so kill-server NEVER targets user sessions. |
| test process → systemd user manager | Tests invoke `systemd-run --user --scope --unit=<unique>` and `systemctl --user stop <unique>.scope`. All scope names use the `fake-login-<hex>` or `agentdeck-tmux-<hex>` prefix with random suffix; no broad teardown commands. |

## STRIDE Threat Register

No production code modified; threat surface unchanged. Tests run with real `tmux` and `systemd-run` binaries; tests MUST use unique tmux server names with the `agentdeck-test-*` prefix and MUST NOT call `tmux kill-server` without a `-t <name>` filter (per repo CLAUDE.md tmux safety mandate, after the 2025-12-10 incident that killed 40 user sessions).

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-02-01 | Denial of Service | tmux kill-server invocations | mitigate | Every kill-server call is scoped by `-L <unique-socket>` (per-test isolated tmux server). `-L` socket names use `agentdeck-test-persist-<hex>` — grep-verified in acceptance criteria. |
| T-02-02 | Denial of Service | systemctl --user stop invocations | mitigate | Every `systemctl stop` targets a unit name with the `fake-login-<hex>` or `agentdeck-tmux-<hex>` prefix + random suffix — grep-verified. No `systemctl daemon-reexec` / no `systemctl --user stop '*'`. |
| T-02-03 | Tampering | pid inspection via pgrep | mitigate | pgrep uses the unique `-L <serverName>` argument as the match pattern; cannot accidentally match other tmux instances. |
| T-02-04 | Information Disclosure | test stdout logs | accept | Tests log PIDs and scope names to t.Logf; no secrets. |
</threat_model>

<verification>
Plan-level verification (run after both tasks complete, sequentially in this order):

1. `go vet ./internal/session/...` exits 0.
2. `go build ./...` exits 0.
3. `go test -run TestPersistence_ ./internal/session/... -race -count=1 -v` runs exactly 4 tests. On Linux+systemd: 1 PASS (TEST-02), 2 FAIL (TEST-01 with "TEST-01 RED:", TEST-03 with "TEST-03 RED:"), 1 SKIP (TEST-04). On macOS: all 4 SKIP via requireSystemdRun or the macOS-only branch.
4. `tmux list-sessions 2>/dev/null | grep -c 'agentdeck-test-persist-'` returns 0 after the suite.
5. `systemctl --user list-units --type=scope --no-legend 2>/dev/null | grep -c 'fake-login-\\|agentdeck-tmux-.*test'` returns 0 after the suite.
6. `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces no output.
7. Every `tmux kill-server` invocation in the test file is scoped by `-L <serverName>` or `-t <name>` (grep-verified).
</verification>

<success_criteria>
1. TEST-01 exists and fails RED on Linux+systemd with diagnostic "TEST-01 RED: GetLaunchInUserScope() default is false".
2. TEST-02 exists and passes on Linux+systemd (inverse pin green).
3. Both skip cleanly on macOS/non-systemd hosts with "no systemd-run available:" in the output.
4. `go vet` exits 0.
5. No stray `agentdeck-test-persist-*` tmux servers or `fake-login-*` systemd scopes remain after the suite runs.
6. No production code modified.
7. All tmux kill-server calls use `-L <socket>` or `-t <name>` — never bare `kill-server`.
</success_criteria>

<output>
After completion, create `.planning/phases/01-persistence-test-scaffolding-red/01-02-SUMMARY.md`.

Commit files with:
```
git add internal/session/session_persistence_test.go
git add -f .planning/phases/01-persistence-test-scaffolding-red/01-02-SUMMARY.md
git commit -m "test(persistence): add TEST-01 RED and TEST-02 inverse pin"
```
(`-f` required because `.git/info/exclude` blocks `.planning/`. No Claude attribution.)
</output>
