#!/usr/bin/env python3
"""
Agent-Deck Auto-Restart Watchdog (v2 — with continuity message, atomic cascade revive, Telegram escalation).

See DESIGN.md for architecture. Changes in v2:
  - Critical-session filter narrowed to explicit allow-list
  - Rate limit: 3 per 600s (10 min) per session
  - Cascade: when 5+ criticals are simultaneously in error, wait 30s for
    system to stabilize, then fire all restarts in parallel (atomic-ish revive)
  - After each successful restart, send a continuity message via
    `agent-deck session send <id> "..." --no-wait`
  - Telegram escalation to a fixed chat_id for rate-limit breaches

Usage:
    watchdog.py                 # daemon mode (systemd)
    watchdog.py --dry-run       # log only, no restart / send / escalation
    watchdog.py --once          # one pass of safety poll, then exit
    watchdog.py --verbose
"""

import argparse
import hashlib
import json
import logging
import os
import queue
import re
import signal
import subprocess
import sys
import threading
import time
import urllib.parse
import urllib.request
from collections import deque
from pathlib import Path

AGENT_DECK_ROOT = Path(os.environ.get("AGENT_DECK_ROOT", str(Path.home() / ".agent-deck")))
HOOKS_DIR = AGENT_DECK_ROOT / "hooks"
WATCHDOG_DIR = AGENT_DECK_ROOT / "watchdog"
AUTORESTART_DIR = WATCHDOG_DIR / "autorestart"
ESCALATIONS_LOG = WATCHDOG_DIR / "escalations.log"
RESTART_LOG = WATCHDOG_DIR / "restart.log"
ESCALATE_SCRIPT = WATCHDOG_DIR / "escalate.sh"

AGENT_DECK_BIN = os.environ.get("AGENT_DECK_BIN", "/usr/local/bin/agent-deck")
TELEGRAM_ENV_FILE = Path.home() / ".claude/channels/telegram/.env"
TELEGRAM_ESCALATION_CHAT_ID = os.environ.get("TELEGRAM_ESCALATION_CHAT_ID", "")

# --- guardrails ---
RATE_LIMIT_MAX = 3
RATE_LIMIT_WINDOW_S = 600          # 10 min per user's spec
CASCADE_THRESHOLD = 5
CASCADE_WAIT_S = 30                # settle-time before batch revive
POST_RESTART_COOLDOWN_S = 30
SAFETY_POLL_INTERVAL_S = 5
SUBPROCESS_TIMEOUT_S = 30
EVENT_QUEUE_MAXSIZE = 100

# --- v1.7.63 capability A: poller-existence check ---
# Name of the telegram MCP channel as registered on a session. Presence on a
# conductor session's `channels` array means that session is expected to have
# a bun-telegram poller subprocess running somewhere on this host.
TELEGRAM_CHANNEL_NAME = "plugin:telegram@claude-plugins-official"
# Per-conductor dedup: don't fire more than one poller-restart per hour, even
# if the poller keeps dying. Prevents thrash; human attention kicks in after that.
POLLER_RESTART_DEDUP_S = 3600

# --- v1.7.63 capability B: waiting-too-long patrol ---
# A child session (parent_session_id set) sitting in status=waiting with an
# unchanged pane for longer than this gets a "report status?" nudge.
WAITING_PATROL_THRESHOLD_S = 600        # 10 min
WAITING_PATROL_NUDGE_DEDUP_S = 3600     # 1 hour — don't re-nudge within this
WAITING_PATROL_NUDGE_TEXT = "report status?"

# --- API rate-limit (429) handling ---
# When claude itself returns 429, we must NOT count it as a crash (that escalates
# to telegram and eventually removes the session). Instead, pause the specific
# session for API_429_BACKOFF_S, and if 429s keep piling up globally, trip a
# circuit breaker so we stop hammering the API entirely.
API_429_BACKOFF_S = 180                       # per-session pause after a 429
MIN_GLOBAL_RESTART_INTERVAL_S = 60            # serialize ALL restart attempts ≥ 60s apart
CIRCUIT_BREAKER_429_THRESHOLD = 3             # 3 recent 429s trips the breaker
CIRCUIT_BREAKER_429_WINDOW_S = 600            # counted over last 10 min
CIRCUIT_BREAKER_PAUSE_S = 600                 # breaker pauses restarts for 10 min
PROMPT_RESUME_TIMEOUT_S = 120                 # one-shot `claude --resume -p` timeout

# Patterns we grep against tmux pane output / subprocess stderr.
RATE_LIMIT_PATTERNS = (
    re.compile(r"\brate[- ]?limit", re.IGNORECASE),
    re.compile(r"\bHTTP[/ ]?429\b", re.IGNORECASE),
    re.compile(r"\(429\)"),
    re.compile(r"exceed.*rate.*limit", re.IGNORECASE),
)
DEFERRED_MARKER_PATTERN = re.compile(r"deferred tool marker", re.IGNORECASE)

# Profile → CLAUDE_CONFIG_DIR mapping. Used only for the prompt-resume fallback
# (restricted to personal profile per explicit user instruction).
PROFILE_CONFIG_DIRS = {
    "personal": str(Path.home() / ".claude"),
    "work": str(Path.home() / ".claude-work"),
}
PROMPT_RESUME_TEXT = (
    "continue — your tmux session was stopped unexpectedly; "
    "check your state and report a one-line status"
)

# --- critical-session allow-list ---
CRITICAL_TITLE_PREFIXES = ("conductor-",)
CRITICAL_TITLES_EXACT = ("meeting-watcher", "gmail-watcher", "agent-deck")
CRITICAL_GROUPS = ("conductor", "watchers")

CONTINUITY_MESSAGE_TEMPLATE = (
    "[SESSION AUTO-RESTARTED] Your session was abruptly stopped "
    "(suspected cascade event at {timestamp}). If you were mid-task, "
    "re-read your task-log.md + state.json and continue from where you "
    "left off. Tell the user via Telegram if you resumed successfully."
)

log = logging.getLogger("watchdog")


# ------------------------------------------------------------------
# Subprocess helpers
# ------------------------------------------------------------------

def run_cmd(args, timeout=SUBPROCESS_TIMEOUT_S):
    try:
        res = subprocess.run(
            args, capture_output=True, text=True, timeout=timeout, check=False
        )
        return res.returncode, res.stdout, res.stderr
    except subprocess.TimeoutExpired as e:
        log.error("subprocess timeout: %s", args)
        return 124, e.stdout or "", e.stderr or ""
    except FileNotFoundError:
        return 127, "", f"binary not found: {args[0]}"


def list_all_sessions():
    rc, out, err = run_cmd([AGENT_DECK_BIN, "list", "--all", "--json"])
    if rc != 0:
        log.warning("list --all failed rc=%d err=%s", rc, err.strip())
        return []
    try:
        return json.loads(out)
    except json.JSONDecodeError:
        return []


def show_session(instance_id, profile=None):
    args = [AGENT_DECK_BIN]
    if profile:
        args += ["-p", profile]
    args += ["session", "show", instance_id, "--json"]
    rc, out, err = run_cmd(args)
    if rc != 0:
        return None
    try:
        return json.loads(out)
    except json.JSONDecodeError:
        return None


def fetch_session_output(instance_id, profile):
    """Read the session's most-recent tmux pane output via agent-deck. Used to
    inspect why claude's startup failed (429 vs deferred-tool marker vs other)."""
    args = [AGENT_DECK_BIN]
    if profile:
        args += ["-p", profile]
    args += ["session", "output", instance_id, "-quiet"]
    rc, out, err = run_cmd(args, timeout=15)
    if rc != 0:
        return ""
    return out or ""


def detect_rate_limit(text):
    if not text:
        return False
    return any(p.search(text) for p in RATE_LIMIT_PATTERNS)


def detect_deferred_marker(text):
    if not text:
        return False
    return bool(DEFERRED_MARKER_PATTERN.search(text))


def send_continuity_message(instance_id, profile, dry_run=False):
    msg = CONTINUITY_MESSAGE_TEMPLATE.format(
        timestamp=time.strftime("%Y-%m-%d %H:%M:%S %Z")
    )
    if dry_run:
        log.info("[DRY] continuity→ %s: %s", instance_id, msg)
        return True
    args = [AGENT_DECK_BIN]
    if profile:
        args += ["-p", profile]
    args += ["session", "send", instance_id, msg, "--no-wait"]
    rc, out, err = run_cmd(args, timeout=15)
    if rc != 0:
        log.warning("continuity send failed %s rc=%d err=%s", instance_id, rc, err.strip())
        return False
    log.info("continuity sent to %s", instance_id)
    return True


# ------------------------------------------------------------------
# Telegram escalation
# ------------------------------------------------------------------

def _load_telegram_token():
    try:
        for line in TELEGRAM_ENV_FILE.read_text().splitlines():
            if line.startswith("TELEGRAM_BOT_TOKEN="):
                return line.split("=", 1)[1].strip().strip('"').strip("'")
    except OSError:
        return None
    return None


def telegram_send(text, chat_id=None, dry_run=False):
    if chat_id is None:
        chat_id = TELEGRAM_ESCALATION_CHAT_ID
    if not chat_id:
        log.warning("telegram escalation skipped: TELEGRAM_ESCALATION_CHAT_ID env var not set")
        return False
    token = _load_telegram_token()
    if not token:
        log.warning("telegram escalation skipped: no token in %s", TELEGRAM_ENV_FILE)
        return False
    if dry_run:
        log.info("[DRY] telegram→%s: %s", chat_id, text)
        return True
    url = f"https://api.telegram.org/bot{token}/sendMessage"
    data = urllib.parse.urlencode({
        "chat_id": chat_id,
        "text": text,
    }).encode()
    try:
        with urllib.request.urlopen(url, data=data, timeout=10) as resp:
            ok = resp.status == 200
            if not ok:
                log.warning("telegram non-200: %d", resp.status)
            return ok
    except Exception as e:
        log.warning("telegram send failed: %s", e)
        return False


# ------------------------------------------------------------------
# Critical-session classifier
# ------------------------------------------------------------------

def is_critical(sess):
    """Broad: should the watchdog attempt auto-restart at all?"""
    if not sess:
        return False
    title = sess.get("title") or ""
    group = sess.get("group") or sess.get("group_path") or ""
    sid = sess.get("id") or ""
    is_cond = bool(sess.get("is_conductor") or sess.get("IsConductor"))

    if is_cond:
        return True
    if group in CRITICAL_GROUPS:
        return True
    if any(title.startswith(p) for p in CRITICAL_TITLE_PREFIXES):
        return True
    if title in CRITICAL_TITLES_EXACT:
        return True
    if sid and (AUTORESTART_DIR / sid).exists():
        return True
    return False


def is_escalation_critical(sess):
    """Narrow: should failures escalate to Telegram?
    Workers (exact-title allow-list, autorestart markers, group=conductor one-offs like 'test-visual')
    get auto-restart but NOT telegram spam.
    Telegram is reserved for: title=conductor-*  OR  group=watchers  OR  IsConductor flag."""
    if not sess:
        return False
    title = sess.get("title") or ""
    group = sess.get("group") or sess.get("group_path") or ""
    is_cond = bool(sess.get("is_conductor") or sess.get("IsConductor"))
    if is_cond:
        return True
    if title.startswith("conductor-"):
        return True
    if group == "watchers":
        return True
    return False


# ------------------------------------------------------------------
# v1.7.63 helpers: poller-existence check + waiting-too-long patrol
# ------------------------------------------------------------------

_ENVFILE_STATE_DIR_RE = re.compile(
    r'^\s*(?:export\s+)?TELEGRAM_STATE_DIR\s*=\s*["\']?(.*?)["\']?\s*$'
)


def bun_telegram_state_dirs(proc_root="/proc"):
    """Return the set of TELEGRAM_STATE_DIR values exported by every running
    bun-telegram process on this host.

    The agent-deck telegram plugin spawns one `bun ... telegram ... start` per
    conductor; each such process has its conductor's TELEGRAM_STATE_DIR in its
    environment. Enumerating those state dirs is how the watchdog decides
    whether a given conductor's poller is currently alive.
    """
    rc, out, _ = run_cmd(["pgrep", "-af", "bun.*telegram"], timeout=5)
    if rc != 0:
        return set()
    dirs = set()
    for line in out.splitlines():
        parts = line.split(None, 1)
        if not parts or not parts[0].isdigit():
            continue
        pid = parts[0]
        environ_path = Path(proc_root) / pid / "environ"
        try:
            raw = environ_path.read_bytes()
        except (OSError, PermissionError):
            continue
        for chunk in raw.split(b"\x00"):
            if chunk.startswith(b"TELEGRAM_STATE_DIR="):
                dirs.add(chunk.decode("utf-8", "replace").split("=", 1)[1])
                break
    return dirs


def parse_envfile_state_dir(env_file_path):
    """Extract TELEGRAM_STATE_DIR from a conductor's env_file (a shell .envrc
    like `export TELEGRAM_STATE_DIR=/foo/bar`). Returns None if the file is
    missing, unreadable, or does not declare the var."""
    if not env_file_path:
        return None
    try:
        text = Path(env_file_path).read_text()
    except OSError:
        return None
    for line in text.splitlines():
        m = _ENVFILE_STATE_DIR_RE.match(line)
        if m:
            return m.group(1).strip()
    return None


def compute_pane_hash(sid, profile):
    """Cheap 'has anything changed on this pane' signal for the waiting patrol.
    Hashes the current agent-deck session output so caller can compare snapshots
    across ticks without storing full pane text."""
    text = fetch_session_output(sid, profile)
    return hashlib.sha256(text.encode("utf-8", "replace")).hexdigest()


def send_status_query(sid, profile, dry_run=False):
    """Inject the 'report status?' nudge via `agent-deck session send --no-wait`.
    Non-blocking on the receiver so a truly stuck agent doesn't stall this tick."""
    if dry_run:
        log.info("[DRY] status-query→ %s", sid)
        return True
    args = [AGENT_DECK_BIN]
    if profile:
        args += ["-p", profile]
    args += ["session", "send", sid, WAITING_PATROL_NUDGE_TEXT, "--no-wait"]
    rc, _, err = run_cmd(args, timeout=15)
    if rc != 0:
        log.warning("status query send failed %s rc=%d err=%s", sid, rc, err.strip()[:200])
        return False
    log.info("status query sent to %s", sid)
    return True


def poller_restart_session(sid, profile, dry_run=False):
    """Issue a plain `agent-deck session restart <sid>` for a session whose
    telegram poller has died. This path deliberately does NOT go through the
    429-serialized `_do_restart` — the session itself is healthy, only its
    out-of-band poller subprocess needs to be respawned."""
    if dry_run:
        log.info("[DRY] poller-restart→ %s", sid)
        return True
    args = [AGENT_DECK_BIN]
    if profile:
        args += ["-p", profile]
    args += ["session", "restart", sid]
    rc, _, err = run_cmd(args, timeout=60)
    if rc != 0:
        log.warning("poller restart failed %s rc=%d err=%s", sid, rc, err.strip()[:200])
        return False
    log.info("poller restart ok for %s", sid)
    return True


# ------------------------------------------------------------------
# Watchdog core
# ------------------------------------------------------------------

ESCALATION_DEDUP_S = 300            # don't re-escalate same sid+severity within 5 min
STALE_ERROR_CLEANUP_S = 600         # non-escalation-critical session in error > this → remove
STALE_CLEANUP_SCAN_INTERVAL_S = 60  # how often to scan for stale non-criticals


class Watchdog:
    def __init__(self, dry_run=False):
        self.dry_run = dry_run
        self.restart_history = {}          # id -> deque[ts]
        self.consecutive_failures = {}     # id -> int
        self.cooldown_until = {}           # id -> ts
        self.cascade_settling_until = 0.0  # during cascade window, NO restarts fire
        self.first_error_seen_at = {}      # id -> ts of first consecutive error observation
        self.removed_ids = set()           # ids we've auto-removed (won't retry)
        self.last_escalation_at = {}       # (sid, severity) -> ts
        self.lock = threading.Lock()
        self.event_q = queue.Queue(maxsize=EVENT_QUEUE_MAXSIZE)
        self.stop_event = threading.Event()
        # Rate-limit state (429 handling)
        self.restart_lock = threading.Lock()      # serialize ALL restart attempts globally
        self.last_global_restart_at = 0.0
        self.rate_limited_until = {}              # sid -> ts (skip restart while < ts)
        self.recent_429s = deque()                # timestamps of recent 429 detections
        self.circuit_breaker_until = 0.0          # if > now, pause ALL restarts
        self.circuit_breaker_notified = False     # avoid re-telegramming the same trip
        # v1.7.63 capability A: one restart per conductor per POLLER_RESTART_DEDUP_S
        self.last_poller_restart_at = {}          # sid -> ts
        # v1.7.63 capability B: per-waiting-child {first_seen_at, pane_hash, last_nudge_at}
        self.waiting_tracker = {}

    # ---- logging helpers ----

    def _audit(self, path, record):
        try:
            with open(path, "a") as f:
                f.write(json.dumps(record) + "\n")
        except OSError as e:
            log.error("audit write failed %s: %s", path, e)

    def escalate(self, severity, message, telegram=False, sid=None):
        """Log locally. Telegram only if `telegram=True` AND the (sid,severity)
        hasn't escalated within ESCALATION_DEDUP_S. Local log also dedup'd."""
        now = time.time()
        dedup_key = (sid or "_global", severity)
        last = self.last_escalation_at.get(dedup_key, 0)
        dedup_active = (now - last) < ESCALATION_DEDUP_S

        if dedup_active:
            log.debug("escalation deduped [%s] sid=%s (last %ds ago)", severity, sid, int(now - last))
            return

        self.last_escalation_at[dedup_key] = now
        rec = {"ts": now, "severity": severity, "message": message, "sid": sid, "telegram": telegram}
        self._audit(ESCALATIONS_LOG, rec)
        log.warning("ESCALATION [%s]%s %s", severity, f" telegram" if telegram else " local", message)
        if ESCALATE_SCRIPT.exists() and os.access(ESCALATE_SCRIPT, os.X_OK):
            try:
                subprocess.Popen(
                    [str(ESCALATE_SCRIPT), severity, message],
                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                )
            except OSError:
                pass
        if telegram:
            telegram_send(f"[agent-deck watchdog] {severity}: {message}", dry_run=self.dry_run)

    # ---- gate checks ----

    def _prune_history(self, now):
        for hist in self.restart_history.values():
            while hist and now - hist[0] > RATE_LIMIT_WINDOW_S:
                hist.popleft()

    def _in_cooldown(self, sid, now):
        return now < self.cooldown_until.get(sid, 0)

    def _rate_limited(self, sid):
        hist = self.restart_history.get(sid)
        return hist and len(hist) >= RATE_LIMIT_MAX

    def _cascade_active(self, now):
        return now < self.cascade_settling_until

    # ---- rate-limit (429) state machine ----

    def _note_429(self, sid):
        """Record a 429 hit for `sid`. Pauses that session for API_429_BACKOFF_S
        and (if the threshold is crossed) trips the global circuit breaker.
        Must be called with self.lock held."""
        now = time.time()
        self.rate_limited_until[sid] = now + API_429_BACKOFF_S
        self.recent_429s.append(now)
        while self.recent_429s and now - self.recent_429s[0] > CIRCUIT_BREAKER_429_WINDOW_S:
            self.recent_429s.popleft()
        if (len(self.recent_429s) >= CIRCUIT_BREAKER_429_THRESHOLD
                and now >= self.circuit_breaker_until):
            self.circuit_breaker_until = now + CIRCUIT_BREAKER_PAUSE_S
            if not self.circuit_breaker_notified:
                self.circuit_breaker_notified = True
                # Defer the telegram — we're holding self.lock.
                threading.Thread(
                    target=telegram_send,
                    args=(
                        f"[agent-deck watchdog] circuit breaker tripped — "
                        f"{len(self.recent_429s)} API 429s in last "
                        f"{CIRCUIT_BREAKER_429_WINDOW_S // 60} min. "
                        f"Pausing restarts for "
                        f"{CIRCUIT_BREAKER_PAUSE_S // 60} min.",
                    ),
                    kwargs={"dry_run": self.dry_run},
                    daemon=True,
                ).start()
            log.warning("circuit breaker tripped until %s (%d 429s in window)",
                        time.strftime("%H:%M:%S", time.localtime(self.circuit_breaker_until)),
                        len(self.recent_429s))

    def _circuit_broken(self, now):
        """Must be called with self.lock held."""
        if now < self.circuit_breaker_until:
            return True
        # Breaker expired — reset notification flag so a future trip re-alerts.
        if self.circuit_breaker_until > 0:
            self.circuit_breaker_until = 0.0
            self.circuit_breaker_notified = False
        return False

    def _session_rate_limited(self, sid, now):
        """Must be called with self.lock held."""
        return now < self.rate_limited_until.get(sid, 0)

    # ---- single-session restart path ----

    def _poll_status(self, sid, profile, timeout_s, want_not="error"):
        """Poll session status every 1s for up to timeout_s. Return final status."""
        deadline = time.time() + timeout_s
        last = ""
        while time.time() < deadline:
            detail = show_session(sid, profile=profile)
            status = (detail.get("status") if detail else "") or ""
            last = status
            if status.lower() != want_not:
                return status
            time.sleep(1.0)
        return last

    def _restart_or_start(self, sid, profile):
        """Call `session restart`, verify status recovered; if not, fall back to `session start`.
        Returns (ok, final_status, err_msg)."""
        base = [AGENT_DECK_BIN]
        if profile:
            base += ["-p", profile]
        rc, _, err = run_cmd(base + ["session", "restart", sid], timeout=60)
        if rc != 0:
            return False, None, f"restart rc={rc}: {err.strip()[:200]}"
        # Restart can silently no-op when tmux was fully dead. Poll up to 4s first.
        status = self._poll_status(sid, profile, timeout_s=4)
        if status.lower() != "error":
            return True, status, None
        log.warning("%s still in error after restart; falling back to session start", sid)
        rc2, _, err2 = run_cmd(base + ["session", "start", sid], timeout=60)
        if rc2 != 0:
            return False, status, f"fallback start rc={rc2}: {err2.strip()[:200]}"
        # `start` can take longer — tmux bootstrap + claude cold start.
        status2 = self._poll_status(sid, profile, timeout_s=15)
        if status2.lower() == "error":
            return False, status2, "fallback start returned success but status still error after 15s"
        return True, status2, None

    def _prompt_resume_personal(self, sid, profile):
        """One-shot `claude --resume <claude_sid> -p "continue..."` to burn a
        stale deferred-tool marker. Scoped to profile=personal per user spec.
        Returns (ok, rate_limited_detected)."""
        if profile != "personal":
            return False, False
        detail = show_session(sid, profile=profile)
        if not detail:
            return False, False
        claude_sid = detail.get("claude_session_id") or ""
        if not claude_sid:
            log.info("prompt-resume: no claude_session_id for %s, skip", sid)
            return False, False
        config_dir = PROFILE_CONFIG_DIRS.get(profile)
        if not config_dir:
            return False, False
        env = os.environ.copy()
        env["CLAUDE_CONFIG_DIR"] = config_dir
        # Strip any inherited TELEGRAM_STATE_DIR to avoid the plugin spawning a
        # second bot poller (see bug_child_session_telegram_poller_leak).
        env.pop("TELEGRAM_STATE_DIR", None)
        log.info("prompt-resume: claude --resume %s -p '<continuity>' (profile=%s)", claude_sid, profile)
        try:
            res = subprocess.run(
                ["claude", "--resume", claude_sid,
                 "--dangerously-skip-permissions",
                 "-p", PROMPT_RESUME_TEXT],
                env=env, capture_output=True, text=True,
                timeout=PROMPT_RESUME_TIMEOUT_S, check=False,
            )
        except subprocess.TimeoutExpired:
            log.warning("prompt-resume TIMEOUT for %s", sid)
            return False, False
        except FileNotFoundError:
            log.error("prompt-resume: claude binary not found")
            return False, False
        combined = (res.stdout or "") + "\n" + (res.stderr or "")
        if detect_rate_limit(combined):
            log.warning("prompt-resume: 429 detected for %s", sid)
            return False, True
        ok = (res.returncode == 0)
        log.info("prompt-resume %s rc=%d ok=%s (out=%d bytes)", sid, res.returncode, ok, len(combined))
        return ok, False

    def _do_restart(self, sid, title, profile, escalate_critical=False):
        """Fire the restart (with start-fallback) + continuity message. Returns True on success.

        Serialized globally via `self.restart_lock` — only one restart in-flight
        at a time, and each attempt is spaced at least MIN_GLOBAL_RESTART_INTERVAL_S
        from the previous one. This is the primary defense against 429 cascades.
        """
        # Global serialization: wait for any in-flight restart to finish, and
        # enforce the minimum inter-restart interval.
        with self.restart_lock:
            now = time.time()
            wait = (self.last_global_restart_at + MIN_GLOBAL_RESTART_INTERVAL_S) - now
            if wait > 0:
                log.info("serialize: waiting %.1fs before restarting %s (%s)", wait, sid, title)
                # Interruptible sleep — stop_event lets --once/sigterm bail out cleanly.
                self.stop_event.wait(wait)
                if self.stop_event.is_set():
                    return False

            # Re-check circuit breaker and rate-limit state now that we've waited.
            with self.lock:
                if self._circuit_broken(time.time()):
                    log.info("circuit breaker active, aborting restart of %s", sid)
                    return False
                if self._session_rate_limited(sid, time.time()):
                    log.info("%s still rate-limited, aborting restart", sid)
                    return False

            log.info("RESTART %s (%s) profile=%s dry_run=%s", sid, title, profile, self.dry_run)
            self._audit(RESTART_LOG, {
                "ts": time.time(), "id": sid, "title": title, "profile": profile,
                "dry_run": self.dry_run,
            })
            if self.dry_run:
                self.last_global_restart_at = time.time()
                return True
            ok, final_status, err_msg = self._restart_or_start(sid, profile)
            self.last_global_restart_at = time.time()

            # Inspect pane output if we failed — distinguish 429 from real crash
            # from deferred-tool-marker (where the -p fallback can help).
            if not ok:
                pane_text = fetch_session_output(sid, profile)
                if detect_rate_limit(pane_text):
                    log.warning("rate-limit (429) detected for %s — backing off %ds, NOT counted as crash",
                                sid, API_429_BACKOFF_S)
                    self._audit(RESTART_LOG, {
                        "ts": time.time(), "id": sid, "title": title, "profile": profile,
                        "action": "rate-limited", "backoff_s": API_429_BACKOFF_S,
                    })
                    with self.lock:
                        self._note_429(sid)
                    return False
                # Deferred-tool-marker fallback — scoped to personal profile.
                if profile == "personal" and detect_deferred_marker(pane_text):
                    log.info("deferred-tool-marker detected for %s — trying -p prompt-resume", sid)
                    self._audit(RESTART_LOG, {
                        "ts": time.time(), "id": sid, "title": title, "profile": profile,
                        "action": "prompt-resume-attempt",
                    })
                    resumed, was_rate_limited = self._prompt_resume_personal(sid, profile)
                    if was_rate_limited:
                        with self.lock:
                            self._note_429(sid)
                        return False
                    if resumed:
                        # Marker is burned — retry `agent-deck session start` to
                        # get the session back into its normal tmux scope.
                        base = [AGENT_DECK_BIN]
                        if profile:
                            base += ["-p", profile]
                        rc3, _, err3 = run_cmd(base + ["session", "start", sid], timeout=60)
                        if rc3 == 0:
                            status3 = self._poll_status(sid, profile, timeout_s=15)
                            if status3.lower() != "error":
                                log.info("prompt-resume recovery OK for %s final_status=%s", sid, status3)
                                self.consecutive_failures[sid] = 0
                                self.first_error_seen_at.pop(sid, None)
                                ok = True
                                final_status = status3
                                err_msg = None
                        if not ok:
                            log.warning("prompt-resume ran but session %s is still in error", sid)

            if not ok:
                n = self.consecutive_failures.get(sid, 0) + 1
                self.consecutive_failures[sid] = n
                log.error("restart FAILED %s: %s", sid, err_msg)
                if n >= 2:
                    self.escalate(
                        "restart-failed",
                        f"{title} ({sid}) failed to restart {n}x consecutively; last err: {err_msg}",
                        telegram=escalate_critical,
                        sid=sid,
                    )
                return False
            self.consecutive_failures[sid] = 0
            self.first_error_seen_at.pop(sid, None)  # clear stale-error timer on recovery
            log.info("restart OK %s final_status=%s", sid, final_status)
            def _send_continuity():
                time.sleep(2.0)
                send_continuity_message(sid, profile, dry_run=self.dry_run)
            threading.Thread(target=_send_continuity, daemon=True).start()
            return True

    def maybe_restart(self, sess_summary):
        sid = sess_summary.get("id")
        title = sess_summary.get("title", "?")
        profile = sess_summary.get("profile")
        if not sid:
            return
        if sid in self.removed_ids:
            return

        # Fast-path status check outside the lock — avoids rate-limit false positives
        # on an already-recovered session, and avoids holding the lock during subprocess.
        detail = show_session(sid, profile=profile)
        if not detail:
            return
        status = (detail.get("status") or "").lower()
        if status != "error":
            log.debug("session %s status=%s (not error), skip", sid, status)
            with self.lock:
                self.first_error_seen_at.pop(sid, None)
            return

        # Escalation-critical is a stricter classification than restart-critical.
        # Workers (exact-title allow-list, autorestart markers, one-offs in group=conductor)
        # still get auto-restart attempts but DO NOT escalate to Telegram.
        escalate_critical = is_escalation_critical(sess_summary) or is_escalation_critical(detail)

        with self.lock:
            now = time.time()
            self._prune_history(now)
            self.first_error_seen_at.setdefault(sid, now)

            if self._circuit_broken(now):
                log.debug("circuit breaker active; skipping %s (until %s)",
                          sid, time.strftime("%H:%M:%S", time.localtime(self.circuit_breaker_until)))
                return
            if self._session_rate_limited(sid, now):
                log.debug("session %s in 429 backoff until %s; skip",
                          sid, time.strftime("%H:%M:%S", time.localtime(self.rate_limited_until[sid])))
                return
            if self._cascade_active(now):
                log.debug("cascade settling active; skipping %s", sid)
                return
            if self._in_cooldown(sid, now):
                return
            if self._rate_limited(sid):
                # Dedup'd inside escalate(); workers never telegram.
                self.escalate(
                    "rate-limit",
                    f"Session {title} ({sid}) keeps crashing — needs human attention",
                    telegram=escalate_critical,
                    sid=sid,
                )
                return

            self.restart_history.setdefault(sid, deque()).append(now)
            self.cooldown_until[sid] = now + POST_RESTART_COOLDOWN_S

        self._do_restart(sid, title, profile, escalate_critical=escalate_critical)

    # ---- cascade detection + batch revive ----

    def _scan_critical_error_sessions(self):
        """Return list of (id, title, profile) for all critical sessions currently in error."""
        errored = []
        for s in list_all_sessions():
            if not is_critical(s):
                continue
            detail = show_session(s.get("id"), profile=s.get("profile"))
            if not detail:
                continue
            if (detail.get("status") or "").lower() == "error":
                errored.append((s.get("id"), s.get("title") or "?", s.get("profile")))
        return errored

    def _maybe_trigger_cascade(self):
        """Called by safety poll. If ≥ THRESHOLD criticals are in error, enter
        cascade mode: wait CASCADE_WAIT_S, then batch-revive all still-errored."""
        now = time.time()
        with self.lock:
            if self._cascade_active(now):
                return False
        errored = self._scan_critical_error_sessions()
        if len(errored) < CASCADE_THRESHOLD:
            return False

        self.escalate(
            "cascade-detected",
            f"{len(errored)} critical sessions in error simultaneously — waiting {CASCADE_WAIT_S}s to stabilize, then batch-reviving",
            telegram=True,
        )
        with self.lock:
            self.cascade_settling_until = now + CASCADE_WAIT_S

        def _batch_revive():
            time.sleep(CASCADE_WAIT_S)
            # Re-scan and pull full session summaries so we can compute escalation criticality
            snapshot = {s.get("id"): s for s in list_all_sessions() if s.get("id")}
            still_errored = self._scan_critical_error_sessions()
            log.info("cascade settle complete; %d still in error, reviving SERIALLY (1/min)", len(still_errored))
            # SERIAL revive — _do_restart acquires restart_lock and enforces
            # MIN_GLOBAL_RESTART_INTERVAL_S spacing, so running sequentially here
            # is what we want. The original parallel-thread design was exactly
            # what caused the 2026-04-20 429 cascade (9 simultaneous API calls).
            for sid, title, profile in still_errored:
                if self.stop_event.is_set():
                    break
                summary = snapshot.get(sid, {})
                is_esc = is_escalation_critical(summary)
                with self.lock:
                    now = time.time()
                    if self._circuit_broken(now):
                        log.info("batch-revive: circuit breaker tripped mid-batch, aborting remaining restarts")
                        break
                    if self._session_rate_limited(sid, now):
                        log.info("batch-revive: %s in 429 backoff, skip", sid)
                        continue
                    if self._rate_limited(sid):
                        self.escalate(
                            "rate-limit",
                            f"Session {title} ({sid}) keeps crashing — needs human attention",
                            telegram=is_esc,
                            sid=sid,
                        )
                        continue
                    self.restart_history.setdefault(sid, deque()).append(now)
                    self.cooldown_until[sid] = now + POST_RESTART_COOLDOWN_S
                # _do_restart is blocking and globally serialized via restart_lock.
                self._do_restart(sid, title, profile, escalate_critical=is_esc)
            with self.lock:
                self.cascade_settling_until = 0

        threading.Thread(target=_batch_revive, name="cascade-revive", daemon=True).start()
        return True

    # ---- triggers ----

    def inotify_listener(self):
        if not HOOKS_DIR.exists():
            log.warning("hooks dir missing: %s — inotify disabled", HOOKS_DIR)
            return
        while not self.stop_event.is_set():
            try:
                log.info("inotify start on %s", HOOKS_DIR)
                proc = subprocess.Popen(
                    ["inotifywait", "-m", "-q",
                     "-e", "close_write,moved_to",
                     "--format", "%f",
                     str(HOOKS_DIR)],
                    stdout=subprocess.PIPE,
                    stderr=subprocess.DEVNULL,
                    text=True,
                )
                for line in proc.stdout:
                    if self.stop_event.is_set():
                        break
                    name = line.strip()
                    if not name.endswith(".json"):
                        continue
                    instance_id = name[:-5].split("-")[0]
                    try:
                        self.event_q.put_nowait(("inotify", instance_id))
                    except queue.Full:
                        pass
                proc.terminate()
            except Exception as e:
                log.error("inotify listener crashed: %s; retrying in 5s", e)
                time.sleep(5)

    def _stale_cleanup_scan(self):
        """Silently remove sessions that:
          - have been in error state for > STALE_ERROR_CLEANUP_S, AND
          - are NOT escalation-critical (i.e. workers / one-offs, not real conductors)
        Critical sessions are NEVER auto-removed — they need human attention."""
        now = time.time()
        for s in list_all_sessions():
            sid = s.get("id")
            if not sid or sid in self.removed_ids:
                continue
            if not is_critical(s):
                continue  # we only track sessions we're responsible for
            if is_escalation_critical(s):
                continue  # never auto-remove real conductors
            detail = show_session(sid, profile=s.get("profile"))
            if not detail:
                continue
            status = (detail.get("status") or "").lower()
            if status != "error":
                with self.lock:
                    self.first_error_seen_at.pop(sid, None)
                continue
            with self.lock:
                first = self.first_error_seen_at.setdefault(sid, now)
                stuck_for = now - first
            if stuck_for < STALE_ERROR_CLEANUP_S:
                continue
            # Time's up — evict.
            title = s.get("title") or "?"
            profile = s.get("profile")
            log.warning("stale-cleanup: removing non-critical session %s (%s) stuck in error for %.0fs",
                        sid, title, stuck_for)
            args = [AGENT_DECK_BIN]
            if profile:
                args += ["-p", profile]
            args += ["remove", sid]
            rc, _, err = run_cmd(args, timeout=20)
            if rc == 0:
                with self.lock:
                    self.removed_ids.add(sid)
                    self.first_error_seen_at.pop(sid, None)
                    self.restart_history.pop(sid, None)
                    self.cooldown_until.pop(sid, None)
                self._audit(RESTART_LOG, {
                    "ts": now, "id": sid, "title": title, "profile": profile,
                    "action": "stale-removed", "stuck_for_s": int(stuck_for),
                })
                # Local-only log, NO telegram (these are noisy one-offs, that's the whole point)
                log.info("stale-cleanup: removed %s (%s)", sid, title)
            else:
                log.warning("stale-cleanup: failed to remove %s rc=%d err=%s", sid, rc, err.strip()[:200])

    def safety_poll(self):
        last_stale_scan = 0.0
        while not self.stop_event.is_set():
            try:
                if not self._maybe_trigger_cascade():
                    sessions = list_all_sessions()
                    for s in sessions:
                        if is_critical(s) and s.get("id") not in self.removed_ids:
                            try:
                                self.event_q.put_nowait(("poll", s))
                            except queue.Full:
                                pass
                now = time.time()
                if now - last_stale_scan > STALE_CLEANUP_SCAN_INTERVAL_S:
                    self._stale_cleanup_scan()
                    last_stale_scan = now
            except Exception as e:
                log.error("safety poll error: %s", e)
            self.stop_event.wait(SAFETY_POLL_INTERVAL_S)

    def dispatcher(self):
        cache = {}
        cache_refreshed = 0.0
        CACHE_TTL_S = 10.0
        while not self.stop_event.is_set():
            try:
                kind, payload = self.event_q.get(timeout=1.0)
            except queue.Empty:
                continue
            try:
                if kind == "poll":
                    if is_critical(payload):
                        self.maybe_restart(payload)
                elif kind == "inotify":
                    now = time.time()
                    if now - cache_refreshed > CACHE_TTL_S:
                        sessions = list_all_sessions()
                        cache = {s.get("id"): s for s in sessions if s.get("id")}
                        cache_refreshed = now
                    sess = cache.get(payload)
                    if sess and is_critical(sess):
                        self.maybe_restart(sess)
            except Exception as e:
                log.error("dispatcher error: %s", e)

    # ---- v1.7.63 capability A: poller-existence check ----

    def check_poller_existence(self, now=None):
        """For each conductor session whose `channels` declares the telegram
        plugin, verify a bun-telegram process with the matching TELEGRAM_STATE_DIR
        is running. If missing, fire one `agent-deck session restart <sid>` —
        deduped to at most one per conductor per POLLER_RESTART_DEDUP_S.

        Returns the list of session IDs for which a restart was actually issued
        (for test and log observability)."""
        if now is None:
            now = time.time()
        running_state_dirs = bun_telegram_state_dirs()
        restarted = []
        for sess in list_all_sessions():
            sid = sess.get("id")
            if not sid:
                continue
            channels = sess.get("channels") or []
            if TELEGRAM_CHANNEL_NAME not in channels:
                continue
            expected = parse_envfile_state_dir(sess.get("env_file"))
            if not expected:
                continue
            if expected in running_state_dirs:
                continue
            last = self.last_poller_restart_at.get(sid, 0.0)
            if now - last < POLLER_RESTART_DEDUP_S:
                log.debug("poller missing for %s but dedup-gated (%.0fs ago)",
                          sid, now - last)
                continue
            log.warning("telegram poller missing for %s (expected state_dir=%s) — restarting session",
                        sid, expected)
            ok = poller_restart_session(sid, sess.get("profile"), dry_run=self.dry_run)
            if ok:
                self.last_poller_restart_at[sid] = now
                restarted.append(sid)
        return restarted

    # ---- v1.7.63 capability B: waiting-too-long patrol ----

    def check_waiting_too_long(self, now=None):
        """For each child session (parent_session_id set) in status=waiting,
        check whether its pane has been unchanged for >WAITING_PATROL_THRESHOLD_S.
        If so, inject a 'report status?' nudge via `agent-deck session send`.
        Dedup at most one nudge per session per WAITING_PATROL_NUDGE_DEDUP_S.

        Returns the list of session IDs that were nudged this tick."""
        if now is None:
            now = time.time()
        nudged = []
        active_sids = set()
        for sess in list_all_sessions():
            sid = sess.get("id")
            if not sid:
                continue
            if not sess.get("parent_session_id"):
                continue
            status = (sess.get("status") or "").lower()
            if status != "waiting":
                continue
            active_sids.add(sid)
            pane_hash = compute_pane_hash(sid, sess.get("profile"))
            tracker = self.waiting_tracker.get(sid)
            if tracker is None or tracker["pane_hash"] != pane_hash:
                self.waiting_tracker[sid] = {
                    "first_seen_at": now,
                    "pane_hash": pane_hash,
                    "last_nudge_at": tracker["last_nudge_at"] if tracker else 0.0,
                }
                continue
            if now - tracker["first_seen_at"] < WAITING_PATROL_THRESHOLD_S:
                continue
            last_nudge = tracker["last_nudge_at"]
            if last_nudge and now - last_nudge < WAITING_PATROL_NUDGE_DEDUP_S:
                continue
            log.warning("session %s waiting > %ds with unchanged pane — nudging",
                        sid, int(now - tracker["first_seen_at"]))
            if send_status_query(sid, sess.get("profile"), dry_run=self.dry_run):
                tracker["last_nudge_at"] = now
                nudged.append(sid)
        # Cull tracker entries for sessions that are no longer waiting.
        for sid in list(self.waiting_tracker.keys()):
            if sid not in active_sids:
                self.waiting_tracker.pop(sid, None)
        return nudged

    def run(self):
        threads = [
            threading.Thread(target=self.inotify_listener, name="inotify", daemon=True),
            threading.Thread(target=self.safety_poll, name="poll", daemon=True),
            threading.Thread(target=self.dispatcher, name="dispatch", daemon=True),
        ]
        for t in threads:
            t.start()
        while not self.stop_event.is_set():
            time.sleep(1)

    def stop(self):
        self.stop_event.set()


# ------------------------------------------------------------------
# CLI
# ------------------------------------------------------------------

def setup_logging(verbose=False):
    logging.basicConfig(
        level=logging.DEBUG if verbose else logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s %(threadName)s: %(message)s",
    )


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dry-run", action="store_true")
    ap.add_argument("--once", action="store_true")
    ap.add_argument("--verbose", action="store_true")
    args = ap.parse_args()

    setup_logging(verbose=args.verbose)
    WATCHDOG_DIR.mkdir(parents=True, exist_ok=True)
    AUTORESTART_DIR.mkdir(parents=True, exist_ok=True)

    wd = Watchdog(dry_run=args.dry_run)

    if args.once:
        log.info("--once: running single safety poll (cascade check + critical-error scan)")
        if wd._maybe_trigger_cascade():
            log.info("cascade triggered; waiting for batch revive to finish")
            time.sleep(CASCADE_WAIT_S + 10)
        else:
            for s in list_all_sessions():
                if is_critical(s):
                    log.info("critical candidate: %s (%s) profile=%s",
                             s.get("id"), s.get("title"), s.get("profile"))
                    wd.maybe_restart(s)
            # give continuity-send threads a moment
            time.sleep(3)
        return 0

    def sigterm(signum, frame):
        log.info("signal %d received, shutting down", signum)
        wd.stop()

    signal.signal(signal.SIGTERM, sigterm)
    signal.signal(signal.SIGINT, sigterm)
    log.info("watchdog starting dry_run=%s", args.dry_run)
    try:
        wd.run()
    finally:
        wd.stop()
    return 0


if __name__ == "__main__":
    sys.exit(main())
