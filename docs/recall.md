# Recall: cross-harness conversation store

Recall is agent-deck's memory of what every managed session was for and what
happened in it, across harnesses (Claude, Codex, Gemini, Pi, OpenCode,
Hermes). It has two halves with different durability:

- **state.db** (per profile, existing) holds what a human or a binding wrote:
  hints, tags and harness links. This survives any rebuild.
- **recall.db** (machine-global, coming in phase 2) holds what is
  reproducible from transcripts: messages, an FTS5 index, cards, derived
  summaries. Deleting it costs a rebuild and nothing else.

Phase 1 ships the first half only. There is no ingestion, no reader and no
search yet; `session search` is unchanged and `recall search` is a future
command.

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

## Later phases

Phase 2: `recall.db`, the Claude reader, backfill. Phase 3: hook and event
triggers, the other harness readers, the TUI `G` key. Phase 4: analysis
queue, remote federation, `recall context --into current`, MCP.
