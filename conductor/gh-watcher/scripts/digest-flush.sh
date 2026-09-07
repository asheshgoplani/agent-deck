#!/usr/bin/env bash
# Flush gh_digest into one low priority queue row per class, then clear the bucket.
# tick.sh calls this once per day at dispatcher.digest_flush_local_time.
set -euo pipefail
BIN="$(cd "$(dirname "$0")" && pwd)"
eval "$(python3 "$BIN/watcherlib.py" --shell)"
python3 - "$GHW_DB" "$GHW_REPO" "$GHW_LOG" <<'PY'
import sqlite3, sys
from datetime import datetime, timezone
db, repo, log = sys.argv[1:4]
con = sqlite3.connect(db, timeout=30)
stamp = datetime.now(timezone.utc).isoformat(timespec="seconds")
for (cls,) in con.execute("SELECT DISTINCT class FROM gh_digest").fetchall():
    items = con.execute("SELECT summary FROM gh_digest WHERE class=?", (cls,)).fetchall()
    if not items:
        continue
    msg = f"[github:digest:{repo}] {cls} x{len(items)} in last 24h"
    con.execute(
        "INSERT OR IGNORE INTO gh_deliver_queue(enqueued_at, event_id, parent_key, priority, items_count, message) "
        "VALUES (datetime('now'), ?, NULL, 15, ?, ?)",
        (f"digest-{cls}-{stamp[:10]}", len(items), msg),
    )
    con.execute("DELETE FROM gh_digest WHERE class=?", (cls,))
    with open(log, "a") as fh:
        fh.write(f"{stamp} [digest-flush] flushed class={cls} count={len(items)}\n")
con.commit()
PY
