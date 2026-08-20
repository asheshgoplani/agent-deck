# Conductor: Shared Knowledge Base

This file contains shared infrastructure knowledge (CLI reference, protocols, formats) for all conductor sessions.
Each conductor has its own identity in its subdirectory and its own policy in POLICY.md.

## Agent-Deck CLI Reference

### Status & Listing
| Command | Description |
|---------|-------------|
| `agent-deck -p <PROFILE> status --json` | Get counts: `{"waiting": N, "running": N, "idle": N, "error": N, "stopped": N, "total": N}` |
| `agent-deck -p <PROFILE> list --json` | List all sessions with details (id, title, path, tool, status, group) |
| `agent-deck -p <PROFILE> session show --json <id_or_title>` | Full details for one session |

### Reading Session Output
| Command | Description |
|---------|-------------|
| `agent-deck -p <PROFILE> session output <id_or_title> -q` | Get the last response (raw text, perfect for reading) |

### Sending Messages to Sessions
| Command | Description |
|---------|-------------|
| `agent-deck -p <PROFILE> session send <id_or_title> "message"` | Send a message. Has built-in 60s wait for agent readiness. |
| `agent-deck -p <PROFILE> session send <id_or_title> "message" --wait -q --timeout 300s` | Single-call send + wait + raw output (preferred when you need the reply now). |
| `agent-deck -p <PROFILE> session send <id_or_title> "message" --no-wait` | Send immediately without waiting for ready state. |
| `agent-deck -p <PROFILE> session approve <id_or_title> [once|always|session|N]` | Resolve a visible Codex approval prompt with one keypress. Never use `session send "1"` for Codex approvals. |

### Session Control
| Command | Description |
|---------|-------------|
| `agent-deck -p <PROFILE> session start <id_or_title>` | Start a stopped session |
| `agent-deck -p <PROFILE> session stop <id_or_title>` | Stop a running session |
| `agent-deck -p <PROFILE> session restart <id_or_title>` | Restart a managed session |
| `agent-deck -p <PROFILE> add <path> -t "Title" -c claude -g "group"` | Create a new Claude Code session |
| `agent-deck -p <PROFILE> launch <path> -t "Title" -c claude -g "group" -m "prompt"` | Create + start + send initial prompt in one command (preferred for new task sessions) |
| `agent-deck -p <PROFILE> add <path> -t "Title" -c claude --worktree feature/branch -b` | Create a new Claude Code session with a worktree |

### Session Resolution
Commands accept: **exact title**, **ID prefix** (e.g., first 4 chars), **path**, or **fuzzy match**.

## Session Status Values

| Status | Meaning | Your Action |
|--------|---------|-------------|
| `running` (green) | The conductor is actively processing | Do nothing. Wait. |
| `waiting` (yellow) | The conductor finished and needs input | Read output, decide: auto-respond or escalate |
| `idle` (gray) | Waiting, but user acknowledged | User knows about it. Skip unless asked. |
| `error` (red) | Crashed, missing, or wedged (auth/model failure) | Check the substate first. Then try `session restart`; if that fails, escalate. |

**Substate (Claude sessions only; refines status in `list`/`show` JSON):** `auth-401` covers two different pane banners. A credential banner (`Please run /login`, `API Error: 401`) means the fleet is HOLDING the session; restarting will NOT fix it. Check `session show --json <id>` for the `auth_hold` object (the authoritative source, present even after the pane exits) and escalate for re-login. A dropped-socket banner (`socket connection closed`) also classifies as `auth-401` but is NOT held and IS restart-recoverable: restart it. `model-unavailable` means the selected model is down (shows as error, not running); self-heal currently only observes this and takes no action, so switch it yourself with `agent-deck -p <PROFILE> session set <id> model <model>` then `agent-deck -p <PROFILE> session restart <id>`. `idle-at-empty-prompt` (shown as coarse status `idle` or `waiting`) means the session is genuinely sitting at its prompt with nothing happening. Never restart-loop an `error` session that `auth_hold` confirms is credential-held.

## Heartbeat Protocol

Every N minutes, the bridge sends you a message like:

```
[HEARTBEAT] [<name>] Status: 2 waiting, 3 running, 1 idle, 0 error. Waiting sessions: frontend (project: ~/src/app), api-fix (project: ~/src/api). Check if any need auto-response or user attention.
```

**FIRST step of EVERY heartbeat — drain your inbox:**

```bash
agent-deck inbox drain self
```

This pulls any child completions that landed in your durable outbox while you were
busy (issue #1225/#1226). Delivery is pull, not push: a child that finished mid-turn
committed its completion to `~/.agent-deck/inboxes/<your-id>.jsonl` rather than typing
into your pane. The drain marks records consumed (exactly-once effects) and prints
them; act on each before composing your status. Your Stop hook drains the same queue
automatically at each turn boundary, so this heartbeat drain is the idle-conductor
fallback — together they guarantee no completion is missed whether you are busy or idle.

**Your heartbeat response format:**

```
[STATUS] All clear.
```

or:

```
[STATUS] Auto-responded to 1 session. 1 needs your attention.

AUTO: frontend - told it to use the existing auth middleware
NEED: api-fix - asking whether to run integration tests against staging or prod
```

Your response is parsed: if it contains `NEED:` lines, those get forwarded to the user (via remote channels if configured, or visible in the TUI/task-log).

## State Management

Maintain `./state.json` for persistent context across compactions:

```json
{
  "sessions": {
    "session-id-here": {
      "title": "frontend",
      "project": "~/src/app",
      "summary": "Building auth flow with React Router v7",
      "last_auto_response": "2025-01-15T10:30:00Z",
      "escalated": false
    }
  },
  "last_heartbeat": "2025-01-15T10:30:00Z",
  "auto_responses_today": 5,
  "escalations_today": 2
}
```

Read state.json at the start of each interaction. Update it after taking action. Keep session summaries current based on what you observe in their output.

## Task Log

Append every action to `./task-log.md`:

```markdown
## 2025-01-15 10:30 - Heartbeat
- Scanned 5 sessions (2 waiting, 3 running)
- Auto-responded to frontend: "Use the existing AuthProvider component"
- Escalated api-fix: needs decision on test environment

## 2025-01-15 10:15 - User Message
- User asked: "What's the status of the api server?"
- Checked session 'api-server': running, working on endpoint validation
- Responded with summary
```

## Self-Improvement

Maintain `LEARNINGS.md` to track orchestration patterns. Two tiers exist:
- `../LEARNINGS.md` (shared): patterns that work across all conductors
- `./LEARNINGS.md` (per-conductor): patterns specific to your profile and sessions

### When to Log

| Situation | Entry Type |
|-----------|-----------|
| You auto-responded and user later said it was wrong | `auto_response_wrong` |
| You auto-responded and it worked well | `auto_response_ok` |
| You escalated but user said it was fine to auto-respond | `escalation_unnecessary` |
| You escalated and user confirmed it needed attention | `escalation_correct` |
| You notice a recurring session behavior | `session_behavior` |
| You discover a useful pattern | `pattern` |

### Promotion to Policy

When an entry reaches Recurrence 3+ and has proven reliable, promote it:
1. Distill into a concise rule
2. Add to `./POLICY.md` (create if needed) or request update to `../POLICY.md` (shared)
3. Set entry Status to `promoted`

### At Startup

Read both `./LEARNINGS.md` and `../LEARNINGS.md` before responding. Past patterns inform current decisions.

## Quick Commands

You may receive these special commands (from remote channels or the CLI):

| Command | What to Do |
|---------|------------|
| `/status` | Run `agent-deck -p <PROFILE> status --json` and format a brief summary |
| `/sessions` | Run `agent-deck -p <PROFILE> list --json` and list active sessions with status |
| `/check <name>` | Run `agent-deck -p <PROFILE> session output <name> -q` and summarize what it's doing |
| `/send <name> <msg>` | Forward the message to that session via `agent-deck -p <PROFILE> session send` |
| `/help` | List available commands |

For any other text, treat it as a conversational message from the user. They might ask about session progress, give instructions for specific sessions, or ask you to create/manage sessions.

## Slack Message Format

When messages arrive from Slack, the bridge tags them with sender and channel context:

```
[from:alice (U12345)] [channel:#bugs (C67890)] the login button is broken
[from:bob (U11111)] [dm] can you check the API?
[from:charlie (U22222)] [channel:#feature-requests (C33333)] add dark mode support
```

- `[from:<name> (<user_id>)]` — The Slack display name and stable user ID of the sender
- `[channel:#<name> (<channel_id>)]` — The Slack channel name and stable channel ID
- `[dm]` — The message was sent via direct message

Use these tags to:
- **Identify the requester** when logging actions or escalating
- **Route by channel** — messages from #bugs are likely bug reports, #ideas are feature requests
- **Include sender context in escalations** — e.g., "NEED: @alice (#bugs): login button broken"

If the bridge cannot resolve a name (temporary API failure), the raw Slack ID appears alone (e.g., `[from:U12345 (U12345)]`, `[channel:C99999]`). Failed lookups are retried automatically after 5 minutes.

## Important Notes

- This project is `asheshgoplani/agent-deck` on GitHub. When referencing GitHub issues or PRs, always use owner `asheshgoplani` and repo `agent-deck`. Never use `anthropics` as the owner.
- You cannot directly access other sessions' files. Use `session output` to read their latest response.
- Prefer `launch ... -m "prompt"` over separate `add` + `session start` + `session send` when creating a new task session.
- Keep parent linkage for event routing; if you need a specific group, pass `-g <group>` explicitly (it overrides inherited parent group).
- Transition notifications are parent-linked. If `parent_session_id` is empty or points elsewhere, this conductor will not receive child completion events.
- `session send` waits up to ~80 seconds for the agent to be ready. If the session is running (busy), the send will wait.
- When a Codex child shows a numbered approval menu, use `session approve <id> <choice>`. A digit sent through `session send` is composer text plus Enter and can interrupt the resumed turn.
- For periodic nudges/heartbeats where blocking is harmful, prefer `session send --no-wait -q`.
- Remote channels send with `session send --wait -q` and wait in a single CLI call. Reply promptly.
- Your own session can be restarted by the bridge if it detects you're in an error state.
- Keep state.json small (no large output dumps). Store summaries, not full text.
