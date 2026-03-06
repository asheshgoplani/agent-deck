# Feature Research

**Domain:** Integration testing framework for multi-process tmux-based orchestration (Go)
**Researched:** 2026-03-06
**Confidence:** HIGH

## Feature Landscape

### Table Stakes (Tests You Must Have)

Features that define a credible integration test framework. Without these, the test suite cannot verify the core product functionality (conductor orchestration, cross-session coordination, multi-tool support).

| Feature | Why Expected | Complexity | Notes |
|---------|--------------|------------|-------|
| tmux session test fixture lifecycle | Every integration test needs managed tmux sessions. Without create/cleanup, tests leak sessions or collide. The existing `createTestSession` in `controlpipe_test.go` is a good starting point but lacks cross-package reuse. | MEDIUM | Build a shared `testutil/tmux.go` helper. Must use `t.Cleanup()` for automatic teardown. Must prefix names distinctively (e.g., `agentdeck_it_`) to avoid collision with user sessions. Must call `skipIfNoTmuxServer(t)` at entry. |
| Session lifecycle integration tests | Agent-deck's core loop: `start -> running -> waiting -> idle`. Unit tests mock this, but integration tests must verify real tmux sessions transition status correctly via pane capture and busy-indicator detection. | MEDIUM | Reuse existing `NewSession`/`ReconnectSessionLazy`, send real commands via `SendKeysAndEnter`, verify status via the status detection engine in `tmux.go`. Cover start, stop, restart-with-flags flows. |
| Conductor parent-child command delivery | Conductor sends commands to child sessions via `SendSessionMessageReliable`. This is the core orchestration primitive. Must verify: message arrives in child pane, child processes it, status transitions reach parent. | HIGH | Requires creating parent + child tmux sessions, linking via `ParentSessionID`, sending text, and verifying pane content. The `sendWithRetryTarget` mock tests exist but never test real tmux delivery. |
| Cross-session event notification tests | `TransitionNotifier` dispatches events when child sessions transition (running -> waiting). Must verify: event file written, event watcher detects it, parent receives notification message. | HIGH | The unit tests in `transition_notifier_test.go` mock storage. Integration tests need real SQLite storage, real event files, and real `StatusEventWatcher` (fsnotify-based). |
| Multi-tool session creation and detection | Agent-deck supports Claude Code, Gemini CLI, OpenCode, Codex, and shell. Each tool has different launch commands, sleep detection patterns, and session ID mechanisms. Integration tests must verify each tool's session creates correctly and status detection works. | HIGH | Existing e2e tests (`opencode_e2e_test.go`, `opencode_fullflow_test.go`) require the actual CLI tool installed. Integration tests should use mock tool commands (e.g., `echo`/`sleep`) with tool-specific pane patterns to test the detection engine without real tools. |
| Sleep/wait detection across tools | The busy-indicator and prompt-detection system in `tmux.go` (~2500 lines) is the most complex detection logic. Must verify: each tool's "running" patterns are detected as GREEN, each tool's "waiting" patterns are detected as YELLOW, transitions happen within expected timeframes. | HIGH | Use tmux sessions running `printf` commands that emit tool-specific busy indicators and prompts. Verify `GetStatus()` returns correct status. Cover Claude ("Thinking...", spinner), Gemini ("Generating..."), OpenCode (thinking indicator), Codex patterns. |
| Test profile isolation | Tests must use `AGENTDECK_PROFILE=_test` to prevent corrupting production data. This is non-negotiable given the 2025-12-11 incident (36 sessions overwritten). | LOW | Already implemented via TestMain pattern. Integration tests in new packages must include their own `TestMain` file. Template exists. |
| SQLite fixture management | Integration tests need pre-populated databases (sessions with specific tools, statuses, parent-child relationships, groups). Must create, seed, and tear down SQLite state cleanly. | MEDIUM | Extend existing `newTestStorage(t)` pattern from `storage_test.go`. Add a `testutil/fixtures.go` with helpers: `CreateTestInstance(t, tool, status)`, `CreateLinkedPair(t, parentTool, childTool)`, `SeedConductorMeta(t, name)`. |
| Timeout and polling assertions | Many integration tests involve waiting for async events (status transitions, event file writes, pane content changes). Need reliable polling helpers with configurable timeouts that fail with descriptive messages. | MEDIUM | Pattern exists in `event_watcher_test.go` (`select` with `time.After`). Generalize into `testutil/wait.go`: `WaitForCondition(t, timeout, interval, checkFn)`, `WaitForPaneContent(t, session, contains, timeout)`, `WaitForStatus(t, session, expectedStatus, timeout)`. |

### Differentiators (Competitive Advantage)

Features that make the test framework robust and catch real-world regressions that unit tests miss.

| Feature | Value Proposition | Complexity | Notes |
|---------|-------------------|------------|-------|
| Conductor heartbeat round-trip test | Verifies the full heartbeat loop: conductor session receives heartbeat message, processes it (simulated), responds with status. This is the production-critical path that breaks silently. | HIGH | Create conductor session with `meta.json` fixture, simulate heartbeat script sending to child sessions, verify response arrives back. Uses real tmux + real file system. |
| Concurrent session status polling stress test | Agent-deck polls status for N sessions every 2s tick. Integration test creates 10+ sessions and verifies that batch polling (`RefreshSessions`) correctly identifies status for all of them without race conditions. | MEDIUM | Creates multiple tmux sessions in different states (some running commands, some idle). Runs one poll cycle via the tmux cache. Asserts correct status for each. Exercises the "1 subprocess instead of N" optimization. |
| Fork flow with real tmux sessions | The fork operation creates a child session from a parent's Claude session ID. Current `fork_integration_test.go` only validates the command string. A real integration test would execute the fork command in tmux and verify both sessions exist. | HIGH | Requires real `tmux new-session`, then fork command execution. Cannot use real Claude (needs API key), but can verify tmux session creation, environment variable propagation (`AGENTDECK_INSTANCE_ID`, `CLAUDE_SESSION_ID`), and parent-child linkage in storage. |
| MCP attach/detach with scoped config | MCP attachment writes to `.mcp.json` (LOCAL) or `~/.claude-work/.claude.json` (GLOBAL). Integration test verifies: file written correctly, detach removes entry, re-attach after detach works. | MEDIUM | Use temp directories to avoid touching real config. Verify JSON structure matches what Claude Code expects. Tests `mcp_catalog.go` and `mcp_dialog.go` logic end-to-end. |
| Event watcher recovery after restart | The `StatusEventWatcher` uses fsnotify. If the watched directory is recreated (e.g., after a crash), the watcher should reconnect or fail gracefully. | LOW | Create watcher, delete events dir, recreate it, write event, verify delivery or clean error. |
| Storage watcher multi-instance sync | `StorageWatcher` detects external SQLite changes for multi-instance sync. Integration test runs two storage instances, writes from one, verifies the other detects the change within the `pollInterval`. | MEDIUM | Tests the `ignoreWindow` (3s) > `pollInterval` (2s) invariant documented in CLAUDE.md memory. Creates two `Storage` instances pointing at same `state.db`. |
| Skills attachment and triggering integration | Skills are loaded from the cache directory and attached to sessions. Integration test verifies: skill discovered from `skills/` directory, attached to session instance, triggering conditions evaluated correctly. | MEDIUM | Create temp skills directory structure matching Anthropic skill-creator format (`SKILL.md` + optional `scripts/`). Verify `SkillsCatalog` discovers them. Attach to test instance. Verify trigger evaluation. |
| Send-with-retry real tmux verification | `sendWithRetryTarget` has extensive mock tests. Integration test sends to a real tmux session running a script that delays Enter processing (simulating paste buffer behavior), verifies retry logic works. | MEDIUM | Create tmux session, send large text, verify it appears in pane capture. Exercises chunked sending (`SendKeysChunked`) and the paste-marker detection. |

### Anti-Features (Commonly Requested, Often Problematic)

Features that seem valuable but create more problems than they solve.

| Feature | Why Requested | Why Problematic | Alternative |
|---------|---------------|-----------------|-------------|
| Full Claude Code / Gemini CLI end-to-end tests | "We should test the actual tools" | Requires API keys in CI (security risk for public repo), costs money per test run, flaky due to model latency/availability, violates "no production side effects" constraint | Use tool-pattern simulation: tmux sessions running scripts that emit the exact terminal patterns each tool produces (busy indicators, prompts, spinners). Test the detection engine, not the AI tools. |
| TUI (Bubble Tea) integration tests | "Test the UI renders correctly" | Explicitly out of scope per PROJECT.md. Bubble Tea testing requires a separate approach (tea.Test or VHS). The `home.go` file is 8500 lines. Testing it end-to-end is a separate, massive effort. | Keep TUI testing to unit tests of individual components (already done: `components_test.go`, `hotkeys_test.go`, `newdialog_test.go`). Integration tests focus on the data/orchestration layer beneath the TUI. |
| Docker-based test isolation | "Run tests in Docker for reproducibility" | Agent-deck is fundamentally a tmux-based tool. Running tmux inside Docker is fragile (no PTY by default, socket issues). Testcontainers adds CGO dependency (modernc.org/sqlite avoids CGO intentionally). macOS is the primary dev platform. | Use profile isolation (`AGENTDECK_PROFILE=_test`), temp directories (`t.TempDir()`), and `t.Cleanup()`. These provide reliable isolation without Docker overhead. |
| Performance/load testing | "How many sessions can we handle?" | Out of scope per PROJECT.md. Performance testing requires fundamentally different infrastructure (benchmarks, profiling, sustained load). Conflating it with integration testing makes both worse. | Use `go test -bench` for hot-path benchmarks (status polling, SQLite reads). Keep separate from correctness-focused integration tests. |
| Parallel integration test execution | "Run all integration tests in parallel for speed" | tmux sessions share a global namespace. Parallel tests create race conditions on session names, port conflicts, and state file corruption. The `SessionPrefix + unique suffix` pattern helps but isn't bulletproof for parallel tmux operations. | Run integration tests sequentially (`go test -count=1 -p 1`). Use `testing.Short()` to skip slow tests in rapid feedback loops. Each test cleans up its own sessions via `t.Cleanup()`. |
| CI/CD pipeline integration | "Tests should run in GitHub Actions" | Out of scope per PROJECT.md. tmux requires a running server, which needs special CI configuration (start tmux server in background, handle PTY allocation). This is a separate effort. | Design tests with `skipIfNoTmuxServer(t)` so they skip gracefully in CI. Run full suite locally before push. Add CI later as a separate milestone. |
| Mocking tmux entirely | "Abstract tmux away for unit-testable orchestration" | The entire value of integration tests is verifying real tmux behavior (send-keys timing, pane capture accuracy, busy-indicator detection). Mocking tmux defeats the purpose. | Use mocks for unit tests (already done: `mockStatusChecker`, `mockSendRetryTarget`). Integration tests use real tmux. The two layers complement each other. |

## Feature Dependencies

```
[tmux session test fixtures]
    |
    +--requires--> [Test profile isolation]
    |
    +--enables--> [Session lifecycle integration tests]
    |                 |
    |                 +--enables--> [Sleep/wait detection across tools]
    |                 |
    |                 +--enables--> [Multi-tool session creation and detection]
    |
    +--enables--> [Conductor parent-child command delivery]
    |                 |
    |                 +--requires--> [SQLite fixture management]
    |                 |
    |                 +--enables--> [Conductor heartbeat round-trip test]
    |
    +--enables--> [Cross-session event notification tests]
    |                 |
    |                 +--requires--> [Timeout and polling assertions]
    |                 |
    |                 +--requires--> [SQLite fixture management]
    |
    +--enables--> [Fork flow with real tmux sessions]
    |
    +--enables--> [Send-with-retry real tmux verification]

[SQLite fixture management]
    |
    +--requires--> [Test profile isolation]
    |
    +--enables--> [Storage watcher multi-instance sync]

[Timeout and polling assertions]
    |
    +--enables--> [Concurrent session status polling stress test]
    |
    +--enables--> [Event watcher recovery after restart]

[Skills attachment and triggering integration]
    +--requires--> [SQLite fixture management]
```

### Dependency Notes

- **tmux session test fixtures require Test profile isolation:** Every tmux test helper must enforce `AGENTDECK_PROFILE=_test` via TestMain. Without this, test sessions could corrupt production state.
- **Conductor tests require SQLite fixtures:** Conductor parent-child relationships are stored in SQLite. Tests must create pre-linked session pairs with correct `ParentSessionID` fields.
- **Cross-session events require Timeout/polling helpers:** Event watcher tests are inherently async (fsnotify-based). Without reliable polling/timeout helpers, tests become flaky.
- **Sleep detection requires Session lifecycle:** You cannot test sleep detection without first being able to create and manage tmux sessions. The detection engine (`GetStatus`) operates on real pane content.
- **All features require tmux session fixtures:** This is the foundation. Build it first, then everything else layers on top.

## MVP Definition

### Launch With (Phase 1: Foundation)

Minimum viable test framework that enables all subsequent test development.

- [ ] **tmux session test fixture helpers** (`testutil/tmux.go`) -- Foundation for every integration test. Provides `CreateTestSession(t, name)`, `KillTestSession(t, name)`, session name generation. Cross-package reusable.
- [ ] **Timeout and polling assertion helpers** (`testutil/wait.go`) -- Eliminates flaky async tests. Provides `WaitForCondition`, `WaitForPaneContent`, `WaitForStatus`.
- [ ] **SQLite fixture helpers** (`testutil/fixtures.go`) -- Seed test databases with sessions, groups, parent-child links. Extends existing `newTestStorage` pattern.
- [ ] **TestMain template for new packages** -- Standardized `TestMain` with profile isolation and session cleanup. Copy from existing pattern.
- [ ] **Session lifecycle integration tests** -- End-to-end: create tmux session, run command, verify status transitions via real detection engine.

### Add After Foundation (Phase 2: Orchestration)

Tests for the conductor and cross-session systems, built on the Phase 1 foundation.

- [ ] **Conductor parent-child command delivery** -- Trigger: foundation helpers working, session lifecycle tests passing.
- [ ] **Cross-session event notification tests** -- Trigger: SQLite fixtures and polling helpers proven reliable.
- [ ] **Sleep/wait detection across tools** -- Trigger: session lifecycle tests demonstrate status detection works with real tmux.
- [ ] **Multi-tool session creation and detection** -- Trigger: sleep detection tests establish the pattern for simulating tool output.

### Add After Orchestration (Phase 3: Edge Cases)

Differentiator tests that catch production regressions.

- [ ] **Conductor heartbeat round-trip test** -- Trigger: conductor parent-child tests passing.
- [ ] **Fork flow with real tmux sessions** -- Trigger: session lifecycle and SQLite fixture helpers working.
- [ ] **Send-with-retry real tmux verification** -- Trigger: session lifecycle tests with real tmux proven stable.
- [ ] **Concurrent session status polling stress test** -- Trigger: multiple sessions can be created and polled reliably.
- [ ] **Storage watcher multi-instance sync** -- Trigger: SQLite fixtures working.
- [ ] **Skills attachment and triggering integration** -- Trigger: SQLite fixtures working.
- [ ] **MCP attach/detach with scoped config** -- Trigger: basic session lifecycle working.
- [ ] **Event watcher recovery after restart** -- Trigger: cross-session event tests passing.

## Feature Prioritization Matrix

| Feature | User Value | Implementation Cost | Priority |
|---------|------------|---------------------|----------|
| tmux session test fixtures | HIGH | LOW | P1 |
| Timeout and polling assertions | HIGH | LOW | P1 |
| SQLite fixture management | HIGH | LOW | P1 |
| TestMain template | HIGH | LOW | P1 |
| Session lifecycle integration tests | HIGH | MEDIUM | P1 |
| Conductor parent-child command delivery | HIGH | HIGH | P1 |
| Cross-session event notification tests | HIGH | MEDIUM | P1 |
| Sleep/wait detection across tools | HIGH | HIGH | P2 |
| Multi-tool session creation and detection | MEDIUM | HIGH | P2 |
| Conductor heartbeat round-trip test | MEDIUM | HIGH | P2 |
| Fork flow with real tmux sessions | MEDIUM | MEDIUM | P2 |
| Send-with-retry real tmux verification | MEDIUM | MEDIUM | P2 |
| Concurrent status polling stress test | MEDIUM | MEDIUM | P3 |
| Storage watcher multi-instance sync | LOW | MEDIUM | P3 |
| Skills attachment and triggering integration | LOW | MEDIUM | P3 |
| MCP attach/detach scoped config | LOW | MEDIUM | P3 |
| Event watcher recovery after restart | LOW | LOW | P3 |

**Priority key:**
- P1: Must have for launch. These tests verify the core product promises.
- P2: Should have. These catch real production regressions.
- P3: Nice to have. Valuable but can wait.

## Existing Infrastructure Analysis

The codebase already has significant test infrastructure that the integration framework should build on, not replace.

| Existing Asset | Location | Reuse Strategy |
|---------------|----------|----------------|
| `skipIfNoTmuxServer(t)` | `session/testmain_test.go`, `tmux/testmain_test.go` | Extract to shared `testutil/tmux.go` |
| `createTestSession(t, suffix)` | `tmux/controlpipe_test.go` | Basis for shared fixture helper |
| `newTestStorage(t)` | `session/storage_test.go` | Basis for fixture helper |
| `mockStatusChecker` | `cmd/agent-deck/session_send_test.go` | Keep for unit tests, complement with real tmux tests |
| `StatusEventWatcher` tests | `session/event_watcher_test.go` | Pattern for async event testing |
| `TestIntegration_*` notification tests | `session/notifications_integration_test.go` | Good model for multi-phase integration tests |
| `TestForkFlow_Integration` | `session/fork_integration_test.go` | Extend with real tmux execution |
| TestMain pattern | 5 packages | Template for new test packages |
| `UnsetGitRepoEnv` | `testutil/gitenv.go` | Already cross-package; add tmux helpers to same package |

## Sources

- Codebase analysis: `internal/session/`, `internal/tmux/`, `cmd/agent-deck/` test files (HIGH confidence, primary source)
- [DoltHub: Debugging Multiple Go Processes](https://www.dolthub.com/blog/2023-05-25-debugging-multiple-golang-processes/) -- Multi-process Go integration test patterns
- [David MacIver: Using tmux to Test Console Applications](https://www.drmaciver.com/2015/05/using-tmux-to-test-your-console-applications/) -- tmux as test harness pattern (capture-pane, send-keys for assertions)
- [tmux-plugins/tmux-test](https://github.com/tmux-plugins/tmux-test) -- Isolated tmux testing framework (Vagrant-based, inspiration for isolation patterns)
- [Go testing.T cleanup](https://ieftimov.com/posts/testing-in-go-clean-tests-using-t-cleanup/) -- `t.Cleanup()` patterns for resource management
- [Testcontainers for Go](https://golang.testcontainers.org/) -- Lifecycle management patterns (TestMain for shared resources, garbage collection). Not used directly, but cleanup patterns apply.
- [Go Test Parallelism](https://threedots.tech/post/go-test-parallelism/) -- Why sequential execution is safer for resource-dependent tests

---
*Feature research for: Integration testing framework for agent-deck v1.1*
*Researched: 2026-03-06*
