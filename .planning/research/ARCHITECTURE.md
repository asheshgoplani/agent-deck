# Architecture Research: Integration Testing Framework for Agent-Deck

**Domain:** Go integration testing for terminal session manager with tmux, multi-tool orchestration, and cross-session events
**Researched:** 2026-03-06
**Confidence:** HIGH

## System Overview: How Integration Tests Fit the Existing Architecture

```
Existing Codebase                                   New Testing Infrastructure
==================================                  ==================================

cmd/agent-deck/                                     internal/integration/ (NEW PACKAGE)
  main.go, session_cmd.go,                            testutil.go        -- shared helpers
  conductor_cmd.go, ...                               fixtures.go        -- fixture management
  *_test.go (unit + CLI tests)                        tmux_harness.go    -- tmux lifecycle
                                                      storage_harness.go -- DB setup/teardown
internal/session/                                     conductor_test.go  -- orchestration pipeline
  instance.go, conductor.go,                          lifecycle_test.go  -- session start/stop/fork
  claude.go, gemini.go, ...                           events_test.go     -- cross-session events
  *_test.go (unit tests, some                         multitool_test.go  -- Claude/Gemini/OpenCode/Codex
             integration tests)                       sleep_test.go      -- sleep/wait detection
  testmain_test.go                                    skills_test.go     -- skills attachment
                                                      testmain_test.go   -- profile isolation
internal/tmux/
  tmux.go, pipemanager.go, ...                      internal/testutil/ (EXTENDED)
  *_test.go (unit tests)                              gitenv.go          -- existing
  testmain_test.go                                    tmux.go            -- tmux session helpers (NEW)
                                                      storage.go         -- test DB helpers (NEW)
internal/statedb/                                     fixtures.go        -- fixture loading (NEW)
  statedb.go, migrate.go
  statedb_test.go
```

### Key Decision: New `internal/integration/` Package

**Why a new package, not scattered in existing packages:**

1. **Import graph freedom.** Integration tests need to import from `session`, `tmux`, `statedb`, and `cmd/agent-deck` simultaneously. Placing tests in any one of those packages creates circular dependency risks and limits what the test can access. A separate package imports everything it needs cleanly.

2. **Clear boundary with existing unit tests.** The existing `*_test.go` files in `internal/session/` and `internal/tmux/` are package-internal tests (they use unexported functions like `newTestStorage`, `cleanupTestSessions`). Integration tests should test through public APIs only, exercising the same interfaces the CLI and TUI use.

3. **Shared test infrastructure.** Integration tests share helpers (tmux harness, fixture loading, storage setup) that do not belong to any single production package. Putting them in `internal/testutil/` (shared helpers) and `internal/integration/` (test files) keeps concerns separated.

4. **Existing precedent.** The `internal/web/handlers_ws_integration_test.go` already demonstrates integration-style tests in their own domain area. The conductor orchestration tests cross multiple domains, so they need their own home.

**What stays in existing packages:** Tests that exercise internal (unexported) behavior remain where they are. The existing `fork_integration_test.go`, `notifications_integration_test.go`, and `opencode_integration_test.go` in `internal/session/` are fine because they test internal session logic. The new integration tests exercise cross-package behavior.

## Component Responsibilities

| Component | Responsibility | New vs Modified |
|-----------|---------------|-----------------|
| `internal/integration/` | Cross-package integration test files | **NEW** |
| `internal/integration/testmain_test.go` | Profile isolation, tmux cleanup | **NEW** |
| `internal/testutil/tmux.go` | Shared tmux session helpers (create, cleanup, wait-for-prompt) | **NEW** |
| `internal/testutil/storage.go` | Shared test DB setup (extracted from `session/storage_test.go` pattern) | **NEW** |
| `internal/testutil/fixtures.go` | Fixture loading and config builder helpers | **NEW** |
| `internal/testutil/gitenv.go` | Git env cleanup (existing, unchanged) | **EXISTING** |

## Recommended Project Structure

```
internal/
├── integration/                    # Integration test package (NEW)
│   ├── testmain_test.go            # TestMain: AGENTDECK_PROFILE=_test, tmux cleanup
│   ├── helpers_test.go             # Package-local test helpers
│   ├── conductor_test.go           # Conductor setup, meta.json, orchestration pipeline
│   ├── lifecycle_test.go           # Session start/stop/fork/restart with real tmux
│   ├── events_test.go              # StatusEvent write/watch cycle across sessions
│   ├── multitool_test.go           # Tool-specific behavior (Claude, Gemini, OpenCode, Codex)
│   ├── sleep_detection_test.go     # PromptDetector accuracy with real tmux pane content
│   ├── skills_test.go              # Skills loading, attachment, triggering
│   └── send_test.go                # session send/output CLI pipeline
│
├── testutil/                       # Shared test helpers (EXTENDED)
│   ├── gitenv.go                   # Existing: git env cleanup
│   ├── tmux.go                     # NEW: TmuxHarness for session lifecycle
│   ├── storage.go                  # NEW: TestStorage builder
│   └── fixtures.go                 # NEW: Fixture helpers (conductor meta, config, etc.)
│
├── session/                        # Existing (unchanged test files)
│   ├── testmain_test.go            # Existing profile isolation
│   ├── conductor_test.go           # Existing unit tests for conductor logic
│   ├── fork_integration_test.go    # Existing fork flow tests
│   └── ...
│
└── tmux/                           # Existing (unchanged test files)
    ├── testmain_test.go            # Existing profile isolation
    └── ...
```

### Structure Rationale

- **`internal/integration/`:** Dedicated package for cross-cutting integration tests. Uses `_test.go` suffix convention so Go only compiles these during `go test`. Imports from `session`, `tmux`, `statedb`, and `cmd/agent-deck` freely.
- **`internal/testutil/`:** Shared helpers used by both the new integration package and existing package-level tests. Extracted patterns from the duplicated `newTestStorage()` and `skipIfNoTmuxServer()` functions that currently exist in both `session/` and `tmux/` testmain files.

## Architectural Patterns

### Pattern 1: TmuxHarness (Session Lifecycle Management)

**What:** A test helper that manages tmux session creation, cleanup, and provides assertion-friendly APIs for pane content inspection.
**When to use:** Any integration test that needs real tmux sessions.
**Trade-offs:** Requires a running tmux server (tests skip gracefully without one via `skipIfNoTmuxServer`). Slower than mocks but catches real subprocess behavior.

**Example:**
```go
// internal/testutil/tmux.go
package testutil

import (
    "os/exec"
    "testing"
    "time"
    "github.com/asheshgoplani/agent-deck/internal/tmux"
)

// TmuxHarness manages tmux sessions for integration tests.
// All sessions use a unique prefix to avoid collision with production sessions.
type TmuxHarness struct {
    t        *testing.T
    sessions []string // track created sessions for cleanup
}

func NewTmuxHarness(t *testing.T) *TmuxHarness {
    t.Helper()
    SkipIfNoTmuxServer(t)
    h := &TmuxHarness{t: t}
    t.Cleanup(h.cleanup)
    return h
}

// CreateSession creates a tmux session running the given command.
// Returns the tmux.Session wrapper. Session is killed on test cleanup.
func (h *TmuxHarness) CreateSession(name, workDir, command string) *tmux.Session {
    h.t.Helper()
    sess := tmux.NewSession(name, workDir)
    if err := sess.Start(command); err != nil {
        h.t.Fatalf("TmuxHarness.CreateSession(%q): %v", name, err)
    }
    h.sessions = append(h.sessions, sess.Name)
    return sess
}

// WaitForContent polls pane content until predicate returns true or timeout.
func (h *TmuxHarness) WaitForContent(sess *tmux.Session, predicate func(string) bool, timeout time.Duration) string {
    // ... poll CapturePane until predicate matches or timeout
}

func (h *TmuxHarness) cleanup() {
    for _, name := range h.sessions {
        _ = exec.Command("tmux", "kill-session", "-t", name).Run()
    }
}

// SkipIfNoTmuxServer is a shared version of the duplicated function.
func SkipIfNoTmuxServer(t *testing.T) {
    t.Helper()
    if _, err := exec.LookPath("tmux"); err != nil {
        t.Skip("tmux not available")
    }
    if err := exec.Command("tmux", "list-sessions").Run(); err != nil {
        t.Skip("tmux server not running")
    }
}
```

### Pattern 2: TestStorage (Isolated Database per Test)

**What:** Factory function for creating isolated SQLite test databases, extracted from the pattern already in `internal/session/storage_test.go`.
**When to use:** Any integration test that needs to persist session data.
**Trade-offs:** Each test gets its own temp directory and DB. Slightly slower than in-memory but matches production behavior. Uses `t.TempDir()` for automatic cleanup.

**Example:**
```go
// internal/testutil/storage.go
package testutil

import (
    "testing"
    "path/filepath"
    "github.com/asheshgoplani/agent-deck/internal/session"
    "github.com/asheshgoplani/agent-deck/internal/statedb"
)

// NewTestStorage creates a Storage backed by a temp-dir SQLite database.
// The database and directory are automatically cleaned up when the test finishes.
func NewTestStorage(t *testing.T) *session.Storage {
    t.Helper()
    tmpDir := t.TempDir()
    dbPath := filepath.Join(tmpDir, "state.db")
    db, err := statedb.Open(dbPath)
    if err != nil {
        t.Fatalf("NewTestStorage: open db: %v", err)
    }
    if err := db.Migrate(); err != nil {
        t.Fatalf("NewTestStorage: migrate: %v", err)
    }
    t.Cleanup(func() { db.Close() })
    return session.NewStorageForTest(db, dbPath, "_test")
}
```

**Important:** This requires exposing a `NewStorageForTest` constructor from the `session` package (or using an existing one). The current `newTestStorage` is unexported and package-internal. The integration package needs a public version. Alternatively, the integration tests can call `statedb.Open` + `statedb.Migrate` directly and construct instances without going through `Storage`.

### Pattern 3: Fixture Builders (Conductor Meta, Instances, Config)

**What:** Builder helpers that construct test conductor metadata, instances, and config files with sensible defaults, overridable via functional options.
**When to use:** Tests that need conductor setup, multi-session scenarios, or tool-specific configurations.
**Trade-offs:** More setup code upfront, but dramatically reduces boilerplate in test functions.

**Example:**
```go
// internal/testutil/fixtures.go
package testutil

import (
    "os"
    "path/filepath"
    "testing"
    "encoding/json"
    "github.com/asheshgoplani/agent-deck/internal/session"
)

// ConductorFixture creates a conductor directory with meta.json in a temp dir.
// Returns the conductor name and the base directory path.
func ConductorFixture(t *testing.T, name, profile string) string {
    t.Helper()
    tmpDir := t.TempDir()

    // Override HOME so conductor functions use our temp dir
    conductorDir := filepath.Join(tmpDir, ".agent-deck", "conductor", name)
    if err := os.MkdirAll(conductorDir, 0755); err != nil {
        t.Fatalf("ConductorFixture: mkdir: %v", err)
    }

    meta := &session.ConductorMeta{
        Name:             name,
        Profile:          profile,
        HeartbeatEnabled: false,
        CreatedAt:        "2026-01-01T00:00:00Z",
    }
    data, _ := json.MarshalIndent(meta, "", "  ")
    if err := os.WriteFile(filepath.Join(conductorDir, "meta.json"), data, 0644); err != nil {
        t.Fatalf("ConductorFixture: write meta.json: %v", err)
    }

    return tmpDir
}

// InstanceBuilder provides a fluent API for creating test instances.
type InstanceBuilder struct {
    inst *session.Instance
}

func NewInstanceBuilder(title, projectPath string) *InstanceBuilder {
    return &InstanceBuilder{inst: session.NewInstance(title, projectPath)}
}

func (b *InstanceBuilder) WithTool(tool string) *InstanceBuilder {
    b.inst.Tool = tool
    return b
}

func (b *InstanceBuilder) WithStatus(status session.Status) *InstanceBuilder {
    b.inst.Status = status
    return b
}

func (b *InstanceBuilder) Build() *session.Instance {
    return b.inst
}
```

### Pattern 4: Mock vs Real Subprocess Strategy

**What:** A clear decision tree for when to use mock interfaces vs real tmux subprocesses.
**When to use:** Every integration test design decision.

**Decision tree:**

```
Need to test? ────────────────────────────────────────────────────────┐
    │                                                                  │
    ├── Prompt/status detection logic?                                 │
    │       └── Use mock PromptDetector (already tested in unit tests) │
    │                                                                  │
    ├── tmux pane content parsing?                                     │
    │       └── Use mock pane content (strings, not real tmux)         │
    │                                                                  │
    ├── Full session lifecycle (start, run, detect status, stop)?      │
    │       └── Use REAL tmux with TmuxHarness                        │
    │                                                                  │
    ├── Conductor orchestration pipeline?                              │
    │       └── Use REAL tmux for session creation                     │
    │           + mock tool (echo/sleep instead of claude/gemini)      │
    │                                                                  │
    ├── Send/output CLI pipeline?                                      │
    │       └── Use REAL tmux (existing pattern in session_send_test)  │
    │           + mock statusChecker interface (already exists)        │
    │                                                                  │
    ├── Event watcher/writer cycle?                                    │
    │       └── Use filesystem events in temp dirs (existing pattern)  │
    │                                                                  │
    └── Storage persistence?                                           │
            └── Use real SQLite in t.TempDir() (existing pattern)     │
                                                                       │
────────────────────────────────────────────────────────────────────────┘
```

**Key principle:** Use real tmux for session lifecycle tests (start, kill, exists, capture-pane), but use simple commands (`echo`, `sleep`, `cat`) instead of actual AI tools. This tests the plumbing without requiring Claude/Gemini API keys.

## Data Flow

### Integration Test Execution Flow

```
TestMain (testmain_test.go)
    │
    ├── os.Setenv("AGENTDECK_PROFILE", "_test")
    ├── testutil.UnsetGitRepoEnv()
    │
    └── m.Run()
         │
         ├── TestConductorPipeline
         │     ├── testutil.NewTmuxHarness(t)         // real tmux
         │     ├── testutil.ConductorFixture(t, ...)   // temp conductor dir
         │     ├── session.SetupConductor(...)          // production function
         │     ├── instance.Start()                     // creates tmux session
         │     ├── harness.WaitForContent(...)          // wait for prompt
         │     ├── tmuxSession.SendKeysAndEnter(...)    // send message
         │     ├── harness.WaitForContent(...)          // verify response
         │     └── instance.Kill()                      // cleanup (also in t.Cleanup)
         │
         ├── TestSessionLifecycle
         │     ├── testutil.NewTmuxHarness(t)
         │     ├── session.NewInstanceWithTool(...)
         │     ├── instance.Start() / Kill() / Restart()
         │     └── assert status transitions
         │
         └── TestCrossSessionEvents
               ├── session.NewStatusEventWatcher("")
               ├── session.WriteStatusEvent(event)
               └── watcher.WaitForStatus(...)
```

### Status Detection Integration Flow (What Tests Must Validate)

```
tmux session running command
    │
    ├── tmux.CapturePane() ←── real tmux subprocess
    │       │
    │       └── pane content string
    │               │
    │               ├── PromptDetector.HasPrompt()   → "waiting"
    │               ├── BusyDetector.IsBusy()        → "running"
    │               └── neither                       → "idle" or "starting"
    │
    ├── StatusEvent written to ~/.agent-deck/events/
    │       │
    │       └── StatusEventWatcher picks up via fsnotify
    │               │
    │               └── EventCh() delivers to subscriber
    │
    └── Instance.UpdateStatus() sets inst.Status
```

## Test Isolation Strategy

### Profile Isolation (Critical)

Every test package MUST have a `TestMain` that sets `AGENTDECK_PROFILE=_test`. This is already enforced in existing packages. The new `internal/integration/testmain_test.go` must follow the same pattern.

```go
// internal/integration/testmain_test.go
package integration

import (
    "os"
    "testing"
    "github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
    testutil.UnsetGitRepoEnv()
    os.Setenv("AGENTDECK_PROFILE", "_test")
    code := m.Run()
    // Cleanup any orphaned integration test tmux sessions
    cleanupIntegrationSessions()
    os.Exit(code)
}
```

### tmux Session Naming (Prevents Collisions)

Integration test sessions should use a distinguishable prefix pattern:

```
agentdeck_inttest_{testname}_{unique_suffix}
```

The `TmuxHarness` creates sessions via `tmux.NewSession()` which already uses `agentdeck_` prefix + unique hex suffix. Integration tests should use display names like `inttest-lifecycle-start` to make identification clear.

### Filesystem Isolation

Tests that interact with conductor directories, event files, or config files must use `t.TempDir()` and override relevant paths. The existing patterns use:
- Temp dirs for event watcher tests (override `watcher.eventsDir`)
- Temp dirs for storage tests (via `statedb.Open(tempPath)`)
- Env var overrides for `CLAUDE_CONFIG_DIR`

The same patterns apply to conductor tests, which need to override `~/.agent-deck/conductor/` to a temp dir. This can be done by setting `AGENTDECK_HOME` or by having conductor functions accept a base directory parameter for testability.

### Database Isolation

Each test that needs storage creates its own SQLite DB in `t.TempDir()`. No shared state between tests. The existing `newTestStorage` pattern in `session/storage_test.go` is the model.

## Integration Points

### Integration Between Test Infrastructure and Production Code

| Integration Point | Direction | Method | Notes |
|-------------------|-----------|--------|-------|
| `testutil.TmuxHarness` -> `tmux.Session` | Test -> Production | Creates real `tmux.Session` objects | Uses production `NewSession()` and `Start()` |
| `testutil.NewTestStorage` -> `statedb` | Test -> Production | Opens real SQLite database | Uses production `statedb.Open()` and `Migrate()` |
| Integration tests -> `session.Instance` | Test -> Production | Creates instances, calls `Start()`, `Kill()` | Tests production lifecycle |
| Integration tests -> `session.SetupConductor` | Test -> Production | Sets up conductor in temp dir | Tests production conductor setup |
| Integration tests -> `session.StatusEventWatcher` | Test -> Production | Creates watcher, writes events | Tests production event system |
| Integration tests -> `tmux.PromptDetector` | Test -> Production | Tests prompt detection accuracy | Uses production detectors |

### New Public APIs Needed

To enable the integration package to construct test objects, a few small public constructors or interfaces may need to be exposed:

| What | Currently | Needed |
|------|-----------|--------|
| `session.Storage` construction for tests | `newTestStorage` (unexported, in `session` package) | Either export it or have integration tests construct via `statedb` directly |
| `session.Instance` with tmux session | `NewInstanceWithTool` exists (public) | Already sufficient, no change needed |
| Conductor base dir override | Hardcoded to `~/.agent-deck/conductor` | Tests use `t.Setenv("HOME", tmpDir)` or override via env var |
| Event dir override | `GetEventsDir()` returns fixed path | Tests use `t.Setenv("HOME", tmpDir)` to redirect |

### Internal Boundaries

| Boundary | Communication | Notes |
|----------|---------------|-------|
| `integration/` -> `session/` | Public API calls (`NewInstance`, `Start`, `Kill`) | Tests only use exported functions |
| `integration/` -> `tmux/` | Public API calls (`NewSession`, `CapturePane`) | Tests only use exported functions |
| `integration/` -> `statedb/` | Public API calls (`Open`, `Migrate`) | For database setup only |
| `integration/` -> `testutil/` | Helper calls (`TmuxHarness`, `SkipIfNoTmuxServer`) | Shared infrastructure |
| Existing `session/*_test.go` -> `testutil/` | Helper calls (migrate `skipIfNoTmuxServer`) | Can optionally refactor to use shared version |

## Build Order (Dependencies Between Test Infrastructure and Tests)

The integration testing framework should be built in this order, because each layer depends on the previous:

### Layer 1: Shared Test Utilities (`internal/testutil/`)

Build first because everything else depends on these helpers.

1. `testutil/tmux.go` with `SkipIfNoTmuxServer()` and `TmuxHarness`
2. `testutil/storage.go` with `NewTestStorage()` (if storage export path chosen)
3. `testutil/fixtures.go` with `ConductorFixture()` and `InstanceBuilder`

**Dependencies:** `internal/tmux`, `internal/session`, `internal/statedb` (all existing)

### Layer 2: Integration Package Scaffold (`internal/integration/`)

Set up the package with TestMain and verify it can import everything.

1. `integration/testmain_test.go` with profile isolation
2. `integration/helpers_test.go` with any package-local helpers

**Dependencies:** Layer 1 (`testutil`)

### Layer 3: Session Lifecycle Tests

The foundation. Other tests build on reliable session start/stop.

1. `integration/lifecycle_test.go`
   - `TestSessionStart_CreatesRealTmuxSession` (start shell, verify exists)
   - `TestSessionKill_RemovesTmuxSession` (start, kill, verify gone)
   - `TestSessionRestart_RecreatesSession` (start, restart, verify alive)
   - `TestSessionFork_CreatesChildSession` (start parent, fork, verify both exist)
   - `TestSessionStartWithMessage_DeliversMessage` (start with initial prompt)

**Dependencies:** Layer 2 (scaffold), `tmux.Session.Start/Kill/Exists`

### Layer 4: Status Detection and Events

Tests for the status lifecycle and event system.

1. `integration/sleep_detection_test.go`
   - `TestPromptDetection_RealTmuxOutput` (run commands, verify detection from real pane)
   - `TestStatusTransition_StartingToRunning` (start with command, verify status progression)
   - `TestStatusTransition_RunningToWaiting` (command finishes, verify waiting detected)

2. `integration/events_test.go`
   - `TestStatusEventWriteAndWatch` (write event, verify watcher receives it)
   - `TestCrossSessionEventNotification` (session A writes event, watcher for session B filters correctly)
   - `TestEventCleanupStaleFiles` (create old events, verify cleanup)

**Dependencies:** Layer 3 (lifecycle works), `session.StatusEventWatcher`, `session.WriteStatusEvent`

### Layer 5: Conductor Orchestration

The most complex tests, requiring everything below to work.

1. `integration/conductor_test.go`
   - `TestConductorSetup_CreatesMetaAndDirectory` (SetupConductor, verify files)
   - `TestConductorList_FindsAllConductors` (create multiple, list, verify)
   - `TestConductorSession_Lifecycle` (create conductor session, start, verify status)
   - `TestConductorClearOnCompact_Behavior` (test clear-on-compact flag interaction)

**Dependencies:** Layer 3 + 4, `session.SetupConductor`, `session.LoadConductorMeta`

### Layer 6: Multi-Tool and Send/Output

Tool-specific behavior and the send pipeline.

1. `integration/multitool_test.go`
   - `TestClaudeToolSession_CommandConstruction` (verify built command includes expected flags)
   - `TestGeminiToolSession_CommandConstruction`
   - `TestShellToolSession_DirectCommand`

2. `integration/send_test.go`
   - `TestSessionSend_DeliversToTmuxPane` (start session, send text, verify in pane)
   - `TestSessionOutput_RetrievesContent` (start session, run command, get output)

3. `integration/skills_test.go`
   - `TestSkillsLoading_FindsSkillFiles`
   - `TestSkillsAttachment_AppendsToConfig`

**Dependencies:** Layer 3 + 4, tool-specific session logic

## Anti-Patterns

### Anti-Pattern 1: Testing Against Real AI Tools

**What people do:** Start a real Claude Code or Gemini CLI session in integration tests.
**Why it's wrong:** Requires API keys (can't run in CI or by contributors), slow, non-deterministic, expensive. Also violates the "public repo: no API keys" constraint.
**Do this instead:** Use simple shell commands (`echo "prompt text"`, `sleep 5`, `cat`) that simulate the prompt/response pattern. Test the plumbing (tmux management, status detection, event routing), not the AI tool itself.

### Anti-Pattern 2: Broad tmux Session Cleanup Patterns

**What people do:** `tmux kill-session` with patterns like `HasPrefix("agentdeck_test")` or `Contains("test")`.
**Why it's wrong:** Kills real user sessions with "test" in their title. This happened in production (see 2025-12-11 incident in CLAUDE.md).
**Do this instead:** Track created session names explicitly in `TmuxHarness.sessions` and kill only those specific sessions. The existing `cleanupTestSessions()` pattern only targets the exact known artifact name `"Test-Skip-Regen"`.

### Anti-Pattern 3: Shared State Between Tests

**What people do:** Use a single tmux session or database across multiple tests for "efficiency".
**Why it's wrong:** Tests become order-dependent, flaky, and hard to debug. One test's failure contaminates others.
**Do this instead:** Each test creates its own tmux sessions and database. Use `t.TempDir()` for filesystem isolation and `t.Cleanup()` for deterministic teardown. The slight performance cost is worth the reliability.

### Anti-Pattern 4: Polling with Fixed Sleep

**What people do:** `time.Sleep(2 * time.Second)` then check state.
**Why it's wrong:** Too slow on fast machines, too fast on slow machines, wastes CI time.
**Do this instead:** Poll with short intervals and a timeout. The `WaitForContent` pattern in `TmuxHarness` polls every 100ms with a configurable timeout. The existing `event_watcher_test.go` uses `select` with `time.After` for event delivery, which is the correct pattern.

## Sources

- Agent-deck codebase analysis (direct code reading, HIGH confidence)
- Existing test patterns in `internal/session/testmain_test.go`, `internal/tmux/testmain_test.go`, `cmd/agent-deck/testmain_test.go`
- Existing integration test patterns in `internal/session/fork_integration_test.go`, `internal/session/notifications_integration_test.go`, `internal/web/handlers_ws_integration_test.go`
- Go testing best practices: `testing.Short()` for long-running tests, `t.TempDir()` for cleanup, `t.Cleanup()` for deferred teardown
- CLAUDE.md incident history: profile isolation requirements, tmux session safety, test data corruption prevention

---
*Architecture research for: Agent-Deck Integration Testing Framework*
*Researched: 2026-03-06*
