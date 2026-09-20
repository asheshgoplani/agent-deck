# Recall: cross-harness conversation store

Recall is agent-deck's memory of what every managed session was for and what
happened in it, across harnesses (Claude, Codex, Gemini, Pi, OpenCode,
Hermes). It has two halves with different durability:

- **state.db** (per profile, existing) holds what a human or a binding wrote:
  hints, tags and harness links. This survives any rebuild.
- **recall.db** (machine-global) holds what is reproducible from
  transcripts: messages, an FTS5 index, cards, derived summaries. Deleting
  it costs a rebuild and nothing else.

Phase 1 shipped the first half (hints). Phase 2 shipped the index for
Claude transcripts behind `[recall] enabled = true`: `agent-deck recall
backfill|sweep|status|sessions|search|show|open|gc|rebuild`. Phase 3 adds
the Codex, pi, Gemini, OpenCode and Hermes readers to the same index, the
hook and session-event triggers that keep it fresh, and the TUI `G` key.
`session search` is unchanged (substring, active profile, no index);
`recall search` is a separate command over every profile and harness.

## Phase 1: hints

A hint is a single-valued key on a session (`purpose`, `ticket`, `why`,
`decision`, `outcome`, `note`, `parent`, or any identifier-shaped key you
choose). Setting a key again replaces it: correcting `ticket=SB-412` to
`SB-413` leaves one row, not two. Tags are a set. Everything is written to
the profile's `state.db` (`session_hints`, `session_tags`) and pruned with
the session when it is removed.

### At creation

```bash
agent-deck add -t auth-fix -c claude ~/src/app \
    --hint purpose="fix flaky auth test" --ticket SB-412 --tag auth --tag flaky \
    --why "3rd regression this month"
agent-deck launch ~/src/app -c claude --ticket SB-412 -m "Fix the flaky auth test"
```

`add` and `launch` also derive two hints without any flag, so the corpus
grows on the high-volume path: `parent=<parent session id>` for every child,
and, on `launch`, `purpose` from the first line of the message (clipped to
200 characters). An explicit `--hint purpose=...` always wins over the
derived value. A conductor session gets `purpose=conductor <name>` when it is
registered by `conductor setup`.

### Afterwards

```bash
agent-deck session annotate auth-fix --decision "root cause was clock skew" --outcome worked --tag clock-skew
agent-deck session annotate auth-fix --set-hint ticket=SB-413 --remove-tag flaky --unset why
agent-deck session annotate --self --note-stdin < summary.md   # an agent, on its own session
agent-deck session annotate auth-fix --json                     # read: hints, tags, links
agent-deck remote lab session annotate auth-fix --outcome worked
```

`--self` resolves the calling session from `AGENTDECK_INSTANCE_ID` (or the
current tmux session). Over `remote <host>`, the command runs on the remote
and writes the remote's own `state.db`; a remote session's hints are only
reachable through that path and never copied locally. A remote whose
agent-deck predates `session annotate` answers with one line, `remote "lab"
runs v1.16.10 without session annotate; update it with 'agent-deck remote
update lab'`, exit 1; with `--json` the same failure is
`{"error", "remote", "remote_version"}` (`remote_version` is `unknown` when
the remote's version could not be read). The remote's own usage text is never
forwarded.

Hint values are capped at 8 KiB. `--note-stdin` reads one note from stdin
and stores it under the `note` key (replacing a previous note).

### Harness links

`session_links` records which harness conversation id an instance is bound
to (`claude`, `codex`, `gemini`, or a custom tool name). It is written only
by the binding writers that already know the mapping. For Claude that is two
paths: a hook-driven bind or rebind, and the first live hook that confirms an
id agent-deck minted itself at launch (`--session-id`), so every local Claude
session that fires hooks ends up with an authoritative row. A rebind keeps
the previous id as history with `authoritative = 0`, and a candidate the
adoption arbitration rejects has its row retracted. Later phases bind
transcripts to sessions only through an authoritative link, never by
working directory.

### Config

```toml
[recall]
enabled = false   # reserves the section; gates the recall.db index (phase 2+)
```

Hints and `session annotate` do not depend on `enabled`.

### Schema

Additive `CREATE TABLE IF NOT EXISTS` in `Migrate()`; `SchemaVersion` stays
13 because an older binary sharing the same `state.db` would rewrite a
bumped version back on every open.

```sql
session_hints(id, scope_kind, scope_id, harness, key, value, seq, source, author, created_at, superseded_at,
              UNIQUE(scope_kind, scope_id, key))
session_tags (scope_kind, scope_id, tag, seq, source, created_at, deleted_at, PRIMARY KEY(scope_kind, scope_id, tag))
session_links(session_id, harness, native_id, host, authoritative, first_seen, last_seen,
              PRIMARY KEY(session_id, harness, native_id))
```

`scope_kind` is `instance` (keyed by the agent-deck session id) or
`harness_session` (keyed by the harness conversation id, populated by the
binding path in later phases). `source` is one of `cli_create`, `annotate`,
`hook`, `conductor`, `fleet`, `derived`.

### Measurements shipped with phase 1

`internal/recall` holds the `recall.db` FTS5 DDL and a build-time test that
executes it against `modernc.org/sqlite`, asserting the facts the design
depends on: `contentless_delete=1` excludes `columnsize=0`; `msg_fts`
(`detail=none`) is membership-only (no phrase queries, no `rebuild`); the
three `card_fts` triggers match on insert, drop the stale posting on update
and support phrase queries with `snippet()`.

Two benchmarks turn the design's extrapolated numbers into measured ones:

```bash
go test ./internal/recall/ -run '^$' -bench . -benchtime 1x
RECALL_BENCH_CORPUS=~/.claude/projects go test ./internal/recall/ -run '^$' -bench ParseClaude -benchtime 1x
```

`BenchmarkParseClaudeJSONL` reports MB/s over a generated corpus of the same
record shape (or a real one via `RECALL_BENCH_CORPUS`);
`BenchmarkFTS5BulkInsert` times `msg` + `msg_fts` inserts in 8 MB
transactions through the pure-Go driver (`RECALL_BENCH_ROWS`, default
33,458); `BenchmarkCardFTSInsertViaTriggers` times the ranked surface.

## Phase 2: the index (Claude)

```toml
[recall]
enabled = true
```

Everything below needs that switch. Hints and `session annotate` never do.

### What is indexed

Every Claude config dir agent-deck can launch a session under (each
`[profiles.<name>.claude].config_dir`, the global `[claude].config_dir`,
`~/.claude`, `$CLAUDE_CONFIG_DIR`, conductor and group dirs) plus the
worker-scratch homes, whose `projects` symlink resolves onto one of those
dirs and is walked once. Under each `projects/` tree: `<slug>/<uuid>.jsonl`
and `<uuid>/subagents/agent-*.jsonl`; `tool-results/` and `workflows/` are
never opened. Sources are keyed on `(device, inode)` with the path as an
attribute, so a transcript reachable through 189 scratch symlinks is one
row, and each file carries the profile that owns its config dir.

From a transcript the reader keeps: user prompts and assistant text (each
message body zstd-compressed and clipped to 8 KiB, the FTS index over the
full text), every `tool_use` with its name, timestamp, the duration to its
`tool_result`, whether it errored and a 200-character argument digest,
the files those calls read, wrote or edited, token usage per assistant
record (summed on the session and, for a conversation bound to a deck
session, written as cost events in the same pass, so `costs sync` and the
index never read a transcript twice), interrupts (one per Escape press: the
`[Request interrupted by user]` marker; the interrupted tool result before
it is the same press), compactions, API errors,
the harness title (`custom-title`, `agent-name`, `ai-title`, `summary`),
cwd, branch and model. Tool results, thinking, attachments, snapshots,
progress and queue records are never stored. Unknown record kinds are
counted and skipped; a line over 4 MiB is skipped whole.

Message classes (`prompt`, `meta`, `assist`, `heartbeat`, `skill_load`,
`interrupt`, `compact_summary`) follow the taxonomy of
`skills/agent-deck/scripts/self-improvement/distill.py`; `turns` counts
prompts only.

### Commands

```bash
agent-deck recall backfill [--since 90d] [--budget 5m] [--force] [--json]
agent-deck recall sweep [--full] [--force] [--json]
agent-deck recall status [--json]
agent-deck recall sessions [--profile work] [--project PATH] [--since 30d] [--hint k=v] [--tag t] [--session ID] [--subagents] [--limit 20] [--json]
agent-deck recall search "<q>" [same filters] [--role user|assistant] [--phrase] [--phrase-scan-limit 2000] [--limit 20] [--no-sweep] [--json]
agent-deck recall show <session> [--tier card|excerpt|raw] [--turns 40] [--json]
agent-deck recall open <session> [--title T] [--dry-run] [--json]
agent-deck recall gc [--keep-days 30] [--json]
agent-deck recall rebuild [--force] [--json]
```

`<session>` is the number printed by the listing, a Claude conversation id
or a unique prefix of it, or an agent-deck session id.

**backfill** indexes everything, newest file first, and is resumable: every
source keeps a byte cursor and a signature of the 4 KiB before it, so
Ctrl-C keeps what was done and the next run continues. **sweep** does the
same for what changed since: it stats every candidate file (about 50 ms
for 7,000 files), compares size and mtime against the ledger, and parses
only appended tails. A torn trailing line waits for its newline. A file
rewritten in place at the same length is caught by the tail signature and
reparsed from zero. A copied transcript (fork, session-share import,
switch-account within one profile) keeps its conversation id and is
quarantined, never merged; the same file under another profile is its own
session. A vanished file has its messages dropped at once and a tombstone
written; the session and card stay, labelled missing. A file that comes
back under the same path (an unmounted volume, a permission hiccup, a
rename and back) is parsed again from byte 0 on the next sweep, whether or
not `gc` dropped its ledger row in between, and its tombstone goes. A
source whose pass failed with a read error keeps every row up to its last
checkpoint, and the usage of that prefix (handed to the cost store with
each checkpoint, never ahead of it), and is retried from there on the next
sweep; the retry folds only what the failed pass rolled back. `sweep --full` also
re-verifies every signature and re-projects every card.

`recall.db` is machine-global; `state.db` is per profile, and a deck
session of one profile may run under another account (its transcript then
lives under that account's config dir). A sweep therefore consults every
profile's `state.db` for links, hints and tags, the transcript's own
profile first: the profile whose `state.db` holds the authoritative link
owns the card's hints and receives the cost events, whichever profile ran
the sweep. Only the invoking profile's `state.db` is created or migrated;
the others are opened as they are and skipped when absent.

**search** ranks sessions, not messages: a hit in the title, hints or tags
(the card, a ranked phrase-capable FTS surface) always outranks any number
of body mentions; body hits then rank by count and recency. Terms are
AND-ed, `AND`/`OR`/`NOT` and a trailing `*` work, and identifiers such as
`SB-412` or `handle_sess` are single terms. The body index is a
membership filter; `--profile`, `--since`, `--project`, `--session` and
`--role` narrow it in the same SQL before the 5,000-message ceiling is
applied to the newest matches, so a filtered search on a common term sees
every matching session of that profile, and the output says when the
ceiling was hit. `--phrase` checks the literal phrase (the query's words,
without `AND`/`OR`/`NOT` or `title:` prefixes) in the ranked hits' own
matching bodies, hit by hit and newest message first, decompressing up to
`--phrase-scan-limit` bodies in all; a hit is `phrase verified` once one
body carries the phrase, `phrase NOT found` only after every matching body
was read whole without it, `phrase unverified (clipped body)` when a
matching body is stored clipped (`text_tier = "clipped"`, 8 KiB) and the
phrase is not in the stored part (it may sit past the clip; the body index
cannot say and the source file is not reopened at query time, so the
answer is unknown, not absent; `text_tier = "full"` removes the case), and
`phrase unverified (scan limit)` when the limit ran out before it was
reached. The output prints how many sessions it verified over how many
bodies.
`--hint` and `--tag` join the active profile's `state.db` live, so an
annotation typed a second ago filters immediately. Before every
search a bounded sweep runs: 150 ms and 32 MB, after which the search
proceeds on the index as it is and the output says what was deferred.

**show** prints the card, a tool summary (calls, errors, total duration per
tool), the touched files and the decoded messages. **open** relaunches: a
conversation still bound to an agent-deck session starts that session,
under the profile whose `state.db` holds the link (`-p <profile> session
start <id>` when that is not the invoking profile); a transcript with no
record is re-registered with `add --resume-session` in its original
directory (and its account), so old history becomes a live session again.
`--dry-run` prints the plan.

**status** reports sources by state (ok, partial, error, missing,
quarantined), bytes indexed and pending, the index size as a share of its
input, and how many indexed bytes Claude's own retention
(`cleanupPeriodDays`, read per config dir) will delete within 30 days:
those survive in the index. **gc** drops old tombstones and hands freed
pages back; **rebuild** deletes `recall.db` and backfills.

### Load guarantees (all tested)

- No daemon or file watcher *inside `internal/recall`*: a sweep runs inside
  the caller's goroutine and ends with it (`TestRecall_NoDaemonNoWatcher`
  forbids a goroutine launch or a watcher import anywhere under
  `internal/recall` and `cmd/agent-deck/recall_cmd.go`). The one caller
  that keeps its own goroutine alive across many sweeps is
  `backfill_on_enable`'s initial-backfill pass, and it lives in
  `internal/session` (the existing notify-daemon), not in `internal/recall`
  itself — the package that owns `recall.db` still makes no daemon of its
  own.
- The interactive sweep before a read stops at 150 ms or 32 MB and reports
  what it deferred; the index stays consistent at every cut.
- `backfill`, `sweep` and `rebuild` refuse to start while any session of
  the active profile is `running` or the one-minute load average is above
  `max_loadavg` (default 4.0); `--force` overrides. Reads are never gated.
  The daemon-driven initial backfill (`backfill_on_enable`, below) is the
  one exception to "refuse": it throttles instead, in small ungated
  chunks, so it still finishes on a machine that would refuse the manual
  command.
- One writer connection, transactions bounded at 8 MB of decoded text, one
  record resident at a time: a 100 MB transcript adds about 11 MB of heap.
- One `flock` beside `recall.db`, so every profile contends on one lock.
- An unchanged tree costs one `readdir` per directory and one `lstat` per
  file, never an `open`; symlinked roots cost one resolution each.
- The index is about 3.8% of its input and the first backfill pass peaks
  at about 93 MB resident (later passes 48 to 76 MB), measured on the real
  corpus (3.4 GB of Claude transcripts, 1,809 files). The design projected
  about 2% and 60 to 90 MB; the difference is the per-call `tool_call`
  table (0.9 points, kept: `show` prints the tool timeline from it) and
  per-message zstd without a shared dictionary (2.1x on real chat text
  where the projection assumed 3.5x). These measured numbers are the
  acceptance bar; a trained zstd dictionary is the follow-up that would
  recover most of the gap.
- A read of `recall.db` written by another schema version never deletes it
  outside the sweep lock: the file is recreated under the lock, so a
  running backfill is never pulled out from under.

`ValidateRecallTranscriptPath` (internal/session/recall_roots.go) has no
caller yet: it is the phase 3 hook containment check, landed with the
roots it validates against. Progress records are no longer read by `costs
sync`; no transcript on the design machine carries one today, and the
recall reader takes usage from assistant records only.

### Initial backfill

Enabling `[recall] enabled = true` used to index nothing until someone ran
`agent-deck recall backfill` by hand, and that command refuses outright
under the load gate — a busy machine never gets a window. `[recall]
backfill_on_enable = true` (default) closes that gap: `agent-deck
notify-daemon`, the always-on daemon/timer path every machine (including
remotes) already runs, checks on every poll tick whether recall is enabled
with an empty index or a persisted marker saying the initial backfill never
finished, and if so starts it, once per daemon process. It never runs from
the TUI's render loop and never from the Stop/SessionEnd hook.

The background pass is a different load-gate policy from the manual
command: it **throttles instead of refusing**. Each chunk is small (a
couple of seconds, a few megabytes) and ungated; between chunks it sleeps,
scaled by the one-minute load average (minimally at or under half of
`max_loadavg`, up to several seconds at or above it), so a heavy machine
still finishes, just slower, instead of never starting. On Linux the pass's
own goroutine runs at a lower scheduling priority (`setpriority`); there is
no equivalent on other platforms, so this is a Linux-only refinement, not a
correctness requirement. Exactly one chunk runs anywhere on the machine at
a time: each chunk takes the same machine-global sweep lock the manual
commands use, without waiting, and releases it before sleeping, so an
interactive search's pre-search sweep or a hook's inline sweep is never
starved for the whole pass — only for one chunk at a time. The manual
`recall backfill`/`sweep`/`rebuild` are unchanged: they still refuse
outright under load or a busy session, and `--force` still overrides them.

Progress and completion are in `recall status --json`'s `initial_backfill`:
`state` (`pending`, `running`, `done`), `done_at`, `sessions_done`,
`sessions_pending`, checkpointed after every chunk. One log line marks the
start (`recall_initial_backfill_started`) and one the end — success
(`recall_initial_backfill_done`) or an interruption that leaves the marker
"running" for the next daemon to resume (`recall_initial_backfill_interrupted`).
The marker lives in `recall.db`'s `meta` table, so it resets with the
disposable index (a `recall rebuild` or a schema bump both warrant a fresh
catch-up) and survives a process restart: a daemon killed mid-pass leaves
"running", and the next one treats that exactly like "pending" — nothing
already committed is re-parsed, since the ledger's own per-source byte
cursors (not the marker) decide where each file resumes. An index that
already holds sessions when this feature first runs, with no marker (built
by an older binary, or by a plain `recall backfill`), is treated as
already caught up: no background pass fires for it.

The daemon's pass indexes text and builds cards exactly like the manual
`recall backfill`, but writes no cost events (no `UsageSink`, unlike the
CLI's own `recall backfill`/`sweep`): the daemon has no per-profile cost
store wiring of its own to reuse safely across every profile a machine-wide
pass may touch. A backfilled session's token counts land in the index and
`recall show`; its usage history reaches `costs sync` from a later `recall
sweep`/`backfill` run by hand, or the next time that session's own hook
fires.

**Remotes:** nothing remote-specific was needed. `notify-daemon` is the
same binary and the same poll loop on every machine, so a remote enabling
recall (`agent-deck remote <host> ...`, or the remote's own config edit)
gets the same background catch-up from its own daemon; a fleet on
auto-update indexes itself without an operator visiting each host by hand.

### Config

```toml
[recall]
enabled = true
max_loadavg = 4.0        # backfill/sweep refuse above this (0 disables); also scales backfill_on_enable's sleep
text_tier = "clipped"    # or "full": whole bodies instead of 8 KiB
keep_missing_days = 30   # tombstone retention for vanished transcripts
per_source_mb = 64       # per-sweep cap on one file; the rest continues next sweep
backfill_on_enable = true  # the daemon runs one throttled background pass to catch an empty/unfinished index up
```

### Where the files are

`recall.db`, `recall/sweep.lock` and `recall/queue.jsonl` live in the
agent-deck data dir beside `profiles/`, resolved through
`internal/agentpaths` and included in `migrate-paths`.

### Harness links

`add --resume-session <uuid>` now writes an authoritative link at creation
(an operator-named conversation is an explicit ownership declaration), and
the first hook that confirms a minted id writes one too. The index binds
`session.deck_id` only from such links.

### What did not survive contact with the code

- `spanv` (byte spans of text blocks inside a record) is NULL: bodies are
  stored, so nothing reads spans, and `text_tier = "none"` is not offered.
- `card_fts` hint columns are the ranking feed and refresh on the next
  sweep for sessions whose `state.db` rows changed; the `--hint`/`--tag`
  filters do not wait for that.

### Known limits

- **Copy ownership is decided at first sight and frozen.** When two files
  carry the same conversation id under one profile (a fork, a
  session-share import, a switch-account, a conversation continued under
  a second working directory), the first one indexed owns the session and
  the other is quarantined (`recall status` counts it, `recall show`
  labels it). A backfill walks newest first, so a copy that is newer than
  the original at backfill time becomes the owner, and turns appended to
  the original afterwards are never indexed: the sweep sees the original
  grow, re-checks it, and keeps it quarantined. Exposure on the design
  machine: 2 of 1,823 files, both the same conversation continued under a
  second directory; the data at risk is only what is appended to the
  quarantined file after that point. Filed as a follow-up rather than
  fixed here because the two candidate fixes trade off: re-evaluating
  ownership when a quarantined file grows ping-pongs (a reparse each
  time) when both files keep growing, and keying sessions on conversation
  id plus prefix signature is a schema change. Acceptance for the
  follow-up: a file quarantined on a backfill that later grows is
  indexed; both orders tested; no reparse loop when both grow.

## Phase 3: triggers, every harness, the TUI

Still behind `[recall] enabled = true`. Phase 3 adds the other five
harnesses to the same index, the triggers that keep it fresh without a
daemon, and the `G` key in the TUI.

### Readers

One reader per harness (`internal/recall/reader`, one file each, one line
in the registry), each declaring how its sources resume:

| Harness | Source | Cursor | How it resumes | Trigger |
|---|---|---|---|---|
| Claude | `<cfgdir>/projects/**.jsonl` | bytes | append-only; `tail_sig` guards the cursor | Stop / SessionEnd hook, `session stop`, daemon turn end, sweep |
| Codex | `<home>/sessions/YYYY/MM/DD/rollout-*.jsonl`, `archived_sessions/` | bytes | as Claude, and a fresh file (written in the last 10 min) stops at `min(next_rollout_byte_offset, size)` from Codex's own `thread_history_1.sqlite` (opened `mode=ro`), so a turn Codex has not committed is not indexed half way; an older or unprojected file is tailed to its size | daemon turn end, sweep |
| pi | `<home>/agent/sessions/<cwd>/*.jsonl`, `<home>/agent-deck/<instance>/*.jsonl` | bytes | as Claude | daemon turn end, sweep |
| Gemini | `<home>/tmp/<hash>/chats/session-*.json` | none | the document is rewritten every turn: any change is a full reparse, streamed with a bounded structural scanner (one message resident; a 31.6 MB file costs ~5 MB of heap) | sweep |
| OpenCode | `<data>/opencode/storage/{session,message,part}/**.json` | none | one source per session, sized and dated over its message and part files; any change reparses that session; `snapshot/` is never entered | sweep |
| Hermes | `<home>/state.db` (`mode=ro`) | opaque | one source per Hermes session addressed `state.db#<id>`; the cursor is the last mirrored message id | sweep |

What each keeps: prompts and assistant text (Codex `input_text` /
`output_text`, pi text blocks, Gemini `content` strings or part lists,
OpenCode text parts, Hermes `content`), tool calls with durations and
error flags where the harness records them (Codex `function_call` /
`custom_tool_call` / `local_shell_call` and their outputs, pi `toolCall` /
`toolResult`, Gemini `toolCalls[]`, OpenCode tool parts, Hermes
`tool_calls` / `tool` rows), token usage with a stable id per record so a
reparse never double counts cost events, titles (Codex `session_index.jsonl`
`thread_name`, best effort; pi `session_info`; Gemini `summary`; OpenCode
and Hermes `title`), compactions and interrupts. Codex developer messages,
reasoning blocks, pi thinking and Gemini thoughts are never stored.

Codex `compacted` records: the record supersedes everything before it in
the session (`msg.superseded = 1`, still searchable) and a
`compacted_into` self-edge counts the compactions. On real rollouts the
record's `message` is always empty (the summary itself is the encrypted
compaction item), so no row is stored for it; when a rollout does carry
summary text it is indexed once as a `compact_summary` message.
`replacement_history` is never re-emitted (it repeats records already
indexed from their own lines), and a rollout holding nothing but its
header and compactions (Codex writes one when a thread resumes after
compaction) gets no session row. An index written before this rule is
cleaned once, on its next sweep. A pass that stops at Codex's own
projection cursor below the file size records the size it parsed to, so
the tail past the cursor is picked up by the next sweep once Codex commits
it, even when the rollout is never written again. pi `parentSession`,
OpenCode `parentID` and Hermes `parent_session_id` become `fork_of`
edges.

Gemini records only a hash of the working directory, so its sessions have
no `cwd` and `--project` cannot match them.

### Triggers

Nothing watches anything. Three things move the index once it exists; a
fourth, distinct one gets it going in the first place:

1. **The Claude hook.** On `Stop` and `SessionEnd`, `agent-deck hook-handler`
   appends one line to `recall/queue.jsonl` (`{ts, harness, path, event,
   instance}`) after the recall containment check. `Stop` is installed
   synchronous and sits on Claude's turn-end latency, so that is all it
   does: no database open, no lock, no sweep. `SessionEnd` is
   asynchronous; there, when `hook_sweep = true` (the default) and the
   sweep lock is free, the hook also indexes exactly that file within the
   interactive budget (150 ms / 32 MB), with the profile's hints and deck
   id on the card. Everything is behind `recover()`; a hook never fails
   or blocks Claude on recall work. The
   containment check accepts a path under any Claude config dir agent-deck
   can launch under, including account slots and the worker-scratch homes
   whose `projects` symlink into one, and refuses spoofed siblings and
   symlinks escaping every root. The same hook's cost event now comes from
   the turn's main-chain assistant record found by walking back over the
   transcript tail, not from the literal last line, which Claude Code
   often follows with `system`, `attachment` or sidechain records; that
   silent drop is gone.
2. **Session events.** `session stop`, a task-worker completion
   (`worker_done`) and the transition daemon's running-to-not-running edge
   queue the session's transcript (Claude, Codex or pi; remote sessions
   never resolve a local file and queue nothing). The daemon hands its
   notifies to a bounded background worker that resolves the recall roots
   once per batch, so the root walk never runs on the goroutine every
   profile's status detection depends on.
3. **The sweep.** Every sweep drains the queue first and parses the files
   it names before the rest of the walk, so a transcript a hook reported
   is fresh even when the bounded interactive budget would not reach it.
   The queue is advisory: a path is re-validated before it is opened, and
   the walk finds the same files without it.
4. **The initial backfill** (`backfill_on_enable`, phase 2 above) is not a
   freshness trigger like the three above: it fires once, when recall's
   index has nothing in it yet (or a marker says an earlier attempt never
   finished), and stops firing once it has caught up.

### The TUI

`G` opens Recall search over the index, in place of the in-memory global
search that was disabled for opening one watcher per project directory
and loading 4.4 GB into memory; `/` stays the quick local title filter
(Tab switches between the two). Typing runs `recall search` (debounced);
the right pane is `recall show #n` for the selected hit (a failed preview
says so in the pane); Enter jumps to the registered session that owns the
conversation (bound deck id or Claude session id) or, for an unowned
Claude conversation, registers a session that resumes it, exactly as
`recall open` does. Codex, pi, Gemini, OpenCode and Hermes conversations
are searchable and previewable but not resumable from the TUI or
`recall open` yet; the footer names the `recall show` command.

Nothing parses on a keypress. Opening the overlay runs one ungated
bounded sweep pass in a background command and shows a staleness line
("index behind by N source(s) / M MB; catching up in the background",
then "index swept 3m ago"); while anything was deferred the overlay keeps
running bounded passes, one second apart and through the busy/load gate,
so the catch-up never holds a core or competes with a running agent. The
overlay is the terminal width less a margin, capped at 160 columns, and
fits 80. The catch-up tick is routed by the TUI's message loop whatever
overlay is on top, and closing the overlay ends the chain. With
`[recall] enabled = false`, `G` falls back to the local title search and
a notice inside that overlay says so for as long as it is open ("Recall
is off ([recall] enabled = false in config.toml); showing the local title
search instead"); when the index failed to open, the notice quotes the
error instead ("Recall index unavailable: ..."). Frames at 200, 140, 120
and 80 columns: `internal/ui/testdata/recall_search_*.golden`,
`recall_catchup_home_120.golden`, `local_search_recall_off_*.golden`.

### Config

```toml
[recall]
harnesses = ["claude", "codex", "pi", "gemini", "opencode", "hermes"]  # default: all
hook_sweep = true        # the async SessionEnd hook indexes its own transcript inline (Stop only queues)
```

### What did not survive contact with the code

- Hermes is a reader that mirrors its rows (one source per Hermes session,
  cursor = last message id), not the federated body search into Hermes's
  own `messages_fts` the design proposed: that would have been a second
  query path (search, show, phrase verification, snippets) for a 225 KB
  store. Index bytes for Hermes are proportional to that store.
- The `compacted_into` edge is a self-edge: a compacted Codex thread keeps
  its id and its rollout file, so there is no second session to point at.
- A Gemini message is read field by field, each field under 4 MiB, so a
  short prompt survives a 10 MB `displayContent` of inline video beside
  it (the shape of the largest real file); only a field itself above the
  cap is dropped and counted, like an over-long JSONL line. That is what
  keeps the resident set bounded.
- The SessionEnd hook's inline sweep is ungated, like the CLI's pre-search
  sweep: it parses at most one file's tail within 150 ms / 32 MB. Only
  the background continuation in the TUI goes through the busy/load gate.
- A source pass that fails or is quarantined is counted in the sweep
  result (`errors`, `quarantined`) and leaves one line in the log
  (`recall_source_failed` / `recall_source_quarantined`).
- `recall open` and the TUI resume Claude conversations only; other
  harnesses need a registered session to jump to.

## Phase 4: analysis, remote, handoff, MCP

Still behind `[recall] enabled = true`. Phase 4 adds the cheap analysis
layer, the remote surface (federated by default, card sync opt-in), the
context handoff into any session, and an MCP server over the same index.

### Analysis: rules.json, the queue, the artifacts

One taxonomy, one file. `internal/recall/classify/rules.json` holds the
noise record types, the skill-load marker, the heartbeat prefixes, the
interrupt marker, the meta prefixes and the clip lengths that
`distill.py` used to carry as constants, plus the thresholds of the three
phase-4 classifiers. The Go side embeds it; `distill.py` reads the copy
beside itself (`skills/agent-deck/scripts/self-improvement/rules.json`);
`TestRulesJSONSharedWithDistill` fails the build when the two differ, so
the Go and Python paths cannot drift.

`enrich_queue` is filled from one place: every card re-projection (a
content change bumped `derived_rev`, or hints changed in `state.db`)
queues the three cheap kinds for that session. Every sweep drains what
it queued within what is left of its budget, so a backfill leaves every
session classified and an interactive 150 ms pass never overruns on
classification; `agent-deck recall enrich` drains the rest (same load
gate, exit 3 while a session is busy; `--kind`, `--limit`, `--budget`,
`--retry-failed`). There are no worker goroutines: a drain is a bounded
pass in the caller's goroutine, like the sweep, which is what the
no-daemon test enforces.

The classifiers run on SQL rows only, never by re-reading a transcript:

| kind | reads | says |
|---|---|---|
| `lost_time` | `tool_call` (name, error, duration), `session.interrupts`, `session.compacts` | tools that errored often (`error_share_min`, `min_errors`) or looped on retries (`retry_window`), calls over `slow_call_ms`, interrupts, compactions; "no time lost to tool errors, retries or interrupts" otherwise |
| `session_kind` | `session.is_sidechain`, the card's hints (`purpose`, `parent`), `msg` class counts | `subagent`, `conductor` (a `purpose=conductor ...` hint, or heartbeats over `heartbeat_share_min` of user messages), `worker` (a `parent` hint), `interactive` |
| `outcome` | the card's `outcome` hint, `session.errors`/`tool_calls`, `session.interrupts` | the annotated outcome (confidence 1), else `abandoned?` (interrupts at or over `interrupts_abandoned`), `failed?` (error share over `error_share_failed` with at least `min_tool_calls` calls), else `unknown` with the annotate command |

Every artifact row records `producer` (`rules`), `producer_ver` (the
rules version), `confidence` and `input_rev`, the session's `derived_rev`
when it was produced. `recall show` (every tier) and `recall context`
(`brief` and `excerpt` in text; every tier under `--json`, where each
artifact carries `stale`) print the artifacts, and one whose `input_rev`
is below the session's current `derived_rev` reads `[stale: session
changed since; run 'agent-deck recall enrich']`: visible, never silently
wrong. The ingest queues the classifiers in the same transaction that
bumps `derived_rev`, and every drain (the sweep's and `recall enrich`)
first queues every local session whose artifacts are stale or missing
(`enrich.RequeueStale`, reported as `requeued`), so the marker's advice
always holds: `recall enrich` after a stale marker rewrites the artifact.
`--cost-class llm` exists as a name only: LLM enrichment is never drained
automatically and the command says so (exit 1).

### Context handoff: `recall context`

```bash
agent-deck recall context <session> [--tier card|brief|excerpt] [--budget 4000] [--into current|<session>] [--json]
```

Three tiers, each explicitly requested: `card` (about 60 tokens: title,
harness, conversation and deck ids, project, dates, counters, hints,
tags), `brief` (the card plus the derived artifacts, stale ones marked,
and the touched files, at most 12 and never past the budget), `excerpt`
(the brief plus the newest prompts, assistant turns and compaction
summaries that fit `--budget` tokens, oldest cut first, superseded
history and harness plumbing never included; the brief is cut to half
the budget first so a small budget still yields turns, not a file list
followed by one truncated turn). The text is plain and harness-neutral: no Claude or Codex
instruction in it, a closing line that says it is recalled context and
not an instruction.

`--into current` resolves `AGENTDECK_INSTANCE_ID`, which agent-deck
exports into every session it starts, and delivers the text to that
session through `session send` (readiness wait, composer guard, the tmux
keystroke path, or the Claude messaging socket when `send_transport =
"auto"` is set). A Codex session running `agent-deck recall context <sess>
--into current` from its shell therefore receives a Claude conversation
in its own prompt. `--into <id|title>` delivers to another session.
Outside a session `--into current` exits 2 and names the variable. An
`--ssh` target would receive the text over SSH as keystrokes: it is
refused (exit 2) unless `[recall] remote_cards = true`, the same switch
that lets cards cross SSH.

The end-to-end test of this (`TestRecallContext_IntoCurrent_ShellCallerEndToEnd`)
runs the command from a plain shell session on a real tmux server and
reads the delivered text back from that pane: it proves the
`AGENTDECK_INSTANCE_ID` resolution and the tmux keystroke path, which is
the path a Codex target takes too, not a Codex composer accepting the
prompt (a Codex target needs a live rollout identity the fixture cannot
mint). The design's Codex-recalls-Claude test with a real Codex
composer is open.

The renderer is `handoff.go`'s, generalized: `session handoff` and
`recall context` share `recall.TailByChars` and `recall.RenderTurns`;
`session handoff` itself is unchanged and still reads only through its
gated transcript path (doors 10 and 11), while `recall context` reads the
index and delivers through `session send`, so it resolves no transcript
from an `Instance` and adds no door. A card pulled from another machine
stops at `brief` (exit 2 for `excerpt`, with the `remote <host> recall
show` command to run).

### Remote

Three modes, in the order the design ranks them:

1. **Federated query, the default, nothing stored.** `recall search
   --remote <host>` (repeatable) or `--all-remotes` runs `agent-deck
   recall search <q> <filters> --json` on each configured remote over SSH
   (one round trip each, 1 to 2 s dominated by the handshake), prints its
   hits under the remote's name labelled `remote <host>`, and returns
   them in `remotes[]` under `--json`. Remotes rank on their own index;
   hits are not re-ranked here. `agent-deck remote <host> recall
   search|sessions|show|context|export|status ...` forwards the same
   read-only verbs directly; the option set per verb is closed
   (`remoteRecallOptions`), so `--into`, `--remote`, `--all-remotes`, any
   write verb and any unknown option are refused before SSH, and the
   booleans are known to the forwarder so none is ever mis-shifted as
   value-taking.

   Older remotes degrade as `session annotate` did in phase 1: a remote
   whose agent-deck predates recall (v1.16.12 and older), runs v1.16.13
   with `[recall] enabled = false`, or runs v1.16.13 without a phase-4
   verb it was asked for (`pull`, remote `context`, remote `export`, all
   answering `unknown recall command: <verb>`) is reported in one line,
   `remote "lab" runs v1.16.12 that predates recall; update it with
   'agent-deck remote update lab'` (or `... that has [recall] enabled =
   false; set [recall] enabled = true in its config.toml`, or `... that
   predates recall context; update it with ...`), the command exits 1,
   and under `--json` the remote's entry (or the whole output of the
   forwarded form) is `{"error", "remote", "remote_version"}`. The one
   classifier (`remoteRecallUnsupported`) covers all three shapes for
   every surface (forwarded, federated search, `pull`), so the remote's
   own usage or JSON text is never forwarded.
2. **Card sync, opt-in, derived only.** Off by default: `[recall]
   remote_cards = true` on both ends turns it on. `recall export --cards`
   writes NDJSON: a header with this machine's `host_uid` (32 hex
   characters minted once and kept in `recall.db`'s `meta`), then
   `session`, `card`, `artifact` and `edge` rows, then a trailer. It
   never emits a message body, a byte offset, a span or a filesystem
   path; the only conversation text is the card's 200-character preview,
   and `TestExport_NeverEmitsOffsetsSpansBodiesOrPaths` asserts all of
   it. `recall pull <host>` runs that export on the remote from the last
   pull's cursor and imports it here; `recall import --host <alias>
   [file|-]` imports a saved stream. An import is refused when no
   `--host` is given, when the stream carries no `host_uid`, when the
   `host_uid` disagrees with the one recorded for that alias (a renamed
   or repointed remote), when the same `host_uid` is already imported
   under another alias (it would double every card), and when the stream
   is this machine's own. Imported rows carry the remote's `host_uid`,
   `digest_only = 1`, `is_local = 0` on the host row (no default, fails
   closed), and every listing labels them `card from <host_uid> (no
   messages here)`; `recall show` has no messages for them and `recall
   context` stops at `brief`. The incremental pull's cursor is the last
   `exported_at`; an export includes every session active since and
   every session whose artifacts were rewritten since, so a hint change
   on an old session (its re-drain rewrites the artifacts) reaches the
   puller without `--full`.
3. **Raw fetch** (`recall fetch <host> <session> --raw --yes`) is not
   built: nothing in phase 4 moves transcript bytes across SSH.

`remote_transcript_boundary.go` now lists recall's doors (17
`RecallNotifyInstance`, 18 the daemon hand-off worker) and
`TestRemoteTranscriptBoundary_EveryEntryPointRefuses` walks every door
as one table: the remote instance refuses, the local one still resolves.
Door 15 (`sessionhost.BuildRequest`) stays: it exists, resolves through
door 4 and then directly against per-instance config dirs, and is now
gated itself (its own package's test plants a local transcript at the
placeholder path and sees it refused).

### MCP

`agent-deck recall mcp` is a Model Context Protocol server over stdio
(newline-delimited JSON-RPC 2.0; `initialize`, `ping`, `tools/list`,
`tools/call`) exposing `recall_search`, `recall_show` and
`recall_context` with the same arguments as the CLI. A tool failure (no
such session, index off) comes back as a tool result with `isError`, so
the model can read it; a bad argument is a JSON-RPC invalid-params
error. It reads the same index the CLI does, runs the same bounded sweep
before a search, and exits when stdin closes. While `[recall] enabled =
true`, `agent-deck mcp list` shows it as the built-in `recall` entry
(this binary, `recall mcp`), so `agent-deck mcp attach <session> recall`
followed by `session restart` attaches it per session like any other
MCP; a user-defined `[mcps.recall]` wins over the built-in. For a harness
without MCP, the `--json` CLI stays the fallback.

### Config

```toml
[recall]
remote_cards = false     # let cards (never bodies or paths) cross SSH: export / pull / import
```

### What did not survive contact with the code

- "Bounded workers" for the queue are bounded passes, not goroutines:
  the load-guarantee test forbids a goroutine launch anywhere under
  `internal/recall`, and a drain that runs in the sweep's goroutine and
  ends with it is the same guarantee the sweep gives.
- `host_uid` is minted by recall itself (`meta.host_uid`) and carried in
  the export header, not fetched through the forwarded `status` call:
  agent-deck has no machine id of its own (the telemetry install id is
  opt-in and rotatable), and the header costs no extra round trip.
- Door 15 exists (`internal/ctxinspect/sessionhost.BuildRequest`
  resolves through `GetJSONLPathChecked`, door 4, and then directly);
  it is kept in the list and gated instead of being removed.
- `recall fetch` (raw transcript bytes over SSH into a quarantined
  directory) is not built.
- The excerpt tier reads at most the newest 400 conversational rows
  before the character budget trims them, so a 100 MB conductor
  transcript is never decompressed whole for a 4,000-token excerpt.
