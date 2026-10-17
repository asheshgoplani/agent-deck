# Remote conversation and status reads

The controller forwards these read commands to the configured owner host:

```sh
agent-deck remote HOST recall timeline ID --json --tail 100
agent-deck remote HOST recall timeline ID --json --since CURSOR
agent-deck remote HOST recall timeline ID --json --before BEFORE_CURSOR --limit 100
agent-deck remote HOST recall follow ID --after CURSOR --jsonl --status
agent-deck remote HOST events follow --jsonl --since 123
agent-deck remote HOST session send-status SEND_ID --json
```

Use `remote exec HOST ...` if the host alias collides with a remote management
command. To obtain the owner's command help, use the verb alone followed by
`--help`, for example `remote HOST recall timeline --help`. Timeline snapshots
use `agent-deck.recall.rows/v2` unchanged, with additive `before_cursor` for older
history. Follow emits the local row/update/remove/status/delivery/resync_required
frames. Events emits the local canonical event frames with numeric `cursor`,
`event_id`, `ts`, `kind`, `session_id`, and producer-specific `data`. Resume from
the last processed cursor; a cursor outside retained history requires a fresh
snapshot. See [Recall timeline](recall-timeline.md) for row and paging contracts.

Every new remote read performs capability and flag validation inside the same
SSH channel as the actual read. Help output stays on the owner host. An older
remote that rejects the verb or flags yields exit 1 and, for JSON/JSONL requests,
a single object with `error`, `remote`, and `remote_version: "unknown"`. Its error
contains `unsupported remote command`; clients can fall back without treating
missing delivery evidence as confirmation. Older controllers still refuse the
new forwarded verbs before opening SSH. Runtime failures remain failures.

Snapshots and send-status have a five-minute deadline. Capability negotiation
has a twenty-second deadline. Connections use the existing SSH dial timeout,
noninteractive authentication, and keepalives every fifteen seconds with three
misses allowed. Follow owns one dedicated long-lived channel and does not retry;
a consumer reconnects explicitly using its last processed cursor. The
controller forwards its stdin over SSH as the lifetime signal, so a follow
runs only while that stdin stays open: close it or terminate the controller to
end the channel, which then exits 0. A follow started with stdin from
`/dev/null` (`</dev/null`, or `cmd &` inside a non-interactive `sh -c`) ends
within seconds with exit 0 and no output; a long-lived consumer holds a pipe
to its stdin. Local `recall follow` and `events follow` ignore stdin. The owner supervisor
terminates and reaps its own core child, with bounded escalation for a child
that ignores TERM. Output streams directly through the controller.

`list --json`, `list --all --json`, and `remote sessions HOST --json` include
`transcript_path` and the applicable `claude_session_id` or `codex_session_id`
when known. Paths belong to the host that owns the session. Missing fields mean
unknown. SSH placeholder sessions never resolve a transcript on the controller.
Claude listing lookup uses only the expected physical/logical project paths.
Codex listing groups stored IDs by home and enumerates the date-layout archive
once per home, retaining only matches and reading only matched metadata headers.
It does not inspect panes or scan conversation bodies to discover a replacement
thread; Recall resolves those cases when opened.

One events stream per host forwards status deltas without full-list polling.
The owner host must enable `[macapp] status_events = true` and have an active
status producer (TUI or notify daemon). Forwarding does not change the owner
configuration or create status transitions. Add `--read-only` to observe the
remote bus without restarting its queued send workers, opening a writer or
taking a demand lease (docs/events.md); a remote whose agent-deck predates the
flag rejects it.
Transcript notifications similarly require the existing `transcript_events`
setting.
Optional `remote sessions --since` delta snapshots are not implemented. Initial
session metadata still requires a list snapshot. Deep history pages transmit
only the requested rows and retain only those row bodies, but parser identity
memory and archive enumeration remain proportional to inspected history.
Attached sub-agent children preserve their existing behavior and can be large.
