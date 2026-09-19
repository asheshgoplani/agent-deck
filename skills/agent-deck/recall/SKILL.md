---
name: agent-deck-recall
description: Record and later find what an agent-deck session was for, across harnesses. Use when the user says "remember what this session was for", "tag this session", "annotate", "mark the outcome", "ticket for this session", "find the session where we...", "what did we decide about", or wants to search past conversations. Phases 1 and 2 are available: hints and tags (add/launch --hint, session annotate, remote annotate) and the Claude transcript index (recall search/sessions/show/open/backfill/sweep/status/gc/rebuild, behind [recall] enabled = true); other harnesses, context handoff, remote federation and MCP are coming in the next phases.
metadata:
  compatibility: "claude, codex, opencode"
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

## Phase 2 (available now): the Claude transcript index

Needs `[recall] enabled = true` in config.toml (every command exits 2 with a
clear message otherwise). One index for every profile on the machine;
`--profile` narrows. Nothing runs in the background: a sweep runs inside
the command and ends with it.

```bash
agent-deck recall backfill                     # once; resumable; refuses while a session is busy (--force)
agent-deck recall search "clock skew" --since 30d --profile work --json
agent-deck recall search SB-412 --hint ticket=SB-412 --phrase   # hint filters read state.db live
agent-deck recall sessions --tag auth --limit 10 --json
agent-deck recall show <session> --tier card|excerpt|raw --turns 20 --json
agent-deck recall open <session> [--dry-run]   # start the bound session, or re-register the transcript (add --resume-session)
agent-deck recall status --json                # sizes, sources by state, pending, expiring
agent-deck recall sweep [--full] | gc | rebuild
```

- `<session>` = the `#n` from a listing, a Claude conversation id or a unique
  prefix, or an agent-deck session id.
- Search ranks sessions: a title, hint or tag hit always beats any number of
  body mentions. Terms are AND-ed; `SB-412` and `handle_sess` stay whole;
  `--phrase` verifies the literal phrase in the hits' bodies and says how
  many bodies it read; a hit whose matching body is clipped (8 KiB tier)
  is `unverified (clipped body)`, never `NOT found`. Filters (`--profile`, `--since`, `--project`, `--session`,
  `--role`) narrow the body candidates before the 5,000-message ceiling
  (newest first), so a filtered search on a common term is complete. The
  output (and `index` in `--json`) says when the bounded pre-search sweep
  left files behind; run `recall sweep` then.
- One index, every profile: links, hints and cost events are read from and
  written to the profile whose state.db holds the link, whichever profile
  ran the sweep; `recall open` starts a session under its own profile.
- Exit codes: 2 recall off / not found, 3 load-gated or locked.
- Typical agent flow: `recall search "<what you remember>" --json`, pick a
  hit, `recall show <id> --tier card`, then `--turns 40` for the excerpt;
  `recall open <id>` to continue that conversation in a new session.

## Coming in the next phases

Not available yet; do not call these:

- Codex, Pi, Gemini, OpenCode and Hermes transcripts in the index; hook-triggered sweeps; the TUI `G` key over the index, `a` annotate, `R` recall into the current session
- `agent-deck recall context <query|session> --budget 4000 --tier card|brief|excerpt [--into current]` (hand a past conversation to the current session, any harness)
- `agent-deck recall search --host web1|all`, `recall export --cards | pull <host> | fetch <host> <session>` (remote, off by default)
- `agent-deck recall enrich --cost-class cheap|llm` and `agent-deck recall mcp`

`session search` keeps its substring semantics and is not an alias for any of these.
