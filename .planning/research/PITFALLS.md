# Pitfalls Research

**Domain:** Integration testing for tmux-based multi-process orchestration (Go)
**Researched:** 2026-03-06
**Confidence:** HIGH (based on codebase analysis of existing test infrastructure, two documented production incidents, Go testing ecosystem knowledge, and community patterns)

## Critical Pitfalls

### Pitfall 1: Test Profile Isolation Failure (Production Data Corruption)

**What goes wrong:**
Integration tests run against the default or active user profile, overwriting real session data. Agent-deck stores sessions in `~/.agent-deck/profiles/{profile}/state.db`. Without explicit profile isolation, tests write to the user's real state.db, destroying production session metadata.

**Why it happens:**
This already happened. On 2025-12-11, tests running with `AGENTDECK_PROFILE=work` overwrote all 36 production sessions. The root cause was that `TestMain` did not force `AGENTDECK_PROFILE=_test` in every package. New test packages are easy to create without realizing they need their own `TestMain`.

**How to avoid:**
1. Every test package that touches session data or storage MUST have a `TestMain` that sets `AGENTDECK_PROFILE=_test`. This pattern already exists in `internal/session/testmain_test.go`, `internal/tmux/testmain_test.go`, `internal/ui/testmain_test.go`, and `cmd/agent-deck/testmain_test.go`.
2. Create a shared `internal/testutil` helper that validates the profile is `_test` at the start of any integration test. Add a build-time check (via `go vet` or custom analyzer) that flags test packages without TestMain.
3. Integration test framework initialization should refuse to proceed if `AGENTDECK_PROFILE != "_test"`.
4. Use `t.TempDir()` for all SQLite databases in tests (existing `newTestStorage()` pattern in `storage_test.go` is correct).

**Warning signs:**
- New test file in a package without `testmain_test.go`
- Tests that import `internal/session` but don't have profile isolation
- State.db file modified during test runs in a non-test profile directory

**Phase to address:**
Phase 1 (Framework Architecture). This is a foundation requirement. The test framework MUST enforce profile isolation as its first invariant.

---

### Pitfall 2: Orphaned tmux Sessions Leaking Resources

**What goes wrong:**
Tests create real tmux sessions (via `session.Start()` or `tmux.NewSession().Start()`) that are not cleaned up when tests fail, panic, or time out. These orphaned sessions accumulate and consume memory. On 2026-01-20, 20+ `Test-Skip-Regen` sessions were orphaned, wasting approximately 3GB of RAM.

**Why it happens:**
1. `defer inst.Kill()` is placed after `inst.Start()`, but if `Start()` partially succeeds (tmux session created, but returned error due to command setup), the defer never registers.
2. `t.Fatal()` / `t.FailNow()` runs deferred functions, but `os.Exit()` does not. If a test helper calls `os.Exit()` or the process is killed, cleanup is skipped.
3. Tests that spawn multiple sessions may clean up the first but fail before cleaning later ones.
4. Test timeout (`go test -timeout`) kills the process without running deferred cleanup.

**How to avoid:**
1. Use `t.Cleanup()` instead of `defer` for tmux session teardown. `t.Cleanup()` is called even when subtests fail, and the cleanup order is well-defined (LIFO).
2. Implement a session registry in the test framework: every created session registers itself, and a `TestMain` cleanup pass kills all registered sessions. The existing `cleanupTestSessions()` in `testmain_test.go` only matches `Test-Skip-Regen` by name, which is too narrow.
3. Use a consistent naming convention for test tmux sessions (e.g., `agentdeck_cptest-{test-name}-{random}`) and have `TestMain` cleanup kill anything matching `agentdeck_cptest-*` (the `controlpipe_test.go` pattern with `cptest-` is good).
4. Add a global test timeout safety net: if any tmux session with the test prefix has existed for more than 5 minutes, kill it unconditionally in TestMain.
5. Never rely on the test process staying alive for cleanup. Always have a second line of defense.

**Warning signs:**
- `tmux list-sessions | grep agentdeck` shows sessions from test runs after tests complete
- RAM usage grows on developer machines over time
- Test names appearing in `tmux list-sessions` that don't match any running test

**Phase to address:**
Phase 1 (Framework Architecture). Build the session registry and naming convention into the framework from day one.

---

### Pitfall 3: Timing-Dependent Tests That Flake Under Load

**What goes wrong:**
Tests use `time.Sleep()` to wait for tmux operations to complete, then assert on the result. These pass on fast machines with low load but fail on slower machines or under parallel test execution. The codebase already has 86 occurrences of `time.Sleep` or `time.After` across 20 test files.

**Why it happens:**
tmux operations are asynchronous by nature. `send-keys` enqueues input but doesn't guarantee when it will be processed. `capture-pane` may return content from before or after a command executes. Session startup takes variable time. The control pipe needs time to connect. Status transitions depend on background polling intervals.

Specific timing hotspots in this codebase:
- `controlpipe_test.go`: 100-300ms sleeps after session creation and key sending
- `status_lifecycle_test.go`: 2-second sleep to wait past 1.5s grace period
- `event_watcher_test.go`: 200ms sleep for watcher startup, 2-second timeout for event delivery
- `tmux_test.go` (flicker test): 100ms sleep after `SendKeys`, manipulates `lastChangeTime` directly

**How to avoid:**
1. Replace `time.Sleep()` with polling/retry loops that check for the expected condition. Use a helper like:
   ```go
   func waitFor(t *testing.T, timeout time.Duration, condition func() bool, msg string) {
       t.Helper()
       deadline := time.Now().Add(timeout)
       for time.Now().Before(deadline) {
           if condition() {
               return
           }
           time.Sleep(50 * time.Millisecond)
       }
       t.Fatalf("timeout waiting for: %s", msg)
   }
   ```
2. For tmux content assertions, poll `capture-pane` until the expected content appears (with timeout), rather than sleeping then asserting.
3. For status transitions, poll `UpdateStatus()` in a loop rather than sleeping for a fixed duration.
4. Use channels and `select` with `time.After` for event-driven assertions (the `event_watcher_test.go` pattern with `select/case` is correct).
5. Set generous timeouts (5-10 seconds) for CI but keep poll intervals tight (50-100ms) so tests finish fast when conditions are met quickly.

**Warning signs:**
- Tests pass 9 out of 10 times
- Tests fail more often during parallel test runs or on CI
- Magic sleep durations in test code (especially anything under 500ms or round numbers like `time.Sleep(2 * time.Second)`)

**Phase to address:**
Phase 1 (Framework Architecture). Define the polling helpers as part of the test framework, then Phase 2+ uses them consistently.

---

### Pitfall 4: Package-Level Global State Causing Test Interference

**What goes wrong:**
Tests in the same package mutate shared global state (package-level variables), causing tests that pass individually to fail when run together or in different orders. In agent-deck, the `internal/tmux` package has critical shared state: `sessionCacheData`, `sessionCacheMu`, `windowCacheData`, and the `PipeManager` singleton. The `internal/session` package has package-level loggers.

**Why it happens:**
Go tests in the same package share the same process. Package-level variables like `sessionCacheData` persist across test functions. When one test populates the session cache (via `RefreshSessionCache()`) and another test expects an empty cache, the second test fails. The session cache has a 2-second TTL (`time.Since(sessionCacheTime) > 2*time.Second`), creating a window where tests interfere.

`t.Parallel()` makes this worse because multiple tests access the same global cache concurrently. Even with mutex protection, the logical state (what's in the cache) can be wrong for a given test's expectations.

**How to avoid:**
1. Reset global caches in a `t.Cleanup()` function at the start of each integration test that interacts with tmux. Create a `resetTmuxState(t)` helper.
2. Do NOT use `t.Parallel()` for integration tests that create real tmux sessions. The tmux server is a shared resource and parallelizing against it creates ordering dependencies.
3. Consider using a separate tmux socket for each test (or test suite) via `TMUX_TMPDIR` to fully isolate tmux state.
4. For the `PipeManager` singleton: ensure tests that create pipes also close them, and don't leak connections to sessions that other tests may kill.

**Warning signs:**
- Tests pass with `go test -v ./internal/tmux/...` but fail with `go test -v ./...`
- Adding or removing a test causes unrelated tests to fail
- Tests fail only when run in parallel but pass sequentially
- `-race` flag catches data races on package-level variables

**Phase to address:**
Phase 1 (Framework Architecture). Define state reset helpers. Phase 2+ tests must use them.

---

### Pitfall 5: Tests That Work Locally But Skip or Fail in CI

**What goes wrong:**
Integration tests require a running tmux server, specific CLI tools (claude, gemini, opencode, codex), and macOS-specific features. In CI, tmux may be installed but not running a server. The existing `skipIfNoTmuxServer(t)` pattern handles this, but integration tests that require real tool binaries (like `TestOpenCodeDetectionE2E`) are inherently non-portable.

**Why it happens:**
The project has two classes of tests with different requirements:
1. **tmux-required**: Need a running tmux server (handled by `skipIfNoTmuxServer`)
2. **tool-required**: Need specific CLI tools installed (opencode, claude, etc.)
3. **environment-required**: Need specific files/directories (e.g., `/Users/ashesh/claude-deck` hardcoded in `opencode_e2e_test.go`)

When all integration tests skip in CI, the test suite provides zero integration coverage in automated environments. This creates a false sense of security where "all tests pass" means "all tests were skipped."

**How to avoid:**
1. Separate integration tests into tiers:
   - **Tier 1 (mock tmux):** Tests that use a tmux mock or simulate tmux behavior. Run everywhere.
   - **Tier 2 (real tmux):** Tests that need a real tmux server. Skip in CI, run locally.
   - **Tier 3 (full stack):** Tests that need real tool binaries. Manual-only, skip in CI.
2. Use Go build tags to separate tiers: `//go:build integration` for Tier 2, `//go:build e2e` for Tier 3.
3. Never hardcode absolute paths like `/Users/ashesh/claude-deck`. Use `t.TempDir()` or detect project root dynamically.
4. Track skip counts in CI: if > 50% of tests skip, the run should be flagged for attention.
5. Design integration test helpers that can operate in "stub mode" when tmux is unavailable, testing the orchestration logic without the subprocess layer.

**Warning signs:**
- CI shows "all tests pass" but actually 80% skipped
- `opencode_e2e_test.go` line 31: `projectPath := "/Users/ashesh/claude-deck"` (hardcoded local path)
- Regressions caught locally weeks after merge because CI never ran the integration tests

**Phase to address:**
Phase 1 (Framework Architecture). Define test tiers and build tags. Phase 2 implements Tier 1 mock-based tests. Phase 3+ adds Tier 2 real tmux tests.

---

### Pitfall 6: Over-Coupling Tests to Internal State Manipulation

**What goes wrong:**
Tests directly manipulate internal struct fields (e.g., `session.stateTracker.lastChangeTime`, `session.stateTracker.acknowledged`, `session.mu.Lock()`) to set up preconditions. When the implementation changes (field renamed, state machine redesigned), dozens of tests break even though the behavior being tested hasn't changed.

**Why it happens:**
It's the path of least resistance. In `tmux_test.go` line 1674-1677, the test directly locks the mutex and sets internal state tracker fields. In `fork_integration_test.go`, the test directly sets `parent.ClaudeSessionID`. This feels efficient but creates tight coupling between tests and implementation details.

**How to avoid:**
1. Create test-only setup methods on types: `func (s *Session) SetTestState(status string, lastChange time.Time)` in a `_test.go` file. These methods encapsulate the internal state setup and only need updating in one place when internals change.
2. Prefer driving state through the public API where possible. Instead of `session.stateTracker.acknowledged = false`, use the public method that would cause that state change.
3. For conductor tests: create test fixture builders that produce valid orchestration state without touching internals.
4. Accept that some internal access is necessary in integration tests (this is Go, not Java). The goal is to minimize the surface area and centralize it.

**Warning signs:**
- Tests that import unexported types or access unexported fields (only possible within the same package)
- A single struct field rename breaks 10+ tests
- Test setup code that's longer than the actual assertion

**Phase to address:**
Phase 2 (Test Implementation). Define test helpers and fixture builders alongside the tests.

---

### Pitfall 7: Cleanup Pattern Leaks When Tests Create Multiple Resources

**What goes wrong:**
An integration test creates several related resources (tmux session + SQLite database + temp directory + control pipe + event watcher). If the test fails partway through setup, some resources are cleaned up (via earlier `defer` / `t.Cleanup()`) but others are not (because the code hadn't reached their cleanup registration yet).

**Why it happens:**
The typical pattern:
```go
sess := createSession(t)     // OK, t.Cleanup registered
pipe := connectPipe(t, sess) // OK, t.Cleanup registered
watcher := startWatcher(t)   // FAIL here: pipe and sess cleaned up, but watcher is half-started
```

If `startWatcher` panics after creating a goroutine but before registering cleanup, the goroutine leaks. The existing `StatusEventWatcher` in `event_watcher_test.go` uses `defer watcher.Stop()` plus a goroutine (`go watcher.Start()`), which is leak-prone if the test fails between `Start()` and the eventual `Stop()`.

**How to avoid:**
1. Create a "test environment" struct that bundles all resources and has a single `Cleanup()` method:
   ```go
   type TestEnv struct {
       Session  *tmux.Session
       Storage  *session.Storage
       Watcher  *session.StatusEventWatcher
       TempDir  string
   }
   func NewTestEnv(t *testing.T) *TestEnv { ... }
   ```
   Register `TestEnv.Cleanup()` via `t.Cleanup()` immediately at the top.
2. Use `t.Cleanup()` over `defer` everywhere. `t.Cleanup()` runs even if the test is in a subtest, and cleanup functions are called in reverse registration order.
3. For goroutines: always start them within a helper that also registers their shutdown. Use `context.Context` cancellation for clean goroutine shutdown.
4. Use `goleak` (uber-go/goleak) in TestMain to detect goroutine leaks across the test suite.

**Warning signs:**
- "goroutine leak" warnings in test output
- File descriptors left open after test run (check with `lsof`)
- Test process doesn't exit promptly after all tests complete

**Phase to address:**
Phase 1 (Framework Architecture). The `TestEnv` pattern should be defined in Phase 1 and used by all subsequent test implementations.

---

## Technical Debt Patterns

Shortcuts that seem reasonable but create long-term problems.

| Shortcut | Immediate Benefit | Long-term Cost | When Acceptable |
|----------|-------------------|----------------|-----------------|
| Hardcoded sleep durations | Fast to write, tests pass | Flaky tests, slow CI, unpredictable failures | Never in committed tests. OK for one-off debugging |
| Skipping cleanup on test failure | Faster test development | Resource leaks, 3GB RAM waste incident | Never. Always register cleanup before the action |
| Testing against real CLI tools (claude, opencode) | Tests "real" behavior | Tests skip in CI, non-portable, depend on external auth | Only in Tier 3 manual tests with explicit skip guards |
| Directly manipulating struct internals for test setup | Easy precondition setup | Brittle tests that break on refactor | Only through centralized test helper methods, not inline |
| One big test function covering the whole flow | Covers full lifecycle | Impossible to debug which step failed, long runtime | Split into subtests with `t.Run()`, share setup via `TestEnv` |
| Using `os.Setenv()` for test configuration | Simple profile isolation | Race condition with `t.Parallel()`, Go docs prohibit it | Only in `TestMain` (single-threaded), use `t.Setenv()` in tests |

## Integration Gotchas

Common mistakes when connecting to the tmux subprocess layer.

| Integration | Common Mistake | Correct Approach |
|-------------|----------------|------------------|
| tmux `send-keys` | Asserting on pane content immediately after send | Poll `capture-pane` until expected content appears, with timeout |
| tmux session creation | Assuming session is ready after `Start()` returns | Wait for shell prompt detection or specific content before proceeding |
| tmux `capture-pane` | Assuming content is complete and stable | Capture multiple times with normalization (strip ANSI, BEL, etc.) and compare for stability |
| SQLite storage | Using production database path in tests | Always use `t.TempDir()` with `newTestStorage(t)` pattern |
| Control pipe | Assuming pipe is connected after `NewControlPipe()` | Wait for initial handshake, check `pipe.IsAlive()`, handle reconnection |
| Event watcher (fsnotify) | Assuming events fire immediately after file write | Use atomic write pattern (write to .tmp, rename to final path). Still poll with timeout |
| Session cache | Assuming cache reflects current state | Call `RefreshSessionCache()` before checking, or clear cache at test start |
| Profile system | Testing with default profile active | Force `_test` profile in TestMain, validate in test setup |
| Status lifecycle | Assuming a single `UpdateStatus()` reflects the final state | Status depends on cooldown timers, grace periods, and content hash. May need multiple calls with waits |

## Performance Traps

Patterns that work at small scale but fail as test count grows.

| Trap | Symptoms | Prevention | When It Breaks |
|------|----------|------------|----------------|
| Creating a real tmux session per test | Tests pass but take 30+ seconds | Share sessions across subtests where possible; use mock tmux for unit-level logic | At 20+ tmux-requiring tests, total suite time exceeds 2 minutes |
| Polling with `time.Sleep(50ms)` in tight loops | Tests pass fast | Add exponential backoff or jitter to avoid CPU spin | When running 50+ polling tests in parallel |
| Each test creating its own SQLite database | Tests are well isolated | Reuse databases across subtests within a single test function | At 100+ tests, temp directory cleanup time becomes noticeable |
| Running all integration tests with `-race` | Catches real races | The race detector adds 5-10x overhead. Use `-race` in CI but not for quick local iteration | At 50+ integration tests, `-race` run exceeds 5 minutes |
| Subprocess spawning per assertion | Each check calls `tmux capture-pane` | Batch captures and assert on the captured content | At 10+ assertions per test, subprocess overhead dominates |

## Security Mistakes

Domain-specific security issues for test infrastructure.

| Mistake | Risk | Prevention |
|---------|------|------------|
| Committing test fixtures with real API keys or session IDs | Public repo exposure of credentials | Use dummy/generated values. Grep for patterns like `sk_`, `xoxb-`, `PMAK-` in test files |
| Integration tests that connect to real Telegram/Slack/Discord | Accidental notifications to real users during test runs | Stub all external service integrations. The conductor bridge settings (Telegram, Slack, Discord) must never be used in tests |
| Test tmux sessions running real `claude` or `gemini` commands | Unintended API usage, billing charges, context pollution | Integration tests should use `echo` or `sleep` commands, never real AI tool binaries |
| Hardcoded paths like `/Users/ashesh/...` in test files | Leaks username and directory structure | Use `t.TempDir()`, `os.UserHomeDir()`, or relative paths |

## UX Pitfalls

Common mistakes in the developer experience of the test framework itself.

| Pitfall | User Impact | Better Approach |
|---------|-------------|-----------------|
| Unclear error messages on test failure | Developer spends 10 minutes debugging a "false" assertion | Use `assert.Equal(t, expected, actual, "descriptive message about what went wrong and how to debug")` |
| No guidance on which test tier to use | New contributors write E2E tests for everything | Document test tiers in the framework README and enforce via linting/build tags |
| Tests that require specific tmux state | Developer must manually set up tmux before running tests | Tests should be self-contained: create what they need, clean up after |
| Long test output with no structure | `go test -v` produces 500 lines of logs | Use `t.Run()` subtests and structured logging. Name tests descriptively |
| Flaky test with no skip mechanism | CI blocks on intermittent failure | Tag known-flaky tests, provide a way to skip them, and track them for fixing |

## "Looks Done But Isn't" Checklist

Things that appear complete but are missing critical pieces.

- [ ] **Session lifecycle test:** Often missing the "externally killed" case, where tmux session is killed outside of agent-deck and `UpdateStatus()` must detect the loss
- [ ] **Conductor orchestration test:** Often missing the case where a child session crashes mid-task and the conductor must handle the failure
- [ ] **Event watcher test:** Often missing the race where two events fire within the same fsnotify batch and only one is delivered
- [ ] **Status detection test:** Often missing the ANSI-heavy content case where `StripANSI` must normalize before hash comparison
- [ ] **Fork test:** Often missing verification that the forked session's tmux environment variables are correctly set (not just the command string)
- [ ] **Multi-tool test:** Often missing the case where tool detection changes mid-session (user exits claude, starts gemini in same tmux session)
- [ ] **Profile isolation:** Often missing verification that `TestMain` actually ran (a new test file in a new package won't have TestMain)
- [ ] **Cleanup verification:** Often missing the check that no tmux sessions remain after the test suite completes
- [ ] **Cross-session notification test:** Often missing the case where the notification target session no longer exists when the notification fires
- [ ] **Sleep/wait detection test:** Often missing the transition from "starting" status during the 2-minute startup window, which uses different detection logic than steady-state

## Recovery Strategies

When pitfalls occur despite prevention, how to recover.

| Pitfall | Recovery Cost | Recovery Steps |
|---------|---------------|----------------|
| Production data corruption (profile isolation failure) | HIGH | Restore from `state.db` backup if available. Otherwise, sessions must be manually re-added. Add TestMain to the offending package immediately |
| Orphaned tmux sessions | LOW | `tmux list-sessions \| grep agentdeck_cptest \| awk -F: '{print $1}' \| xargs -I{} tmux kill-session -t {}`. Fix the test's cleanup |
| Flaky timing-dependent test | MEDIUM | Temporarily skip with `t.Skip("flaky: #issue-number")`. Replace sleep with polling helper. Verify with 100 runs: `go test -count=100 -run TestName` |
| Global state interference between tests | MEDIUM | Add state reset to the failing test. Run tests sequentially (`-parallel 1`) to confirm the interference, then bisect which test is the polluter |
| CI test coverage blindness (all tests skip) | LOW | Add skip-count reporting to CI. Ensure Tier 1 tests (mock-based) never skip |
| Goroutine leak | MEDIUM | Add `goleak.VerifyTestMain(m)` to TestMain. Use `runtime.NumGoroutine()` before/after tests to detect growth |

## Pitfall-to-Phase Mapping

How roadmap phases should address these pitfalls.

| Pitfall | Prevention Phase | Verification |
|---------|------------------|--------------|
| Profile isolation failure | Phase 1: Framework Architecture | CI check: grep for TestMain in every test package. Runtime guard in test framework init |
| Orphaned tmux sessions | Phase 1: Framework Architecture | Post-suite check: `tmux list-sessions` contains no test-prefixed sessions. Add to TestMain |
| Timing-dependent flakiness | Phase 1: Framework Architecture (define helpers), Phase 2+ (use them) | Run integration tests with `-count=10` in CI. Zero flakes policy |
| Global state interference | Phase 1: Framework Architecture | Run tests with `-race` flag. Run sequentially and in parallel, compare results |
| CI test blindness | Phase 1: Framework Architecture | Track skip percentage per CI run. Alert if > 40% skip rate |
| Over-coupled tests | Phase 2: Test Implementation | Code review: no direct field access to unexported struct fields outside test helpers |
| Multi-resource cleanup | Phase 1: Framework Architecture | Use `goleak` in TestMain. Post-suite resource audit |
| Hardcoded paths | Phase 2: Test Implementation | `grep -r '/Users/' *_test.go` returns zero results |
| Missing edge cases | Phase 2-3: Test Implementation | Coverage report per test domain. Review checklist above |

## Sources

- Agent-deck codebase analysis: `internal/tmux/testmain_test.go`, `internal/session/testmain_test.go` (profile isolation patterns)
- Agent-deck CLAUDE.md: documented incidents (2025-12-11 production data corruption, 2026-01-20 RAM leak)
- Agent-deck existing test patterns: `controlpipe_test.go` (session creation/cleanup), `status_lifecycle_test.go` (timing), `event_watcher_test.go` (fsnotify), `opencode_e2e_test.go` (E2E patterns)
- [Testing Time and Asynchronicities in Go (official Go blog)](https://go.dev/blog/testing-time)
- [Introducing the Go Race Detector (official Go blog)](https://go.dev/blog/race-detector)
- [Test parallelization in Go: t.Parallel() (Mercari Engineering)](https://engineering.mercari.com/en/blog/entry/20220408-how_to_use_t_parallel/)
- [Parallel Table-Driven Tests in Go (Rost Glukhov)](https://www.glukhov.org/post/2025/12/parallel-table-driven-tests-in-go/)
- [Cutting Test Runtime by 60% with Selective Parallelism (Mattermost)](https://mattermost.com/blog/cutting-test-runtime-by-60-with-selective-parallelism-in-go/)
- [Flaky Tests in 2026: Key Causes, Fixes, and Prevention (AccelQ)](https://www.accelq.com/blog/flaky-tests/)
- [Why Your Kubernetes Tests Are Flaky (Testkube)](https://testkube.io/blog/flaky-tests-cicd-kubernetes-infrastructure-issues)

---
*Pitfalls research for: Integration testing framework for tmux-based multi-process orchestration*
*Researched: 2026-03-06*
