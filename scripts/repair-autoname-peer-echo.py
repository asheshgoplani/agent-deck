#!/usr/bin/env python3
"""Repair session titles polluted by the Claude --name feedback loop.

agent-deck launches every Claude session with `--name <slug(title)>-<id8>` as a
ListAgents/SendMessage address. Claude Code 2.1.237 stopped stamping
`nameSource` on the names it records, so agent-deck's title sync read that
address back as if the user had typed it. Three things followed:

  1. `title` was overwritten with the deck's own handle and `auto_name` cleared,
     so the session could never show Claude's task description again;
  2. the next launch computed the address from the ALREADY suffixed title, so
     each restart appended another `-<id8>` ("cool-dune-03665cc4-03665cc4-03665cc4");
  3. the background capture loop stored that handle as the session's
     `auto_name_description`, which outlives the pane and became the fallback.

The code fix (session.IsClaudePeerNameEcho) stops all three going forward. This
script cleans up rows already damaged. It is idempotent.

Usage:
    python3 repair-autoname-peer-echo.py [--apply] [PATH_TO_state.db]

Without --apply it prints what it would change and touches nothing. Quit the
agent-deck TUI first: it holds sessions in memory and a full-table reconcile
would write the old values straight back.
"""

import argparse
import os
import re
import shutil
import sqlite3
import sys
import time


def id_suffix(instance_id: str) -> str:
    """Mirror of session.peerIDSuffix: first 8 alphanumerics of the id."""
    alnum = "".join(c for c in instance_id.lower() if c.isalnum())
    return alnum[:8] if alnum else "unknown"


def collapse_repeated_suffix(title: str, suffix: str) -> str:
    """'cool-dune-03665cc4-03665cc4-03665cc4' -> 'cool-dune-03665cc4'."""
    pattern = re.compile(r"(?:-" + re.escape(suffix) + r")+$", re.IGNORECASE)
    if not pattern.search(title):
        return title
    return pattern.sub("-" + suffix, title)


def default_db_path() -> str:
    root = os.path.expanduser("~/.agent-deck")
    per_profile = os.path.join(root, "profiles", "default", "state.db")
    return per_profile if os.path.exists(per_profile) else os.path.join(root, "state.db")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("db", nargs="?", default=default_db_path())
    ap.add_argument("--apply", action="store_true", help="write the changes (default: dry run)")
    args = ap.parse_args()

    if not os.path.exists(args.db):
        print("no such database: %s" % args.db, file=sys.stderr)
        return 1

    conn = sqlite3.connect(args.db)
    rows = conn.execute(
        "SELECT id, title, auto_name, title_locked, auto_name_description FROM instances"
    ).fetchall()

    title_fixes = []   # (id, old, new)
    desc_clears = []   # (id, bogus description)
    autoname_restores = []  # (id, title)

    for inst_id, title, auto_name, title_locked, desc in rows:
        suffix = id_suffix(inst_id)
        tail = "-" + suffix

        collapsed = collapse_repeated_suffix(title, suffix)
        if collapsed != title:
            title_fixes.append((inst_id, title, collapsed))
            title = collapsed

        # The stored "task description" is really the deck's own address.
        if desc and desc.lower().endswith(tail):
            desc_clears.append((inst_id, desc))

        # An unlocked machine handle with auto_name cleared is a session the
        # sync stole the flag from; it can never show a task name again because
        # mergeAutoNameFields refuses to resurrect auto_name from a full save.
        if not auto_name and not title_locked and title.lower().endswith(tail):
            autoname_restores.append((inst_id, title))

    print("database: %s" % args.db)
    print("  titles with a repeated id suffix : %d" % len(title_fixes))
    print("  bogus auto-name descriptions     : %d" % len(desc_clears))
    print("  auto_name to restore             : %d" % len(autoname_restores))
    for inst_id, old, new in title_fixes[:10]:
        print("    %s  %r -> %r" % (inst_id[:8], old, new))

    if not args.apply:
        print("\ndry run — re-run with --apply to write these changes")
        return 0

    backup = "%s.bak-peer-echo-%s" % (args.db, time.strftime("%Y%m%d-%H%M%S"))
    shutil.copy2(args.db, backup)
    print("\nbackup: %s" % backup)

    with conn:
        conn.executemany(
            "UPDATE instances SET title = ? WHERE id = ?",
            [(new, inst_id) for inst_id, _, new in title_fixes],
        )
        conn.executemany(
            "UPDATE instances SET auto_name_description = '' WHERE id = ?",
            [(inst_id,) for inst_id, _ in desc_clears],
        )
        conn.executemany(
            "UPDATE instances SET auto_name = 1 WHERE id = ?",
            [(inst_id,) for inst_id, _ in autoname_restores],
        )
    print("applied.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
