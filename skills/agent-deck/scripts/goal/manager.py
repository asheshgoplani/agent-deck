#!/usr/bin/env python3
"""Goal manager — runs the verifier, reads receipts, nudges the worker.

A one-shot script (intentionally — no daemon, no signal handling). Run
periodically via cron or by hand. Each invocation walks every active
goal JSON in ~/.agent-deck/goals/ and advances its state by one tick:

    1. Run the goal's done_cmd. If exit 0 → mark done, stop worker.
    2. Else read task-log.md tail; look for receipts newer than what we've
       already seen. If found → reset nudge counter, record cycle.
    3. Else, if idle for max_idle seconds → send a context-rich nudge to
       the worker via `agent-deck session send`.
    4. If nudges_sent >= escalate_after → write a stuck bundle, log loudly.
    5. If cycles_completed >= max_cycles → finalize as failed.

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
import json
import os
import re
import shlex
import subprocess
import sys
import time
from dataclasses import dataclass
from datetime import datetime, timezone
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


def run_done_cmd(done_cmd: str, timeout_s: int = 30) -> tuple[int, str]:
    """Run the external verifier. Returns (returncode, combined_output)."""
    try:
        r = subprocess.run(
            done_cmd, shell=True, capture_output=True, text=True, timeout=timeout_s
        )
        out = (r.stdout or "") + (r.stderr or "")
        return r.returncode, out.strip()[:500]
    except subprocess.TimeoutExpired:
        return 124, "verifier timed out"
    except Exception as e:  # noqa: BLE001  (defensive at boundary)
        return 1, f"verifier raised: {e}"


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
    cmd = ["agent-deck", "session", "send", session_id_or_title, message, "--no-wait", "-q"]
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


def write_escalation_bundle(goal: dict, idle_minutes: int) -> Path:
    ESCALATIONS_DIR.mkdir(parents=True, exist_ok=True)
    ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H-%M-%S")
    out = ESCALATIONS_DIR / f"{goal['id']}-{ts}.md"
    last = goal["state"].get("last_receipt_text") or "(none)"
    recent = goal.get("history", [])[-6:]
    recent_str = "\n".join(
        f"- {e['ts']} {e['event']}: {e['detail'][:120]}" for e in recent
    )
    body = (
        f"# Goal escalation: {goal['goal']}\n"
        f"\n"
        f"Stuck after {goal['state']['nudges_sent']} nudges, no receipt in {idle_minutes} minutes.\n"
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


def walk_goal(path: Path, dry_run: bool, verbose: bool) -> None:
    goal = load_goal(path)
    if goal.get("state", {}).get("status") != "active":
        vlog(verbose, f"skip {path.name}: status={goal.get('state', {}).get('status')}")
        return

    pid = goal["id"]
    schedule = goal["schedule"]
    state = goal["state"]

    vlog(verbose, f"=== {pid} ===")

    # Step 1: verifier
    rc, out = run_done_cmd(goal["done_cmd"], timeout_s=30)
    state["last_verified_at"] = now_iso()
    record_event(goal, "verifier_check", f"rc={rc}")
    vlog(verbose, f"verifier rc={rc}")

    if rc == 0:
        finalize(goal, "done", "verifier passed")
        agent_deck_stop(goal["worker_session_title"] or goal["worker_session_id"], dry_run, verbose)
        write_history_artifact(goal)
        print(f"[manager] DONE: {pid} ({goal['goal']})", file=sys.stderr)
        save_goal(path, goal, dry_run)
        return

    # Step 2: scan for new receipts
    workdir = Path(goal.get("workdir", str(Path.home())))
    task_log = workdir / "task-log.md"
    receipts = parse_task_log_tail(task_log)
    last_seen = state.get("last_receipt_seen_at")
    new_receipts = [r for r in receipts if newer_than(r.ts, last_seen)]

    if new_receipts:
        newest = new_receipts[-1]
        state["last_receipt_seen_at"] = newest.ts
        state["last_receipt_text"] = newest.body[:1000]
        state["cycles_completed"] = state.get("cycles_completed", 0) + 1
        state["nudges_sent"] = 0
        record_event(goal, "receipt", newest.ts)
        vlog(verbose, f"new receipt at {newest.ts}; cycles={state['cycles_completed']}")

        # Step 2a: detect STUCK marker. If the worker wrote STUCK: in its
        # most recent receipt, it's telling us it can't proceed. Escalate
        # immediately with the worker's own reason rather than continuing
        # to nudge it. This is the contract: STUCK is the worker's honest
        # signal that it needs help.
        if "STUCK:" in newest.body or "STUCK :" in newest.body:
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
                f"[manager] WORKER STUCK: {pid} — {stuck_line.strip()[:120]}",
                file=sys.stderr,
            )
            print(f"[manager] bundle at {bundle}", file=sys.stderr)
            agent_deck_stop(goal.get("worker_session_title") or goal["worker_session_id"], dry_run, verbose)
            save_goal(path, goal, dry_run)
            return
    else:
        # Step 3: nudge if idle
        last_activity_iso = state.get("last_receipt_seen_at") or state.get("created_at")
        if last_activity_iso:
            try:
                last_activity = datetime.fromisoformat(last_activity_iso.replace("Z", "+00:00"))
                idle = datetime.now(timezone.utc) - last_activity
                idle_seconds = idle.total_seconds()
            except Exception:  # noqa: BLE001
                idle_seconds = schedule["max_idle_seconds"] + 1  # force nudge if we can't parse
        else:
            idle_seconds = schedule["max_idle_seconds"] + 1

        vlog(verbose, f"idle={int(idle_seconds)}s, max_idle={schedule['max_idle_seconds']}s")

        if idle_seconds > schedule["max_idle_seconds"]:
            target = goal.get("worker_session_title") or goal["worker_session_id"]
            nudge_text = build_nudge(goal, int(idle_seconds // 60))
            sent_ok = agent_deck_send(target, nudge_text, dry_run, verbose)
            # Count attempts, not just successes: N failed nudges = N signals
            # that something is wrong, and the user deserves to be paged.
            state["nudges_sent"] = state.get("nudges_sent", 0) + 1
            if sent_ok:
                record_event(goal, "nudge_sent", nudge_text[:120])
                vlog(verbose, f"nudge #{state['nudges_sent']} sent")
            else:
                record_event(goal, "nudge_failed", "worker unreachable or session not found")
                vlog(verbose, f"nudge #{state['nudges_sent']} send FAILED (worker likely missing)")

            # Step 4: escalate if too many nudge attempts
            if state["nudges_sent"] >= schedule["escalate_after_stuck_nudges"]:
                bundle = write_escalation_bundle(goal, int(idle_seconds // 60))
                state["status"] = "escalated"
                state["escalated_at"] = now_iso()
                record_event(goal, "escalated", str(bundle))
                print(
                    f"[manager] ESCALATED: {pid} — bundle at {bundle}",
                    file=sys.stderr,
                )

    # Step 5: hard cycle cap
    if state.get("cycles_completed", 0) >= schedule["max_cycles"]:
        finalize(goal, "failed", "max_cycles_exceeded")
        agent_deck_stop(goal["worker_session_title"] or goal["worker_session_id"], dry_run, verbose)
        write_history_artifact(goal)
        print(f"[manager] FAILED (max cycles): {pid}", file=sys.stderr)

    save_goal(path, goal, dry_run)


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
