#!/usr/bin/env python3
"""
Conductor Bridge: Telegram & Slack & Discord & Mattermost <-> Agent-Deck conductor sessions (multi-conductor).

A thin bridge that:
  A) Forwards Telegram/Slack/Discord/Mattermost messages -> conductor session (via agent-deck CLI)
  B) Forwards conductor responses -> Telegram/Slack/Discord/Mattermost
  C) Runs a periodic heartbeat to trigger conductor status checks

Discovers conductors dynamically from meta.json files in ~/.agent-deck/conductor/*/
Each conductor has its own name, profile, and heartbeat settings.

Dependencies: pip3 install toml aiogram slack-bolt slack-sdk discord.py aiohttp
  - aiogram is only needed if Telegram is configured
  - slack-bolt/slack-sdk are only needed if Slack is configured
  - discord.py is only needed if Discord is configured
  - aiohttp is only needed if Mattermost is configured
"""

from __future__ import annotations

import contextlib
import asyncio
import functools
import hashlib
import json
import logging
import os
import re
import signal
import subprocess
import sys
import threading
import time
import urllib.parse
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

# Conditional import for Mattermost (its REST API and WebSocket, via aiohttp)
try:
    import aiohttp
    HAS_AIOHTTP = True
except ImportError:
    HAS_AIOHTTP = False

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

# Keep aligned with conductorAgentSpecs; setup persists these canonical names.
CONDUCTOR_AGENTS = frozenset({"claude", "codex", "hermes", "pi"})


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

# Mattermost post length limit (the server's default MaxPostSize is 16383)
MM_MAX_LENGTH = 16000

# Marker for uploading local images through the Discord bridge.
IMAGE_MARKER_RE = re.compile(r"\[IMAGE:(?P<path>[^\]]+)\]")

# How long to wait for conductor to respond (seconds)
RESPONSE_TIMEOUT = 300

# issue #1981 / #1999: the interactive-state guard skips a heartbeat while a
# picker or unsent draft is on screen. Its evidence is a single tmux pane
# capture, which can LIE — a stale glyph buffer (#1999) keeps showing composer
# text that is no longer really there and never repaints, so the guard would
# report "blocked" every cycle and silence the conductor forever. After this
# many CONSECUTIVE gated skips the heartbeat is delivered anyway (with a warning),
# so a stale buffer or a forgotten draft cannot starve heartbeats indefinitely.
HEARTBEAT_SKIP_LIMIT = 3

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

    Returns a dict with a nested sub-dict per platform (telegram, slack,
    discord, mattermost), each with a 'configured' flag.
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

    # Mattermost config. `user` is the one person the bot obeys, as a username
    # or a user ID; with no channel_id the bot talks to them in a DM.
    mm = conductor_cfg.get("mattermost", {})
    mm_server_url = str(mm.get("server_url", "") or "").strip().rstrip("/")
    mm_bot_token = _resolve_secret(mm.get("bot_token", ""))
    mm_user = _resolve_secret(str(mm.get("user", "") or "")).strip().lstrip("@")
    mm_allow_insecure_http = bool(mm.get("allow_insecure_http", False))
    mm_configured = bool(mm_server_url and mm_bot_token and mm_user)
    if mm_configured:
        problem = mattermost_url_problem(mm_server_url, mm_allow_insecure_http)
        if problem:
            log.error("[conductor.mattermost] ignored: %s", problem)
            mm_configured = False

    if not tg_configured and not sl_configured and not dc_configured and not mm_configured:
        log.error(
            "No messaging platform configured in config.toml. "
            "Set [conductor.telegram], [conductor.slack], [conductor.discord], "
            "or [conductor.mattermost]."
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
        "mattermost": {
            "server_url": mm_server_url,
            "bot_token": mm_bot_token,
            "user": mm_user,
            "channel_id": str(mm.get("channel_id", "") or "").strip(),
            "listen_mode": mm.get("listen_mode", "all"),  # "mentions" or "all"
            "allow_insecure_http": mm_allow_insecure_http,
            "configured": mm_configured,
        },
        "heartbeat_interval": conductor_cfg.get("heartbeat_interval", 15),
        # Fallback threshold for filter_need_lines when `conductor tier-filter`
        # is unavailable; the Go side reads the same key.
        "need_retire_cycles": _positive_int(
            conductor_cfg.get("need_retire_cycles"), NEED_RETIRE_THRESHOLD,
        ),
    }


def _positive_int(value, default: int) -> int:
    return value if isinstance(value, int) and not isinstance(value, bool) and value > 0 else default


class _JSONObject(dict):
    def __init__(self, pairs: list[tuple[str, Any]]) -> None:
        super().__init__(pairs)
        self.pairs = pairs


def _load_conductor_meta(meta_path: Path) -> dict | None:
    """Read one durable conductor record, rejecting malformed metadata."""
    try:
        with open(meta_path, encoding="utf-8") as f:
            meta = json.load(f, object_pairs_hook=_JSONObject)
    except (json.JSONDecodeError, UnicodeDecodeError, OSError) as e:
        log.warning("Failed to read %s: %s", meta_path, e)
        return None
    if not isinstance(meta, dict):
        log.warning("Invalid conductor metadata in %s: expected an object", meta_path)
        return None
    return meta


def _conductor_agent_from_meta(meta: dict, meta_path: Path) -> str | None:
    """Resolve a persisted runtime using encoding/json's field-name rules."""
    value = None
    # Match Go encoding/json: case-insensitive keys; type errors invalidate;
    # null is a no-op and later strings replace earlier ones.
    pairs = meta.pairs if isinstance(meta, _JSONObject) else meta.items()
    for key, candidate in pairs:
        if not isinstance(key, str) or key.lower() != "agent":
            continue
        if candidate is None:
            continue
        if not isinstance(candidate, str):
            log.warning("Invalid conductor agent in %s: expected a string", meta_path)
            return None
        value = candidate
    if value is None:
        return "claude"
    agent = value.strip().lower()
    if not agent:
        return "claude"
    if agent not in CONDUCTOR_AGENTS:
        log.warning("Unsupported conductor agent %r in %s", value, meta_path)
        return None
    return agent


def _load_conductor_agent(name: str) -> str | None:
    meta_path = CONDUCTOR_DIR / name / "meta.json"
    meta = _load_conductor_meta(meta_path)
    if meta is None:
        return None
    return _conductor_agent_from_meta(meta, meta_path)


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
                meta = _load_conductor_meta(meta_path)
                if meta is None:
                    continue
                agent = _conductor_agent_from_meta(meta, meta_path)
                if agent is None:
                    continue
                meta["agent"] = agent
                conductors.append(meta)
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
    *args: str, profile: str | None = None, timeout: int = 120,
    input_text: str | None = None,
) -> subprocess.CompletedProcess:
    """Run an agent-deck CLI command and return the result.

    If profile is provided, prepends -p <profile> to the command. input_text,
    when given, is written to the command's stdin.
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
            stdin=subprocess.PIPE if input_text is not None else None,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            start_new_session=True,  # own process group -> killpg kills grandchildren too
        )
        try:
            stdout, stderr = proc.communicate(input=input_text, timeout=timeout)
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



# Hook-driven statuses that mean "mid-turn / interactive", mirroring the Go
# send path's send.StatusIsBusy (internal/send/deferbusy.go) — the same
# signal `session send --defer-if-busy` and the post-#2273 verification loop
# already treat as authoritative. A fresh "running" hook status also covers
# an OPEN AskUserQuestion picker: its PreToolUse event writes "running" and
# nothing advances it to a Stop/PostToolUse event until the human answers, so
# the picker window reads as busy here even though the derived "status"
# field can still show "waiting" (issue #1981's original false-negative).
_HOOK_INTERACTIVE_STATUSES = {"running", "starting"}


def hook_driven_interactive(session: str, profile: str | None = None) -> tuple[bool, bool]:
    """Hook-driven busy/interactive signal for one session: (interactive, known).

    ``known`` is False whenever the signal cannot be trusted — the hook has
    never fired for this session, its last sample is stale, or the CLI call
    itself failed/parsed badly. Callers MUST treat known=False as "no
    evidence either way", never as "not interactive": that distinction is the
    whole point of gating on this instead of a raw pane-text guess (#2080
    review of #1981/#2043's heartbeat guard).
    """
    try:
        result = run_cli(
            "session", "show", session, "--json", profile=profile, timeout=15
        )
        if result.returncode != 0:
            return False, False
        data = json.loads(result.stdout)
    except (json.JSONDecodeError, OSError, ValueError):
        return False, False
    if not data.get("hook_status_fresh"):
        return False, False
    status = data.get("hook_status") or ""
    if not status:
        return False, False
    return status in _HOOK_INTERACTIVE_STATUSES, True


# Prefix of the placeholder get_session_output returns when the CLI read fails.
SESSION_OUTPUT_ERROR_PREFIX = "[Error getting output:"


def get_session_output(session: str, profile: str | None = None) -> str:
    """Get the last response from a session.

    Uses --json mode so we get the structured 'content' field (the actual
    assistant reply) instead of the raw pane capture (which includes the
    cosmetic frame / statusline at the top and can be mistaken for a reply).
    """
    return get_session_output_state(session, profile=profile)[0]


def get_session_output_state(
    session: str, profile: str | None = None,
) -> tuple[str, str]:
    """Return response text and its exact Codex thread:turn identity."""
    result = run_cli("session", "output", session, "--json", profile=profile, timeout=30)
    if result.returncode != 0:
        return f"{SESSION_OUTPUT_ERROR_PREFIX} {result.stderr.strip()}]", ""
    try:
        data = json.loads(result.stdout)
        return (
            (data.get("content") or "").strip(),
            data.get("codex_turn_generation") or "",
        )
    except json.JSONDecodeError:
        # Fallback: stdout might be the legacy raw-text format.
        return result.stdout.strip(), ""


def capture_pane(session: str, profile: str | None = None) -> str:
    """Raw tmux pane capture for a session, via ``session output --pane``.

    Unlike get_session_output (which returns the parsed "last response"), this
    returns the live pane content WITH ANSI/SGR escapes preserved — the only
    reliable way to see an open option-picker or the current composer contents.
    Returns "" on any failure so callers fail OPEN (never block on an unknown
    pane state).
    """
    result = run_cli(
        "session", "output", session, "--pane", "--json", profile=profile, timeout=15
    )
    if result.returncode != 0:
        return ""
    try:
        data = json.loads(result.stdout)
        return data.get("content") or ""
    except json.JSONDecodeError:
        # Legacy/quiet builds may print the raw pane directly.
        return result.stdout


# ---------------------------------------------------------------------------
# Interactive-state guard for automated sends (issue #1981)
# ---------------------------------------------------------------------------
#
# A routine/heartbeat ``session send`` types text and presses Enter. When the
# target Claude Code pane is mid-interaction, that Enter is destructive:
#
#   (a) an open AskUserQuestion option-picker resolves to its HIGHLIGHTED
#       default — the model receives an answer the user never gave; and
#   (b) a composer holding the user's half-typed input gets overwritten.
#
# Both states still report session status ``waiting`` (waiting fires on
# AskUserQuestion / EnterPlanMode), so status alone cannot gate the send. The
# helpers below inspect a raw pane capture and report whether an automated send
# would clobber live interaction, so the caller can skip that cycle. Every
# check fails OPEN: any capture/parse failure is treated as "safe to send".

# Matches a full CSI escape sequence (used to strip ANSI for plain-text scans).
_ANSI_RE = re.compile(r"\x1b\[[0-9;?]*[a-zA-Z]")
# Matches only an SGR sequence and captures its parameters (for dim detection).
_SGR_RE = re.compile(r"\x1b\[([0-9;]*)m")

# AskUserQuestion option-picker markers. The footer strings mirror the ones the
# Go prompt detector already keys on ("Press Enter to select" / "Use arrow keys
# to navigate"), so the two agree about what a picker looks like.
_PICKER_OPTION_RE = re.compile(r"^\s*[❯>]?\s*(\d+)\.\s+(.+?)\s*$")
_PICKER_TITLE_RE = re.compile(r"^\s*[☐☑☒◻◼▢]\s*(.+?)\s*$")
_PICKER_FOOTER_RE = re.compile(r"enter to select|to navigate", re.I)
_PICKER_FREETEXT_RE = re.compile(r"^type something\b", re.I)
_PICKER_META_RES = (re.compile(r"^chat about this\b", re.I),)

# Composer region markers: the hint line under the input box, and a box border
# made of horizontal rules.
_INPUT_FOOTER_RE = re.compile(r"⏵⏵|bypass permissions|esc to interrupt|shift\+tab", re.I)
_BORDERISH = re.compile(r"^[│\s]*[─—-]{4,}")
# How far above the picker footer to look for the option block. A live picker's
# options sit directly above the footer; a numbered list that merely scrolled by
# earlier in the transcript is well outside this window.
_PICKER_WINDOW = 12


def _pane_has_open_picker(pane_text: str) -> bool:
    """True if an AskUserQuestion option-picker is currently open in the pane.

    A live picker has, reading upward from the footer ("enter to select" /
    "to navigate"): a CONTIGUOUS block of two or more real numbered answer
    options immediately above it, and a checkbox-style title above that block.
    Two guards stop ordinary transcript prose from matching (either would
    otherwise starve heartbeats — issue #1981):

      * an open picker REPLACES the composer, so if a composer input-footer
        ("⏵⏵" / "bypass permissions" / "esc to interrupt") appears BELOW the
        matched picker-footer line, that line is really prose sitting above a
        normal composer, not a picker; and
      * the option rows must be contiguous with the footer and within a small
        window above it, so a numbered list elsewhere in the scrollback does not
        count.

    The "Type something" free-text row and meta rows ("Chat about this") are
    tolerated inside the block but not counted as answer options.
    """
    lines = _ANSI_RE.sub("", pane_text).splitlines()
    # The capture is up to 2000 lines of scrollback, so a picker resolved
    # earlier in the transcript still has its footer in the buffer (with the
    # composer that replaced it below). Only the LAST footer can be live — a
    # picker occupies the bottom of the pane — so anchor on it rather than on
    # the first match, which the composer-below guard would always reject
    # (#2080 review). Scanning from the end also keeps this independent of the
    # pane height, unlike a fixed tail window.
    footer_idx = next(
        (i for i in range(len(lines) - 1, -1, -1) if _PICKER_FOOTER_RE.search(lines[i])), None
    )
    if footer_idx is None:
        return False
    # A genuine picker occupies the input region — a composer footer below the
    # match means we matched prose above an ordinary composer, not a picker.
    if any(_INPUT_FOOTER_RE.search(ln) for ln in lines[footer_idx + 1:]):
        return False
    window_top = max(-1, footer_idx - 1 - _PICKER_WINDOW)
    # Skip a box border / blank lines directly beneath the footer.
    i = footer_idx - 1
    while i > window_top and (not lines[i].strip() or _BORDERISH.match(lines[i])):
        i -= 1
    # Walk the contiguous option block, tolerating blank / free-text rows.
    real = 0
    top_opt = None
    while i > window_top:
        m = _PICKER_OPTION_RE.match(lines[i])
        if m:
            label = m.group(2).strip()
            if not (_PICKER_FREETEXT_RE.match(label) or any(p.match(label) for p in _PICKER_META_RES)):
                real += 1
            top_opt = i
            i -= 1
            continue
        body = lines[i].strip().lstrip("❯>").strip()
        if not body or _PICKER_FREETEXT_RE.match(body):
            i -= 1
            continue
        break
    if real < 2 or top_opt is None:
        return False
    # A checkbox-style title must sit just above the option block (blanks allowed).
    while i > window_top and not lines[i].strip():
        i -= 1
    return i > window_top and bool(_PICKER_TITLE_RE.match(lines[i]))


def _post_prompt_is_ghost(raw_line: str) -> bool:
    """True iff the composer body on this RAW (ansi-bearing) line has visible
    text and ALL of it is DIM.

    Claude Code renders a SUGGESTED (ghost) next prompt in the composer as
    dim/faint text (SGR 2); a real user-typed draft is bright/default. A ghost
    is clobberable (Claude offers one nearly every turn, so protecting them
    would starve automated sends); a real draft is not. This errs toward False
    (i.e. "not a ghost — protect it") the moment any visible char is non-dim,
    so real user input is never mistaken for a ghost. SGR param "2" = faint-on;
    "0"/"22"/empty = faint-off.
    """
    m = re.search(r"[❯>]", raw_line)
    seg = raw_line[m.end():] if m else raw_line
    dim = False
    saw_visible = False
    i = 0
    n = len(seg)
    while i < n:
        if seg[i] == "\x1b":
            sm = _SGR_RE.match(seg, i)
            if sm:
                params = sm.group(1)
                for p in (params.split(";") if params else ["0"]):
                    if p == "2":
                        dim = True
                    elif p in ("0", "22", ""):
                        dim = False
                i = sm.end()
                continue
            am = _ANSI_RE.match(seg, i)  # non-SGR CSI/escape → skip it
            if am:
                i = am.end()
                continue
        ch = seg[i]
        if not ch.isspace() and ch not in ("│", "❯", ">"):
            saw_visible = True
            if not dim:
                return False
        i += 1
    return saw_visible


def _composer_has_unsent_draft(pane_text: str) -> bool:
    """True if the live composer holds the user's unsent text (mid-typing) that
    a routine send would clobber.

    Walks up from the input footer to the ``❯`` prompt, stopping at the box
    border; an empty prompt (``❯`` with nothing after it) is not a draft. A
    Claude-suggested ghost draft (rendered dim, SGR 2) is NOT protected — only
    real, non-dim user input counts (see _post_prompt_is_ghost).
    """
    raw_lines = pane_text.splitlines()
    text = _ANSI_RE.sub("", pane_text)
    lines = text.splitlines()  # index-aligned with raw_lines (SGR strip keeps line count)
    fi = None
    for i in range(len(lines) - 1, -1, -1):
        if _INPUT_FOOTER_RE.search(lines[i]):
            fi = i
            break
    if fi is None:
        return False
    bi = next((j for j in range(fi - 1, -1, -1) if _BORDERISH.match(lines[j])), None)
    if bi is None:
        return False
    for j in range(bi - 1, max(-1, bi - 40), -1):
        raw = lines[j].rstrip()
        if _BORDERISH.match(raw):  # top border / titled bar → stop before the transcript
            break
        s = raw.strip().lstrip("│").strip()
        starts = s[:1] in ("❯", ">")
        body = s[1:].strip() if starts else s
        if body:
            rawline = raw_lines[j] if j < len(raw_lines) else ""
            if _post_prompt_is_ghost(rawline):
                # dim ghost suggestion — clobberable, not a real draft
                if starts:
                    break     # composer prompt line holds only a ghost → no real draft
                continue      # a dim continuation line → keep scanning up
            return True
        if starts:  # reached an empty ❯ prompt → no draft
            break
    return False


def _pane_blocks_automated_send(
    pane_text: str, hook_known: bool = False, hook_interactive: bool = False
) -> str | None:
    """Reason string if an automated send into this pane would disrupt live
    interaction, else None. Cheapest/most-authoritative check first.

    Interactive/busy detection is now gated on the hook-driven signal
    (``hook_driven_interactive``, #2080) whenever it is known: a fresh
    "running"/"starting" hook status is the same evidence the Go send path
    treats as authoritative (``--defer-if-busy``, the #2273 verification
    loop), and it catches an open AskUserQuestion picker without guessing
    from pane glyphs. Pane-text picker detection (``_pane_has_open_picker``)
    is used ONLY as a fallback when the hook signal is unknown (hooks never
    fired for this session, the last sample went stale, or the CLI read
    failed) — that verdict is reported with an "unknown:" prefix, since it is
    a heuristic guess rather than confirmed evidence.

    The composer-unsent-draft check is orthogonal to turn state (a user can
    be mid-typing while the hook genuinely reads idle) and always runs off
    pane text regardless of hook_known.

    Pure and total: an empty or unparseable capture, plus hook_known=False
    with no picker match, yields None (send allowed) — callers fail OPEN.
    """
    if hook_known:
        if hook_interactive:
            return "hook-busy-interactive"
    elif pane_text and _pane_has_open_picker(pane_text):
        return "unknown:askuserquestion-picker-open"
    if pane_text and _composer_has_unsent_draft(pane_text):
        return "composer-holds-unsent-input"
    return None


def _heartbeat_skip_action(consecutive_skips: int, limit: int = HEARTBEAT_SKIP_LIMIT) -> str:
    """Decide what a heartbeat should do given how many cycles the
    interactive-state guard has blocked IN A ROW (counting the current one).

    Returns ``"skip"`` to hold this cycle, or ``"override"`` to deliver anyway
    despite the block. A real picker or draft rarely survives ``limit`` heartbeat
    intervals; a stale pane buffer (#1999) would block forever, so at the limit
    the heartbeat overrides the guard rather than starve (#1981/#1999).
    """
    return "override" if consecutive_skips >= limit else "skip"


# Async callable type for reply notifications: (response_text: str) -> None
ReplyCallback = Callable[[str], Coroutine[Any, Any, None]]

_WAIT_SEND_QUEUE_REQUIRED = "queue_required"
_LEGACY_REPLY_CLAIM = "legacy"
_reply_owner_lock = threading.Lock()
_wait_send_reservations: dict[tuple[str | None, str], str | None] = {}


def _conductor_inbox_snapshot(sessions: list[dict], name: str) -> tuple[int, str, bool]:
    """Count durable records and identify replacement records at the same count."""
    conductor = next((s for s in sessions if s.get("title") == conductor_session_title(name)), None)
    if not conductor or not conductor.get("id"):
        return 0, "", False
    session_id = str(conductor["id"]).strip().replace("/", "_").replace("..", "_").replace(" ", "_")
    path = resolve_data_dir("inboxes") / "inboxes" / f"{session_id}.jsonl"
    try:
        data = path.read_bytes()
        count = sum(bool(line.strip()) for line in data.splitlines())
        return count, hashlib.sha256(data).hexdigest() if count else "", False
    except FileNotFoundError:
        return 0, "", False
    except OSError as exc:
        log.warning("Heartbeat [%s]: cannot read inbox %s: %s", name, path, exc)
        return 0, "", True


def _heartbeat_fingerprint(scoped_sessions: list[dict], inbox_pending: int = 0, inbox_digest: str = "") -> str:
    """Identify the actionable part of a heartbeat (issue #2348).

    Every delivered heartbeat is a new turn that re-reads the conductor's whole
    conversation, so a tick whose waiting/error set equals the last delivered
    one is skipped. Running/idle churn is not actionable and is left out.
    """
    actionable = "|".join(sorted(
        f"{s.get('status', '')}:{s.get('title', '')}:{s.get('path', '')}"
        for s in scoped_sessions
        if s.get("status", "") in ("waiting", "error")
    ))
    return f"{actionable}|inbox={inbox_pending}:{inbox_digest}"


def _cli_json(stdout: str) -> dict:
    try:
        data = json.loads(stdout)
    except (json.JSONDecodeError, TypeError):
        return {}
    return data if isinstance(data, dict) else {}


def _accepted_turn_from_timeout(payload: dict) -> dict | None:
    """Return a fail-closed late-reply owner from a structured CLI timeout."""
    if (
        payload.get("completion") != "timeout"
        or payload.get("delivery") != "submitted"
        or payload.get("submitted") is not True
        or payload.get("accepted_turn_kind") != "codex_rollout"
    ):
        return None
    receipt = payload.get("accepted_turn")
    if not isinstance(receipt, dict):
        return None
    required = (
        "receipt_id", "instance_id", "codex_session_id", "turn_generation", "accepted_at",
    )
    if any(not isinstance(receipt.get(key), str) or not receipt[key] for key in required):
        return None
    if not receipt["turn_generation"].startswith(receipt["codex_session_id"] + ":"):
        return None
    return receipt


def _legacy_submitted_timeout(payload: dict) -> bool:
    """Preserve async completion for tools without exact turn receipts."""
    return (
        payload.get("completion") == "timeout"
        and payload.get("delivery") == "submitted"
        and payload.get("submitted") is True
        and payload.get("accepted_turn_kind") != "codex_rollout"
    )


def send_to_conductor(
    session: str,
    message: str,
    profile: str | None = None,
    wait_for_reply: bool = False,
    response_timeout: int = RESPONSE_TIMEOUT,
    reply_callback: ReplyCallback | None = None,
    force_queue: bool = False,
    claim_late_reply: bool = False,
) -> tuple[bool, str, dict | bool | str]:
    """Send a message to the conductor session.

    Returns (success, response_text, pending). An exact receipt means Codex
    accepted a turn before completion timed out; True preserves the legacy
    async signal for receipt-less tools; queue_required prevents concurrent
    remote sends from creating an unowned turn.

    When wait_for_reply=False and the conductor is busy (running/active/starting),
    the message is queued in-memory and delivered automatically once the conductor
    returns to idle/waiting state (see _drain_queue). reply_callback, if provided,
    is an async callable(response_text: str) invoked after drain delivery.

    force_queue=True skips the internal status check and enqueues immediately.
    Use this when the caller already knows the conductor is busy to avoid a
    redundant blocking subprocess call.

    claim_late_reply reserves one in-memory owner across the blocking wait and,
    on an accepted timeout, until the caller registers its reply watcher.
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

    # Serialize accepted-turn ownership before launching the blocking command;
    # a colliding remote arrival must queue instead of becoming an unowned turn.
    key = (profile, session)
    with _reply_owner_lock:
        if key in _wait_send_reservations or key in _pending_reply_tasks:
            return False, "", _WAIT_SEND_QUEUE_REQUIRED
        _wait_send_reservations[key] = None

    retain_reservation = False
    try:
        # `--wait --json` returns the accepted turn and its exact correlated
        # response as one result; never issue a second output read here.
        result = run_cli(
            "session", "send", session, message,
            "--wait", "--timeout", f"{response_timeout}s", "--json",
            profile=profile,
            timeout=max(response_timeout + 30, 60),
        )
        if result.returncode != 0:
            payload = _cli_json(result.stdout)
            receipt = _accepted_turn_from_timeout(payload)
            if receipt is not None:
                log.info(
                    "Conductor %s: accepted turn %s outlasted --wait; reply pending",
                    session, receipt["receipt_id"],
                )
                if claim_late_reply:
                    with _reply_owner_lock:
                        _wait_send_reservations[key] = receipt["receipt_id"]
                    retain_reservation = True
                return False, "", receipt
            if _legacy_submitted_timeout(payload):
                if claim_late_reply:
                    with _reply_owner_lock:
                        _wait_send_reservations[key] = _LEGACY_REPLY_CLAIM
                    retain_reservation = True
                return False, "", True
            error = payload.get("error") or result.stderr.strip()
            log.error("Failed to send to conductor: %s", error)
            return False, "", False
        payload = _cli_json(result.stdout)
        content = payload.get("content")
        if (
            payload.get("success") is True
            and payload.get("completion") == "complete"
            and isinstance(content, str)
        ):
            return True, content.strip(), False
        log.error("Conductor %s returned an invalid structured wait result", session)
        return False, "", False
    finally:
        if not retain_reservation:
            with _reply_owner_lock:
                _wait_send_reservations.pop(key, None)


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
            with _reply_owner_lock:
                if ((profile, session) in _wait_send_reservations or
                        (profile, session) in _pending_reply_tasks):
                    continue
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
# working — only the synchronous reply is lost. send_to_conductor returns the
# CLI's accepted-turn receipt; the handler registers its reply owner here.
#
# Unlike _drain_queue this NEVER sends a message — the message is already
# in-flight, so re-sending would double-process it. The watcher only polls
# until the turn finishes and delivers the captured output via reply_callback.

# Generous ceiling: the whole point is the turn already outran RESPONSE_TIMEOUT,
# so wait well beyond it before giving up.
PENDING_REPLY_MAX_WAIT = 3600  # seconds
PENDING_REPLY_POLL_INTERVAL = 5  # seconds

# Keep one late-reply owner per session referenced until it finishes.
_pending_reply_tasks: dict[tuple[str | None, str], asyncio.Task] = {}


async def _watch_pending_reply(
    session: str,
    profile: str | None,
    receipt: dict | None,
    reply_callback: ReplyCallback,
) -> None:
    """Deliver the accepted turn's output without re-sending its message.

    Codex requires an exact rollout generation because status, timestamps, and
    content are not ownership evidence. Receipt-less tools retain the previous
    status-based watcher until they expose equivalent turn identity.
    """
    loop = asyncio.get_running_loop()
    max_polls = max(1, PENDING_REPLY_MAX_WAIT // PENDING_REPLY_POLL_INTERVAL)
    for _ in range(max_polls):
        if receipt is None:
            status = await loop.run_in_executor(
                None, functools.partial(get_session_status, session, profile=profile),
            )
            if status in ("running", "active", "starting", "unknown"):
                await asyncio.sleep(PENDING_REPLY_POLL_INTERVAL)
                continue
            output = await loop.run_in_executor(
                None, functools.partial(get_session_output, session, profile=profile),
            )
            await _fire_callback(
                reply_callback, output.strip() or "[No output from conductor.]",
            )
            return
        output, generation = await loop.run_in_executor(
            None, functools.partial(get_session_output_state, session, profile=profile),
        )
        if generation == receipt["turn_generation"]:
            text = output.strip()
            await _fire_callback(reply_callback, text or "[No output from conductor.]")
            log.info(
                "Pending reply %s for %s delivered after matching Codex completion",
                receipt["receipt_id"], session,
            )
            return
        await asyncio.sleep(PENDING_REPLY_POLL_INTERVAL)

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
    receipt: dict | None,
    reply_callback: ReplyCallback,
) -> bool:
    """Schedule a reply-only watcher for an in-flight turn (no message re-send).

    Safe to call from a running-loop context; skips with a warning if there is
    no event loop (e.g. called from a bare sync context).
    """
    try:
        loop = asyncio.get_running_loop()
    except RuntimeError:
        log.warning("No running event loop — cannot watch for pending reply on %s", session)
        return False
    key = (profile, session)
    claim_id = receipt.get("receipt_id") if receipt is not None else _LEGACY_REPLY_CLAIM
    with _reply_owner_lock:
        if key in _pending_reply_tasks:
            return False
        reservation = _wait_send_reservations.get(key)
        if key in _wait_send_reservations and reservation != claim_id:
            return False
        task = loop.create_task(
            _watch_pending_reply(session, profile, receipt, reply_callback)
        )
        _wait_send_reservations.pop(key, None)
        _pending_reply_tasks[key] = task

    def _release_owner(done: asyncio.Task) -> None:
        with _reply_owner_lock:
            if _pending_reply_tasks.get(key) is done:
                _pending_reply_tasks.pop(key, None)

    task.add_done_callback(_release_owner)
    return True


def _release_late_reply_claim(
    session: str, profile: str | None, receipt: dict | None,
) -> None:
    """Release a retained send reservation when watcher setup fails."""
    key = (profile, session)
    claim_id = receipt.get("receipt_id") if receipt is not None else _LEGACY_REPLY_CLAIM
    with _reply_owner_lock:
        if _wait_send_reservations.get(key) == claim_id:
            _wait_send_reservations.pop(key, None)


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


def get_sessions_list(
    profile: str | None = None, *, fail_closed: bool = False
) -> list | None:
    """Get list of all sessions for a single profile."""
    result = run_cli("list", "--json", profile=profile, timeout=30)
    if result.returncode != 0:
        return None if fail_closed else []
    try:
        data = json.loads(result.stdout)
        # list --json returns {"sessions": [...]}
        if isinstance(data, dict):
            sessions = data.get("sessions")
            if isinstance(sessions, list):
                return sessions
            return None if fail_closed else []
        if isinstance(data, list):
            return data
        return None if fail_closed else []
    except json.JSONDecodeError:
        if result.stdout.strip().startswith("No sessions found in profile "):
            return []
        return None if fail_closed else []


def get_sessions_list_all(profiles: list[str]) -> list[tuple[str, dict]]:
    """Get sessions from all profiles, each tagged with profile name."""
    all_sessions = []
    for profile in profiles:
        sessions = get_sessions_list(profile)
        for s in sessions or []:
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


def _normalized_session_path(path: object) -> str | None:
    """Normalize a list-JSON path without requiring it to exist."""
    if not isinstance(path, str) or not path:
        return None
    return os.path.normcase(os.path.abspath(os.path.expanduser(path)))


def _find_conductor_session_by_path(
    sessions: list, session_path: str, profile: str
) -> tuple[dict | None, bool]:
    """Return the unique profile-scoped session at path and ambiguity state."""
    canonical_path = _normalized_session_path(session_path)
    matches = []
    for session in sessions:
        if not isinstance(session, dict):
            continue
        session_profile = session.get("profile")
        if session_profile not in (None, "", profile):
            continue
        if _normalized_session_path(session.get("path")) == canonical_path:
            matches.append(session)
    if len(matches) > 1:
        return None, True
    return (matches[0] if matches else None), False


async def ensure_conductor_running(name: str, profile: str) -> bool:
    """Ensure the conductor session exists and is running."""
    session_title = conductor_session_title(name)
    session_path = str(CONDUCTOR_DIR / name)
    loop = asyncio.get_running_loop()
    status = await loop.run_in_executor(
        None, functools.partial(get_session_status, session_title, profile=profile)
    )
    if status in ("waiting", "running", "idle", "active", "starting"):
        return True

    initial_start = await loop.run_in_executor(
        None,
        functools.partial(
            run_cli,
            "session",
            "start",
            session_title,
            profile=profile,
            timeout=60,
        ),
    )
    if initial_start.returncode == 0:
        await asyncio.sleep(5)
        final_status = await loop.run_in_executor(
            None,
            functools.partial(get_session_status, session_title, profile=profile),
        )
        return final_status not in ("error", "unknown")

    log.warning(
        "Failed to start conductor %s before dedupe: %s",
        name,
        initial_start.stderr.strip(),
    )
    sessions = await loop.run_in_executor(
        None,
        functools.partial(get_sessions_list, profile=profile, fail_closed=True),
    )
    if sessions is None:
        log.error(
            "Cannot verify conductor %s identity because list --json failed; "
            "refusing to create a possibly duplicate session",
            name,
        )
        return False

    path_match, ambiguous = _find_conductor_session_by_path(
        sessions, session_path, profile
    )
    if ambiguous:
        log.error(
            "Multiple sessions in profile %s use conductor path %s; "
            "refusing to select one or create another",
            profile,
            session_path,
        )
        return False

    exact_match = _find_session_by_title(sessions, session_title, profile)
    if path_match is not None and exact_match is not None:
        path_id = path_match.get("id")
        exact_id = exact_match.get("id")
        same_session = path_match is exact_match or (
            path_id is not None and path_id == exact_id
        )
        if not same_session:
            log.error(
                "Conductor path %s and exact title %s identify different "
                "sessions in profile %s; refusing migration",
                session_path,
                session_title,
                profile,
            )
            return False

    existing = path_match or exact_match
    session_ref = session_title
    if existing is not None:
        session_ref = existing.get("id") or existing.get("title")
        if not session_ref:
            log.error(
                "Conductor session at %s has neither id nor title; refusing migration",
                session_path,
            )
            return False

        if existing.get("title") != session_title:
            log.info(
                "Restoring drifted conductor title %r to %r using session %s",
                existing.get("title"),
                session_title,
                session_ref,
            )
            rename_result = await loop.run_in_executor(
                None,
                functools.partial(
                    run_cli,
                    "session",
                    "set",
                    session_ref,
                    "title",
                    session_title,
                    profile=profile,
                    timeout=60,
                ),
            )
            if rename_result.returncode != 0:
                log.error(
                    "Failed to restore conductor %s identity: %s",
                    name,
                    rename_result.stderr.strip(),
                )
                return False
        else:
            session_ref = session_title

        log.info(
            "Reusing existing conductor session %s in profile %s",
            session_ref,
            profile,
        )
    else:
        agent = _load_conductor_agent(name)
        if agent is None:
            log.error(
                "Cannot recreate conductor %s without valid runtime metadata",
                name,
            )
            return False
        log.info("Creating conductor session for %s...", name)
        result = await loop.run_in_executor(
            None,
            functools.partial(
                run_cli,
                "add",
                session_path,
                "-t",
                session_title,
                "-c",
                agent,
                "-g",
                "conductor",
                "--title-lock",
                profile=profile,
                timeout=60,
            ),
        )
        if result.returncode != 0:
            log.error(
                "Failed to create conductor %s: %s",
                name,
                result.stderr.strip(),
            )
            return False

    result = await loop.run_in_executor(
        None,
        functools.partial(
            run_cli,
            "session",
            "start",
            session_ref,
            profile=profile,
            timeout=60,
        ),
    )
    if result.returncode != 0:
        log.warning(
            "Failed to start conductor %s using %s: %s",
            name,
            session_ref,
            result.stderr.strip(),
        )
        return False

    await asyncio.sleep(5)
    final_status = await loop.run_in_executor(
        None, functools.partial(get_session_status, session_ref, profile=profile)
    )
    return final_status not in ("error", "unknown")


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
# Conductor -> human tier (issue #2469)
# ---------------------------------------------------------------------------
#
# The Go side owns the rules and the durable state: `conductor tier-filter`
# applies the urgent/info tiers and the NEED retire to a reply (counts on disk,
# so a bridge restart does not re-alert), and `conductor outbox` holds what a
# conductor queued with `conductor notify` from any turn. The bridge forwards
# and acks only after a platform accepted the message, so a stale token or a
# failed send keeps the items queued.

# Poll cadence for the human outbox, and the longest the CLI goes unpolled
# while the outbox file is unchanged (retries a failed send / a pending digest).
HUMAN_OUTBOX_POLL_SECONDS = 5
HUMAN_OUTBOX_IDLE_POLL_SECONDS = 60
# Urgent outbox items go out one message each, at most this many per poll;
# a digest message carries at most this many info items / characters, so one
# message the platform refuses holds back only itself.
HUMAN_OUTBOX_MAX_URGENT_PER_POLL = 10
HUMAN_DIGEST_MAX_ITEMS = 20
HUMAN_DIGEST_MAX_CHARS = 3500


# One sender of outbox items per conductor at a time: the outbox loop and the
# heartbeat / scan alerts (which carry a due digest) each list, send and ack
# under this lock, so an item listed by one is never sent again by the other.
_HUMAN_SEND_LOCKS: dict[str, asyncio.Lock] = {}


def _human_send_lock(name: str) -> asyncio.Lock:
    return _HUMAN_SEND_LOCKS.setdefault(name, asyncio.Lock())


def _cli_json_value(result: subprocess.CompletedProcess):
    """Parsed JSON stdout of a successful CLI call, else None."""
    if result.returncode != 0:
        return None
    try:
        return json.loads(result.stdout)
    except (json.JSONDecodeError, TypeError):
        return None


def tier_filter_reply(
    name: str, profile: str | None, response: str, prev_counts: dict,
    threshold: int = NEED_RETIRE_THRESHOLD, reply_id: str | None = None,
) -> dict:
    """Route a conductor reply through `agent-deck conductor tier-filter`.

    Returns {"lines": urgent lines to send now, "digest": info items due to go
    out after the urgent lines (ack their ids after delivery), "counts":
    filter_need_lines counts, "reply_id": the id to ack once the lines were
    delivered, None on the fallback}. The in-process filter_need_lines always runs so its counts
    stay current; its lines are used only when the CLI call fails (old binary,
    missing CLI), which keeps today's NEED forwarding as the fallback.
    reply_id: the CLI keeps this reply's retire counts pending until
    ack_tier_filter_reply(reply_id) confirms a channel accepted the message,
    so an undelivered reply (a stale token, a platform outage) never advances
    a NEED line toward STILL BLOCKED or retirement.
    """
    local = filter_need_lines(response, prev_counts, threshold)
    out = {
        "lines": local["alerts"] + local["retired"], "digest": [],
        "counts": local["counts"], "reply_id": None,
    }
    args = ["conductor", "tier-filter", "--json", "--conductor", name]
    if reply_id:
        args += ["--reply-id", reply_id]
    try:
        data = _cli_json_value(run_cli(
            *args, profile=profile, timeout=30, input_text=response,
        ))
    except Exception as e:  # noqa: BLE001 - any CLI failure falls back, never drops an alert
        log.warning("tier-filter [%s]: CLI call failed: %s", name, e)
        data = None
    if not isinstance(data, dict) or not isinstance(data.get("send_now"), list):
        log.warning("tier-filter [%s]: CLI unavailable, using in-process NEED filter", name)
        return out
    out["lines"] = [str(line) for line in data["send_now"]]
    out["reply_id"] = reply_id or None
    if data.get("digest_due") and isinstance(data.get("digest"), list):
        out["digest"] = [d for d in data["digest"] if isinstance(d, dict) and d.get("id")]
    return out


def format_human_digest(items: list[dict]) -> str:
    """One digest block for queued info items."""
    lines = [f"Digest ({len(items)} update{'s' if len(items) != 1 else ''}):"]
    lines += [f"- {str(i.get('text', '')).strip()}" for i in items]
    return "\n".join(lines)


def human_digest_batches(items: list[dict]) -> list[list[dict]]:
    """Split info items into digest messages of at most HUMAN_DIGEST_MAX_ITEMS
    items and about HUMAN_DIGEST_MAX_CHARS characters (one item always fits)."""
    batches: list[list[dict]] = []
    size = 0
    for item in items:
        n = len(str(item.get("text", ""))) + 3
        if not batches or len(batches[-1]) >= HUMAN_DIGEST_MAX_ITEMS or size + n > HUMAN_DIGEST_MAX_CHARS:
            batches.append([])
            size = 0
        batches[-1].append(item)
        size += n
    return batches


def ack_human_outbox(name: str, profile: str | None, ids: list[str]) -> bool:
    """Mark outbox ids delivered; True when the CLI accepted the ack."""
    if not ids:
        return True
    args = ["conductor", "outbox", "--json", "--conductor", name]
    for item_id in ids:
        args += ["--ack", item_id]
    ok = run_cli(*args, profile=profile, timeout=30).returncode == 0
    if not ok:
        log.error("Human outbox [%s]: ack of %d item(s) failed; they will be resent", name, len(ids))
    return ok


def ack_tier_filter_reply(name: str, profile: str | None, reply_id: str) -> bool:
    """Commit a delivered reply's NEED retire counts; True when the CLI took it."""
    ok = run_cli(
        "conductor", "tier-filter", "--json", "--conductor", name, "--ack", reply_id,
        profile=profile, timeout=30,
    ).returncode == 0
    if not ok:
        log.error("tier-filter [%s]: ack of reply %s failed; its NEED lines will repeat", name, reply_id[:12])
    return ok


def need_alert_deliverer(
    tg_user_id, telegram_bot, slack_app, slack_channel_id, discord_bot, discord_channel_id,
    mattermost_bot=None,
):
    """deliver(text) -> bool that sends text to every configured channel
    (_deliver_need_alert): True when at least one accepted it."""
    async def deliver(text: str) -> bool:
        return await _deliver_need_alert(
            text, tg_user_id, telegram_bot, slack_app,
            slack_channel_id, discord_bot, discord_channel_id, mattermost_bot,
        )

    return deliver


def heartbeat_reply_id(name: str, response: str) -> str:
    """A fresh id for one bridge-tick heartbeat reply (two ticks may return
    the same text, and each delivered one is its own retire cycle)."""
    return hashlib.sha256(f"{name}\0{time.time_ns()}\0{response}".encode("utf-8")).hexdigest()


async def send_human_digest(loop, name: str, profile: str | None, items: list[dict], prefix: str, deliver) -> bool:
    """Send queued info items as digest messages (human_digest_batches), each
    acked after a channel accepted it. True when every batch was delivered."""
    ok = True
    for batch in human_digest_batches([i for i in items if isinstance(i, dict) and i.get("id")]):
        if not await deliver(f"{prefix}{format_human_digest(batch)}"):
            log.error("Human digest [%s]: %d item(s) NOT delivered; kept queued", name, len(batch))
            ok = False
            continue
        await loop.run_in_executor(None, functools.partial(
            ack_human_outbox, name, profile, [str(i["id"]) for i in batch],
        ))
    return ok


async def deliver_tiered_reply(loop, name: str, profile: str | None, filtered: dict, prefix: str, deliver) -> bool:
    """Send one tier_filter_reply result: its urgent lines, then any due digest
    as separate message(s), so a digest the platform refuses never holds back
    the alert (and the reverse).

    deliver(text) -> bool sends to every channel. Only after a channel
    accepted a message are its digest items acked or the reply's retire counts
    committed. Returns whether the urgent lines were delivered (the digest's
    result when there are none), True when there was nothing to send; False
    means retry later.
    """
    lines, digest = filtered["lines"], filtered["digest"]
    delivered = True
    if lines:
        delivered = await deliver(f"{prefix}Conductor alert:\n" + "\n".join(lines))
        if delivered and filtered.get("reply_id"):
            await loop.run_in_executor(None, functools.partial(
                ack_tier_filter_reply, name, profile, filtered["reply_id"],
            ))
    if digest:
        digest_ok = await send_human_digest(loop, name, profile, digest, prefix, deliver)
        if not lines:
            delivered = digest_ok
    return delivered


def _human_outbox_signature(name: str):
    """(mtime_ns, size) of the conductor's outbox file, or None when absent."""
    safe = name.strip().replace("/", "_").replace("..", "_").replace(" ", "_")  # sanitizeInboxName
    path = resolve_data_dir("runtime") / "runtime" / "human-outbox" / f"{safe}.jsonl"
    try:
        st = path.stat()
        return (st.st_mtime_ns, st.st_size)
    except OSError:
        return None


async def human_outbox_cycle(
    conductors: list[dict], poll_state: dict, deliver, now: float | None = None,
) -> None:
    """One poll of every conductor's human outbox (issue #2469).

    The CLI is called when the outbox file changed since the last call or at
    least HUMAN_OUTBOX_IDLE_POLL_SECONDS ago, so an idle bridge costs one stat
    per conductor per poll. deliver(text) -> bool sends to every channel.
    """
    now = time.monotonic() if now is None else now
    loop = asyncio.get_running_loop()
    for conductor in conductors:
        name = conductor.get("name", "")
        profile = conductor.get("profile") or "default"
        if not name:
            continue
        sig = _human_outbox_signature(name)
        last = poll_state.get(name)
        if last and last["sig"] == sig and now - last["at"] < HUMAN_OUTBOX_IDLE_POLL_SECONDS:
            continue
        poll_state[name] = {"sig": sig, "at": now}
        try:
            async with _human_send_lock(name):
                await _human_outbox_send(loop, name, profile, deliver)
        except Exception as e:
            log.error("Human outbox [%s] error: %s", name, e)


async def _human_outbox_send(loop, name: str, profile: str, deliver) -> None:
    """List, send and ack one conductor's outbox (caller holds its send lock).

    Each urgent item is its own "[<name>] <text>" message, acked right after a
    channel accepted it, so an item the platform refuses never holds back the
    others. Queued info leaves as digest message(s) when an urgent item went
    out this poll or the digest window is due.
    """
    items = _cli_json_value(await loop.run_in_executor(None, functools.partial(
        run_cli, "conductor", "outbox", "--json", "--conductor", name,
        profile=profile, timeout=30,
    )))
    if not isinstance(items, list) or not items:
        return
    items = [i for i in items if isinstance(i, dict) and i.get("id")]
    urgent = [i for i in items if i.get("tier") == "urgent"]
    info = [i for i in items if i.get("tier") == "info"]
    sent = 0
    for item in urgent[:HUMAN_OUTBOX_MAX_URGENT_PER_POLL]:
        if not await deliver(f"[{name}] {str(item.get('text', '')).strip()}"):
            log.error("Human outbox [%s]: item %s NOT delivered; kept queued", name, item["id"])
            continue
        await loop.run_in_executor(None, functools.partial(
            ack_human_outbox, name, profile, [str(item["id"])],
        ))
        sent += 1
    if sent:
        log.info("Human outbox [%s]: delivered %d urgent item(s)", name, sent)
    if not info:
        return
    digest_due = sent > 0
    if not digest_due:
        tf = _cli_json_value(await loop.run_in_executor(None, functools.partial(
            run_cli, "conductor", "tier-filter", "--json", "--conductor", name,
            profile=profile, timeout=30, input_text="",
        )))
        digest_due = isinstance(tf, dict) and bool(tf.get("digest_due"))
    if digest_due:
        await send_human_digest(loop, name, profile, info, f"[{name}] ", deliver)


async def human_outbox_loop(
    telegram_bot=None, tg_user_id=None, slack_app=None, slack_channel_id=None,
    discord_bot=None, discord_channel_id=None, mattermost_bot=None,
):
    """Forward what conductors queued for the human, every 5 s (issue #2469)."""
    poll_state: dict = {}
    deliver = need_alert_deliverer(
        tg_user_id, telegram_bot, slack_app, slack_channel_id, discord_bot, discord_channel_id,
        mattermost_bot,
    )

    log.info("Human outbox loop started (poll every %d s)", HUMAN_OUTBOX_POLL_SECONDS)
    while True:
        try:
            await human_outbox_cycle(discover_conductors(), poll_state, deliver)
        except Exception as e:
            log.error("Human outbox cycle failed: %s", e)
        await asyncio.sleep(HUMAN_OUTBOX_POLL_SECONDS)


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


def _is_tg_parse_error(exc: Exception) -> bool:
    """Telegram refused the HTML itself ("can't parse entities: Unmatched end
    tag ..."), not the delivery: the same text as plain text will go through."""
    msg = str(exc).lower()
    return "parse entities" in msg or "can't find end tag" in msg or "unsupported start tag" in msg


def tg_html_to_plain(chunk: str) -> str:
    """The plain text of an md_to_tg_html chunk (its tags dropped, unescaped)."""
    import html as _html

    return _html.unescape(re.sub(r"</?(?:b|i|code)>", "", chunk))


async def send_telegram_html(bot, chat_id, text: str) -> None:
    """Send text as Telegram HTML (md_to_tg_html, split_message). A chunk
    Telegram cannot parse (md_to_tg_html can mis-nest tags, e.g. for
    '**all *.go** files and *.ts') is resent as plain text instead of failing
    the whole send, which would keep a durable alert queued forever."""
    for chunk in split_message(md_to_tg_html(text)):
        try:
            await bot.send_message(chat_id, chunk, parse_mode="HTML")
        except Exception as e:  # noqa: BLE001 - only a parse error is retried
            if not _is_tg_parse_error(e):
                raise
            log.warning("Telegram refused HTML (%s); resending the chunk as plain text", e)
            await bot.send_message(chat_id, tg_html_to_plain(chunk), parse_mode=None)


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
                claim_late_reply=True,
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

                if still_running == _WAIT_SEND_QUEUE_REQUIRED:
                    _enqueue_message(
                        session_title, cleaned_msg, target_profile, _tg_late_reply,
                    )
                    await message.answer(
                        f"{profile_tag}⏳ Conductor busy — message queued, will reply here when done."
                    )
                elif (still_running is True or isinstance(still_running, dict)) and _register_pending_reply(
                    session_title, target_profile,
                    still_running if isinstance(still_running, dict) else None,
                    _tg_late_reply,
                ):
                    await message.answer(
                        f"{profile_tag}⏳ Still working — will reply here when done."
                    )
                else:
                    if still_running is True or isinstance(still_running, dict):
                        _release_late_reply_claim(
                            session_title, target_profile,
                            still_running if isinstance(still_running, dict) else None,
                        )
                    await message.answer(
                        f"[Accepted turn could not acquire a reply watcher [{target_profile}].]"
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
                claim_late_reply=True,
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

                if still_running == _WAIT_SEND_QUEUE_REQUIRED:
                    _enqueue_message(
                        session_title, cleaned_msg, profile, _slack_late_reply,
                    )
                    notice = f"{name_tag}⏳ Conductor busy — message queued, will reply here when done."
                elif (still_running is True or isinstance(still_running, dict)) and _register_pending_reply(
                    session_title, profile,
                    still_running if isinstance(still_running, dict) else None,
                    _slack_late_reply,
                ):
                    notice = f"{name_tag}⏳ Still working — will reply here when done."
                else:
                    if still_running is True or isinstance(still_running, dict):
                        _release_late_reply_claim(
                            session_title, profile,
                            still_running if isinstance(still_running, dict) else None,
                        )
                    notice = f"[Accepted turn could not acquire a reply watcher {target['name']}]."
                await _safe_say(say, text=notice, thread_ts=thread_ts)
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
    if listen_mode not in {"all", "mentions", "mentions_all_channels"}:
        log.warning("Unknown Discord listen_mode %r, falling back to 'all'", listen_mode)
        listen_mode = "all"
    ignore_replies_to_others = bool(
        config["discord"].get("ignore_replies_to_others", False)
    )

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
        # Channel scope, then mention — decided before the auth check so
        # mentions_all_channels doesn't log "unauthorized" for every channel.
        if listen_mode != "mentions_all_channels" and message.channel.id != bot.target_channel_id:
            return
        if listen_mode in ("mentions", "mentions_all_channels") and not message_mentions_bot(message):
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
        if listen_mode in ("mentions", "mentions_all_channels"):
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
        async def _best_effort_typing():
            try:
                async with message.channel.typing():
                    while True:
                        await asyncio.sleep(5)
            except Exception as exc:
                log.warning("Discord typing indicator failed; continuing message delivery: %s", exc)

        typing_task = asyncio.create_task(_best_effort_typing())
        try:
            loop = asyncio.get_event_loop()
            ok, response, still_running = await loop.run_in_executor(
                None,
                lambda: send_to_conductor(
                    session_title,
                    cleaned_msg,
                    profile=profile,
                    wait_for_reply=True,
                    response_timeout=RESPONSE_TIMEOUT,
                    claim_late_reply=True,
                ),
            )
        finally:
            typing_task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await typing_task
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

                if still_running == _WAIT_SEND_QUEUE_REQUIRED:
                    _enqueue_message(
                        session_title, cleaned_msg, profile, _dc_late_reply,
                    )
                    notice = "⏳ Conductor busy — message queued, will reply here when done."
                elif (still_running is True or isinstance(still_running, dict)) and _register_pending_reply(
                    session_title, profile,
                    still_running if isinstance(still_running, dict) else None,
                    _dc_late_reply,
                ):
                    notice = "⏳ Still working — will reply here when done."
                else:
                    if still_running is True or isinstance(still_running, dict):
                        _release_late_reply_claim(
                            session_title, profile,
                            still_running if isinstance(still_running, dict) else None,
                        )
                    notice = "[Accepted turn could not acquire a reply watcher.]"
                await message.channel.send(notice)
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
# Mattermost bot setup
# ---------------------------------------------------------------------------

# Set on every post the bridge makes, so its own posts are never relayed back.
MM_BRIDGE_POST_PROP = "from_agent_deck"

# Mattermost IDs are 26 lowercase alphanumerics; anything else is a username.
_MM_ID_RE = re.compile(r"^[a-z0-9]{26}$")

# Text commands. Mattermost slash commands need an HTTP endpoint the server can
# reach, so the bridge reads "!status" and friends from ordinary posts instead.
MM_COMMANDS = ("status", "sessions", "restart", "help")

# Props Mattermost sets on posts that a webhook, bot or plugin made on behalf
# of a user. Values arrive as "true" strings or booleans.
MM_AUTOMATED_POST_PROPS = ("from_webhook", "from_bot", "from_plugin")

# Loopback hosts may be reached over plain http without allow_insecure_http.
_MM_LOOPBACK_HOSTS = ("localhost", "127.0.0.1", "::1")


def describe_error(error: BaseException) -> str:
    """Text for a log line; some exceptions (asyncio.TimeoutError) have none."""
    return str(error) or type(error).__name__


def _mm_prop_set(value) -> bool:
    if isinstance(value, str):
        return value.strip().lower() not in ("", "false", "0")
    return bool(value)


def mattermost_retry_after(headers) -> float | None:
    """Seconds a 429 response asks the client to wait, or None if it does not
    say. Mattermost sends X-Ratelimit-Reset (seconds until the limit resets);
    proxies may send Retry-After."""
    for name in ("Retry-After", "X-Ratelimit-Reset"):
        try:
            return max(0.0, float(headers.get(name)))
        except (TypeError, ValueError):
            continue
    return None


def mattermost_url_problem(server_url: str, allow_insecure_http: bool) -> str | None:
    """Why server_url must not be used, or None. The bot token and every
    message travel on this connection, so plain http needs a loopback host or
    an explicit allow_insecure_http."""
    parsed = urllib.parse.urlparse(server_url)
    if parsed.scheme not in ("https", "http") or not parsed.hostname:
        return f"server_url {server_url!r} is not an http(s) URL"
    if parsed.scheme == "http" and parsed.hostname not in _MM_LOOPBACK_HOSTS and not allow_insecure_http:
        return (
            f"server_url {server_url!r} uses plain http, which would send the bot token "
            "unencrypted; use https, or set allow_insecure_http = true"
        )
    return None


def mattermost_post_text(
    post: dict,
    bot_user_id: str,
    bot_username: str,
    owner_user_id: str,
    home_channel_id: str,
    home_is_dm: bool,
    listen_mode: str,
) -> str | None:
    """The text to act on for a post, or None when the bridge must ignore it.

    Ignored: the bridge's own posts, system posts, posts made by a webhook, bot
    or plugin, posts outside the home channel, posts by anyone but the owner,
    and (in a channel with listen_mode "mentions") posts that do not @mention
    the bot. A mention of the bot is stripped from the text.
    """
    if post.get("user_id") == bot_user_id:
        return None
    props = post.get("props") or {}
    if props.get(MM_BRIDGE_POST_PROP):
        return None
    if post.get("type"):  # join/leave and other system posts
        return None
    # A webhook's posts carry its creator's user_id, so the owner check alone
    # would obey anyone holding the URL of a webhook the owner created.
    automated = [p for p in MM_AUTOMATED_POST_PROPS if _mm_prop_set(props.get(p))]
    if automated:
        log.warning("Ignoring Mattermost post %s marked %s", post.get("id"), ", ".join(automated))
        return None
    if post.get("channel_id") != home_channel_id:
        return None
    text = (post.get("message") or "").strip()
    mention = re.compile(r"(?<![\w@.-])@" + re.escape(bot_username) + r"(?![\w.-])", re.IGNORECASE)
    if not home_is_dm and listen_mode == "mentions" and not mention.search(text):
        return None
    if post.get("user_id") != owner_user_id:
        log.warning("Unauthorized Mattermost message from user %s", post.get("user_id"))
        return None
    text = mention.sub("", text).strip()
    return text or None


def mattermost_reply_root(post: dict, home_is_dm: bool) -> str:
    """The thread a reply belongs in: the post's own thread, or a new thread
    under it in a channel. In a DM an unthreaded post is answered unthreaded."""
    if post.get("root_id"):
        return post["root_id"]
    return "" if home_is_dm else post.get("id", "")


def parse_mattermost_command(text: str) -> tuple[str, str] | None:
    """("restart", "ops") for "!restart ops"; None when text is not a command."""
    if not text.startswith("!"):
        return None
    word, _, argument = text[1:].partition(" ")
    word = word.lower()
    if word not in MM_COMMANDS:
        return None
    return word, argument.strip()


def mattermost_command_reply(command: str, argument: str) -> str:
    """Run a text command and return the reply. Blocking (it calls the CLI)."""
    if command == "status":
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
        return "```\n" + "\n".join(lines) + "\n```"

    if command == "sessions":
        profiles = get_unique_profiles()
        all_sessions = get_sessions_list_all(profiles)
        if not all_sessions:
            return "No sessions found."
        lines = []
        for profile, s in all_sessions:
            prefix = f"[{profile}] " if len(profiles) > 1 else ""
            lines.append(
                f"{prefix}{s.get('title', 'untitled')} ({s.get('tool', '')}) - {s.get('status', 'unknown')}"
            )
        return "```\n" + "\n".join(lines) + "\n```"

    if command == "restart":
        target = None
        if argument:
            target = next((c for c in discover_conductors() if c["name"] == argument), None)
            if target is None:
                return f"No conductor named {argument}."
        else:
            target = get_default_conductor()
        if target is None:
            return "No conductors found."
        result = run_cli(
            "session", "restart", conductor_session_title(target["name"]),
            profile=target["profile"], timeout=60,
        )
        if result.returncode == 0:
            return f"Conductor {target['name']} restarted."
        return f"Restart of {target['name']} failed: {result.stderr.strip()}"

    names = [c["name"] for c in discover_conductors()]
    return (
        "Conductor commands:\n"
        "- `!status`: aggregated status across all profiles\n"
        "- `!sessions`: list all sessions (all profiles)\n"
        "- `!restart [name]`: restart a conductor (default: the first)\n"
        "- `!help`: this message\n\n"
        f"Conductors: {', '.join(names) if names else 'none'}\n"
        "Route a message with `<name>: <message>`; otherwise it goes to the first conductor."
    )


class MattermostAPIError(RuntimeError):
    def __init__(self, message: str, status: int):
        super().__init__(message)
        self.status = status


class MattermostBridge:
    """A Mattermost bot account driven through the REST API and the WebSocket
    event stream, with nothing but aiohttp.

    It listens in one home channel: a DM between the bot and its owner, or the
    configured channel_id. Only the owner is obeyed, and every reply and alert
    goes to the home channel.

    Missed posts are recovered by reading the home channel back to a little
    before the newest post handled. All timestamps compared are the server's
    create_at values, so the local clock never decides what was missed.
    """

    RECONNECT_INITIAL_DELAY = 2
    RECONNECT_MAX_DELAY = 60
    # Only a connection that stayed up this long resets the reconnect backoff.
    STABLE_CONNECTION_SECONDS = 60
    REQUEST_TIMEOUT = 30
    TYPING_INTERVAL = 4
    POST_ATTEMPTS = 3
    POST_RETRY_DELAY = 1
    # A 429 is retried after the wait the server asks for, capped here.
    RATE_LIMIT_ATTEMPTS = 6
    RATE_LIMIT_DEFAULT_WAIT = 1
    RATE_LIMIT_MAX_WAIT = 60
    # Catch-up re-reads this far behind the newest post handled, for posts in
    # the same millisecond or delivered out of order. Post IDs in the window
    # are remembered, so nothing in it is handled twice.
    CATCH_UP_OVERLAP_MS = 60_000
    CATCH_UP_PAGE_SIZE = 200
    # After a catch-up, the IDs it read are all kept this long, while the
    # events that queued on the socket during it are read and matched.
    CATCH_UP_DEDUP_HOLD_SECONDS = 60

    def __init__(self, settings: dict):
        self.server_url = settings["server_url"].rstrip("/")
        problem = mattermost_url_problem(self.server_url, bool(settings.get("allow_insecure_http")))
        if problem:
            raise ValueError(problem)
        self._token = settings["bot_token"]
        self._owner = settings["user"].lstrip("@")
        self._configured_channel_id = settings.get("channel_id", "")
        self.listen_mode = settings.get("listen_mode", "all")
        self._api = f"{self.server_url}/api/v4"
        self._ws_url = re.sub(r"^http", "ws", self.server_url) + "/api/v4/websocket"
        self._http = None
        self.bot_user_id = ""
        self.bot_username = ""
        self.owner_user_id = ""
        self.owner_username = ""
        self.home_channel_id = ""
        self.home_is_dm = True
        self.home_channel_tag = "[dm]"
        # create_at of every home-channel post handled (or present at start)
        # inside the catch-up window, by post ID.
        self._seen_posts: dict[str, int] = {}
        self._prune_at = 1024
        # Until _dedup_hold_until, IDs at or after _dedup_hold_floor are kept.
        self._dedup_hold_floor = 0
        self._dedup_hold_until = 0.0
        self._last_create_at = 0
        self._connected_at: float | None = None
        self._tasks: set = set()

    @property
    def ready(self) -> bool:
        return bool(self.home_channel_id)

    async def _request(self, method: str, path: str, **kwargs):
        timeout = aiohttp.ClientTimeout(total=self.REQUEST_TIMEOUT)
        for attempt in range(1, self.RATE_LIMIT_ATTEMPTS + 1):
            async with self._http.request(method, f"{self._api}{path}", timeout=timeout, **kwargs) as resp:
                # Error bodies need not be JSON: the rate limiter answers a
                # 429 with plain text.
                if resp.status == 429 and attempt < self.RATE_LIMIT_ATTEMPTS:
                    wait = mattermost_retry_after(resp.headers)
                    if wait is None:
                        wait = self.RATE_LIMIT_DEFAULT_WAIT * attempt
                    wait = min(wait, self.RATE_LIMIT_MAX_WAIT)
                    log.warning("Mattermost %s %s rate limited; retrying in %ss", method, path, wait)
                elif resp.status >= 300:
                    text = await resp.text()
                    try:
                        detail = json.loads(text).get("message") or text
                    except (ValueError, AttributeError):
                        detail = text.strip()
                    raise MattermostAPIError(
                        f"Mattermost {method} {path} failed ({resp.status}): {detail}", resp.status,
                    )
                else:
                    return await resp.json(content_type=None)
            await asyncio.sleep(wait)

    async def start(self) -> None:
        """Identify the bot and its owner and resolve the home channel."""
        if self._http is None:
            self._http = aiohttp.ClientSession(headers={"Authorization": f"Bearer {self._token}"})
        me = await self._request("GET", "/users/me")
        self.bot_user_id, self.bot_username = me["id"], me["username"]
        owner_path = f"/users/{self._owner}" if _MM_ID_RE.match(self._owner) else f"/users/username/{self._owner}"
        owner = await self._request("GET", owner_path)
        self.owner_user_id, self.owner_username = owner["id"], owner["username"]
        if self._configured_channel_id:
            channel = await self._request("GET", f"/channels/{self._configured_channel_id}")
        else:
            channel = await self._request(
                "POST", "/channels/direct", json=[self.bot_user_id, self.owner_user_id],
            )
        self.home_is_dm = channel.get("type") == "D"
        self.home_channel_tag = (
            "[dm]" if self.home_is_dm
            else f"[channel:~{channel.get('name') or channel['id']} ({channel['id']})]"
        )
        if not self._last_create_at:
            await self._skip_existing_posts(channel["id"])
        self.home_channel_id = channel["id"]
        log.info(
            "Mattermost bot @%s ready (owner=@%s, home=%s)",
            self.bot_username, self.owner_username,
            "DM" if self.home_is_dm else self.home_channel_tag,
        )

    async def _skip_existing_posts(self, channel_id: str) -> None:
        """Start the catch-up point at the channel's newest post, and remember
        the posts in its overlap window so a catch-up does not replay them.

        The newest post and the window come from one newest-first read whose
        pages are anchored on posts already read, so posts arriving meanwhile
        are never on its later pages and are left for the first catch-up.
        """
        for post in await self._read_back(channel_id, None):
            self._seen_posts[post["id"]] = post.get("create_at", 0)
            self._last_create_at = max(self._last_create_at, post.get("create_at", 0))

    async def _read_back(self, channel_id: str, floor: int | None) -> list[dict]:
        """Every live post in the channel created at or after floor, oldest
        first. With floor None, the floor is the overlap window behind the
        newest post on the first page.

        Pages back from the newest post until it passes floor, however many
        pages that takes: stopping early would abandon the oldest missed
        posts. (The `since` query is no use: the server caps it at 1000 posts,
        other users' posts included.) Posts the owner did not write can never
        be acted on, so only their ID and create_at are kept.

        Each page after the first is the posts `before` one already read, not
        a numeric offset: a post deleted or added mid-read would shift offsets
        and make a page skip or repeat posts at its edge.
        """
        found: dict[str, dict] = {}
        anchor: dict | None = None
        while True:
            params = {"per_page": str(self.CATCH_UP_PAGE_SIZE)}
            if anchor is not None:
                params["before"] = anchor["id"]
            listing = await self._request("GET", f"/channels/{channel_id}/posts", params=params)
            order = listing.get("order") or []
            posts = listing.get("posts") or {}
            # `posts` also holds the thread roots of replies on the page.
            batch = [posts[i] for i in order if i in posts]
            if floor is None:
                newest = max((p.get("create_at", 0) for p in batch), default=0)
                floor = newest - self.CATCH_UP_OVERLAP_MS
            for post in batch:
                create_at = post.get("create_at", 0)
                if create_at < floor or post.get("delete_at") or post["id"] in found:
                    continue
                if post.get("user_id") == self.owner_user_id:
                    found[post["id"]] = post
                else:
                    found[post["id"]] = {"id": post["id"], "create_at": create_at, "compact": True}
            if len(order) < self.CATCH_UP_PAGE_SIZE or min((p.get("create_at", 0) for p in batch), default=0) < floor:
                break
            next_anchor = self._page_anchor(batch)
            if anchor is not None and next_anchor.get("create_at", 0) >= anchor.get("create_at", 0):
                raise RuntimeError("Mattermost catch-up is not making progress; will retry")
            anchor = next_anchor
        return sorted(found.values(), key=lambda p: (p.get("create_at", 0), p["id"]))

    @staticmethod
    def _page_anchor(batch: list[dict]) -> dict:
        """The post to read the next page `before`.

        `before` returns posts strictly older than the anchor's create_at, and
        a full page can end part way through the posts of one millisecond. So
        the anchor is the oldest post newer than the page's oldest
        millisecond, and the next page re-reads that millisecond whole.
        """
        oldest = min(p.get("create_at", 0) for p in batch)
        newer = [p for p in batch if p.get("create_at", 0) > oldest]
        if newer:
            return min(newer, key=lambda p: p.get("create_at", 0))
        log.warning(
            "Mattermost: a full page of posts shares one millisecond; others in it may be skipped",
        )
        return min(batch, key=lambda p: p.get("create_at", 0))

    async def run(self) -> None:
        """Start, then keep the event stream open, reconnecting with backoff.

        A failed start raises, so _run_platform_task retries it.
        """
        if not self.ready:
            await self.start()
        delay = self.RECONNECT_INITIAL_DELAY
        while True:
            self._connected_at = None
            try:
                await self._listen()
            except asyncio.CancelledError:
                raise
            except Exception as e:
                log.warning("Mattermost event stream failed: %s", describe_error(e))
            if (
                self._connected_at is not None
                and time.monotonic() - self._connected_at >= self.STABLE_CONNECTION_SECONDS
            ):
                delay = self.RECONNECT_INITIAL_DELAY
            log.info("Mattermost: reconnecting in %ss", delay)
            await asyncio.sleep(delay)
            delay = min(delay * 2, self.RECONNECT_MAX_DELAY)

    async def _listen(self) -> None:
        async with self._http.ws_connect(self._ws_url, heartbeat=30) as ws:
            self._connected_at = time.monotonic()
            log.info("Mattermost event stream connected")
            await self._catch_up()
            async for msg in ws:
                if msg.type == aiohttp.WSMsgType.TEXT:
                    self._on_event(json.loads(msg.data))
                elif msg.type == aiohttp.WSMsgType.ERROR:
                    raise ws.exception() or ConnectionError("websocket error")
        log.warning("Mattermost event stream closed")

    async def _catch_up(self) -> None:
        """Handle home-channel posts made while the stream was down.

        The whole backlog is read before any of it is handled, so a read that
        fails part way leaves the catch-up point where it was and the next
        connection reads it all again.
        """
        floor = self._last_create_at - self.CATCH_UP_OVERLAP_MS if self._last_create_at else 0
        posts = await self._read_back(self.home_channel_id, floor)
        self._forget_old_posts()
        # Posts made during the catch-up are queued on the socket too, and may
        # be anywhere in the window it read: keep every ID in it until they
        # have been read.
        self._dedup_hold_floor = floor
        self._dedup_hold_until = float("inf")
        skipped = 0
        for post in posts:
            if post.get("compact"):
                skipped += self._remember(post) is not None
            else:
                self._accept(post)
        if skipped:
            log.info("Mattermost catch-up passed over %d posts by other users", skipped)
        self._dedup_hold_until = time.monotonic() + self.CATCH_UP_DEDUP_HOLD_SECONDS

    def _forget_old_posts(self) -> None:
        """Drop remembered IDs older than any catch-up or queued socket event
        can deliver again."""
        floor = self._last_create_at - self.CATCH_UP_OVERLAP_MS
        if time.monotonic() < self._dedup_hold_until:
            floor = min(floor, self._dedup_hold_floor)
        self._seen_posts = {i: t for i, t in self._seen_posts.items() if t >= floor}
        self._prune_at = max(1024, 2 * len(self._seen_posts))

    def _on_event(self, event: dict) -> None:
        if event.get("event") != "posted":
            return
        try:
            post = json.loads(event["data"]["post"])
        except (KeyError, TypeError, ValueError) as e:
            log.warning("Mattermost: unreadable posted event: %s", describe_error(e))
            return
        self._accept(post)

    def _remember(self, post: dict) -> str | None:
        """Record a home-channel post as seen. Its ID if it is new, else None."""
        post_id = post.get("id")
        if not post_id or post_id in self._seen_posts:
            return None
        create_at = post.get("create_at", 0)
        self._seen_posts[post_id] = create_at
        self._last_create_at = max(self._last_create_at, create_at)
        if len(self._seen_posts) >= self._prune_at:
            self._forget_old_posts()
        return post_id

    def _accept(self, post: dict) -> None:
        # Posts elsewhere (the bot can be in other channels) must not move
        # the catch-up point past an unseen home-channel post.
        if post.get("channel_id") != self.home_channel_id:
            return
        if self._remember(post) is None:
            return
        # Handled in its own task: a relay can wait minutes for the conductor,
        # and the event stream must keep being read meanwhile.
        task = asyncio.create_task(self._handle_post(post))
        self._tasks.add(task)
        task.add_done_callback(self._tasks.discard)

    async def _handle_post(self, post: dict) -> None:
        try:
            text = mattermost_post_text(
                post, self.bot_user_id, self.bot_username, self.owner_user_id,
                self.home_channel_id, self.home_is_dm, self.listen_mode,
            )
            if text is None:
                return
            root_id = mattermost_reply_root(post, self.home_is_dm)
            command = parse_mattermost_command(text)
            if command is not None:
                loop = asyncio.get_running_loop()
                reply = await loop.run_in_executor(
                    None, functools.partial(mattermost_command_reply, *command),
                )
                await self.post(reply, root_id)
                return
            await self._relay(text, root_id)
        except Exception as e:
            log.error("Mattermost: handling post %s failed: %s", post.get("id"), describe_error(e))

    async def post(self, text: str, root_id: str = "") -> bool:
        """Post text to the home channel (split if long). True if all of it posted."""
        if not self.ready:
            log.warning("Mattermost: not connected yet; dropping post")
            return False
        ok = True
        for chunk in split_message(text, max_len=MM_MAX_LENGTH):
            ok = await self._post_chunk(chunk, root_id) and ok
        return ok

    async def _post_chunk(self, chunk: str, root_id: str) -> bool:
        """Post one chunk, retrying network errors and 5xx responses. A retry
        after a lost response can duplicate the post; that beats losing a reply."""
        for attempt in range(1, self.POST_ATTEMPTS + 1):
            try:
                await self._request("POST", "/posts", json={
                    "channel_id": self.home_channel_id,
                    "message": chunk,
                    "root_id": root_id,
                    "props": {MM_BRIDGE_POST_PROP: True},
                })
                return True
            except Exception as e:
                retryable = not isinstance(e, MattermostAPIError) or e.status >= 500
                if not retryable or attempt == self.POST_ATTEMPTS:
                    log.error("Mattermost post failed (attempt %d): %s", attempt, describe_error(e))
                    return False
                log.warning("Mattermost post failed (attempt %d), retrying: %s", attempt, describe_error(e))
                await asyncio.sleep(self.POST_RETRY_DELAY * attempt)
        return False

    async def _show_typing(self, root_id: str) -> None:
        """Show "<bot> is typing" until cancelled. Best effort."""
        try:
            while True:
                await self._request("POST", f"/users/{self.bot_user_id}/typing", json={
                    "channel_id": self.home_channel_id, "parent_id": root_id,
                })
                await asyncio.sleep(self.TYPING_INTERVAL)
        except asyncio.CancelledError:
            raise
        except Exception as e:
            log.warning("Mattermost typing indicator failed; continuing: %s", describe_error(e))

    async def _relay(self, text: str, root_id: str) -> None:
        """Send text to its conductor and post the reply, mirroring the Slack
        flow: queue while the conductor is busy, else wait for the reply, and
        hand a turn that outlasts the wait to a late-reply watcher."""
        async def reply(message: str) -> None:
            await self.post(message, root_id)

        conductors = discover_conductors()
        target_name, cleaned = parse_conductor_prefix(text, [c["name"] for c in conductors])
        target = next((c for c in conductors if c["name"] == target_name), None) or get_default_conductor()
        if target is None:
            await reply("[No conductors configured. Run: agent-deck conductor setup <name>]")
            return

        message = (
            f"[from:{self.owner_username} ({self.owner_user_id})] "
            f"{self.home_channel_tag} {cleaned or text}"
        )
        session_title = conductor_session_title(target["name"])
        profile = target["profile"]
        name_tag = f"[{target['name']}] " if len(conductors) > 1 else ""

        if not await ensure_conductor_running(target["name"], profile):
            await reply(f"[Could not start conductor {target['name']}. Check agent-deck.]")
            return

        loop = asyncio.get_running_loop()
        status = await loop.run_in_executor(
            None, functools.partial(get_session_status, session_title, profile=profile),
        )
        log.info("Mattermost message -> [%s]: %s", target["name"], message[:100])
        started_at = time.monotonic()
        busy_notice = f"{name_tag}⏳ Conductor busy — message queued, will reply here when done."

        async def late_reply(response_text: str) -> None:
            elapsed = int(time.monotonic() - started_at)
            waited = f"{elapsed // 60}m {elapsed % 60}s" if elapsed >= 60 else f"{elapsed}s"
            await reply(f"{name_tag}Queued response (waited {waited}):\n{response_text}")

        if status in ("running", "active", "starting"):
            ok, _, _ = send_to_conductor(
                session_title, message, profile=profile,
                wait_for_reply=False, reply_callback=late_reply, force_queue=True,
            )
            await reply(busy_notice if ok else f"[Failed to send message to conductor {target['name']}.]")
            return

        typing = asyncio.create_task(self._show_typing(root_id))
        try:
            ok, response, still_running = await loop.run_in_executor(
                None,
                functools.partial(
                    send_to_conductor, session_title, message, profile=profile,
                    wait_for_reply=True, response_timeout=RESPONSE_TIMEOUT,
                    claim_late_reply=True,
                ),
            )
        finally:
            typing.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await typing

        if ok:
            log.info("Conductor [%s] response: %s", target["name"], response[:100])
            await reply(f"{name_tag}{response}")
            return
        if not still_running:
            await reply(f"[Failed to send message to conductor {target['name']}.]")
            return
        # The message WAS delivered; the turn just outran the blocking wait.
        # Don't re-send (that would double-process): watch for the reply.
        receipt = still_running if isinstance(still_running, dict) else None
        if still_running == _WAIT_SEND_QUEUE_REQUIRED:
            _enqueue_message(session_title, message, profile, late_reply)
            notice = busy_notice
        elif _register_pending_reply(session_title, profile, receipt, late_reply):
            notice = f"{name_tag}⏳ Still working — will reply here when done."
        else:
            _release_late_reply_claim(session_title, profile, receipt)
            notice = f"[Accepted turn could not acquire a reply watcher {target['name']}.]"
        await reply(notice)

    async def close(self) -> None:
        if self._http is not None:
            await self._http.close()
            self._http = None


def create_mattermost_bot(config: dict) -> MattermostBridge | None:
    """The Mattermost bot, or None if Mattermost is not configured or aiohttp
    is unavailable. Nothing touches the network until run()."""
    if not HAS_AIOHTTP:
        log.warning("aiohttp not installed, skipping Mattermost bot")
        return None
    if not config["mattermost"]["configured"]:
        return None
    try:
        return MattermostBridge(config["mattermost"])
    except ValueError as e:
        log.error("Mattermost bot not started: %s", describe_error(e))
        return None


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


# Scan-only NEED forwarding state (issue #2426). Lives next to the conductors
# so it follows AGENT_DECK_CONDUCTOR_DIR and the legacy ~/.agent-deck layout.
NEED_SCAN_STATE_FILE = "need-scan-state.json"


def load_need_scan_state(path: Path) -> dict:
    """Read the persisted scan state: {conductor: {"reply": sha256 of the last
    processed reply, "counts": filter_need_lines counts}}. Missing or corrupt
    state starts empty."""
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return {}
    except (OSError, ValueError) as e:
        log.warning("NEED scan: ignoring unreadable state %s: %s", path, e)
        return {}
    conductors = data.get("conductors") if isinstance(data, dict) else None
    if not isinstance(conductors, dict):
        return {}
    state: dict = {}
    for name, entry in conductors.items():
        if not isinstance(entry, dict):
            continue
        counts = entry.get("counts")
        state[str(name)] = {
            "reply": str(entry.get("reply") or ""),
            "counts": {
                str(line): n for line, n in counts.items() if isinstance(n, int)
            } if isinstance(counts, dict) else {},
        }
    return state


def save_need_scan_state(path: Path, state: dict) -> None:
    """Atomically persist the scan state (tmp file, fsync, rename)."""
    tmp = path.with_name(path.name + ".tmp")
    try:
        with open(tmp, "w", encoding="utf-8") as f:
            json.dump({"conductors": state}, f, sort_keys=True)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    except OSError as e:
        log.error("NEED scan: state save failed (%s): %s", path, e)


async def need_scan_cycle(
    config: dict,
    need_state: dict,
    save_state,
    telegram_bot=None, slack_app=None, slack_channel_id=None,
    discord_bot=None, discord_channel_id=None, mattermost_bot=None,
) -> None:
    """One scan pass over all heartbeat-enabled conductors (issue #2426).

    Read-only: it reads each conductor's last reply with get_session_output
    and never sends to the conductor (the OS heartbeat drives the ticks).
    A reply is processed once: a NEW reply goes through filter_need_lines
    (#971), exactly like the in-process loop, so first sight forwards,
    repeats escalate then retire, and counts keep only the lines present in
    that reply (a NEED that disappears and later recurs alerts again). An
    unchanged reply is skipped. need_state is updated only after the alert
    was delivered to at least one channel (otherwise the reply is retried on
    the next scan), and save_state() runs when anything changed.
    """
    tg_user_id = config["telegram"]["user_id"] if config["telegram"]["configured"] else None
    all_conductors = discover_conductors()
    selected = select_heartbeat_conductors(all_conductors)
    changed = False
    deliver = need_alert_deliverer(
        tg_user_id, telegram_bot, slack_app, slack_channel_id, discord_bot, discord_channel_id,
        mattermost_bot,
    )

    # Forget conductors that are gone or no longer heartbeat-enabled so the
    # state stays bounded.
    active = {c.get("name", "") for c in selected}
    for gone in [n for n in need_state if n not in active]:
        del need_state[gone]
        changed = True

    loop = asyncio.get_running_loop()
    for conductor in selected:
        name = conductor.get("name", "")
        profile = conductor.get("profile") or "default"
        if not name:
            continue
        try:
            response = await loop.run_in_executor(
                None,
                functools.partial(
                    get_session_output, conductor_session_title(name), profile=profile,
                ),
            )
            # A failed read is not a reply without NEED lines: keep the counts.
            if not response or response.startswith(SESSION_OUTPUT_ERROR_PREFIX):
                continue
            reply_id = hashlib.sha256(response.encode("utf-8")).hexdigest()
            entry = need_state.get(name) or {}
            if entry.get("reply") == reply_id:
                continue  # same reply as the last scan: already handled

            async with _human_send_lock(name):
                # reply_id: the retire counts advance only once this reply is
                # delivered and acked, never for a failed send or a retry.
                filtered = await loop.run_in_executor(None, functools.partial(
                    tier_filter_reply, name, profile, response, entry.get("counts") or {},
                    config.get("need_retire_cycles", NEED_RETIRE_THRESHOLD), reply_id,
                ))
                lines = filtered["lines"]
                prefix = f"[{name}] " if len(all_conductors) > 1 else ""
                if not await deliver_tiered_reply(loop, name, profile, filtered, prefix, deliver):
                    log.error(
                        "NEED scan [%s]: %d NEED line(s) NOT delivered (no channel ok); retrying next scan",
                        name, len(lines),
                    )
                    continue
                if lines:
                    log.info("NEED scan [%s]: forwarded %d NEED line(s)", name, len(lines))

            need_state[name] = {"reply": reply_id, "counts": filtered["counts"]}
            changed = True
        except Exception as e:
            log.error("NEED scan [%s] error: %s", name, e)

    if changed:
        save_state()


async def _deliver_need_alert(
    alert_msg: str, tg_user_id, telegram_bot, slack_app, slack_channel_id,
    discord_bot, discord_channel_id, mattermost_bot=None,
) -> bool:
    """Send a NEED alert to every configured channel; True if any accepted it."""
    delivered = False
    if telegram_bot and tg_user_id:
        try:
            await send_telegram_html(telegram_bot, tg_user_id, alert_msg)
            delivered = True
        except Exception as e:
            log.error("Failed to send Telegram notification: %s", e)
    if slack_app and slack_channel_id:
        try:
            await slack_app.client.chat_postMessage(channel=slack_channel_id, text=alert_msg)
            delivered = True
        except Exception as e:
            log.error("Failed to send Slack notification: %s", e)
    if discord_bot and discord_channel_id:
        try:
            channel = discord_bot.get_channel(discord_channel_id)
            if channel:
                await send_discord_output(channel, alert_msg)
                delivered = True
        except Exception as e:
            log.error("Failed to send Discord notification: %s", e)
    if mattermost_bot is not None:
        try:
            if await mattermost_bot.post(alert_msg):
                delivered = True
        except Exception as e:
            log.error("Failed to send Mattermost notification: %s", describe_error(e))
    return delivered


async def heartbeat_need_scan_loop(
    config: dict, telegram_bot=None, slack_app=None, slack_channel_id=None,
    discord_bot=None, discord_channel_id=None, mattermost_bot=None,
):
    """Scan-only NEED: forwarder for OS-heartbeat mode (issue #2426).

    When systemd/launchd heartbeat timers drive the conductors, the bridge
    must not send its own ticks (double-trigger). But NEED: -> channel
    forwarding used to live in the send-loop's reply handling, so with OS
    heartbeats installed (the default since conductor setup installs them)
    NEED: lines never reached Slack/Telegram/Discord at all.

    This loop sends nothing. On entry and then every half heartbeat interval
    it runs need_scan_cycle. get_session_output returns only the latest
    reply and the OS timer is not phase-locked with this loop, so scanning
    at half the interval narrows the window in which two replies land
    between scans and the earlier one's NEED lines are never seen; unchanged
    replies are skipped, so the extra scans cost one read per conductor.

    State persists to CONDUCTOR_DIR/need-scan-state.json so a restart does
    not re-alert. On the first start without that file, NEED lines in each
    conductor's current reply are forwarded once (they were never forwarded
    before, since OS-heartbeat mode dropped them).
    """
    scan_seconds = max(1, config["heartbeat_interval"]) * 60 // 2
    state_path = CONDUCTOR_DIR / NEED_SCAN_STATE_FILE
    need_state = load_need_scan_state(state_path)

    log.info(
        "NEED scan loop active (scan-only; OS heartbeat drives ticks; scan every %d s)",
        scan_seconds,
    )

    while True:
        try:
            await need_scan_cycle(
                config, need_state, lambda: save_need_scan_state(state_path, need_state),
                telegram_bot, slack_app, slack_channel_id,
                discord_bot, discord_channel_id, mattermost_bot,
            )
        except Exception as e:
            log.error("NEED scan cycle failed: %s", e)
        await asyncio.sleep(scan_seconds)


async def heartbeat_loop(
    config: dict, telegram_bot=None, slack_app=None, slack_channel_id=None,
    discord_bot=None, discord_channel_id=None, mattermost_bot=None,
):
    """Periodic heartbeat: check status for each conductor and trigger checks."""
    global_interval = config["heartbeat_interval"]
    if global_interval <= 0:
        log.info("Heartbeat disabled (interval=0)")
        return

    if _os_heartbeat_daemon_installed():
        log.info("OS heartbeat daemon detected; switching to scan-only NEED forwarding (no bridge ticks)")
        await heartbeat_need_scan_loop(
            config, telegram_bot, slack_app, slack_channel_id,
            discord_bot, discord_channel_id, mattermost_bot,
        )
        return

    interval_seconds = global_interval * 60
    tg_user_id = config["telegram"]["user_id"] if config["telegram"]["configured"] else None
    deliver_alert = need_alert_deliverer(
        tg_user_id, telegram_bot, slack_app, slack_channel_id, discord_bot, discord_channel_id,
        mattermost_bot,
    )

    # Per-conductor NEED: dedup state for issue #971 — tracks consecutive
    # identical NEED lines so we can escalate-once-then-drop instead of
    # firing the same alert verbatim for 12+ hours.
    need_state_by_conductor: dict[str, dict] = {}

    # issue #1981/#1999: consecutive interactive-state skips per conductor. Reset
    # on any clear pane or successful send; once it reaches HEARTBEAT_SKIP_LIMIT we
    # override the guard and deliver anyway (see the block below).
    skip_count_by_conductor: dict[str, int] = {}

    # issue #2348: fingerprint of the last DELIVERED heartbeat per conductor.
    # An unchanged waiting/error set skips the tick instead of paying a
    # full-conversation re-read for a message the conductor already acted on.
    delivered_fingerprint_by_conductor: dict[str, str] = {}

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
                inbox_pending, inbox_digest, inbox_error = _conductor_inbox_snapshot(sessions, name)

                log.info(
                    "Heartbeat [%s/%s]: %d waiting, %d running, %d idle, %d error, %d stopped",
                    name, profile, waiting, running, idle, error, stopped,
                )

                # Inbox-only child transitions also need a turn to drain them.
                if waiting == 0 and error == 0 and inbox_pending == 0 and not inbox_error:
                    delivered_fingerprint_by_conductor.pop(name, None)
                    continue

                fingerprint = _heartbeat_fingerprint(scoped_sessions, inbox_pending, inbox_digest)
                if not inbox_error and delivered_fingerprint_by_conductor.get(name) == fingerprint:
                    log.info("Heartbeat [%s]: nothing changed since last delivery, skipping", name)
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
                if inbox_pending:
                    parts.append(f"Inbox: {inbox_pending} pending, run `agent-deck inbox drain self` first.")
                if inbox_error:
                    parts.append("Inbox unreadable; inspect the conductor inbox before continuing.")
                # Reference HEARTBEAT_RULES.md by path. Inlining the whole file
                # on every tick bloats prompts and destabilizes the cache prefix.
                rules_path_ref = None
                for rules_path in [
                    CONDUCTOR_DIR / name / "HEARTBEAT_RULES.md",
                    CONDUCTOR_DIR / profile / "HEARTBEAT_RULES.md",
                    CONDUCTOR_DIR / "HEARTBEAT_RULES.md",
                ]:
                    if rules_path.is_file():
                        rules_path_ref = rules_path.resolve()
                        break
                if rules_path_ref:
                    parts.append(f"Read heartbeat rules from {rules_path_ref}.")
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

                # issue #1981 / #2080: a routine send types text + Enter. If the
                # target is mid-interaction — an AskUserQuestion picker is open,
                # or the composer holds the user's unsent draft — that Enter
                # selects the picker default or clobbers the draft. Skip this
                # cycle when either is true.
                #
                # #2080 review: interactive/busy detection is gated on the
                # hook-driven signal (the same fresh "running"/"starting" status
                # `--defer-if-busy` and the send verification loop already treat
                # as authoritative) whenever it is known — it covers an open
                # picker without guessing from pane glyphs. Raw pane-text picker
                # detection now runs ONLY as a fallback when the hook signal is
                # unknown. Fail OPEN throughout: any read/capture/parse failure
                # leaves block_reason None so the send still goes out (heartbeats
                # are never permanently blocked). Both calls run in the executor
                # so the blocking CLI calls never freeze the event loop.
                #
                # NOTE this narrows but does NOT close the clobber window: the
                # checks and the send are separate steps, so a user who starts
                # typing (or a turn that starts) in the gap between them can
                # still be clobbered. It is a best-effort guard, not a guarantee
                # — see the bounded override below, which stops a persistently-
                # blocking signal from starving heartbeats entirely (issue #1999).
                try:
                    hook_interactive, hook_known = await loop.run_in_executor(
                        None,
                        functools.partial(hook_driven_interactive, session_title, profile=profile),
                    )
                    pane_text = await loop.run_in_executor(
                        None,
                        functools.partial(capture_pane, session_title, profile=profile),
                    )
                    block_reason = _pane_blocks_automated_send(
                        pane_text, hook_known=hook_known, hook_interactive=hook_interactive
                    )
                except Exception as e:  # noqa: BLE001 — fail-open: ANY capture/parse error must allow the send
                    log.warning(
                        "Heartbeat [%s]: interactive-state check failed (%s); allowing send",
                        name, e,
                    )
                    block_reason = None
                if block_reason:
                    skips = skip_count_by_conductor.get(name, 0) + 1
                    skip_count_by_conductor[name] = skips
                    if _heartbeat_skip_action(skips) == "skip":
                        log.info(
                            "Heartbeat [%s]: skipping this cycle (%d/%d) — %s",
                            name, skips, HEARTBEAT_SKIP_LIMIT, block_reason,
                        )
                        continue
                    # Bounded skip: the guard has blocked HEARTBEAT_SKIP_LIMIT
                    # cycles in a row. A real picker or draft rarely survives that
                    # many heartbeat intervals; a stale pane buffer (#1999) would
                    # survive forever. Deliver anyway so the conductor is never
                    # silenced permanently, and reset the counter.
                    log.warning(
                        "Heartbeat [%s]: interactive-state guard blocked %d consecutive "
                        "cycles (%s); overriding and delivering to prevent heartbeat "
                        "starvation (guards against the #1999 stale-pane-buffer case)",
                        name, skips, block_reason,
                    )
                    skip_count_by_conductor[name] = 0
                    # fall through and deliver this cycle
                else:
                    skip_count_by_conductor[name] = 0  # pane read clear → reset

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

                skip_count_by_conductor[name] = 0  # delivered → reset skip counter
                delivered_fingerprint_by_conductor[name] = fingerprint

                # Response is captured via get_session_output (see send_to_conductor).
                log.info(
                    "Heartbeat [%s] response: %s",
                    name, response[:200],
                )

                # Tier the reply (issue #2469): urgent lines now, with repeating
                # NEED lines escalated once then retired (#971, counts on disk
                # via `conductor tier-filter`), info into the human outbox, a due
                # digest as its own message. Falls back to the in-process
                # filter_need_lines when the CLI call fails. The retire counts
                # advance only for a delivered reply.
                prev_counts = need_state_by_conductor.get(name, {})
                async with _human_send_lock(name):
                    filtered = await loop.run_in_executor(None, functools.partial(
                        tier_filter_reply, name, profile, response, prev_counts,
                        config.get("need_retire_cycles", NEED_RETIRE_THRESHOLD),
                        heartbeat_reply_id(name, response),
                    ))
                    has_alerts = bool(filtered["lines"])
                    prefix = f"[{name}] " if len(all_conductors) > 1 else ""
                    if await deliver_tiered_reply(loop, name, profile, filtered, prefix, deliver_alert):
                        need_state_by_conductor[name] = filtered["counts"]
                    else:
                        log.error("Heartbeat [%s]: alert NOT delivered (no channel ok)", name)

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


async def _run_platform_task(
    name: str, coro_factory: Callable[[], Coroutine[Any, Any, None]], max_backoff: int = 300
) -> None:
    """Run a platform task, restarting it with backoff instead of raising.

    main() awaits every platform task via a single asyncio.gather(), so a
    transient failure (e.g. a proxy timeout on Telegram's getMe call) must
    not escape here: an uncaught exception kills gather(), which kills the
    whole bridge process. The OS service manager then respawns the process
    in a tight loop, and every respawn re-runs the conductor pre-start step.
    """
    backoff = 5
    while True:
        try:
            await coro_factory()
            return
        except asyncio.CancelledError:
            raise
        except Exception as e:
            log.error("%s task failed: %s; retrying in %ds", name, describe_error(e), backoff)
            await asyncio.sleep(backoff)
            backoff = min(backoff * 2, max_backoff)


async def main():
    log.info("Loading config from %s", CONFIG_PATH)
    config = load_config()

    conductors = discover_conductors()
    conductor_names = [c["name"] for c in conductors]

    # Verify at least one integration is configured and available
    tg_ok = config["telegram"]["configured"] and HAS_AIOGRAM
    sl_ok = config["slack"]["configured"] and HAS_SLACK
    dc_ok = config["discord"]["configured"] and HAS_DISCORD
    mm_ok = config["mattermost"]["configured"] and HAS_AIOHTTP

    if not tg_ok and not sl_ok and not dc_ok and not mm_ok:
        if config["telegram"]["configured"] and not HAS_AIOGRAM:
            log.error("Telegram configured but aiogram not installed. pip install aiogram")
        if config["slack"]["configured"] and not HAS_SLACK:
            log.error("Slack configured but slack-bolt not installed. pip install slack-bolt slack-sdk")
        if config["discord"]["configured"] and not HAS_DISCORD:
            log.error("Discord configured but discord.py not installed. pip install discord.py")
        if config["mattermost"]["configured"] and not HAS_AIOHTTP:
            log.error("Mattermost configured but aiohttp not installed. pip install aiohttp")
        if not any(config[p]["configured"] for p in ("telegram", "slack", "discord", "mattermost")):
            log.error("No messaging platform configured. Exiting.")
        sys.exit(1)

    platforms = []
    if tg_ok:
        platforms.append("Telegram")
    if sl_ok:
        platforms.append("Slack")
    if dc_ok:
        platforms.append("Discord")
    if mm_ok:
        platforms.append("Mattermost")

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

    # Create Mattermost bot (it connects in its platform task)
    mattermost_bot = create_mattermost_bot(config) if mm_ok else None

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
            mattermost_bot=mattermost_bot,
        )
    )

    # Forward what conductors queued for the human (`conductor notify`)
    human_outbox_task = asyncio.create_task(
        human_outbox_loop(
            telegram_bot=telegram_bot,
            tg_user_id=config["telegram"]["user_id"] if config["telegram"]["configured"] else None,
            slack_app=slack_app,
            slack_channel_id=slack_channel_id,
            discord_bot=discord_bot,
            discord_channel_id=discord_channel_id,
            mattermost_bot=mattermost_bot,
        )
    )

    # Run all concurrently
    tasks = [heartbeat_task, human_outbox_task]
    if telegram_dp and telegram_bot:
        tasks.append(asyncio.create_task(_run_platform_task(
            "Telegram polling",
            lambda: telegram_dp.start_polling(telegram_bot),
        )))
        log.info("Telegram bot polling started")
    if slack_handler:
        tasks.append(asyncio.create_task(_run_platform_task(
            "Slack Socket Mode", slack_handler.start_async,
        )))
        log.info("Slack Socket Mode handler started")
    if discord_bot:
        tasks.append(asyncio.create_task(_run_platform_task(
            "Discord bot",
            lambda: discord_bot.start(config["discord"]["bot_token"]),
        )))
        log.info("Discord bot started")
    if mattermost_bot:
        tasks.append(asyncio.create_task(_run_platform_task(
            "Mattermost", mattermost_bot.run,
        )))
        log.info("Mattermost bot started")

    try:
        await asyncio.gather(*tasks)
    finally:
        heartbeat_task.cancel()
        human_outbox_task.cancel()
        if telegram_bot:
            await telegram_bot.session.close()
        if slack_handler:
            await slack_handler.close_async()
        if discord_bot:
            await discord_bot.close()
        if mattermost_bot:
            await mattermost_bot.close()


if __name__ == "__main__":
    asyncio.run(main())
