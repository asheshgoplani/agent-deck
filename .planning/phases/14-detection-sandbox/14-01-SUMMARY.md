---
phase: 14-detection-sandbox
plan: 01
subsystem: session
tags: [sandbox, docker, tmux, environment, tdd]

# Dependency graph
requires: []
provides:
  - Host-side SetEnvironment for all tool session IDs (Claude, Gemini, OpenCode, Codex)
  - Go-side UUID pre-generation for new Claude sessions (no shell uuidgen dependency)
  - Zero tmux set-environment calls in any command builder shell string
---

## Self-Check: PASSED

## What was built
Removed all embedded `tmux set-environment` calls from command builder shell strings across every tool path (Claude, Gemini, OpenCode, Codex, generic, fork, restart). Environment propagation now uses host-side Go `SetEnvironment()` calls after tmux session start, fixing Docker sandbox containers that cannot reach the host tmux socket (#266).

## Key changes
1. **Command builders cleaned**: All 15 call sites in `instance.go` no longer emit `tmux set-environment` in shell strings
2. **Host-side SetEnvironment**: Added explicit `SetEnvironment` calls in `Start()`, `StartWithMessage()`, and `Restart()` for CLAUDE_SESSION_ID, GEMINI_SESSION_ID, and GEMINI_YOLO_MODE
3. **SyncSessionIDsToTmux in Restart**: Ensures all known session IDs are propagated after restart
4. **Go-side UUID generation**: New `generateUUID()` function replaces shell `$(uuidgen | tr ...)` for Claude session IDs, avoiding Docker sandbox failures where uuidgen is unavailable
5. **Fork commands updated**: Both Claude and OpenCode fork paths use pre-generated UUIDs

## key-files
### created
- `internal/session/sandbox_env_test.go` — 183 lines, 8 table-driven builder tests + UUID format tests

### modified
- `internal/session/instance.go` — Cleaned all command builders, added SetEnvironment in Start/Restart paths
- `internal/session/instance_test.go` — Updated fork/build tests for new pattern (no uuidgen, no tmux set-environment)
- `internal/session/fork_integration_test.go` — Updated integration tests for Go-side UUID
- `internal/session/opencode_test.go` — Updated resume test expectations

## Decisions
- Applied universally (not `IsSandboxed()` conditional) per research recommendation: host-side SetEnvironment is idempotent for non-sandbox sessions
- Used `crypto/rand` for UUID generation rather than importing `github.com/google/uuid` to avoid new dependency

## Deviations
- Reverted an unrelated `waitForPaneReady` change in `tmux.go` that the initial executor agent had added (not in plan scope)
