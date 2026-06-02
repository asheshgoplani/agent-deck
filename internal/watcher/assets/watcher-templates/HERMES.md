# Watcher: Shared Knowledge Base

This file contains shared infrastructure knowledge for all watcher instances.
Each watcher has its own identity in its subdirectory and its own LEARNINGS.md.
Agent sessions inspecting this directory will find the layout and CLI reference below.

## Layout

The singular watcher root mirrors `~/.agent-deck/conductor/`:

```
~/.agent-deck/watcher/
  HERMES.md        — this file (shared knowledge base)
  POLICY.md        — behavior rules (escalation, dedup, retry, health thresholds)
  LEARNINGS.md     — cross-watcher patterns accumulated over time
  clients.json     — routing map: sender key -> conductor name

  <name>/          — per-watcher directory (one per registered watcher)
    meta.json      — watcher registration metadata (type, created_at, expiry)
    state.json     — latest health snapshot (last_event_ts, error_count, adapter_healthy)
    task-log.md    — append-only event log (one Markdown heading per event)
    LEARNINGS.md   — per-watcher patterns (optional, watcher-specific)
```

## Files

| File | Purpose |
|------|---------|
| `HERMES.md` | Shared mechanism knowledge. Read by agent sessions to understand watcher infrastructure. |
| `POLICY.md` | Behavior rules: escalation thresholds, dedup strategy, retry backoff, health thresholds. |
| `LEARNINGS.md` | Cross-watcher patterns. Add entries as you notice recurring behaviors. |
| `clients.json` | Routing map loaded by the engine. Key format: `<adapter>:<channel>` or email address. |

## For Agents Inspecting This Directory

To inspect watcher health and list all registered watchers:

```bash
agent-deck watcher list --json
agent-deck watcher status <name>
```

The `list --json` output includes `last_event_ts`, `error_count`, and `health_status` per watcher.

To check if a specific sender would be routed:

```bash
agent-deck watcher test <name>
```

## Why This Folder Shape

Watcher state mirrors the conductor pattern (`~/.agent-deck/conductor/`) for consistency:
both subsystems use a singular directory root with shared top-level files and
per-instance subdirectories. This makes the layout predictable and tooling reusable.

The legacy `~/.agent-deck/watchers/` path is preserved as a relative compatibility
symlink pointing to `watcher/`. Existing tooling that reads `watchers/` continues
to work without modification.