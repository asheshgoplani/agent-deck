---
name: agent-deck-recall
description: Record and later find what an agent-deck session was for, across harnesses. Use when the user says "remember what this session was for", "tag this session", "annotate", "mark the outcome", "ticket for this session", or wants to search past conversations. Phase 1 covers hints and tags (add/launch --hint, session annotate, remote annotate); transcript search, context handoff and MCP are coming in the next phases.
metadata:
  compatibility: "claude, codex, opencode"
---

# Recall

Recall is agent-deck's memory of what every session was for and what
happened in it. Human intent (hints, tags) lives in the profile's state.db
and survives everything; the transcript index (recall.db) is disposable and
arrives in the next phases. Full design: `docs/recall.md` in the repo.

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

## Coming in the next phases

Not available yet; do not call these:

- `agent-deck recall search "<q>" [--harness claude] [--project PATH] [--since 30d] [--hint ticket=SB-412] [--tag auth] [--phrase] [--host web1|all] [--json]`
- `agent-deck recall sessions | show <session> [--tier card|brief|excerpt|raw] | status | sweep | backfill | gc | rebuild`
- `agent-deck recall context <query|session> --budget 4000 --tier card|brief|excerpt [--into current]` (hand a past conversation to the current session, any harness)
- `agent-deck recall export --cards | pull <host> | fetch <host> <session>` (remote card sync, off by default)
- `agent-deck recall enrich --cost-class cheap|llm` and `agent-deck recall mcp`
- TUI: `G` global search over the index, `a` annotate the focused session, `R` recall it into the current session

`session search` keeps its substring semantics and is not an alias for any of these.
