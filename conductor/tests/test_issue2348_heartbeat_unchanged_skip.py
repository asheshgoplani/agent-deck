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


def _run_loop(monkeypatch, tmp_path, session_lists):
    """Drive heartbeat_loop for len(session_lists) ticks; return per-tick bytes sent."""
    ticks = iter(session_lists)
    current = {"sessions": []}
    per_tick: list[int] = []
    real_sleep = asyncio.sleep

    async def fake_sleep(_seconds):
        try:
            current["sessions"] = next(ticks)
        except StopIteration:
            raise _StopLoop()
        per_tick.append(0)
        await real_sleep(0)

    def fake_send(_session, message, **_kwargs):
        per_tick[-1] += len(message.encode())
        return True, "[STATUS] All clear.", None

    async def fake_running(_name, _profile):
        return True

    monkeypatch.setattr(bridge, "CONDUCTOR_DIR", tmp_path)
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
