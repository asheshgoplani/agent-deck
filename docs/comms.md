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

Two edges: `turn_end` (the harness's final assistant text, capped, plus
the sha256/16 of the full text) and `prompt_start` (the prompt that started
the turn, so the daemon knows why it ran; it is never stored on the record).

P1 enables two producers, each pinned by a versioned payload fixture under
`cmd/agent-deck/testdata/comms/` (G7 of the architecture review):

| Harness | Producer | prompt_start | turn_end | Fixture |
|---|---|---|---|---|
| Claude Code | `hook-handler` | `UserPromptSubmit.prompt` | `Stop.last_assistant_message` + `transcript_path` | `claude_*_v1.json` |
| Codex CLI | `codex-notify` (the existing `notify` line) | `input-messages` (last) on the same payload | `agent-turn-complete.last-assistant-message`, keyed by `turn-id` | `codex_notify_v1.json` |

A Codex `Stop` hook pointed at `hook-handler` is recognised by its
`turn_id` and never spooled, so notify and Stop cannot produce one turn
twice (`codex_stop_v1.json`). Every other harness (Gemini, Cursor, pi,
Hermes, OpenCode, shell) is **status-only** in P1: the daemon commits a
`status` record on the observed edge, with no text, and the inbox's legacy
record is unchanged. Their text producers are specified in the test
matrix and land one at a time, each with its own fixture and lab evidence.

Classification in the daemon: a Claude child is classified from its
transcript tail exactly as the inbox record is (same uuid, trigger, tier,
sentinel), so the two stores agree. Every other harness is classified from
what its hook carried: the prompt gives the trigger (a `[agent-deck from:]`
envelope is `send`, `[INBOX`/`[HEARTBEAT]`/`[agent-deck msg]` is `inbox`,
else `human`; no prompt seen is `unknown` and tiers urgent), the text gives
the hash, the sentinel and the question. The tier rule is
`ClassifyTurnTier` from #2469 against the child's previous ledger turn.

Not in P1 (notes):

- Codex gets no new `hooks.json` installer (the budget allows only the
  hooks already installed; `notify` carries the text).
- Gemini, Cursor, pi, Hermes and OpenCode producers: specified (matrix
  rows), not enabled. No installed file of theirs changes in this PR.
- Dropped from the research row, for later: a `tools/replay2469` run
  against the ledger, Cursor transcript roots in `ValidateTranscriptPath`,
  and fswatch on the spool (the daemon polls; its interval is seconds).
- Claude classification and the inbox differ only where the inbox path
  sees something the spool does not carry: a flip into the error status and
  an observed running->waiting flip with a stale transcript (both urgent
  in the inbox). A spool backlog is classified entry by entry: the
  transcript tail is used only for the turn it still describes (same
  full-text hash, not signalled before the tail record).

## Identity

Every record carries `v` (schema version 1), `id` (ULID), `key` (the
producer's idempotency key: transcript uuid, harness turn id, or the spool
entry id when a harness has neither), `host`, and `store` + `epoch`: the id
of the ledger it was first committed to and that ledger's epoch
(`<ledger>/store.json`, minted once; a reset or a restore bumps the epoch).
Imported records keep all of these and add `origin` and `src_cursor`.
Dedup is on `origin + key` within a window of the newest 4096 records,
restored at open; two distinct turns with identical text are two records,
one turn observed a hundred times is one. A text-less `status` edge is
collapsed only by the inbox's own content rule (same state and output
signal within the 2 h TTL), never by a time bucket.

## Consumers, receipts and retention (contract, built in P2)

Frozen now in `internal/comms/receipt.go` with fixtures under
`internal/comms/testdata/`:

- **Receipt** per `(message id, recipient, consumer generation, attempt)`
  with evidence states that only strengthen within an attempt:
  `durable` -> `attempted` -> `transport_accepted` -> `context_observed` ->
  `application_acked`; `failed` ends an attempt (the next starts at
  `durable`); `unknown` is kept as such when the adapter cannot observe
  landing. A timeout is a reconciliation trigger, never a promotion.
- **ConsumerState** per consumer (`<ledger>/cursors/<consumer>.json`): a
  contiguous acknowledged `watermark` plus bounded sparse `acked` cursors
  above it, bound to the ledger `store` and `epoch` and a consumer
  `generation`. Pending = every record above the watermark not in the
  sparse set, so an urgent record acknowledged ahead never hides an
  earlier info record. A state from another epoch is rejected and rebuilt.
- **Retention**: audit retention is `RetentionDays` (90); pending-delivery
  retention is `RetainFrom(consumers)` (one above the lowest watermark); a
  segment is compacted only when both allow it. A consumer whose watermark
  falls below the oldest retained cursor gets an explicit `Gap` (recorded
  as an `error` record addressed to itself) and is never silently
  restarted at the newest segment.

Four rules from the MonoCode relay comparison are part of this contract:

1. **Request id receipts.** A send carries the caller's request id (`req`);
   its receipt is the `send` record, committed before the action. A retry
   with the same id gets the stored record (`ErrDuplicate` + record,
   `Ledger.Lookup`), never a second delivery. Ledger: now; `session send`
   wiring: P2.
2. **Bounded reads.** At most N recent records, a per-message byte cap, a
   cursor for older ones, tool noise never stored. `ReadAfter(limit)`: now;
   `msg read --last N --max-bytes`: P2.
3. **Combined idle wake with rollback and a cap.** One wake carrying every
   pending record when the parent is idle; a failed parent turn returns the
   records to pending (`failed` receipt, `Retry`); at most `MaxAutoWakes`
   (20) automatic wakes per parent without a human or own turn in between.
   P2.
4. **Protocol-stream producers.** Where agent-deck launches the harness, an
   adapter may read its protocol stream (stream-json, app-server, ACP, pi
   rpc, OpenCode SSE) instead of hooks and spool the same edges. P3 or
   later, per harness, with the fixture rule of every producer.

## Recovery at every boundary

| Boundary | Crash or failure | Outcome |
|---|---|---|
| hook -> spool | crash before the rename | a `.tmp` file, never read as an entry; the turn is in the transcript (Claude) or lost to the ledger only (status edge still in the inbox) |
| spool -> daemon | daemon down or switch off | entries wait (per-instance cap 512, pruned after 24 h); the inbox path is untouched |
| daemon ingest | commit fails (disk) | the entry and every later one stay, the ledger is closed and reopened next pass; the inbox record and wake already happened |
| daemon ingest | crash after commit, before the spool file is removed | the entry is replayed and dropped as a duplicate of its key |
| ledger file | torn tail | truncated at open; mid-history corruption is left in place, logged, and skipped by readers |
| ledger dir | rotation or checkpoint | file fsync, atomic rename, directory fsync |
| two daemons | second process | cannot take `daemon.lock`; it reads, never writes or ingests |
| store restore | restored from backup | same `store` id, `epoch` must be bumped by the operator; consumer states from the old epoch are rejected |

The inbox and the turn journal keep their issue #2469 order (commit, then
journal); the ledger ingest runs after both and reads neither.

## Reading it

```
agent-deck events follow --bus comms --json [--after <cursor>] [--kind turn,status] [--session <id>]
agent-deck events stats --bus comms --json
```

The follower opens the ledger read-only (`events.Options{ReadOnly: true}`):
no writer goroutine, no tail repair, `Commit` refused. P2 adds `agent-deck
msg read|peek|ack|stats|export` with per-consumer cursor files.

## Surface

Files added: `internal/comms/{record,ulid,ledger,render,receipt}.go`,
`internal/events/commit.go`, `internal/session/{comms_spool,comms_ingest}.go`,
this file and the test matrix. Hooks installed per harness: unchanged (no
installed file of any harness changes). Config keys: `+1` (`[comms]
ledger`). Daemons: unchanged. Dependencies: unchanged (the ULID is in
`internal/comms/ulid.go`).
