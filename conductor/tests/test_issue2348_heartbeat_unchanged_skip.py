#!/usr/bin/env python3
"""Regression tests for issue #2348.

Every delivered heartbeat is a new turn on the conductor's long-lived session,
which re-reads the whole conversation. ``heartbeat_loop`` used to resend the
same waiting/error summary every interval; it now skips a tick whose
waiting/error set equals the last delivered one.
"""

from __future__ import annotations

import asyncio
import subprocess

import pytest

import bridge


class _StopLoop(Exception):
    pass


def _run_loop(monkeypatch, tmp_path, session_lists, inbox_counts=None, inbox_payloads=None, remote_pull=None):
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
    monkeypatch.setattr(bridge, "_pull_remote_talkback", remote_pull or (lambda *_args: False))

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


def test_remote_talkback_record_wakes_bridge(monkeypatch, tmp_path):
    conductor = [{"id": "conductor-id", "title": "conductor-ops", "status": "idle", "group": "ops"}]
    record = '{"source_remote":"build-box","child_session_id":"remote-child"}\n'
    calls = 0

    def pull_remote(_session_id, _profile):
        nonlocal calls
        calls += 1
        if calls >= 2:
            inbox = tmp_path / "inboxes" / "conductor-id.jsonl"
            inbox.write_text(record)
        return False

    per_tick = _run_loop(monkeypatch, tmp_path, [conductor] * 3, remote_pull=pull_remote)
    assert per_tick[0] == 0 and per_tick[1] > 0 and per_tick[2] == 0, per_tick


def test_remote_pull_writes_synthetic_talkback_before_snapshot(monkeypatch, tmp_path):
    inbox = tmp_path / "inboxes" / "conductor-id.jsonl"
    inbox.parent.mkdir()

    def cli(*args, **_kwargs):
        if args[:2] == ("remote", "list"):
            return subprocess.CompletedProcess(args, 0, '[{"name":"build-box"}]', "")
        assert args == ("remote", "drain", "build-box", "--into", "conductor-id", "--json")
        inbox.write_text('{"source_remote":"build-box","child_session_id":"remote-child"}\n')
        return subprocess.CompletedProcess(args, 0, '{"written":1}', "")

    monkeypatch.setattr(bridge, "run_cli", cli)
    monkeypatch.setattr(bridge, "resolve_data_dir", lambda *_markers: tmp_path)
    assert bridge._pull_remote_talkback("conductor-id", "default") is False
    count, digest, error = bridge._conductor_inbox_snapshot(
        [{"id": "conductor-id", "title": "conductor-ops"}], "ops"
    )
    assert count == 1 and digest and error is False
