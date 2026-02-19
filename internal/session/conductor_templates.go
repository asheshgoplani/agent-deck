package session

// conductorSharedClaudeMDTemplate is the shared CLAUDE.md written to ~/.agent-deck/conductor/CLAUDE.md.
// It contains CLI reference, protocols, and formats shared by all conductors (mechanism).
// Agent behavior (rules, auto-response policy) lives in POLICY.md, not here.
// Claude Code walks up the directory tree, so per-conductor CLAUDE.md files inherit this automatically.
const conductorSharedClaudeMDTemplate = `# Conductor: Shared Knowledge Base

This file contains shared infrastructure knowledge (CLI reference, protocols, formats) for all conductor sessions.
Each conductor has its own identity in its subdirectory and its own policy in POLICY.md.

## Agent-Deck CLI Reference

### Status & Listing
| Command | Description |
|---------|-------------|
| ` + "`" + `agent-deck -p <PROFILE> status --json` + "`" + ` | Get counts: ` + "`" + `{"waiting": N, "running": N, "idle": N, "error": N, "total": N}` + "`" + ` |
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
| ` + "`" + `agent-deck -p <PROFILE> session send <id_or_title> "message" --no-wait` + "`" + ` | Send immediately without waiting for ready state. |

### Session Control
| Command | Description |
|---------|-------------|
| ` + "`" + `agent-deck -p <PROFILE> session start <id_or_title>` + "`" + ` | Start a stopped session |
| ` + "`" + `agent-deck -p <PROFILE> session stop <id_or_title>` + "`" + ` | Stop a running session |
| ` + "`" + `agent-deck -p <PROFILE> session restart <id_or_title>` + "`" + ` | Restart (reloads MCPs for Claude) |
| ` + "`" + `agent-deck -p <PROFILE> add <path> -t "Title" -c claude -g "group"` + "`" + ` | Create new Claude session |
| ` + "`" + `agent-deck -p <PROFILE> add <path> -t "Title" -c claude --worktree feature/branch -b` + "`" + ` | Create session with new worktree |

### Session Resolution
Commands accept: **exact title**, **ID prefix** (e.g., first 4 chars), **path**, or **fuzzy match**.

## Session Status Values

| Status | Meaning | Your Action |
|--------|---------|-------------|
| ` + "`" + `running` + "`" + ` (green) | Claude is actively processing | Do nothing. Wait. |
| ` + "`" + `waiting` + "`" + ` (yellow) | Claude finished, needs input | Read output, decide: auto-respond or escalate |
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

## Important Notes

- This project is ` + "`" + `asheshgoplani/agent-deck` + "`" + ` on GitHub. When referencing GitHub issues or PRs, always use owner ` + "`" + `asheshgoplani` + "`" + ` and repo ` + "`" + `agent-deck` + "`" + `. Never use ` + "`" + `anthropics` + "`" + ` as the owner.
- You cannot directly access other sessions' files. Use ` + "`" + `session output` + "`" + ` to read their latest response.
- ` + "`" + `session send` + "`" + ` waits up to 60 seconds for the agent to be ready. If the session is running (busy), the send will wait.
- The bridge polls your status every 2 seconds after sending you a message. Reply promptly.
- Your own session can be restarted by the bridge if it detects you're in an error state.
- Keep state.json small (no large output dumps). Store summaries, not full text.
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

// conductorPerNameClaudeMDTemplate is the per-conductor CLAUDE.md written to ~/.agent-deck/conductor/<name>/CLAUDE.md.
// It contains only the conductor's identity. Shared knowledge is inherited from the parent directory's CLAUDE.md.
// {NAME} and {PROFILE} placeholders are replaced at setup time.
const conductorPerNameClaudeMDTemplate = `# Conductor: {NAME} ({PROFILE} profile)

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

// conductorBridgePy is the Python bridge script that connects Telegram and/or Slack to conductor sessions.
// This is embedded so the binary is self-contained.
// Updated for multi-conductor: discovers conductors from meta.json files on disk.
// Supports both Telegram (polling) and Slack (Socket Mode) concurrently.
const conductorBridgePy = `#!/usr/bin/env python3
"""
Conductor Bridge: Telegram & Slack <-> Agent-Deck conductor sessions (multi-conductor).

A thin bridge that:
  A) Forwards Telegram/Slack messages -> conductor session (via agent-deck CLI)
  B) Forwards conductor responses -> Telegram/Slack
  C) Runs a periodic heartbeat to trigger conductor status checks

Discovers conductors dynamically from meta.json files in ~/.agent-deck/conductor/*/
Each conductor has its own name, profile, and heartbeat settings.

Dependencies: pip3 install toml aiogram slack-bolt slack-sdk
  - aiogram is only needed if Telegram is configured
  - slack-bolt/slack-sdk are only needed if Slack is configured
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

# How long to wait for conductor to respond (seconds)
RESPONSE_TIMEOUT = 300

# Poll interval when waiting for conductor response (seconds)
POLL_INTERVAL = 2

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
    tg_token = tg.get("token", "")
    tg_user_id = tg.get("user_id", 0)
    tg_configured = bool(tg_token and tg_user_id)

    # Slack config
    sl = conductor_cfg.get("slack", {})
    sl_bot_token = sl.get("bot_token", "")
    sl_app_token = sl.get("app_token", "")
    sl_channel_id = sl.get("channel_id", "")
    sl_listen_mode = sl.get("listen_mode", "mentions")  # "mentions" or "all"
    sl_allowed_users = sl.get("allowed_user_ids", [])  # List of authorized Slack user IDs
    sl_configured = bool(sl_bot_token and sl_app_token and sl_channel_id)

    if not tg_configured and not sl_configured:
        log.error(
            "Neither Telegram nor Slack configured in config.toml. "
            "Set [conductor.telegram] or [conductor.slack]."
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
    """Select at most one heartbeat conductor per profile.

    Multiple conductors may share a profile. Heartbeat auto-actions are profile-wide,
    so running all of them would duplicate interventions. We choose one deterministic
    conductor (oldest by created_at, then name) per profile.
    """
    selected: dict[str, dict] = {}
    for c in conductors:
        if not c.get("heartbeat_enabled", True):
            continue
        profile = c.get("profile") or "default"
        current = selected.get(profile)
        if current is None:
            selected[profile] = c
            continue

        cur_key = (
            str(current.get("created_at", "")),
            str(current.get("name", "")),
        )
        cand_key = (
            str(c.get("created_at", "")),
            str(c.get("name", "")),
        )
        if cand_key < cur_key:
            selected[profile] = c
    return list(selected.values())


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
            cmd, capture_output=True, text=True, timeout=timeout
        )
        return result
    except subprocess.TimeoutExpired:
        log.warning("CLI timeout: %s", " ".join(cmd))
        return subprocess.CompletedProcess(cmd, 1, "", "timeout")
    except FileNotFoundError:
        log.error("agent-deck not found in PATH")
        return subprocess.CompletedProcess(cmd, 1, "", "not found")


def get_session_status(session: str, profile: str | None = None) -> str:
    """Get the status of a session (running/waiting/idle/error)."""
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
    """Get the last response from a session."""
    result = run_cli(
        "session", "output", session, "-q", profile=profile, timeout=30
    )
    if result.returncode != 0:
        return f"[Error getting output: {result.stderr.strip()}]"
    return result.stdout.strip()


def send_to_conductor(
    session: str, message: str, profile: str | None = None
) -> bool:
    """Send a message to the conductor session. Returns True on success."""
    result = run_cli(
        "session", "send", session, message, profile=profile, timeout=120
    )
    if result.returncode != 0:
        log.error(
            "Failed to send to conductor: %s", result.stderr.strip()
        )
        return False
    return True


def get_status_summary(profile: str | None = None) -> dict:
    """Get agent-deck status as a dict for a single profile."""
    result = run_cli("status", "--json", profile=profile, timeout=30)
    if result.returncode != 0:
        return {"waiting": 0, "running": 0, "idle": 0, "error": 0, "total": 0}
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError:
        return {"waiting": 0, "running": 0, "idle": 0, "error": 0, "total": 0}


def get_status_summary_all(profiles: list[str]) -> dict:
    """Aggregate status across all profiles."""
    totals = {"waiting": 0, "running": 0, "idle": 0, "error": 0, "total": 0}
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
# Response polling
# ---------------------------------------------------------------------------


async def wait_for_response(
    session: str, profile: str | None = None, timeout: int = RESPONSE_TIMEOUT
) -> str:
    """Poll until the conductor finishes processing (status = waiting/idle).

    Two phases:
    1. Wait for the session to become active (processing the message).
       This avoids reading stale output from before the message was sent.
    2. Wait for the session to return to waiting/idle (response ready).

    If reading the output fails (e.g. session file not yet created for new
    sessions), keeps polling instead of returning the error immediately.
    """
    elapsed = 0
    saw_active = False
    last_error = ""

    while elapsed < timeout:
        await asyncio.sleep(POLL_INTERVAL)
        elapsed += POLL_INTERVAL

        status = get_session_status(session, profile=profile)
        if status == "error":
            return "[Conductor session is in error state. Try /restart]"

        if status in ("running", "active", "starting"):
            saw_active = True
            continue

        if status in ("waiting", "idle"):
            should_read = saw_active or elapsed >= 6
            if should_read:
                output = get_session_output(session, profile=profile)
                if output.startswith("[Error"):
                    # Output not available yet (e.g. JSONL file not created).
                    # Keep polling — it should appear soon.
                    last_error = output
                    saw_active = True  # prevent re-reading every poll
                    continue
                return output

    if last_error:
        return last_error
    return f"[Conductor timed out after {timeout}s. It may still be processing.]"


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

    def is_authorized(message: types.Message) -> bool:
        """Check if message is from the authorized user."""
        if message.from_user.id != authorized_user:
            log.warning(
                "Unauthorized message from user %d", message.from_user.id
            )
            return False
        return True

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

        conductor_names = get_conductor_names()
        conductors = discover_conductors()

        # Determine target conductor from message prefix
        target_name, cleaned_msg = parse_conductor_prefix(
            message.text, conductor_names
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
            cleaned_msg = message.text

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
        if not send_to_conductor(
            session_title, cleaned_msg, profile=profile
        ):
            await message.answer(
                f"[Failed to send message to conductor {target['name']}.]"
            )
            return

        # Wait for response
        name_tag = (
            f"[{target['name']}] " if len(conductors) > 1 else ""
        )
        await message.answer(f"{name_tag}...")  # typing indicator
        response = await wait_for_response(
            session_title, profile=profile
        )
        log.info("Conductor [%s] response: %s", target["name"], response[:100])

        # Send response back (split if needed)
        for chunk in split_message(response):
            prefixed = f"{name_tag}{chunk}" if name_tag else chunk
            await message.answer(prefixed)

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

    def get_default_conductor() -> dict | None:
        """Get the first conductor (default target for messages)."""
        conductors = discover_conductors()
        return conductors[0] if conductors else None

    async def _safe_say(say, **kwargs):
        """Wrapper around say() that catches network/API errors."""
        try:
            await say(**kwargs)
        except Exception as e:
            log.error("Slack say() failed: %s", e)

    async def _handle_slack_text(text: str, say, thread_ts: str = None):
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
        if not send_to_conductor(session_title, cleaned_msg, profile=profile):
            await _safe_say(
                say,
                text=f"[Failed to send message to conductor {target['name']}.]",
                thread_ts=thread_ts,
            )
            return

        name_tag = f"[{target['name']}] " if len(conductors) > 1 else ""
        await _safe_say(say, text=f"{name_tag}...", thread_ts=thread_ts)

        response = await wait_for_response(session_title, profile=profile)
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
# Heartbeat loop
# ---------------------------------------------------------------------------


async def heartbeat_loop(config: dict, telegram_bot=None, slack_app=None, slack_channel_id=None):
    """Periodic heartbeat: check status for each conductor and trigger checks."""
    global_interval = config["heartbeat_interval"]
    if global_interval <= 0:
        log.info("Heartbeat disabled (interval=0)")
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

                # Get current status for this conductor's profile
                summary = get_status_summary(profile)
                waiting = summary.get("waiting", 0)
                running = summary.get("running", 0)
                idle = summary.get("idle", 0)
                error = summary.get("error", 0)

                log.info(
                    "Heartbeat [%s/%s]: %d waiting, %d running, %d idle, %d error",
                    name, profile, waiting, running, idle, error,
                )

                # Only trigger conductor if there are waiting or error sessions
                if waiting == 0 and error == 0:
                    continue

                # Build heartbeat message with waiting session details
                sessions = get_sessions_list(profile)
                waiting_details = []
                error_details = []
                for s in sessions:
                    s_title = s.get("title", "untitled")
                    s_status = s.get("status", "")
                    s_path = s.get("path", "")
                    # Skip conductor sessions
                    if s_title.startswith("conductor-"):
                        continue
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
                    f"{running} running, {idle} idle, {error} error."
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
                if not send_to_conductor(
                    session_title, heartbeat_msg, profile=profile
                ):
                    log.error(
                        "Heartbeat [%s]: failed to send to conductor",
                        name,
                    )
                    continue

                # Wait for conductor's response
                response = await wait_for_response(
                    session_title, profile=profile
                )
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
                            await telegram_bot.send_message(
                                tg_user_id, alert_msg,
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

    if not tg_ok and not sl_ok:
        if config["telegram"]["configured"] and not HAS_AIOGRAM:
            log.error("Telegram configured but aiogram not installed. pip install aiogram")
        if config["slack"]["configured"] and not HAS_SLACK:
            log.error("Slack configured but slack-bolt not installed. pip install slack-bolt slack-sdk")
        if not config["telegram"]["configured"] and not config["slack"]["configured"]:
            log.error("Neither Telegram nor Slack configured. Exiting.")
        sys.exit(1)

    platforms = []
    if tg_ok:
        platforms.append("Telegram")
    if sl_ok:
        platforms.append("Slack")

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

    # Pre-start all conductors so they're warm when messages arrive
    for c in conductors:
        if ensure_conductor_running(c["name"], c["profile"]):
            log.info("Conductor %s is running", c["name"])
        else:
            log.warning("Failed to pre-start conductor %s", c["name"])

    # Start heartbeat (shared, notifies both platforms)
    heartbeat_task = asyncio.create_task(
        heartbeat_loop(
            config,
            telegram_bot=telegram_bot,
            slack_app=slack_app,
            slack_channel_id=slack_channel_id,
        )
    )

    # Run both concurrently
    tasks = [heartbeat_task]
    if telegram_dp and telegram_bot:
        tasks.append(asyncio.create_task(telegram_dp.start_polling(telegram_bot)))
        log.info("Telegram bot polling started")
    if slack_handler:
        tasks.append(asyncio.create_task(slack_handler.start_async()))
        log.info("Slack Socket Mode handler started")

    try:
        await asyncio.gather(*tasks)
    finally:
        heartbeat_task.cancel()
        if telegram_bot:
            await telegram_bot.session.close()
        if slack_handler:
            await slack_handler.close_async()


if __name__ == "__main__":
    asyncio.run(main())
`
