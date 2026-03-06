# Project Research Summary

**Project:** Agent Deck: Integration Testing Framework (v1.1)
**Domain:** Go integration testing for tmux-based multi-process orchestration
**Researched:** 2026-03-06
**Confidence:** HIGH

## Executive Summary

Agent-deck is a terminal session manager for AI coding agents, built with Go + Bubble Tea TUI + tmux. The v1.1 milestone needs an integration testing framework that validates conductor orchestration, cross-session events, multi-tool session behavior, and sleep/wait detection using real tmux sessions. The research unanimously finds that no new dependencies are needed. The existing Go testing stack (stdlib `testing`, testify, errgroup, modernc.org/sqlite, fsnotify) already provides everything required. The work is about building test infrastructure patterns, not importing libraries.

The recommended approach is to create a dedicated `internal/integration/` package for cross-cutting tests, extend `internal/testutil/` with shared helpers (TmuxHarness, TestStorage, fixture builders, polling utilities), and build tests in a strict dependency order: shared utilities first, then session lifecycle, then status detection and events, and finally conductor orchestration. This layered approach matters because each tier depends on the reliability of the tier below it. All four research files agree on this sequencing.

The primary risks are test profile isolation failure (which already caused a production incident in 2025-12-11, destroying 36 sessions), orphaned tmux session leaks (3GB RAM waste in 2026-01-20), and timing-dependent test flakiness (86 instances of `time.Sleep`/`time.After` across 20 test files). All three must be addressed in the framework layer before writing any tests. Prevention is straightforward: enforce `AGENTDECK_PROFILE=_test` in every TestMain, use `t.Cleanup()` with a session registry for deterministic teardown, and replace fixed sleeps with polling helpers that have configurable timeouts.

## Key Findings

### Recommended Stack

No new dependencies. The codebase already contains everything needed. See [STACK.md](./STACK.md) for full details.

**Core technologies:**
- `testing` (Go stdlib): Test execution, subtests, `t.Cleanup`, `testing.Short()` for skipping slow tests
- `testify` v1.11.1 (already in go.mod): `require` for preconditions, `assert` for verification, `suite` for complex lifecycle (conductor tests)
- `errgroup` v0.19.0 (already in go.mod): Concurrent multi-session test orchestration with error propagation
- `modernc.org/sqlite` v1.44.3 (already in go.mod): In-memory test databases via `t.TempDir()`
- `fsnotify` v1.9.0 (already in go.mod): Event watcher tests for cross-session notification verification

**Critical version note:** Use `go test -timeout 120s` for conductor orchestration tests (default 30s is too short). Use `go test -count=1` to disable caching for tmux-dependent tests.

### Expected Features

See [FEATURES.md](./FEATURES.md) for the full feature landscape and dependency graph.

**Must have (table stakes):**
- tmux session test fixture lifecycle (create, cleanup, naming convention)
- Session lifecycle integration tests (start, stop, fork, restart with real tmux)
- Conductor parent-child command delivery tests
- Cross-session event notification tests (StatusEventWatcher + fsnotify)
- Multi-tool session creation and detection (Claude, Gemini, OpenCode, Codex patterns)
- Sleep/wait detection accuracy tests across all tools
- Test profile isolation enforcement (`AGENTDECK_PROFILE=_test`)
- SQLite fixture management (seed databases with instances, groups, conductor links)
- Timeout and polling assertion helpers (eliminate flaky sleeps)

**Should have (differentiators):**
- Conductor heartbeat round-trip test
- Concurrent session status polling stress test (10+ sessions)
- Fork flow with real tmux sessions (verify env var propagation)
- Send-with-retry real tmux verification (chunked sending, paste-marker detection)
- Storage watcher multi-instance sync test

**Defer (v2+):**
- TUI (Bubble Tea) integration tests (explicitly out of scope)
- Docker-based test isolation (tmux in Docker is fragile)
- CI/CD pipeline integration (out of scope per PROJECT.md)
- Performance/load testing (separate concern)
- Full end-to-end tests with real AI tools (requires API keys, non-portable)

### Architecture Approach

The framework centers on a new `internal/integration/` package for cross-cutting tests and an extended `internal/testutil/` package for shared helpers. See [ARCHITECTURE.md](./ARCHITECTURE.md) for full component layout and build order.

**Major components:**
1. `internal/testutil/tmux.go` (NEW): `TmuxHarness` struct managing session creation, cleanup, pane content polling. Foundation for every integration test.
2. `internal/testutil/storage.go` (NEW): `NewTestStorage()` extracted from existing pattern in `session/storage_test.go`. Isolated SQLite per test via `t.TempDir()`.
3. `internal/testutil/fixtures.go` (NEW): `ConductorFixture()`, `InstanceBuilder` with fluent API. Reduces boilerplate for conductor and multi-session test setup.
4. `internal/integration/` (NEW): Dedicated package for cross-package integration tests. Imports from `session`, `tmux`, `statedb` freely without circular dependency risk.
5. `internal/integration/testmain_test.go` (NEW): TestMain with profile isolation, git env cleanup, and post-suite orphan session cleanup.

**Key architectural decision:** Integration tests use real tmux sessions but simple commands (`echo`, `sleep`, `cat`) instead of real AI tools. This tests the plumbing without requiring API keys or incurring costs.

### Critical Pitfalls

See [PITFALLS.md](./PITFALLS.md) for all 7 pitfalls with recovery strategies.

1. **Test profile isolation failure** -- Enforce `AGENTDECK_PROFILE=_test` in every TestMain. Add a runtime guard in framework init that refuses to proceed if profile is not `_test`. Two production incidents prove this is non-negotiable.
2. **Orphaned tmux session leaks** -- Use `t.Cleanup()` (not `defer`) for all session teardown. Track sessions in a registry within `TmuxHarness`. Have TestMain cleanup kill any `agentdeck_inttest-*` sessions that survived.
3. **Timing-dependent test flakiness** -- Replace all `time.Sleep()` assertions with polling helpers (`WaitForCondition`, `WaitForPaneContent`, `WaitForStatus`). Poll every 50-100ms with 5-10s timeouts.
4. **Package-level global state interference** -- Do NOT use `t.Parallel()` for integration tests with real tmux. Reset global caches (`sessionCacheData`, `PipeManager`) at test boundaries. Run integration tests sequentially.
5. **Over-coupled tests** -- Use fixture builders and test-only setup methods. Drive state through public API where possible. Centralize internal state access in helper methods.

## Implications for Roadmap

Based on the combined research, the framework should be built in 3 phases following the dependency graph from FEATURES.md and the build order from ARCHITECTURE.md.

### Phase 1: Test Framework Foundation

**Rationale:** Everything depends on reliable test infrastructure. STACK.md confirms no new deps needed. ARCHITECTURE.md defines the exact build order. PITFALLS.md identifies 5 critical pitfalls that must be prevented at this layer.
**Delivers:** Shared test utilities, integration package scaffold, and session lifecycle tests that prove the foundation works.
**Addresses:** tmux session test fixtures, timeout/polling helpers, SQLite fixtures, TestMain template, session lifecycle integration tests.
**Avoids:** Profile isolation failure (Pitfall 1), orphaned tmux sessions (Pitfall 2), timing flakiness (Pitfall 3), global state interference (Pitfall 4), multi-resource cleanup leaks (Pitfall 7).

Suggested breakdown:
- `internal/testutil/tmux.go` with `TmuxHarness` and `SkipIfNoTmuxServer`
- `internal/testutil/storage.go` with `NewTestStorage`
- `internal/testutil/fixtures.go` with `ConductorFixture` and `InstanceBuilder`
- `internal/testutil/wait.go` with `WaitForCondition`, `WaitForPaneContent`, `WaitForStatus`
- `internal/integration/testmain_test.go` with profile isolation
- `internal/integration/lifecycle_test.go` with 5 core tests (start, stop, restart, fork, start-with-message)

### Phase 2: Status Detection and Event Tests

**Rationale:** Status detection and events are prerequisites for conductor tests (Phase 3). The sleep/wait detection engine is the most complex subsystem (~2500 lines in `tmux.go`). Cross-session events are the communication backbone for conductor orchestration. Both must be proven reliable before testing the full pipeline.
**Delivers:** Verified status detection accuracy, proven event notification cycle, multi-tool detection coverage.
**Addresses:** Sleep/wait detection across tools, cross-session event notification, multi-tool session creation, conductor parent-child command delivery (basic).
**Avoids:** Over-coupled tests (Pitfall 6) by using fixture builders. Missing edge cases (PITFALLS.md checklist) by covering ANSI normalization, tool-switch mid-session, and startup window detection.

Suggested breakdown:
- `integration/sleep_detection_test.go`: Prompt detection with real tmux pane content, status transitions (starting -> running -> waiting)
- `integration/events_test.go`: Event write/watch cycle, cross-session filtering, stale event cleanup
- `integration/multitool_test.go`: Tool-specific command construction, detection patterns for Claude/Gemini/OpenCode/Codex
- `integration/conductor_test.go` (basic): Setup, meta.json creation, session lifecycle

### Phase 3: Conductor Orchestration and Edge Cases

**Rationale:** This phase exercises the full orchestration pipeline, which requires all lower layers to be stable. Includes differentiator tests that catch production-grade regressions.
**Delivers:** Full conductor round-trip testing, send/output pipeline verification, fork flow with env var propagation, concurrent polling stress test.
**Addresses:** Conductor heartbeat round-trip, fork flow with real tmux, send-with-retry verification, concurrent status polling, storage watcher sync, skills attachment, MCP scoped config.
**Avoids:** CI test blindness (Pitfall 5) by ensuring Tier 1 tests never skip. Missing edge cases for conductor failure scenarios (child crash mid-task, notification target gone).

Suggested breakdown:
- `integration/conductor_test.go` (advanced): Full pipeline (parent -> child message -> response), heartbeat round-trip, clear-on-compact behavior
- `integration/send_test.go`: Real tmux send/output, chunked sending, paste-marker detection
- `integration/skills_test.go`: Skills discovery, attachment, trigger evaluation
- Concurrent polling stress test (10+ sessions)
- Storage watcher multi-instance sync test

### Phase Ordering Rationale

- Phase 1 must come first because the TmuxHarness, polling helpers, and fixture builders are prerequisites for every subsequent test. ARCHITECTURE.md Layer 1-3 maps directly to this phase.
- Phase 2 before Phase 3 because conductor orchestration depends on reliable status detection and event delivery. Testing the pipeline without proving the components is premature.
- The dependency graph in FEATURES.md confirms: `tmux fixtures -> session lifecycle -> sleep detection -> conductor tests`. This is a strict dependency chain, not a preference.
- PITFALLS.md maps 5 of 7 pitfalls to Phase 1, confirming that the framework layer must be rock-solid before writing feature tests.

### Research Flags

Phases likely needing deeper research during planning:
- **Phase 2 (sleep detection):** The detection engine is ~2500 lines with tool-specific patterns. Phase planning should map exactly which patterns each test covers and which edge cases from the PITFALLS.md "looks done but isn't" checklist to target.
- **Phase 3 (conductor orchestration):** The conductor pipeline involves real tmux message delivery, heartbeat scripts, and child session management. The exact test setup for simulating heartbeat behavior without real AI tools needs design work.

Phases with standard patterns (skip research-phase):
- **Phase 1 (framework foundation):** All patterns are well-documented. Go stdlib testing, testify, t.Cleanup, t.TempDir are standard. The existing codebase has templates for every helper needed. STACK.md provides concrete code examples.

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | HIGH | Zero new dependencies. All libraries already in go.mod with established usage patterns across 80+ test files. No version decisions to make. |
| Features | HIGH | Feature landscape derived directly from codebase analysis (existing tests, conductor logic, multi-tool support). Dependency graph verified against import structure. |
| Architecture | HIGH | Architecture follows existing codebase patterns. New package structure (`internal/integration/`, extended `internal/testutil/`) avoids circular imports. Build order verified against Go package dependencies. |
| Pitfalls | HIGH | Two pitfalls validated by documented production incidents (2025-12-11 data corruption, 2026-01-20 RAM leak). Remaining pitfalls confirmed by codebase analysis (86 sleep occurrences, global state in tmux package). |

**Overall confidence:** HIGH

### Gaps to Address

- **Public API surface for test construction:** `newTestStorage()` in `session/storage_test.go` is unexported. The integration package needs either a public `NewStorageForTest()` or must construct storage directly via `statedb.Open()` + `Migrate()`. Decision needed during Phase 1 implementation.
- **Conductor base directory override:** Currently hardcoded to `~/.agent-deck/conductor`. Tests can use `t.Setenv("HOME", tmpDir)` but this affects all home-relative paths. A more surgical override (env var or parameter) may be cleaner. Evaluate during Phase 1.
- **Event directory override:** Same pattern as conductor. `GetEventsDir()` returns a fixed path. Override via HOME or add testability parameter.
- **tmux socket isolation:** PITFALLS.md mentions using separate tmux sockets via `TMUX_TMPDIR` for full isolation. This would prevent any possible interference with user sessions but adds complexity. Evaluate whether the naming convention approach (`agentdeck_inttest-*`) is sufficient.

## Sources

### Primary (HIGH confidence)
- Agent-deck codebase analysis: `internal/session/`, `internal/tmux/`, `cmd/agent-deck/`, `internal/statedb/` (direct code reading)
- Existing test infrastructure: 5 TestMain files, `fork_integration_test.go`, `notifications_integration_test.go`, `opencode_e2e_test.go`
- CLAUDE.md documented incidents: 2025-12-11 production data corruption, 2026-01-20 orphaned tmux session RAM leak
- [Go stdlib os/exec test patterns](https://go.dev/src/os/exec/exec_test.go): Re-exec subprocess pattern
- [testify/suite docs](https://pkg.go.dev/github.com/stretchr/testify/suite): Suite lifecycle methods
- [errgroup docs](https://pkg.go.dev/golang.org/x/sync/errgroup): Concurrent test orchestration

### Secondary (MEDIUM confidence)
- [Testing Time and Asynchronicities in Go (official Go blog)](https://go.dev/blog/testing-time): Timing-dependent test patterns
- [Go Race Detector (official Go blog)](https://go.dev/blog/race-detector): Race detection for global state issues
- [t.Cleanup vs defer in parallel tests](https://brandur.org/fragments/go-prefer-t-cleanup-with-parallel-subtests): Cleanup pattern recommendations
- [DoltHub: Debugging Multiple Go Processes](https://www.dolthub.com/blog/2023-05-25-debugging-multiple-golang-processes/): Multi-process test patterns
- [David MacIver: Using tmux to Test Console Applications](https://www.drmaciver.com/2015/05/using-tmux-to-test-your-console-applications/): tmux as test infrastructure

### Tertiary (LOW confidence)
- [tmux-plugins/tmux-test](https://github.com/tmux-plugins/tmux-test): Vagrant-based tmux testing (inspiration only, not directly applicable)
- [Mattermost: Cutting Test Runtime by 60%](https://mattermost.com/blog/cutting-test-runtime-by-60-with-selective-parallelism-in-go/): Selective parallelism (agent-deck should avoid parallel integration tests per PITFALLS.md)

---
*Research completed: 2026-03-06*
*Ready for roadmap: yes*
