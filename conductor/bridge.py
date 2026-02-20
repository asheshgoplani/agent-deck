#!/usr/bin/env python3
"""
Conductor Bridge: Telegram <-> Agent-Deck conductor sessions (multi-profile).

A thin bridge that:
  A) Forwards Telegram messages -> conductor session (via agent-deck CLI)
  B) Forwards conductor responses -> Telegram
  C) Runs a periodic heartbeat to trigger conductor status checks

Supports multiple profiles: each profile gets its own conductor session.
The bridge aggregates status across all profiles.

Dependencies: pip3 install aiogram toml
"""

import asyncio
import json
import logging
import os
import subprocess
import sys
import time
from pathlib import Path

import toml
from aiogram import Bot, Dispatcher, types
from aiogram.filters import Command, CommandStart

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

AGENT_DECK_DIR = Path.home() / ".agent-deck"
CONFIG_PATH = AGENT_DECK_DIR / "config.toml"
CONDUCTOR_DIR = AGENT_DECK_DIR / "conductor"
LOG_PATH = CONDUCTOR_DIR / "bridge.log"

# Telegram message length limit
TG_MAX_LENGTH = 4096

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
    """Load [conductor] section from config.toml."""
    if not CONFIG_PATH.exists():
        log.error("Config not found: %s", CONFIG_PATH)
        sys.exit(1)

    config = toml.load(CONFIG_PATH)
    conductor_cfg = config.get("conductor", {})

    if not conductor_cfg.get("enabled", False):
        log.error("[conductor] section missing or not enabled in config.toml")
        sys.exit(1)

    tg = conductor_cfg.get("telegram", {})
    token = tg.get("token", "")
    user_id = tg.get("user_id", 0)

    if not token:
        log.error("conductor.telegram.token not set in config.toml")
        sys.exit(1)
    if not user_id:
        log.error("conductor.telegram.user_id not set in config.toml")
        sys.exit(1)

    profiles = conductor_cfg.get("profiles", ["default"])

    return {
        "token": token,
        "user_id": int(user_id),
        "heartbeat_interval": conductor_cfg.get("heartbeat_interval", 15),
        "profiles": profiles,
    }


def conductor_session_title(profile: str) -> str:
    """Return the conductor session title for a given profile."""
    return f"conductor-{profile}"


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
        "session", "show", session, "--json", profile=profile, timeout=30
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
        "session", "send", session, message, "--no-wait", profile=profile, timeout=30
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


def ensure_conductor_running(profile: str) -> bool:
    """Ensure the conductor session for a profile exists and is running."""
    session_title = conductor_session_title(profile)
    status = get_session_status(session_title, profile=profile)

    if status == "error":
        log.info(
            "Conductor session for %s not running, attempting to start...",
            profile,
        )
        # Try starting first (session might exist but be stopped)
        result = run_cli(
            "session", "start", session_title, profile=profile, timeout=60
        )
        if result.returncode != 0:
            # Session might not exist, try creating it
            log.info("Creating conductor session for %s...", profile)
            session_path = str(CONDUCTOR_DIR / profile)
            result = run_cli(
                "add", session_path,
                "-t", session_title,
                "-c", "claude",
                "-g", "infra",
                profile=profile,
                timeout=60,
            )
            if result.returncode != 0:
                log.error(
                    "Failed to create conductor for %s: %s",
                    profile,
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


def ensure_all_conductors_running(profiles: list[str]) -> dict[str, bool]:
    """Ensure conductor sessions are running for all profiles."""
    results = {}
    for profile in profiles:
        results[profile] = ensure_conductor_running(profile)
    return results


# ---------------------------------------------------------------------------
# Message routing
# ---------------------------------------------------------------------------


def parse_profile_prefix(text: str, profiles: list[str]) -> tuple[str | None, str]:
    """Parse profile prefix from user message.

    Supports formats:
      /p <profile> <message>
      <profile>: <message>

    Returns (profile_or_None, cleaned_message).
    """
    # Check /p <profile> <message>
    if text.startswith("/p "):
        parts = text[3:].strip().split(None, 1)
        if len(parts) >= 2 and parts[0] in profiles:
            return parts[0], parts[1]
        if len(parts) == 1 and parts[0] in profiles:
            return parts[0], ""

    # Check <profile>: <message>
    for profile in profiles:
        prefix = f"{profile}:"
        if text.startswith(prefix):
            return profile, text[len(prefix):].strip()

    return None, text


# ---------------------------------------------------------------------------
# Response polling
# ---------------------------------------------------------------------------


async def wait_for_response(
    session: str, profile: str | None = None, timeout: int = RESPONSE_TIMEOUT
) -> str:
    """Poll until the conductor finishes processing (status = waiting/idle)."""
    elapsed = 0
    while elapsed < timeout:
        await asyncio.sleep(POLL_INTERVAL)
        elapsed += POLL_INTERVAL

        status = get_session_status(session, profile=profile)
        if status in ("waiting", "idle"):
            return get_session_output(session, profile=profile)
        if status == "error":
            return "[Conductor session is in error state. Try /restart]"

    return f"[Conductor timed out after {timeout}s. It may still be processing.]"


# ---------------------------------------------------------------------------
# Telegram message splitting
# ---------------------------------------------------------------------------


def split_message(text: str, max_len: int = TG_MAX_LENGTH) -> list[str]:
    """Split a long message into chunks that fit Telegram's limit."""
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


def create_bot(config: dict) -> tuple[Bot, Dispatcher]:
    """Create and configure the Telegram bot."""
    bot = Bot(token=config["token"])
    dp = Dispatcher()
    authorized_user = config["user_id"]
    profiles = config["profiles"]
    default_profile = profiles[0]

    def is_authorized(message: types.Message) -> bool:
        """Check if message is from the authorized user."""
        if message.from_user.id != authorized_user:
            log.warning(
                "Unauthorized message from user %d", message.from_user.id
            )
            return False
        return True

    @dp.message(CommandStart())
    async def cmd_start(message: types.Message):
        if not is_authorized(message):
            return
        profile_list = ", ".join(profiles)
        await message.answer(
            "Conductor bridge active.\n"
            f"Profiles: {profile_list}\n"
            "Commands: /status /sessions /help /restart\n"
            f"Route to profile: /p <profile> <message> or <profile>: <message>\n"
            f"Default profile: {default_profile}"
        )

    @dp.message(Command("status"))
    async def cmd_status(message: types.Message):
        if not is_authorized(message):
            return
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
        profile_list = ", ".join(profiles)
        await message.answer(
            "Conductor Commands:\n"
            "/status    - Aggregated status across all profiles\n"
            "/sessions  - List all sessions (all profiles)\n"
            "/restart   - Restart a conductor (default or specify profile)\n"
            "/help      - This message\n\n"
            f"Profiles: {profile_list}\n"
            f"Route: /p <profile> <message> or <profile>: <message>\n"
            f"Default: messages go to {default_profile} conductor"
        )

    @dp.message(Command("restart"))
    async def cmd_restart(message: types.Message):
        if not is_authorized(message):
            return

        # Parse optional profile argument: /restart work
        text = message.text.strip()
        parts = text.split(None, 1)
        target_profile = default_profile
        if len(parts) > 1 and parts[1] in profiles:
            target_profile = parts[1]

        session_title = conductor_session_title(target_profile)
        await message.answer(
            f"Restarting conductor for [{target_profile}]..."
        )
        result = run_cli(
            "session", "restart", session_title,
            profile=target_profile, timeout=60,
        )
        if result.returncode == 0:
            await message.answer(
                f"Conductor [{target_profile}] restarted."
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

        # Determine target profile from message prefix
        target_profile, cleaned_msg = parse_profile_prefix(
            message.text, profiles
        )
        if target_profile is None:
            target_profile = default_profile
        if not cleaned_msg:
            cleaned_msg = message.text

        session_title = conductor_session_title(target_profile)

        # Ensure conductor is running for this profile
        if not ensure_conductor_running(target_profile):
            await message.answer(
                f"[Could not start conductor for {target_profile}. Check agent-deck.]"
            )
            return

        # Send to conductor
        log.info(
            "User message -> [%s]: %s", target_profile, cleaned_msg[:100]
        )
        if not send_to_conductor(
            session_title, cleaned_msg, profile=target_profile
        ):
            await message.answer(
                f"[Failed to send message to conductor [{target_profile}].]"
            )
            return

        # Wait for response
        profile_tag = (
            f"[{target_profile}] " if len(profiles) > 1 else ""
        )
        await message.answer(f"{profile_tag}...")  # typing indicator
        response = await wait_for_response(
            session_title, profile=target_profile
        )
        log.info("Conductor [%s] response: %s", target_profile, response[:100])

        # Send response back (split if needed)
        for chunk in split_message(response):
            prefixed = f"{profile_tag}{chunk}" if profile_tag else chunk
            await message.answer(prefixed)

    return bot, dp


# ---------------------------------------------------------------------------
# Heartbeat loop
# ---------------------------------------------------------------------------


async def heartbeat_loop(bot: Bot, config: dict):
    """Periodic heartbeat: check status across all profiles and trigger conductors."""
    interval_minutes = config["heartbeat_interval"]
    if interval_minutes <= 0:
        log.info("Heartbeat disabled (interval=0)")
        return

    interval_seconds = interval_minutes * 60
    profiles = config["profiles"]
    authorized_user = config["user_id"]

    log.info(
        "Heartbeat loop started (every %d minutes, %d profiles)",
        interval_minutes,
        len(profiles),
    )

    while True:
        await asyncio.sleep(interval_seconds)

        for profile in profiles:
            try:
                session_title = conductor_session_title(profile)

                # Get current status for this profile
                summary = get_status_summary(profile)
                waiting = summary.get("waiting", 0)
                running = summary.get("running", 0)
                idle = summary.get("idle", 0)
                error = summary.get("error", 0)

                log.info(
                    "Heartbeat [%s]: %d waiting, %d running, %d idle, %d error",
                    profile, waiting, running, idle, error,
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
                    # Skip the conductor itself
                    if s_title == session_title:
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
                    f"[HEARTBEAT] [{profile}] Status: {waiting} waiting, "
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

                # Ensure conductor is running for this profile
                if not ensure_conductor_running(profile):
                    log.error(
                        "Heartbeat [%s]: conductor not running, skipping",
                        profile,
                    )
                    continue

                # Send heartbeat to conductor
                if not send_to_conductor(
                    session_title, heartbeat_msg, profile=profile
                ):
                    log.error(
                        "Heartbeat [%s]: failed to send to conductor",
                        profile,
                    )
                    continue

                # Wait for conductor's response
                response = await wait_for_response(
                    session_title, profile=profile
                )
                log.info(
                    "Heartbeat [%s] response: %s",
                    profile, response[:200],
                )

                # If conductor flagged items needing attention, notify via Telegram
                if "NEED:" in response:
                    try:
                        prefix = (
                            f"[{profile}] " if len(profiles) > 1 else ""
                        )
                        await bot.send_message(
                            authorized_user,
                            f"{prefix}Conductor alert:\n{response}",
                        )
                    except Exception as e:
                        log.error(
                            "Failed to send Telegram notification: %s", e
                        )

            except Exception as e:
                log.error("Heartbeat [%s] error: %s", profile, e)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------


async def main():
    log.info("Loading config from %s", CONFIG_PATH)
    config = load_config()

    log.info(
        "Starting conductor bridge (user_id=%d, heartbeat=%dm, profiles=%s)",
        config["user_id"],
        config["heartbeat_interval"],
        ", ".join(config["profiles"]),
    )

    bot, dp = create_bot(config)

    # Run heartbeat in background
    heartbeat_task = asyncio.create_task(heartbeat_loop(bot, config))

    try:
        log.info("Telegram bot polling started")
        await dp.start_polling(bot)
    finally:
        heartbeat_task.cancel()
        await bot.session.close()


if __name__ == "__main__":
    asyncio.run(main())
