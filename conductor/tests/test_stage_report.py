"""Tests for conductor/scripts/stage-report.py and stage-event.sh.

The report script is stdlib only, so it is loaded straight from disk by path
(the file name has a dash, so it is not importable by module name).
"""

from __future__ import annotations

import datetime
import importlib.util
import json
import os
import subprocess
import sys
from pathlib import Path

import pytest

SCRIPTS = Path(__file__).resolve().parents[1] / "scripts"
REPORT = SCRIPTS / "stage-report.py"
EVENT_SH = SCRIPTS / "stage-event.sh"


def _load_report():
    spec = importlib.util.spec_from_file_location("stage_report", REPORT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


report = _load_report()


def _line(pr, stage, ts, actor, **extra):
    event = {"pr": pr, "stage": stage, "ts": ts, "actor": actor, "repo": "o/r"}
    event.update(extra)
    return json.dumps(event)


SAMPLE = [
    # PR 1: external contributor arrives, maintainer merges 10h later.
    _line(1, "arrived", "2026-09-01T00:00:00Z", "alice"),
    _line(1, "discovered", "2026-09-01T02:00:00Z", "conductor"),
    _line(1, "merged", "2026-09-01T10:00:00Z", "maint"),
    # PR 2: takes 30h open-to-merge, 20h discovered-to-merge, merged by external.
    _line(2, "arrived", "2026-09-02T00:00:00Z", "bob"),
    _line(2, "discovered", "2026-09-02T10:00:00Z", "conductor"),
    _line(2, "merged", "2026-09-03T06:00:00Z", "bob"),
    # PR 3: duplicate merged events; the first one counts. Out of --until range.
    _line(3, "arrived", "2026-09-10T00:00:00Z", "carol"),
    _line(3, "merged", "2026-09-10T04:00:00Z", "maint"),
    _line(3, "merged", "2026-09-10T09:00:00Z", "maint"),
]


def test_parse_ts_accepts_z_and_offsets():
    z = report.parse_ts("2026-09-01T00:00:00Z")
    off = report.parse_ts("2026-09-01T02:00:00+02:00")
    assert z == off
    assert z.tzinfo is not None


def test_read_events_skips_bad_lines(capsys):
    lines = SAMPLE + ["not json", json.dumps({"pr": 9, "stage": "nope", "ts": "x", "actor": "a"}), ""]
    events = report.read_events(lines)
    assert len(events) == len(SAMPLE)
    err = capsys.readouterr().err
    assert "skipping line" in err


def test_median_and_percentile():
    assert report.median([1.0, 3.0]) == 2.0
    assert report.median([5.0, 1.0, 3.0]) == 3.0
    assert report.percentile([1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0, 9.0, 10.0], 90) == 9.0
    with pytest.raises(ValueError):
        report.median([])


def test_durations_split_by_maintainer_and_first_occurrence():
    per_pr = report.first_by_stage(report.read_events(SAMPLE))
    buckets = report.durations(per_pr, "arrived", "merged", "maint", None, None)
    assert buckets["maintainer"] == [10.0, 4.0]
    assert buckets["external"] == [30.0]
    disc = report.durations(per_pr, "discovered", "merged", "maint", None, None)
    assert disc["maintainer"] == [8.0]
    assert disc["external"] == [20.0]


def test_date_range_filters_on_end_event():
    per_pr = report.first_by_stage(report.read_events(SAMPLE))
    since = report.parse_date_bound("2026-09-01", end=False)
    until = report.parse_date_bound("2026-09-03", end=True)
    buckets = report.durations(per_pr, "arrived", "merged", "maint", since, until)
    assert buckets["maintainer"] == [10.0]
    assert buckets["external"] == [30.0]
    assert until == datetime.datetime(2026, 9, 4, tzinfo=datetime.timezone.utc)


def test_build_report_lists_every_transition_and_summary():
    text = report.build_report(report.read_events(SAMPLE), "maint", None, None)
    for a, b in report.TRANSITIONS:
        assert "%s->%s" % (a, b) in text
    assert "open-to-merge" in text
    assert "discovered-to-merge" in text
    rows = [l for l in text.splitlines() if l.startswith("open-to-merge")]
    assert rows[0].split()[1:3] == ["maintainer", "2"]
    assert rows[1].split()[1:3] == ["external", "1"]
    assert rows[2].split()[1:3] == ["all", "3"]


def test_cli_end_to_end(tmp_path):
    events = tmp_path / "events.jsonl"
    events.write_text("\n".join(SAMPLE) + "\n", encoding="utf-8")
    proc = subprocess.run(
        [sys.executable, str(REPORT), str(events), "--maintainer", "maint", "--since", "2026-09-01", "--until", "2026-09-05"],
        capture_output=True,
        text=True,
        check=True,
    )
    assert "open-to-merge" in proc.stdout
    rows = [l for l in proc.stdout.splitlines() if l.startswith("open-to-merge")]
    assert rows[2].split()[1:3] == ["all", "2"]


def test_stage_event_sh_appends_valid_line(tmp_path):
    events = tmp_path / "nested" / "events.jsonl"
    env = dict(os.environ, STAGE_EVENTS_FILE=str(events), STAGE_EVENTS_ACTOR="tester", STAGE_EVENTS_REPO="o/r")
    subprocess.run([str(EVENT_SH), "42", "merged", 'note with "quotes"'], env=env, check=True)
    subprocess.run([str(EVENT_SH), "42", "closed"], env=env, check=True)
    lines = events.read_text(encoding="utf-8").splitlines()
    assert len(lines) == 2
    first = json.loads(lines[0])
    assert first["pr"] == 42
    assert first["stage"] == "merged"
    assert first["actor"] == "tester"
    assert first["repo"] == "o/r"
    assert first["note"] == 'note with "quotes"'
    report.parse_ts(first["ts"])
    assert "note" not in json.loads(lines[1])


def test_stage_event_sh_rejects_bad_input(tmp_path):
    events = tmp_path / "events.jsonl"
    env = dict(os.environ, STAGE_EVENTS_FILE=str(events))
    bad_stage = subprocess.run([str(EVENT_SH), "1", "shipped"], env=env, capture_output=True, text=True)
    assert bad_stage.returncode == 2
    assert "unknown stage" in bad_stage.stderr
    bad_pr = subprocess.run([str(EVENT_SH), "abc", "merged"], env=env, capture_output=True, text=True)
    assert bad_pr.returncode == 2
    assert not events.exists()
