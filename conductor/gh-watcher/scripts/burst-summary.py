#!/usr/bin/env python3
"""Collapse every pending queue row into one digest trigger.

Called by dispatcher.sh when pending depth exceeds high_water. Emits
`[github:burst:<owner>/<repo>] N pending: issue x3 (#12 #13 #14), pr x2 (#15 #16)`,
sends it through send.sh, then marks all summarized rows as sent with the
burst's items_count so the queue drains in one step.
"""
from __future__ import annotations

import re
import sqlite3
import subprocess
import sys
import time
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path

from watcherlib import Config

BURST_MAX = 400  # a burst is the one trigger allowed past the 200 char rule
_TAG = re.compile(r"^\[github:([^:\]]+):[^\]#]*(?:#(\d+))?\]")


def summarize(rows: list[tuple[int, str]]) -> tuple[str, list[int]]:
    groups: dict[str, list[str]] = defaultdict(list)
    for _seq, message in rows:
        m = _TAG.match(message)
        ttype = m.group(1) if m else "other"
        base = ttype.split("_", 1)[0] if ttype not in ("issue_comment", "pr_review") else ttype
        groups[base].append(f"#{m.group(2)}" if m and m.group(2) else "")
    parts = []
    for base, items in sorted(groups.items(), key=lambda kv: -len(kv[1])):
        numbers = sorted({i for i in items if i}, key=lambda s: int(s[1:]))
        part = f"{base} x{len(items)}"
        if numbers:
            part += " (" + " ".join(numbers) + ")"
        parts.append(part)
    return ", ".join(parts), [seq for seq, _ in rows]


def main() -> int:
    cfg = Config()
    con = sqlite3.connect(str(cfg.db), timeout=30)
    rows = con.execute(
        "SELECT seq, message FROM gh_deliver_queue WHERE status='pending' ORDER BY enqueued_at ASC"
    ).fetchall()
    if not rows:
        return 0
    body, seqs = summarize(rows)
    msg = f"[github:burst:{cfg.repo}] {len(rows)} pending: {body}"
    if len(msg) > BURST_MAX:
        msg = msg[: BURST_MAX - 1] + "…"

    send = Path(__file__).resolve().parent / "send.sh"
    rc = subprocess.run([str(send), msg], capture_output=True).returncode
    stamp = datetime.now(timezone.utc).isoformat(timespec="seconds")
    if rc != 0:
        with cfg.log.open("a") as fh:
            fh.write(f"{stamp} [dispatcher] WARN burst send failed depth={len(rows)} (will retry next tick)\n")
        return 1

    placeholders = ",".join("?" * len(seqs))
    con.execute(
        f"UPDATE gh_deliver_queue SET status='sent', sent_at=datetime('now') WHERE seq IN ({placeholders})", seqs
    )
    con.execute(
        "INSERT INTO gh_dispatch_state(k,v) VALUES('last_send_ts',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v",
        (str(int(time.time())),),
    )
    con.commit()
    with cfg.log.open("a") as fh:
        fh.write(f"{stamp} [dispatcher] burst sent items={len(rows)} mode={cfg.mode} high_water={cfg.high_water}\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
