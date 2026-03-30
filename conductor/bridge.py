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
import re
import signal
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
# Telegram message length limit
TG_MAX_LENGTH = 4096

# How long to wait for conductor to respond (seconds)
RESPONSE_TIMEOUT = 300

# ---------------------------------------------------------------------------
# Logging
# ---------------------------------------------------------------------------

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    handlers=[
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
        # Use Popen + communicate(timeout=) so we have the proc object available
        # when TimeoutExpired fires — subprocess.run() does NOT set exc.proc.
        proc = subprocess.Popen(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=True,  # own process group → killpg kills grandchildren too
        )
        try:
            stdout, stderr = proc.communicate(timeout=timeout)
            return subprocess.CompletedProcess(cmd, proc.returncode, stdout, stderr)
        except subprocess.TimeoutExpired:
            log.warning("CLI timeout: %s", " ".join(cmd))
            try:
                # Kill the entire process group so grandchildren (e.g. tmux send-keys)
                # don't survive as orphans and jam the pane's input queue.
                os.killpg(os.getpgid(proc.pid), signal.SIGKILL)
            except (ProcessLookupError, PermissionError):
                proc.kill()  # fallback: kill direct child only
            proc.communicate()
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
        # Single-call flow: send + wait + print raw response.
        # Avoids extra status/output polling round-trips.
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
    return True, result.stdout.strip()


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
# Hook system
# ---------------------------------------------------------------------------

DEFAULT_HOOK_TIMEOUT = 30  # seconds


def resolve_hook(profile: str, hook_name: str) -> Path | None:
    """Find a hook script by name, checking profile-level then global.

    Returns the path to the executable hook, or None if not found.
    Profile-level hooks take precedence over global hooks.
    """
    candidates = [
        CONDUCTOR_DIR / profile / "hooks" / hook_name,
        CONDUCTOR_DIR / "hooks" / hook_name,
    ]
    for path in candidates:
        if path.exists():
            if os.access(path, os.X_OK):
                return path
            log.warning(
                "Hook '%s' found at %s but not executable, skipping",
                hook_name, path,
            )
            return None
    return None


def run_hook(
    hook_path: Path, stdin_data: dict, timeout: int = DEFAULT_HOOK_TIMEOUT
) -> tuple[int, str, str]:
    """Execute a hook script and return (exit_code, stdout, stderr).

    Context is passed as JSON on stdin. Returns (exit_code, stdout, stderr).
    On timeout, returns (1, "", "timeout").
    """
    payload = json.dumps(stdin_data)
    try:
        result = subprocess.run(
            [str(hook_path)],
            input=payload,
            capture_output=True,
            text=True,
            timeout=timeout,
            env={
                **os.environ,
                "CONDUCTOR_PROFILE": stdin_data.get("profile", ""),
                "CONDUCTOR_DIR": str(CONDUCTOR_DIR),
            },
        )
        return result.returncode, result.stdout, result.stderr
    except subprocess.TimeoutExpired:
        log.error("Hook '%s' timed out after %ds", hook_path.name, timeout)
        return 1, "", "timeout"
    except Exception as e:
        log.error("Hook '%s' crashed: %s", hook_path.name, e)
        return 1, "", str(e)


def invoke_hook(
    profile: str, hook_name: str, context: dict
) -> tuple[bool, str] | None:
    """Resolve and run a hook, returning (success, stdout) or None if no hook.

    Reads timeout from meta.json hooks.timeout if available.
    Logs all invocations, stdout, stderr, and exit codes.
    """
    hook_path = resolve_hook(profile, hook_name)
    if hook_path is None:
        return None

    # Read timeout from meta.json if available
    timeout = DEFAULT_HOOK_TIMEOUT
    meta_path = CONDUCTOR_DIR / profile / "meta.json"
    if meta_path.exists():
        try:
            meta = json.loads(meta_path.read_text())
            timeout = meta.get("hooks", {}).get("timeout", DEFAULT_HOOK_TIMEOUT)
        except Exception:
            pass

    log.info("Hook [%s/%s]: invoking %s", profile, hook_name, hook_path)
    exit_code, stdout, stderr = run_hook(hook_path, context, timeout)

    if stderr.strip():
        log.warning("Hook [%s/%s] stderr: %s", profile, hook_name, stderr.strip())

    log.info(
        "Hook [%s/%s]: exit_code=%d, stdout_len=%d",
        profile, hook_name, exit_code, len(stdout),
    )

    return (exit_code == 0, stdout.strip())


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

    text = re.sub(r'`(.+?)`', _save_code, text)

    # 2. Escape HTML special chars
    text = _html.escape(text, quote=False)

    # 3. Convert bold/italic (code spans are already replaced with placeholders)
    text = re.sub(r'\*\*(.+?)\*\*', r'<b>\1</b>', text)
    text = re.sub(r'(?<!\*)\*(?!\*)(.+?)(?<!\*)\*(?!\*)', r'<i>\1</i>', text)

    # 4. Restore code spans (escaped content wrapped in <code>)
    for i, code in enumerate(code_spans):
        text = text.replace(f"\x00CODE{i}\x00", f"<code>{_html.escape(code, quote=False)}</code>")

    return text


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
        await ensure_bot_info(message.bot)
        if not is_bot_addressed(message):
            return

        # Strip @botname mention from group messages
        text = strip_bot_mention(message.text)
        if not text:
            return

        # Determine target profile from message prefix
        target_profile, cleaned_msg = parse_profile_prefix(text, profiles)
        if target_profile is None:
            target_profile = default_profile
        if not cleaned_msg:
            cleaned_msg = text

        session_title = conductor_session_title(target_profile)

        # Run pre-message hook (can transform or gate the message)
        hook_result = invoke_hook(target_profile, "pre-message", {
            "profile": target_profile,
            "message_text": cleaned_msg,
            "user_id": message.from_user.id,
        })
        if hook_result is not None:
            success, stdout = hook_result
            if not success:
                log.info("Message dropped by pre-message hook for [%s]", target_profile)
                return
            if stdout:
                cleaned_msg = stdout

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
        ok, response = send_to_conductor(
            session_title,
            cleaned_msg,
            profile=target_profile,
            wait_for_reply=True,
            response_timeout=RESPONSE_TIMEOUT,
        )
        if not ok:
            await message.answer(
                f"[Failed to send message to conductor [{target_profile}].]"
            )
            return

        # Response is returned directly by `session send --wait`.
        profile_tag = (
            f"[{target_profile}] " if len(profiles) > 1 else ""
        )
        await message.answer(f"{profile_tag}...")  # typing indicator
        log.info("Conductor [%s] response: %s", target_profile, response[:100])

        # Convert to HTML first, then split to respect post-conversion length
        html_response = md_to_tg_html(
            f"{profile_tag}{response}" if profile_tag else response
        )
        for chunk in split_message(html_response):
            await message.answer(chunk, parse_mode="HTML")

        # Run post-message hook (non-gating)
        invoke_hook(target_profile, "post-message", {
            "profile": target_profile,
            "message_text": cleaned_msg,
            "response": response,
        })

    return bot, dp


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


async def heartbeat_loop(bot: Bot, config: dict):
    """Periodic heartbeat: check status across all profiles and trigger conductors."""
    interval_minutes = config["heartbeat_interval"]
    if interval_minutes <= 0:
        log.info("Heartbeat disabled (interval=0)")
        return

    if _os_heartbeat_daemon_installed():
        log.info("OS heartbeat daemon detected, bridge heartbeat loop disabled (avoiding double-trigger)")
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
                # Append HEARTBEAT_RULES.md (per-profile, then global fallback)
                rules_text = None
                for rules_path in [
                    CONDUCTOR_DIR / profile / "HEARTBEAT_RULES.md",
                    CONDUCTOR_DIR / "HEARTBEAT_RULES.md",
                ]:
                    if rules_path.exists():
                        try:
                            rules_text = rules_path.read_text().strip()
                        except Exception as e:
                            log.warning("Failed to read %s: %s", rules_path, e)
                        break
                if rules_text:
                    parts.append(f"\n\n{rules_text}")
                else:
                    parts.append(
                        "Check if any need auto-response or user attention."
                    )

                heartbeat_msg = " ".join(parts)

                # Run pre-heartbeat hook (can transform or gate the message)
                sessions_for_hook = [
                    {"title": s.get("title", ""), "status": s.get("status", ""), "path": s.get("path", "")}
                    for s in sessions if s.get("title") != session_title
                ]
                hook_result = invoke_hook(profile, "pre-heartbeat", {
                    "profile": profile,
                    "waiting": waiting,
                    "running": running,
                    "idle": idle,
                    "error": error,
                    "sessions": sessions_for_hook,
                    "draft_message": heartbeat_msg,
                })
                if hook_result is not None:
                    success, stdout = hook_result
                    if not success:
                        log.info("Heartbeat [%s]: gated by pre-heartbeat hook", profile)
                        continue
                    if stdout:
                        heartbeat_msg = stdout

                # Ensure conductor is running for this profile
                if not ensure_conductor_running(profile):
                    log.error(
                        "Heartbeat [%s]: conductor not running, skipping",
                        profile,
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
                        profile,
                    )
                    continue

                # Response is returned directly by `session send --wait`.
                log.info(
                    "Heartbeat [%s] response: %s",
                    profile, response[:200],
                )

                # If conductor flagged items needing attention, notify via Telegram
                has_alerts = "NEED:" in response
                if has_alerts:
                    try:
                        prefix = (
                            f"[{profile}] " if len(profiles) > 1 else ""
                        )
                        alert_html = md_to_tg_html(
                            f"{prefix}Conductor alert:\n{response}"
                        )
                        for chunk in split_message(alert_html):
                            await bot.send_message(
                                authorized_user,
                                chunk,
                                parse_mode="HTML",
                            )
                    except Exception as e:
                        log.error(
                            "Failed to send Telegram notification: %s", e
                        )

                # Run post-heartbeat hook (non-gating)
                invoke_hook(profile, "post-heartbeat", {
                    "profile": profile,
                    "response": response,
                    "has_alerts": has_alerts,
                })

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
