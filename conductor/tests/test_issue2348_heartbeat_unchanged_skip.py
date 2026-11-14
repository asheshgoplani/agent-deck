#!/usr/bin/env python3
"""Regression tests for issue #2348.

Every delivered heartbeat is a new turn on the conductor's long-lived session,
which re-reads the whole conversation. ``heartbeat_loop`` used to resend the
same waiting/error summary every interval; it now skips a tick whose
waiting/error set equals the last delivered one.
"""

from __future__ import annotations

import asyncio

import pytest

import bridge


class _StopLoop(Exception):
    pass


def _run_loop(monkeypatch, tmp_path, session_lists, inbox_counts=None, inbox_payloads=None, sent=None):
    """Drive heartbeat_loop for len(session_lists) ticks; return per-tick bytes sent."""
    ticks = iter(session_lists)
    inbox_ticks = iter(inbox_counts or [0] * len(session_lists))
    payload_ticks = iter(inbox_payloads or [None] * len(session_lists))
    current = {"sessions": []}
    per_tick: list[int] = []
    real_sleep = asyncio.sleep

    async def fake_sleep(_seconds):
        try:
            current["sessions"] = next(ticks)
            pending = next(inbox_ticks)
            payload = next(payload_ticks)
        except StopIteration:
            raise _StopLoop()
        inbox = tmp_path / "inboxes" / "conductor-id.jsonl"
        inbox.parent.mkdir(exist_ok=True)
        if payload == "__unreadable__":
            inbox.mkdir(exist_ok=True)
        else:
            inbox.write_text(payload if payload is not None else "{}\n" * pending)
        per_tick.append(0)
        await real_sleep(0)

    def fake_send(_session, message, **_kwargs):
        per_tick[-1] += len(message.encode())
        if sent is not None:
            sent.append(message)
        return True, "[STATUS] All clear.", None

    async def fake_running(_name, _profile):
        return True

    monkeypatch.setattr(bridge, "CONDUCTOR_DIR", tmp_path)
    monkeypatch.setattr(bridge, "resolve_data_dir", lambda *_markers: tmp_path)
    monkeypatch.setattr(bridge, "_os_heartbeat_daemon_installed", lambda: False)
    monkeypatch.setattr(bridge.asyncio, "sleep", fake_sleep)
    monkeypatch.setattr(bridge, "discover_conductors", lambda: [{"name": "ops", "profile": "default"}])
    monkeypatch.setattr(bridge, "get_sessions_list", lambda _p: current["sessions"])
    monkeypatch.setattr(bridge, "invoke_hook", lambda *_a, **_k: None)
    monkeypatch.setattr(bridge, "ensure_conductor_running", fake_running)
    monkeypatch.setattr(bridge, "get_session_status", lambda *_a, **_k: "idle")
    monkeypatch.setattr(bridge, "hook_driven_interactive", lambda *_a, **_k: (False, True))
    monkeypatch.setattr(bridge, "capture_pane", lambda *_a, **_k: "")
    monkeypatch.setattr(bridge, "send_to_conductor", fake_send)

    config = {"heartbeat_interval": 1, "telegram": {"configured": False}}
    with pytest.raises(_StopLoop):
        asyncio.run(bridge.heartbeat_loop(config))
    return per_tick


WAITING = [
    {"title": "api-fix", "status": "waiting", "group": "ops", "path": "/src/api"},
    {"title": "frontend", "status": "running", "group": "ops", "path": "/src/app"},
]


def test_unchanged_ticks_send_nothing_after_first(monkeypatch, tmp_path):
    per_tick = _run_loop(monkeypatch, tmp_path, [WAITING] * 20)
    assert per_tick[0] > 0
    assert per_tick[1:] == [0] * 19, per_tick


def test_running_churn_is_not_a_change(monkeypatch, tmp_path):
    churned = [dict(s) for s in WAITING]
    churned[1]["status"] = "idle"
    per_tick = _run_loop(monkeypatch, tmp_path, [WAITING, churned, WAITING])
    assert per_tick[0] > 0 and per_tick[1:] == [0, 0], per_tick


def test_new_waiting_session_and_recurrence_are_delivered(monkeypatch, tmp_path):
    more = WAITING + [{"title": "db", "status": "error", "group": "ops", "path": "/src/db"}]
    per_tick = _run_loop(monkeypatch, tmp_path, [WAITING, more, more, [], WAITING])
    assert per_tick[0] > 0
    assert per_tick[1] > 0  # new error session
    assert per_tick[2] == 0  # unchanged
    assert per_tick[3] == 0  # nothing actionable
    assert per_tick[4] > 0  # came back after being resolved


def test_inbox_only_transition_wakes_once_and_recurrence_wakes_again(monkeypatch, tmp_path):
    conductor = [{"id": "conductor-id", "title": "conductor-ops", "status": "idle", "group": "ops"}]
    per_tick = _run_loop(monkeypatch, tmp_path, [conductor] * 5, [0, 1, 1, 0, 1])
    assert per_tick[0] == 0
    assert per_tick[1] > 0
    assert per_tick[2:4] == [0, 0]
    assert per_tick[4] > 0


def test_replaced_inbox_record_at_same_count_wakes(monkeypatch, tmp_path):
    conductor = [{"id": "conductor-id", "title": "conductor-ops", "status": "idle", "group": "ops"}]
    per_tick = _run_loop(
        monkeypatch, tmp_path, [conductor] * 3, [1, 1, 1],
        ['{"first":true}\n', '{"first":true}\n', '{"second":true}\n'],
    )
    assert per_tick[0] > 0 and per_tick[1] == 0 and per_tick[2] > 0, per_tick


def test_unreadable_inbox_does_not_silence_heartbeat(monkeypatch, tmp_path):
    conductor = [{"id": "conductor-id", "title": "conductor-ops", "status": "idle", "group": "ops"}]
    per_tick = _run_loop(monkeypatch, tmp_path, [conductor], [0], ["__unreadable__"])
    assert per_tick[0] > 0, per_tick


ARCHIVED_WAITING = {"title": "old-worker", "status": "waiting", "group": "ops", "path": "/src/old", "archived": True}
ARCHIVED_ERROR = {"title": "old-crash", "status": "error", "group": "ops/sub", "path": "/src/crash", "archived": True}
ARCHIVED_STOPPED = {"title": "old-done", "status": "stopped", "group": "ops", "path": "/src/done", "archived": True}


def test_archived_children_do_not_wake(monkeypatch, tmp_path):
    per_tick = _run_loop(monkeypatch, tmp_path, [[ARCHIVED_WAITING], [ARCHIVED_ERROR], [ARCHIVED_WAITING, ARCHIVED_ERROR]])
    assert per_tick == [0, 0, 0], per_tick


def test_archiving_stops_wakes_and_unarchiving_restores_them(monkeypatch, tmp_path):
    live = {**ARCHIVED_WAITING, "archived": False}
    per_tick = _run_loop(monkeypatch, tmp_path, [[live], [ARCHIVED_WAITING], [live]])
    assert per_tick[0] > 0 and per_tick[1] == 0 and per_tick[2] > 0, per_tick


def test_archived_children_are_left_out_of_counts_and_details(monkeypatch, tmp_path):
    sent: list[str] = []
    rows = WAITING + [ARCHIVED_WAITING, ARCHIVED_ERROR, ARCHIVED_STOPPED]
    _run_loop(monkeypatch, tmp_path, [rows], sent=sent)
    assert len(sent) == 1, sent
    assert "1 waiting, 1 running, 0 idle, 0 error, 0 stopped." in sent[0]
    assert "api-fix" in sent[0]
    assert "old-" not in sent[0]


def test_inbox_still_wakes_when_only_archived_children_remain(monkeypatch, tmp_path):
    conductor = {"id": "conductor-id", "title": "conductor-ops", "status": "idle", "group": "ops"}
    sent: list[str] = []
    per_tick = _run_loop(monkeypatch, tmp_path, [[conductor, ARCHIVED_STOPPED]], [1], sent=sent)
    assert per_tick[0] > 0, per_tick
    assert "Inbox: 1 pending" in sent[0] and "0 stopped" in sent[0]


def test_archived_conductor_row_keeps_its_inbox_lookup(monkeypatch, tmp_path):
    # The child filter must not reach the full list the inbox lookup reads.
    conductor = {"id": "conductor-id", "title": "conductor-ops", "status": "idle", "group": "ops", "archived": True}
    per_tick = _run_loop(monkeypatch, tmp_path, [[conductor]], [1])
    assert per_tick[0] > 0, per_tick
