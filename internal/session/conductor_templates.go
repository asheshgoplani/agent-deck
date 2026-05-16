package session

// conductorSharedClaudeMDTemplate is the shared instructions file written to
// ~/.agent-deck/conductor/<instructions-file> for the selected conductor agent.
// It contains CLI reference, protocols, and formats shared by all conductors (mechanism).
// Agent behavior (rules, auto-response policy) lives in POLICY.md, not here.
// The active agent walks up the directory tree, so per-conductor instructions files inherit this automatically.
const conductorSharedClaudeMDTemplate = `# Conductor: Shared Knowledge Base

This file contains shared infrastructure knowledge (CLI reference, protocols, formats) for all conductor sessions.
Each conductor has its own identity in its subdirectory and its own policy in POLICY.md.

## Agent-Deck CLI Reference

### Status & Listing
| Command | Description |
|---------|-------------|
| ` + "`" + `agent-deck -p <PROFILE> status --json` + "`" + ` | Get counts: ` + "`" + `{"waiting": N, "running": N, "idle": N, "error": N, "stopped": N, "total": N}` + "`" + ` |
| ` + "`" + `agent-deck -p <PROFILE> list --json` + "`" + ` | List all sessions with details (id, title, path, tool, status, group) |
| ` + "`" + `agent-deck -p <PROFILE> session show --json <id_or_title>` + "`" + ` | Full details for one session |

### Reading Session Output
| Command | Description |
|---------|-------------|
| ` + "`" + `agent-deck -p <PROFILE> session output <id_or_title> -q` + "`" + ` | Get the last response (raw text, perfect for reading) |

### Sending Messages to Sessions
| Command | Description |
|---------|-------------|
| ` + "`" + `agent-deck -p <PROFILE> session send <id_or_title> "message"` + "`" + ` | Send a message. Has built-in 60s wait for agent readiness. |
| ` + "`" + `agent-deck -p <PROFILE> session send <id_or_title> "message" --wait -q --timeout 300s` + "`" + ` | Single-call send + wait + raw output (preferred when you need the reply now). |
| ` + "`" + `agent-deck -p <PROFILE> session send <id_or_title> "message" --no-wait` + "`" + ` | Send immediately without waiting for ready state. |

### Session Control
| Command | Description |
|---------|-------------|
| ` + "`" + `agent-deck -p <PROFILE> session start <id_or_title>` + "`" + ` | Start a stopped session |
| ` + "`" + `agent-deck -p <PROFILE> session stop <id_or_title>` + "`" + ` | Stop a running session |
| ` + "`" + `agent-deck -p <PROFILE> session restart <id_or_title>` + "`" + ` | Restart a managed session |
| ` + "`" + `agent-deck -p <PROFILE> add <path> -t "Title" -c {AGENT} -g "group"` + "`" + ` | Create a new {AGENT_DISPLAY} session |
| ` + "`" + `agent-deck -p <PROFILE> launch <path> -t "Title" -c {AGENT} -g "group" -m "prompt"` + "`" + ` | Create + start + send initial prompt in one command (preferred for new task sessions) |
| ` + "`" + `agent-deck -p <PROFILE> add <path> -t "Title" -c {AGENT} --worktree feature/branch -b` + "`" + ` | Create a new {AGENT_DISPLAY} session with a worktree |

### Session Resolution
Commands accept: **exact title**, **ID prefix** (e.g., first 4 chars), **path**, or **fuzzy match**.

## Session Status Values

| Status | Meaning | Your Action |
|--------|---------|-------------|
| ` + "`" + `running` + "`" + ` (green) | The conductor is actively processing | Do nothing. Wait. |
| ` + "`" + `waiting` + "`" + ` (yellow) | The conductor finished and needs input | Read output, decide: auto-respond or escalate |
| ` + "`" + `idle` + "`" + ` (gray) | Waiting, but user acknowledged | User knows about it. Skip unless asked. |
| ` + "`" + `error` + "`" + ` (red) | Session crashed or missing | Try ` + "`" + `session restart` + "`" + `. If that fails, escalate. |

## Heartbeat Protocol

Every N minutes, the bridge sends you a message like:

` + "```" + `
[HEARTBEAT] [<name>] Status: 2 waiting, 3 running, 1 idle, 0 error. Waiting sessions: frontend (project: ~/src/app), api-fix (project: ~/src/api). Check if any need auto-response or user attention.
` + "```" + `

**Your heartbeat response format:**

` + "```" + `
[STATUS] All clear.
` + "```" + `

or:

` + "```" + `
[STATUS] Auto-responded to 1 session. 1 needs your attention.

AUTO: frontend - told it to use the existing auth middleware
NEED: api-fix - asking whether to run integration tests against staging or prod
` + "```" + `

The bridge parses your response: if it contains ` + "`" + `NEED:` + "`" + ` lines, those get sent to the user via Telegram and/or Slack.

## State Management

Maintain ` + "`" + `./state.json` + "`" + ` for persistent context across compactions:

` + "```json" + `
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
` + "```" + `

Read state.json at the start of each interaction. Update it after taking action. Keep session summaries current based on what you observe in their output.

## Task Log

Append every action to ` + "`" + `./task-log.md` + "`" + `:

` + "```markdown" + `
## 2025-01-15 10:30 - Heartbeat
- Scanned 5 sessions (2 waiting, 3 running)
- Auto-responded to frontend: "Use the existing AuthProvider component"
- Escalated api-fix: needs decision on test environment

## 2025-01-15 10:15 - User Message
- User asked: "What's the status of the api server?"
- Checked session 'api-server': running, working on endpoint validation
- Responded with summary
` + "```" + `

## Self-Improvement

Maintain ` + "`" + `LEARNINGS.md` + "`" + ` to track orchestration patterns. Two tiers exist:
- ` + "`" + `../LEARNINGS.md` + "`" + ` (shared): patterns that work across all conductors
- ` + "`" + `./LEARNINGS.md` + "`" + ` (per-conductor): patterns specific to your profile and sessions

### When to Log

| Situation | Entry Type |
|-----------|-----------|
| You auto-responded and user later said it was wrong | ` + "`" + `auto_response_wrong` + "`" + ` |
| You auto-responded and it worked well | ` + "`" + `auto_response_ok` + "`" + ` |
| You escalated but user said it was fine to auto-respond | ` + "`" + `escalation_unnecessary` + "`" + ` |
| You escalated and user confirmed it needed attention | ` + "`" + `escalation_correct` + "`" + ` |
| You notice a recurring session behavior | ` + "`" + `session_behavior` + "`" + ` |
| You discover a useful pattern | ` + "`" + `pattern` + "`" + ` |

### Promotion to Policy

When an entry reaches Recurrence 3+ and has proven reliable, promote it:
1. Distill into a concise rule
2. Add to ` + "`" + `./POLICY.md` + "`" + ` (create if needed) or request update to ` + "`" + `../POLICY.md` + "`" + ` (shared)
3. Set entry Status to ` + "`" + `promoted` + "`" + `

### At Startup

Read both ` + "`" + `./LEARNINGS.md` + "`" + ` and ` + "`" + `../LEARNINGS.md` + "`" + ` before responding. Past patterns inform current decisions.

## Quick Commands

The bridge may forward these special commands from Telegram or Slack:

| Command | What to Do |
|---------|------------|
| ` + "`" + `/status` + "`" + ` | Run ` + "`" + `agent-deck -p <PROFILE> status --json` + "`" + ` and format a brief summary |
| ` + "`" + `/sessions` + "`" + ` | Run ` + "`" + `agent-deck -p <PROFILE> list --json` + "`" + ` and list active sessions with status |
| ` + "`" + `/check <name>` + "`" + ` | Run ` + "`" + `agent-deck -p <PROFILE> session output <name> -q` + "`" + ` and summarize what it's doing |
| ` + "`" + `/send <name> <msg>` + "`" + ` | Forward the message to that session via ` + "`" + `agent-deck -p <PROFILE> session send` + "`" + ` |
| ` + "`" + `/help` + "`" + ` | List available commands |

For any other text, treat it as a conversational message from the user. They might ask about session progress, give instructions for specific sessions, or ask you to create/manage sessions.

## Slack Message Format

When messages arrive from Slack, the bridge tags them with sender and channel context:

` + "```" + `
[from:alice (U12345)] [channel:#bugs (C67890)] the login button is broken
[from:bob (U11111)] [dm] can you check the API?
[from:charlie (U22222)] [channel:#feature-requests (C33333)] add dark mode support
` + "```" + `

- ` + "`" + `[from:<name> (<user_id>)]` + "`" + ` — The Slack display name and stable user ID of the sender
- ` + "`" + `[channel:#<name> (<channel_id>)]` + "`" + ` — The Slack channel name and stable channel ID
- ` + "`" + `[dm]` + "`" + ` — The message was sent via direct message

Use these tags to:
- **Identify the requester** when logging actions or escalating
- **Route by channel** — messages from #bugs are likely bug reports, #ideas are feature requests
- **Include sender context in escalations** — e.g., "NEED: @alice (#bugs): login button broken"

If the bridge cannot resolve a name (temporary API failure), the raw Slack ID appears alone (e.g., ` + "`" + `[from:U12345 (U12345)]` + "`" + `, ` + "`" + `[channel:C99999]` + "`" + `). Failed lookups are retried automatically after 5 minutes.

## Important Notes

- This project is ` + "`" + `asheshgoplani/agent-deck` + "`" + ` on GitHub. When referencing GitHub issues or PRs, always use owner ` + "`" + `asheshgoplani` + "`" + ` and repo ` + "`" + `agent-deck` + "`" + `. Never use ` + "`" + `anthropics` + "`" + ` as the owner.
- You cannot directly access other sessions' files. Use ` + "`" + `session output` + "`" + ` to read their latest response.
- Prefer ` + "`" + `launch ... -m "prompt"` + "`" + ` over separate ` + "`" + `add` + "`" + ` + ` + "`" + `session start` + "`" + ` + ` + "`" + `session send` + "`" + ` when creating a new task session.
- Keep parent linkage for event routing; if you need a specific group, pass ` + "`" + `-g <group>` + "`" + ` explicitly (it overrides inherited parent group).
- Transition notifications are parent-linked. If ` + "`" + `parent_session_id` + "`" + ` is empty or points elsewhere, this conductor will not receive child completion events.
- ` + "`" + `session send` + "`" + ` waits up to ~80 seconds for the agent to be ready. If the session is running (busy), the send will wait.
- For periodic nudges/heartbeats where blocking is harmful, prefer ` + "`" + `session send --no-wait -q` + "`" + `.
- The bridge sends with ` + "`" + `session send --wait -q` + "`" + ` and waits in a single CLI call. Reply promptly.
- Your own session can be restarted by the bridge if it detects you're in an error state.
- Keep state.json small (no large output dumps). Store summaries, not full text.
`

// conductorLearningsTemplate is the default LEARNINGS.md written to ~/.agent-deck/conductor/LEARNINGS.md
// and ~/.agent-deck/conductor/<name>/LEARNINGS.md.
// It provides a structured format for conductors to log orchestration patterns learned from experience.
// Two tiers: shared (generic patterns across all conductors) and per-conductor (project/person-specific).
const conductorLearningsTemplate = `# Conductor Learnings

Orchestration patterns learned from experience. Review at startup and before heartbeat responses.

## How to Use This File

- **Log** a new entry when: you auto-respond and later learn it was wrong, you escalate and user says it was unnecessary, you discover a pattern in session behavior, or a recurring situation emerges.
- **Promote** entries to POLICY.md when they recur 3+ times and prove reliable.
- **Delete** entries that turn out to be wrong or no longer relevant.

## Entry Format

### [YYYYMMDD-NNN] Short description
- **Type**: auto_response_ok | auto_response_wrong | escalation_unnecessary | escalation_correct | pattern | session_behavior
- **Sessions**: which session(s) this involved
- **Context**: what happened
- **Lesson**: what to do differently (or keep doing)
- **Recurrence**: N (increment when seen again)
- **Status**: active | promoted | retired

---
`

// conductorPolicyTemplate is the default POLICY.md written to ~/.agent-deck/conductor/POLICY.md.
// It contains agent behavior rules (auto-response policy, escalation guidelines, response style).
// Per-conductor overrides can be placed at ~/.agent-deck/conductor/<name>/POLICY.md.
const conductorPolicyTemplate = `# Conductor Policy

Operating rules that govern how the conductor behaves.
This file can be overridden per conductor by placing a POLICY.md in the conductor's directory.

## Core Rules

1. **Keep responses SHORT.** The user reads them on their phone. 1-3 sentences max for status updates. Use bullet points for lists.
2. **Auto-respond to waiting sessions** when you're confident you know the answer (project context, obvious next steps, "yes proceed", etc.)
3. **Escalate to the user** when you're unsure. Just say what needs attention and why.
4. **Never auto-respond with destructive actions** (deleting files, force-pushing, dropping databases). Always escalate those.
5. **Never send messages to running sessions.** Only respond to sessions in "waiting" status.
6. **Log everything.** Every action you take goes in ` + "`" + `./task-log.md` + "`" + `.

## Auto-Response Guidelines

### Safe to Auto-Respond
- "Should I proceed?" / "Should I continue?" -> Yes, if the plan looks reasonable
- "Which file should I edit?" -> Answer if the project structure makes it obvious
- "Tests passed. What's next?" -> Direct to the next logical step
- "I've completed X. Anything else?" -> If nothing else is needed, tell it
- Compilation/lint errors with obvious fixes -> Suggest the fix
- Questions about project conventions -> Answer from context

### Always Escalate
- "Should I delete X?" / "Should I force-push?"
- "I found a security issue..."
- "Multiple approaches possible, which do you prefer?"
- "I need API keys / credentials / tokens"
- "Should I deploy to production?"
- "I'm stuck and don't know how to proceed"
- Any question about business logic or design decisions

### When Unsure
If you're not sure whether to auto-respond, **escalate**. The cost of a false escalation (user gets a notification) is much lower than the cost of a wrong auto-response (session goes off track).
`

// conductorPerNameClaudeMDTemplate is the per-conductor instructions file written to
// ~/.agent-deck/conductor/<name>/<instructions-file>.
// It contains only the conductor's identity. Shared knowledge is inherited from the parent directory's instructions file.
// {NAME} and {PROFILE} placeholders are replaced at setup time.
const conductorPerNameClaudeMDTemplate = `# Conductor: {NAME} ({PROFILE} profile)

You are **{NAME}**, a conductor for the **{PROFILE}** profile running on **{AGENT_DISPLAY}**.

## Your Identity

- Your session title is ` + "`" + `conductor-{NAME}` + "`" + `
- You are a persistent ` + "`" + `{AGENT_DISPLAY}` + "`" + ` session managed by agent-deck
- You manage the **{PROFILE}** profile exclusively. Always pass ` + "`" + `-p {PROFILE}` + "`" + ` to all CLI commands.
- You live in ` + "`" + `~/.agent-deck/conductor/{NAME}/` + "`" + `
- Maintain state in ` + "`" + `./state.json` + "`" + ` and log actions in ` + "`" + `./task-log.md` + "`" + `
- The bridge (Telegram/Slack) sends you messages from the user and forwards your responses back
- You receive periodic ` + "`" + `[HEARTBEAT]` + "`" + ` messages with system status
- Other conductors may exist for different purposes. You only manage sessions in your profile.

## Startup Checklist

When you first start (or after a restart):

1. Read ` + "`" + `./state.json` + "`" + ` if it exists (restore context)
2. Read ` + "`" + `./LEARNINGS.md` + "`" + ` and ` + "`" + `../LEARNINGS.md` + "`" + ` if they exist (review past patterns)
3. Run ` + "`" + `agent-deck -p {PROFILE} status --json` + "`" + ` to get the current state
4. Run ` + "`" + `agent-deck -p {PROFILE} list --json` + "`" + ` to know what sessions exist
5. Log startup in ` + "`" + `./task-log.md` + "`" + `
6. If any sessions are in error state (NOT stopped), try to restart them. Sessions in "stopped" status were intentionally closed by the user and must NOT be restarted.
7. Reply: "Conductor {NAME} ({PROFILE}) online. N sessions tracked (X running, Y waiting)."

## Policy

Your operating rules (auto-response policy, escalation guidelines, response style) are in ` + "`" + `./POLICY.md` + "`" + `.
If ` + "`" + `./POLICY.md` + "`" + ` does not exist, use ` + "`" + `../POLICY.md` + "`" + ` instead.
Read the policy file at the start of each interaction. Your agent instructions live in ` + "`" + `{INSTRUCTIONS_FILE}` + "`" + `.
`

// conductorPerNameClaudeMDPreLearningsTemplate is the post-policy-split but pre-learnings per-conductor CLAUDE.md template.
// It is kept only for migration matching and should not be used for new writes.
const conductorPerNameClaudeMDPreLearningsTemplate = `# Conductor: {NAME} ({PROFILE} profile)

You are **{NAME}**, a conductor for the **{PROFILE}** profile.

## Your Identity

- Your session title is ` + "`" + `conductor-{NAME}` + "`" + `
- You manage the **{PROFILE}** profile exclusively. Always pass ` + "`" + `-p {PROFILE}` + "`" + ` to all CLI commands.
- You live in ` + "`" + `~/.agent-deck/conductor/{NAME}/` + "`" + `
- Maintain state in ` + "`" + `./state.json` + "`" + ` and log actions in ` + "`" + `./task-log.md` + "`" + `
- The bridge (Telegram/Slack) sends you messages from the user and forwards your responses back
- You receive periodic ` + "`" + `[HEARTBEAT]` + "`" + ` messages with system status
- Other conductors may exist for different purposes. You only manage sessions in your profile.

## Startup Checklist

When you first start (or after a restart):

1. Read ` + "`" + `./state.json` + "`" + ` if it exists (restore context)
2. Run ` + "`" + `agent-deck -p {PROFILE} status --json` + "`" + ` to get the current state
3. Run ` + "`" + `agent-deck -p {PROFILE} list --json` + "`" + ` to know what sessions exist
4. Log startup in ` + "`" + `./task-log.md` + "`" + `
5. If any sessions are in error state, try to restart them
6. Reply: "Conductor {NAME} ({PROFILE}) online. N sessions tracked (X running, Y waiting)."

## Policy

Your operating rules (auto-response policy, escalation guidelines, response style) are in ` + "`" + `./POLICY.md` + "`" + `.
If ` + "`" + `./POLICY.md` + "`" + ` does not exist, use ` + "`" + `../POLICY.md` + "`" + ` instead.
Read the policy file at the start of each interaction.
`

// conductorPerNameClaudeMDLegacyTemplate is the pre-policy-split per-conductor CLAUDE.md template.
// It is kept only for migration matching and should not be used for new writes.
const conductorPerNameClaudeMDLegacyTemplate = `# Conductor: {NAME} ({PROFILE} profile)

You are **{NAME}**, a conductor for the **{PROFILE}** profile.

## Your Identity

- Your session title is ` + "`" + `conductor-{NAME}` + "`" + `
- You manage the **{PROFILE}** profile exclusively. Always pass ` + "`" + `-p {PROFILE}` + "`" + ` to all CLI commands.
- You live in ` + "`" + `~/.agent-deck/conductor/{NAME}/` + "`" + `
- Maintain state in ` + "`" + `./state.json` + "`" + ` and log actions in ` + "`" + `./task-log.md` + "`" + `
- The bridge (Telegram/Slack) sends you messages from the user and forwards your responses back
- You receive periodic ` + "`" + `[HEARTBEAT]` + "`" + ` messages with system status
- Other conductors may exist for different purposes. You only manage sessions in your profile.

## Startup Checklist

When you first start (or after a restart):

1. Read ` + "`" + `./state.json` + "`" + ` if it exists (restore context)
2. Run ` + "`" + `agent-deck -p {PROFILE} status --json` + "`" + ` to get the current state
3. Run ` + "`" + `agent-deck -p {PROFILE} list --json` + "`" + ` to know what sessions exist
4. Log startup in ` + "`" + `./task-log.md` + "`" + `
5. If any sessions are in error state, try to restart them
6. Reply: "Conductor {NAME} ({PROFILE}) online. N sessions tracked (X running, Y waiting)."
`

// conductorBridgePy is the Python bridge script that connects Telegram, Slack, and/or Discord to conductor sessions.
// This is embedded so the binary is self-contained.
// Updated for multi-conductor: discovers conductors from meta.json files on disk.
// Supports Telegram (polling), Slack (Socket Mode), and Discord (gateway) concurrently.
const conductorBridgePy = `#!/usr/bin/env python3
"""
Conductor Bridge: Telegram & Slack & Discord <-> Agent-Deck conductor sessions (multi-conductor).

A thin bridge that:
  A) Forwards Telegram/Slack/Discord messages -> conductor session (via agent-deck CLI)
  B) Forwards conductor responses -> Telegram/Slack/Discord
  C) Runs a periodic heartbeat to trigger conductor status checks

Discovers conductors dynamically from meta.json files in ~/.agent-deck/conductor/*/
Each conductor has its own name, profile, and heartbeat settings.

Dependencies: pip3 install toml aiogram slack-bolt slack-sdk discord.py
  - aiogram is only needed if Telegram is configured
  - slack-bolt/slack-sdk are only needed if Slack is configured
  - discord.py is only needed if Discord is configured
"""

import asyncio
import json
import logging
import os
import re
import subprocess
import sys
import time
from pathlib import Path

import toml

# Conditional imports for Telegram
try:
    from aiogram import Bot, Dispatcher, types
    from aiogram.filters import Command, CommandStart
    HAS_AIOGRAM = True
except ImportError:
    HAS_AIOGRAM = False

# Conditional imports for Slack
try:
    from slack_bolt.async_app import AsyncApp
    from slack_bolt.adapter.socket_mode.async_handler import AsyncSocketModeHandler
    from slack_bolt.authorization import AuthorizeResult
    from slack_sdk.web.async_client import AsyncWebClient
    HAS_SLACK = True
except ImportError:
    HAS_SLACK = False

# Conditional imports for Discord
try:
    import discord
    from discord import app_commands
    HAS_DISCORD = True
except ImportError:
    HAS_DISCORD = False

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

AGENT_DECK_DIR = Path.home() / ".agent-deck"
CONFIG_PATH = AGENT_DECK_DIR / "config.toml"
CONDUCTOR_DIR = AGENT_DECK_DIR / "conductor"
LOG_PATH = CONDUCTOR_DIR / "bridge.log"

# Telegram message length limit
TG_MAX_LENGTH = 4096

# Slack message length limit
SLACK_MAX_LENGTH = 40000

# Discord message length limit
DISCORD_MAX_LENGTH = 2000

# Marker for uploading local images through the Discord bridge.
IMAGE_MARKER_RE = re.compile(r"\[IMAGE:(?P<path>[^\]]+)\]")

# How long to wait for conductor to respond (seconds)
RESPONSE_TIMEOUT = 300

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    handlers=[
        logging.FileHandler(LOG_PATH, encoding="utf-8"),
        logging.StreamHandler(sys.stdout),
    ],
)
log = logging.getLogger("conductor-bridge")


# ---------------------------------------------------------------------------
# Config loading
# ---------------------------------------------------------------------------


def _resolve_secret(value: str) -> str:
    """Resolve a config value that may be an env-var reference or a macOS Keychain reference.

    Supports:
      - "$ENV_VAR" or "${ENV_VAR}" -> os.environ lookup
      - "keychain:service-name" -> macOS Keychain lookup via /usr/bin/security
      - Plain strings are returned as-is.
    """
    if not value:
        return value
    if value.startswith("$"):
        # Strip ${...} or $... syntax
        var_name = value.lstrip("$").strip("{}")
        resolved = os.environ.get(var_name, "")
        if not resolved:
            log.warning("Environment variable %s is not set", var_name)
        return resolved
    if value.startswith("keychain:"):
        service_name = value[len("keychain:"):]
        try:
            import subprocess
            result = subprocess.run(
                ["/usr/bin/security", "find-generic-password", "-s", service_name, "-w"],
                capture_output=True, text=True, timeout=5,
            )
            if result.returncode == 0:
                return result.stdout.strip()
            log.warning("Keychain lookup failed for service '%s': %s", service_name, result.stderr.strip())
        except Exception as e:
            log.warning("Keychain lookup error for service '%s': %s", service_name, e)
        return ""
    return value


def load_config() -> dict:
    """Load [conductor] section from config.toml.

    Returns a dict with nested 'telegram' and 'slack' sub-dicts,
    each with a 'configured' flag.
    """
    if not CONFIG_PATH.exists():
        log.error("Config not found: %s", CONFIG_PATH)
        sys.exit(1)

    config = toml.load(CONFIG_PATH)
    conductor_cfg = config.get("conductor", {})

    if not conductor_cfg.get("enabled", False):
        log.error("[conductor] section missing or not enabled in config.toml")
        sys.exit(1)

    # Telegram config
    tg = conductor_cfg.get("telegram", {})
    tg_token = _resolve_secret(tg.get("token", ""))
    tg_user_id = tg.get("user_id", 0)
    tg_configured = bool(tg_token and tg_user_id)

    # Slack config
    sl = conductor_cfg.get("slack", {})
    sl_bot_token = _resolve_secret(sl.get("bot_token", ""))
    sl_app_token = _resolve_secret(sl.get("app_token", ""))
    sl_channel_id = sl.get("channel_id", "")
    sl_listen_mode = sl.get("listen_mode", "mentions")  # "mentions" or "all"
    sl_allowed_users = sl.get("allowed_user_ids", [])  # List of authorized Slack user IDs
    sl_configured = bool(sl_bot_token and sl_app_token and sl_channel_id)

    # Discord config
    dc = conductor_cfg.get("discord", {})
    dc_bot_token = _resolve_secret(dc.get("bot_token", ""))
    dc_guild_id = dc.get("guild_id", 0)
    dc_channel_id = dc.get("channel_id", 0)
    dc_user_id = dc.get("user_id", 0)
    dc_listen_mode = dc.get("listen_mode", "all")  # "mentions" or "all"
    dc_ignore_replies_to_others = dc.get("ignore_replies_to_others", False)
    dc_configured = bool(dc_bot_token and dc_guild_id and dc_channel_id and dc_user_id)

    if not tg_configured and not sl_configured and not dc_configured:
        log.error(
            "No messaging platform configured in config.toml. "
            "Set [conductor.telegram], [conductor.slack], or [conductor.discord]."
        )
        sys.exit(1)

    return {
        "telegram": {
            "token": tg_token,
            "user_id": int(tg_user_id) if tg_user_id else 0,
            "configured": tg_configured,
        },
        "slack": {
            "bot_token": sl_bot_token,
            "app_token": sl_app_token,
            "channel_id": sl_channel_id,
            "listen_mode": sl_listen_mode,
            "allowed_user_ids": sl_allowed_users,
            "configured": sl_configured,
        },
        "discord": {
            "bot_token": dc_bot_token,
            "guild_id": int(dc_guild_id) if dc_guild_id else 0,
            "channel_id": int(dc_channel_id) if dc_channel_id else 0,
            "user_id": int(dc_user_id) if dc_user_id else 0,
            "listen_mode": dc_listen_mode,
            "ignore_replies_to_others": bool(dc_ignore_replies_to_others),
            "configured": dc_configured,
        },
        "heartbeat_interval": conductor_cfg.get("heartbeat_interval", 15),
    }


def discover_conductors() -> list[dict]:
    """Discover all conductors by scanning meta.json files."""
    conductors = []
    if not CONDUCTOR_DIR.exists():
        return conductors
    for entry in CONDUCTOR_DIR.iterdir():
        if entry.is_dir():
            meta_path = entry / "meta.json"
            if meta_path.exists():
                try:
                    with open(meta_path) as f:
                        meta = json.load(f)
                    if not isinstance(meta, dict):
                        continue
                    # Backward compatibility: normalize missing fields.
                    meta["name"] = meta.get("name") or entry.name
                    meta["profile"] = meta.get("profile") or "default"
                    conductors.append(meta)
                except (json.JSONDecodeError, IOError) as e:
                    log.warning("Failed to read %s: %s", meta_path, e)
    return conductors


def conductor_session_title(name: str) -> str:
    """Return the conductor session title for a given conductor name."""
    return f"conductor-{name}"


def get_conductor_names() -> list[str]:
    """Get list of all conductor names."""
    return [c["name"] for c in discover_conductors()]


def get_unique_profiles() -> list[str]:
    """Get unique profile names from all conductors."""
    profiles = set()
    for c in discover_conductors():
        profiles.add(c.get("profile") or "default")
    return sorted(profiles)


def select_heartbeat_conductors(conductors: list[dict]) -> list[dict]:
    """Select all heartbeat-enabled conductors in deterministic order."""
    enabled = [c for c in conductors if c.get("heartbeat_enabled", True)]
    return sorted(
        enabled,
        key=lambda c: (
            str(c.get("profile") or "default"),
            str(c.get("created_at", "")),
            str(c.get("name", "")),
        ),
    )


# ---------------------------------------------------------------------------
# Agent-Deck CLI helpers
# ---------------------------------------------------------------------------


def run_cli(
    *args: str, profile: str | None = None, timeout: int = 120
) -> subprocess.CompletedProcess:
    """Run an agent-deck CLI command and return the result.

    If profile is provided, prepends -p <profile> to the command.
    """
    cmd = ["agent-deck"]
    if profile:
        cmd += ["-p", profile]
    cmd += list(args)
    log.debug("CLI: %s", " ".join(cmd))
    try:
        result = subprocess.run(
            cmd, capture_output=True, text=True, timeout=timeout,
            errors="replace",
        )
        return result
    except subprocess.TimeoutExpired:
        log.warning("CLI timeout: %s", " ".join(cmd))
        return subprocess.CompletedProcess(cmd, 1, "", "timeout")
    except FileNotFoundError:
        log.error("agent-deck not found in PATH")
        return subprocess.CompletedProcess(cmd, 1, "", "not found")


def get_session_status(session: str, profile: str | None = None) -> str:
    """Get the status of a session (running/waiting/idle/error/stopped)."""
    result = run_cli(
        "session", "show", "--json", session, profile=profile, timeout=30
    )
    if result.returncode != 0:
        return "error"
    try:
        data = json.loads(result.stdout)
        return data.get("status", "error")
    except (json.JSONDecodeError, KeyError):
        return "error"


def get_session_output(session: str, profile: str | None = None) -> str:
    """Get the last response from a session.

    Uses --json mode so we get the structured 'content' field (the actual
    assistant reply) instead of the raw pane capture (which includes the
    cosmetic frame / statusline at the top and can be mistaken for a reply).
    """
    result = run_cli(
        "session", "output", session, "--json", profile=profile, timeout=30
    )
    if result.returncode != 0:
        return f"[Error getting output: {result.stderr.strip()}]"
    try:
        data = json.loads(result.stdout)
        return (data.get("content") or "").strip()
    except json.JSONDecodeError:
        # Fallback: stdout might be the legacy raw-text format.
        return result.stdout.strip()


def send_to_conductor(
    session: str,
    message: str,
    profile: str | None = None,
    wait_for_reply: bool = False,
    response_timeout: int = RESPONSE_TIMEOUT,
) -> tuple[bool, str]:
    """Send a message to the conductor session.

    Returns (success, response_text). When wait_for_reply=False, response_text is "".
    """
    if wait_for_reply:
        # ` + "`" + `session send --wait` + "`" + ` returns only the send-confirmation as JSON;
        # the response itself is printed as a raw pane capture afterward.
        # We ignore that stdout entirely and re-fetch via ` + "`" + `session output
        # --json` + "`" + `, which DOES return a clean { content, role, ... } object.
        # ` + "`" + `--wait` + "`" + ` blocks until the assistant's JSONL entry is flushed, so
        # the subsequent output call sees the new reply.
        result = run_cli(
            "session", "send", session, message,
            "--wait", "--timeout", f"{response_timeout}s", "-q",
            profile=profile,
            timeout=max(response_timeout+30, 60),
        )
    else:
        result = run_cli(
            "session", "send", session, message, "--no-wait",
            profile=profile, timeout=30,
        )
    if result.returncode != 0:
        log.error(
            "Failed to send to conductor: %s", result.stderr.strip()
        )
        return False, ""
    if wait_for_reply:
        # Now fetch the clean assistant reply.
        return True, get_session_output(session, profile=profile)
    return True, result.stdout.strip()


def get_status_summary(profile: str | None = None) -> dict:
    """Get agent-deck status as a dict for a single profile."""
    result = run_cli("status", "--json", profile=profile, timeout=30)
    if result.returncode != 0:
        return {"waiting": 0, "running": 0, "idle": 0, "error": 0, "stopped": 0, "total": 0}
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError:
        return {"waiting": 0, "running": 0, "idle": 0, "error": 0, "stopped": 0, "total": 0}


def get_status_summary_all(profiles: list[str]) -> dict:
    """Aggregate status across all profiles."""
    totals = {"waiting": 0, "running": 0, "idle": 0, "error": 0, "stopped": 0, "total": 0}
    per_profile = {}
    for profile in profiles:
        summary = get_status_summary(profile)
        per_profile[profile] = summary
        for key in totals:
            totals[key] += summary.get(key, 0)
    return {"totals": totals, "per_profile": per_profile}


def get_sessions_list(profile: str | None = None) -> list:
    """Get list of all sessions for a single profile."""
    result = run_cli("list", "--json", profile=profile, timeout=30)
    if result.returncode != 0:
        return []
    try:
        data = json.loads(result.stdout)
        # list --json returns {"sessions": [...]}
        if isinstance(data, dict):
            return data.get("sessions", [])
        return data if isinstance(data, list) else []
    except json.JSONDecodeError:
        return []


def get_sessions_list_all(profiles: list[str]) -> list[tuple[str, dict]]:
    """Get sessions from all profiles, each tagged with profile name."""
    all_sessions = []
    for profile in profiles:
        sessions = get_sessions_list(profile)
        for s in sessions:
            all_sessions.append((profile, s))
    return all_sessions


def ensure_conductor_running(name: str, profile: str) -> bool:
    """Ensure the conductor session exists and is running."""
    profile = profile or "default"
    session_title = conductor_session_title(name)
    status = get_session_status(session_title, profile=profile)

    if status == "error":
        log.info(
            "Conductor %s not running, attempting to start...", name,
        )
        # Try starting first (session might exist but be stopped)
        result = run_cli(
            "session", "start", session_title, profile=profile, timeout=60
        )
        if result.returncode != 0:
            # Session might not exist, try creating it
            log.info("Creating conductor session for %s...", name)
            session_path = str(CONDUCTOR_DIR / name)
            result = run_cli(
                "add", session_path,
                "-t", session_title,
                "-c", "claude",
                "-g", "conductor",
                profile=profile,
                timeout=60,
            )
            if result.returncode != 0:
                log.error(
                    "Failed to create conductor %s: %s",
                    name,
                    result.stderr.strip(),
                )
                return False
            # Start the newly created session
            run_cli(
                "session", "start", session_title,
                profile=profile, timeout=60,
            )

        # Wait a moment for the session to initialize
        time.sleep(5)
        return (
            get_session_status(session_title, profile=profile) != "error"
        )

    return True


# ---------------------------------------------------------------------------
# Message routing
# ---------------------------------------------------------------------------


def parse_conductor_prefix(text: str, conductor_names: list[str]) -> tuple[str | None, str]:
    """Parse conductor name prefix from user message.

    Supports formats:
      <name>: <message>

    Returns (name_or_None, cleaned_message).
    """
    for name in conductor_names:
        prefix = f"{name}:"
        if text.startswith(prefix):
            return name, text[len(prefix):].strip()

    return None, text


# ---------------------------------------------------------------------------
# Message splitting
# ---------------------------------------------------------------------------


def split_message(text: str, max_len: int = TG_MAX_LENGTH) -> list[str]:
    """Split a long message into chunks that fit the platform limit."""
    if len(text) <= max_len:
        return [text]

    chunks = []
    while text:
        if len(text) <= max_len:
            chunks.append(text)
            break
        # Try to split at a newline
        split_at = text.rfind("\n", 0, max_len)
        if split_at == -1:
            # No newline found, split at max_len
            split_at = max_len
        chunks.append(text[:split_at])
        text = text[split_at:].lstrip("\n")
    return chunks


def md_to_tg_html(text: str) -> str:
    """Convert markdown bold/italic/code to Telegram HTML and escape unsafe chars.

    Processes code spans first to protect their content from bold/italic conversion.
    """
    import html as _html

    # 1. Extract code spans before escaping (protect their content)
    code_spans: list[str] = []

    def _save_code(m: re.Match) -> str:
        code_spans.append(m.group(1))
        return f"\x00CODE{len(code_spans) - 1}\x00"

    text = re.sub(r'` + "`" + `(.+?)` + "`" + `', _save_code, text)

    # 2. Escape HTML special chars
    text = _html.escape(text, quote=False)

    # 3. Convert bold/italic (code spans are already replaced with placeholders)
    text = re.sub(r'\*\*(.+?)\*\*', r'<b>\1</b>', text)
    text = re.sub(r'(?<!\*)\*(?!\*)(.+?)(?<!\*)\*(?!\*)', r'<i>\1</i>', text)

    # 4. Restore code spans (escaped content wrapped in <code>)
    for i, code in enumerate(code_spans):
        text = text.replace(f"\x00CODE{i}\x00", f"<code>{_html.escape(code, quote=False)}</code>")

    return text


def parse_discord_message_parts(text: str) -> list[tuple[str, str]]:
    """Split Discord output into plain-text and image-upload segments."""
    parts = []
    last_idx = 0

    for match in IMAGE_MARKER_RE.finditer(text):
        if match.start() > last_idx:
            parts.append(("text", text[last_idx:match.start()]))

        image_path = match.group("path").strip()
        if image_path:
            parts.append(("image", image_path))
        last_idx = match.end()

    if last_idx < len(text):
        parts.append(("text", text[last_idx:]))

    if not parts:
        parts.append(("text", text))

    return parts


async def send_discord_output(channel, text: str, name_tag: str = ""):
    """Send Discord output, uploading [IMAGE:/path] markers as attachments."""
    prefix = name_tag if name_tag else ""
    attachment_content = name_tag.strip() if name_tag else None

    for part_type, payload in parse_discord_message_parts(text):
        if part_type == "text":
            if not payload.strip():
                continue
            for chunk in split_message(payload, max_len=DISCORD_MAX_LENGTH):
                prefixed = f"{prefix}{chunk}" if prefix else chunk
                await channel.send(prefixed)
            continue

        image_path = Path(payload).expanduser()
        if not image_path.is_absolute():
            warning = f"[Image path must be absolute: {payload}]"
            prefixed = f"{prefix}{warning}" if prefix else warning
            await channel.send(prefixed)
            continue
        if not image_path.is_file():
            warning = f"[Image not found: {image_path}]"
            prefixed = f"{prefix}{warning}" if prefix else warning
            await channel.send(prefixed)
            continue

        try:
            await channel.send(
                content=attachment_content,
                file=discord.File(str(image_path)),
            )
        except Exception as e:
            log.error("Failed to upload Discord image %s: %s", image_path, e)
            warning = f"[Failed to upload image: {image_path}]"
            prefixed = f"{prefix}{warning}" if prefix else warning
            await channel.send(prefixed)


# ---------------------------------------------------------------------------
# Telegram bot setup
# ---------------------------------------------------------------------------


def create_telegram_bot(config: dict):
    """Create and configure the Telegram bot.

    Returns (bot, dp) or None if Telegram is not configured or aiogram is not available.
    """
    if not HAS_AIOGRAM:
        log.warning("aiogram not installed, skipping Telegram bot")
        return None
    if not config["telegram"]["configured"]:
        return None

    bot = Bot(token=config["telegram"]["token"])
    dp = Dispatcher()
    authorized_user = config["telegram"]["user_id"]
    bot_info = {"username": ""}

    async def ensure_bot_info(bot_instance: Bot):
        """Lazy-init bot username on first message."""
        if not bot_info["username"]:
            me = await bot_instance.get_me()
            bot_info["username"] = me.username.lower()
            log.info("Bot username: @%s", bot_info["username"])

    def is_authorized(message: types.Message) -> bool:
        """Check if message is from the authorized user."""
        if message.from_user.id != authorized_user:
            log.warning(
                "Unauthorized message from user %d", message.from_user.id
            )
            return False
        return True

    def is_bot_addressed(message: types.Message) -> bool:
        """Check if message is directed at the bot (mention or reply in groups)."""
        if message.chat.type == "private":
            return True
        # Reply to the bot's own message
        if message.reply_to_message and message.reply_to_message.from_user:
            reply_username = message.reply_to_message.from_user.username
            if reply_username and reply_username.lower() == bot_info["username"]:
                return True
        # @mention in message entities
        if message.entities and message.text:
            for entity in message.entities:
                if entity.type == "mention":
                    mentioned = message.text[
                        entity.offset : entity.offset + entity.length
                    ].lower()
                    if mentioned == f"@{bot_info['username']}":
                        return True
        return False

    def strip_bot_mention(text: str) -> str:
        """Remove @botusername from message text."""
        if not bot_info["username"]:
            return text
        return re.sub(
            rf"@{re.escape(bot_info['username'])}\b",
            "",
            text,
            flags=re.IGNORECASE,
        ).strip()

    def get_default_conductor() -> dict | None:
        """Get the first conductor (default target for messages)."""
        conductors = discover_conductors()
        return conductors[0] if conductors else None

    @dp.message(CommandStart())
    async def cmd_start(message: types.Message):
        if not is_authorized(message):
            return
        conductors = discover_conductors()
        names = [c["name"] for c in conductors]
        default = names[0] if names else "none"
        await message.answer(
            "Conductor bridge active.\n"
            f"Conductors: {', '.join(names) if names else 'none'}\n"
            "Commands: /status /sessions /help /restart\n"
            f"Route to conductor: <name>: <message>\n"
            f"Default conductor: {default}"
        )

    @dp.message(Command("status"))
    async def cmd_status(message: types.Message):
        if not is_authorized(message):
            return
        profiles = get_unique_profiles()
        agg = get_status_summary_all(profiles)
        totals = agg["totals"]

        lines = [
            f"Total: {totals['total']} sessions",
            f"  Running: {totals['running']}",
            f"  Waiting: {totals['waiting']}",
            f"  Idle: {totals['idle']}",
            f"  Error: {totals['error']}",
        ]

        # Per-profile breakdown (only if multiple profiles)
        if len(profiles) > 1:
            lines.append("")
            for profile in profiles:
                p = agg["per_profile"][profile]
                lines.append(
                    f"[{profile}] {p['total']}s "
                    f"({p['running']}R {p['waiting']}W {p['idle']}I {p['error']}E)"
                )

        await message.answer("\n".join(lines))

    @dp.message(Command("sessions"))
    async def cmd_sessions(message: types.Message):
        if not is_authorized(message):
            return
        profiles = get_unique_profiles()
        all_sessions = get_sessions_list_all(profiles)
        if not all_sessions:
            await message.answer("No sessions found.")
            return

        STATUS_ICONS = {
            "running": "\U0001f7e2",
            "waiting": "\U0001f7e1",
            "idle": "\u26aa",
            "error": "\U0001f534",
            "stopped": "\u23f9",
        }

        lines = []
        for profile, s in all_sessions:
            icon = STATUS_ICONS.get(s.get("status", ""), "\u2753")
            title = s.get("title", "untitled")
            tool = s.get("tool", "")
            prefix = f"[{profile}] " if len(profiles) > 1 else ""
            lines.append(f"{icon} {prefix}{title} ({tool})")

        await message.answer("\n".join(lines))

    @dp.message(Command("help"))
    async def cmd_help(message: types.Message):
        if not is_authorized(message):
            return
        conductors = discover_conductors()
        names = [c["name"] for c in conductors]
        await message.answer(
            "Conductor Commands:\n"
            "/status    - Aggregated status across all profiles\n"
            "/sessions  - List all sessions (all profiles)\n"
            "/restart   - Restart a conductor (specify name)\n"
            "/help      - This message\n\n"
            f"Conductors: {', '.join(names) if names else 'none'}\n"
            f"Route: <name>: <message>\n"
            f"Default: messages go to first conductor"
        )

    @dp.message(Command("restart"))
    async def cmd_restart(message: types.Message):
        if not is_authorized(message):
            return

        # Parse optional conductor name: /restart ryan
        text = message.text.strip()
        parts = text.split(None, 1)
        conductor_names = get_conductor_names()

        target = None
        if len(parts) > 1 and parts[1] in conductor_names:
            for c in discover_conductors():
                if c["name"] == parts[1]:
                    target = c
                    break
        if target is None:
            target = get_default_conductor()

        if target is None:
            await message.answer("No conductors found.")
            return

        session_title = conductor_session_title(target["name"])
        await message.answer(
            f"Restarting conductor {target['name']}..."
        )
        result = run_cli(
            "session", "restart", session_title,
            profile=target["profile"], timeout=60,
        )
        if result.returncode == 0:
            await message.answer(
                f"Conductor {target['name']} restarted."
            )
        else:
            await message.answer(
                f"Restart failed: {result.stderr.strip()}"
            )

    @dp.message()
    async def handle_message(message: types.Message):
        """Forward any text message to the conductor and return its response."""
        if not is_authorized(message):
            return
        if not message.text:
            return
        await ensure_bot_info(message.bot)
        if not is_bot_addressed(message):
            return

        # Strip @botname mention from group messages
        text = strip_bot_mention(message.text)
        if not text:
            return

        conductor_names = get_conductor_names()
        conductors = discover_conductors()

        # Determine target conductor from message prefix
        target_name, cleaned_msg = parse_conductor_prefix(
            text, conductor_names
        )

        target = None
        if target_name:
            for c in conductors:
                if c["name"] == target_name:
                    target = c
                    break
        if target is None:
            target = get_default_conductor()
        if target is None:
            await message.answer("[No conductors configured. Run: agent-deck conductor setup <name>]")
            return

        if not cleaned_msg:
            cleaned_msg = text

        session_title = conductor_session_title(target["name"])
        profile = target["profile"]

        # Ensure conductor is running
        if not ensure_conductor_running(target["name"], profile):
            await message.answer(
                f"[Could not start conductor {target['name']}. Check agent-deck.]"
            )
            return

        # Send to conductor
        log.info(
            "User message -> [%s]: %s", target["name"], cleaned_msg[:100]
        )
        ok, response = send_to_conductor(
            session_title,
            cleaned_msg,
            profile=profile,
            wait_for_reply=True,
            response_timeout=RESPONSE_TIMEOUT,
        )
        if not ok:
            await message.answer(
                f"[Failed to send message to conductor {target['name']}.]"
            )
            return

        # Response is returned directly by session send --wait.
        name_tag = (
            f"[{target['name']}] " if len(conductors) > 1 else ""
        )
        await message.answer(f"{name_tag}...")  # typing indicator
        log.info("Conductor [%s] response: %s", target["name"], response[:100])

        # Convert to HTML first, then split to respect post-conversion length
        html_response = md_to_tg_html(
            f"{name_tag}{response}" if name_tag else response
        )
        for chunk in split_message(html_response):
            await message.answer(chunk, parse_mode="HTML")

    return bot, dp


# ---------------------------------------------------------------------------
# Slack app setup
# ---------------------------------------------------------------------------


def create_slack_app(config: dict):
    """Create and configure the Slack app with Socket Mode.

    Returns (app, channel_id) or None if Slack is not configured or slack-bolt is not available.
    """
    if not HAS_SLACK:
        log.warning("slack-bolt not installed, skipping Slack app")
        return None
    if not config["slack"]["configured"]:
        return None

    bot_token = config["slack"]["bot_token"]
    channel_id = config["slack"]["channel_id"]

    # Cache auth.test() result to avoid calling it on every event.
    # The default SingleTeamAuthorization middleware calls auth.test()
    # per-event until it succeeds; if the Slack API is slow after a
    # Socket Mode reconnect, this causes cascading TimeoutErrors.
    _auth_cache: dict = {}
    _auth_lock = asyncio.Lock()

    async def _cached_authorize(**kwargs):
        async with _auth_lock:
            if "result" in _auth_cache:
                return _auth_cache["result"]
            client = AsyncWebClient(token=bot_token, timeout=30)
            for attempt in range(3):
                try:
                    resp = await client.auth_test()
                    _auth_cache["result"] = AuthorizeResult(
                        enterprise_id=resp.get("enterprise_id"),
                        team_id=resp.get("team_id"),
                        bot_user_id=resp.get("user_id"),
                        bot_id=resp.get("bot_id"),
                        bot_token=bot_token,
                    )
                    return _auth_cache["result"]
                except Exception as e:
                    log.warning("Slack auth.test attempt %d/3 failed: %s", attempt + 1, e)
                    if attempt < 2:
                        await asyncio.sleep(2 ** attempt)
            raise RuntimeError("Slack auth.test failed after 3 attempts")

    app = AsyncApp(token=bot_token, authorize=_cached_authorize)
    listen_mode = config["slack"].get("listen_mode", "mentions")

    # Authorization setup
    allowed_users = config["slack"]["allowed_user_ids"]

    def is_slack_authorized(user_id: str) -> bool:
        """Check if Slack user is authorized to use the bot.

        If allowed_user_ids is empty, allow all users (backward compatible).
        Otherwise, only allow users in the list.
        """
        if not allowed_users:  # Empty list = no restrictions
            return True
        if user_id not in allowed_users:
            log.warning("Unauthorized Slack message from user %s", user_id)
            return False
        return True

    # Caches for Slack user/channel name resolution.
    # Entries: (value: str, expires_at: float | None).
    # Successful lookups never expire; failures expire after 5 minutes.
    _NEGATIVE_TTL = 300  # seconds
    _user_cache: dict[str, tuple[str, float | None]] = {}
    _channel_cache: dict[str, tuple[str, float | None]] = {}

    def _cache_get(cache: dict, key: str) -> str | None:
        entry = cache.get(key)
        if entry is None:
            return None
        value, expires_at = entry
        if expires_at is not None and time.monotonic() > expires_at:
            del cache[key]
            return None
        return value

    async def resolve_slack_username(user_id: str) -> str:
        """Resolve a Slack user ID to a display name, with caching."""
        cached = _cache_get(_user_cache, user_id)
        if cached is not None:
            return cached
        try:
            resp = await app.client.users_info(user=user_id)
            profile = resp["user"]["profile"]
            name = profile.get("display_name") or profile.get("real_name") or user_id
            _user_cache[user_id] = (name, None)
            return name
        except Exception as e:
            log.warning("Failed to resolve Slack user %s: %s", user_id, e)
            _user_cache[user_id] = (user_id, time.monotonic() + _NEGATIVE_TTL)
            return user_id

    async def resolve_slack_channel(event_channel: str) -> str:
        """Resolve a Slack channel ID to a context tag.

        Returns '[channel:#name (ID)]' for channels or '[dm]' for DMs.
        """
        cached = _cache_get(_channel_cache, event_channel)
        if cached is not None:
            return cached
        try:
            resp = await app.client.conversations_info(channel=event_channel)
            ch = resp["channel"]
            if ch.get("is_im"):
                tag = "[dm]"
            else:
                name = ch.get("name", event_channel)
                tag = f"[channel:#{name} ({event_channel})]"
            _channel_cache[event_channel] = (tag, None)
            return tag
        except Exception as e:
            log.warning("Failed to resolve Slack channel %s: %s", event_channel, e)
            tag = f"[channel:{event_channel}]"
            _channel_cache[event_channel] = (tag, time.monotonic() + _NEGATIVE_TTL)
            return tag

    def get_default_conductor() -> dict | None:
        """Get the first conductor (default target for messages)."""
        conductors = discover_conductors()
        return conductors[0] if conductors else None

    def _markdown_to_slack(text: str) -> str:
        """Convert GitHub-flavored markdown to Slack mrkdwn format.

        Preserves code blocks and inline code. Converts:
        - Headers (# H1 ... ###### H6) -> *bold text*
        - Bold (**text**) -> *text*
        - Strikethrough (~~text~~) -> ~text~
        - Links [text](url) -> <url|text>
        - Bullet lists (- item, * item) -> bullet_char item
        """
        # Protect code blocks: extract fenced blocks, replace with placeholders.
        code_blocks = []
        def _save_code_block(m):
            code_blocks.append(m.group(0))
            return f"__CODE_BLOCK_{len(code_blocks) - 1}__"
        text = re.sub(r"` + "```" + `[\s\S]*?` + "```" + `", _save_code_block, text)

        # Protect inline code.
        inline_codes = []
        def _save_inline_code(m):
            inline_codes.append(m.group(0))
            return f"__INLINE_CODE_{len(inline_codes) - 1}__"
        text = re.sub(r"` + "`" + `[^` + "`" + `\n]+` + "`" + `", _save_inline_code, text)

        # Headers -> bold
        text = re.sub(r"^#{1,6}\s+(.+)$", r"*\1*", text, flags=re.MULTILINE)
        # Bold **text** -> *text*  (must come after headers to avoid double-wrapping)
        text = re.sub(r"\*\*(.+?)\*\*", r"*\1*", text)
        # Strikethrough ~~text~~ -> ~text~
        text = re.sub(r"~~(.+?)~~", r"~\1~", text)
        # Links [text](url) -> <url|text>
        text = re.sub(r"\[([^\]]+)\]\(([^)]+)\)", r"<\2|\1>", text)
        # Bullet lists: - item or * item -> bullet char item
        text = re.sub(r"^(\s*)[-*]\s+", "\\1\u2022 ", text, flags=re.MULTILINE)

        # Restore inline code.
        for i, code in enumerate(inline_codes):
            text = text.replace(f"__INLINE_CODE_{i}__", code)
        # Restore code blocks.
        for i, block in enumerate(code_blocks):
            text = text.replace(f"__CODE_BLOCK_{i}__", block)

        return text

    async def _safe_say(say, **kwargs):
        """Wrapper around say() that catches network/API errors and converts markdown."""
        if "text" in kwargs:
            kwargs["text"] = _markdown_to_slack(kwargs["text"])
        try:
            await say(**kwargs)
        except Exception as e:
            log.error("Slack say() failed: %s", e)

    async def _handle_slack_text(
        text: str, say, thread_ts: str = None,
        user_id: str = None, event_channel: str = None,
    ):
        """Shared handler for Slack messages and mentions."""
        conductor_names = get_conductor_names()
        conductors = discover_conductors()

        target_name, cleaned_msg = parse_conductor_prefix(text, conductor_names)

        target = None
        if target_name:
            for c in conductors:
                if c["name"] == target_name:
                    target = c
                    break
        if target is None:
            target = get_default_conductor()
        if target is None:
            await _safe_say(
                say,
                text="[No conductors configured. Run: agent-deck conductor setup <name>]",
                thread_ts=thread_ts,
            )
            return

        if not cleaned_msg:
            cleaned_msg = text

        # Enrich message with sender and channel context for the conductor.
        prefix_parts = []
        if user_id and event_channel:
            username, channel_tag = await asyncio.gather(
                resolve_slack_username(user_id),
                resolve_slack_channel(event_channel),
            )
            prefix_parts.append(f"[from:{username} ({user_id})]")
            prefix_parts.append(channel_tag)
        elif user_id:
            username = await resolve_slack_username(user_id)
            prefix_parts.append(f"[from:{username} ({user_id})]")
        elif event_channel:
            channel_tag = await resolve_slack_channel(event_channel)
            prefix_parts.append(channel_tag)
        if prefix_parts:
            cleaned_msg = " ".join(prefix_parts) + " " + cleaned_msg

        session_title = conductor_session_title(target["name"])
        profile = target["profile"]

        if not ensure_conductor_running(target["name"], profile):
            await _safe_say(
                say,
                text=f"[Could not start conductor {target['name']}. Check agent-deck.]",
                thread_ts=thread_ts,
            )
            return

        log.info("Slack message -> [%s]: %s", target["name"], cleaned_msg[:100])
        ok, response = send_to_conductor(
            session_title,
            cleaned_msg,
            profile=profile,
            wait_for_reply=True,
            response_timeout=RESPONSE_TIMEOUT,
        )
        if not ok:
            await _safe_say(
                say,
                text=f"[Failed to send message to conductor {target['name']}.]",
                thread_ts=thread_ts,
            )
            return

        name_tag = f"[{target['name']}] " if len(conductors) > 1 else ""
        await _safe_say(say, text=f"{name_tag}...", thread_ts=thread_ts)

        # Response is returned directly by session send --wait.
        log.info("Conductor [%s] response: %s", target["name"], response[:100])

        for chunk in split_message(response, max_len=SLACK_MAX_LENGTH):
            prefixed = f"{name_tag}{chunk}" if name_tag else chunk
            await _safe_say(say, text=prefixed, thread_ts=thread_ts)

    @app.event("message")
    async def handle_slack_message(event, say):
        """Handle messages in the configured channel.

        Only active when listen_mode is "all". Ignored in "mentions" mode.
        """
        if listen_mode != "all":
            return
        # Ignore bot messages
        if event.get("bot_id") or event.get("subtype"):
            return
        # Only listen in configured channel
        if event.get("channel") != channel_id:
            return

        # Authorization check
        user_id = event.get("user", "")
        if not is_slack_authorized(user_id):
            return

        text = event.get("text", "").strip()
        if not text:
            return
        await _handle_slack_text(
            text, say,
            thread_ts=event.get("thread_ts") or event.get("ts"),
            user_id=user_id,
            event_channel=event.get("channel"),
        )

    @app.event("app_mention")
    async def handle_slack_mention(event, say):
        """Handle @bot mentions in any channel the bot is in. Always active."""

        # Authorization check
        user_id = event.get("user", "")
        if not is_slack_authorized(user_id):
            return

        text = event.get("text", "")
        # Strip the bot mention (e.g., "<@U01234> message" -> "message")
        text = re.sub(r"<@[A-Z0-9]+>\s*", "", text).strip()
        if not text:
            return
        thread_ts = event.get("thread_ts") or event.get("ts")
        await _handle_slack_text(
            text, say,
            thread_ts=thread_ts,
            user_id=user_id,
            event_channel=event.get("channel"),
        )

    @app.command("/ad-status")
    async def slack_cmd_status(ack, respond, command):
        """Handle /ad-status slash command."""
        await ack()

        # Authorization check
        user_id = command.get("user_id", "")
        if not is_slack_authorized(user_id):
            await respond("⛔ Unauthorized. Contact your administrator.")
            return

        profiles = get_unique_profiles()
        agg = get_status_summary_all(profiles)
        totals = agg["totals"]

        lines = [
            f"Total: {totals['total']} sessions",
            f"  Running: {totals['running']}",
            f"  Waiting: {totals['waiting']}",
            f"  Idle: {totals['idle']}",
            f"  Error: {totals['error']}",
        ]

        if len(profiles) > 1:
            lines.append("")
            for profile in profiles:
                p = agg["per_profile"][profile]
                lines.append(
                    f"[{profile}] {p['total']}s "
                    f"({p['running']}R {p['waiting']}W {p['idle']}I {p['error']}E)"
                )

        await respond("\n".join(lines))

    @app.command("/ad-sessions")
    async def slack_cmd_sessions(ack, respond, command):
        """Handle /ad-sessions slash command."""
        await ack()

        # Authorization check
        user_id = command.get("user_id", "")
        if not is_slack_authorized(user_id):
            await respond("⛔ Unauthorized. Contact your administrator.")
            return

        profiles = get_unique_profiles()
        all_sessions = get_sessions_list_all(profiles)
        if not all_sessions:
            await respond("No sessions found.")
            return

        lines = []
        for profile, s in all_sessions:
            title = s.get("title", "untitled")
            status = s.get("status", "unknown")
            tool = s.get("tool", "")
            prefix = f"[{profile}] " if len(profiles) > 1 else ""
            lines.append(f"  {prefix}{title} ({tool}) - {status}")

        await respond("\n".join(lines))

    @app.command("/ad-restart")
    async def slack_cmd_restart(ack, respond, command):
        """Handle /ad-restart slash command."""
        await ack()

        # Authorization check
        user_id = command.get("user_id", "")
        if not is_slack_authorized(user_id):
            await respond("⛔ Unauthorized. Contact your administrator.")
            return

        target_name = command.get("text", "").strip()
        conductor_names = get_conductor_names()

        target = None
        if target_name and target_name in conductor_names:
            for c in discover_conductors():
                if c["name"] == target_name:
                    target = c
                    break
        if target is None:
            target = get_default_conductor()

        if target is None:
            await respond("No conductors found.")
            return

        session_title = conductor_session_title(target["name"])
        await respond(f"Restarting conductor {target['name']}...")
        result = run_cli(
            "session", "restart", session_title,
            profile=target["profile"], timeout=60,
        )
        if result.returncode == 0:
            await respond(f"Conductor {target['name']} restarted.")
        else:
            await respond(f"Restart failed: {result.stderr.strip()}")

    @app.command("/ad-help")
    async def slack_cmd_help(ack, respond, command):
        """Handle /ad-help slash command."""
        await ack()

        # Authorization check
        user_id = command.get("user_id", "")
        if not is_slack_authorized(user_id):
            await respond("⛔ Unauthorized. Contact your administrator.")
            return

        conductors = discover_conductors()
        names = [c["name"] for c in conductors]
        await respond(
            "Conductor Commands:\n"
            "/ad-status    - Aggregated status across all profiles\n"
            "/ad-sessions  - List all sessions (all profiles)\n"
            "/ad-restart   - Restart a conductor (specify name)\n"
            "/ad-help      - This message\n\n"
            f"Conductors: {', '.join(names) if names else 'none'}\n"
            f"Route: <name>: <message>\n"
            f"Default: messages go to first conductor"
        )

    log.info("Slack app initialized (Socket Mode, channel=%s)", channel_id)
    return app, channel_id


# ---------------------------------------------------------------------------
# Discord bot setup
# ---------------------------------------------------------------------------


def create_discord_bot(config: dict):
    """Create and configure the Discord bot.

    Returns (client, channel_id) or None if Discord is not configured or discord.py unavailable.
    """
    if not HAS_DISCORD:
        log.warning("discord.py not installed, skipping Discord bot")
        return None
    if not config["discord"]["configured"]:
        return None

    bot_token = config["discord"]["bot_token"]
    guild_id = config["discord"]["guild_id"]
    channel_id = config["discord"]["channel_id"]
    authorized_user = config["discord"]["user_id"]
    listen_mode = str(config["discord"].get("listen_mode", "all") or "all").strip().lower()
    ignore_replies_to_others = bool(
        config["discord"].get("ignore_replies_to_others", False)
    )

    if listen_mode not in {"all", "mentions"}:
        log.warning("Unknown Discord listen_mode %r, falling back to 'all'", listen_mode)
        listen_mode = "all"

    intents = discord.Intents.default()
    intents.message_content = True

    class ConductorBot(discord.Client):
        def __init__(self):
            super().__init__(intents=intents)
            self.tree = app_commands.CommandTree(self)
            self.target_channel_id = channel_id
            self.authorized_user_id = authorized_user

        async def setup_hook(self):
            g = discord.Object(id=guild_id)
            self.tree.copy_global_to(guild=g)
            await self.tree.sync(guild=g)
            log.info("Discord slash commands synced to guild %d", guild_id)

        async def on_ready(self):
            log.info(
                "Discord bot ready: %s (id=%d)", self.user, self.user.id
            )

    bot = ConductorBot()

    def is_authorized(user_id: int) -> bool:
        return user_id == authorized_user

    def message_mentions_bot(message: discord.Message) -> bool:
        if not bot.user:
            return False
        return any(getattr(user, "id", 0) == bot.user.id for user in message.mentions)

    def strip_bot_mentions(text: str) -> str:
        if not bot.user:
            return text.strip()
        return re.sub(rf"<@!?{bot.user.id}>", "", text).strip()

    async def should_ignore_reply_to_other(message: discord.Message) -> bool:
        if not ignore_replies_to_others:
            return False

        reference = getattr(message, "reference", None)
        reference_id = getattr(reference, "message_id", None)
        if not reference_id:
            return False

        referenced = getattr(reference, "resolved", None)
        if not isinstance(referenced, discord.Message):
            try:
                referenced = await message.channel.fetch_message(reference_id)
            except Exception as e:
                log.warning(
                    "Failed to resolve Discord reply target %d: %s",
                    reference_id, e,
                )
                return False

        if not bot.user:
            return False

        if referenced.author.id != bot.user.id:
            log.info(
                "Ignoring Discord reply to non-bot message %d from user %d",
                referenced.id, message.author.id,
            )
            return True
        return False

    async def ensure_discord_channel(interaction: discord.Interaction) -> bool:
        """Restrict slash commands to the configured channel."""
        if interaction.channel_id != channel_id:
            await interaction.response.send_message(
                "This command is only available in the configured channel.",
                ephemeral=True,
            )
            return False
        return True

    def get_default_conductor() -> dict | None:
        conductors = discover_conductors()
        return conductors[0] if conductors else None

    # Register slash commands
    g = discord.Object(id=guild_id)

    @bot.tree.command(
        name="ad-status",
        description="Aggregated status across all profiles",
        guild=g,
    )
    async def dc_cmd_status(interaction: discord.Interaction):
        if not is_authorized(interaction.user.id):
            await interaction.response.send_message(
                "Unauthorized.", ephemeral=True,
            )
            return
        if not await ensure_discord_channel(interaction):
            return

        profiles = get_unique_profiles()
        agg = get_status_summary_all(profiles)
        totals = agg["totals"]

        lines = [
            f"**Total:** {totals['total']} sessions",
            f"  Running: {totals['running']}",
            f"  Waiting: {totals['waiting']}",
            f"  Idle: {totals['idle']}",
            f"  Error: {totals['error']}",
        ]

        if len(profiles) > 1:
            lines.append("")
            for profile in profiles:
                p = agg["per_profile"][profile]
                lines.append(
                    f"[{profile}] {p['total']}s "
                    f"({p['running']}R {p['waiting']}W {p['idle']}I {p['error']}E)"
                )

        await interaction.response.send_message("\n".join(lines))

    @bot.tree.command(
        name="ad-sessions",
        description="List all sessions (all profiles)",
        guild=g,
    )
    async def dc_cmd_sessions(interaction: discord.Interaction):
        if not is_authorized(interaction.user.id):
            await interaction.response.send_message(
                "Unauthorized.", ephemeral=True,
            )
            return
        if not await ensure_discord_channel(interaction):
            return

        profiles = get_unique_profiles()
        all_sessions = get_sessions_list_all(profiles)
        if not all_sessions:
            await interaction.response.send_message("No sessions found.")
            return

        STATUS_ICONS = {
            "running": "\U0001f7e2",
            "waiting": "\U0001f7e1",
            "idle": "\u26aa",
            "error": "\U0001f534",
            "stopped": "\u23f9",
        }

        lines = []
        for profile, s in all_sessions:
            icon = STATUS_ICONS.get(s.get("status", ""), "\u2753")
            title = s.get("title", "untitled")
            tool = s.get("tool", "")
            prefix = f"[{profile}] " if len(profiles) > 1 else ""
            lines.append(f"{icon} {prefix}{title} ({tool})")

        text = "\n".join(lines)
        for i, chunk in enumerate(split_message(text, max_len=DISCORD_MAX_LENGTH)):
            if i == 0:
                await interaction.response.send_message(chunk)
            else:
                await interaction.followup.send(chunk)

    @bot.tree.command(
        name="ad-restart",
        description="Restart a conductor",
        guild=g,
    )
    @app_commands.describe(name="Conductor name (optional, defaults to first)")
    async def dc_cmd_restart(
        interaction: discord.Interaction, name: str = "",
    ):
        if not is_authorized(interaction.user.id):
            await interaction.response.send_message(
                "Unauthorized.", ephemeral=True,
            )
            return
        if not await ensure_discord_channel(interaction):
            return

        conductor_names = get_conductor_names()
        target = None
        if name and name in conductor_names:
            for c in discover_conductors():
                if c["name"] == name:
                    target = c
                    break
        if target is None:
            target = get_default_conductor()

        if target is None:
            await interaction.response.send_message("No conductors found.")
            return

        session_title = conductor_session_title(target["name"])
        await interaction.response.send_message(
            f"Restarting conductor {target['name']}...",
        )

        result = run_cli(
            "session", "restart", session_title,
            profile=target["profile"], timeout=60,
        )
        if result.returncode == 0:
            await interaction.followup.send(
                f"Conductor {target['name']} restarted.",
            )
        else:
            await interaction.followup.send(
                f"Restart failed: {result.stderr.strip()}",
            )

    @bot.tree.command(
        name="ad-help",
        description="Show conductor bridge help",
        guild=g,
    )
    async def dc_cmd_help(interaction: discord.Interaction):
        if not is_authorized(interaction.user.id):
            await interaction.response.send_message(
                "Unauthorized.", ephemeral=True,
            )
            return
        if not await ensure_discord_channel(interaction):
            return

        conductors = discover_conductors()
        names = [c["name"] for c in conductors]
        await interaction.response.send_message(
            "**Conductor Commands:**\n"
            "` + "`" + `/ad-status` + "`" + `    - Aggregated status across all profiles\n"
            "` + "`" + `/ad-sessions` + "`" + `  - List all sessions (all profiles)\n"
            "` + "`" + `/ad-restart` + "`" + `   - Restart a conductor (specify name)\n"
            "` + "`" + `/ad-help` + "`" + `      - This message\n\n"
            f"**Conductors:** {', '.join(names) if names else 'none'}\n"
            f"**Route:** ` + "`" + `<name>: <message>` + "`" + `\n"
            f"**Default:** messages go to first conductor"
        )

    @bot.event
    async def on_message(message):
        # Ignore bot's own messages
        if message.author == bot.user:
            return
        # Ignore messages from other bots
        if message.author.bot:
            return
        # Only listen in the configured channel
        if message.channel.id != bot.target_channel_id:
            return
        # Authorization check
        if not is_authorized(message.author.id):
            log.warning(
                "Unauthorized Discord message from user %d",
                message.author.id,
            )
            return
        if await should_ignore_reply_to_other(message):
            return
        text = message.content
        if listen_mode == "mentions":
            if not message_mentions_bot(message):
                return
            text = strip_bot_mentions(text)
        # Ignore empty messages
        if not text:
            return

        conductor_names = get_conductor_names()
        conductors = discover_conductors()

        target_name, cleaned_msg = parse_conductor_prefix(
            text, conductor_names,
        )

        target = None
        if target_name:
            for c in conductors:
                if c["name"] == target_name:
                    target = c
                    break
        if target is None:
            target = get_default_conductor()
        if target is None:
            await message.channel.send(
                "[No conductors configured. Run: agent-deck conductor setup <name>]",
            )
            return

        if not cleaned_msg:
            cleaned_msg = text

        session_title = conductor_session_title(target["name"])
        profile = target["profile"]

        if not ensure_conductor_running(target["name"], profile):
            await message.channel.send(
                f"[Could not start conductor {target['name']}. Check agent-deck.]",
            )
            return

        log.info(
            "Discord message -> [%s]: %s",
            target["name"], cleaned_msg[:100],
        )
        async with message.channel.typing():
            loop = asyncio.get_event_loop()
            ok, response = await loop.run_in_executor(
                None,
                lambda: send_to_conductor(
                    session_title,
                    cleaned_msg,
                    profile=profile,
                    wait_for_reply=True,
                    response_timeout=RESPONSE_TIMEOUT,
                ),
            )
        if not ok:
            await message.channel.send(
                f"[Failed to send message to conductor {target['name']}.]",
            )
            return

        log.info(
            "Conductor [%s] response: %s",
            target["name"], response[:100],
        )

        name_tag = (
            f"[{target['name']}] " if len(conductors) > 1 else ""
        )
        await send_discord_output(message.channel, response, name_tag=name_tag)

    log.info(
        "Discord bot initialized (guild=%d, channel=%d)",
        guild_id, channel_id,
    )
    return bot, channel_id


# ---------------------------------------------------------------------------
# Heartbeat loop
# ---------------------------------------------------------------------------


def _os_heartbeat_daemon_installed() -> bool:
    """Check if an OS-level heartbeat daemon (launchd or systemd) is installed."""
    import platform
    home = os.path.expanduser("~")
    if platform.system() == "Darwin":
        # Check for any launchd plist matching the heartbeat pattern
        agents_dir = os.path.join(home, "Library", "LaunchAgents")
        if os.path.isdir(agents_dir):
            for f in os.listdir(agents_dir):
                if f.startswith("com.agentdeck.conductor-heartbeat.") and f.endswith(".plist"):
                    return True
    else:
        # Check for any systemd timer matching the heartbeat pattern
        timers_dir = os.path.join(home, ".config", "systemd", "user")
        if os.path.isdir(timers_dir):
            for f in os.listdir(timers_dir):
                if f.startswith("agent-deck-conductor-heartbeat-") and f.endswith(".timer"):
                    return True
    return False


async def heartbeat_loop(
    config: dict, telegram_bot=None, slack_app=None, slack_channel_id=None,
    discord_bot=None, discord_channel_id=None,
):
    """Periodic heartbeat: check status for each conductor and trigger checks."""
    global_interval = config["heartbeat_interval"]
    if global_interval <= 0:
        log.info("Heartbeat disabled (interval=0)")
        return

    if _os_heartbeat_daemon_installed():
        log.info("OS heartbeat daemon detected, bridge heartbeat loop disabled (avoiding double-trigger)")
        return

    interval_seconds = global_interval * 60
    tg_user_id = config["telegram"]["user_id"] if config["telegram"]["configured"] else None

    log.info("Heartbeat loop started (global interval: %d minutes)", global_interval)

    while True:
        await asyncio.sleep(interval_seconds)

        all_conductors = discover_conductors()
        conductors = select_heartbeat_conductors(all_conductors)
        for conductor in conductors:
            try:
                name = conductor.get("name", "")
                profile = conductor.get("profile") or "default"
                if not name:
                    continue

                session_title = conductor_session_title(name)

                # Scope heartbeat monitoring to this conductor's own group.
                sessions = get_sessions_list(profile)
                scoped_sessions = []
                for s in sessions:
                    s_title = s.get("title", "untitled")
                    s_group = s.get("group", "") or ""
                    if s_title.startswith("conductor-"):
                        continue
                    if s_group != name and not s_group.startswith(f"{name}/"):
                        continue
                    scoped_sessions.append(s)

                waiting = sum(1 for s in scoped_sessions if s.get("status", "") == "waiting")
                running = sum(1 for s in scoped_sessions if s.get("status", "") == "running")
                idle = sum(1 for s in scoped_sessions if s.get("status", "") == "idle")
                error = sum(1 for s in scoped_sessions if s.get("status", "") == "error")
                stopped = sum(1 for s in scoped_sessions if s.get("status", "") == "stopped")

                log.info(
                    "Heartbeat [%s/%s]: %d waiting, %d running, %d idle, %d error, %d stopped",
                    name, profile, waiting, running, idle, error, stopped,
                )

                # Only trigger conductor if there are waiting or error sessions
                if waiting == 0 and error == 0:
                    continue

                # Build heartbeat message with waiting session details
                waiting_details = []
                error_details = []
                for s in scoped_sessions:
                    s_title = s.get("title", "untitled")
                    s_status = s.get("status", "")
                    s_path = s.get("path", "")
                    if s_status == "waiting":
                        waiting_details.append(
                            f"{s_title} (project: {s_path})"
                        )
                    elif s_status == "error":
                        error_details.append(
                            f"{s_title} (project: {s_path})"
                        )

                parts = [
                    f"[HEARTBEAT] [{name}] Status: {waiting} waiting, "
                    f"{running} running, {idle} idle, {error} error, {stopped} stopped."
                ]
                if waiting_details:
                    parts.append(
                        f"Waiting sessions: {', '.join(waiting_details)}."
                    )
                if error_details:
                    parts.append(
                        f"Error sessions: {', '.join(error_details)}."
                    )
                parts.append(
                    "Check if any need auto-response or user attention."
                )

                heartbeat_msg = " ".join(parts)

                # Ensure conductor is running
                if not ensure_conductor_running(name, profile):
                    log.error(
                        "Heartbeat [%s]: conductor not running, skipping",
                        name,
                    )
                    continue

                # Send heartbeat to conductor
                ok, response = send_to_conductor(
                    session_title,
                    heartbeat_msg,
                    profile=profile,
                    wait_for_reply=True,
                    response_timeout=RESPONSE_TIMEOUT,
                )
                if not ok:
                    log.error(
                        "Heartbeat [%s]: failed to send to conductor",
                        name,
                    )
                    continue

                # Response is returned directly by session send --wait.
                log.info(
                    "Heartbeat [%s] response: %s",
                    name, response[:200],
                )

                # If conductor flagged items needing attention, notify via Telegram and Slack
                if "NEED:" in response:
                    prefix = (
                        f"[{name}] " if len(all_conductors) > 1 else ""
                    )
                    alert_msg = f"{prefix}Conductor alert:\n{response}"

                    # Notify via Telegram
                    if telegram_bot and tg_user_id:
                        try:
                            alert_html = md_to_tg_html(alert_msg)
                            for chunk in split_message(alert_html):
                                await telegram_bot.send_message(
                                    tg_user_id, chunk, parse_mode="HTML",
                                )
                        except Exception as e:
                            log.error(
                                "Failed to send Telegram notification: %s", e
                            )

                    # Notify via Slack
                    if slack_app and slack_channel_id:
                        try:
                            await slack_app.client.chat_postMessage(
                                channel=slack_channel_id, text=alert_msg,
                            )
                        except Exception as e:
                            log.error(
                                "Failed to send Slack notification: %s", e
                            )

                    # Notify via Discord
                    if discord_bot and discord_channel_id:
                        try:
                            channel = discord_bot.get_channel(
                                discord_channel_id,
                            )
                            if channel:
                                await send_discord_output(channel, alert_msg)
                        except Exception as e:
                            log.error(
                                "Failed to send Discord notification: %s",
                                e,
                            )

            except Exception as e:
                log.error("Heartbeat [%s] error: %s", conductor.get("name", "?"), e)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


async def main():
    log.info("Loading config from %s", CONFIG_PATH)
    config = load_config()

    conductors = discover_conductors()
    conductor_names = [c["name"] for c in conductors]

    # Verify at least one integration is configured and available
    tg_ok = config["telegram"]["configured"] and HAS_AIOGRAM
    sl_ok = config["slack"]["configured"] and HAS_SLACK
    dc_ok = config["discord"]["configured"] and HAS_DISCORD

    if not tg_ok and not sl_ok and not dc_ok:
        if config["telegram"]["configured"] and not HAS_AIOGRAM:
            log.error("Telegram configured but aiogram not installed. pip install aiogram")
        if config["slack"]["configured"] and not HAS_SLACK:
            log.error("Slack configured but slack-bolt not installed. pip install slack-bolt slack-sdk")
        if config["discord"]["configured"] and not HAS_DISCORD:
            log.error("Discord configured but discord.py not installed. pip install discord.py")
        if not config["telegram"]["configured"] and not config["slack"]["configured"] and not config["discord"]["configured"]:
            log.error("No messaging platform configured. Exiting.")
        sys.exit(1)

    platforms = []
    if tg_ok:
        platforms.append("Telegram")
    if sl_ok:
        platforms.append("Slack")
    if dc_ok:
        platforms.append("Discord")

    log.info(
        "Starting conductor bridge (platforms=%s, heartbeat=%dm, conductors=%s)",
        "+".join(platforms),
        config["heartbeat_interval"],
        ", ".join(conductor_names) if conductor_names else "none",
    )

    # Create Telegram bot
    telegram_bot, telegram_dp = None, None
    if tg_ok:
        result = create_telegram_bot(config)
        if result:
            telegram_bot, telegram_dp = result
            log.info("Telegram bot initialized (user_id=%d)", config["telegram"]["user_id"])

    # Create Slack app
    slack_app, slack_handler, slack_channel_id = None, None, None
    if sl_ok:
        result = create_slack_app(config)
        if result:
            slack_app, slack_channel_id = result
            slack_handler = AsyncSocketModeHandler(slack_app, config["slack"]["app_token"])

    # Create Discord bot
    discord_bot, discord_channel_id = None, None
    if dc_ok:
        result = create_discord_bot(config)
        if result:
            discord_bot, discord_channel_id = result

    # Pre-start all conductors so they're warm when messages arrive
    for c in conductors:
        if ensure_conductor_running(c["name"], c["profile"]):
            log.info("Conductor %s is running", c["name"])
        else:
            log.warning("Failed to pre-start conductor %s", c["name"])

    # Start heartbeat (shared, notifies all platforms)
    heartbeat_task = asyncio.create_task(
        heartbeat_loop(
            config,
            telegram_bot=telegram_bot,
            slack_app=slack_app,
            slack_channel_id=slack_channel_id,
            discord_bot=discord_bot,
            discord_channel_id=discord_channel_id,
        )
    )

    # Run all platforms concurrently
    tasks = [heartbeat_task]
    if telegram_dp and telegram_bot:
        tasks.append(asyncio.create_task(telegram_dp.start_polling(telegram_bot)))
        log.info("Telegram bot polling started")
    if slack_handler:
        tasks.append(asyncio.create_task(slack_handler.start_async()))
        log.info("Slack Socket Mode handler started")
    if discord_bot:
        tasks.append(asyncio.create_task(discord_bot.start(config["discord"]["bot_token"])))
        log.info("Discord bot started")

    try:
        await asyncio.gather(*tasks)
    finally:
        heartbeat_task.cancel()
        if telegram_bot:
            await telegram_bot.session.close()
        if slack_handler:
            await slack_handler.close_async()
        if discord_bot:
            await discord_bot.close()


if __name__ == "__main__":
    asyncio.run(main())
`
