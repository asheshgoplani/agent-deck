#!/usr/bin/env python3
"""The heartbeat finds a renamed conductor's inbox by its stable name.

`list --json` carries ``conductor_name`` on a conductor row. The bridge used
to match the conductor by the exact title ``conductor-<name>``, so a renamed
or decorated conductor counted an inbox of 0 and its pending records were
never delivered. The title stays the fallback for rows without the field.
"""

from __future__ import annotations

import json

import bridge

CONDUCTOR_ID = "conductor-id"


def _write_inbox(monkeypatch, tmp_path, session_id=CONDUCTOR_ID, records=2):
    monkeypatch.setattr(bridge, "resolve_data_dir", lambda *_markers: tmp_path)
    inbox = tmp_path / "inboxes" / f"{session_id}.jsonl"
    inbox.parent.mkdir(parents=True, exist_ok=True)
    inbox.write_text("".join(json.dumps({"child_session_id": f"c{i}"}) + "\n" for i in range(records)))


def test_renamed_conductor_counts_its_inbox(monkeypatch, tmp_path):
    _write_inbox(monkeypatch, tmp_path)
    sessions = [{"title": "ops (renamed)", "id": CONDUCTOR_ID, "conductor_name": "ops"}]
    count, digest, err = bridge._conductor_inbox_snapshot(sessions, "ops")
    assert (count, err) == (2, False)
    assert digest


def test_title_fallback_without_the_field(monkeypatch, tmp_path):
    _write_inbox(monkeypatch, tmp_path)
    sessions = [{"title": bridge.conductor_session_title("ops"), "id": CONDUCTOR_ID}]
    assert bridge._conductor_inbox_snapshot(sessions, "ops")[0] == 2


def test_stable_name_wins_over_a_title_match(monkeypatch, tmp_path):
    _write_inbox(monkeypatch, tmp_path)
    sessions = [
        {"title": bridge.conductor_session_title("ops"), "id": "worker-id"},
        {"title": "ops (renamed)", "id": CONDUCTOR_ID, "conductor_name": "ops"},
    ]
    assert bridge._conductor_inbox_snapshot(sessions, "ops")[0] == 2


def test_row_named_as_another_conductor_never_matches_by_title(monkeypatch, tmp_path):
    _write_inbox(monkeypatch, tmp_path)
    sessions = [{"title": bridge.conductor_session_title("ops"), "id": CONDUCTOR_ID, "conductor_name": "dev"}]
    assert bridge._conductor_inbox_snapshot(sessions, "ops") == (0, "", False)
