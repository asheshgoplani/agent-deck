#!/usr/bin/env python3
"""Goal manager — runs the verifier, reads receipts, nudges the worker.

A one-shot script (intentionally — no daemon, no signal handling). Run
periodically via cron or by hand. Each invocation walks every active
goal JSON in ~/.agent-deck/goals/ and advances its state by one tick:

    1. Run the goal's done_cmd. If exit 0 → mark done, stop worker.
    2. Read task-log.md tail; look for receipts newer than what we've
       already seen. A STUCK receipt escalates and stops the worker.
    3. No new receipt and idle longer than max_idle_seconds → nudge.
    4. If nudges_sent >= escalate_after → write a stuck bundle, log loudly.
    5. If cycles_completed >= max_cycles → finalize as failed.

Opt-in per goal (schedule keys; a goal without them runs exactly the loop
above):

    no_progress_cap N      after N consecutive empty turns (a due idle
                           wake with no progress) the goal escalates, or
                           with no_progress_action "stop" ends as failed
                           and stops the worker. Wakes then wait a doubling
                           backoff window (no_progress_backoff_base_seconds,
                           default max_idle_seconds; 2x, 4x, ...).
    worktree_progress      a change of the worktree content hash counts as
                           progress too. A new receipt always does.
    worktree_gate          a quiet tick skips done_cmd while the worktree
                           hash equals the one it last failed on; with the
                           cap on, a wake is skipped after 2 delivered wakes
                           over the same unchanged failed worktree.

Run budgets: after the verifier and before any wake, a goal whose
schedule.max_tokens (tokens the worker session used, from `agent-deck costs
sessions --json`) or schedule.max_minutes (wall time since created_at) is
spent is finalized as failed (budget_exceeded) and its worker is stopped. A
nudge that `session send` refuses (code BUDGET_EXCEEDED) because an
enforced [costs.budgets] limit is spent is not retried: a daily, weekly or
monthly limit pauses the goal until its window resets (only done_cmd runs
meanwhile), a session lifetime limit ends it as failed. max_cycles 0 is no
cap.

Phase 1: no Telegram, no daemon, no PID files. Escalation logs to stderr
and writes ~/.agent-deck/goals/escalations/<id>-<ts>.md. Wire up real
push (Telegram, ntfy, etc.) in Phase 3.

Usage:
    python3 manager.py            # walks all goals once
    python3 manager.py --id <slug>     # walk one specific goal
    python3 manager.py --dry-run       # print what would happen, change nothing
    python3 manager.py --verbose       # detailed per-step logging
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shlex
import subprocess
import sys
import time
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from pathlib import Path

# Retry policy for nudge delivery (regression: issue #976).
# An autonomous ScheduleWakeup loop went silent for ~5h when a burst of
# upstream 5xx errors landed on the wake-up that nudges the worker. The
# retry below absorbs transient failures and emits a log line per attempt
# so a stalled loop is observable instead of silent.
BACKOFF_SECONDS = (1, 5, 30)


def log_attempt(attempt: int, total: int, *, reason: str) -> None:
    """Emit one observability line per wake-up attempt (#976)."""
    print(
        f"[manager] wakeup attempt {attempt}/{total}: {reason}",
        file=sys.stderr,
    )

# `session send --json` refuses a send to a target over an enforced
# [costs.budgets] limit with exit 1 and this code; retrying cannot help.
BUDGET_EXCEEDED_CODE = "BUDGET_EXCEEDED"


class BudgetRefused(Exception):
    """agent-deck refused the wake because a cost budget is spent.

    resets_at is when the limit's window starts over (a daily, weekly or
    monthly limit), or None for a limit that does not reset (a session's
    lifetime limit)."""

    def __init__(self, reason: str, resets_at: str | None = None):
        super().__init__(reason)
        self.resets_at = resets_at


def budget_refusal(stdout: str) -> BudgetRefused | None:
    """The BudgetRefused a failed `session send --json` reports, or None."""
    try:
        payload = json.loads(stdout or "")
    except ValueError:
        return None
    if not isinstance(payload, dict) or payload.get("code") != BUDGET_EXCEEDED_CODE:
        return None
    return BudgetRefused(str(payload.get("error") or "budget exceeded"), payload.get("resets_at") or None)

GOALS_DIR = Path.home() / ".agent-deck" / "goals"
ESCALATIONS_DIR = GOALS_DIR / "escalations"
HISTORY_DIR = GOALS_DIR / "history"

# A receipt is a markdown block headed by `## <iso timestamp>`. The rest
# of the block (cycle/changed/next/blockers) is preserved verbatim for the
# escalation bundle but only the timestamp is the structural signal.
RECEIPT_HEADING_RE = re.compile(r"^## (\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)\s*$", re.M)


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def vlog(verbose: bool, msg: str) -> None:
    if verbose:
        print(f"[manager] {msg}", file=sys.stderr)


def load_goal(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def save_goal(path: Path, data: dict, dry_run: bool) -> None:
    if dry_run:
        return
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")
    tmp.replace(path)


def record_event(goal: dict, event: str, detail: str) -> None:
    history = goal.setdefault("history", [])
    history.append({"ts": now_iso(), "event": event, "detail": detail})
    # Keep history bounded so JSON doesn't grow forever
    if len(history) > 200:
        goal["history"] = history[-200:]


# run_done_cmd's return code for a verifier that timed out (no verdict).
VERIFIER_TIMEOUT_RC = 124


def run_done_cmd(done_cmd: str, timeout_s: int = 30) -> tuple[int, str]:
    """Run the external verifier. Returns (returncode, combined_output)."""
    try:
        r = subprocess.run(
            done_cmd, shell=True, capture_output=True, text=True, timeout=timeout_s
        )
        out = (r.stdout or "") + (r.stderr or "")
        return r.returncode, out.strip()[:500]
    except subprocess.TimeoutExpired:
        return VERIFIER_TIMEOUT_RC, "verifier timed out"
    except Exception as e:  # noqa: BLE001  (defensive at boundary)
        return 1, f"verifier raised: {e}"


# No-progress cap (ported from prime-agent's goal driver), opt-in per goal
# through schedule.no_progress_cap. A turn closes when the manager sees a
# new receipt or an idle wake falls due; a turn without progress is empty,
# and this many consecutive empty turns end the goal's run. Absent or 0 is
# off, the loop from before the cap existed.
DEFAULT_NO_PROGRESS_CAP = 0

# What reaching the cap does (schedule.no_progress_action): "escalate" pages
# the user and leaves the worker running, as the stuck-nudge escalation
# does; "stop" ends the goal as failed and stops the worker.
NO_PROGRESS_ACTIONS = ("escalate", "stop")

# Delivered wakes over one unchanged, failed worktree before the gate skips
# further wakes on it: a single wake may not have reached the worker's turn.
WAKES_BEFORE_SKIP = 2

# Upper bound for one doubling wake backoff window (7 days), so a long
# streak with the cap off cannot overflow the date arithmetic.
MAX_WAKE_BACKOFF_SECONDS = 7 * 24 * 3600

# The worker's receipt file. It is excluded from the worktree snapshot:
# appending a receipt is not progress on the goal.
TASK_LOG_NAME = "task-log.md"

# Above this many untracked files the snapshot is skipped (None): hashing
# them every tick would cost more than the verifier run it saves.
MAX_SNAPSHOT_UNTRACKED_FILES = 5000

# Untracked files above this size are hashed by path, size and mtime, not
# by content, so a large build artefact does not cost a full read per tick.
MAX_HASHED_UNTRACKED_BYTES = 1 << 20

# Submodule nesting the worktree hash follows.
MAX_SUBMODULE_DEPTH = 4


def parse_iso(ts: str | None) -> datetime | None:
    """Parse an ISO timestamp (Z or offset). A naive value (a receipt heading
    written without an offset) is taken as the host's local time."""
    if not ts:
        return None
    try:
        dt = datetime.fromisoformat(ts.replace("Z", "+00:00"))
    except ValueError:
        return None
    return dt if dt.tzinfo else dt.astimezone(timezone.utc)


def worktree_tree_hash(workdir: Path, timeout_s: int = 30, _depth: int = 0) -> str | None:
    """Content hash of the worker's git worktree, untracked files included.

    The read-only equivalent of `git add -A && git write-tree`: the HEAD
    tree id, the binary diff of tracked files against it, the porcelain
    status, the bytes of every untracked, non-ignored file (path, size and mtime above
    MAX_HASHED_UNTRACKED_BYTES), and the same hash of each submodule's
    worktree, so edits inside a submodule count too. Nothing is written to
    the repository (no index refresh, no objects). task-log.md is excluded. Returns None when the workdir is not a git worktree, is
    the home directory, has too many untracked files, or any git call
    fails; None means "always rerun the verifier".
    """
    try:
        if not workdir.is_dir() or workdir.resolve() == Path.home().resolve():
            return None

        def git(*args: str) -> bytes | None:
            r = subprocess.run(
                ["git", "--no-optional-locks", "-C", str(workdir), *args],
                capture_output=True, timeout=timeout_s,
            )
            return r.stdout if r.returncode == 0 else None

        pathspec = ("--", ".", f":(exclude){TASK_LOG_NAME}")
        top = git("rev-parse", "--show-toplevel")
        if top is None:
            return None
        root = Path(top.strip().decode("utf-8", "surrogateescape"))  # porcelain paths are relative to the top
        base = git("rev-parse", "--verify", "-q", "HEAD^{tree}")
        if base is None:  # unborn branch: diff against the empty tree
            base = git("hash-object", "-t", "tree", "/dev/null")
        if base is None:
            return None
        base = base.strip()
        status = git("status", "--porcelain=v1", "-z", "-uall", "--no-renames", *pathspec)
        # Plumbing diff-index, not `git diff`: porcelain diff refreshes a
        # stat-dirty index and writes it even with --no-optional-locks.
        diff = git("diff-index", "-p", "--binary", "--no-ext-diff", base.decode(), *pathspec)
        submodules = git("submodule", "status")
        if status is None or diff is None or submodules is None:
            return None

        h = hashlib.sha256()
        for part in (base, status, diff):
            h.update(part)
            h.update(b"\0")
        untracked = [
            e[3:].decode("utf-8", "surrogateescape")
            for e in status.split(b"\0") if e.startswith(b"?? ")
        ]
        if len(untracked) > MAX_SNAPSHOT_UNTRACKED_FILES:
            return None
        for rel in sorted(untracked):
            fp = root / rel
            h.update(rel.encode("utf-8", "surrogateescape") + b"\0")
            if fp.is_symlink():
                h.update(os.readlink(fp).encode("utf-8", "surrogateescape"))
            elif fp.is_file() and (st := fp.stat()).st_size > MAX_HASHED_UNTRACKED_BYTES:
                h.update(f"size={st.st_size} mtime_ns={st.st_mtime_ns}".encode())
            elif fp.is_file():
                with fp.open("rb") as f:
                    for chunk in iter(lambda: f.read(1 << 20), b""):
                        h.update(chunk)
            h.update(b"\0")
        # Each initialized submodule ("<flag><sha> <path> (<describe>)"):
        # its checkout and uncommitted work, which the parent's diff shows
        # only as "-dirty". Nested ones are reached by the recursion.
        for line in submodules.decode("utf-8", "surrogateescape").splitlines():
            fields = line[1:].split()
            if line.startswith("-") or len(fields) < 2 or _depth >= MAX_SUBMODULE_DEPTH:
                continue
            h.update(fields[1].encode("utf-8", "surrogateescape") + b"\0")
            sub = worktree_tree_hash(workdir / fields[1], timeout_s, _depth + 1)  # paths are relative to -C
            h.update((sub or "unhashable").encode() + b"\0")
        return h.hexdigest()
    except (OSError, subprocess.SubprocessError):
        return None


@dataclass
class Receipt:
    ts: str          # the heading timestamp, raw
    body: str        # everything from the heading through the next heading (or EOF)


def parse_task_log_tail(path: Path, max_bytes: int = 65536) -> list[Receipt]:
    """Read the tail of task-log.md and return receipts in file order."""
    if not path.exists():
        return []
    size = path.stat().st_size
    with path.open("rb") as f:
        if size > max_bytes:
            f.seek(size - max_bytes)
            # Discard partial line at the top
            f.readline()
        text = f.read().decode("utf-8", errors="replace")

    # Split on receipt headings, preserve them
    indices = [m.start() for m in RECEIPT_HEADING_RE.finditer(text)]
    receipts: list[Receipt] = []
    for i, start in enumerate(indices):
        end = indices[i + 1] if i + 1 < len(indices) else len(text)
        block = text[start:end].strip()
        heading_match = RECEIPT_HEADING_RE.match(block)
        if heading_match:
            receipts.append(Receipt(ts=heading_match.group(1), body=block))
    return receipts


def newer_than(ts_a: str, ts_b: str | None) -> bool:
    """True if ts_a > ts_b (or ts_b is None). Lexicographic on ISO timestamps."""
    if ts_b is None:
        return True
    return ts_a > ts_b


def agent_deck_send(session_id_or_title: str, message: str, dry_run: bool, verbose: bool) -> bool:
    """Send a nudge to a worker. Returns True on success.

    Retries on transient failures with exponential backoff (BACKOFF_SECONDS).
    Logs every attempt with the reason so a stalled autonomous loop is
    observable instead of silent. Regression fix for issue #976.
    """
    cmd = ["agent-deck", "session", "send", session_id_or_title, message, "--no-wait", "-q", "--json"]
    vlog(verbose, "nudge cmd: " + " ".join(shlex.quote(c) for c in cmd))
    if dry_run:
        return True

    total = len(BACKOFF_SECONDS)
    for i in range(total):
        try:
            r = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
            if r.returncode == 0:
                log_attempt(i + 1, total, reason="ok")
                return True
            reason = (r.stderr or r.stdout or "non-zero exit").strip() or "non-zero exit"
        except Exception as e:  # noqa: BLE001
            reason = f"exception: {e}"
        else:
            refused = budget_refusal(r.stdout)
            if refused is not None:
                log_attempt(i + 1, total, reason=str(refused))
                raise refused

        log_attempt(i + 1, total, reason=reason)
        if i < total - 1:
            time.sleep(BACKOFF_SECONDS[i])

    return False


def agent_deck_stop(session_id_or_title: str, dry_run: bool, verbose: bool) -> bool:
    cmd = ["agent-deck", "session", "stop", session_id_or_title, "--quiet"]
    vlog(verbose, "stop cmd: " + " ".join(shlex.quote(c) for c in cmd))
    if dry_run:
        return True
    try:
        subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        return True
    except Exception as e:  # noqa: BLE001
        print(f"[manager] stop error: {e}", file=sys.stderr)
        return False


# Row cap for `costs sessions`; a full page means the worker may be cut off.
COST_ROWS_LIMIT = 500


def worker_tokens(goal: dict) -> int | None:
    """Tokens the worker session used so far (input, output and cache
    write), read from `agent-deck costs sessions --json`. Cache reads are
    left out: every turn re-reads the cached context, so they grow with the
    conversation length rather than with the work done, and would end a long
    run long before its real spend. 0 when the
    session has no cost events yet; None when the figure is unavailable
    (cost tracking off, an older agent-deck, or any CLI failure)."""
    cmd = ["agent-deck"]
    if goal.get("profile"):
        cmd += ["-p", goal["profile"]]
    cmd += ["costs", "sessions", "--json", "--period", "all", "--limit", str(COST_ROWS_LIMIT)]
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        if r.returncode != 0:
            return None
        rows = json.loads(r.stdout or "[]")
    except Exception:  # noqa: BLE001  (defensive at boundary)
        return None
    if not isinstance(rows, list):
        return None
    for row in rows:
        if isinstance(row, dict) and row.get("session_id") == goal.get("worker_session_id"):
            return sum(int(row.get(k) or 0) for k in ("input_tokens", "output_tokens", "cache_write"))
    return 0 if len(rows) < COST_ROWS_LIMIT else None


def run_budget_exceeded(goal: dict) -> str | None:
    """The reason a goal's run budget is spent, or None. Unset or 0 caps are
    off. A token cap whose usage cannot be read is recorded, not enforced."""
    schedule, state = goal["schedule"], goal["state"]
    max_minutes = int(schedule.get("max_minutes") or 0)
    if max_minutes > 0 and state.get("created_at"):
        try:
            created = datetime.fromisoformat(state["created_at"].replace("Z", "+00:00"))
            minutes = (datetime.now(timezone.utc) - created).total_seconds() / 60
        except ValueError:
            minutes = None
        if minutes is not None and minutes >= max_minutes:
            return f"max_minutes: ran {int(minutes)} of {max_minutes} minutes"
    max_tokens = int(schedule.get("max_tokens") or 0)
    if max_tokens > 0:
        used = worker_tokens(goal)
        if used is None:
            # Once per change, not every tick.
            if not state.get("tokens_unmeasured"):
                state["tokens_unmeasured"] = True
                record_event(goal, "budget_unmeasured", "worker token use unavailable; max_tokens not enforced until it is")
        else:
            state.pop("tokens_unmeasured", None)
            state["tokens_used"] = used
            if used >= max_tokens:
                return f"max_tokens: used {used} of {max_tokens} tokens"
    return None


def stop_over_budget(path: Path, goal: dict, reason: str, dry_run: bool, verbose: bool) -> None:
    finalize(goal, "failed", f"budget_exceeded: {reason}")
    record_event(goal, "budget_exceeded", reason)
    agent_deck_stop(goal.get("worker_session_title") or goal["worker_session_id"], dry_run, verbose)
    write_history_artifact(goal)
    print(f"[manager] BUDGET EXCEEDED: {goal['id']} ({reason})", file=sys.stderr)
    save_goal(path, goal, dry_run)


def handle_budget_refusal(path: Path, goal: dict, refused: BudgetRefused, dry_run: bool, verbose: bool) -> None:
    """A wake refused for a spent cost budget. A limit whose window resets
    (daily, weekly, monthly) pauses the goal until then, worker untouched;
    one that does not (a session lifetime limit) ends it as failed."""
    resets_at = parse_iso(refused.resets_at)
    if resets_at is None:
        stop_over_budget(path, goal, str(refused)[:300], dry_run, verbose)
        return
    goal["state"]["budget_paused_until"] = resets_at.isoformat()
    record_event(goal, "budget_paused", f"{str(refused)[:200]}; paused until {resets_at.isoformat()}")
    print(f"[manager] BUDGET PAUSED: {goal['id']} until {resets_at.isoformat()} ({str(refused)[:120]})", file=sys.stderr)
    save_goal(path, goal, dry_run)


def walk_budget_paused(path: Path, goal: dict, dry_run: bool, verbose: bool) -> bool:
    """While a budget pause lasts, a tick only runs done_cmd (it costs no
    tokens): done ends the goal, otherwise nothing else happens. True when
    the tick was handled here."""
    state = goal["state"]
    until = parse_iso(state.get("budget_paused_until"))
    if until is None:
        return False
    if datetime.now(timezone.utc) >= until:
        state.pop("budget_paused_until", None)
        record_event(goal, "budget_resumed", "budget window reset")
        return False
    rc, _out = run_done_cmd(goal["done_cmd"], timeout_s=30)
    state["last_verified_at"] = now_iso()
    vlog(verbose, f"budget paused until {until.isoformat()}; verifier rc={rc}")
    if rc == 0:
        record_event(goal, "verifier_check", "rc=0")
        finish_done(goal, path, goal.get("worker_session_title") or goal["worker_session_id"], dry_run, verbose)
        return True
    save_goal(path, goal, dry_run)
    return True


def build_nudge(goal: dict, idle_minutes: int) -> str:
    last = goal["state"].get("last_receipt_text") or "(no prior receipt)"
    goal_text = goal["goal"]
    escalate_after = goal["schedule"]["escalate_after_stuck_nudges"]
    nudges_sent = goal["state"]["nudges_sent"] + 1  # this is the count after we send
    return (
        f"[GOAL NUDGE — nudge {nudges_sent}/{escalate_after}]\n"
        f"\n"
        f"No progress receipt for {idle_minutes} minutes on goal:\n"
        f'  "{goal_text}"\n'
        f"\n"
        f"Last receipt:\n"
        f"  {last[:300]}\n"
        f"\n"
        f"The manager just re-ran the done-condition: NOT YET MET.\n"
        f"\n"
        f"Pick ONE within the next 5 minutes:\n"
        f"  a) Try a different angle. The previous direction didn't produce a receipt — what's a different decomposition of the goal?\n"
        f"  b) If you're waiting on something external (CI, review, third-party), VERIFY it's actually blocking (don't assume — check), then write STUCK with the specific external blocker.\n"
        f"  c) If you've genuinely tried everything, write STUCK: <reason> to task-log.md and exit cleanly. The manager will escalate to the user.\n"
        f"\n"
        f"Contract reminder: ONE bounded step + ONE receipt per cycle. "
        f"Do not investigate forever; act."
    )


def write_escalation_bundle(goal: dict, idle_minutes: int, headline: str | None = None) -> Path:
    ESCALATIONS_DIR.mkdir(parents=True, exist_ok=True)
    ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H-%M-%S")
    out = ESCALATIONS_DIR / f"{goal['id']}-{ts}.md"
    last = goal["state"].get("last_receipt_text") or "(none)"
    recent = goal.get("history", [])[-6:]
    recent_str = "\n".join(
        f"- {e['ts']} {e['event']}: {e['detail'][:120]}" for e in recent
    )
    headline = headline or (
        f"Stuck after {goal['state']['nudges_sent']} nudges, no receipt in {idle_minutes} minutes."
    )
    body = (
        f"# Goal escalation: {goal['goal']}\n"
        f"\n"
        f"{headline}\n"
        f"\n"
        f"- Goal id: `{goal['id']}`\n"
        f"- Worker: `{goal.get('worker_session_title')}` (`{goal.get('worker_session_id')}`)\n"
        f"- Verifier: `{goal['done_cmd']}`\n"
        f"  Last check: NOT MET ({goal['state'].get('last_verified_at')})\n"
        f"\n"
        f"## Last receipt\n"
        f"{last}\n"
        f"\n"
        f"## Recent events\n"
        f"{recent_str}\n"
        f"\n"
        f"## Options\n"
        f"- Hint and resume: `agent-deck session send {goal.get('worker_session_title')} \"<your hint>\"`\n"
        f"- Inspect worker:  `agent-deck session output {goal.get('worker_session_title')} -q`\n"
        f"- Cancel:          mark this goal `stopped_by_user` in `{GOALS_DIR}/{goal['id']}.json`\n"
    )
    out.write_text(body, encoding="utf-8")
    return out


def write_history_artifact(goal: dict) -> None:
    HISTORY_DIR.mkdir(parents=True, exist_ok=True)
    out = HISTORY_DIR / f"{goal['id']}-{goal['state']['status']}.md"
    out.write_text(json.dumps(goal, indent=2) + "\n", encoding="utf-8")


def finalize(goal: dict, status: str, reason: str) -> None:
    goal["state"]["status"] = status
    goal["state"]["ended_at"] = now_iso()
    goal["state"]["ended_reason"] = reason
    record_event(goal, "finalize", f"{status}: {reason}")


def check_verifier(goal: dict, tree_hash: str | None, verbose: bool) -> int:
    """Run done_cmd once and record the verdict.

    A failed verdict remembers the worktree tree hash it was taken on, so
    the next tick can skip the rerun while the worktree is unchanged. A
    timeout is not a verdict, so it never arms the skip.
    """
    state = goal["state"]
    rc, _out = run_done_cmd(goal["done_cmd"])
    state["last_verified_at"] = now_iso()
    record_event(goal, "verifier_check", f"rc={rc}")
    vlog(verbose, f"verifier rc={rc}")
    if rc != 0:
        state["last_failed_tree_hash"] = tree_hash if rc != VERIFIER_TIMEOUT_RC else None
    return rc


def finish_done(goal: dict, path: Path, worker: str, dry_run: bool, verbose: bool) -> None:
    finalize(goal, "done", "verifier passed")
    agent_deck_stop(worker, dry_run, verbose)
    write_history_artifact(goal)
    print(f"[manager] DONE: {goal['id']} ({goal['goal']})", file=sys.stderr)
    save_goal(path, goal, dry_run)


def idle_seconds_since_activity(state: dict, fallback: float) -> float:
    """Seconds since the newest of: last receipt, last progress, creation."""
    stamps = [
        parse_iso(state.get(key))
        for key in ("last_receipt_seen_at", "last_progress_at", "created_at")
    ]
    stamps = [s for s in stamps if s is not None]
    if not stamps:
        return fallback  # force a wake if nothing parses
    return (datetime.now(timezone.utc) - max(stamps)).total_seconds()


def wake_backoff_seconds(base: int, streak: int) -> int:
    """Doubling wake window after `streak` empty turns: base, 2x, 4x, ... (clamped)."""
    return min(base * 2 ** min(streak - 1, 32), MAX_WAKE_BACKOFF_SECONDS)


def progress_features(schedule: dict) -> tuple[int, bool, bool]:
    """(cap, worktree_progress, worktree_gate) from a goal's schedule; all
    off for a goal file that does not set them."""
    cap = max(int(schedule.get("no_progress_cap") or DEFAULT_NO_PROGRESS_CAP), 0)
    return cap, bool(schedule.get("worktree_progress", False)), bool(schedule.get("worktree_gate", False))


def end_no_progress(goal: dict, path: Path, worker: str, streak: int, idle_minutes: int,
                    dry_run: bool, verbose: bool) -> None:
    """Act on the no-progress cap: escalate (default) or end and stop."""
    state = goal["state"]
    action = goal["schedule"].get("no_progress_action") or "escalate"
    if action not in NO_PROGRESS_ACTIONS:
        action = "escalate"
    record_event(goal, "no_progress_cap", f"{streak} empty turns; action={action}")
    if action == "stop":
        finalize(goal, "failed", "no_progress_cap")
        headline = f"No progress in {streak} consecutive turns: ended as failed (no_progress_cap), worker stopped."
    else:
        state["status"] = "escalated"
        headline = f"No progress in {streak} consecutive turns (no_progress_cap): escalated, worker left running."
    bundle = write_escalation_bundle(goal, idle_minutes, headline)
    state["escalated_at"] = now_iso()
    record_event(goal, "escalated", str(bundle))
    if action == "stop":
        agent_deck_stop(worker, dry_run, verbose)
        write_history_artifact(goal)
        print(f"[manager] FAILED (no progress in {streak} turns): {goal['id']}; bundle at {bundle}", file=sys.stderr)
    else:
        print(f"[manager] ESCALATED (no progress in {streak} turns): {goal['id']}; bundle at {bundle}", file=sys.stderr)
    save_goal(path, goal, dry_run)


def apply_new_receipts(goal: dict, new_receipts: list[Receipt], path: Path, worker: str,
                       dry_run: bool, verbose: bool) -> bool:
    """Record the newest receipt. True when it reported STUCK (the goal is
    escalated, the worker stopped and the goal saved)."""
    state = goal["state"]
    newest = new_receipts[-1]
    state["last_receipt_seen_at"] = newest.ts
    state["last_receipt_text"] = newest.body[:1000]
    state["cycles_completed"] = state.get("cycles_completed", 0) + 1
    state["nudges_sent"] = 0
    record_event(goal, "receipt", newest.ts)
    vlog(verbose, f"new receipt at {newest.ts}; cycles={state['cycles_completed']}")

    # STUCK marker. If the worker wrote STUCK: in its most recent receipt,
    # it's telling us it can't proceed. Escalate immediately with the
    # worker's own reason rather than continuing to nudge it. This is the
    # contract: STUCK is the worker's honest signal that it needs help.
    if "STUCK:" not in newest.body and "STUCK :" not in newest.body:
        return False
    stuck_line = next(
        (line for line in newest.body.splitlines() if "STUCK" in line),
        "STUCK (no detail line)",
    )
    bundle = write_escalation_bundle(goal, 0)
    state["status"] = "escalated"
    state["escalated_at"] = now_iso()
    state["ended_reason"] = f"worker reported {stuck_line.strip()[:200]}"
    record_event(goal, "stuck_reported", stuck_line.strip()[:200])
    record_event(goal, "escalated", str(bundle))
    print(
        f"[manager] WORKER STUCK: {goal['id']} — {stuck_line.strip()[:120]}",
        file=sys.stderr,
    )
    print(f"[manager] bundle at {bundle}", file=sys.stderr)
    agent_deck_stop(worker, dry_run, verbose)
    save_goal(path, goal, dry_run)
    return True


def enforce_max_cycles(goal: dict, worker: str, dry_run: bool, verbose: bool) -> None:
    """max_cycles 0 (goal.sh --max-turns 0) is no cap."""
    max_cycles = int(goal["schedule"].get("max_cycles") or 0)
    if max_cycles > 0 and goal["state"].get("cycles_completed", 0) >= max_cycles:
        finalize(goal, "failed", "max_cycles_exceeded")
        agent_deck_stop(worker, dry_run, verbose)
        write_history_artifact(goal)
        print(f"[manager] FAILED (max cycles): {goal['id']}", file=sys.stderr)


def walk_goal(path: Path, dry_run: bool, verbose: bool) -> None:
    goal = load_goal(path)
    if goal.get("state", {}).get("status") != "active":
        vlog(verbose, f"skip {path.name}: status={goal.get('state', {}).get('status')}")
        return
    if walk_budget_paused(path, goal, dry_run, verbose):
        return
    if any(progress_features(goal["schedule"])):
        walk_goal_progress(path, goal, dry_run, verbose)
    else:
        walk_goal_classic(path, goal, dry_run, verbose)


def walk_goal_classic(path: Path, goal: dict, dry_run: bool, verbose: bool) -> None:
    """One tick of a goal that opted into none of the progress features:
    verifier every tick, a nudge on every tick once idle, escalation after
    escalate_after_stuck_nudges nudges."""
    pid = goal["id"]
    schedule = goal["schedule"]
    state = goal["state"]
    worker = goal.get("worker_session_title") or goal["worker_session_id"]

    vlog(verbose, f"=== {pid} ===")

    # Step 1: verifier
    rc, _out = run_done_cmd(goal["done_cmd"], timeout_s=30)
    state["last_verified_at"] = now_iso()
    record_event(goal, "verifier_check", f"rc={rc}")
    vlog(verbose, f"verifier rc={rc}")
    if rc == 0:
        finish_done(goal, path, worker, dry_run, verbose)
        return

    # Step 1b: run budget (tokens, minutes) before any further wake
    over = run_budget_exceeded(goal)
    if over:
        stop_over_budget(path, goal, over, dry_run, verbose)
        return

    # Step 2: scan for new receipts
    workdir = Path(goal.get("workdir", str(Path.home())))
    receipts = parse_task_log_tail(workdir / TASK_LOG_NAME)
    new_receipts = [r for r in receipts if newer_than(r.ts, state.get("last_receipt_seen_at"))]

    if new_receipts:
        if apply_new_receipts(goal, new_receipts, path, worker, dry_run, verbose):
            return
    else:
        # Step 3: nudge if idle
        last_activity = parse_iso(state.get("last_receipt_seen_at") or state.get("created_at"))
        if last_activity is not None:
            idle_seconds = (datetime.now(timezone.utc) - last_activity).total_seconds()
        else:
            idle_seconds = schedule["max_idle_seconds"] + 1  # force nudge if we can't parse
        vlog(verbose, f"idle={int(idle_seconds)}s, max_idle={schedule['max_idle_seconds']}s")
        if idle_seconds > schedule["max_idle_seconds"]:
            try:
                send_wake(goal, worker, int(idle_seconds // 60), dry_run, verbose)
            except BudgetRefused as refused:
                handle_budget_refusal(path, goal, refused, dry_run, verbose)
                return

    # Step 4: hard cycle cap
    enforce_max_cycles(goal, worker, dry_run, verbose)
    save_goal(path, goal, dry_run)


def walk_goal_progress(path: Path, goal: dict, dry_run: bool, verbose: bool) -> None:
    """One tick of a goal that opted into the no-progress cap, worktree
    progress or the worktree gate."""
    pid = goal["id"]
    schedule = goal["schedule"]
    state = goal["state"]
    max_idle = schedule["max_idle_seconds"]
    cap, worktree_progress, gate = progress_features(schedule)
    backoff_base = int(schedule.get("no_progress_backoff_base_seconds") or max_idle)
    workdir = Path(goal.get("workdir", str(Path.home())))

    vlog(verbose, f"=== {pid} ===")

    tree_hash = worktree_tree_hash(workdir) if (gate or worktree_progress) else None
    vlog(verbose, f"worktree tree hash={tree_hash}")
    worker = goal.get("worker_session_title") or goal["worker_session_id"]

    # Step 1: scan for new receipts
    receipts = parse_task_log_tail(workdir / TASK_LOG_NAME)
    new_receipts = [r for r in receipts if newer_than(r.ts, state.get("last_receipt_seen_at"))]

    # Step 2: verifier. With the gate on, a quiet tick skips it while the
    # worktree is unchanged since the last failed verdict. A new receipt, a
    # wake that is actually sent and the no-progress cap always get a fresh
    # verdict, so completion outside the worktree is still seen and a worker
    # is never told NOT YET MET from a stale verdict. done_cmd runs at most
    # once per tick: a verdict from earlier in the same tick was taken on
    # the same tree hash, so it is fresh.
    unchanged_failed = gate and tree_hash is not None and tree_hash == state.get("last_failed_tree_hash")
    verified = False

    def verify_done() -> bool:
        nonlocal verified
        if verified:
            return False
        verified = True
        if check_verifier(goal, tree_hash, verbose) != 0:
            return False
        finish_done(goal, path, worker, dry_run, verbose)
        return True

    if (new_receipts or not unchanged_failed) and verify_done():
        return

    # Step 2b: run budget (tokens, minutes) before any further wake
    over = run_budget_exceeded(goal)
    if over:
        stop_over_budget(path, goal, over, dry_run, verbose)
        return

    if new_receipts and apply_new_receipts(goal, new_receipts, path, worker, dry_run, verbose):
        return

    # Step 3: progress. A new receipt always is; with worktree_progress a
    # change of the worktree hash against the recorded baseline is too
    # (goal.sh records the baseline at creation; otherwise the first hashed
    # tick does).
    baseline = state.get("progress_tree_hash")
    tree_changed = worktree_progress and tree_hash is not None and baseline is not None and tree_hash != baseline
    progressed = bool(new_receipts) or tree_changed
    if tree_hash is not None and (baseline is None or tree_changed):
        state["progress_tree_hash"] = tree_hash

    if progressed:
        state["last_progress_at"] = now_iso()
        state["no_progress_streak"] = 0
        state["next_wake_at"] = None
        state["nudges_sent"] = 0
        record_event(goal, "progress", f"tree={tree_hash[:12]}" if tree_changed else "receipt")

    # Step 4: an idle wake that falls due without progress is an empty
    # turn. With the cap on, wakes wait a doubling window: base, 2*base,
    # 4*base, ... (clamped); with it off a nudge goes out on every idle
    # tick, as before the cap existed.
    idle_seconds = idle_seconds_since_activity(state, max_idle + 1)
    idle_minutes = int(idle_seconds // 60)
    next_wake_at = parse_iso(state.get("next_wake_at")) if cap > 0 else None
    wake_due = not progressed and idle_seconds > max_idle and (
        next_wake_at is None or datetime.now(timezone.utc) >= next_wake_at
    )
    vlog(verbose, f"idle={int(idle_seconds)}s, max_idle={max_idle}s, next_wake_at={state.get('next_wake_at')}")

    if wake_due:
        streak = state.get("no_progress_streak", 0) + 1
        state["no_progress_streak"] = streak
        record_event(goal, "no_progress_turn", f"{streak}/{cap}")
        vlog(verbose, f"empty turn {streak}/{cap}")

        if cap > 0 and streak >= cap:
            # Last look before acting: done_cmd may check state outside
            # the worktree.
            if verify_done():
                return
            end_no_progress(goal, path, worker, streak, idle_minutes, dry_run, verbose)
            return

        if cap > 0:
            state["next_wake_at"] = (
                datetime.now(timezone.utc) + timedelta(seconds=wake_backoff_seconds(backoff_base, streak))
            ).isoformat()

        # With the gate and the cap on, a wake over an unchanged, already
        # failed worktree is skipped once WAKES_BEFORE_SKIP wakes reached the
        # worker on it: another cannot change the verdict, and the empty
        # turn still counts toward the cap. A wake that was not delivered
        # does not count. With the cap off nothing would end the goal, so
        # every due wake goes out.
        if (cap > 0 and unchanged_failed and tree_hash == state.get("last_wake_tree_hash")
                and state.get("wakes_on_tree", 0) >= WAKES_BEFORE_SKIP):
            record_event(goal, "wake_skipped", "worktree unchanged since the last delivered wakes and failed verdict")
            vlog(verbose, "wake skipped: worktree unchanged since the last delivered wakes")
        else:
            if verify_done():
                return
            try:
                delivered = send_wake(goal, worker, idle_minutes, dry_run, verbose)
            except BudgetRefused as refused:
                handle_budget_refusal(path, goal, refused, dry_run, verbose)
                return
            if delivered:
                if tree_hash is not None and tree_hash == state.get("last_wake_tree_hash"):
                    state["wakes_on_tree"] = state.get("wakes_on_tree", 0) + 1
                else:
                    state["last_wake_tree_hash"] = tree_hash
                    state["wakes_on_tree"] = 1

    if not verified:
        record_event(goal, "verifier_skipped", f"worktree unchanged since last failure ({tree_hash[:12]})")
        vlog(verbose, "verifier skipped: worktree unchanged since last failure")

    # Step 5: hard cycle cap
    enforce_max_cycles(goal, worker, dry_run, verbose)
    save_goal(path, goal, dry_run)


def send_wake(goal: dict, worker: str, idle_minutes: int, dry_run: bool, verbose: bool) -> bool:
    """Nudge the worker and escalate once nudge attempts reach the limit.
    Returns whether the nudge was delivered."""
    state = goal["state"]
    schedule = goal["schedule"]
    nudge_text = build_nudge(goal, idle_minutes)
    sent_ok = agent_deck_send(worker, nudge_text, dry_run, verbose)
    # Count attempts, not just successes: N failed nudges = N signals
    # that something is wrong, and the user deserves to be paged.
    state["nudges_sent"] = state.get("nudges_sent", 0) + 1
    if sent_ok:
        record_event(goal, "nudge_sent", nudge_text[:120])
        vlog(verbose, f"nudge #{state['nudges_sent']} sent")
    else:
        record_event(goal, "nudge_failed", "worker unreachable or session not found")
        vlog(verbose, f"nudge #{state['nudges_sent']} send FAILED (worker likely missing)")

    # Escalate if too many nudge attempts
    if state["nudges_sent"] >= schedule["escalate_after_stuck_nudges"]:
        bundle = write_escalation_bundle(goal, idle_minutes)
        state["status"] = "escalated"
        state["escalated_at"] = now_iso()
        record_event(goal, "escalated", str(bundle))
        print(
            f"[manager] ESCALATED: {goal['id']} — bundle at {bundle}",
            file=sys.stderr,
        )
    return sent_ok


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    ap.add_argument("--id", help="walk only this goal id (default: walk all active)")
    ap.add_argument("--dry-run", action="store_true", help="don't change anything")
    ap.add_argument("--verbose", action="store_true", help="verbose per-step logging")
    args = ap.parse_args()

    if not GOALS_DIR.exists():
        print(f"[manager] no goals dir at {GOALS_DIR}", file=sys.stderr)
        return 0

    targets = []
    if args.id:
        candidate = GOALS_DIR / f"{args.id}.json"
        if not candidate.exists():
            print(f"[manager] not found: {candidate}", file=sys.stderr)
            return 1
        targets = [candidate]
    else:
        targets = sorted(GOALS_DIR.glob("*.json"))

    if not targets:
        vlog(args.verbose, "no goal JSONs found")
        return 0

    for path in targets:
        try:
            walk_goal(path, dry_run=args.dry_run, verbose=args.verbose)
        except Exception as e:  # noqa: BLE001  (top-level safety)
            print(f"[manager] error on {path.name}: {e}", file=sys.stderr)

    return 0


if __name__ == "__main__":
    sys.exit(main())
