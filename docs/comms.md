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

A repeated background answer is stored too (tier `noise`, never delivered)
so the noise share is countable from the ledger alone; a re-observed turn
(same key) is not stored at all, it is a duplicate.

A tagged send whose turn hands off to background work (issue #2473) is
stored as two turns: the held send turn, which replies to no one, and the
task turn that settles the work, which carries `reply_to`, so the sender is
answered once, with the result. The ledger keeps the sender it owes in its own
record (`runtime/held-send-ledger/<child>.json`). The ledger and the inbox
each answer the sender exactly once, but not always on the same turn: when a
permission menu opens while the work runs, the inbox answers on the send turn
and the ledger on the result turn; when a held send is drained only after the
work has settled, the ledger answers on the send turn itself.

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
transcript tail as the inbox record is (same uuid, trigger, tier,
sentinel) while the tail still describes the spooled turn; the two
inbox-only inputs listed under "Not in P1" are the known differences. Every other harness is classified from
what its hook carried: the prompt gives the trigger (a `[agent-deck from:]`
envelope is `send`, `[INBOX`/`[HEARTBEAT]`/`[agent-deck msg]` is `inbox`,
else `human`; no prompt seen is `unknown`), the text gives the hash, the
sentinel and the question (urgent only for a sentinel, an error or a
question, as the inbox since #2478). The tier rule is
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
Dedup is on `key` (namespaced by the origin **store id** for a pulled
record; the remote alias and `host` are display only) within a window of
the newest 4096 records, rebuilt at open up to the newest parseable frame
(a malformed last line does not block the open; open fails visibly,
`ErrTailUnreadable`, only if the log cannot be read at all, so the daemon
never writes with a partial window). Two distinct turns with
identical text are two records; one turn observed a hundred times is one;
the same key with different content is a conflict (`ErrConflict`): the
spool entry is moved to `spool/conflict/<instance>/` and logged, never
committed as a second record. `seq` is a per-sender counter restored from
that window: monotonic across restarts for a sender active in the newest
4096 records, restarting at 1 after a longer silence (ordering is the
cursor and the id). A text-less `status` edge is keyed on its spool entry
id (minted once by the producer), so a replayed entry is a duplicate of
its key however late it comes; separately, a re-observation of the same
edge is collapsed by the inbox's own content rule (same from->to edge with
the same non-empty output signal within the 2 h TTL, or within the 90 s
short window when the signal is empty), which mirrors what the inbox
records and is never what makes a replay safe.

Restore detection: `store.json` keeps the ledger's high-water cursor
(persisted at close and every 256 commits). A ledger that opens with a
cursor below it was restored from an older copy: the epoch is bumped and
logged (`comms_store_restored`), and consumer states from the old epoch are
rejected until rebuilt.

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
- **Spent cursors (gap rule)**: a cursor no record carries (a malformed
  line, a rolled-back commit, a frame that is not a record) can never be
  delivered or acknowledged. `ConsumerState.SkipSpent(after, records,
  through)` moves the watermark over such cursors once every record below
  them is acknowledged, given one complete read pass from at most the
  watermark (`ReadAfter` / `Export`, which return the cursor the pass
  covered, past any trailing spent cursors). Spent cursors never enter the
  sparse set and never hold `RetainFrom`. Fixture:
  `testdata/consumer_state_fixture.json` (`skip_spent`).
- **Retention and quota**: compaction is by count (1024 sealed segments)
  and age (`RetentionDays`, 90), and since P2 never drops a segment a
  recently active consumer still needs (`RetainFrom(consumers)`, one above
  the lowest watermark). The ledger is bounded at `DefaultMaxBytes` (2 GiB per
  profile): past it `Commit` returns `events.ErrQuota`, the spool keeps its
  entries (bounded by the per-instance cap of 512), the daemon logs the
  overload once per minute, and nothing is silently dropped. A consumer
  whose watermark falls below the oldest retained cursor gets an explicit
  `Gap` (kept in its state file's `gaps` and printed by `msg read`) and is
  never silently restarted at the newest segment.
- **Pending indexes** (P2, built): the per-consumer state file and the
  pending flag are caches; a pass always scans from the consumer's
  watermark, and the daemon rebuilds the flags from its dedup window at
  open, so a crash between a commit and a flag update is repaired, never
  trusted.

Four rules from the MonoCode relay comparison are part of this contract:

1. **Request id receipts.** A send carries the caller's request id (`req`);
   its receipt is the `send` record, committed before the action. A retry
   with the same id and the same content gets the stored record
   (`ErrDuplicate` + record, `Ledger.Lookup`), never a second delivery; a
   retry with the same id and different content is a conflict. The window
   is the newest 4096 records (a retry weeks later is a new send). Ledger:
   now; `session send` wiring: P2.
2. **Bounded reads.** At most N recent records, a per-message byte cap, a
   cursor for older ones, tool noise never stored. `ReadAfter(limit)`: now;
   `msg read --last N --max-bytes`: built (P2).
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
| spool -> daemon | daemon down, or the ledger cannot open | entries wait (per-instance cap 512; never pruned while the switch is on); the inbox path is untouched |
| spool -> daemon | switch off | entries no daemon will consume are pruned after 24 h, each expiry logged |
| daemon ingest | commit fails (disk, quota) | the entry, its prompt edge and every later one stay, the ledger is closed and reopened after a 1 min backoff; the inbox record and wake already happened (status edges are spooled after the inbox record is committed, never before) |
| daemon ingest | same key, different content | the entry is quarantined under `spool/conflict/` and logged |
| daemon ingest | crash after commit, before the spool file is removed | the entry is replayed and dropped as a duplicate of its key |
| ledger file | torn tail | truncated at open; mid-history corruption is left in place, logged, and skipped by readers |
| ledger file | malformed first, middle or last line, or a file of only malformed lines after a rotation | the writer and every reader open it; each malformed line's cursor is spent (counted on top of the sealed history and checkpoint), readers stop at the newest parseable frame and the next commit takes the cursor after the spent ones |
| `Commit` | short write or failed fsync | the bytes are truncated away, the error is returned, the bus disables itself until reopened, and the cursor is recorded as spent (`spent.cursor`, fsynced) so it is never reused for a different frame |
| ledger dir | rotation or checkpoint | file fsync, atomic rename, directory fsync |
| two daemons | second process | cannot take `daemon.lock`; it reads, never writes or ingests |
| ledger open | the dedup window cannot be rebuilt to the newest frame (the log cannot be read) | open fails with `ErrTailUnreadable`, retried after 1 min; the inbox path is untouched |
| store restore | restored from an older copy | detected at open from the high-water mark: the epoch is bumped and logged; consumer states from the old epoch are rejected |

The inbox and the turn journal keep their issue #2469 order (commit, then
journal); the ledger ingest runs after both and reads neither.

## Reading it

```
agent-deck events follow --bus comms --json [--after <cursor>] [--kind turn,status] [--session <id>]
agent-deck events stats --bus comms --json
```

The follower opens the ledger read-only (`comms.OpenReader`:
`events.Options{ReadOnly: true, KeepCorrupt: true}`): no writer goroutine,
no tail repair, `Commit` refused, malformed lines skipped as the writer
skips them. Visibility boundary: a frame is committed when `Commit`
returns its cursor (bytes and fsync done, or rolled back on failure). A
ledger follower reads new bytes of the active file under the writer lock,
which `Commit` holds from the append through the fsync or the rollback, so
it sees a frame only after its `Commit` finished and never sees a
rolled-back one. Under the same lock it checks that no rotation sealed the
file since its listing (if one did, it re-lists, so sealed frames are never
skipped). The locked part is a plain read of the new bytes; frames are
decoded and emitted after the lock is released, so a follower that is slow
to drain never holds the lock. Bounds are found from the first and last
line; only a malformed first or last line costs a full scan. A rolled-back
cursor is also recorded as spent, so a reader that saw anything under that
number could never be handed a different frame with it. One window
remains: a writer process that dies between its append and its fsync
releases the lock, and a follower may read that complete line before the
next open; the next ledger open fsyncs the active file, so the frame is
kept and its cursor never reused unless the machine itself loses power in
that window.

## Reading by consumer (`agent-deck msg`, P2)

```
agent-deck msg read   [--for <session|self|name>] [--last N] [--max-bytes B] [--json]
agent-deck msg peek   [--for <session|self|name>] [--last N] [--max-bytes B] [--json]
agent-deck msg ack    [--for <session|self|name>] [--json] (<id>|<cursor>... | --all)
agent-deck msg export [--after <cursor>] [--limit N] [--for <session>] [--json]
agent-deck msg stats  [--since 24h] [--parent <session>] [--json]
```

A **consumer** is whoever records are addressed to: a session id in a
record's `to`, or a named reader such as `human:<conductor>`. `--for`
defaults to the calling session (`AGENTDECK_INSTANCE_ID`). A record is a
consumer's news (`comms.Deliverable`) when it is a `turn`, `status`,
`delivery`, `human` or `error` record addressed to it and not `noise`; a
`send` record's first recipient is its target, which the send's own
transport reached, so only the observers after it (a parent following
its children's exchange) read it here; `wake` and `call` records are
measurement rows and never delivered.

State: `<ledger>/cursors/<consumer>.json` holds the P0 `ConsumerState`
(watermark plus sparse acknowledgements, store, epoch, generation) and
the last pass (`through`, `pending`, `gaps`). Every read-modify-write runs
under `<consumer>.lock` (flock, 5 s wait), so the prompt hook, the Stop
hook, `msg read|ack` and the daemon never lose each other's
acknowledgements. A pass reads the log from the watermark, moves the
watermark over every cursor that is not the consumer's pending news
(spent, addressed elsewhere, noise, measurement, acknowledged) and
returns the rest oldest first. `read` takes urgent records first, then
the rest, within `--max-bytes` (9000, under Claude's additionalContext
limit) and `--last`, prints them, and acknowledges exactly what reached
stdout; the rest stays pending. `peek` acknowledges nothing. `ack` takes
record ids, their last 6 characters (what every rendered line shows) or
cursors; an unknown reference fails and acknowledges nothing.

Where a consumer starts: the daemon creates a recipient's state, just
before the first record addressed to it becomes visible, so it reads
from that record on whoever reads first; a name nobody ever addressed
starts at the end of the log. Records committed before a recipient's
state existed (a ledger written by a P1 daemon, which created none) are
not pending for it: the inbox delivered them, and `msg export` still
shows them. The pending flag is only a fast-path hint and never decides
where a consumer starts. A state from another ledger store or epoch is
rebuilt (generation + 1) at the start of the new epoch (`store.json`
`epoch_start`: what the restored copy holds may or may not have been
shown), and whatever it had not read is kept as a gap. A watermark that compaction overtook is moved to the
oldest retained cursor and the loss kept as a gap (`gaps[]`, also printed
by `read`): never a silent restart.

Pending flags: the daemon keeps `<ledger>/pending/<consumer>.json` (the
newest cursor of a deliverable record addressed to it, and the epoch),
written before the record becomes visible and rebuilt from its dedup
window at open. A reader checks the flag and the consumer file before it
opens the log (`HasNothingPending`); the flag only lets it skip work, a
pass always reads from the watermark.

Pending-delivery retention: the ledger's compaction asks
`ConsumersRetainFrom` (one above the lowest watermark of every consumer
that read within the 90-day audit window) and never drops a sealed
segment holding a cursor at or above it, whatever its age or the segment
count; the 2 GiB quota still bounds the log, so a stuck reader becomes an
explicit `ErrQuota`, never a silent drop.

### `msg export --json` (stable contract)

```json
{"v":1,"ledger":true,"profile":"default","store":"01J...","epoch":1,"now_ms":1791100000000,
 "after":0,"through":42,"more":false,
 "records":[{"cursor":1,"record":{"v":1,"id":"01J...","kind":"turn","from":"...","to":["..."], "...": "..."}}]}
```

Nothing is consumed. `through` is the last cursor the call covered;
when `more` is true, call again with `--after <through>`. `--limit`
bounds a call (default 1000); `--for` keeps records from or addressed to
one session. With no ledger for the profile (switch off, daemon never
ran) the call succeeds with `"ledger": false` and no records, so a rig can
tell "off" from "empty". Record fields are the table above; readers keep
the fields they know (`v` is the schema version).

### `msg stats --json`: the #2482 targets

One scan of the records committed in the window (`--since`, default 24 h;
an imported record counts when it arrived). Each target carries `value`,
`target`, `op`, `met` and `n` (its denominator); with no data `value` and
`met` are null, so an empty window never reads as a pass.

| Target | Value | Counted from |
|---|---|---|
| `wakes_per_parent_hour` <= 4 | the busiest parent's wakes per hour | `wake` records: every machine wake on either path (inbox typed nudge, inbox digest, inbox Stop-hook block, and the ledger's own wakes), spooled by whoever fired it |
| `records_with_text_pct` >= 95 | share of `turn`, `status`, `send`, `human` records (noise excluded) with text | records |
| `records_per_finished` <= 3 | deliverable `turn` and `status` records per turn carrying a completion sentinel | records |
| `duplicate_turn_pct` == 0 | turn records repeating a key, or one child's same full-text hash signalled within 5 s under another key | `turn` records |
| `output_and_drain_calls_per_wake` == 0 | `session output` and `inbox drain` runs by any session (heartbeat drains included) per recorded wake; calls with no wake are not met | `call` records, spooled by the CLI when it runs inside a session (`msg read` is recorded too, not counted) |
| `send_with_sender_and_text_pct` == 100 | `send` records with a sender and a text hash | `send` records (P3) |
| `cross_host_records_with_latency` > 0 | imported records whose offset-corrected latency was measured | records with `origin` (P3) |

Limits, stated: the wake count covers the wakes agent-deck records
(inbox typed nudge, inbox digest, inbox Stop-hook block, the ledger's own
wakes); heartbeats, `/loop` timers and direct `session send` lines are
not wake records, so the value is a lower bound of the #2482 baseline's
"machine wakes" (sends become `send` records in P3). A window with records
but no wake record has value 0 and no verdict (a daemon too old to record
wakes looks the same). Re-read calls by a session that was never woken
still count; calls with no wake at all are not met. Both `msg stats` and
a consumer pass read their range in one bounded pass (10 s); a ledger
past about a gigabyte in one window needs paging, which is not built.

`parents[]` breaks wakes down per parent and per path/transport
(`inbox/tmux`, `inbox/stop`, `ledger/tmux`, ...) so a canary parent on
the ledger path can be compared with the others on the inbox path.

## Surface

P2 (msg verbs): `internal/comms/{consumer,stats}.go`,
`cmd/agent-deck/msg_cmd.go`; on disk `cursors/` and `pending/` under the
ledger; record kind `call` (measurement) and the `refs`, `t_import`,
`xlat_ms`, `xlat_err_ms` fields; `events.Options.RetainFrom`,
`Bus.Oldest`, `Bus.CursorBefore`. Config keys, hooks and daemons
unchanged.

P0 + P1: files added: `internal/comms/{record,ulid,ledger,render,receipt}.go`,
`internal/events/commit.go`, `internal/session/{comms_spool,comms_ingest}.go`,
this file and the test matrix. Hooks installed per harness: unchanged (no
installed file of any harness changes). Config keys: `+1` (`[comms]
ledger`). Daemons: unchanged. Dependencies: unchanged (the ULID is in
`internal/comms/ulid.go`).
