#!/usr/bin/env python3
"""The heartbeat's pending count skips records the conductor already read.

Follow-up to #2721: a record the conductor read the result of with a plain
``session output`` after the turn ended is withdrawn at the next drain, so the
Go heartbeat (``InboxSnapshot``) does not count it. The bridge heartbeat must
count the same way, from the same files.
"""

from __future__ import annotations

import json

import bridge

CONDUCTOR_ID = "conductor-id"


def _setup(monkeypatch, tmp_path, records, reads):
    monkeypatch.setattr(bridge, "resolve_data_dir", lambda *_markers: tmp_path)
    inbox = tmp_path / "inboxes" / f"{CONDUCTOR_ID}.jsonl"
    inbox.parent.mkdir(parents=True, exist_ok=True)
    inbox.write_text("".join(json.dumps(r) + "\n" for r in records))
    if reads is not None:
        path = tmp_path / "runtime" / "result-reads" / f"{CONDUCTOR_ID}.json"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(reads))
    return [{"title": bridge.conductor_session_title("ops"), "id": CONDUCTOR_ID}]


def _record(child, uuid, stamp, **extra):
    rec = {"child_session_id": child, "to_status": "waiting", "turn_uuid": uuid, "timestamp": stamp}
    rec.update(extra)
    return rec


# 2026-10-18T10:00:00Z in Unix ms.
WHOLE = "2026-10-18T10:00:00Z"
WHOLE_MS = 1792317600000
SUB = "2026-10-18T10:00:00.250000000+00:00"


def test_record_read_after_the_turn_is_not_pending(monkeypatch, tmp_path):
    records = [_record("c-read", "u-read", SUB), _record("c-unread", "u-unread", SUB)]
    reads = {"c-read": {"turns": ["u-read"], "at": WHOLE_MS + 5000, "read_at": {"u-read": WHOLE_MS + 5000}}}
    sessions = _setup(monkeypatch, tmp_path, records, reads)
    count, digest, err = bridge._conductor_inbox_snapshot(sessions, "ops")
    assert (count, err) == (1, False)
    assert digest

    reads["c-unread"] = {"turns": ["u-unread"], "at": WHOLE_MS + 6000, "read_at": {"u-unread": WHOLE_MS + 6000}}
    sessions = _setup(monkeypatch, tmp_path, records, reads)
    assert bridge._conductor_inbox_snapshot(sessions, "ops") == (0, "", False)


def test_records_that_are_never_withdrawn_still_count(monkeypatch, tmp_path):
    read_at = {"u": WHOLE_MS + 400}  # same second as a whole-second stamp
    records = [
        _record("c-same-second", "u", WHOLE),                      # mid-turn peek in the stamp's second
        _record("c-interim", "u", SUB, turn_text_interim=True),   # final text not written yet
        _record("c-question", "u", SUB, question=True),
        _record("c-remote", "u", SUB, source_remote="box"),
        _record("c-digest", "u", SUB, overflow_turns=3),
        _record("c-error", "u", SUB, to_status="error"),
        _record("c-no-time", "u", "0001-01-01T00:00:00Z"),
        _record("c-legacy", "u", SUB),                            # read file without read_at
    ]
    reads = {r["child_session_id"]: {"turns": ["u"], "at": WHOLE_MS + 60000, "read_at": dict(read_at)} for r in records}
    reads["c-interim"]["read_at"] = {"u": WHOLE_MS + 60000}
    reads["c-legacy"] = {"turns": ["u"], "at": WHOLE_MS + 60000}
    sessions = _setup(monkeypatch, tmp_path, records, reads)
    count, _digest, err = bridge._conductor_inbox_snapshot(sessions, "ops")
    assert (count, err) == (len(records), False)


def test_whole_second_stamp_needs_a_read_a_second_later(monkeypatch, tmp_path):
    records = [_record("c", "u", WHOLE)]
    reads = {"c": {"turns": ["u"], "at": WHOLE_MS + 1000, "read_at": {"u": WHOLE_MS + 1000}}}
    sessions = _setup(monkeypatch, tmp_path, records, reads)
    assert bridge._conductor_inbox_snapshot(sessions, "ops")[0] == 0


def test_no_read_file_counts_every_line(monkeypatch, tmp_path):
    sessions = _setup(monkeypatch, tmp_path, [_record("c", "u", SUB), {"not": "a record"}], None)
    (tmp_path / "inboxes" / f"{CONDUCTOR_ID}.jsonl").write_text('{"child_session_id": "c"}\nnot json\n')
    assert bridge._conductor_inbox_snapshot(sessions, "ops")[0] == 2
