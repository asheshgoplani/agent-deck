# Roadmap: Agent Deck

## Milestones

- ~~**v1.0 Skills Reorganization & Stabilization**~~ -- Phases 1-3 (shipped 2026-03-06)
- ~~**v1.1 Integration Testing**~~ -- Phases 4-6 (shipped 2026-03-07)
- ~~**v1.2 Conductor Reliability & Learnings Cleanup**~~ -- Phases 7-10 (shipped 2026-03-07)
- **v1.3 Session Reliability & Resume** -- Phases 11-16 (active, rescoped 2026-03-12)

## Phases

**Phase Numbering:**
- Integer phases (1, 2, 3): Planned milestone work
- Decimal phases (2.1, 2.2): Urgent insertions (marked with INSERTED)

Decimal phases appear between their surrounding integers in numeric order.

<details>
<summary>v1.0 Skills Reorganization & Stabilization (Phases 1-3) -- SHIPPED 2026-03-06</summary>

- [x] **Phase 1: Skills Reorganization** (2/2 plans) -- completed 2026-03-06
- [x] **Phase 2: Testing & Bug Fixes** (3/3 plans) -- completed 2026-03-06
- [x] **Phase 3: Stabilization & Release Readiness** (2/2 plans) -- completed 2026-03-06

</details>

<details>
<summary>v1.1 Integration Testing (Phases 4-6) -- SHIPPED 2026-03-07</summary>

- [x] **Phase 4: Framework Foundation** (2/2 plans) -- completed 2026-03-07
- [x] **Phase 5: Status Detection & Events** (2/2 plans) -- completed 2026-03-07
- [x] **Phase 6: Conductor Pipeline & Edge Cases** (2/2 plans) -- completed 2026-03-07

</details>

<details>
<summary>v1.2 Conductor Reliability & Learnings Cleanup (Phases 7-10) -- SHIPPED 2026-03-07</summary>

- [x] **Phase 7: Send Reliability** (2/2 plans) -- completed 2026-03-07
- [x] **Phase 8: Heartbeat & CLI Fixes** (2/2 plans) -- completed 2026-03-07
- [x] **Phase 9: Process Stability** (2/2 plans) -- completed 2026-03-07
- [x] **Phase 10: Learnings Promotion** (2/2 plans) -- completed 2026-03-06

</details>

**v1.3 Session Reliability & Resume (Phases 11-16) — rescoped 2026-03-12**

Original scope had Phases 11-15. Rescoped after #320 and #318 closed, and new critical issues (#324, #322) emerged. Added #266 (unblocked by #320), #255, #225, #216 from backlog. Added Phase 16 for comprehensive testing.

- [ ] **Phase 11: MCP Proxy Reliability** - Fix request ID collisions in shared MCP socket proxy (#324)
- [x] **Phase 12: Session List & Resume UX** - Stopped sessions visible with distinct styling; resume deduplication (#307, #224) (completed 2026-03-13)
- [ ] **Phase 13: Auto-Start & Platform** - WSL/Linux TTY fix for non-interactive auto-start (#311)
- [x] **Phase 14: Detection & Sandbox** - Docker tmux set-environment fix (#266); OpenCode waiting status (#255) (completed 2026-03-13)
- [ ] **Phase 15: Mouse, Theme & Polish** - Mouse scroll, light theme fix, docs, heartbeat cleanup, worktree reuse (#262, #254, #322, #228, #225, #216)
- [ ] **Phase 16: Comprehensive Testing** - Integration tests for all v1.3 fixes, regression suite

## Phase Details

### Phase 11: MCP Proxy Reliability
**Goal:** Multiple sessions sharing an MCP socket proxy can issue concurrent tool calls without request ID collisions or response cross-talk
**Depends on:** Nothing (first phase of rescoped v1.3; critical production issue)
**Requirements:** MCP-01, MCP-02, MCP-03
**Issues:** #324
**Success Criteria** (what must be TRUE):
  1. Two sessions sharing a proxy instance can issue concurrent tool calls and each receives only its own responses
  2. Request IDs are unique per proxy instance (not per session), eliminating collisions
  3. Response routing uses session-scoped ID mapping so a response never reaches the wrong caller
  4. An integration test under the race detector verifies concurrent tool calls through a shared proxy
**Plans:** 1 plan
Plans:
- [ ] 11-01-PLAN.md — Atomic ID rewriting in SocketProxy with concurrent integration tests

### Phase 12: Session List & Resume UX
**Goal:** Users can see, identify, and resume stopped sessions directly from the main TUI without creating duplicate records
**Depends on:** Phase 11
**Requirements:** VIS-01, VIS-02, VIS-03, DEDUP-01, DEDUP-02, DEDUP-03
**Issues:** #307, #224
**Success Criteria** (what must be TRUE):
  1. A stopped session appears in the main TUI session list with distinct styling from error sessions
  2. The preview pane for a stopped session shows user-intentional stop context with a resume affordance; error sessions show crash context with different guidance
  3. The conductor session picker excludes stopped sessions (correct filtering preserved)
  4. Resuming a stopped session reuses the existing record (one entry, not two)
  5. UpdateClaudeSessionsWithDedup runs immediately in memory at the resume call site
  6. A concurrent-write integration test covering two Storage instances against the same SQLite file passes green
**Plans:** 2/2 plans complete
Plans:
- [ ] 12-01-PLAN.md — Preview pane stopped vs error differentiation (VIS-01, VIS-02, VIS-03)
- [ ] 12-02-PLAN.md — In-memory dedup at resume site and concurrent storage test (DEDUP-01, DEDUP-02, DEDUP-03)

### Phase 13: Auto-Start & Platform
**Goal:** Users on WSL/Linux can run agent-deck session start from non-interactive contexts and tool processes receive a working PTY
**Depends on:** Phase 12
**Requirements:** PLAT-01, PLAT-02
**Issues:** #311
**Success Criteria** (what must be TRUE):
  1. Running agent-deck session start from a non-interactive shell on WSL/Linux starts the session without tool processes rejecting input due to a missing PTY
  2. After auto-starting and stopping a session on WSL/Linux, resuming it attaches to the correct tool conversation (identified by the tool conversation ID, not the agent-deck internal UUID)
**Plans:** 2 plans
Plans:
- [ ] 13-01-PLAN.md — Pane-ready detection + Go-side UUID generation replacing shell uuidgen (PLAT-01)
- [ ] 13-02-PLAN.md — SyncSessionIDsFromTmux before Kill in stop path (PLAT-02)

### Phase 14: Detection & Sandbox
**Goal:** Docker sandbox tmux environment propagation works correctly; OpenCode waiting status is detected
**Depends on:** Phase 11 (sandbox persistence fix from #320 is prerequisite, already shipped)
**Requirements:** DET-01, DET-02
**Issues:** #266, #255
**Success Criteria** (what must be TRUE):
  1. tmux set-environment inside a Docker sandbox session sets environment variables that are visible to spawned processes
  2. OpenCode's question tool prompt triggers the "waiting" status detection, transitioning the session from "running" to "waiting"
**Plans:** 2/2 plans complete
Plans:
- [ ] 14-01-PLAN.md — Remove tmux set-environment from command strings, use host-side SetEnvironment (DET-01)
- [ ] 14-02-PLAN.md — OpenCode question tool prompt detection with false-positive prevention (DET-02)

### Phase 15: Mouse, Theme & Polish
**Goal:** Mouse scroll works everywhere, light theme renders correctly, heartbeat is consolidated, worktree reuse works, docs updated
**Depends on:** Phase 11 (independent of Phases 12-14; can be parallelized)
**Requirements:** UX-01, UX-02, UX-03, UX-04, UX-05
**Issues:** #262, #254, #322, #228, #225, #216
**Success Criteria** (what must be TRUE):
  1. Mouse wheel scroll works in session list, settings panel, global search results, and dialogs
  2. Light theme renders correctly in Codex preview and live session views with no dark background bleed-through
  3. The README sandbox section documents auto_cleanup with clear explanation
  4. Redundant heartbeat mechanisms are consolidated into a single mechanism
  5. When a git worktree already exists for a target branch, agent-deck detects and reuses it instead of creating a new one
**Plans:** 2/3 plans executed
Plans:
- [ ] 15-00-PLAN.md — Wave 0 test stubs for mouse scroll, light theme, worktree reuse (UX-01, UX-02, UX-05)
- [ ] 15-01-PLAN.md — Mouse wheel scroll support and light theme preview fix (UX-01, UX-02)
- [ ] 15-02-PLAN.md — auto_cleanup docs, heartbeat consolidation, worktree reuse (UX-03, UX-04, UX-05)

### Phase 16: Comprehensive Testing
**Goal:** Agent-deck is perfectly tested: every fix has a regression test, performance is validated, resource leaks are impossible, and all tool types are covered
**Depends on:** Phases 11-15 (all implementation phases complete)
**Requirements:** TEST-01, TEST-02, TEST-03, TEST-04, TEST-05, TEST-06, TEST-07, TEST-08, TEST-09, TEST-10
**Success Criteria** (what must be TRUE):
  1. Every fix in Phases 11-15 has at least one integration test asserting the correct behavior
  2. MCP proxy concurrent session test passes under `go test -race`
  3. Session lifecycle test covers the full stopped -> resumed -> running -> error transition chain
  4. Light theme rendering test validates no hardcoded dark-only color values leak into preview or session views
  5. Memory leak detection: extended sessions (10+ min) show RSS stays below 2x initial after forced GC
  6. CPU profiling: idle polling with 10 sessions uses <5% CPU; hot paths in active sessions are documented
  7. Resource cleanup: after session stop/delete, zero orphaned tmux sessions, zero leaked goroutines, zero stale file handles
  8. Regression framework: every bug fix in Phases 11-15 has a dedicated test that first reproduces the original failure condition, then asserts the fix
  9. Cross-tool matrix: full test suite passes for Claude, Codex, Gemini, and OpenCode session types covering status detection, send, and lifecycle
  10. Concurrent stress test: 20+ simultaneous sessions with randomized operations (start, stop, send, fork) pass under `go test -race` with no deadlocks or data corruption
  11. All tests pass with `make test` and `make ci`
**Plans:** TBD

## Progress

| Phase | Milestone | Plans Complete | Status | Completed |
|-------|-----------|----------------|--------|-----------|
| 1. Skills Reorganization | v1.0 | 2/2 | Complete | 2026-03-06 |
| 2. Testing & Bug Fixes | v1.0 | 3/3 | Complete | 2026-03-06 |
| 3. Stabilization & Release Readiness | v1.0 | 2/2 | Complete | 2026-03-06 |
| 4. Framework Foundation | v1.1 | 2/2 | Complete | 2026-03-07 |
| 5. Status Detection & Events | v1.1 | 2/2 | Complete | 2026-03-07 |
| 6. Conductor Pipeline & Edge Cases | v1.1 | 2/2 | Complete | 2026-03-07 |
| 7. Send Reliability | v1.2 | 2/2 | Complete | 2026-03-07 |
| 8. Heartbeat & CLI Fixes | v1.2 | 2/2 | Complete | 2026-03-07 |
| 9. Process Stability | v1.2 | 2/2 | Complete | 2026-03-07 |
| 10. Learnings Promotion | v1.2 | 2/2 | Complete | 2026-03-06 |
| 11. MCP Proxy Reliability | v1.3 | 0/1 | Not started | - |
| 12. Session List & Resume UX | v1.3 | 2/2 | Complete | 2026-03-13 |
| 13. Auto-Start & Platform | v1.3 | 0/2 | Not started | - |
| 14. Detection & Sandbox | 2/2 | Complete   | 2026-03-13 | - |
| 15. Mouse, Theme & Polish | 2/3 | In Progress|  | - |
| 16. Comprehensive Testing | v1.3 | 0/TBD | Not started | - |
