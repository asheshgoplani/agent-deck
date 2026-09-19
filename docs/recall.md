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

- No daemon, no file watcher. A sweep runs inside the command that asked
  for it and ends with it.
- The interactive sweep before a read stops at 150 ms or 32 MB and reports
  what it deferred; the index stays consistent at every cut.
- `backfill`, `sweep` and `rebuild` refuse to start while any session of
  the active profile is `running` or the one-minute load average is above
  `max_loadavg` (default 4.0); `--force` overrides. Reads are never gated.
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

### Config

```toml
[recall]
enabled = true
max_loadavg = 4.0        # backfill/sweep refuse above this (0 disables)
text_tier = "clipped"    # or "full": whole bodies instead of 8 KiB
keep_missing_days = 30   # tombstone retention for vanished transcripts
per_source_mb = 64       # per-sweep cap on one file; the rest continues next sweep
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

Codex `compacted` records: the summary is indexed once as a
`compact_summary` message that supersedes everything before it in the
session (`msg.superseded = 1`, still searchable, labelled in `show`), a
`compacted_into` self-edge counts the compactions, and
`replacement_history` is never re-emitted (it repeats records already
indexed from their own lines). pi `parentSession`, OpenCode `parentID` and
Hermes `parent_session_id` become `fork_of` edges.

Gemini records only a hash of the working directory, so its sessions have
no `cwd` and `--project` cannot match them.

### Triggers

Nothing watches anything. Three things move the index:

1. **The Claude hook.** On `Stop` and `SessionEnd`, `agent-deck hook-handler`
   appends one line to `recall/queue.jsonl` (`{ts, harness, path, event,
   instance}`) after the recall containment check, then, when
   `hook_sweep = true` (the default) and the sweep lock is free, indexes
   exactly that file within the interactive budget (150 ms / 32 MB), with
   the profile's hints and deck id on the card. Everything is behind
   `recover()`; a hook never fails or blocks Claude on recall work. The
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
   never resolve a local file and queue nothing).
3. **The sweep.** Every sweep drains the queue first and parses the files
   it names before the rest of the walk, so a transcript a hook reported
   is fresh even when the bounded interactive budget would not reach it.
   The queue is advisory: a path is re-validated before it is opened, and
   the walk finds the same files without it.

### The TUI

`G` (and `/` when recall is on) opens Recall search over the index, in
place of the in-memory global search that was disabled for opening one
watcher per project directory and loading 4.4 GB into memory. Typing runs
`recall search` (debounced); the right pane is `recall show` for the
selected hit; Enter jumps to the registered session that owns the
conversation (bound deck id or Claude session id) or, for an unowned
Claude conversation, registers a session that resumes it, exactly as
`recall open` does. Codex, pi, Gemini, OpenCode and Hermes conversations
are searchable and previewable but not resumable from the TUI or
`recall open` yet; the footer names the `recall show` command.

Nothing parses on a keypress. Opening the overlay runs one ungated
bounded sweep pass in a background command and shows a staleness line
("index behind by N source(s) / M MB; catching up in the background",
then "index fresh as of ..."); while anything was deferred the overlay
keeps running bounded passes through the busy/load gate, so the catch-up
never competes with a running agent. With `[recall] enabled = false` the
key falls back to the local title search as before. Frames:
`internal/ui/testdata/recall_search_*.golden`.

### Config

```toml
[recall]
harnesses = ["claude", "codex", "pi", "gemini", "opencode", "hermes"]  # default: all
hook_sweep = true        # Stop/SessionEnd hooks index their own transcript inline
```

### What did not survive contact with the code

- Hermes is a reader that mirrors its rows (one source per Hermes session,
  cursor = last message id), not the federated body search into Hermes's
  own `messages_fts` the design proposed: that would have been a second
  query path (search, show, phrase verification, snippets) for a 225 KB
  store. Index bytes for Hermes are proportional to that store.
- The `compacted_into` edge is a self-edge: a compacted Codex thread keeps
  its id and its rollout file, so there is no second session to point at.
- A Gemini message above 4 MiB (the largest real one is 10.5 MB of
  video-analysis content) is skipped and counted, like an over-long JSONL
  line; that is what keeps the resident set bounded.
- The hook's inline sweep is ungated, like the CLI's pre-search sweep: it
  parses at most one file's tail within 150 ms / 32 MB. Only the
  background continuation in the TUI goes through the busy/load gate.
- `recall open` and the TUI resume Claude conversations only; other
  harnesses need a registered session to jump to.

## Later phases

Phase 4: analysis queue, remote federation, `recall context --into
current`, MCP.
