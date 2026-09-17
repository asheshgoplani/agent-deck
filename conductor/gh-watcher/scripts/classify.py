#!/usr/bin/env python3
"""Classify unclassified gh_events_seen rows; route to the delivery queue or the digest bucket.

Triggers follow docs/WATCHER-SETUP.md: `[github:<type>:<owner>/<repo>#<n>] <hint>`,
at most TRIGGER_MAX characters, no payload. The conductor fetches details itself.
"""
from __future__ import annotations

import json
import sqlite3
import sys
from datetime import datetime, timezone
from pathlib import Path

from watcherlib import Config

TRIGGER_MAX = 200
CLASSES = json.loads((Path(__file__).resolve().parent.parent / "classes.json").read_text())

# GitHub event type -> short trigger type. Anything else falls back to the
# lowercased event name without the "Event" suffix.
TYPE_NAMES = {
    "IssuesEvent": "issue",
    "IssueCommentEvent": "issue_comment",
    "PullRequestEvent": "pr",
    "PullRequestReviewEvent": "pr_review",
    "PullRequestReviewCommentEvent": "pr_review_comment",
    "PullRequestReviewThreadEvent": "pr_review_thread",
    "DiscussionEvent": "discussion",
    "DiscussionCommentEvent": "discussion_comment",
    "SecurityAdvisoryEvent": "security_advisory",
    "WorkflowRunEvent": "workflow_run",
    "ReleaseEvent": "release",
    "PushEvent": "push",
}


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


def trigger_type(event_type: str, subtype: str) -> str:
    base = TYPE_NAMES.get(event_type) or event_type.removesuffix("Event").lower()
    return f"{base}_{subtype}" if subtype else base


def item_number(event_type: str, payload: dict) -> int | None:
    for key in ("issue", "pull_request", "discussion"):
        n = (payload.get(key) or {}).get("number")
        if n:
            return int(n)
    n = payload.get("number")
    return int(n) if n else None


def clip(text: str) -> str:
    text = " ".join(text.split())
    return text if len(text) <= TRIGGER_MAX else text[: TRIGGER_MAX - 1] + "…"


def format_message(cfg: Config, event_type: str, subtype: str, payload: dict) -> str:
    ttype = trigger_type(event_type, subtype)
    number = item_number(event_type, payload)
    ident = f"{cfg.repo}#{number}" if number else cfg.repo
    hint = ""
    if event_type in ("IssuesEvent", "PullRequestEvent", "DiscussionEvent"):
        hint = (payload.get("issue") or payload.get("pull_request") or payload.get("discussion") or {}).get("title", "")
    elif event_type == "PullRequestReviewEvent":
        hint = "state=" + str((payload.get("review") or {}).get("state", ""))
    elif event_type == "WorkflowRunEvent":
        run = payload.get("workflow_run") or payload
        hint = f"{run.get('name', '')} on {run.get('head_branch', '')} -> {run.get('conclusion', '')}"
        ident = f"{cfg.repo}@{str(run.get('head_sha', ''))[:7]}"
    elif event_type == "PushEvent":
        ident = f"{cfg.repo}@{str(payload.get('head', ''))[:7]}"
        hint = payload.get("ref", "")
    elif event_type == "ReleaseEvent":
        hint = (payload.get("release") or {}).get("tag_name", "")
    return clip(f"[github:{ttype}:{ident}] {hint}".rstrip())


def parent_key_for(event_type: str, payload: dict) -> str | None:
    pk = CLASSES.get(event_type, {}).get("parent_key")
    if pk == "pr_review":
        n = item_number(event_type, payload)
        return f"pr_review:{n}" if n else None
    if pk == "commit_sha":
        sha = payload.get("sha") or (payload.get("check_run") or {}).get("head_sha") or (payload.get("check_suite") or {}).get("head_sha")
        return f"commit_sha:{sha}" if sha else None
    return None


def conditional_class(event_type: str, payload: dict) -> tuple[str, int]:
    if event_type == "WorkflowRunEvent":
        run = payload.get("workflow_run") or payload
        conclusion = run.get("conclusion") or ""
        if conclusion not in ("success", "") and run.get("head_branch") in ("main", "master"):
            return ("immediate", 0)
        if conclusion == "success":
            return ("paced-digest", 15)
    return ("paced", 10)


def coalesced_message(cfg: Config, parent_key: str, items_count: int) -> str:
    _, _, tail = parent_key.partition(":")
    return clip(f"[github:coalesced:{cfg.repo}#{tail}] {items_count} review events")


def classify_one(cfg: Config, con: sqlite3.Connection, row: tuple) -> str:
    event_id, _created_at, event_type, subtype, payload_str = row
    payload = json.loads(payload_str)
    spec = CLASSES.get(event_type, CLASSES["_default"])
    klass = spec["class"]
    priority = spec.get("priority", 10)
    if klass == "conditional":
        klass, priority = conditional_class(event_type, payload)

    msg = format_message(cfg, event_type, subtype or "", payload)
    pk = parent_key_for(event_type, payload)
    cur = con.cursor()
    cur.execute("UPDATE gh_events_seen SET delivery_class=? WHERE event_id=?", (klass, event_id))

    if klass == "suppressed":
        return "suppressed"

    if klass in ("paced-digest", "daily-digest"):
        digest_class = {"WatchEvent": "stars", "ForkEvent": "forks"}.get(event_type, klass)
        cur.execute(
            "INSERT OR IGNORE INTO gh_digest(class, window_start, event_id, summary) VALUES (?, ?, ?, ?)",
            (digest_class, now_iso()[:10], event_id, msg),
        )
        return f"digest:{digest_class}"

    if klass == "paced-coalesced" and pk:
        existing = cur.execute(
            "SELECT seq, items_count FROM gh_deliver_queue WHERE parent_key=? AND status='pending'", (pk,)
        ).fetchone()
        if existing:
            seq, items = existing
            cur.execute(
                "UPDATE gh_deliver_queue SET items_count=?, message=? WHERE seq=?",
                (items + 1, coalesced_message(cfg, pk, items + 1), seq),
            )
            return f"coalesced:{pk}:{items + 1}"
        cur.execute(
            "INSERT OR IGNORE INTO gh_deliver_queue(enqueued_at, event_id, parent_key, priority, items_count, message) "
            "VALUES (?, ?, ?, ?, 1, ?)",
            (now_iso(), event_id, pk, priority, msg),
        )
        return f"coalesced-new:{pk}"

    cur.execute(
        "INSERT OR IGNORE INTO gh_deliver_queue(enqueued_at, event_id, parent_key, priority, items_count, message) "
        "VALUES (?, ?, NULL, ?, 1, ?)",
        (now_iso(), event_id, priority, msg),
    )
    return f"queued:{klass}:p{priority}"


def main() -> int:
    cfg = Config()
    con = sqlite3.connect(str(cfg.db), timeout=30)
    con.execute("PRAGMA journal_mode=WAL;")
    rows = con.execute(
        "SELECT event_id, created_at, event_type, subtype, payload_json "
        "FROM gh_events_seen WHERE delivery_class='unclassified' ORDER BY created_at ASC"
    ).fetchall()
    for row in rows:
        try:
            result = classify_one(cfg, con, row)
            print(f"{row[0]} {row[2]} -> {result}", file=sys.stderr)
        except Exception as exc:  # noqa: BLE001
            print(f"WARN classify {row[0]}: {exc}", file=sys.stderr)
    con.commit()
    con.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
