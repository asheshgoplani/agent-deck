# TUI Codex Process-Snapshot Design

**Date:** 2026-08-04  
**Status:** Approved for implementation

## Goal

Reduce terminal UI status-sweep CPU spikes without changing session-status or
Codex-session binding behavior.

Profiling on macOS showed the TUI scanning 61 sessions every two seconds while
the Codex fallback rebuilt the full OS process table for each eligible session.
The host had more than 11,000 threads, so repeated `ps -eo pid=,ppid=` calls
and parsing were visible as periodic 100%+ CPU bursts.

## Decision

Keep the existing adaptive status-worker cadence. It already backs off from the
two-second base interval when a sweep overruns. Add a process-table snapshot
cache used only by Codex process-file probes:

- The first eligible probe collects and parses the process table.
- Concurrent probes reuse that snapshot for a short, bounded TTL.
- A failed collection is not cached; the existing `pgrep` fallback remains.
- Session-ID binding, probe intervals, and sandbox behavior remain unchanged.

The TTL is deliberately shorter than the two-second status cadence. It coalesces
the concurrent work in one sweep without making a newly spawned process
invisible for a meaningful period.

## Alternatives considered

1. Pass a snapshot explicitly from `Home.backgroundStatusUpdate` into every
   status and Codex-probe call. This gives exact sweep scoping, but it expands
   broadly used method signatures and would duplicate plumbing for CLI/web
   callers.
2. Slow all status polls. Rejected: the worker already backs off on slow
   sweeps, while a global cadence reduction would make real status changes less
   responsive.
3. Cache the focused process-table helper. Chosen: it removes repeated work
   for concurrent callers while keeping callers and observable behavior intact.

## Verification

- A regression test proves concurrent snapshot users invoke the collector once.
- A mutation that bypasses the cache causes that test to fail.
- Run the focused session package tests and the full Go suite with an isolated
  home directory.

## Out of scope

- Changing status labels, hook freshness, or Codex binding rules.
- New configuration knobs.
- Host-wide process or agent cleanup.
