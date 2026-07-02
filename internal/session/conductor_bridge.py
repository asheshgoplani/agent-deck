#!/usr/bin/env python3
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
from pathlib import Path
from typing import Any, Callable, Coroutine

import toml

# Conditional imports for Telegram
try:
    from aiogram import Bot, Dispatcher, types
    from aiogram.filters import Command, CommandStart
    from aiogram.client.session.aiohttp import AiohttpSession
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

# --- issue #1350: XDG path resolution (mirror of internal/agentpaths) ---
# The Go side (internal/agentpaths) resolves agent-deck paths XDG-first with a
# legacy ~/.agent-deck fallback. bridge.py must mirror that exactly, or on a
# fresh XDG install the Go side writes conductors/config under XDG while the
# bridge reads ~/.agent-deck -> routing dies (issue #1350). Keep this region
# byte-identical with the embedded copy in conductor_templates.go.
APP_DIR_NAME = "agent-deck"


def _xdg_dir(env_name: str, *fallback_parts: str) -> Path:
    """Mirror agentpaths.xdgDir: $XDG_*/agent-deck if absolute, else ~/<fallback>/agent-deck."""
    value = os.environ.get(env_name, "").strip()
    if value and os.path.isabs(value):
        return Path(value) / APP_DIR_NAME
    return Path.home().joinpath(*fallback_parts, APP_DIR_NAME)


def _legacy_dir() -> Path:
    """Mirror agentpaths.LegacyDir: ~/.agent-deck."""
    return Path.home() / ".agent-deck"


def resolve_config_path(name: str) -> Path:
    """Mirror agentpaths.EffectiveConfigPath: XDG config file if it exists, else
    legacy file if it exists, else default XDG path."""
    base = os.path.basename(name)
    xdg_path = _xdg_dir("XDG_CONFIG_HOME", ".config") / base
    if xdg_path.exists():
        return xdg_path
    legacy_path = _legacy_dir() / base
    if legacy_path.exists():
        return legacy_path
    return xdg_path


def resolve_data_dir(*markers: str) -> Path:
    """Mirror agentpaths.EffectiveDataDir: return the XDG data dir if any marker
    exists there, else legacy if any marker exists there, else default XDG.
    The returned path is the agent-deck data root; callers join the marker."""
    data_dir = _xdg_dir("XDG_DATA_HOME", ".local", "share")
    clean = [m for m in markers if m]
    if not clean:
        return data_dir
    if any((data_dir / m).exists() for m in clean):
        return data_dir
    legacy = _legacy_dir()
    if any((legacy / m).exists() for m in clean):
        return legacy
    return data_dir


# Prefer the [conductor].dir override injected by the Go side as
# AGENT_DECK_CONDUCTOR_DIR (frozen into the daemon env at install time). When
# unset, fall back to the byte-identical issue #1350 XDG/legacy resolver.
_override = os.environ.get("AGENT_DECK_CONDUCTOR_DIR", "").strip()
CONDUCTOR_DIR = Path(os.path.expanduser(_override)) if _override else resolve_data_dir("conductor") / "conductor"
CONFIG_PATH = resolve_config_path("config.toml")
# --- end issue #1350 resolver ---
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

# Mirror the deployed bridge's file logging (<data>/conductor/bridge.log) when
# the conductor data dir already exists. Guard so importing this module in an
# environment without that dir (e.g. CI/tests) never fails at import time.
_log_handlers = [logging.StreamHandler(sys.stdout)]
try:
    if CONDUCTOR_DIR.exists():
        _log_handlers.append(logging.FileHandler(LOG_PATH, encoding="utf-8"))
except OSError:
    pass

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    handlers=_log_handlers,
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

    # The conductor system is "active" when at least one conductor exists on
    # disk (meta.json under CONDUCTOR_DIR), mirroring ConductorSystemActive()
    # on the Go side. The legacy [conductor].enabled flag has been removed
    # (#1361); it was write-once-true and its only reachable "off" value
    # silently killed the bridge daemon. Old configs that still carry
    # `enabled = false` no longer disable the bridge.
    if not discover_conductors():
        log.error(
            "No conductors found under %s; run 'agent-deck conductor setup <name>'",
            CONDUCTOR_DIR,
        )
        sys.exit(1)

    # Telegram config
    tg = conductor_cfg.get("telegram", {})
    tg_token = _resolve_secret(tg.get("token", ""))
    # Resolve user_id like the token so it may be an env-var reference
    # (e.g. "$TELEGRAM_USER_ID" / "${TELEGRAM_USER_ID}"); a literal integer
    # in config.toml still works. Empty/unset resolves to "" -> int 0 below.
    tg_user_id = _resolve_secret(str(tg.get("user_id", "") or ""))
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
    # Resolve user_id like the bot token so it may be an env-var reference
    # (e.g. "$DISCORD_USER_ID"); a literal integer in config.toml still works.
    dc_user_id = _resolve_secret(str(dc.get("user_id", "") or ""))
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


# Async callable type for reply notifications: (response_text: str) -> None
ReplyCallback = Callable[[str], Coroutine[Any, Any, None]]


def _is_still_running_timeout(stderr: str) -> bool:
    """True when a blocking `--wait` failed *only* because the turn outran the
    timeout while the agent keeps working — the message WAS delivered.

    The CLI reports this with stderr like:
        "timeout waiting for completion: agent still running after 5m0s"

    Distinguishing this benign case from a genuine send failure lets callers
    deliver the reply asynchronously instead of reporting a false failure.
    """
    s = stderr.lower()
    return "timeout waiting for completion" in s or "still running" in s


def send_to_conductor(
    session: str,
    message: str,
    profile: str | None = None,
    wait_for_reply: bool = False,
    response_timeout: int = RESPONSE_TIMEOUT,
    reply_callback: ReplyCallback | None = None,
    force_queue: bool = False,
) -> tuple[bool, str, bool]:
    """Send a message to the conductor session.

    Returns (success, response_text, still_running). When wait_for_reply=False,
    response_text is "". still_running is True only on the wait path when the
    blocking `--wait` timed out because the agent is still working (the message
    was delivered and the reply should be awaited asynchronously); it is False
    in every other case.

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
            return True, "", False

        # For non-blocking sends (user messages), check if conductor is busy
        # and queue instead of dropping.
        status = get_session_status(session, profile=profile)
        if status in ("running", "active", "starting"):
            log.info(
                "Conductor %s is busy (%s), queueing message", session, status,
            )
            _enqueue_message(session, message, profile, reply_callback)
            return True, "", False  # queued, not failed

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
                return True, "", False
            log.error("Failed to send to conductor: %s", stderr)
            return False, "", False
        return True, "", False

    # wait_for_reply=True: single-call flow used by heartbeats and the idle
    # user-message path. `--wait` blocks until the assistant's reply is flushed;
    # we then re-fetch the clean reply via get_session_output (`session output
    # --json` -> content), rather than parsing the raw `--wait` pane capture.
    # This mirrors the deployed bridge's reply-capture (issue #926).
    result = run_cli(
        "session", "send", session, message,
        "--wait", "--timeout", f"{response_timeout}s", "-q",
        profile=profile,
        timeout=max(response_timeout + 30, 60),
    )
    if result.returncode != 0:
        stderr = result.stderr.strip()
        # The turn outran --timeout but the message was delivered and the agent
        # is still working. Signal still_running so the caller can await the
        # reply asynchronously rather than reporting a false failure.
        if _is_still_running_timeout(stderr):
            log.info(
                "Conductor %s: --wait timed out but agent still running "
                "(message delivered, reply pending): %s",
                session, stderr,
            )
            return False, "", True
        log.error("Failed to send to conductor: %s", stderr)
        return False, "", False
    return True, get_session_output(session, profile=profile), False


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
                    # Re-fetch the clean reply via get_session_output (consistent
                    # with send_to_conductor's wait path) rather than the raw
                    # `--wait` stdout. Off-loop to avoid blocking the drain.
                    output = await loop.run_in_executor(
                        None,
                        functools.partial(get_session_output, session, profile=profile),
                    )
                    text = output.strip() or "[No output from conductor.]"
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


# ---------------------------------------------------------------------------
# Pending reply watchers for in-flight turns
# ---------------------------------------------------------------------------
#
# When the conductor is IDLE on arrival the handler delivers the message with a
# blocking `session send --wait --timeout {RESPONSE_TIMEOUT}s`. If that single
# turn outruns the timeout the message is already delivered and the agent keeps
# working — only the synchronous reply is lost. send_to_conductor flags this
# (still_running=True); the handler then registers a reply-only watcher here.
#
# Unlike _drain_queue this NEVER sends a message — the message is already
# in-flight, so re-sending would double-process it. The watcher only polls
# until the turn finishes and delivers the captured output via reply_callback.

# Generous ceiling: the whole point is the turn already outran RESPONSE_TIMEOUT,
# so wait well beyond it before giving up.
PENDING_REPLY_MAX_WAIT = 3600  # seconds
PENDING_REPLY_POLL_INTERVAL = 5  # seconds

# Keeps watcher tasks referenced so the event loop doesn't GC them mid-flight.
_pending_reply_tasks: set[asyncio.Task] = set()


async def _watch_pending_reply(
    session: str,
    profile: str | None,
    reply_callback: ReplyCallback,
) -> None:
    """Wait for an in-flight conductor turn to finish, then deliver its output.

    Used when a blocking `--wait` send timed out because the agent is still
    running (not a send failure). The message was already delivered, so this
    does NOT re-send — it polls until the conductor leaves the busy state and
    then fires reply_callback exactly once with the captured output.

    Mirrors _drain_queue's polling/backoff but never sends a message. Caps the
    total wait at PENDING_REPLY_MAX_WAIT and logs if it gives up.
    """
    loop = asyncio.get_running_loop()
    max_polls = max(1, PENDING_REPLY_MAX_WAIT // PENDING_REPLY_POLL_INTERVAL)
    for _ in range(max_polls):
        status = await loop.run_in_executor(
            None, functools.partial(get_session_status, session, profile=profile),
        )
        # Still working, or a transient CLI failure — keep waiting. This also
        # naturally handles the race where the conductor finishes between the
        # timeout and this first poll: a non-busy status falls straight through
        # to fetching the output below.
        if status in ("running", "active", "starting", "unknown"):
            await asyncio.sleep(PENDING_REPLY_POLL_INTERVAL)
            continue
        # The turn is no longer running (idle/waiting/error/...) — fetch whatever
        # output is available and deliver it once.
        output = await loop.run_in_executor(
            None, functools.partial(get_session_output, session, profile=profile),
        )
        text = output.strip() or "[No output from conductor.]"
        await _fire_callback(reply_callback, text)
        log.info(
            "Pending reply for %s delivered after in-flight turn finished", session,
        )
        return

    log.warning(
        "Pending reply watcher for %s gave up after %ds — turn still running",
        session, PENDING_REPLY_MAX_WAIT,
    )
    await _fire_callback(
        reply_callback,
        "[Conductor is still working after a long time — reply not captured. "
        "Check the session directly.]",
    )


def _register_pending_reply(
    session: str,
    profile: str | None,
    reply_callback: ReplyCallback,
) -> None:
    """Schedule a reply-only watcher for an in-flight turn (no message re-send).

    Safe to call from a running-loop context; skips with a warning if there is
    no event loop (e.g. called from a bare sync context).
    """
    try:
        loop = asyncio.get_running_loop()
    except RuntimeError:
        log.warning("No running event loop — cannot watch for pending reply on %s", session)
        return
    task = loop.create_task(_watch_pending_reply(session, profile, reply_callback))
    _pending_reply_tasks.add(task)
    task.add_done_callback(_pending_reply_tasks.discard)


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


def _find_session_by_title(sessions: list, title: str, profile: str) -> dict | None:
    """Find an exact title match from a profile-scoped session list."""
    for session in sessions:
        if not isinstance(session, dict):
            continue
        if session.get("title") != title:
            continue
        session_profile = session.get("profile")
        if session_profile in (None, "", profile):
            return session
    return None


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
            log.warning(
                "Failed to start conductor %s before dedupe: %s",
                name,
                result.stderr.strip(),
            )
            sessions = await loop.run_in_executor(
                None, functools.partial(get_sessions_list, profile=profile)
            )
            existing = _find_session_by_title(sessions, session_title, profile)
            if existing is not None:
                log.info(
                    "Reusing existing conductor session %s in profile %s",
                    session_title,
                    profile,
                )
                retry = await loop.run_in_executor(
                    None, functools.partial(
                        run_cli,
                        "session",
                        "start",
                        session_title,
                        profile=profile,
                        timeout=60,
                    )
                )
                if retry.returncode != 0:
                    log.warning(
                        "Failed to start existing conductor %s: %s",
                        name,
                        retry.stderr.strip(),
                    )
            else:
                # Session is absent from this profile, so create it.
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
                        run_cli,
                        "session",
                        "start",
                        session_title,
                        profile=profile,
                        timeout=60,
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
# NEED-line retire (issue #971)
# ---------------------------------------------------------------------------

# Default: after this many *consecutive* identical NEED: lines, escalate
# once with a distinct "STILL BLOCKED" tactic, then drop on later cycles.
NEED_RETIRE_THRESHOLD = 3


def filter_need_lines(
    response: str,
    prev_counts: dict,
    threshold: int = NEED_RETIRE_THRESHOLD,
) -> dict:
    """De-duplicate consecutive identical heartbeat NEED: lines (issue #971).

    Args:
      response: full conductor reply text (may contain zero or more NEED: lines).
      prev_counts: per-line consecutive-occurrence counts from the previous
        heartbeat cycle, keyed by the trimmed NEED: line text.
      threshold: how many consecutive cycles of an identical NEED: line trigger
        a one-shot escalation. Subsequent cycles drop the line entirely.

    Returns dict with:
      "alerts":  list[str]  — NEED lines to forward as-is this cycle.
      "retired": list[str]  — one-shot escalation notices for lines that just
                              hit threshold (forwarded instead of the plain
                              NEED line so the user sees the tactic change).
      "counts":  dict[str,int] — updated counts for the next cycle. Lines no
                              longer present are dropped (reset on return).

    Rules (matches issue #971's expected table):
      * Cycles 1 .. threshold-1: NEED line is forwarded as-is.
      * Cycle threshold:         line moves to "retired" (escalation tactic
                                  change, e.g. "STILL BLOCKED for 3h: ...").
      * Cycle threshold+1..:    line is silently dropped (auto-retire).
    """
    counts: dict[str, int] = {}
    alerts: list[str] = []
    retired: list[str] = []

    for raw_line in response.splitlines():
        line = raw_line.strip()
        if not line.startswith("NEED:"):
            continue

        prior = prev_counts.get(line, 0)
        new_count = prior + 1
        counts[line] = new_count

        if new_count < threshold:
            alerts.append(line)
        elif new_count == threshold:
            retired.append(
                f"STILL BLOCKED ({threshold} cycles, no reply): {line}"
            )
        # new_count > threshold: dropped — already retired previously.

    return {"alerts": alerts, "retired": retired, "counts": counts}


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
# Discord bot setup
# ---------------------------------------------------------------------------


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
    """Send Discord output, uploading [IMAGE:/path] markers as attachments.

    The optional name_tag prefix is applied to the FIRST emitted segment only
    (matching the Telegram/Slack handlers), not repeated on every chunk.
    """
    prefix = name_tag if name_tag else ""
    prefix_applied = False

    def _apply_prefix(body: str) -> str:
        nonlocal prefix_applied
        if prefix and not prefix_applied:
            prefix_applied = True
            return f"{prefix}{body}"
        return body

    for part_type, payload in parse_discord_message_parts(text):
        if part_type == "text":
            if not payload.strip():
                continue
            for chunk in split_message(payload, max_len=DISCORD_MAX_LENGTH):
                await channel.send(_apply_prefix(chunk))
            continue

        image_path = Path(payload).expanduser()
        if not image_path.is_absolute():
            await channel.send(_apply_prefix(f"[Image path must be absolute: {payload}]"))
            continue
        if not image_path.is_file():
            await channel.send(_apply_prefix(f"[Image not found: {image_path}]"))
            continue

        try:
            # Carry the prefix as the upload's message content only once.
            content = None
            if prefix and not prefix_applied:
                content = prefix.strip()
                prefix_applied = True
            await channel.send(
                content=content,
                file=discord.File(str(image_path)),
            )
        except Exception as e:
            log.error("Failed to upload Discord image %s: %s", image_path, e)
            await channel.send(_apply_prefix(f"[Failed to upload image: {image_path}]"))


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

    # Configure aiohttp session with proxy if HTTP_PROXY is set in environment.
    # Required for environments where direct access to Telegram API is blocked
    # (e.g. mainland China, corporate networks).
    # Note: aiogram requires 'aiohttp-socks' for proxy support.
    proxy_url = (
        os.environ.get("HTTPS_PROXY")
        or os.environ.get("https_proxy")
        or os.environ.get("HTTP_PROXY")
        or os.environ.get("http_proxy")
    )
    if proxy_url:
        log.info("Using proxy for Telegram bot: %s", proxy_url)
        session = AiohttpSession(proxy=proxy_url)
        bot = Bot(token=config["telegram"]["token"], session=session)
    else:
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

            ok, _, _ = send_to_conductor(
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
        wait_started_at = time.monotonic()
        ok, response, still_running = await loop.run_in_executor(
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
            if still_running:
                # The message WAS delivered; the single turn just outran the
                # blocking wait. Don't report a false failure and don't re-send
                # (that would double-process) — watch for the reply async-ly.
                tg_bot = message.bot
                tg_chat_id = message.chat.id
                profile_tag_captured = profile_tag

                async def _tg_late_reply(response_text: str):
                    elapsed = int(time.monotonic() - wait_started_at)
                    waited = f"{elapsed // 60}m {elapsed % 60}s" if elapsed >= 60 else f"{elapsed}s"
                    header = (
                        f"{profile_tag_captured}Queued response (waited {waited}):\n"
                        if profile_tag_captured
                        else f"Queued response (waited {waited}):\n"
                    )
                    html = md_to_tg_html(f"{header}{response_text}")
                    for chunk in split_message(html):
                        await tg_bot.send_message(tg_chat_id, chunk, parse_mode="HTML")

                _register_pending_reply(session_title, target_profile, _tg_late_reply)
                await message.answer(
                    f"{profile_tag}⏳ Still working — will reply here when done."
                )
                return
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
        text = re.sub(r"```[\s\S]*?```", _save_code_block, text)

        # Protect inline code.
        inline_codes = []
        def _save_inline_code(m):
            inline_codes.append(m.group(0))
            return f"__INLINE_CODE_{len(inline_codes) - 1}__"
        text = re.sub(r"`[^`\n]+`", _save_inline_code, text)

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

            ok, _, _ = send_to_conductor(
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
        wait_started_at = time.monotonic()
        ok, response, still_running = await loop.run_in_executor(
            None,
            functools.partial(
                send_to_conductor,
                session_title, cleaned_msg, profile=profile,
                wait_for_reply=True, response_timeout=RESPONSE_TIMEOUT,
            ),
        )
        if not ok:
            if still_running:
                # The message WAS delivered; the single turn just outran the
                # blocking wait. Don't report a false failure and don't re-send
                # (that would double-process) \u2014 watch for the reply async-ly.
                name_tag_captured = name_tag

                async def _slack_late_reply(response_text: str):
                    elapsed = int(time.monotonic() - wait_started_at)
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

                _register_pending_reply(session_title, profile, _slack_late_reply)
                await _safe_say(
                    say,
                    text=f"{name_tag}\u23f3 Still working \u2014 will reply here when done.",
                    thread_ts=thread_ts,
                )
                return
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
            user_id=user_id, event_channel=event.get("channel"),
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
            user_id=user_id, event_channel=event.get("channel"),
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

    def build_discord_context_tag(message: discord.Message) -> str:
        """Build a `[from:... (id)] [channel:#... (id)|thread:... in #...|dm]` prefix
        so the conductor knows which Discord channel/thread/DM a message came from.
        Mirrors the Slack tagging convention (see resolve_slack_channel)."""
        author = message.author
        author_name = getattr(author, "display_name", None) or getattr(author, "name", "?")
        from_tag = f"[from:{author_name} ({author.id})]"
        channel = message.channel
        ct = getattr(channel, "type", None)
        thread_types = (
            getattr(discord.ChannelType, "public_thread", None),
            getattr(discord.ChannelType, "private_thread", None),
            getattr(discord.ChannelType, "news_thread", None),
        )
        if ct == getattr(discord.ChannelType, "private", None):
            chan_tag = "[dm]"
        elif ct in thread_types:
            parent = getattr(channel, "parent", None)
            parent_name = f"#{parent.name}" if parent and getattr(parent, "name", None) else "?"
            chan_tag = f"[thread:#{channel.name} ({channel.id}) in {parent_name}]"
        else:
            chan_name = getattr(channel, "name", "?")
            chan_tag = f"[channel:#{chan_name} ({channel.id})]"
        return f"{from_tag} {chan_tag}"

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
            "`/ad-status`    - Aggregated status across all profiles\n"
            "`/ad-sessions`  - List all sessions (all profiles)\n"
            "`/ad-restart`   - Restart a conductor (specify name)\n"
            "`/ad-help`      - This message\n\n"
            f"**Conductors:** {', '.join(names) if names else 'none'}\n"
            f"**Route:** `<name>: <message>`\n"
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

        # Prepend Discord channel/thread/DM context so the conductor knows
        # where the message came from (mirrors the Slack tagging convention).
        cleaned_msg = f"{build_discord_context_tag(message)} {cleaned_msg}"

        session_title = conductor_session_title(target["name"])
        profile = target["profile"]

        if not await ensure_conductor_running(target["name"], profile):
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
            ok, response, still_running = await loop.run_in_executor(
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
            if still_running:
                # The message WAS delivered; the single turn just outran the
                # blocking wait. Don't report a false failure and don't re-send
                # (that would double-process) — watch for the reply async-ly.
                # Mirrors the Telegram/Slack idle paths (#1404).
                dc_channel = message.channel
                dc_name_tag = (
                    f"[{target['name']}] " if len(conductors) > 1 else ""
                )

                async def _dc_late_reply(response_text: str):
                    await send_discord_output(
                        dc_channel, response_text, name_tag=dc_name_tag,
                    )

                _register_pending_reply(session_title, profile, _dc_late_reply)
                await message.channel.send(
                    "⏳ Still working — will reply here when done.",
                )
                return
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

    # Per-conductor NEED: dedup state for issue #971 — tracks consecutive
    # identical NEED lines so we can escalate-once-then-drop instead of
    # firing the same alert verbatim for 12+ hours.
    need_state_by_conductor: dict[str, dict] = {}

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

                # Scope heartbeat monitoring to this conductor's own group
                # (mirrors the deployed bridge: per-conductor, not profile-wide).
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

                # Build heartbeat message with waiting/error session details
                waiting_details = []
                error_details = []
                for s in scoped_sessions:
                    s_title = s.get("title", "untitled")
                    s_status = s.get("status", "")
                    s_path = s.get("path", "")
                    if s_status == "waiting":
                        waiting_details.append(f"{s_title} (project: {s_path})")
                    elif s_status == "error":
                        error_details.append(f"{s_title} (project: {s_path})")

                parts = [
                    f"[HEARTBEAT] [{name}] Status: {waiting} waiting, "
                    f"{running} running, {idle} idle, {error} error, {stopped} stopped."
                ]
                if waiting_details:
                    parts.append(f"Waiting sessions: {', '.join(waiting_details)}.")
                if error_details:
                    parts.append(f"Error sessions: {', '.join(error_details)}.")
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
                    parts.append("Check if any need auto-response or user attention.")

                heartbeat_msg = " ".join(parts)

                # Run pre-heartbeat hook (can transform or gate the message)
                sessions_for_hook = [
                    {"title": s.get("title", ""), "status": s.get("status", ""), "path": s.get("path", "")}
                    for s in scoped_sessions
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
                ok, response, _ = await loop.run_in_executor(
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

                # Response is captured via get_session_output (see send_to_conductor).
                log.info(
                    "Heartbeat [%s] response: %s",
                    name, response[:200],
                )

                # Dedup repeating NEED: lines (issue #971). Forward only
                # fresh + escalation lines; drop verbatim repeats past
                # threshold so the user isn't trained to ignore heartbeats.
                prev_counts = need_state_by_conductor.get(name, {})
                need_filtered = filter_need_lines(response, prev_counts)
                need_state_by_conductor[name] = need_filtered["counts"]

                forwarded_need_lines = (
                    need_filtered["alerts"] + need_filtered["retired"]
                )
                has_alerts = bool(forwarded_need_lines)
                if need_filtered["retired"]:
                    log.info(
                        "Heartbeat [%s]: retiring %d stale NEED line(s) "
                        "after >= %d cycles: %s",
                        name,
                        len(need_filtered["retired"]),
                        NEED_RETIRE_THRESHOLD,
                        need_filtered["retired"],
                    )
                if has_alerts:
                    prefix = (
                        f"[{name}] " if len(all_conductors) > 1 else ""
                    )
                    alert_body = "\n".join(forwarded_need_lines)
                    alert_msg = f"{prefix}Conductor alert:\n{alert_body}"

                    # Notify via Telegram (with HTML formatting)
                    if telegram_bot and tg_user_id:
                        try:
                            alert_html = md_to_tg_html(alert_msg)
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
                                channel=slack_channel_id, text=alert_msg,
                            )
                        except Exception as e:
                            log.error(
                                "Failed to send Slack notification: %s", e
                            )

                    # Notify via Discord
                    if discord_bot and discord_channel_id:
                        try:
                            channel = discord_bot.get_channel(discord_channel_id)
                            if channel:
                                await send_discord_output(channel, alert_msg)
                        except Exception as e:
                            log.error(
                                "Failed to send Discord notification: %s", e
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
        if await ensure_conductor_running(c["name"], c["profile"]):
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

    # Run all concurrently
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
