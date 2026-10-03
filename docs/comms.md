# Comms Ledger (`internal/comms`)

One append-only message log per profile, written only by the notify daemon,
read by cursor. It generalises the per-parent inbox and the per-child turn
journal of issue #2469 into one record schema and one store, and it is fed by
the hooks agent-deck already installs: they forward the text they receive
instead of discarding it.

Phase P0 + P1 (this document): the primitive and the producers, behind one
switch, written next to the old stores (dual write, zero behaviour change).
P2 adds the consumers (`agent-deck msg`). Later phases retire the inbox, the
journal and the inbox stats so the total surface shrinks.

## Switch

```toml
[comms]
ledger = true   # default false
```

Off: no spool file is written, no ledger directory is created, nothing
changes. On: producers spool, the daemon commits, `agent-deck events follow
--bus comms` streams the records.

## Record

One canonical-JSON line per record on an `internal/events` bus opened at
`<data>/comms/<profile>/` (`active.ndjson`, sealed `seg-*` files, 90-day
retention). The frame's `kind` is the record kind and its `session_id` the
record's `from`; the record is the frame's `data`:

| Field | Meaning |
|---|---|
| `id` | ULID assigned at commit (time-ordered; the last 6 characters are what a consumer prints) |
| `key` | idempotency key; a second commit with the same key within the window is dropped |
| `kind` | `turn`, `send`, `delivery`, `wake`, `human`, `error`, `status` |
| `from`, `to[]` | session ids (`to` is the parent, or `_unowned`; a tagged send's reply also lists the sender) |
| `profile`, `tool`, `origin` | owning profile, harness of `from`, remote name when pulled over ssh |
| `tier`, `trigger` | `urgent` / `info` / `noise`; `human` / `send` / `task` / `system` / `inbox` / `unknown` |
| `text`, `th`, `bytes`, `q` | capped text ([inbox] `max_text_bytes`), its sha256/16, length, parent-facing question |
| `done`, `summary`, `err` | completion sentinel; error text |
| `seq`, `reply_to`, `via`, `state`, `ref` | per-`from` sequence; sender of the prompt; transport; delivery state; related record id |
| `t_signal`, `t_record`, `t_pushed`, `t_seen`, `latency_ms` | the harness signal, the commit, the push, the consumer's prompt; signal to commit in ms |

Noise is stored too (tier `noise`, never delivered) so dedup and noise share
are countable from the ledger alone.

### Remote first

Sessions on different hosts talk through the same records. Every record
carries `host` (the producing machine, stamped at commit) so a reader can
name where a child ran. When a conductor pulls another host's ledger (P3:
`msg export --after <cursor>` over ssh, or a daemon push over the remote
channel when it is up), each record is committed locally through
`Ledger.Import(origin, exported)`:

- `origin` is the configured remote name, `src_cursor` the record's cursor
  on the origin ledger; id, key, host, sequence and timestamps are kept.
- idempotency is on `origin + key` (`Record.DedupKey`), so a re-pull adds
  nothing and two hosts' children can never collide, whatever their ids.
- the puller stores, per origin, the highest `src_cursor` Import returned:
  every exported record up to it is durable locally. An error stops the
  cursor just before the failed record so the next pull retries it.
- addressing does not change: `to` holds session ids; the record's `origin`
  says which host's ledger to reach the sender on, which is what child to
  child across hosts needs (the daemon routes a `send` record to the host
  whose ledger owns the target).

`comms.Export(bus, after, limit)` returns `{cursor, record}` pairs for the
transport to ship; the same pairs are what a future workflow runner waits
on (see RESULTS of the build worker).

## Producers (P1)

The daemon is the only ledger writer (the #824 rule: an in-process mutex
cannot serialise hook processes). Producers spool one small JSON file per
observed edge under `<data>/runtime/comms/spool/<instance>/<ulid>.json`
(tmp + fsync + rename) and the daemon drains it on its poll loop.

Two edges: `turn_end` (the harness's final assistant text) and
`prompt_start` (the prompt that started the turn, so the daemon knows why
it ran). What each harness forwards, all from hooks `agent-deck launch`
already installs:

| Harness | Producer | prompt_start | turn_end |
|---|---|---|---|
| Claude Code | `hook-handler` | `UserPromptSubmit.prompt` | `Stop.last_assistant_message` + `transcript_path` |
| Codex CLI | `codex-notify` (the existing `notify` line) | `input-messages` (last) on the same payload | `agent-turn-complete.last-assistant-message` |
| Gemini CLI | `hook-handler` | `BeforeAgent.prompt` | `AfterAgent.prompt_response` |
| Cursor CLI | `hook-handler` (`afterAgentResponse` added to the installed events) | `beforeSubmitPrompt.prompt` | `afterAgentResponse.text` |
| Pi / Oh My Pi | extension v3 (same file) | `input.text` | `agent_settled` with the newest `turn_end.message` text |
| Hermes | `hook-handler` | `pre_llm_call.user_message` | `post_llm_call.assistant_response` |
| OpenCode | the TUI's SSE watcher | last user message | root session idle (2 s debounce, parentID chain), `GET /session/:id/message` |
| Plain shell | the daemon | none | `status` record on the observed edge |

Classification in the daemon: a Claude child is classified from its
transcript tail exactly as the inbox record is (same uuid, trigger, tier,
sentinel), so the two stores agree. Every other harness is classified from
what its hook carried: the prompt gives the trigger (a `[agent-deck from:]`
envelope is `send`, `[INBOX`/`[HEARTBEAT]`/`[agent-deck msg]` is `inbox`,
else `human`; no prompt seen is `unknown` and tiers urgent), the text gives
the hash, the sentinel and the question. The tier rule is
`ClassifyTurnTier` from #2469 against the child's previous ledger turn.

Not in P1 (notes): Codex gets no new `hooks.json` installer (the budget
allows only the hooks already installed; `notify` carries the text). Pi
turns without an `agent_settled` (older pi) are status only. OpenCode text
needs a session launched with `--port` and a running TUI (the SSE watcher
lives there); without one it is status only.

## Reading it

```
agent-deck events follow --bus comms --json [--after <cursor>] [--kind turn,status] [--session <id>]
agent-deck events stats --bus comms --json
```

The follower opens the ledger read-only (`events.Options{ReadOnly: true}`):
no writer goroutine, no tail repair, `Commit` refused. P2 adds `agent-deck
msg read|peek|ack|stats|export` with per-consumer cursor files.

## Surface

Files added: `internal/comms/{record,ulid,ledger,render}.go`,
`internal/events/commit.go`, `internal/session/{comms_spool,comms_ingest}.go`,
this file. Hooks installed per harness: unchanged (Cursor gains one event in
the hooks.json agent-deck already writes; the pi extension file is
re-versioned). Config keys: `+1` (`[comms] ledger`). Daemons: unchanged.
Dependencies: unchanged (the ULID is 80 lines in `internal/comms/ulid.go`).
