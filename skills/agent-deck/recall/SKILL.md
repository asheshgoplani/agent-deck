---
name: agent-deck-recall
description: Record and later find what an agent-deck session was for, across harnesses. Use when the user says "remember what this session was for", "tag this session", "annotate", "mark the outcome", "ticket for this session", "find the session where we...", "what did we decide about", "which Codex/pi/Gemini session did X", or wants to search past conversations. Available (phases 1 to 3): hints and tags (add/launch --hint, session annotate, remote annotate) and the transcript index over Claude, Codex, pi, Gemini, OpenCode and Hermes (recall search/sessions/show/open/backfill/sweep/status/gc/rebuild, the TUI G key, hook-driven freshness; behind [recall] enabled = true). Phase 4, not yet available: recall context --into current, remote federation, enrich, MCP.
metadata:
  compatibility: "claude, codex, pi, gemini, opencode, hermes"
---

# Recall

Recall is agent-deck's memory of what every session was for and what
happened in it. Human intent (hints, tags) lives in the profile's state.db
and survives everything; the transcript index (recall.db) is disposable:
delete it, `recall rebuild`, nothing typed is lost. Full design:
`docs/recall.md` in the repo.

## Phase 1 (available now): hints and tags

Hints are single-valued per key (setting a key again replaces it). Tags are a
set. Well-known keys: `purpose`, `ticket`, `why`, `decision`, `outcome`,
`note`, `parent`; any identifier-shaped key works.

### At creation (`add` and `launch`)

```bash
agent-deck add . -c claude --hint purpose="fix flaky auth test" --ticket SB-412 --tag auth --tag flaky --why "3rd regression"
agent-deck launch . -c claude --ticket SB-412 -m "Fix the flaky auth test"
```

- `launch` derives `purpose` from the first line of `-m` and both commands
  derive `parent=<parent id>` for a child; an explicit `--hint purpose=` wins.
- `conductor setup <name>` records `purpose=conductor <name>`.
- `--json` echoes `hints` and `tags`.

### Afterwards (`session annotate`)

```bash
agent-deck session annotate <id> --decision "root cause was clock skew" --outcome worked --tag clock-skew
agent-deck session annotate <id> --set-hint ticket=SB-413 --remove-tag flaky --unset why
agent-deck session annotate --self --outcome worked            # your own session (AGENTDECK_INSTANCE_ID)
agent-deck session annotate --self --note-stdin < summary.md   # free-text note, 8 KiB cap
agent-deck session annotate <id> --json                        # read: hints, tags, harness links
```

**End of task habit:** before the completion sentinel, run
`agent-deck session annotate --self --outcome <worked|failed|partial> --decision "<one line>"`
so the next agent can find what you concluded.

### Remote sessions

```bash
agent-deck remote <host> session annotate <id> --outcome worked
```

Runs on the remote and writes the remote's own state.db. Remote hints are
never copied locally; read them through the same forwarded command.

### Config

```toml
[recall]
enabled = false   # reserved for the transcript index; hints work regardless
```

## Phases 2 and 3 (available now): the transcript index, every harness

Needs `[recall] enabled = true` in config.toml (every command exits 2 with a
clear message otherwise). One index for every profile on the machine and
every harness: Claude (all profiles), Codex, pi, Gemini, OpenCode, Hermes.
`--profile` narrows Claude to one account, `--harness` to one harness.
Nothing runs in the background: a sweep runs inside the command and ends
with it; the Claude Stop hook, `session stop`, `worker_done` and the
daemon's turn-end edge queue the transcript that just moved, and the Stop
hook indexes its own file within 150 ms, so a conversation is usually
searchable the moment its turn ends.

### Find sessions

```bash
agent-deck recall backfill                     # once; resumable; refuses while a session is busy (--force)
agent-deck recall search "clock skew" --since 30d --profile work --json
agent-deck recall search "retry budget" --harness codex --json
agent-deck recall search SB-412 --hint ticket=SB-412 --phrase   # hint filters read state.db live
agent-deck recall sessions --tag auth --limit 10 --json
agent-deck recall sessions --harness hermes --json
```

- Search ranks sessions: a title, hint or tag hit always beats any number of
  body mentions. Terms are AND-ed; `SB-412` and `handle_sess` stay whole;
  `--phrase` verifies the literal phrase in the hits' bodies and says how
  many bodies it read; a hit whose matching body is clipped (8 KiB tier)
  is `unverified (clipped body)`, never `NOT found`. Filters (`--harness`,
  `--profile`, `--since`, `--project`, `--session`, `--role`) narrow the
  body candidates before the 5,000-message ceiling (newest first), so a
  filtered search on a common term is complete. The output (and `index`
  in `--json`) says when the bounded pre-search sweep left files behind;
  run `recall sweep` then.
- Hits carry `harness`, `profile`, `native_id` (the harness's own
  conversation id), `deck_id` when a registered session owns the
  conversation, `missing` when the file is gone (the text is still
  indexed), `sidechain` for a subagent transcript.
- One index, every profile: links, hints and cost events are read from and
  written to the profile whose state.db holds the link, whichever profile
  ran the sweep; `recall open` starts a session under its own profile.

### Read one: `recall show`

```bash
agent-deck recall show <session> --tier card            # counters, hints, tags, tools, files, no messages
agent-deck recall show <session> --turns 40 --json      # excerpt: the first 40 decoded messages
agent-deck recall show <session> --tier raw             # every message
```

`<session>` = the `#n` from a listing, a harness conversation id or a
unique prefix, or an agent-deck session id. Messages carry `class`
(`prompt`, `assist`, `compact_summary`, ...); a Codex compaction summary
supersedes the messages before it (they stay readable and searchable).

### Continue one: `recall open`

```bash
agent-deck recall open <session> [--dry-run]   # start the bound deck session (any harness), or re-register a Claude transcript (add --resume-session)
```

Codex, pi, Gemini, OpenCode and Hermes conversations that no registered
session owns are searchable, not resumable: `open` exits 2 and names the
`recall show` command instead.

### Annotate

`session annotate` (phase 1, above) is how a found session gets a
decision, an outcome or a ticket; the hint is searchable immediately
(`--hint`/`--tag` join state.db live) and reaches the ranking feed on the
next sweep.

### Keep it fresh: sweep, status, gc, rebuild

```bash
agent-deck recall status --json                # sizes, sources by state, by_harness, queued hook lines, roots
agent-deck recall sweep [--full] [--json]      # drains recall/queue.jsonl first, then what changed
agent-deck recall gc | rebuild
```

Exit codes: 2 recall off / not found, 3 load-gated or locked.

### The TUI

`G` (and `/`) in the TUI opens the same search: typing = `recall search`,
the preview = `recall show`, Enter = `recall open`. It never parses on a
keypress; a bounded sweep refreshes the index after the overlay opens and
a staleness line says what it deferred. With `[recall] enabled = false`
the key is the local title search.

### Remote

Only hints cross the SSH boundary today:
`agent-deck remote <host> session annotate <id> ...`. `recall search
--host` and card sync are phase 4.

### Typical agent flow

`recall search "<what you remember>" --harness <h> --json`, pick a hit,
`recall show <id> --tier card`, then `--turns 40` for the excerpt;
`session annotate <deck id> --decision ...` on what you learned;
`recall open <id>` to continue a Claude conversation in a new session.

## Phase 4 (not available yet; do not call these)

- `agent-deck recall context <query|session> --budget 4000 --tier card|brief|excerpt [--into current]` (hand a past conversation to the current session, any harness)
- `agent-deck recall search --host web1|all`, `recall export --cards | pull <host> | fetch <host> <session>` (remote, off by default)
- `agent-deck recall enrich --cost-class cheap|llm` and `agent-deck recall mcp`
- TUI `a` (annotate the focused session) and `R` (recall into the current session)

`session search` keeps its substring semantics and is not an alias for any of these.
