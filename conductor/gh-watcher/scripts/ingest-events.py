#!/usr/bin/env python3
"""Read a JSON array of GitHub events on stdin; INSERT OR IGNORE into gh_events_seen."""
from __future__ import annotations

import json
import sqlite3
import sys

from watcherlib import Config


def main() -> int:
    cfg = Config()
    raw = sys.stdin.read()
    if not raw.strip():
        return 0
    try:
        events = json.loads(raw)
    except json.JSONDecodeError as exc:
        print(f"ingest-events: bad json: {exc}", file=sys.stderr)
        return 1
    if not isinstance(events, list):
        print(f"ingest-events: expected list, got {type(events).__name__}", file=sys.stderr)
        return 1

    con = sqlite3.connect(str(cfg.db), timeout=30)
    con.execute("PRAGMA journal_mode=WAL;")
    cur = con.cursor()
    inserted = skipped = 0
    for evt in events:
        if not isinstance(evt, dict):
            continue
        event_id = str(evt.get("id", ""))
        if not event_id:
            continue
        if cur.execute("SELECT 1 FROM gh_events_seen WHERE event_id=?", (event_id,)).fetchone():
            skipped += 1
            continue
        cur.execute(
            "INSERT OR IGNORE INTO gh_events_seen "
            "(event_id, created_at, event_type, subtype, delivery_class, payload_json) "
            "VALUES (?, ?, ?, ?, 'unclassified', ?)",
            (
                event_id,
                evt.get("created_at", ""),
                evt.get("type", "UnknownEvent"),
                (evt.get("payload") or {}).get("action", ""),
                json.dumps(evt.get("payload") or {}),
            ),
        )
        inserted += 1
    con.commit()
    con.close()
    if inserted:
        print(f"ingested={inserted} skipped={skipped}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())
