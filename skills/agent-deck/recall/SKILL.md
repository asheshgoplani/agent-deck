---
name: agent-deck-recall
description: Record and later find what an agent-deck session was for, across harnesses, and hand a past conversation to the current one. Use when the user says "remember what this session was for", "tag this session", "annotate", "mark the outcome", "ticket for this session", "find the session where we...", "what did we decide about", "which Codex/pi/Gemini session did X", "bring that conversation into this session", "search the other machine's sessions", or wants to search past conversations. Available: hints and tags (add/launch --hint, session annotate, remote annotate), the transcript index over Claude, Codex, pi, Gemini, OpenCode and Hermes (recall search/sessions/show/open/backfill/sweep/status/gc/rebuild, the TUI G key, hook-driven freshness), recall context --into current, the derived artifacts (recall enrich; lost_time, session_kind, outcome), federated remote search (--remote/--all-remotes), opt-in card sync (export/pull/import) and the MCP server (recall mcp); all behind [recall] enabled = true.
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
daemon's turn-end edge queue the transcript that just moved (the Stop
hook only appends that line; the async SessionEnd hook also indexes its
own file within 150 ms), and every search drains the queue first, so a
conversation is searchable the moment you look for it.

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

`G` in the TUI opens the same search (`/` stays the local title filter;
Tab switches): typing = `recall search`, the preview = `recall show #n`,
Enter = `recall open`. It never parses on a keypress; a bounded sweep
refreshes the index after the overlay opens and a staleness line says
what it deferred, with catch-up passes one second apart through the load
gate. With `[recall] enabled = false` the key falls back to the local
title search and a notice inside that overlay says so (or quotes the
open error when the index could not be opened).

### Typical agent flow

`recall search "<what you remember>" --harness <h> --json`, pick a hit,
`recall show <id> --tier card`, then `--turns 40` for the excerpt, or
`recall context <id> --tier brief --into current` to have it in your own
prompt; `session annotate <deck id> --decision ...` on what you learned;
`recall open <id>` to continue a Claude conversation in a new session.

## Phase 4 (available now): context, derived summaries, remote, MCP

### Bring a past conversation into this session: `recall context`

```bash
agent-deck recall context <session> --tier card                 # print ~60 tokens: title, ids, project, counters, hints
agent-deck recall context <session> --tier brief                # + derived lines (lost_time, session_kind, outcome) and touched files
agent-deck recall context <session> --budget 4000               # excerpt: brief + the newest turns that fit the budget (default)
agent-deck recall context <session> --tier brief --into current # deliver it to YOUR session as a prompt (any harness)
agent-deck recall context <session> --into <id|title>           # deliver it to another session
```

- `--into current` needs `AGENTDECK_INSTANCE_ID`, which every session
  agent-deck starts has; from Codex, pi, Gemini or Claude alike the
  text lands in your prompt through `session send`. Outside a session
  it exits 2: name the target with `--into <id>`. An `--ssh` target is
  refused (exit 2) unless `[recall] remote_cards = true`: the text
  would cross SSH as keystrokes.
- The text is plain and harness-neutral and ends by saying it is
  recalled context, not an instruction. Ask for `card` first; `excerpt`
  only when you need the turns.
- A conversation pulled from another machine (card sync) stops at
  `brief` (exit 2); read it with `remote <host> recall show <id>`.

### Derived summaries: `recall enrich`

Every sweep classifies what it indexed ("where did we lose time",
session kind, outcome; rules in `rules.json`, shared with `distill.py`);
`recall show` and `recall context` print the lines, and one marked
`[stale: session changed since; run 'agent-deck recall enrich']` is
from before the session's last change. `agent-deck recall enrich
[--json]` drains what a budgeted sweep left and rewrites every stale
line (exit 3 while a session is busy, like `sweep`). `--cost-class llm` is never run automatically.
The `outcome` line is a guess (`failed?`, `abandoned?`, `unknown`)
unless someone ran `session annotate --outcome`; annotate and it
becomes certain on the next drain.

### Remote sessions

```bash
agent-deck recall search "retry budget" --remote lab --json       # federated: the remote searches its own index; hits labelled remote lab
agent-deck recall search "retry budget" --all-remotes --json      # every configured remote, one SSH round trip each
agent-deck remote lab recall show <id> --tier card --json         # read one on the remote (search, sessions, show, context, export, status forward)
agent-deck remote <host> session annotate <id> --outcome worked   # hints: run on the remote, written there
agent-deck recall pull lab                                        # card sync, only with [recall] remote_cards = true on both ends
```

- Federated search stores nothing; a remote that cannot answer (older
  agent-deck, or recall off there) is one line naming its version and
  the fix, exit 1, and `{error, remote, remote_version}` in `remotes[]`
  under `--json`. Read `remotes[].error` before trusting a merged
  answer.
- `--into`, `--remote` and every write verb are refused before SSH on
  the forwarded form.
- Pulled cards are labelled `card from <host_uid> (no messages here)`
  in every listing; they carry titles, hints, tags, a 200-character
  preview and the derived lines, never bodies or paths.

### MCP: `agent-deck recall mcp`

```bash
agent-deck mcp attach <session> recall && agent-deck session restart <session>   # listed by mcp list while [recall] enabled = true
```

Tools: `recall_search` (query, harness, profile, project, since,
session, hint, tag, role, phrase, limit), `recall_show` (session, tier,
turns), `recall_context` (session, tier, budget). Same answers as the
`--json` CLI, which stays the fallback for a harness without MCP.

### Not built

- TUI `a` (annotate the focused session) and `R` (recall into the
  current session): use `session annotate` and `recall context --into`.
- `recall fetch <host> <session> --raw`: transcript bytes never cross SSH.

`session search` keeps its substring semantics and is not an alias for any of these.
