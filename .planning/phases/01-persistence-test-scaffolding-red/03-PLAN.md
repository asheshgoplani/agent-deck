---
phase: 01-persistence-test-scaffolding-red
plan: 03
type: execute
wave: 3
depends_on:
  - "01-02"
files_modified:
  - internal/session/session_persistence_test.go
autonomous: true
requirements:
  - TEST-05
  - TEST-06
  - TEST-07
  - TEST-08
user_setup: []

must_haves:
  truths:
    - "TestPersistence_RestartResumesConversation exists, drives the real Restart() dispatch path on a live tmux session whose PATH is prepended with the stub claude binary. Captures the spawned claude argv and asserts --resume <ClaudeSessionID> is present. FAIL-or-PASS on current v1.5.1 is documented in the test header (current code routes Restart() through buildClaudeResumeCommand, so this test passes as a regression guard; the actual RED state lives in TEST-06)."
    - "TestPersistence_StartAfterSIGKILLResumesConversation exists, drives the real Start() dispatch path (which is the CLI session start handler's entry point per cmd/agent-deck/session_cmd.go:188) on an Instance whose Status has been set to StatusError. Asserts the captured claude argv contains --resume <ClaudeSessionID>. FAILS RED on current v1.5.1 because Start() calls buildClaudeCommand() (instance.go:1883), which goes through the capture-resume pattern (instance.go:559+) that generates a NEW UUID — NOT --resume <existing id>. This is the 2026-04-14 incident's REQ-2 root cause."
    - "TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion exists, writes a hook sidecar at ~/.agent-deck/hooks/<id>.sid, deletes it, then drives the real Start() dispatch path and asserts the captured argv still references the ClaudeSessionID from instance JSON storage. FAILS RED because Start() itself does not resume (same root cause as TEST-06); independently guards the invariant from docs/session-id-lifecycle.md that instance JSON is authoritative."
    - "TestPersistence_FreshSessionUsesSessionIDNotResume exists, asserts that buildClaudeResumeCommand() on a fresh Instance with no JSONL transcript uses --session-id (not --resume). Header documents the CONTEXT.md FAIL-or-PASS qualifier: current v1.5.1 code at instance.go:4150 routes this correctly via sessionHasConversationData, so the test PASSES as a regression guard. The unambiguous failure message (not a nil-pointer panic) protects against future regression."
    - "All four tests use synthetic JSONL transcripts written to ~/.claude/projects/<ConvertToClaudeDirName(path)>/<id>.jsonl under the isolated HOME (no real user files touched)."
    - "TEST-05, TEST-06, TEST-07 drive the dispatch by spawning a REAL tmux session (with a unique -L socket from uniqueTmuxServerName) running the stub claude binary from writeStubClaudeBinary. Capture argv via the AGENTDECK_TEST_ARGV_LOG file and grep for --resume / --session-id."
    - "All tests use isolatedHomeDir(t) so HOME is redirected to t.TempDir(); tmux kill-server is always scoped by -L <unique-socket> or -t <name>; no bare tmux kill-server."
    - "No production code under internal/tmux/, internal/session/instance.go, internal/session/userconfig.go, internal/session/storage.go, or cmd/agent-deck/session_cmd.go is modified."
  artifacts:
    - path: "internal/session/session_persistence_test.go"
      provides: "TEST-05 through TEST-08 appended; all 8 tests now present; shared resume-dispatch helpers captureClaudeArgv, runStartAndCaptureArgv, runRestartAndCaptureArgv"
      contains: "func TestPersistence_RestartResumesConversation, func TestPersistence_StartAfterSIGKILLResumesConversation, func TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion, func TestPersistence_FreshSessionUsesSessionIDNotResume"
  key_links:
    - from: "internal/session/session_persistence_test.go::TestPersistence_StartAfterSIGKILLResumesConversation"
      to: "internal/session/instance.go::(*Instance).Start (line 1873) -> buildClaudeCommand (line 477)"
      via: "real tmux spawn with stub claude on PATH; captures argv via AGENTDECK_TEST_ARGV_LOG"
      pattern: "inst\\.Start\\(\\).*AGENTDECK_TEST_ARGV_LOG"
    - from: "internal/session/session_persistence_test.go::TestPersistence_RestartResumesConversation"
      to: "internal/session/instance.go::(*Instance).Restart (line 3763) -> buildClaudeResumeCommand (line 4114)"
      via: "real tmux spawn + Restart() respawn-pane; captures argv via AGENTDECK_TEST_ARGV_LOG"
      pattern: "inst\\.Restart\\(\\).*AGENTDECK_TEST_ARGV_LOG"
    - from: "synthetic JSONL transcripts"
      to: "~/.claude/projects/<ConvertToClaudeDirName(path)>/<id>.jsonl"
      via: "os.WriteFile under isolated HOME"
      pattern: "\\.claude/projects/"
---

<objective>
Append the four resume-routing tests (TEST-05, TEST-06, TEST-07, TEST-08) to `internal/session/session_persistence_test.go`, completing the eight-test suite. These tests pin REQ-2: every dispatch path that starts a Claude session with a non-empty `ClaudeSessionID` and resumable conversation data MUST produce a `claude --resume <id>` command line; a fresh session (no conversation data yet) MUST produce `claude --session-id <id>`; and `ClaudeSessionID` must be sourced from instance JSON storage regardless of hook sidecar state.

Purpose: pin the REAL dispatch paths, not the internal helper in isolation. The 2026-04-14 incident's REQ-2 bug is that `(*Instance).Start()` — the function invoked by `agent-deck session start` and by the error-recovery flow after a SIGKILL — calls `buildClaudeCommand()` (instance.go:1883), which runs through the capture-resume pattern (instance.go:559+) that mints a NEW UUID. It does NOT route through `buildClaudeResumeCommand()` when `ClaudeSessionID != ""` and JSONL transcript exists. `Restart()` is the only path that currently routes correctly (instance.go:3789). The four tests in this plan exercise the ACTUAL dispatch by spawning a real tmux session with a stub claude binary on PATH, capturing the spawned argv, and asserting on `--resume` vs `--session-id`.

Output: The test file contains all 8 `TestPersistence_*` tests. Full suite run on Linux+systemd with current v1.5.1: TEST-02 PASS, TEST-04 SKIP, TEST-01/03 FAIL, TEST-06 FAIL (the real RED), TEST-07 FAIL (same root cause via Start() path), TEST-05 PASS (Restart() already resumes), TEST-08 PASS (regression guard). On macOS: systemd-dependent tests SKIP; resume tests that need real tmux also SKIP if tmux is missing (documented via a `requireTmux(t)` helper).
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
@.planning/phases/01-persistence-test-scaffolding-red/01-02-SUMMARY.md
@docs/SESSION-PERSISTENCE-SPEC.md
@docs/session-id-lifecycle.md
@CLAUDE.md

# Production-code API surface (read-only in this phase)
@internal/session/instance.go
@internal/session/storage.go
@internal/session/hook_session_anchor.go
@cmd/agent-deck/session_cmd.go
@internal/tmux/tmux.go

<dispatch_path_analysis>
<!-- MANDATORY INVESTIGATION RESULT — do not skip. Executor and reviewer both rely on this. -->
<!-- Read this block before writing or debugging TEST-05/06/07. -->

### Finding: Start() and Restart() take DIFFERENT dispatch paths for Claude sessions.

**Entry points inspected** (head files read: internal/session/instance.go, cmd/agent-deck/session_cmd.go, internal/session/hook_session_anchor.go):

1. **`agent-deck session start <id>` CLI handler** → `cmd/agent-deck/session_cmd.go:118 handleSessionStart` → at line 188 calls `inst.Start()` (or `inst.StartWithMessage(initialMessage)` at line 183 if `-m` flag).
2. **`(*Instance).Start()` at `internal/session/instance.go:1873`** → at line 1882-1883 switches on `IsClaudeCompatible(i.Tool)` → calls **`i.buildClaudeCommand(i.Command)`** (NOT `buildClaudeResumeCommand()`).
3. **`(*Instance).buildClaudeCommand(baseCommand)` at `internal/session/instance.go:477`** → delegates to `buildClaudeCommandWithMessage(baseCommand, "")` at line 485.
4. **`(*Instance).buildClaudeCommandWithMessage` at line 485** → when `baseCommand == "claude"` (the default) and `opts.SessionMode == ""` (default — NOT "resume"), falls through the switch at line 525 to the default branch at line 550-570: the **capture-resume pattern**. This pattern pre-generates a NEW UUID via `generateUUID()` (line 566) and spawns claude with `--session-id <NEW_UUID>`. It does NOT consult `i.ClaudeSessionID`. It does NOT call `sessionHasConversationData()`. It does NOT produce `--resume <id>`.

**This is the REQ-2 bug.** When a user runs `agent-deck session start` on an Instance whose `ClaudeSessionID` is already populated (from a prior run that got SIGKILLed, or from the 2026-04-14 SSH-logout incident where tmux died), `Start()` spawns a brand-new claude conversation with a brand-new UUID. The prior conversation is abandoned even though its JSONL transcript is intact on disk.

**The bypass happens in the dispatch path, not in the helper.**

5. **`(*Instance).Restart()` at `internal/session/instance.go:3763`** → at line 3787-3789:
   ```go
   if IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != "" && i.tmuxSession != nil && i.tmuxSession.Exists() {
       resumeCmd, containerName, err := i.prepareCommand(i.buildClaudeResumeCommand())
       ...
       i.tmuxSession.RespawnPane(resumeCmd)
   ```
   When `tmuxSession` exists and `ClaudeSessionID != ""`, Restart() correctly calls `buildClaudeResumeCommand()` and uses `RespawnPane` (atomic restart).

6. **Fallback path in Restart()** (when `tmuxSession` doesn't exist — "dead session" recovery) at line 4011-4019: also calls `buildClaudeResumeCommand()` when `ClaudeSessionID != ""`. This path mints a NEW tmux session (line 4012 `tmux.NewSession(...)`) and calls `i.tmuxSession.Start(command)` at line 4069.

**So Restart() routes correctly through resume in BOTH branches.** The only dispatch path that bypasses resume is `Start()`.

7. **`(*Instance).buildClaudeResumeCommand()` at line 4114** → line 4150 calls `sessionHasConversationData(i.ClaudeSessionID, i.ProjectPath)`:
   - If JSONL transcript exists under `~/.claude/projects/<ConvertToClaudeDirName(i.ProjectPath)>/<i.ClaudeSessionID>.jsonl` (and has real user/assistant content): returns `... claude --resume <id> ...`
   - Otherwise: returns `... claude --session-id <id> ...`
   Function is correct in isolation — this is why asserting on the function directly (the v1 Plan 03 design) cannot produce RED.

8. **Hook sidecar** (`~/.agent-deck/hooks/<instance-id>.sid`) is read ONLY at `internal/session/instance.go:2626` inside `updateSessionIDFromHook` (a hook-event handler), as a fallback when the live hook payload's `session_id` is empty. It is NOT consulted by `Start()` or `Restart()`. `i.ClaudeSessionID` is loaded from instance JSON storage (`internal/session/storage.go` — `SaveInstance`/`LoadInstance`). **Confirmed: sidecar deletion does not affect the dispatch-time `ClaudeSessionID` value.** TEST-07's RED state therefore comes from the SAME root cause as TEST-06 (Start() bypasses resume), not from sidecar reading.

### Test design implication

The RED state for REQ-2 lives in the ARGV PRODUCED BY `Start()`, observable only by capturing the real spawned claude command line. To capture it, tests:

1. Prepend a temp dir containing a stub `claude` script to `PATH` (using `t.Setenv("PATH", ...)`).
2. Set `t.Setenv("AGENTDECK_TEST_ARGV_LOG", <temp-file>)` so the stub writes its argv to a known file.
3. Construct an `*Instance` via `NewInstanceWithTool(title, projectPath, "claude")`, set `ClaudeSessionID`, and write a synthetic JSONL transcript.
4. Call `inst.Start()` (for TEST-06 / TEST-07) or `inst.Restart()` (for TEST-05). The real tmux session is spawned; inside it, the shell runs `claude ...` which resolves to the stub; the stub writes argv to the log file and `sleep 30` keeps the pane alive.
5. Poll the argv log file (up to 3s) for a non-empty first line.
6. `grep` the argv for `--resume <id>` vs `--session-id <id>` and assert.
7. `t.Cleanup` runs `inst.tmuxSession.Kill()` (scoped to the unique session name — SAFE because tmux session names are unique per call via the `NewSession` prefix).

TEST-08 remains a pure-Go assertion on `buildClaudeResumeCommand()` because the contract being pinned is the function-level `useResume == false` branch; no dispatch-path involvement.

### Why NOT use handleSessionStart directly

Tests call `inst.Start()` rather than `handleSessionStart()` because:
- `handleSessionStart` is in package `main` (cmd/agent-deck), unreachable from `package session` tests.
- `handleSessionStart` additionally reads/writes instance JSON via `loadSessionData`/`saveSessionData` and calls `os.Exit` on error — not test-friendly.
- The CLI handler at line 188 calls `inst.Start()` directly with no adaptation; the dispatch logic is entirely inside `Start()`. Asserting on `Start()` is the same assertion minus the CLI wrapper.

### Why NOT mock anything

Per CONTEXT.md no-mocking rule: tests use real tmux and real shell spawn. The stub claude binary is allowed because CONTEXT.md explicitly carves it out ("claude itself can be a stub binary in the test's PATH because the test is asserting on the spawned command line, not on Claude's behavior"). Tmux binary is real; systemd-run (if used by Session.Start via LaunchInUserScope) is real.

### What this plan does NOT test (deferred to Phase 4 verify-session-persistence.sh)

- End-to-end behavior across an actual SSH logout + reconnect sequence.
- Integration with `handleSessionStart`/`handleSessionRestart` CLI handlers (covered by the shell script).
- Real claude process survival under real cgroup teardown (covered by TEST-01's simulated teardown in Plan 02).
</dispatch_path_analysis>

<interfaces>
<!-- Production-code API the four tests call. DO NOT modify in Phase 1. -->

From internal/session/instance.go:
```go
type Instance struct {
    ID              string
    Title           string
    ProjectPath     string
    Tool            string             // "claude", "codex", etc. IsClaudeCompatible(i.Tool) must be true for resume
    Status          Status             // StatusStopped, StatusError, StatusIdle, StatusWaiting, StatusStarting
    ClaudeSessionID string
    Command         string             // usually "claude" for Claude sessions
    tmuxSession     *tmux.Session      // unexported; NewInstanceWithTool initializes it
    // … many more fields (unexported)
}

// Status constants (confirmed via grep — verbatim names):
const (
    StatusWaiting  Status = "waiting"
    StatusIdle     Status = "idle"
    StatusError    Status = "error"
    StatusStarting Status = "starting"
    StatusStopped  Status = "stopped"
)

// Constructors (confirmed signatures):
func NewInstance(title, projectPath string) *Instance           // default Tool="shell"
func NewInstanceWithTool(title, projectPath, tool string) *Instance  // sets Tool=tool

// Dispatch entry points exercised by tests:
func (i *Instance) Start() error        // Start dispatch — calls buildClaudeCommand
func (i *Instance) Restart() error      // Restart dispatch — calls buildClaudeResumeCommand
func (i *Instance) Exists() bool        // wraps tmuxSession.Exists()

// Command builders (read for diagnostic only; tests do not call directly except TEST-08):
func (i *Instance) buildClaudeCommand(baseCommand string) string            // NEW-UUID capture pattern
func (i *Instance) buildClaudeResumeCommand() string                        // --resume or --session-id based on JSONL
func sessionHasConversationData(sessionID, projectPath string) bool
func ConvertToClaudeDirName(projectPath string) string
func IsClaudeCompatible(tool string) bool
```

From internal/tmux/tmux.go:
```go
type Session struct {
    Name              string   // e.g. "agentdeck-<title>-<hex>"
    DisplayName       string
    WorkDir           string
    LaunchInUserScope bool
    // … more fields
}
func NewSession(title, workDir string) *Session
func (s *Session) Start(command string) error
func (s *Session) Exists() bool
func (s *Session) Kill() error
```

From internal/session/hook_session_anchor.go:
```go
func HookSessionAnchorPath(instanceID string) string  // returns ~/.agent-deck/hooks/<id>.sid
func ReadHookSessionAnchor(instanceID string) string
func WriteHookSessionAnchor(instanceID, sessionID string)
func ClearHookSessionAnchor(instanceID string)
```

Helpers available from Plans 01 + 02 (already in the file):
- `isolatedHomeDir(t) string` — sets HOME to temp, creates ~/.agent-deck, ~/.agent-deck/hooks, ~/.claude/projects.
- `writeStubClaudeBinary(t, dir) string` — writes bash script that appends argv to $AGENTDECK_TEST_ARGV_LOG then sleeps.
- `uniqueTmuxServerName(t) string` — returns agentdeck-test-persist-<hex> and registers tmux cleanup.
- `requireSystemdRun(t)` — t.Skipf if systemd-run missing.
- `pidAlive(pid int) bool` (added in Plan 02 Task 1).
</interfaces>
</context>

<tasks>

<task type="auto" tdd="true">
  <name>Task 1: Add resume-dispatch helpers and TEST-08 (FreshSessionUsesSessionIDNotResume)</name>
  <files>internal/session/session_persistence_test.go</files>
  <read_first>
    - internal/session/session_persistence_test.go (test file from Plans 01 + 02 — helpers + 4 tests present; verify `writeStubClaudeBinary` writes to `$AGENTDECK_TEST_ARGV_LOG` and supports argv capture)
    - internal/session/instance.go lines 477-570 (buildClaudeCommand, capture-resume pattern — the path TEST-06/07 prove is broken)
    - internal/session/instance.go lines 1873-1986 (Start() body — verify line 1883 call to buildClaudeCommand)
    - internal/session/instance.go lines 3763-3817 (Restart() body — verify line 3789 call to buildClaudeResumeCommand)
    - internal/session/instance.go lines 4111-4178 (buildClaudeResumeCommand full body)
    - internal/session/instance.go lines 4789-4810 (sessionHasConversationData: verifies ~/.claude/projects/<hash>/<id>.jsonl has at least one message)
    - internal/session/instance.go lines 3195-3205 (ConvertToClaudeDirName usage — hyphen-replacement rule)
    - internal/session/instance.go lines 44-52 (Status constants — verbatim names)
    - internal/session/instance.go lines 394-443 (NewInstance + NewInstanceWithTool: they DO initialize tmuxSession via tmux.NewSession)
    - .planning/phases/01-persistence-test-scaffolding-red/01-CONTEXT.md decisions "Stub claude binary pattern" and "No-mocking rule"
    - internal/session/instance_test.go (search for `NewInstanceWithTool` usage to match existing test conventions)
  </read_first>
  <behavior>
    - Sanity-check the Plan 01 `writeStubClaudeBinary` helper: re-read internal/session/session_persistence_test.go and confirm the stub script appends its argv to `$AGENTDECK_TEST_ARGV_LOG`. If the helper writes to a different file or uses a different env var, STOP — this is a Plan 01 bug to escalate, NOT something to patch here.
    - Add helper `readCapturedClaudeArgv(t *testing.T, logPath string, timeout time.Duration) []string` that polls `logPath` every 100ms until it is non-empty OR timeout elapses. Returns the file contents split by newline (trimmed, empty lines dropped). On timeout, t.Fatalf with "readCapturedClaudeArgv: no argv captured within %s — stub claude was never spawned; check PATH prepending and tmux session creation". Callers get a []string of argv tokens (one per line) that can be inspected with `strings.Contains` against the joined string.
    - Add helper `newClaudeInstanceForDispatch(t *testing.T, home string) *Instance`:
      - Creates a real project dir: `projectPath := filepath.Join(home, "project"); os.MkdirAll(projectPath, 0o755)`.
      - Calls `inst := NewInstanceWithTool("persist-test-"+<4-hex>, projectPath, "claude")`. This initializes `inst.tmuxSession` via `tmux.NewSession(title, projectPath)`.
      - Sets `inst.ID = "test-"+<8-hex>` AFTER NewInstanceWithTool so the ID is deterministic-per-test (NewInstanceWithTool calls `GenerateID()` which uses a global counter; overriding with a test-local ID makes the sidecar path predictable for TEST-07).
      - Generates `inst.ClaudeSessionID` as a uuid-shaped hex string (`8-4-4-4-12` format from rand.Read of 16 bytes).
      - Sets `inst.Command = "claude"` so `buildClaudeCommandWithMessage` enters the `baseCommand == "claude"` branch.
      - Registers `t.Cleanup` to kill the tmux session: `if inst.tmuxSession != nil { _ = inst.tmuxSession.Kill() }`. This is SAFE — `inst.tmuxSession.Kill()` internally targets the unique session Name (prefix `agentdeck-`), not bare `tmux kill-server`.
      - Returns `inst`.
    - Add helper `setupStubClaudeOnPATH(t *testing.T, home string) (argvLogPath string)`:
      - `binDir := filepath.Join(home, "bin"); os.MkdirAll(binDir, 0o755)`.
      - `writeStubClaudeBinary(t, binDir)` — drops `<binDir>/claude` script.
      - `argvLog := filepath.Join(home, "claude-argv.log")`.
      - `t.Setenv("AGENTDECK_TEST_ARGV_LOG", argvLog)` — stub reads this env var.
      - `t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))` — stub claude resolves first.
      - Returns `argvLog`.
    - TEST-08 (`TestPersistence_FreshSessionUsesSessionIDNotResume`):
      - Pure-Go assertion on `buildClaudeResumeCommand()` — no tmux or claude spawn needed.
      - Header comment (verbatim per CONTEXT.md FAIL-or-PASS qualifier):
        ```
        // TestPersistence_FreshSessionUsesSessionIDNotResume pins REQ-2 fresh-session
        // contract: buildClaudeResumeCommand() on an Instance with no JSONL transcript
        // MUST produce "claude --session-id <id>", NOT "claude --resume <id>". Passing
        // --resume for a non-existent conversation id causes claude to exit with
        // "No conversation found".
        //
        // Per CONTEXT.md FAIL-or-PASS qualifier: current v1.5.1 code at
        // internal/session/instance.go:4150 routes this correctly via
        // sessionHasConversationData() — so this test PASSES today as a regression
        // guard. The unambiguous failure message below protects against future
        // regressions that would flip the branch. This test does NOT exercise the
        // Start() dispatch path (TEST-06 does); it guards the helper contract only.
        ```
      - Flow: `isolatedHomeDir(t)` → `newClaudeInstanceForDispatch(t, home)` → NO JSONL transcript written → `cmdLine := inst.buildClaudeResumeCommand()` → assert `strings.Contains(cmdLine, "--session-id "+inst.ClaudeSessionID)` and assert `!strings.Contains(cmdLine, "--resume")`.
      - Failure message on `--session-id` missing: `t.Fatalf("TEST-08: buildClaudeResumeCommand() with NO JSONL transcript MUST use '--session-id %s'. This prevents 'No conversation found' errors on first start. Got: %q", inst.ClaudeSessionID, cmdLine)`.
      - Failure message on `--resume` wrongly present: `t.Fatalf("TEST-08: buildClaudeResumeCommand() must NOT use --resume for a fresh session (no JSONL transcript exists at ~/.claude/projects/<hash>/<id>.jsonl). Got: %q", cmdLine)`.
    - Also add helper `writeSyntheticJSONLTranscript(t *testing.T, home string, inst *Instance) string` (used by Task 2's TEST-05, 06, 07). Signature and body identical to original plan's v1 — writes 2 JSONL lines (user + assistant) at `home/.claude/projects/<ConvertToClaudeDirName(inst.ProjectPath)>/<inst.ClaudeSessionID>.jsonl`, registers t.Cleanup to remove the file, returns the path.
    - No production code changes.
  </behavior>
  <action>
    Append to `internal/session/session_persistence_test.go`. Add `strings`, `encoding/json`, `time` to imports if not already present (some were added by Plan 02).

    1. Helper `readCapturedClaudeArgv`:
       ```go
       // readCapturedClaudeArgv polls the stub claude argv log until it is non-empty
       // (stub has been spawned and wrote its argv), then returns the argv lines.
       // Fatals if timeout elapses with empty log (dispatch never spawned claude).
       func readCapturedClaudeArgv(t *testing.T, logPath string, timeout time.Duration) []string {
           t.Helper()
           deadline := time.Now().Add(timeout)
           for time.Now().Before(deadline) {
               data, err := os.ReadFile(logPath)
               if err == nil && len(data) > 0 {
                   var argv []string
                   for _, line := range strings.Split(string(data), "\n") {
                       line = strings.TrimSpace(line)
                       if line != "" {
                           argv = append(argv, line)
                       }
                   }
                   if len(argv) > 0 {
                       return argv
                   }
               }
               time.Sleep(100 * time.Millisecond)
           }
           t.Fatalf("readCapturedClaudeArgv: no argv captured in %s at %s — stub claude was never spawned; check PATH prepending and tmux session creation", timeout, logPath)
           return nil // unreachable
       }
       ```

    2. Helper `newClaudeInstanceForDispatch`:
       ```go
       func newClaudeInstanceForDispatch(t *testing.T, home string) *Instance {
           t.Helper()
           var idb [4]byte
           if _, err := rand.Read(idb[:]); err != nil {
               t.Fatalf("rand: %v", err)
           }
           var sidb [16]byte
           if _, err := rand.Read(sidb[:]); err != nil {
               t.Fatalf("rand: %v", err)
           }
           sidHex := hex.EncodeToString(sidb[:])
           sid := sidHex[0:8] + "-" + sidHex[8:12] + "-" + sidHex[12:16] + "-" + sidHex[16:20] + "-" + sidHex[20:32]

           projectPath := filepath.Join(home, "project")
           if err := os.MkdirAll(projectPath, 0o755); err != nil {
               t.Fatalf("mkdir project: %v", err)
           }
           title := "persist-test-" + hex.EncodeToString(idb[:])
           inst := NewInstanceWithTool(title, projectPath, "claude")
           // Override the auto-generated ID so the sidecar path is deterministic for
           // TEST-07 and log messages reference a recognizable test ID.
           inst.ID = "test-" + hex.EncodeToString(idb[:])
           inst.ClaudeSessionID = sid
           inst.Command = "claude"

           t.Cleanup(func() {
               // inst.tmuxSession.Kill() targets the unique session Name
               // (prefix "agentdeck-"). It is SAFE — does NOT call bare
               // tmux kill-server.
               if inst.tmuxSession != nil {
                   _ = inst.tmuxSession.Kill()
               }
           })
           return inst
       }
       ```

    3. Helper `setupStubClaudeOnPATH`:
       ```go
       func setupStubClaudeOnPATH(t *testing.T, home string) string {
           t.Helper()
           binDir := filepath.Join(home, "bin")
           if err := os.MkdirAll(binDir, 0o755); err != nil {
               t.Fatalf("mkdir binDir: %v", err)
           }
           writeStubClaudeBinary(t, binDir)
           argvLog := filepath.Join(home, "claude-argv.log")
           t.Setenv("AGENTDECK_TEST_ARGV_LOG", argvLog)
           t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
           return argvLog
       }
       ```

    4. Helper `writeSyntheticJSONLTranscript`:
       ```go
       func writeSyntheticJSONLTranscript(t *testing.T, home string, inst *Instance) string {
           t.Helper()
           projectDirName := ConvertToClaudeDirName(inst.ProjectPath)
           dir := filepath.Join(home, ".claude", "projects", projectDirName)
           if err := os.MkdirAll(dir, 0o755); err != nil {
               t.Fatalf("mkdir projects: %v", err)
           }
           path := filepath.Join(dir, inst.ClaudeSessionID+".jsonl")
           lines := []map[string]interface{}{
               {"role": "user", "content": "hello"},
               {"role": "assistant", "content": "hi back"},
           }
           var buf []byte
           for _, ln := range lines {
               b, err := json.Marshal(ln)
               if err != nil {
                   t.Fatalf("marshal jsonl: %v", err)
               }
               buf = append(buf, b...)
               buf = append(buf, '\n')
           }
           if err := os.WriteFile(path, buf, 0o644); err != nil {
               t.Fatalf("write jsonl: %v", err)
           }
           t.Cleanup(func() { _ = os.Remove(path) })
           return path
       }
       ```

    5. TEST-08:
       ```go
       // TestPersistence_FreshSessionUsesSessionIDNotResume pins REQ-2 fresh-session
       // contract: buildClaudeResumeCommand() on an Instance with no JSONL transcript
       // MUST produce "claude --session-id <id>", NOT "claude --resume <id>". Passing
       // --resume for a non-existent conversation id causes claude to exit with
       // "No conversation found".
       //
       // Per CONTEXT.md FAIL-or-PASS qualifier: current v1.5.1 code at
       // internal/session/instance.go:4150 routes this correctly via
       // sessionHasConversationData() — so this test PASSES today as a regression
       // guard. The unambiguous failure message below protects against future
       // regressions that would flip the branch. This test does NOT exercise the
       // Start() dispatch path (TEST-06 does); it guards the helper contract only.
       func TestPersistence_FreshSessionUsesSessionIDNotResume(t *testing.T) {
           home := isolatedHomeDir(t)
           inst := newClaudeInstanceForDispatch(t, home)
           // NO JSONL transcript — fresh session.

           cmdLine := inst.buildClaudeResumeCommand()

           if !strings.Contains(cmdLine, "--session-id "+inst.ClaudeSessionID) {
               t.Fatalf("TEST-08: buildClaudeResumeCommand() with NO JSONL transcript MUST use '--session-id %s'. This prevents 'No conversation found' errors on first start. Got: %q", inst.ClaudeSessionID, cmdLine)
           }
           if strings.Contains(cmdLine, "--resume") {
               t.Fatalf("TEST-08: buildClaudeResumeCommand() must NOT use --resume for a fresh session (no JSONL transcript). Got: %q", cmdLine)
           }
       }
       ```

    6. Verification:
       - `go vet ./internal/session/...` exits 0.
       - `go test -run TestPersistence_FreshSessionUsesSessionIDNotResume ./internal/session/... -count=1 -v` exits 0 with `--- PASS: TestPersistence_FreshSessionUsesSessionIDNotResume` (regression guard).
       - `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces no output.
  </action>
  <verify>
    <automated>grep -q "func readCapturedClaudeArgv" internal/session/session_persistence_test.go &amp;&amp; grep -q "func newClaudeInstanceForDispatch" internal/session/session_persistence_test.go &amp;&amp; grep -q "func setupStubClaudeOnPATH" internal/session/session_persistence_test.go &amp;&amp; grep -q "func writeSyntheticJSONLTranscript" internal/session/session_persistence_test.go &amp;&amp; grep -q "func TestPersistence_FreshSessionUsesSessionIDNotResume" internal/session/session_persistence_test.go &amp;&amp; grep -q "AGENTDECK_TEST_ARGV_LOG" internal/session/session_persistence_test.go &amp;&amp; grep -q "NewInstanceWithTool" internal/session/session_persistence_test.go &amp;&amp; grep -q "ConvertToClaudeDirName" internal/session/session_persistence_test.go &amp;&amp; go vet ./internal/session/... &amp;&amp; go test -run TestPersistence_FreshSessionUsesSessionIDNotResume ./internal/session/... -count=1 -v 2>&amp;1 | grep -q "^--- PASS: TestPersistence_FreshSessionUsesSessionIDNotResume"</automated>
  </verify>
  <acceptance_criteria>
    - `grep -q "func readCapturedClaudeArgv" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func newClaudeInstanceForDispatch" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func setupStubClaudeOnPATH" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func writeSyntheticJSONLTranscript" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func TestPersistence_FreshSessionUsesSessionIDNotResume" internal/session/session_persistence_test.go` returns 0
    - `grep -q "AGENTDECK_TEST_ARGV_LOG" internal/session/session_persistence_test.go` returns 0
    - `grep -q "NewInstanceWithTool" internal/session/session_persistence_test.go` returns 0
    - `go vet ./internal/session/...` exits 0
    - `go test -run TestPersistence_FreshSessionUsesSessionIDNotResume ./internal/session/... -count=1 -v` output contains `--- PASS: TestPersistence_FreshSessionUsesSessionIDNotResume`
    - `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces no output
  </acceptance_criteria>
  <done>Helpers `readCapturedClaudeArgv`, `newClaudeInstanceForDispatch`, `setupStubClaudeOnPATH`, `writeSyntheticJSONLTranscript` exist and compile; TEST-08 PASSES as documented regression guard; no production code modified. Task 2 can now use the helpers to drive real Start()/Restart() dispatch.</done>
</task>

<task type="auto" tdd="true">
  <name>Task 2: Add TEST-05, TEST-06, TEST-07 — real dispatch path with stub claude argv capture</name>
  <files>internal/session/session_persistence_test.go</files>
  <read_first>
    - internal/session/session_persistence_test.go (Task 1 added helpers + TEST-08; reuse them)
    - internal/session/instance.go lines 477-570 (buildClaudeCommand / buildClaudeCommandWithMessage — the capture-resume pattern at line 550+ is what produces the RED state for TEST-06)
    - internal/session/instance.go lines 1873-1986 (Start() body — line 1883 calls buildClaudeCommand, the bypass)
    - internal/session/instance.go lines 3763-3820 (Restart() body — line 3789 calls buildClaudeResumeCommand correctly)
    - internal/session/instance.go lines 4011-4070 (Restart() fallback path — also calls buildClaudeResumeCommand correctly)
    - internal/session/hook_session_anchor.go (HookSessionAnchorPath construction; sidecar is NOT consulted by Start/Restart dispatch, confirmed in dispatch_path_analysis above)
    - internal/tmux/tmux.go lines 1321-1400 (Session.Start — shell execution; tmux runs the command string; stub claude is resolved via PATH inside the tmux pane's shell)
    - .planning/phases/01-persistence-test-scaffolding-red/01-CONTEXT.md "No-mocking rule" + "Stub claude binary pattern" + "Cleanup invariants"
    - CLAUDE.md tmux safety rule (no bare tmux kill-server; Session.Kill() is scoped — confirmed safe)
  </read_first>
  <behavior>
    - Add `requireTmux(t *testing.T)` helper at the top of the task that t.Skipf's with "no tmux available: %v" if `exec.LookPath("tmux")` fails. Tests on macOS with no tmux skip cleanly; on Linux CI with tmux present, tests run.
    - TEST-05 (`TestPersistence_RestartResumesConversation`):
      - Header comment (documents the FAIL-or-PASS observation from dispatch_path_analysis):
        ```
        // TestPersistence_RestartResumesConversation pins REQ-2 Restart() contract:
        // when a JSONL transcript exists for the instance's ClaudeSessionID,
        // inst.Restart() MUST spawn "claude --resume <id>", NOT "claude --session-id
        // <new-uuid>".
        //
        // Driven via the REAL Restart() dispatch path (internal/session/instance.go
        // line 3763). Stub claude on PATH captures argv to AGENTDECK_TEST_ARGV_LOG.
        //
        // Per CONTEXT.md FAIL-or-PASS qualifier and dispatch_path_analysis in this
        // plan: current v1.5.1 code at instance.go:3789 correctly routes Restart()
        // through buildClaudeResumeCommand() — so this test may PASS today. Phase 3's
        // REQ-2 fix lives in Start() (TEST-06), not Restart(). This test is kept as a
        // REGRESSION GUARD: any future change that breaks Restart()'s resume routing
        // (e.g. removing the `i.ClaudeSessionID != ""` check at line 3788) will fail
        // this test. Either outcome (PASS now, FAIL after regression) is acceptable;
        // the unambiguous failure message below prevents silent breakage.
        ```
      - Flow:
        1. `requireTmux(t)`.
        2. `home := isolatedHomeDir(t)`.
        3. `argvLog := setupStubClaudeOnPATH(t, home)`.
        4. `inst := newClaudeInstanceForDispatch(t, home)`.
        5. `writeSyntheticJSONLTranscript(t, home, inst)` — so sessionHasConversationData() returns true.
        6. **First spawn the tmux session** (necessary precondition for Restart's respawn-pane branch at line 3788): `if err := inst.Start(); err != nil { t.Fatalf("setup: inst.Start: %v", err) }`. Give tmux 500ms to settle and write its initial argv to the log.
        7. **Truncate the argv log** so the subsequent Restart() argv is the only entry: `os.WriteFile(argvLog, nil, 0o644)`.
        8. `if err := inst.Restart(); err != nil { t.Fatalf("Restart: %v", err) }`.
        9. `argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second)`.
        10. `joined := strings.Join(argv, " ")`.
        11. Assert: if `strings.Contains(joined, "--resume") && strings.Contains(joined, inst.ClaudeSessionID)` → PASS. Else → `t.Fatalf("TEST-05 RED: after Restart() with JSONL transcript at %s, captured claude argv must contain '--resume %s'. Got argv: %v", <jsonl path>, inst.ClaudeSessionID, argv)`.
      - Cleanup: handled by `newClaudeInstanceForDispatch` (kills tmux session via Kill()), `isolatedHomeDir` (removes temp HOME), and `writeSyntheticJSONLTranscript` (removes JSONL).
    - TEST-06 (`TestPersistence_StartAfterSIGKILLResumesConversation`):
      - Header comment:
        ```
        // TestPersistence_StartAfterSIGKILLResumesConversation is the core REQ-2 RED
        // test. Models the 2026-04-14 incident: tmux server is SIGKILLed by an SSH
        // logout, instance transitions to Status=error, user runs "agent-deck session
        // start" — which calls inst.Start() (cmd/agent-deck/session_cmd.go:188).
        //
        // The CONTRACT: Start() on an Instance with a populated ClaudeSessionID and
        // JSONL transcript MUST spawn "claude --resume <id>", NOT a new UUID.
        //
        // Per dispatch_path_analysis in this plan: current v1.5.1 Start()
        // (internal/session/instance.go:1873) calls buildClaudeCommand() at line
        // 1883, which runs through the capture-resume pattern (line 550+) that
        // generates a brand-new UUID via generateUUID() and spawns
        // "claude --session-id <NEW_UUID>". It does NOT consult i.ClaudeSessionID.
        // So this test FAILS RED on current code.
        //
        // Phase 3's REQ-2 fix: route Start() through buildClaudeResumeCommand() when
        // IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != "" — mirroring the
        // Restart() code path at line 3789.
        ```
      - Flow:
        1. `requireTmux(t)`.
        2. `home := isolatedHomeDir(t)`.
        3. `argvLog := setupStubClaudeOnPATH(t, home)`.
        4. `inst := newClaudeInstanceForDispatch(t, home)`.
        5. **Simulate the post-SIGKILL state**: `inst.Status = StatusError`.
        6. `writeSyntheticJSONLTranscript(t, home, inst)` — proves a prior conversation existed.
        7. `if err := inst.Start(); err != nil { t.Fatalf("Start: %v", err) }` — this is the dispatch path the user would hit via `agent-deck session start`.
        8. `argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second)`.
        9. `joined := strings.Join(argv, " ")`.
        10. Assert resume happened: `if !strings.Contains(joined, "--resume") || !strings.Contains(joined, inst.ClaudeSessionID)` → `t.Fatalf("TEST-06 RED: after inst.Start() with Status=StatusError, ClaudeSessionID=%s, and JSONL transcript present, captured claude argv must contain '--resume %s'. Got argv: %v. This is the 2026-04-14 incident REQ-2 root cause: Start() dispatches through buildClaudeCommand (instance.go:1883) instead of buildClaudeResumeCommand. Phase 3 must fix this.", inst.ClaudeSessionID, inst.ClaudeSessionID, argv)`.
        11. Also assert no new UUID was minted: `if strings.Contains(joined, "--session-id") && !strings.Contains(joined, inst.ClaudeSessionID)` — the argv had `--session-id <SOME_OTHER_UUID>` → fatal message: `"TEST-06 RED: Start() minted a NEW session UUID instead of resuming ClaudeSessionID=%s. Argv: %v"`.
      - Cleanup: same as TEST-05.
    - TEST-07 (`TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion`):
      - Header comment:
        ```
        // TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion pins the
        // invariant from docs/session-id-lifecycle.md: instance JSON is the
        // authoritative ClaudeSessionID source. The hook sidecar at
        // ~/.agent-deck/hooks/<id>.sid is a read-only fallback for hook-event
        // processing (see updateSessionIDFromHook at instance.go:2626) — it is NOT
        // consulted by Start() or Restart() dispatch. Deleting the sidecar MUST NOT
        // affect the claude --resume command produced by a session start.
        //
        // Flow:
        //   1. Write sidecar at ~/.agent-deck/hooks/<id>.sid with ClaudeSessionID.
        //   2. Delete the sidecar (simulates corruption or cleanup incident).
        //   3. Call inst.Start() with a JSONL transcript present.
        //   4. Assert captured claude argv contains "--resume <ClaudeSessionID>".
        //
        // Per dispatch_path_analysis in this plan: this test FAILS RED on current
        // v1.5.1 for the SAME root cause as TEST-06 (Start() bypasses resume). After
        // Phase 3 fixes Start() to route through buildClaudeResumeCommand, this test
        // will GREEN because ClaudeSessionID is read from the Instance struct (which
        // mirrors instance JSON storage), NOT from the sidecar.
        ```
      - Flow:
        1. `requireTmux(t)`.
        2. `home := isolatedHomeDir(t)`.
        3. `argvLog := setupStubClaudeOnPATH(t, home)`.
        4. `inst := newClaudeInstanceForDispatch(t, home)`.
        5. `sidecarPath := filepath.Join(home, ".agent-deck", "hooks", inst.ID+".sid")` — this matches `HookSessionAnchorPath(inst.ID)` given HOME=home. Verify by calling `HookSessionAnchorPath(inst.ID)` and asserting equality (sanity check that the isolated HOME override is effective): `if got := HookSessionAnchorPath(inst.ID); got != sidecarPath { t.Fatalf("sidecar path mismatch: got %q want %q — isolatedHomeDir HOME override may not have propagated", got, sidecarPath) }`.
        6. Write sidecar: `os.WriteFile(sidecarPath, []byte(inst.ClaudeSessionID+"\n"), 0o644)` after ensuring parent dir exists.
        7. Verify written: `os.Stat(sidecarPath)`.
        8. DELETE sidecar: `os.Remove(sidecarPath)`. Verify gone: `os.Stat(sidecarPath)` returns `os.IsNotExist`.
        9. Assert `inst.ClaudeSessionID != ""` — the in-memory Instance struct still has the ID; the sidecar was never the source.
        10. Write synthetic JSONL.
        11. `if err := inst.Start(); err != nil { t.Fatalf("Start: %v", err) }`.
        12. `argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second); joined := strings.Join(argv, " ")`.
        13. Assert `strings.Contains(joined, "--resume "+inst.ClaudeSessionID)` with fatal message: `t.Fatalf("TEST-07 RED: after deleting hook sidecar at %s, inst.Start() must still spawn 'claude --resume %s' because ClaudeSessionID lives in instance storage, not the sidecar. Got argv: %v. Root cause: Start() bypasses buildClaudeResumeCommand — same as TEST-06. Phase 3 fix will make both tests GREEN.", sidecarPath, inst.ClaudeSessionID, argv)`.
    - Full suite verification after all three tests are appended:
      - `grep -c "^func TestPersistence_" internal/session/session_persistence_test.go` → exactly 8.
      - `go vet ./internal/session/...` exits 0.
      - `go test -run TestPersistence_ ./internal/session/... -race -count=1 -v` runs all 8 tests.
    - RED/GREEN pattern on Linux+systemd with current v1.5.1:
      - TEST-01 FAIL (from Plan 02: GetLaunchInUserScope default is false)
      - TEST-02 PASS (from Plan 02: inverse pin)
      - TEST-03 FAIL (from Plan 01)
      - TEST-04 SKIP (from Plan 01: documented choice)
      - TEST-05 PASS (Restart already routes correctly — regression guard)
      - TEST-06 FAIL ("TEST-06 RED: ...") — THE core REQ-2 RED test
      - TEST-07 FAIL ("TEST-07 RED: ...") — same root cause as TEST-06
      - TEST-08 PASS (regression guard)
    - RED/GREEN pattern on macOS (no systemd-run, tmux may or may not be present):
      - TEST-01 SKIP (requireSystemdRun)
      - TEST-02 SKIP (requireSystemdRun)
      - TEST-03 SKIP (requireSystemdRun)
      - TEST-04 PASS
      - TEST-05: PASS if tmux present, SKIP if not
      - TEST-06: FAIL if tmux present (the root-cause bug is platform-agnostic), SKIP if not
      - TEST-07: FAIL if tmux present, SKIP if not
      - TEST-08: PASS
    - If TEST-05 FAILS on current v1.5.1 (contrary to dispatch_path_analysis expectation): inspect the argv. If Restart() is actually spawning `--session-id` instead of `--resume`, either line 3789 is not being reached (tmux session died between Start and Restart) or the respawn-pane path is silently bypassing buildClaudeResumeCommand. Escalate via `## CHECKPOINT REACHED` rather than silently making the test pass.
    - If TEST-06 or TEST-07 PASSES on current v1.5.1 (contrary to expectation): the spec's REQ-2 root-cause analysis is incorrect. Escalate via `## CHECKPOINT REACHED`.
    - No production code changes.
  </behavior>
  <action>
    Append to `internal/session/session_persistence_test.go`:

    1. Helper `requireTmux` (add at the top of the new section):
       ```go
       func requireTmux(t *testing.T) {
           t.Helper()
           if _, err := exec.LookPath("tmux"); err != nil {
               t.Skipf("no tmux available: %v", err)
           }
       }
       ```

    2. TEST-05:
       ```go
       // TestPersistence_RestartResumesConversation pins REQ-2 Restart() contract:
       // when a JSONL transcript exists for the instance's ClaudeSessionID,
       // inst.Restart() MUST spawn "claude --resume <id>", NOT "claude --session-id
       // <new-uuid>".
       //
       // Driven via the REAL Restart() dispatch path (internal/session/instance.go
       // line 3763). Stub claude on PATH captures argv to AGENTDECK_TEST_ARGV_LOG.
       //
       // Per CONTEXT.md FAIL-or-PASS qualifier and dispatch_path_analysis in this
       // plan: current v1.5.1 code at instance.go:3789 correctly routes Restart()
       // through buildClaudeResumeCommand() — so this test may PASS today. Phase 3's
       // REQ-2 fix lives in Start() (TEST-06), not Restart(). This test is kept as a
       // REGRESSION GUARD: any future change that breaks Restart()'s resume routing
       // will fail this test. Either outcome is acceptable; the unambiguous failure
       // message below prevents silent breakage.
       func TestPersistence_RestartResumesConversation(t *testing.T) {
           requireTmux(t)
           home := isolatedHomeDir(t)
           argvLog := setupStubClaudeOnPATH(t, home)
           inst := newClaudeInstanceForDispatch(t, home)
           jsonlPath := writeSyntheticJSONLTranscript(t, home, inst)

           // First bring the tmux session up so Restart()'s respawn-pane branch
           // (instance.go:3788 — requires tmuxSession.Exists()) is taken.
           if err := inst.Start(); err != nil {
               t.Fatalf("setup: inst.Start: %v", err)
           }
           time.Sleep(500 * time.Millisecond) // let initial argv be written

           // Reset the argv log so the subsequent Restart's argv is the only entry.
           if err := os.WriteFile(argvLog, nil, 0o644); err != nil {
               t.Fatalf("truncate argvLog: %v", err)
           }

           if err := inst.Restart(); err != nil {
               t.Fatalf("Restart: %v", err)
           }

           argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second)
           joined := strings.Join(argv, " ")
           if !strings.Contains(joined, "--resume") || !strings.Contains(joined, inst.ClaudeSessionID) {
               t.Fatalf("TEST-05 RED: after Restart() with JSONL transcript at %s, captured claude argv must contain '--resume %s'. Got argv: %v", jsonlPath, inst.ClaudeSessionID, argv)
           }
       }
       ```

    3. TEST-06:
       ```go
       // TestPersistence_StartAfterSIGKILLResumesConversation is the core REQ-2 RED
       // test. Models the 2026-04-14 incident: tmux server is SIGKILLed by an SSH
       // logout, instance transitions to Status=error, user runs "agent-deck session
       // start" — which calls inst.Start() (cmd/agent-deck/session_cmd.go:188).
       //
       // The CONTRACT: Start() on an Instance with a populated ClaudeSessionID and
       // JSONL transcript MUST spawn "claude --resume <id>", NOT a new UUID.
       //
       // Per dispatch_path_analysis: current v1.5.1 Start() (instance.go:1873)
       // calls buildClaudeCommand() at line 1883, which runs through the capture-
       // resume pattern (line 550+) that generates a brand-new UUID and spawns
       // "claude --session-id <NEW_UUID>". It does NOT consult i.ClaudeSessionID.
       // So this test FAILS RED on current code.
       //
       // Phase 3's REQ-2 fix: route Start() through buildClaudeResumeCommand() when
       // IsClaudeCompatible(i.Tool) && i.ClaudeSessionID != "" — mirroring the
       // Restart() code path at line 3789.
       func TestPersistence_StartAfterSIGKILLResumesConversation(t *testing.T) {
           requireTmux(t)
           home := isolatedHomeDir(t)
           argvLog := setupStubClaudeOnPATH(t, home)
           inst := newClaudeInstanceForDispatch(t, home)
           // Simulate the post-SIGKILL state transition.
           inst.Status = StatusError
           writeSyntheticJSONLTranscript(t, home, inst)

           if err := inst.Start(); err != nil {
               t.Fatalf("Start: %v", err)
           }

           argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second)
           joined := strings.Join(argv, " ")

           if !strings.Contains(joined, "--resume") || !strings.Contains(joined, inst.ClaudeSessionID) {
               t.Fatalf("TEST-06 RED: after inst.Start() with Status=StatusError, ClaudeSessionID=%s, and JSONL transcript present, captured claude argv must contain '--resume %s'. Got argv: %v. This is the 2026-04-14 incident REQ-2 root cause: Start() dispatches through buildClaudeCommand (instance.go:1883) instead of buildClaudeResumeCommand. Phase 3 must fix this.", inst.ClaudeSessionID, inst.ClaudeSessionID, argv)
           }
           if strings.Contains(joined, "--session-id") && !strings.Contains(joined, inst.ClaudeSessionID) {
               t.Fatalf("TEST-06 RED: Start() minted a NEW session UUID instead of resuming ClaudeSessionID=%s. Argv: %v", inst.ClaudeSessionID, argv)
           }
       }
       ```

    4. TEST-07:
       ```go
       // TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion pins the
       // invariant from docs/session-id-lifecycle.md: instance JSON is the
       // authoritative ClaudeSessionID source. The hook sidecar at
       // ~/.agent-deck/hooks/<id>.sid is a read-only fallback for hook-event
       // processing (updateSessionIDFromHook at instance.go:2626) — it is NOT
       // consulted by Start() or Restart() dispatch. Deleting the sidecar MUST NOT
       // affect the claude --resume command produced by a session start.
       //
       // Per dispatch_path_analysis: this test FAILS RED on current v1.5.1 for
       // the SAME root cause as TEST-06 (Start() bypasses resume). After Phase 3
       // fixes Start() to route through buildClaudeResumeCommand, this test will
       // GREEN because ClaudeSessionID is read from the Instance struct (which
       // mirrors instance JSON storage), NOT from the sidecar.
       func TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion(t *testing.T) {
           requireTmux(t)
           home := isolatedHomeDir(t)
           argvLog := setupStubClaudeOnPATH(t, home)
           inst := newClaudeInstanceForDispatch(t, home)

           sidecarPath := filepath.Join(home, ".agent-deck", "hooks", inst.ID+".sid")
           if got := HookSessionAnchorPath(inst.ID); got != sidecarPath {
               t.Fatalf("sidecar path mismatch: got %q want %q — isolatedHomeDir HOME override may not have propagated", got, sidecarPath)
           }
           if err := os.MkdirAll(filepath.Dir(sidecarPath), 0o755); err != nil {
               t.Fatalf("mkdir hooks: %v", err)
           }
           if err := os.WriteFile(sidecarPath, []byte(inst.ClaudeSessionID+"\n"), 0o644); err != nil {
               t.Fatalf("write sidecar: %v", err)
           }
           if _, err := os.Stat(sidecarPath); err != nil {
               t.Fatalf("setup: sidecar not written: %v", err)
           }

           if err := os.Remove(sidecarPath); err != nil {
               t.Fatalf("delete sidecar: %v", err)
           }
           if _, err := os.Stat(sidecarPath); !os.IsNotExist(err) {
               t.Fatalf("setup: sidecar still present after delete: err=%v", err)
           }
           if inst.ClaudeSessionID == "" {
               t.Fatalf("TEST-07 RED: ClaudeSessionID was cleared when sidecar was deleted; expected instance-JSON to remain authoritative")
           }

           writeSyntheticJSONLTranscript(t, home, inst)

           if err := inst.Start(); err != nil {
               t.Fatalf("Start: %v", err)
           }

           argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second)
           joined := strings.Join(argv, " ")
           if !strings.Contains(joined, "--resume") || !strings.Contains(joined, inst.ClaudeSessionID) {
               t.Fatalf("TEST-07 RED: after deleting hook sidecar at %s, inst.Start() must still spawn 'claude --resume %s' because ClaudeSessionID lives in instance storage, not the sidecar. Got argv: %v. Root cause: Start() bypasses buildClaudeResumeCommand — same as TEST-06. Phase 3 fix will make both tests GREEN.", sidecarPath, inst.ClaudeSessionID, argv)
           }
       }
       ```

    5. After appending, run the FULL suite:
       ```
       go vet ./internal/session/...
       go test -run TestPersistence_ ./internal/session/... -race -count=1 -v
       ```

    6. Validate the RED/GREEN pattern. If it matches expectation per `<behavior>` above, proceed. If not (TEST-06 or TEST-07 unexpectedly PASSES, or TEST-05 unexpectedly FAILS), STOP and escalate via `## CHECKPOINT REACHED` — do not silently change the test assertion direction.

    7. Cleanup verification:
       - `tmux list-sessions 2>/dev/null | grep -c 'agentdeck-'` — should show NO test-created sessions (test title prefix is `persist-test-` which `NewInstanceWithTool` wraps with the `agentdeck-` session prefix; `t.Cleanup` in `newClaudeInstanceForDispatch` calls `inst.tmuxSession.Kill()` which targets the unique Name).
       - `systemctl --user list-units --type=scope --no-legend 2>/dev/null | grep -c 'agentdeck-tmux-persist-test'` → 0 (even if `LaunchInUserScope=true`, the cleanup stops the scope via tmux Kill).

    8. `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces NO output.
  </action>
  <verify>
    <automated>grep -q "func TestPersistence_RestartResumesConversation" internal/session/session_persistence_test.go &amp;&amp; grep -q "func TestPersistence_StartAfterSIGKILLResumesConversation" internal/session/session_persistence_test.go &amp;&amp; grep -q "func TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion" internal/session/session_persistence_test.go &amp;&amp; grep -q "func requireTmux" internal/session/session_persistence_test.go &amp;&amp; grep -q "TEST-06 RED:" internal/session/session_persistence_test.go &amp;&amp; grep -q "TEST-07 RED:" internal/session/session_persistence_test.go &amp;&amp; grep -q "inst.Start()" internal/session/session_persistence_test.go &amp;&amp; grep -q "inst.Restart()" internal/session/session_persistence_test.go &amp;&amp; grep -q "HookSessionAnchorPath" internal/session/session_persistence_test.go &amp;&amp; [ "$(grep -c '^func TestPersistence_' internal/session/session_persistence_test.go)" = "8" ] &amp;&amp; go vet ./internal/session/... &amp;&amp; go test -run "TestPersistence_StartAfterSIGKILLResumesConversation|TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion" ./internal/session/... -count=1 -v 2>&amp;1 | tee /tmp/test0607.out; (grep -q "^--- FAIL: TestPersistence_StartAfterSIGKILLResumesConversation\\|^--- SKIP: TestPersistence_StartAfterSIGKILLResumesConversation" /tmp/test0607.out &amp;&amp; grep -q "^--- FAIL: TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion\\|^--- SKIP: TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion" /tmp/test0607.out)</automated>
  </verify>
  <acceptance_criteria>
    - `grep -q "func TestPersistence_RestartResumesConversation" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func TestPersistence_StartAfterSIGKILLResumesConversation" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion" internal/session/session_persistence_test.go` returns 0
    - `grep -q "func requireTmux" internal/session/session_persistence_test.go` returns 0
    - `grep -q "TEST-06 RED:" internal/session/session_persistence_test.go` returns 0
    - `grep -q "TEST-07 RED:" internal/session/session_persistence_test.go` returns 0
    - `grep -q "inst.Start()" internal/session/session_persistence_test.go` returns 0 (real dispatch path exercised)
    - `grep -q "inst.Restart()" internal/session/session_persistence_test.go` returns 0
    - `grep -q "HookSessionAnchorPath" internal/session/session_persistence_test.go` returns 0 (TEST-07 sanity check)
    - `grep -c "^func TestPersistence_" internal/session/session_persistence_test.go` returns exactly 8
    - All 8 verbatim names appear as function definitions:
      ```
      for n in TmuxSurvivesLoginSessionRemoval TmuxDiesWithoutUserScope LinuxDefaultIsUserScope MacOSDefaultIsDirect RestartResumesConversation StartAfterSIGKILLResumesConversation ClaudeSessionIDSurvivesHookSidecarDeletion FreshSessionUsesSessionIDNotResume; do grep -q "^func TestPersistence_$n" internal/session/session_persistence_test.go || exit 1; done
      ```
    - `go vet ./internal/session/...` exits 0
    - On Linux+systemd with tmux installed: `go test -run TestPersistence_StartAfterSIGKILLResumesConversation ./internal/session/... -count=1` exits NON-ZERO and output contains "TEST-06 RED:"
    - On Linux+systemd with tmux installed: `go test -run TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion ./internal/session/... -count=1` exits NON-ZERO and output contains "TEST-07 RED:"
    - On macOS/no-tmux: both TEST-06 and TEST-07 SKIP with "no tmux available:" in output
    - Full suite on Linux+systemd: at minimum TEST-01, TEST-03, TEST-06, TEST-07 FAIL; TEST-02, TEST-08 PASS; TEST-04 SKIP; TEST-05 PASS or FAIL (both acceptable per dispatch_path_analysis)
    - After the suite: `tmux list-sessions 2>/dev/null | grep -c 'agentdeck-persist-test-\\|agentdeck-test-persist-'` returns 0
    - `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces no output
  </acceptance_criteria>
  <done>All 8 TestPersistence_* tests exist with verbatim names; TEST-06 and TEST-07 FAIL RED with unambiguous diagnostic messages referencing the Start() dispatch bypass; TEST-05 passes as regression guard (per dispatch_path_analysis); suite runs cleanly on both Linux+systemd and macOS; no stray tmux servers remain; no production code modified.</done>
</task>

</tasks>

<threat_model>
## Trust Boundaries

| Boundary | Description |
|----------|-------------|
| test process → filesystem under isolated HOME | All writes are to `t.TempDir()` subtrees (via `isolatedHomeDir(t)`). Test cleanup is handled by Go's testing framework. |
| test process → real tmux binary | Tests invoke `inst.Start()` and `inst.Restart()` which internally spawn tmux via `(*tmux.Session).Start()`. tmux session names use the unique `agentdeck-<title>-<hex>` prefix (prefix set by tmux.NewSession; title includes `persist-test-<hex>`). Cleanup via `inst.tmuxSession.Kill()` targets only the unique session Name — SAFE, does not call bare `tmux kill-server`. |
| test process → stub claude binary | Stub is a bash script at `<t.TempDir()>/bin/claude` that writes argv to `$AGENTDECK_TEST_ARGV_LOG` and sleeps. PATH is prepended via `t.Setenv` so real claude is never invoked. |
| test process → systemd user manager (via tmux.Session.Start) | If `LaunchInUserScope=true` (Phase 2+ default), tmux spawn is wrapped in `systemd-run --user --scope`. The scope name uses the `agentdeck-tmux-<name>` prefix with unique hex. Cleanup via `tmux.Session.Kill()` collapses the scope. |

## STRIDE Threat Register

No production code modified; threat surface unchanged. Tests run with real `tmux` and real `systemd-run` binaries; tests MUST use unique tmux session names with the `agentdeck-` prefix (provided by `tmux.NewSession`) and MUST NOT call `tmux kill-server` without a filter (per repo CLAUDE.md tmux safety mandate, after the 2025-12-10 incident that killed 40 user sessions). Cleanup in these tests uses `(*tmux.Session).Kill()` which internally targets only the specific session Name — verified safe by reading `internal/tmux/tmux.go`.

| Threat ID | Category | Component | Disposition | Mitigation Plan |
|-----------|----------|-----------|-------------|-----------------|
| T-03-01 | Tampering | writes to ~/.agent-deck/hooks/ and ~/.claude/projects/ | mitigate | `isolatedHomeDir(t)` redirects HOME to `t.TempDir()`; every write is under the temp tree, auto-removed by Go testing. `HookSessionAnchorPath` uses `GetHooksDir()` which honors HOME — verified by TEST-07's sanity assertion. |
| T-03-02 | Information Disclosure | synthetic JSONL contents + stub claude argv log | accept | JSONL contains only literal "hello" / "hi back"; argv log contains only claude CLI args (no secrets, no user data). All under `t.TempDir()`. |
| T-03-03 | Denial of Service | os.Remove on sidecar + tmux Kill on cleanup | mitigate | sidecar removal targets exactly `<tempHome>/.agent-deck/hooks/<instance-id>.sid`. Tmux cleanup via `(*tmux.Session).Kill()` targets `inst.tmuxSession.Name` only (unique per test). Grep-verified: no bare `tmux kill-server` in test file. |
| T-03-04 | Elevation of Privilege | stub claude on PATH | mitigate | Stub is confined to `<tempHome>/bin/claude`. PATH override via `t.Setenv` is auto-restored at test cleanup. Stub script has no network or privilege escalation. |
| T-03-05 | Denial of Service | systemd scope teardown (if LaunchInUserScope=true) | mitigate | Scope names use `agentdeck-tmux-<unique>` prefix; cleanup via `(*tmux.Session).Kill()` stops the scope. Tests do not call `systemctl --user stop` with glob patterns. |
</threat_model>

<verification>
Phase-level verification (run after both tasks in this plan complete — this is also the GATE for the whole phase):

1. `go vet ./internal/session/...` exits 0.
2. `go build ./...` exits 0.
3. `grep -c "^func TestPersistence_" internal/session/session_persistence_test.go` returns exactly 8.
4. All 8 verbatim names appear as function definitions:
   ```
   for n in TmuxSurvivesLoginSessionRemoval TmuxDiesWithoutUserScope LinuxDefaultIsUserScope MacOSDefaultIsDirect RestartResumesConversation StartAfterSIGKILLResumesConversation ClaudeSessionIDSurvivesHookSidecarDeletion FreshSessionUsesSessionIDNotResume; do grep -q "^func TestPersistence_$n" internal/session/session_persistence_test.go || { echo "MISSING: TestPersistence_$n"; exit 1; }; done
   ```
5. `go test -run TestPersistence_ ./internal/session/... -race -count=1 -v` runs exactly 8 tests.
6. On Linux+systemd with tmux installed: at minimum TEST-01, TEST-03, TEST-06, TEST-07 fail RED with diagnostic messages "TEST-0N RED:"; TEST-02 PASS (inverse pin); TEST-08 PASS (regression guard); TEST-04 SKIP; TEST-05 PASS or FAIL (both acceptable — regression guard).
7. On macOS: systemd-dependent tests (TEST-01, TEST-02, TEST-03) SKIP with "no systemd-run available:". tmux-dependent tests (TEST-05, TEST-06, TEST-07) SKIP with "no tmux available:" IF tmux is absent; if tmux is present, they execute with the same RED/GREEN pattern as Linux (TEST-06/07 FAIL, TEST-05 PASS). TEST-04, TEST-08 PASS.
8. After the suite: no stray `agentdeck-persist-test-*` or `agentdeck-test-persist-*` tmux sessions; no stray `agentdeck-tmux-*test*` or `fake-login-*` systemd scopes.
9. `git diff --stat internal/tmux/ internal/session/instance.go internal/session/userconfig.go internal/session/storage.go cmd/agent-deck/session_cmd.go` produces no output.
10. No `rm` shell commands anywhere in the test file (`grep -n 'exec.Command("rm"' internal/session/session_persistence_test.go` returns nothing). `os.Remove` in test code is allowed (per CONTEXT.md — the `rm` rule applies to shell usage).
</verification>

<success_criteria>
1. All eight `TestPersistence_*` tests exist in `internal/session/session_persistence_test.go` with verbatim names.
2. `go test -run TestPersistence_ ./internal/session/... -race -count=1` compiles and runs on Linux+systemd and macOS.
3. Linux+systemd RED pattern: TEST-01 FAIL, TEST-02 PASS, TEST-03 FAIL, TEST-04 SKIP, TEST-05 PASS (regression guard), TEST-06 FAIL (core REQ-2 RED), TEST-07 FAIL (same root cause), TEST-08 PASS (regression guard).
4. macOS pattern: systemd-dependent tests SKIP; tmux-dependent tests SKIP if tmux absent, otherwise follow Linux RED/GREEN pattern.
5. TEST-05, TEST-06, TEST-07 exercise the REAL dispatch paths (`inst.Start()` and `inst.Restart()`) and capture the spawned claude argv via a stub binary on PATH — they do NOT assert on `buildClaudeResumeCommand()` in isolation.
6. TEST-06 and TEST-07 FAIL RED with diagnostic messages that cite the Start() dispatch bypass as the 2026-04-14 incident REQ-2 root cause.
7. No stray tmux sessions or systemd scopes remain after the suite runs.
8. No production code under the mandated paths is modified.
9. All RED-state failures contain unambiguous diagnostic messages ("TEST-NN RED:") — never compile errors or nil-pointer panics.
</success_criteria>

<output>
After completion, create `.planning/phases/01-persistence-test-scaffolding-red/01-03-SUMMARY.md`. The summary MUST include:
- The 8-test run output on the execution host (as a code block).
- The captured argv from TEST-06 on current v1.5.1 (as a code block) — this is the evidence that Start() spawns `--session-id <NEW_UUID>` instead of `--resume <ClaudeSessionID>`.
- Confirmation that `git diff --stat` shows no production-code modifications.
- The exact RED/GREEN/SKIP status of each of the 8 tests on the execution host, with a one-line justification for any deviation from the expected pattern.
- If TEST-05, TEST-06, or TEST-07 deviated from the dispatch_path_analysis expectation, the summary MUST escalate with `## CHECKPOINT REACHED` and ask the conductor whether to update CONTEXT.md or retire the test as a regression guard.

Commit files with:
```
git add internal/session/session_persistence_test.go
git add -f .planning/phases/01-persistence-test-scaffolding-red/01-03-SUMMARY.md
git commit -m "test(persistence): add TEST-05 through TEST-08 via real dispatch path (RED)"
```
(`-f` required because `.git/info/exclude` blocks `.planning/`. No Claude attribution in the commit message.)
</output>
</content>
</invoke>