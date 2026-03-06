# Stack Research: Integration Testing Framework

**Domain:** Integration testing for Go tmux-based session manager
**Researched:** 2026-03-06
**Confidence:** HIGH

## Recommended Stack

### Core Technologies

No new core dependencies. The integration testing framework should be built entirely on what agent-deck already has, plus Go's standard `testing` package. This is deliberate: adding a test framework dependency (like goconvey, ginkgo, or goblin) would introduce cognitive overhead for zero gain. The existing patterns in the codebase are already well-established and understood.

| Technology | Version | Purpose | Why Recommended |
|------------|---------|---------|-----------------|
| Go `testing` (stdlib) | Go 1.24+ | Test execution, subtests, helpers, cleanup | Already the backbone. `t.Run`, `t.Cleanup`, `t.Helper`, `testing.Short()`, `t.Parallel` cover all integration test lifecycle needs |
| `stretchr/testify` | v1.11.1 (already in go.mod) | Assertions (`assert`) and fail-fast assertions (`require`) | Already used extensively across 80+ test files. `require` for preconditions, `assert` for verification. No reason to switch |
| `golang.org/x/sync` | v0.19.0 (already in go.mod) | `errgroup` for concurrent test orchestration | Already a dependency. `errgroup.Group` with `SetLimit` is the right tool for multi-session parallel test scenarios |
| tmux (system) | 3.x+ | Real session management in integration tests | Integration tests MUST use real tmux. The existing `skipIfNoTmuxServer(t)` pattern handles graceful degradation |

### Supporting Libraries

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `testify/suite` | v1.11.1 (part of testify, already available) | Test suite lifecycle (SetupSuite/TeardownSuite/SetupTest) | Use for conductor orchestration test suites that need shared tmux infrastructure across multiple test methods. NOT for simple one-off integration tests |
| `modernc.org/sqlite` | v1.44.3 (already in go.mod) | In-memory test databases via `t.TempDir()` | Already used in `newTestStorage(t)`. Reuse that pattern for integration tests needing persistence |
| `fsnotify` | v1.9.0 (already in go.mod) | Event watcher tests (filesystem-based cross-session events) | Already used in `StatusEventWatcher` tests. Same pattern applies for conductor event testing |

### Development Tools

| Tool | Purpose | Notes |
|------|---------|-------|
| `go test -run` | Selective test execution | Use `go test -run TestIntegration_ ./...` to run only integration tests |
| `go test -short` | Skip slow integration tests | Already established: `testing.Short()` checks exist in fork_integration_test.go |
| `go test -count=1` | Disable test caching | Critical for integration tests that depend on tmux state; cached results would hide real failures |
| `go test -timeout 120s` | Extended timeout for multi-session tests | Default 30s is too short for conductor orchestration tests that wait for session transitions |
| `go test -v` | Verbose output for debugging | Integration tests should use `t.Logf` liberally to trace session state transitions |

## No New Dependencies Required

This is the key finding. The codebase already contains everything needed:

```
go.mod (existing, no changes needed)
  github.com/stretchr/testify v1.11.1    -> assert, require, suite
  golang.org/x/sync v0.19.0              -> errgroup for parallel orchestration
  modernc.org/sqlite v1.44.3             -> test databases
  github.com/fsnotify/fsnotify v1.9.0    -> event file watching tests
  github.com/gorilla/websocket v1.5.3    -> WebSocket integration tests
```

## Alternatives Considered

| Recommended | Alternative | Why Not |
|-------------|-------------|---------|
| `testing` + `testify` | Ginkgo/Gomega BDD framework | Massive dependency for BDD syntax sugar. Agent-deck's test style is idiomatic Go table-driven tests. Switching would create a split personality in the test codebase |
| `testify/suite` (for complex suites only) | Custom suite implementation | testify/suite is already available (same module). It provides `SetupSuite`/`TeardownSuite` for shared tmux infrastructure, plus per-test `SetupTest`/`TeardownTest` for individual session cleanup |
| Real tmux sessions | Docker containers (testcontainers-go) | tmux is the dependency under test. Wrapping it in Docker adds latency and complexity without value. The existing `skipIfNoTmuxServer(t)` pattern is correct |
| Re-exec subprocess pattern | Interface-based mocking (`exec.Command`) | For CLI command tests (`session start`, `session send`), re-exec gives real subprocess behavior without mocking. For unit tests of internal functions, mocking is fine but already handled |
| `testing.Short()` + `skipIf*` helpers | Build tags (`//go:build integration`) | Build tags require remembering `-tags=integration` and can confuse IDEs. The `skipIf*` pattern is already established and works with standard `go test` invocations |
| `errgroup` | `sync.WaitGroup` | errgroup provides error propagation and context cancellation. For multi-session tests where any session failure should abort the test, errgroup is strictly better |

## What NOT to Use

| Avoid | Why | Use Instead |
|-------|-----|-------------|
| `testcontainers-go` | tmux sessions ARE the thing being tested. Containerizing them adds a layer of indirection that hides real tmux behavior (socket paths, environment variables, pipe mode). The test would test "tmux in Docker" not "tmux on the developer's machine" | Direct tmux subprocess execution with `skipIfNoTmuxServer(t)` |
| Ginkgo/Gomega | Would create two testing styles in one codebase. The 80+ existing test files use `testing.T` + testify. Migration cost is high, value is zero | Standard `testing` + `testify/assert` + `testify/require` |
| `gomock` / `mockery` | Integration tests should exercise real components. Mocking tmux defeats the purpose. Unit tests that need mocking already work fine with manual interface implementations | Real tmux sessions for integration tests; thin interfaces with manual stubs for unit tests |
| `go-testing-interface` (mitchellh) | Adds indirection to use `testing.TB` as a general interface. Not needed when all consumers are test code | Use `testing.TB` directly in test helper signatures |
| `gnomock` | Designed for external services (databases, message queues) in Docker. Agent-deck's dependencies are tmux (local binary) and SQLite (embedded). No Docker services needed | Direct SQLite via `newTestStorage(t)`, direct tmux via `createTestSession(t, name)` |

## Stack Patterns by Test Category

**Session lifecycle integration tests** (start, stop, restart, fork):
- Use real tmux sessions via existing `NewInstance` + `inst.Start()`
- Use `t.Cleanup(func() { _ = inst.Kill() })` for guaranteed cleanup
- Use `skipIfNoTmuxServer(t)` at test entry
- Reference: `status_lifecycle_test.go` is the gold standard

**Conductor orchestration tests** (parent spawns children, sends commands, reads output):
- Use `testify/suite` for shared tmux infrastructure
- `SetupSuite`: create parent tmux session, initialize conductor state
- `SetupTest`: create child sessions per test
- `TeardownTest`: kill child sessions
- `TeardownSuite`: kill parent, clean up conductor state files

**Cross-session event tests** (session A notifies session B):
- Use `fsnotify` + event files (already established in `event_watcher_test.go`)
- Use channels with `select` + `time.After` for event delivery verification
- Reference: `TestStatusEventWatcher_DetectsNewFile` pattern

**CLI command integration tests** (testing `agent-deck session send`, `session output`):
- Use the re-exec subprocess pattern from Go stdlib
- Test binary re-launches itself with `-test.run=TestHelperProcess` and `GO_WANT_HELPER_PROCESS=1`
- This avoids needing to build a separate binary for each test
- Reference: Go stdlib `os/exec/exec_test.go`

**Multi-tool session tests** (Claude, Gemini, OpenCode, Codex):
- Use `skipIfNo{Tool}(t)` helpers (already exist for OpenCode)
- Create `skipIfNoClaude(t)`, `skipIfNoGemini(t)` following same pattern
- Tests gracefully skip when tools are not installed

**Parallel multi-session tests:**
- Use `errgroup.Group` with `SetLimit` to control concurrency
- Each goroutine creates its own tmux session with unique suffix
- Use `errgroup.WithContext` to cancel all sessions if any fails

## Patterns to Build (Not Import)

These are patterns to implement in `internal/testutil/`, not external packages:

### 1. TmuxTestSession helper

```go
// internal/testutil/tmux.go
func CreateTmuxSession(t testing.TB, suffix string) string {
    t.Helper()
    name := tmux.SessionPrefix + "inttest-" + suffix + "-" + randomHex(4)
    cmd := exec.Command("tmux", "new-session", "-d", "-s", name)
    require.NoError(t, cmd.Run())
    t.Cleanup(func() {
        _ = exec.Command("tmux", "kill-session", "-t", name).Run()
    })
    return name
}
```

### 2. WaitForStatus helper

```go
// internal/testutil/wait.go
func WaitForStatus(t testing.TB, inst *session.Instance, want string, timeout time.Duration) {
    t.Helper()
    deadline := time.After(timeout)
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()
    for {
        select {
        case <-deadline:
            t.Fatalf("timeout waiting for status %q, got %q", want, inst.GetStatusThreadSafe())
        case <-ticker.C:
            _ = inst.UpdateStatus()
            if string(inst.GetStatusThreadSafe()) == want {
                return
            }
        }
    }
}
```

### 3. CaptureOutput helper

```go
// internal/testutil/capture.go
func WaitForPaneContent(t testing.TB, sessionName, needle string, timeout time.Duration) string {
    t.Helper()
    deadline := time.After(timeout)
    ticker := time.NewTicker(200 * time.Millisecond)
    defer ticker.Stop()
    for {
        select {
        case <-deadline:
            t.Fatalf("timeout waiting for %q in pane content", needle)
            return ""
        case <-ticker.C:
            out, _ := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p").Output()
            if strings.Contains(string(out), needle) {
                return string(out)
            }
        }
    }
}
```

## Version Compatibility

| Package | Compatible With | Notes |
|---------|-----------------|-------|
| `testify v1.11.1` | Go 1.19+ | Latest stable v1. No v2 planned. Published Aug 2025 |
| `golang.org/x/sync v0.19.0` | Go 1.18+ | errgroup stable API. No breaking changes expected |
| `modernc.org/sqlite v1.44.3` | Go 1.21+ | CGO-free. Matches project's existing usage |
| tmux 3.x | macOS/Linux | Agent-deck's tmux abstraction layer handles version differences |

## Integration with Existing Test Infrastructure

Critical: the integration test framework MUST preserve these existing patterns:

| Existing Pattern | Location | Integration Requirement |
|-----------------|----------|------------------------|
| `TestMain` with `AGENTDECK_PROFILE=_test` | `*/testmain_test.go` (4 files) | All new test packages MUST include TestMain. Use `internal/testutil` helpers |
| `skipIfNoTmuxServer(t)` | `session/testmain_test.go`, `tmux/testmain_test.go` | Extract to `internal/testutil/skip.go` for reuse across packages |
| `cleanupTestSessions()` | `session/testmain_test.go` | Extend to clean up integration test sessions (pattern: `agentdeck_inttest-*`) |
| `newTestStorage(t)` | `session/storage_test.go` | Reuse directly; do not create a competing pattern |
| `createTestSession(t, suffix)` | `tmux/controlpipe_test.go` | Generalize into `internal/testutil/tmux.go` so all packages can create test tmux sessions |
| `t.Cleanup` for tmux sessions | Throughout | Always use `t.Cleanup` (not `defer`) for tmux session teardown. It survives `t.Parallel()` and runs even on `t.FailNow()` |

## Sources

- [Go stdlib os/exec test patterns](https://go.dev/src/os/exec/exec_test.go) -- Re-exec subprocess pattern (HIGH confidence)
- [Re-exec testing Go subprocesses](https://rednafi.com/go/test-subprocesses/) -- Practical re-exec guide (MEDIUM confidence)
- [testify/suite docs](https://pkg.go.dev/github.com/stretchr/testify/suite) -- Suite lifecycle methods (HIGH confidence)
- [testify assert/require docs](https://pkg.go.dev/github.com/stretchr/testify/assert) -- Assertion patterns (HIGH confidence)
- [errgroup docs](https://pkg.go.dev/golang.org/x/sync/errgroup) -- Concurrent test orchestration (HIGH confidence)
- [t.Cleanup vs defer in parallel tests](https://brandur.org/fragments/go-prefer-t-cleanup-with-parallel-subtests) -- Why t.Cleanup is better for tmux session teardown (MEDIUM confidence)
- [Go build tags for test separation](https://mickey.dev/posts/go-build-tags-testing/) -- Evaluated and rejected in favor of skipIf* pattern (MEDIUM confidence)
- [tmux programmatic testing patterns](https://www.drmaciver.com/2015/05/using-tmux-to-test-your-console-applications/) -- Using tmux as test infrastructure (MEDIUM confidence)

---
*Stack research for: Integration testing framework for agent-deck*
*Researched: 2026-03-06*
