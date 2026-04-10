# Session ID Lifecycle (Authoritative and Race-Safe)

This document defines the authoritative lifecycle for tool session IDs (Claude, Codex, Gemini) in agent-deck.

## Invariants

1. Disk scans are non-authoritative for identity binding.
2. Session ID binding/rebinding happens only from:
   - tmux environment (`*_SESSION_ID`)
   - hook payload `session_id`
   - hook sidecar anchor (`~/.agent-deck/session-hooks/<instance>.sid`) when payload omits ID
3. Every bind/rebind/reject decision is appended to:
   - `~/.agent-deck/logs/session-id-lifecycle.jsonl`
4. Reject decisions must preserve the currently bound ID.

## Creation and Persistence

1. Session starts with a generated/preselected ID in agent-deck for capture-resume flows.
2. The ID is mirrored into tmux env (`*_SESSION_ID`).
3. Hook anchor sidecar is written so hook updates can be correlated after restart.

## Reconnect / Restart

1. On reconnect/restart, agent-deck reads tmux env and hook updates.
2. If tmux is gone and no hook evidence exists, the last persisted ID remains unchanged.
3. No disk-based reassignment occurs during reconnect/restart/fork/output.

## Fork / Clear / ID Changes

1. `fork` creates a new target ID and binds it through start/resume paths.
2. Tool-driven ID rotation (`/clear` or equivalent) is accepted only when surfaced by tmux/hook evidence.
3. Unknown or invalid candidates are rejected and logged.

## Event Log Schema

Each JSONL entry contains:

- `instance_id`
- `tool`
- `action` (`bind`, `rebind`, `reject`, `scan_disabled`)
- `source` (`tmux_env`, `hook_payload`, `hook_anchor`, `disk_scan`)
- `old_id`, `new_id`, `candidate`
- `reason`
- `hook_event`
- `ts`

