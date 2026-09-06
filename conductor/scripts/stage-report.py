#!/usr/bin/env python3
"""Summarize per-PR stage events (see conductor/STAGE-EVENTS.md).

Reads a JSONL file of stage events and prints median and p90 hours for every
consecutive stage transition, split by whether the later event's actor is the
maintainer, plus open-to-merge (arrived to merged) and discovered-to-merge.

Stdlib only. Compatible with Python 3.8+.
"""

from __future__ import annotations

import argparse
import datetime
import json
import sys
from typing import Dict, Iterable, List, Optional, Tuple

STAGES = [
    "arrived",
    "discovered",
    "triaged",
    "review_started",
    "verdict",
    "ci_green",
    "merged",
    "closed",
]

# Transitions reported one by one, in pipeline order.
TRANSITIONS: List[Tuple[str, str]] = [
    ("arrived", "discovered"),
    ("discovered", "triaged"),
    ("triaged", "review_started"),
    ("review_started", "verdict"),
    ("verdict", "ci_green"),
    ("ci_green", "merged"),
]

SUMMARIES: List[Tuple[str, str, str]] = [
    ("open-to-merge", "arrived", "merged"),
    ("discovered-to-merge", "discovered", "merged"),
]


def parse_ts(value: str) -> datetime.datetime:
    """Parse an RFC3339 timestamp; a trailing Z means UTC."""
    text = value.strip()
    if text.endswith("Z") or text.endswith("z"):
        text = text[:-1] + "+00:00"
    parsed = datetime.datetime.fromisoformat(text)
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=datetime.timezone.utc)
    return parsed.astimezone(datetime.timezone.utc)


def parse_date_bound(value: Optional[str], end: bool) -> Optional[datetime.datetime]:
    """Accept YYYY-MM-DD or a full timestamp. A bare date for --until is inclusive."""
    if not value:
        return None
    if len(value) == 10:
        day = datetime.date.fromisoformat(value)
        if end:
            day = day + datetime.timedelta(days=1)
        return datetime.datetime.combine(
            day, datetime.time.min, tzinfo=datetime.timezone.utc
        )
    return parse_ts(value)


def read_events(lines: Iterable[str]) -> List[dict]:
    events = []
    for lineno, raw in enumerate(lines, 1):
        raw = raw.strip()
        if not raw:
            continue
        try:
            event = json.loads(raw)
        except json.JSONDecodeError as exc:
            print("skipping line %d: %s" % (lineno, exc), file=sys.stderr)
            continue
        missing = [k for k in ("pr", "stage", "ts", "actor") if k not in event]
        if missing:
            print(
                "skipping line %d: missing %s" % (lineno, ", ".join(missing)),
                file=sys.stderr,
            )
            continue
        if event["stage"] not in STAGES:
            print(
                "skipping line %d: unknown stage %r" % (lineno, event["stage"]),
                file=sys.stderr,
            )
            continue
        try:
            event["_ts"] = parse_ts(str(event["ts"]))
        except ValueError as exc:
            print("skipping line %d: bad ts (%s)" % (lineno, exc), file=sys.stderr)
            continue
        events.append(event)
    return events


def first_by_stage(events: List[dict]) -> Dict[int, Dict[str, dict]]:
    """Map pr -> stage -> earliest event for that stage."""
    per_pr: Dict[int, Dict[str, dict]] = {}
    for event in sorted(events, key=lambda e: e["_ts"]):
        stages = per_pr.setdefault(int(event["pr"]), {})
        stages.setdefault(event["stage"], event)
    return per_pr


def in_range(
    ts: datetime.datetime,
    since: Optional[datetime.datetime],
    until: Optional[datetime.datetime],
) -> bool:
    if since is not None and ts < since:
        return False
    if until is not None and ts >= until:
        return False
    return True


def durations(
    per_pr: Dict[int, Dict[str, dict]],
    start: str,
    end: str,
    maintainer: Optional[str],
    since: Optional[datetime.datetime],
    until: Optional[datetime.datetime],
) -> Dict[str, List[float]]:
    """Hours from first `start` to first `end` per PR, bucketed by actor group.

    A transition is in range when its end event falls inside [since, until).
    """
    buckets: Dict[str, List[float]] = {"maintainer": [], "external": []}
    for stages in per_pr.values():
        if start not in stages or end not in stages:
            continue
        a, b = stages[start], stages[end]
        if not in_range(b["_ts"], since, until):
            continue
        hours = (b["_ts"] - a["_ts"]).total_seconds() / 3600.0
        if hours < 0:
            continue
        group = "maintainer" if maintainer and b["actor"] == maintainer else "external"
        buckets[group].append(hours)
    return buckets


def percentile(values: List[float], pct: float) -> float:
    """Nearest-rank percentile; median is pct=50."""
    if not values:
        raise ValueError("no values")
    ordered = sorted(values)
    if pct <= 0:
        return ordered[0]
    rank = int(-(-pct * len(ordered) // 100))  # ceil(pct/100 * n)
    return ordered[min(max(rank, 1), len(ordered)) - 1]


def median(values: List[float]) -> float:
    ordered = sorted(values)
    n = len(ordered)
    if n == 0:
        raise ValueError("no values")
    mid = n // 2
    if n % 2:
        return ordered[mid]
    return (ordered[mid - 1] + ordered[mid]) / 2.0


def format_row(label: str, group: str, values: List[float]) -> str:
    if not values:
        return "%-24s %-11s %5s %9s %9s" % (label, group, 0, "-", "-")
    return "%-24s %-11s %5d %9.1f %9.1f" % (
        label,
        group,
        len(values),
        median(values),
        percentile(values, 90),
    )


def build_report(
    events: List[dict],
    maintainer: Optional[str],
    since: Optional[datetime.datetime],
    until: Optional[datetime.datetime],
) -> str:
    per_pr = first_by_stage(events)
    lines = []
    lines.append("%-24s %-11s %5s %9s %9s" % ("transition", "actor", "n", "median_h", "p90_h"))
    for start, end in TRANSITIONS:
        label = "%s->%s" % (start, end)
        buckets = durations(per_pr, start, end, maintainer, since, until)
        for group in ("maintainer", "external"):
            lines.append(format_row(label, group, buckets[group]))
    lines.append("")
    for label, start, end in SUMMARIES:
        buckets = durations(per_pr, start, end, maintainer, since, until)
        for group in ("maintainer", "external"):
            lines.append(format_row(label, group, buckets[group]))
        lines.append(format_row(label, "all", buckets["maintainer"] + buckets["external"]))
    return "\n".join(lines)


def main(argv: Optional[List[str]] = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("events_file", help="path to stage-events.jsonl")
    parser.add_argument("--maintainer", help="GitHub login counted as the maintainer")
    parser.add_argument("--since", help="only transitions ending on or after this date (YYYY-MM-DD or RFC3339)")
    parser.add_argument("--until", help="only transitions ending before the end of this date (YYYY-MM-DD or RFC3339)")
    args = parser.parse_args(argv)

    try:
        with open(args.events_file, "r", encoding="utf-8") as fh:
            events = read_events(fh)
    except OSError as exc:
        print("error: %s" % exc, file=sys.stderr)
        return 1

    since = parse_date_bound(args.since, end=False)
    until = parse_date_bound(args.until, end=True)
    print(build_report(events, args.maintainer, since, until))
    return 0


if __name__ == "__main__":
    sys.exit(main())
