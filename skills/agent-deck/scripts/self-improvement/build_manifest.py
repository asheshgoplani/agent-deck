#!/usr/bin/env python3
"""Build/refresh analysis-manifest.json by scanning current on-disk state.

Reads:
  - source jsonl files in ~/.claude/projects/.../*-conductor-agent-deck/
  - distilled/<sid>.md
  - reports/<sid>.md
  - run-status.log (to recover analyzer session ids if logged)

Writes:
  - analysis-manifest.json at the conductor root

Safe to re-run any time; it's a pure scan.
"""
from __future__ import annotations

import hashlib
import json
import os
import re
import sys
from datetime import datetime, timezone
from pathlib import Path

HOME = Path(os.path.expanduser("~"))
CONDUCTOR_DIR = HOME / ".agent-deck" / "conductor" / "agent-deck"
ANALYSIS_DIR = CONDUCTOR_DIR / "analysis"
DISTILLED_DIR = ANALYSIS_DIR / "distilled"
REPORTS_DIR = ANALYSIS_DIR / "reports"
SOURCE_DIR = HOME / ".claude" / "projects" / "-home-ashesh-goplani--agent-deck-conductor-agent-deck"
STATUS_LOG = ANALYSIS_DIR / "run-status.log"
MANIFEST = CONDUCTOR_DIR / "analysis-manifest.json"


def sha256_first_mb(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        h.update(f.read(1024 * 1024))
    return h.hexdigest()


def count_lines(path: Path) -> int:
    n = 0
    with path.open("rb") as f:
        for _ in f:
            n += 1
    return n


def first_last_event_ts(path: Path) -> tuple[str | None, str | None]:
    first = last = None
    with path.open("r", encoding="utf-8") as f:
        for line in f:
            try:
                d = json.loads(line)
                ts = d.get("timestamp")
                if ts:
                    if first is None:
                        first = ts
                    last = ts
            except (json.JSONDecodeError, AttributeError):
                continue
    return first, last


def parse_log_for_analyzer_sessions(log_path: Path) -> dict[str, dict]:
    """Extract per-sid timing + status from run-status.log.

    Format we emit:
        [<iso>] [N/M] BEGIN <sid>
        [<iso>] [N/M] OK <sid> (<elapsed>s, <bytes> bytes)
    """
    info: dict[str, dict] = {}
    if not log_path.exists():
        return info
    pat_begin = re.compile(r"\[(.*?)\] \[\d+/\d+\] BEGIN (\S+)")
    pat_done = re.compile(r"\[(.*?)\] \[\d+/\d+\] (OK|EMPTY|FAIL) (\S+) \((\d+)s, (\d+) bytes\)")
    pat_fail = re.compile(r"\[(.*?)\] \[\d+/\d+\] FAIL (\S+)")
    for line in log_path.read_text(encoding="utf-8", errors="replace").splitlines():
        m = pat_begin.search(line)
        if m:
            info.setdefault(m.group(2), {})["begin_ts"] = m.group(1)
            continue
        m = pat_done.search(line)
        if m:
            sid = m.group(3)
            info.setdefault(sid, {}).update({
                "end_ts": m.group(1),
                "outcome": m.group(2),
                "elapsed_s": int(m.group(4)),
                "report_bytes_at_run": int(m.group(5)),
            })
            continue
        m = pat_fail.search(line)
        if m:
            info.setdefault(m.group(2), {}).update({
                "end_ts": m.group(1),
                "outcome": "FAIL",
            })
    return info


def main() -> int:
    if not SOURCE_DIR.exists():
        print(f"Source dir missing: {SOURCE_DIR}", file=sys.stderr)
        return 1

    log_info = parse_log_for_analyzer_sessions(STATUS_LOG)

    rows = []
    # Index sources by short-sid (first 8 chars)
    source_by_sid: dict[str, Path] = {}
    for jsonl in sorted(SOURCE_DIR.glob("*.jsonl")):
        sid = jsonl.stem[:8]
        source_by_sid[sid] = jsonl

    # Index distilled / reports
    distilled_by_sid = {p.stem: p for p in DISTILLED_DIR.glob("*.md")} if DISTILLED_DIR.exists() else {}
    reports_by_sid = {p.stem: p for p in REPORTS_DIR.glob("*.md")} if REPORTS_DIR.exists() else {}

    # Union of all sids we know about
    all_sids = sorted(set(source_by_sid) | set(distilled_by_sid) | set(reports_by_sid))

    for sid in all_sids:
        src = source_by_sid.get(sid)
        dst = distilled_by_sid.get(sid)
        rpt = reports_by_sid.get(sid)
        row = {"session_id_prefix": sid}

        if src:
            row["source_path"] = str(src)
            row["source_filename"] = src.name
            row["source_size_bytes"] = src.stat().st_size
            row["source_line_count"] = count_lines(src)
            row["source_sha256_first_1mb"] = sha256_first_mb(src)
            first, last = first_last_event_ts(src)
            row["source_first_event_at"] = first
            row["source_last_event_at"] = last
        else:
            row["source_path"] = None
            row["source_missing"] = True

        if dst:
            row["distilled_path"] = str(dst.relative_to(CONDUCTOR_DIR))
            row["distilled_size_bytes"] = dst.stat().st_size
            row["distilled_mtime"] = datetime.fromtimestamp(
                dst.stat().st_mtime, tz=timezone.utc
            ).isoformat()
        else:
            row["distilled_path"] = None

        if rpt:
            row["report_path"] = str(rpt.relative_to(CONDUCTOR_DIR))
            row["report_size_bytes"] = rpt.stat().st_size
            row["report_mtime"] = datetime.fromtimestamp(
                rpt.stat().st_mtime, tz=timezone.utc
            ).isoformat()
            row["analyzed"] = True
        else:
            row["report_path"] = None
            row["analyzed"] = False

        # Merge log info if we have any
        if sid in log_info:
            row["last_run"] = log_info[sid]

        rows.append(row)

    manifest = {
        "version": 1,
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "conductor": "agent-deck",
        "source_dir": str(SOURCE_DIR),
        "total_transcripts": len([r for r in rows if r.get("source_path")]),
        "analyzed_count": sum(1 for r in rows if r.get("analyzed")),
        "files": rows,
    }
    MANIFEST.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({
        "manifest_path": str(MANIFEST),
        "total_transcripts": manifest["total_transcripts"],
        "analyzed_count": manifest["analyzed_count"],
        "pending_count": manifest["total_transcripts"] - manifest["analyzed_count"],
    }, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
