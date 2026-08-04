# TUI and Child-Follow Polling Design

**Date:** 2026-08-04  
**Status:** Approved for implementation

## Goal

Reduce CPU and tmux subprocess pressure when a large Agent Deck fleet has an
active TUI and many `session children --follow` monitors.

## Observed bottlenecks

The restarted host had 66 sessions, 35 concurrent child-follow monitors, and a
TUI using more than one CPU core. Each child-follow poll loaded the full
profile and then refreshed only the matching children. In the TUI, every
eligible Codex card independently enumerated all managed tmux sessions and
read their `CODEX_SESSION_ID` environment variables.

## Decision

1. Give child-follow polling a narrow storage read: confirm the parent and
   load only its direct children. It must preserve the existing output and
   error when the parent was removed.
2. Cache the cross-session Codex ID set for one second. Concurrent status
   checks reuse the same set; a failed lookup is not cached.

## Non-goals

- Do not change the child-follow CLI contract, polling interval, status
  semantics, or completion ledger logic.
- Do not stop, kill, or alter existing user/agent follow monitors.
- Do not add daemon infrastructure or configuration knobs.

## Verification

- Regression tests prove the narrow child query returns only direct children
  and still reports a missing parent.
- A concurrent cache test proves one tmux-snapshot collection serves all
  concurrent Codex callers.
- Build the command and run the focused tests. The current full session suite
  baseline is blocked by an unrelated untracked disconnect-recovery test.
