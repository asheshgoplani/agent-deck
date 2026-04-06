#!/usr/bin/env python3
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

from __future__ import annotations

import asyncio
import functools
import json
import logging
import os
import re
import signal
import subprocess
import sys
import time
from collections import deque
from collections.abc import Coroutine
from pathlib import Path
from typing import Any, Callable

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
# Telegram message length limit
TG_MAX_LENGTH = 4096

# Slack message length limit
SLACK_MAX_LENGTH = 40000

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
    """Discover all conductors by scanning meta.json files.

    Returns a list sorted by conductor name for deterministic default routing.
    """
    conductors = []
    if not CONDUCTOR_DIR.exists():
        return conductors
    for entry in CONDUCTOR_DIR.iterdir():
        if entry.is_dir():
            meta_path = entry / "meta.json"
            if meta_path.exists():
                try:
                    with open(meta_path) as f:
                        conductors.append(json.load(f))
                except (json.JSONDecodeError, IOError) as e:
                    log.warning("Failed to read %s: %s", meta_path, e)
    conductors.sort(key=lambda c: c.get("name", ""))
    return conductors


def conductor_session_title(name: str) -> str:
    """Return the conductor session title for a given conductor name."""
    return f"conductor-{name}"


def get_conductor_names() -> list[str]:
    """Get list of all conductor names."""
    return [c["name"] for c in discover_conductors()]


def get_default_conductor() -> dict | None:
    """Get the first conductor (default target for messages)."""
    conductors = discover_conductors()
    return conductors[0] if conductors else None


def get_unique_profiles() -> list[str]:
    """Get unique profile names from all conductors."""
    profiles = set()
    for c in discover_conductors():
        profiles.add(c.get("profile", "default"))
    return sorted(profiles)


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
            start_new_session=True,  # own process group -> killpg kills grandchildren too
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
    """Get the status of a session (running/waiting/idle/error/unknown).

    Returns "unknown" on CLI failure or parse error — callers should treat
    this as a transient condition and retry rather than dropping state.
    """
    result = run_cli(
        "session", "show", session, "--json", profile=profile, timeout=30
    )
    if result.returncode != 0:
        return "unknown"  # transient CLI failure — not the same as conductor broken
    try:
        data = json.loads(result.stdout)
        return data.get("status", "unknown")
    except (json.JSONDecodeError, KeyError):
        return "unknown"


def get_session_output(session: str, profile: str | None = None) -> str:
    """Get the last response from a session."""
    result = run_cli(
        "session", "output", session, "-q", profile=profile, timeout=30
    )
    if result.returncode != 0:
        return f"[Error getting output: {result.stderr.strip()}]"
    return result.stdout.strip()


# Async callable type for reply notifications: (response_text: str) -> None
ReplyCallback = Callable[[str], Coroutine[Any, Any, None]]


def send_to_conductor(
    session: str,
    message: str,
    profile: str | None = None,
    wait_for_reply: bool = False,
    response_timeout: int = RESPONSE_TIMEOUT,
    reply_callback: ReplyCallback | None = None,
    force_queue: bool = False,
) -> tuple[bool, str]:
    """Send a message to the conductor session.

    Returns (success, response_text). When wait_for_reply=False, response_text is "".

    When wait_for_reply=False and the conductor is busy (running/active/starting),
    the message is queued in-memory and delivered automatically once the conductor
    returns to idle/waiting state (see _drain_queue). reply_callback, if provided,
    is an async callable(response_text: str) invoked after drain delivery.

    force_queue=True skips the internal status check and enqueues immediately.
    Use this when the caller already knows the conductor is busy to avoid a
    redundant blocking subprocess call.
    """
    if not wait_for_reply:
        # force_queue: caller already confirmed conductor is busy — skip status check.
        if force_queue:
            log.info("Conductor %s: force-queueing message", session)
            _enqueue_message(session, message, profile, reply_callback)
            return True, ""

        # For non-blocking sends (user messages), check if conductor is busy
        # and queue instead of dropping.
        status = get_session_status(session, profile=profile)
        if status in ("running", "active", "starting"):
            log.info(
                "Conductor %s is busy (%s), queueing message", session, status,
            )
            _enqueue_message(session, message, profile, reply_callback)
            return True, ""  # queued, not failed

        result = run_cli(
            "session", "send", session, message, "--no-wait",
            profile=profile, timeout=30,
        )
        if result.returncode != 0:
            stderr = result.stderr.strip()
            # If the conductor became busy between the status check and the send,
            # queue instead of dropping.
            if "timeout" in stderr.lower() or "not ready" in stderr.lower():
                log.info(
                    "Conductor %s became busy during send, queueing message",
                    session,
                )
                _enqueue_message(session, message, profile, reply_callback)
                return True, ""
            log.error("Failed to send to conductor: %s", stderr)
            return False, ""
        return True, ""

    # wait_for_reply=True: single-call flow used by heartbeats.
    # Send + wait + capture raw response. Avoids extra polling round-trips.
    result = run_cli(
        "session", "send", session, message,
        "--wait", "--timeout", f"{response_timeout}s", "-q",
        profile=profile,
        timeout=max(response_timeout + 30, 60),
    )
    if result.returncode != 0:
        log.error(
            "Failed to send to conductor: %s", result.stderr.strip()
        )
        return False, ""
    return True, result.stdout.strip()


# ---------------------------------------------------------------------------
# Message queue for busy conductors
# ---------------------------------------------------------------------------

# Per-session max depth — prevents unbounded memory growth when conductor is stuck.
MAX_QUEUE_DEPTH = 20

# In-memory queue: {session_title: deque[(message, profile, reply_callback), ...]}
# reply_callback is an optional ReplyCallback that notifies the originating user
# when the queued message is eventually delivered.
_message_queue: dict[str, deque[tuple[str, str | None, ReplyCallback | None]]] = {}
_drain_task: asyncio.Task | None = None


def _enqueue_message(
    session: str,
    message: str,
    profile: str | None,
    reply_callback: ReplyCallback | None = None,
) -> None:
    """Add a message to the in-memory queue for a busy conductor.

    Enforces MAX_QUEUE_DEPTH by dropping the oldest item when full.
    Fires the dropped item's callback to notify the user.
    reply_callback, if provided, is invoked once the message is delivered.
    """
    if session not in _message_queue:
        _message_queue[session] = deque()
    queue = _message_queue[session]
    if len(queue) >= MAX_QUEUE_DEPTH:
        log.warning(
            "Queue full for %s (depth=%d), dropping oldest message",
            session, MAX_QUEUE_DEPTH,
        )
        _msg, _prof, dropped_cb = queue.popleft()
        if dropped_cb is not None:
            try:
                loop = asyncio.get_running_loop()
                loop.create_task(_fire_callback(
                    dropped_cb,
                    "[Message dropped — conductor queue overflow.]",
                ))
            except RuntimeError:
                pass  # no event loop available, can't fire async callback
    queue.append((message, profile, reply_callback))
    log.info("Queued message for %s (queue depth: %d)", session, len(queue))
    _ensure_drain_task()


async def _fire_callback(cb: ReplyCallback, text: str) -> None:
    """Invoke a reply_callback safely, decoupled from the drain loop."""
    try:
        await cb(text)
    except Exception as e:
        log.error("reply_callback error: %s", e)


def _ensure_drain_task() -> None:
    """Start the background drain task if it's not already running.

    Safe to call from sync context — silently skips if no event loop is running.
    """
    global _drain_task
    if _drain_task is not None and _drain_task.done() and not _drain_task.cancelled():
        exc = _drain_task.exception()
        if exc:
            log.error("Drain task crashed: %s", exc, exc_info=exc)
    if _drain_task is None or _drain_task.done():
        try:
            loop = asyncio.get_running_loop()
        except RuntimeError:
            log.warning("No running event loop — drain task deferred to next async call")
            return
        _drain_task = loop.create_task(_drain_queue_supervised())


async def _drain_queue_supervised() -> None:
    """Supervisor wrapper: restarts _drain_queue on unexpected crash."""
    while True:
        try:
            await _drain_queue()
            return  # normal exit: queue is empty
        except asyncio.CancelledError:
            raise  # propagate shutdown cancellation
        except Exception:
            log.exception("Drain task crashed unexpectedly, restarting in 5s")
            await asyncio.sleep(5)


async def _drain_queue() -> None:
    """Background loop that delivers queued messages once conductors are ready.

    Polls every 5s. For each conductor with queued messages, checks its
    status and delivers the oldest message when it becomes idle/waiting.
    Stops when the queue is empty.
    """
    log.info("Queue drain task started")
    while True:
        await asyncio.sleep(5)

        # Snapshot keys to avoid mutation during iteration
        sessions = list(_message_queue.keys())
        for session in sessions:
            items = _message_queue.get(session)
            if not items:
                _message_queue.pop(session, None)
                continue

            message, profile, reply_callback = items[0]
            loop = asyncio.get_running_loop()
            status = await loop.run_in_executor(
                None,
                functools.partial(get_session_status, session, profile=profile),
            )

            # Still busy or transient CLI failure — retry next cycle
            if status in ("running", "active", "starting", "unknown"):
                continue

            if status == "error":
                log.error(
                    "Conductor %s in error state, dropping %d queued message(s)",
                    session, len(items),
                )
                dropped = _message_queue.pop(session, deque())
                for _msg, _prof, cb in dropped:
                    if cb is not None:
                        loop.create_task(_fire_callback(
                            cb,
                            "[Queued message could not be delivered — conductor is in error state.]",
                        ))
                continue

            # Conductor is ready — deliver the message and wait for the response
            result = await loop.run_in_executor(
                None,
                functools.partial(
                    run_cli,
                    "session", "send", session, message,
                    "--wait", "--timeout", f"{RESPONSE_TIMEOUT}s", "-q",
                    profile=profile,
                    timeout=max(RESPONSE_TIMEOUT + 30, 60),
                ),
            )
            if result.returncode == 0:
                items.popleft()
                remaining = len(items)
                if not remaining:
                    _message_queue.pop(session, None)
                log.info(
                    "Conductor %s delivered queued message (%d remaining)",
                    session, remaining,
                )
                if reply_callback is not None:
                    text = result.stdout.strip() or "[No output from conductor.]"
                    loop.create_task(_fire_callback(reply_callback, text))
            else:
                stderr = result.stderr.strip()
                if "timeout" in stderr.lower() or "not ready" in stderr.lower():
                    log.info(
                        "Conductor %s busy again during drain, will retry",
                        session,
                    )
                else:
                    log.error(
                        "Failed to deliver queued message to %s: %s — dropping",
                        session, stderr,
                    )
                    items.popleft()
                    if not items:
                        _message_queue.pop(session, None)
                    if reply_callback is not None:
                        loop.create_task(_fire_callback(
                            reply_callback,
                            f"[Queued message could not be delivered — send failed: {stderr[:100]}]",
                        ))

        # Exit check AFTER the session loop — avoids missing items enqueued during drain
        if not _message_queue:
            log.info("Queue drain task finished (queue empty)")
            return


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


async def ensure_conductor_running(name: str, profile: str) -> bool:
    """Ensure the conductor session exists and is running."""
    session_title = conductor_session_title(name)
    loop = asyncio.get_running_loop()
    status = await loop.run_in_executor(
        None, functools.partial(get_session_status, session_title, profile=profile)
    )

    if status not in ("waiting", "running", "idle", "active", "starting"):
        log.info("Conductor %s not running (status=%s), attempting to start...", name, status)
        # Try starting first (session might exist but be stopped)
        result = await loop.run_in_executor(
            None, functools.partial(
                run_cli, "session", "start", session_title, profile=profile, timeout=60
            )
        )
        if result.returncode != 0:
            # Session might not exist, try creating it
            log.info("Creating conductor session for %s...", name)
            session_path = str(CONDUCTOR_DIR / name)
            result = await loop.run_in_executor(
                None, functools.partial(
                    run_cli,
                    "add", session_path,
                    "-t", session_title,
                    "-c", "claude",
                    "-g", "conductor",
                    profile=profile,
                    timeout=60,
                )
            )
            if result.returncode != 0:
                log.error(
                    "Failed to create conductor %s: %s",
                    name,
                    result.stderr.strip(),
                )
                return False
            # Start the newly created session
            await loop.run_in_executor(
                None, functools.partial(
                    run_cli, "session", "start", session_title, profile=profile, timeout=60
                )
            )

        # Wait a moment for the session to initialize (non-blocking)
        await asyncio.sleep(5)
        final_status = await loop.run_in_executor(
            None, functools.partial(get_session_status, session_title, profile=profile)
        )
        return final_status not in ("error", "unknown")

    return True


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
# Telegram message splitting
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
    default_conductor = get_default_conductor()
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
        await ensure_bot_info(message.bot)
        if not is_bot_addressed(message):
            return

        # Strip @botname mention from group messages
        text = strip_bot_mention(message.text)
        if not text:
            return

        # Determine target conductor from message prefix
        conductor_names = get_conductor_names()
        conductors = discover_conductors()
        target_name, cleaned_msg = parse_conductor_prefix(text, conductor_names)

        target_conductor = None
        if target_name:
            target_conductor = next(
                (c for c in conductors if c["name"] == target_name), None
            )
        if target_conductor is None:
            target_conductor = get_default_conductor()
        if target_conductor is None:
            await message.answer(
                "[No conductors configured. Run: agent-deck conductor setup]"
            )
            return

        target_profile = target_conductor["profile"]
        if not cleaned_msg:
            cleaned_msg = text

        session_title = conductor_session_title(target_conductor["name"])

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
        if not await ensure_conductor_running(target_conductor["name"], target_profile):
            await message.answer(
                f"[Could not start conductor for {target_profile}. Check agent-deck.]"
            )
            return

        profiles = get_unique_profiles()
        profile_tag = f"[{target_profile}] " if len(profiles) > 1 else ""

        # Check if conductor is busy — non-blocking via executor
        loop = asyncio.get_running_loop()
        conductor_status = await loop.run_in_executor(
            None, functools.partial(get_session_status, session_title, profile=target_profile)
        )
        was_busy = conductor_status in ("running", "active", "starting")

        log.info("User message -> [%s]: %s", target_profile, cleaned_msg[:100])

        if was_busy:
            tg_bot = message.bot
            tg_chat_id = message.chat.id
            profile_tag_captured = profile_tag
            enqueued_at = time.monotonic()

            async def _tg_reply(response_text: str):
                elapsed = int(time.monotonic() - enqueued_at)
                waited = f"{elapsed // 60}m {elapsed % 60}s" if elapsed >= 60 else f"{elapsed}s"
                header = (
                    f"{profile_tag_captured}Queued response (waited {waited}):\n"
                    if profile_tag_captured
                    else f"Queued response (waited {waited}):\n"
                )
                html = md_to_tg_html(f"{header}{response_text}")
                for chunk in split_message(html):
                    await tg_bot.send_message(tg_chat_id, chunk, parse_mode="HTML")

            ok, _ = send_to_conductor(
                session_title,
                cleaned_msg,
                profile=target_profile,
                wait_for_reply=False,
                reply_callback=_tg_reply,
                force_queue=True,
            )
            if not ok:
                await message.answer(
                    f"[Failed to send message to conductor [{target_profile}].]"
                )
                return
            await message.answer(
                f"{profile_tag}\u23f3 Conductor busy \u2014 message queued, will reply here when done."
            )
            return

        # Conductor is free — send and wait for reply (non-blocking via executor)
        await message.answer(f"{profile_tag}\u23f3")  # typing indicator before blocking
        ok, response = await loop.run_in_executor(
            None,
            functools.partial(
                send_to_conductor,
                session_title,
                cleaned_msg,
                profile=target_profile,
                wait_for_reply=True,
                response_timeout=RESPONSE_TIMEOUT,
            ),
        )
        if not ok:
            await message.answer(
                f"[Failed to send message to conductor [{target_profile}].]"
            )
            return

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

        if not await ensure_conductor_running(target["name"], profile):
            await _safe_say(
                say,
                text=f"[Could not start conductor {target['name']}. Check agent-deck.]",
                thread_ts=thread_ts,
            )
            return

        # Check if conductor is busy — non-blocking via executor
        loop = asyncio.get_running_loop()
        conductor_status = await loop.run_in_executor(
            None, functools.partial(get_session_status, session_title, profile=profile)
        )
        was_busy = conductor_status in ("running", "active", "starting")

        log.info("Slack message -> [%s]: %s", target["name"], cleaned_msg[:100])

        name_tag = f"[{target['name']}] " if len(conductors) > 1 else ""

        if was_busy:
            name_tag_captured = name_tag
            enqueued_at = time.monotonic()

            async def _slack_reply(response_text: str):
                elapsed = int(time.monotonic() - enqueued_at)
                waited = f"{elapsed // 60}m {elapsed % 60}s" if elapsed >= 60 else f"{elapsed}s"
                header = (
                    f"{name_tag_captured}Queued response (waited {waited}):\n"
                    if name_tag_captured
                    else f"Queued response (waited {waited}):\n"
                )
                chunks = split_message(response_text, max_len=SLACK_MAX_LENGTH)
                for i, chunk in enumerate(chunks):
                    text = f"{header}{chunk}" if i == 0 else chunk
                    await _safe_say(say, text=text, thread_ts=thread_ts)

            ok, _ = send_to_conductor(
                session_title, cleaned_msg, profile=profile,
                wait_for_reply=False, reply_callback=_slack_reply,
                force_queue=True,
            )
            if not ok:
                await _safe_say(
                    say,
                    text=f"[Failed to send message to conductor {target['name']}.]",
                    thread_ts=thread_ts,
                )
                return
            await _safe_say(
                say,
                text=f"{name_tag}\u23f3 Conductor busy \u2014 message queued, will reply here when done.",
                thread_ts=thread_ts,
            )
            return

        await _safe_say(say, text=f"{name_tag}\u23f3", thread_ts=thread_ts)  # before blocking
        ok, response = await loop.run_in_executor(
            None,
            functools.partial(
                send_to_conductor,
                session_title, cleaned_msg, profile=profile,
                wait_for_reply=True, response_timeout=RESPONSE_TIMEOUT,
            ),
        )
        if not ok:
            await _safe_say(
                say,
                text=f"[Failed to send message to conductor {target['name']}.]",
                thread_ts=thread_ts,
            )
            return

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
            await respond("\u26d4 Unauthorized. Contact your administrator.")
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
            await respond("\u26d4 Unauthorized. Contact your administrator.")
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
            await respond("\u26d4 Unauthorized. Contact your administrator.")
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
            await respond("\u26d4 Unauthorized. Contact your administrator.")
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


async def heartbeat_loop(config: dict, telegram_bot=None, slack_app=None, slack_channel_id=None):
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

        conductors = discover_conductors()
        for conductor in conductors:
            try:
                name = conductor["name"]
                profile = conductor["profile"]

                # Skip conductors with heartbeat disabled
                if not conductor.get("heartbeat_enabled", True):
                    log.debug("Heartbeat skipped for %s (disabled)", name)
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
                # Append HEARTBEAT_RULES.md (per-conductor, per-profile, then global fallback)
                rules_text = None
                for rules_path in [
                    CONDUCTOR_DIR / name / "HEARTBEAT_RULES.md",
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
                        log.info("Heartbeat [%s]: gated by pre-heartbeat hook", name)
                        continue
                    if stdout:
                        heartbeat_msg = stdout

                # Ensure conductor is running for this profile
                if not await ensure_conductor_running(name, profile):
                    log.error(
                        "Heartbeat [%s]: conductor not running, skipping",
                        name,
                    )
                    continue

                # Check if conductor is busy — skip heartbeat if so
                # (heartbeats are periodic; no point queueing them)
                loop = asyncio.get_running_loop()
                conductor_status = await loop.run_in_executor(
                    None,
                    functools.partial(get_session_status, session_title, profile=profile),
                )
                if conductor_status in ("running", "active", "starting"):
                    log.info(
                        "Heartbeat [%s]: conductor busy (%s), skipping this cycle",
                        name, conductor_status,
                    )
                    continue

                # Send heartbeat to conductor (wrapped in executor — blocks up to
                # RESPONSE_TIMEOUT seconds and must not freeze the event loop)
                loop = asyncio.get_running_loop()
                ok, response = await loop.run_in_executor(
                    None,
                    functools.partial(
                        send_to_conductor,
                        session_title,
                        heartbeat_msg,
                        profile=profile,
                        wait_for_reply=True,
                        response_timeout=RESPONSE_TIMEOUT,
                    ),
                )
                if not ok:
                    log.error(
                        "Heartbeat [%s]: failed to send to conductor",
                        name,
                    )
                    continue

                # Response is returned directly by `session send --wait`.
                log.info(
                    "Heartbeat [%s] response: %s",
                    name, response[:200],
                )

                # If conductor flagged items needing attention, notify via Telegram and Slack
                has_alerts = "NEED:" in response
                if has_alerts:
                    all_conductors = discover_conductors()
                    prefix = (
                        f"[{name}] " if len(all_conductors) > 1 else ""
                    )
                    alert_text = f"{prefix}Conductor alert:\n{response}"

                    # Notify via Telegram (with HTML formatting)
                    if telegram_bot and tg_user_id:
                        try:
                            alert_html = md_to_tg_html(alert_text)
                            for chunk in split_message(alert_html):
                                await telegram_bot.send_message(
                                    tg_user_id,
                                    chunk,
                                    parse_mode="HTML",
                                )
                        except Exception as e:
                            log.error(
                                "Failed to send Telegram notification: %s", e
                            )

                    # Notify via Slack
                    if slack_app and slack_channel_id:
                        try:
                            await slack_app.client.chat_postMessage(
                                channel=slack_channel_id, text=alert_text,
                            )
                        except Exception as e:
                            log.error(
                                "Failed to send Slack notification: %s", e
                            )

                # Run post-heartbeat hook (non-gating)
                invoke_hook(profile, "post-heartbeat", {
                    "profile": profile,
                    "response": response,
                    "has_alerts": has_alerts,
                })

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
        if await ensure_conductor_running(c["name"], c["profile"]):
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
