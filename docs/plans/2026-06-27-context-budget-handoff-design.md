# Context Budget: Per-Session Token Warnings + Autonomous Handoff

**Date:** 2026-06-27
**Status:** Design approved, pending implementation plan

## Problem

Sessions should not run their context window past **250k tokens**. The user wants
escalating warnings as a session approaches that ceiling (at **150k** and **200k**),
and — for sessions agent-deck launched autonomously — an automatic, graceful
handoff to a fresh session before the ceiling is crossed, so no work is lost to
auto-compaction and no single session exceeds the budget.

agent-deck already tracks per-session context usage: `internal/session/analytics.go`
parses the Claude JSONL transcript into `CurrentContextTokens` (last turn's input +
cache-read = real context-window usage) and exposes `ContextPercent()`. The TUI shows
a green/yellow/red context bar (60%/80% breakpoints) in `internal/ui/analytics_panel.go`,
and conductor sessions already enforce a context policy: at 80% they proactively send
`/clear` (`clearOnCompactThreshold`, `ConductorClearOnCompact()` in `internal/ui/home.go`).
This design generalizes that existing, proven machinery into an **absolute-token
context budget** with a handoff state machine.

## Goals / Non-goals

**Goals**
- Escalating, absolute-token-aware warnings at 150k / 200k / 250k for every session.
- For autonomous sessions: automatic wrap-up → handoff-file → fork-new-session before 250k.
- All thresholds and behavior configurable via `config.toml`.
- Reuse the existing JSONL/analytics/background-loop/send-keys machinery; in-process Go,
  so enforcement does not depend on the agent cooperating.

**Non-goals**
- No change to interactive sessions beyond warnings — agent-deck never auto-acts on a
  session a human is driving.
- No new token-tracking source. Where a tool has no usable context-token signal
  (non-Claude tools today), warnings/handoff simply do not fire (documented limitation,
  not a silent failure).
- Not lossy: the failsafe never auto-`/clear`s; it pauses and alerts.

## Definitions

- **Current context tokens** — `CurrentContextTokens` from `analytics.go`: last turn's
  input tokens + cache-read tokens. This is context-window occupancy, *not* cumulative
  lifetime token spend. All thresholds are measured against this value.
- **Autonomous session** — a session agent-deck launched non-interactively with an
  initial prompt: conductor sessions, and parented/fleet children started with a prompt.
- **Interactive session** — any other session (one you attach to and type into).

## Thresholds & Configuration

New `config.toml` section, defaults matching the user's numbers:

```toml
[context_budget]
enabled                 = true
warn_tokens             = 150000   # soft warning
high_tokens             = 200000   # loud warning + autonomous wrap-up trigger
ceiling_tokens          = 250000   # hard ceiling (must not cross)
autonomous_handoff      = true     # fork-new-session handoff on autonomous sessions
handoff_timeout_seconds = 300      # failsafe window for the wrap-up to produce its file
```

Budget level derived from `CurrentContextTokens`:

| Level         | Condition                         |
|---------------|-----------------------------------|
| `normal`      | tokens < `warn_tokens`            |
| `warn`        | `warn_tokens` ≤ tokens < `high_tokens` |
| `high`        | `high_tokens` ≤ tokens < `ceiling_tokens` |
| `over`        | tokens ≥ `ceiling_tokens`         |

Boundaries are inclusive at the lower bound (e.g. exactly 150,000 = `warn`,
149,999 = `normal`). The wrap-up trigger reuses `high_tokens` (200k), leaving ~50k of
headroom for the agent to summarize, save work, and write the handoff file before 250k.

## Behavior

### Warnings — all sessions

Layered onto the existing context bar, but driven by absolute tokens against the new
thresholds rather than the 60/80% color breakpoints:

- **≥ `warn_tokens` (150k):** "warn" treatment on the bar/label + a budget badge in the
  session list.
- **≥ `high_tokens` (200k):** "high" (red) treatment + a one-time notification
  (notification bar; OS push if enabled).
- **≥ `ceiling_tokens` (250k):** loudest "over" banner. For interactive sessions this is
  purely informational — agent-deck does not touch a session a human is driving.

Warnings fire only when a usable context-token signal exists for the session's tool.

### Autonomous handoff — state machine

Per-session state machine, evaluated inside the existing `backgroundStatusUpdate()` loop
(2s cadence). State persisted per session so an agent-deck restart mid-wrap-up resumes
cleanly.

1. **NORMAL** — usage < `high_tokens`. Nothing to do.
2. **WRAP_REQUESTED** — usage crosses `high_tokens` (200k). agent-deck:
   - holds new work for the session (heartbeat / conductor dispatch paused),
   - creates the handoff directory `~/.agent-deck/handoff/<session-id>/`,
   - injects a wrap-up instruction (send-keys) telling the agent to finish/save current
     work and write a continuation prompt to `handoff/<session-id>/PROMPT.md` (plus any
     work notes alongside it),
   - sets a "wrapping up" status for the session,
   - records the trigger time (for the timeout failsafe).
3. **WAIT_HANDOFF** — poll for `PROMPT.md` to exist **and** the agent to have gone
   idle/waiting (so we don't fork while it's still writing).
4. **FORK** — read `PROMPT.md`; spawn a **new** agent-deck session that inherits the old
   session's tool, profile, project path, group, parent, and worktree; seed it with a
   short preamble + the handoff prompt. Archive (pause) the old session for history.
   New session title: `<old title> (cont.)`. Transition to **DONE**.
5. **FAILSAFE** — if `handoff_timeout_seconds` elapses with no `PROMPT.md`, OR usage
   crosses `ceiling_tokens` (250k) mid-wrap-up: **pause/stop the old session** (no data
   loss) and raise the loudest alert for the user to handle manually. Never auto-`/clear`
   — the user chose fork-not-clear, and clearing would discard history.

```
        usage ≥ high_tokens
NORMAL ───────────────────────▶ WRAP_REQUESTED
                                     │  inject wrap-up instruction,
                                     │  create handoff dir, pause work
                                     ▼
                                WAIT_HANDOFF
                          ┌──────────┴───────────┐
       PROMPT.md present  │                      │  timeout elapsed
       AND agent idle     ▼                      ▼  OR usage ≥ ceiling
                        FORK                   FAILSAFE
            (new session seeded,        (pause old session,
             old archived)               loudest alert, manual)
                          │
                          ▼
                        DONE
```

## Architecture & Code Layout

| File | Change |
|------|--------|
| `internal/session/userconfig.go` | New `[context_budget]` config struct + `GetContextBudget()`, using the file's existing load/save-safeguard patterns (atomic write, don't drop populated sections). |
| `internal/session/analytics.go`  | Helper exposing absolute `CurrentContextTokens` against the thresholds, e.g. `BudgetLevel(cfg)` returning `normal/warn/high/over`. |
| `internal/ui/analytics_panel.go` | Absolute-token-aware bar/label/badge treatment driven by `BudgetLevel`. |
| `internal/ui/home.go`            | Extend `backgroundStatusUpdate()` with the budget monitor + handoff state machine, reusing the conductor send-keys/debounce path and the existing fork/archive session paths. |
| `internal/statedb`               | Persist per-session handoff state. Prefer the existing `tool_data` JSON blob on the instances row to avoid a schema-version bump; only bump the schema (keeping LOCAL numbering, currently v13) if a dedicated column is genuinely needed. |

Each unit stays testable in isolation: config parse, threshold→level mapping, and the
state machine (behind interfaces for "read context tokens", "send keys", "fork session",
"archive session") are independently unit-testable without real tmux or a live agent.

## Error Handling & Edge Cases

- **No token signal (non-Claude tools):** budget level is undefined → no warnings, no
  handoff. Documented limitation.
- **Agent ignores / never writes the handoff file:** timeout failsafe → pause + alert.
- **Usage runs away during wrap-up:** ceiling-crossed failsafe → pause + alert.
- **agent-deck restart mid-wrap-up:** persisted handoff state resumes the state machine.
- **Concurrent agent-deck instances / external DB edits:** follow existing save-safeguard
  conventions (targeted updates, idempotent archive) to avoid clobbering — consistent with
  known archive/save-abort hazards in this codebase.
- **Debounce:** notifications and the wrap-up instruction are sent once per crossing, not
  every 2s tick (mirrors the conductor clear-on-compact debounce).

## Testing

- **Unit — thresholds:** boundary cases (149,999 / 150,000 / 199,999 / 200,000 /
  249,999 / 250,000) map to the correct level. Config parse/defaults/round-trip.
- **Unit — state machine:** table-driven test simulating context growth →
  WRAP_REQUESTED → `PROMPT.md` appears + agent idle → FORK; plus both failsafe branches
  (timeout, ceiling-crossed). Inject a fake token source and a fake send-keys/fork/archive
  interface — no real tmux, no live agent.
- **Interactive path:** assert warnings escalate but no wrap-up/fork is triggered.
- **Env note:** keep new tests off the env-flaky external deps (`internal/session` JSONL +
  python3, `internal/ui` zoxide, `internal/tmux` PTY) that fail in the sandbox regardless
  of changes.

## Open Questions

None outstanding. Both prior defaults confirmed by the user:
- "Autonomous" = launched-with-a-prompt (conductor + parented/fleet children).
- Failsafe = pause + alert (never auto-`/clear`).
