# Watcher Policy

This file defines default behavioral rules for all watcher instances.
Override per-watcher behavior by editing the watcher's own `meta.json` or
by customizing `clients.json` routing entries.

## Escalation

Unrouted events (no match in `clients.json`) are forwarded to a triage session.

Defaults:
- Triage sessions are rate-limited to 5 per hour per engine instance.
- Events exceeding the rate limit are queued (capacity 16); excess events are
  marked `triage-dropped` in the event log.
- Triage results are written to `$XDG_DATA_HOME/agent-deck/triage/<dedup_key>/result.json`
  on new installs (or legacy `~/.agent-deck/triage/...` when existing triage
  state is present)
  and applied to `clients.json` by the triage reaper.

## Deduplication

The engine uses `INSERT OR IGNORE` with a `UNIQUE(watcher_id, dedup_key)` constraint
on the `watcher_events` table. Do not attempt in-process deduplication:
the database constraint is the single source of truth.

Dedup keys are computed by the adapter (typically a hash of sender + subject + timestamp
rounded to a window). Adapters are responsible for producing stable dedup keys.

## Retry

Adapter-specific reconnect behavior:

| Adapter | Backoff |
|---------|---------|
| ntfy | 2s initial, 2x multiplier, 30s cap |
| webhook | Single-shot HTTP server; no reconnect (always listening) |
| github | Single-shot HTTP server; no reconnect |
| slack | Single-shot HTTP server; no reconnect |
| gmail | OAuth2 Pub/Sub watch renewal 1 hour before expiry |

Consecutive adapter errors increment the health tracker's `consecutiveErrors` counter.
The counter resets on the next successful event.

## Health Thresholds

| Condition | Status |
|-----------|--------|
| `consecutiveErrors >= 10` or adapter unhealthy | `error` |
| `consecutiveErrors >= 3` | `warning` |
| Silence beyond `max_silence_minutes` | `warning` |
| Otherwise | `healthy` |

Health snapshots are persisted to `<name>/state.json` after every successful event.
The CLI command `agent-deck watcher list --json` reads `state.json` to populate
`last_event_ts`, `error_count`, and `health_status` fields without starting the engine.
