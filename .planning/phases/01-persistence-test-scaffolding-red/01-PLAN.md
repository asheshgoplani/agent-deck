---
phase: 01-persistence-test-scaffolding-red
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - internal/session/session_persistence_test.go
autonomous: true
requirements:
  - TEST-03
  - TEST-04
user_setup: []

must_haves:
  truths:
    - "File internal/session/session_persistence_test.go exists and is in package session."
    - "The file compiles as part of `go test -run TestPersistence_ ./internal/session/... -race -count=1` on both Linux+systemd and macOS/non-systemd hosts."
    - "Shared helpers exist in the file: (a) uniqueTmuxServerName(t) producing `agentdeck-test-persist-<random-hex>`, (b) requireSystemdRun(t) that t.Skipf's with 'no systemd-run available: %v' if absent, (c) writeStubClaudeBinary(t, dir) that drops an executable script echoing argv to `$AGENTDECK_TEST_ARGV_LOG`, (d) isolatedHomeDir(t) that returns a temp HOME with ~/.agent-deck/ and ~/.claude/projects/ pre-created."
    - "TEST-03 (TestPersistence_LinuxDefaultIsUserScope) exists, runs on Linux+systemd, fails RED against current v1.5.1 (current default is false). On hosts without systemd-run it t.Skipf's cleanly."
    - "TEST-04 (TestPersistence_MacOSDefaultIsDirect) exists, passes immediately on any host lacking systemd-run, and documents in a header comment which Linux+systemd behavior it chose (skip vs assert-false-until-phase-2-adds-branch)."
    - "Every test cleans up tmux servers, transcripts, and sidecars via t.Cleanup(); no stray `agentdeck-test-*` tmux servers remain after the suite runs."
    - "No production code under internal/tmux/, internal/session/instance.go, internal/session/userconfig.go, internal/session/storage.go, or cmd/agent-deck/session_cmd.go is modified."
  artifacts:
    - path: "internal/session/session_persistence_test.go"
      provides: "Test-file skeleton with shared helpers + TEST-03 + TEST-04"
      contains: "package session, func TestPersistence_LinuxDefaultIsUserScope, func TestPersistence_MacOSDefaultIsDirect, func requireSystemdRun, func uniqueTmuxServerName, func writeStubClaudeBinary, func isolatedHomeDir"
      min_lines: 180
  key_links:
    - from: "internal/session/session_persistence_test.go"
      to: "internal/session/userconfig.go::GetTmuxSettings / TmuxSettings.GetLaunchInUserScope"
      via: "direct function call in TEST-03 / TEST-04"
      pattern: "settings\\.GetLaunchInUserScope\\(\\)"
    - from: "internal/session/session_persistence_test.go"
      to: "exec.LookPath(\"systemd-run\") / exec.Command(\"systemd-run\", \"--user\", \"--version\")"
      via: "requireSystemdRun helper, used at the top of every systemd-dependent test"
      pattern: "t\\.Skipf\\(\"no systemd-run available"
---

<objective>
Create `internal/session/session_persistence_test.go` as the single test file that will hold all eight `TestPersistence_*` regression tests. Land the shared helpers and the two simplest tests (TEST-03, TEST-04) in this plan so Plans 02 and 03 can append their tests to a file that already compiles and runs.

Purpose: RED-state TDD scaffolding for the 2026-04-14 session-persistence incident. No production code changes. TEST-03 must FAIL against current v1.5.1 on Linux+systemd; TEST-04 must PASS on any host lacking `systemd-run`. Skip semantics must be non-vacuous (`t.Skipf("no systemd-run available: %v", err)`).

Output: A compilable Go test file with helpers + 2 tests. Subsequent plans will add 6 more tests to the same file.
</objective>

<execution_context>
@<repo-root>/.worktrees/session-persistence/.claude/get-shit-done/workflows/execute-plan.md
@<repo-root>/.worktrees/session-persistence/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/PROJECT.md
@.planning/ROADMAP.md
@.planning/STATE.md
@.planning/REQUIREMENTS.md
@.planning/phases/01-persistence-test-scaffolding-red/01-CONTEXT.md
@docs/SESSION-PERSISTENCE-SPEC.md
@CLAUDE.md

# Production-code API surface (read-only in this phase)
@internal/session/userconfig.go
@internal/session/userconfig_test.go
@internal/tmux/tmux_test.go

<interfaces>
<!-- Extracted from repo. Executor should use these directly. -->

From internal/session/userconfig.go (lines 866–912):
```go
type TmuxSettings struct {
    // … other fields …
    LaunchInUserScope bool `toml:"launch_in_user_scope"`
}

func (t TmuxSettings) GetLaunchInUserScope() bool { return t.LaunchInUserScope }

func GetTmuxSettings() TmuxSettings  // reads ~/.agent-deck/config.toml, cached; ClearUserConfigCache() invalidates
func ClearUserConfigCache()
```

From internal/session/userconfig_test.go (lines 1085–1131) — reference pattern the two new tests mirror:
```go
// Sets HOME to t.TempDir(), writes ~/.agent-deck/config.toml (possibly empty),
// calls ClearUserConfigCache(), then calls GetTmuxSettings().
```

From internal/tmux/tmux_test.go — reference for real-binary + skip-if-missing pattern.
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Create test file skeleton with package, imports, and shared helpers</name>
  <files>internal/session/session_persistence_test.go</files>
  <read_first>
    - internal/session/session_persistence_test.go (verify it does not exist yet; if it does, STOP and escalate)
    - .planning/phases/01-persistence-test-scaffolding-red/01-CONTEXT.md (locked decisions: file name, package, skip semantics, naming conventions)
    - internal/session/userconfig.go (TmuxSettings, GetTmuxSettings, GetLaunchInUserScope signatures)
    - internal/session/userconfig_test.go lines 1085–1131 (HOME-override + ClearUserConfigCache pattern)
    - internal/tmux/tmux_test.go lines 1–100 (package conventions, import style)
    - CLAUDE.md "Session persistence: mandatory test coverage" section (eight-test rule, tmux safety rule)
  </read_first>
  <behavior>
    - The created file MUST be parseable Go: `go vet ./internal/session/...` exits 0.
    - The file MUST be in `package session` (NOT `session_test`), so it can call unexported helpers like `ClearUserConfigCache()` if needed.
    - The file MUST define exactly these helpers (all unexported, all in the same file — not a separate _helpers.go):
      1. `uniqueTmuxServerName(t *testing.T) string` — returns `"agentdeck-test-persist-" + 8-hex-char random suffix`. Uses crypto/rand. Every call produces a distinct name. Registers a `t.Cleanup(func() { _ = exec.Command("tmux", "kill-server", "-t", name).Run() })` so the server is killed even on test failure. MUST NEVER call `tmux kill-server` without `-t <name>` (repo CLAUDE.md mandate).
      2. `requireSystemdRun(t *testing.T)` — runs `exec.Command("systemd-run", "--user", "--version").Run()`. If the binary is missing OR returns non-zero, calls `t.Skipf("no systemd-run available: %v", err)` and returns. Otherwise no-op. The skip message MUST contain the literal substring `"no systemd-run available:"`.
      3. `writeStubClaudeBinary(t *testing.T, dir string) string` — writes an executable shell script at `dir/claude` whose body is `#!/usr/bin/env bash\nprintf '%s\\n' "$@" >> "${AGENTDECK_TEST_ARGV_LOG:-/dev/null}"\nsleep 30\n`. Returns `dir` so the caller can `PATH=dir:$PATH`. chmod 0755. t.Cleanup removes the file.
      4. `isolatedHomeDir(t *testing.T) string` — creates `t.TempDir()`, mkdirs `<tmp>/.agent-deck`, `<tmp>/.claude/projects`, `<tmp>/.agent-deck/hooks`, sets `t.Setenv("HOME", tmp)`, calls `ClearUserConfigCache()`, and registers `t.Cleanup(func(){ ClearUserConfigCache() })`. Returns the temp path.
    - Top of the file MUST have a package-level comment block explaining: purpose of the suite (regression tests for the 2026-04-14 incident), mandated by CLAUDE.md, MUST NOT be deleted without RFC, lists the eight test names.
    - No test functions are added in this task — only the file skeleton + helpers. Plans 02/03 append tests in later tasks.
    - `go build ./internal/session/...` MUST succeed. `go vet ./internal/session/...` MUST exit 0.
    - `go test -run TestPersistence_ ./internal/session/... -count=1` MUST exit 0 with output "testing: warning: no tests to run" (expected at this point since the skeleton has no tests yet — the next task adds TEST-03 and TEST-04).
  </behavior>
  <action>
    Create `internal/session/session_persistence_test.go` with:

    1. Package clause: `package session`
    2. Header comment (20–30 lines) explaining:
       - Name: "Session persistence regression test suite"
       - Reason: 2026-04-14 incident — SSH logout destroyed 33 Claude conversations on the conductor host (3rd recurrence of the same bug class)
       - Mandate: repo CLAUDE.md "Session persistence: mandatory test coverage" section requires all eight TestPersistence_* tests to exist and gate every PR touching internal/tmux/, internal/session/instance.go, internal/session/userconfig.go, internal/session/storage.go, or cmd/agent-deck/session_cmd.go.
       - The eight test names (verbatim list, no abbreviation): TestPersistence_TmuxSurvivesLoginSessionRemoval, TestPersistence_TmuxDiesWithoutUserScope, TestPersistence_LinuxDefaultIsUserScope, TestPersistence_MacOSDefaultIsDirect, TestPersistence_RestartResumesConversation, TestPersistence_StartAfterSIGKILLResumesConversation, TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion, TestPersistence_FreshSessionUsesSessionIDNotResume.
       - Safety note: tmux server names MUST use the `agentdeck-test-persist-` prefix; NEVER call `tmux kill-server` without `-t <name>` (2025-12-10 incident killed 40 user sessions).
    3. Imports: `crypto/rand`, `encoding/hex`, `fmt`, `os`, `os/exec`, `path/filepath`, `testing`.
    4. Helper #1 — `uniqueTmuxServerName(t *testing.T) string`:
       ```go
       func uniqueTmuxServerName(t *testing.T) string {
           t.Helper()
           var b [4]byte
           if _, err := rand.Read(b[:]); err != nil {
               t.Fatalf("uniqueTmuxServerName: rand.Read: %v", err)
           }
           name := "agentdeck-test-persist-" + hex.EncodeToString(b[:])
           t.Cleanup(func() {
               // Safety: ONLY kill the server we created. Never run bare `tmux kill-server`.
               _ = exec.Command("tmux", "kill-server", "-t", name).Run()
           })
           return name
       }
       ```
    5. Helper #2 — `requireSystemdRun(t *testing.T)`:
       ```go
       func requireSystemdRun(t *testing.T) {
           t.Helper()
           if _, err := exec.LookPath("systemd-run"); err != nil {
               t.Skipf("no systemd-run available: %v", err)
               return
           }
           if err := exec.Command("systemd-run", "--user", "--version").Run(); err != nil {
               t.Skipf("no systemd-run available: %v", err)
           }
       }
       ```
    6. Helper #3 — `writeStubClaudeBinary(t *testing.T, dir string) string`:
       ```go
       func writeStubClaudeBinary(t *testing.T, dir string) string {
           t.Helper()
           script := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" >> \"${AGENTDECK_TEST_ARGV_LOG:-/dev/null}\"\nsleep 30\n"
           path := filepath.Join(dir, "claude")
           if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
               t.Fatalf("writeStubClaudeBinary: %v", err)
           }
           t.Cleanup(func() { _ = os.Remove(path) })
           return dir
       }
       ```
    7. Helper #4 — `isolatedHomeDir(t *testing.T) string`:
       ```go
       func isolatedHomeDir(t *testing.T) string {
           t.Helper()
           home := t.TempDir()
           for _, sub := range []string{".agent-deck", ".agent-deck/hooks", ".claude/projects"} {
               if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
                   t.Fatalf("isolatedHomeDir mkdir %s: %v", sub, err)
               }
           }
           t.Setenv("HOME", home)
           ClearUserConfigCache()
           t.Cleanup(func() { ClearUserConfigCache() })
           return home
       }
       ```
    8. DO NOT add any `TestPersistence_*` functions in this task — Task 2 adds them.
    9. After writing the file, run `go vet ./internal/session/...` and `go test -run TestPersistence_ ./internal/session/... -count=1`. Both must exit 0.
  </action>
  <verify>
    <automated>test -f internal/session/session_persistence_test.go &amp;&amp; grep -q "^package session$" internal/session/session_persistence_test.go &amp;&amp; grep -q "func uniqueTmuxServerName" internal/session/session_persistence_test.go &amp;&amp; grep -q "func requireSystemdRun" internal/session/session_persistence_test.go &amp;&amp; grep -q "func writeStubClaudeBinary" internal/session/session_persistence_test.go &amp;&amp; grep -q "func isolatedHomeDir" internal/session/session_persistence_test.go &amp;&amp; grep -q "no systemd-run available:" internal/session/session_persistence_test.go &amp;&amp; grep -q "agentdeck-test-persist-" internal/session/session_persistence_test.go &amp;&amp; go vet ./internal/session/... &amp;&amp; go test -run TestPersistence_ ./internal/session/... -count=1</automated>
  </verify>
  <acceptance_criteria>
    - `test -f internal/session/session_persistence_test.go` returns 0
    - `grep -q "^package session$" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func uniqueTmuxServerName" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func requireSystemdRun" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func writeStubClaudeBinary" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func isolatedHomeDir" internal/session/session_persistence_test.go` returns 0
    - `grep -q "no systemd-run available:" internal/session/session_persistence_test.go` returns 0
    - `grep -q "agentdeck-test-persist-" internal/session/session_persistence_test.go` returns 0
    - `grep -q "tmux kill-server\", \"-t\"" internal/session/session_persistence_test.go` returns 0 (proves the `-t <name>` filter is used)
    - `grep -c "TestPersistence_TmuxSurvivesLoginSessionRemoval\\|TestPersistence_TmuxDiesWithoutUserScope\\|TestPersistence_LinuxDefaultIsUserScope\\|TestPersistence_MacOSDefaultIsDirect\\|TestPersistence_RestartResumesConversation\\|TestPersistence_StartAfterSIGKILLResumesConversation\\|TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion\\|TestPersistence_FreshSessionUsesSessionIDNotResume" internal/session/session_persistence_test.go` returns 8 or more (the eight names appear at least once — in the header comment)
    - `go vet ./internal/session/...` exits 0
    - `go test -run TestPersistence_ ./internal/session/... -count=1` exits 0 (no tests to run yet is acceptable)
    - `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces NO output (no production-code modifications)
  </acceptance_criteria>
  <done>Test file exists in package session with all four helpers, header comment lists the eight test names verbatim, `go vet` and `go test -run TestPersistence_ ./internal/session/...` both pass, no production code modified.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: Add TEST-03 (LinuxDefaultIsUserScope) and TEST-04 (MacOSDefaultIsDirect)</name>
  <files>internal/session/session_persistence_test.go</files>
  <read_first>
    - internal/session/session_persistence_test.go (the skeleton from Task 1 — helpers are defined)
    - internal/session/userconfig.go lines 858–912 (TmuxSettings struct + GetLaunchInUserScope accessor)
    - internal/session/userconfig_test.go lines 1085–1131 (the existing TestGetTmuxSettings_LaunchInUserScope_Default at :1102 currently pins `false` — TEST-03 is its aspirational counterpart that will turn green in Phase 2)
    - docs/SESSION-PERSISTENCE-SPEC.md REQ-3 items 3 and 4 (exact semantics)
    - .planning/phases/01-persistence-test-scaffolding-red/01-CONTEXT.md decisions section (RED state expectations: TEST-03 FAIL on Linux+systemd, TEST-04 PASS on macOS + implementer-chosen behavior on Linux+systemd documented in header comment)
  </read_first>
  <behavior>
    - TEST-03 (`TestPersistence_LinuxDefaultIsUserScope`):
      - Calls `requireSystemdRun(t)` first — skips on hosts without systemd-run.
      - Calls `isolatedHomeDir(t)` to get a clean HOME with no config file. Writes an empty `~/.agent-deck/config.toml`. Calls `ClearUserConfigCache()`.
      - Calls `settings := GetTmuxSettings()` and asserts `settings.GetLaunchInUserScope() == true`.
      - Failure message: `t.Fatalf("TEST-03 RED: GetLaunchInUserScope() returned false on a Linux+systemd host with no config; expected true. Phase 2 must flip the default. systemd-run present, no config override.")`.
      - On current v1.5.1 this test FAILS RED with that exact diagnostic message because the default is hardcoded `false`.
      - On macOS/no-systemd it t.Skipf's cleanly via requireSystemdRun.
    - TEST-04 (`TestPersistence_MacOSDefaultIsDirect`):
      - Header comment on the function documents the implementer's choice for Linux+systemd hosts: "On Linux+systemd hosts, this test skips with t.Skipf because systemd-run IS available and the Linux branch is covered by TEST-03. The test's assertion body executes only on hosts where systemd-run is absent (macOS/BSD/minimal-Linux)."
      - Probes `exec.LookPath("systemd-run")`. If systemd-run is FOUND, calls `t.Skipf("systemd-run available; TEST-04 only asserts non-systemd behavior — see TEST-03 for Linux+systemd default: %v", nil)`. If NOT found, proceeds to the assertion.
      - Calls `isolatedHomeDir(t)` + empty `config.toml` + `ClearUserConfigCache()`.
      - Asserts `settings.GetLaunchInUserScope() == false` with failure message `t.Fatalf("TEST-04: on a host without systemd-run, GetLaunchInUserScope() must return false, got true")`.
      - On macOS CI (no systemd-run): PASSES.
      - On Linux+systemd: SKIPS cleanly (documented choice).
    - Both tests MUST run as part of `go test -run TestPersistence_ ./internal/session/... -race -count=1`.
    - No production code changes.
  </behavior>
  <action>
    Append two test functions to `internal/session/session_persistence_test.go` (the file created in Task 1):

    1. `TestPersistence_LinuxDefaultIsUserScope(t *testing.T)`:
       ```go
       // TestPersistence_LinuxDefaultIsUserScope pins REQ-1: on a Linux host where
       // systemd-run is available and no config.toml overrides it, the default
       // MUST be launch_in_user_scope=true. Phase 2 will flip the default; this
       // test is RED against current v1.5.1.
       func TestPersistence_LinuxDefaultIsUserScope(t *testing.T) {
           requireSystemdRun(t)
           home := isolatedHomeDir(t)
           // Write empty config so GetTmuxSettings() exercises the default branch.
           cfg := filepath.Join(home, ".agent-deck", "config.toml")
           if err := os.WriteFile(cfg, []byte(""), 0o644); err != nil {
               t.Fatalf("write empty config: %v", err)
           }
           ClearUserConfigCache()

           settings := GetTmuxSettings()
           if !settings.GetLaunchInUserScope() {
               t.Fatalf("TEST-03 RED: GetLaunchInUserScope() returned false on a Linux+systemd host with no config; expected true. Phase 2 must flip the default. systemd-run present, no config override.")
           }
       }
       ```

    2. `TestPersistence_MacOSDefaultIsDirect(t *testing.T)`:
       ```go
       // TestPersistence_MacOSDefaultIsDirect pins REQ-1: on a host WITHOUT
       // systemd-run (macOS, BSD, minimal Linux), the default MUST remain false
       // and no error is logged.
       //
       // Linux+systemd behavior (documented implementer choice, 2026-04-14):
       // this test SKIPS on hosts where systemd-run is available. TEST-03 covers
       // the Linux+systemd default. TEST-04's assertion body only runs on hosts
       // where systemd-run is absent. Rationale: GetTmuxSettings() in Phase 2
       // will detect systemd-run at call time; asserting "false on Linux+systemd"
       // here would lock in a bug.
       func TestPersistence_MacOSDefaultIsDirect(t *testing.T) {
           if _, err := exec.LookPath("systemd-run"); err == nil {
               t.Skipf("systemd-run available; TEST-04 only asserts non-systemd behavior — see TEST-03 for Linux+systemd default")
               return
           }
           home := isolatedHomeDir(t)
           cfg := filepath.Join(home, ".agent-deck", "config.toml")
           if err := os.WriteFile(cfg, []byte(""), 0o644); err != nil {
               t.Fatalf("write empty config: %v", err)
           }
           ClearUserConfigCache()

           settings := GetTmuxSettings()
           if settings.GetLaunchInUserScope() {
               t.Fatalf("TEST-04: on a host without systemd-run, GetLaunchInUserScope() must return false, got true")
           }
       }
       ```

    Verification steps:
    - Run `go vet ./internal/session/...` — must exit 0.
    - Run `go test -run TestPersistence_LinuxDefaultIsUserScope ./internal/session/... -count=1 -v`:
      - On Linux+systemd: MUST FAIL (non-zero exit) with the "TEST-03 RED:" message in the output.
      - On macOS: MUST be skipped with "no systemd-run available:" in the output.
    - Run `go test -run TestPersistence_MacOSDefaultIsDirect ./internal/session/... -count=1 -v`:
      - On Linux+systemd: MUST skip with the "TEST-04 only asserts non-systemd behavior" message.
      - On macOS/no-systemd: MUST PASS (exit 0, line contains "PASS: TestPersistence_MacOSDefaultIsDirect").
    - `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` must produce no output.
  </action>
  <verify>
    <automated>grep -q "func TestPersistence_LinuxDefaultIsUserScope" internal/session/session_persistence_test.go &amp;&amp; grep -q "func TestPersistence_MacOSDefaultIsDirect" internal/session/session_persistence_test.go &amp;&amp; grep -q "TEST-03 RED:" internal/session/session_persistence_test.go &amp;&amp; go vet ./internal/session/... &amp;&amp; go test -run "TestPersistence_MacOSDefaultIsDirect|TestPersistence_LinuxDefaultIsUserScope" ./internal/session/... -count=1 -v 2>&amp;1 | tee /tmp/test03_04.out; (grep -q "^--- FAIL: TestPersistence_LinuxDefaultIsUserScope\\|^--- SKIP: TestPersistence_LinuxDefaultIsUserScope" /tmp/test03_04.out &amp;&amp; grep -q "^--- PASS: TestPersistence_MacOSDefaultIsDirect\\|^--- SKIP: TestPersistence_MacOSDefaultIsDirect" /tmp/test03_04.out)</automated>
  </verify>
  <acceptance_criteria>
    - `grep -q "func TestPersistence_LinuxDefaultIsUserScope" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func TestPersistence_MacOSDefaultIsDirect" internal/session/session_persistence_test.go` returns 0
    - `grep -q "TEST-03 RED:" internal/session/session_persistence_test.go` returns 0 (diagnostic message present)
    - `grep -q "documented implementer choice" internal/session/session_persistence_test.go` returns 0 (TEST-04 header comment documents Linux+systemd choice)
    - `go vet ./internal/session/...` exits 0
    - On Linux+systemd: `go test -run TestPersistence_LinuxDefaultIsUserScope ./internal/session/... -count=1` exits NON-ZERO and output contains "TEST-03 RED:"
    - On macOS/no-systemd: `go test -run TestPersistence_LinuxDefaultIsUserScope ./internal/session/... -count=1 -v` output contains "--- SKIP" and "no systemd-run available:"
    - On Linux+systemd: `go test -run TestPersistence_MacOSDefaultIsDirect ./internal/session/... -count=1 -v` output contains "--- SKIP" and "only asserts non-systemd behavior"
    - On macOS/no-systemd: `go test -run TestPersistence_MacOSDefaultIsDirect ./internal/session/... -count=1` exits 0
    - `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces NO output
  </acceptance_criteria>
  <done>Both tests exist in the test file; TEST-03 fails RED on Linux+systemd with diagnostic message "TEST-03 RED:", TEST-04 passes on macOS / skips cleanly on Linux+systemd; `go vet` passes; no production code modified.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| test process → tmux binary | Real `tmux` is invoked via `exec.Command`; tests MUST use unique server names prefixed `agentdeck-test-persist-` so they never touch user sessions. |
| test process → systemd user manager | Real `systemd-run --user` is invoked only as a capability probe in this plan (`systemd-run --user --version`). No scopes are created in Plan 01. |

## STRIDE Threat Register

No production code modified; threat surface unchanged. Tests run with real `tmux` and `systemd-run` binaries; tests MUST use unique tmux server names with the `agentdeck-test-*` prefix and MUST NOT call `tmux kill-server` without a `-t <name>` filter (per repo CLAUDE.md tmux safety mandate, after the 2025-12-10 incident that killed 40 user sessions).

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-01-01 | Denial of Service | helper `uniqueTmuxServerName` t.Cleanup | mitigate | Every `tmux kill-server` call uses `-t <name>` with the `agentdeck-test-persist-` prefix. Grep-checked in acceptance criteria. |
| T-01-02 | Tampering | helper `isolatedHomeDir` `t.Setenv("HOME", ...)` | mitigate | `t.Setenv` restores the prior value on test cleanup; `ClearUserConfigCache()` is called in t.Cleanup so config state doesn't leak to adjacent tests. |
| T-01-03 | Information Disclosure | stub claude binary argv log | accept | Logs only spawned-command argv; no user secrets. Confined to `t.TempDir()` which is auto-removed. |
</threat_model>

<verification>
Plan-level verification (run after both tasks complete):

1. `go vet ./internal/session/...` exits 0.
2. `go build ./...` exits 0.
3. `go test -run TestPersistence_ ./internal/session/... -race -count=1 -v` runs exactly 2 tests. On Linux+systemd: 1 FAIL (TEST-03), 1 SKIP (TEST-04). On macOS: 1 SKIP (TEST-03 via requireSystemdRun), 1 PASS (TEST-04).
4. `tmux list-sessions 2>&1 | grep "agentdeck-test-persist-" | wc -l` returns 0 after the suite runs (no stray servers — this plan never creates a tmux server, but the invariant must hold).
5. `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces no output.
6. The file MUST end with a trailing newline (`tail -c1 internal/session/session_persistence_test.go | od -c | head -1 | grep -q '\\n'`).
</verification>

<success_criteria>
1. `internal/session/session_persistence_test.go` exists in package `session`.
2. The four shared helpers (`uniqueTmuxServerName`, `requireSystemdRun`, `writeStubClaudeBinary`, `isolatedHomeDir`) are defined once and used by TEST-03 / TEST-04.
3. TEST-03 fails RED on Linux+systemd with message containing "TEST-03 RED:"; skips cleanly on macOS.
4. TEST-04 passes on macOS; skips cleanly on Linux+systemd with a message explaining the documented implementer choice.
5. `go vet ./internal/session/...` exits 0.
6. No production code files are modified (see verification step 5).
7. All tmux-kill calls in the test file include a `-t <name>` filter (grep-verified).
</success_criteria>

<output>
After completion, create `.planning/phases/01-persistence-test-scaffolding-red/01-01-SUMMARY.md`.

Commit files with: `git add internal/session/session_persistence_test.go && git add -f .planning/phases/01-persistence-test-scaffolding-red/01-01-SUMMARY.md && git commit -m "test(persistence): add TEST-03, TEST-04 and shared helpers (RED)"`

(`.git/info/exclude` blocks `.planning/` — the `-f` flag on `git add` is required. No Claude attribution in the commit.)
</output>
