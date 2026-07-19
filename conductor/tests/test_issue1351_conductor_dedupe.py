"""Regression tests for issue #1351.

Restarting the conductor bridge must not register a duplicate conductor row
when a same-title conductor already exists in the requested profile.
"""

from __future__ import annotations

import asyncio
import subprocess
import sys
import types
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent.parent))
try:
    import toml  # noqa: F401
except ModuleNotFoundError:
    sys.modules["toml"] = types.SimpleNamespace(load=lambda *_args, **_kwargs: {})

from bridge import CONDUCTOR_DIR, ensure_conductor_running  # noqa: E402


async def _no_sleep(_seconds: float) -> None:
    return None


def _completed(returncode: int = 0, stderr: str = "") -> subprocess.CompletedProcess:
    return subprocess.CompletedProcess(["agent-deck"], returncode, "", stderr)


def _run(coro):
    with mock.patch("bridge.asyncio.sleep", new=_no_sleep):
        return asyncio.run(coro)


def _calls_for(mock_cli: mock.Mock, *prefix: str) -> list[tuple]:
    return [
        call.args
        for call in mock_cli.call_args_list
        if call.args[:len(prefix)] == prefix
    ]


def test_existing_conductor_title_retries_start_without_add():
    with mock.patch(
        "bridge.get_session_status",
        side_effect=["unknown", "running"],
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=[{"title": "conductor-ops", "profile": "work", "id": "existing"}],
    ) as mock_sessions, mock.patch(
        "bridge.run_cli",
        side_effect=[_completed(1, "not found"), _completed(0)],
    ) as mock_cli:
        assert _run(ensure_conductor_running("ops", "work")) is True

    mock_sessions.assert_called_once_with(profile="work", fail_closed=True)
    assert _calls_for(mock_cli, "add") == []
    assert len(_calls_for(mock_cli, "session", "start", "conductor-ops")) == 2


def test_fresh_setup_creates_session_then_starts_it():
    with mock.patch(
        "bridge.get_session_status",
        side_effect=["unknown", "running"],
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=[],
    ), mock.patch(
        "bridge.run_cli",
        side_effect=[_completed(1, "not found"), _completed(0), _completed(0)],
    ) as mock_cli:
        assert _run(ensure_conductor_running("ops", "work")) is True

    commands = [call.args[:3] for call in mock_cli.call_args_list]
    assert commands == [
        ("session", "start", "conductor-ops"),
        ("add", str(CONDUCTOR_DIR / "ops"), "-t"),
        ("session", "start", "conductor-ops"),
    ]
    assert len(_calls_for(mock_cli, "add")) == 1


def test_running_status_fast_path_has_no_cli_mutations():
    running_statuses = ("waiting", "running", "idle", "active", "starting")

    for status in running_statuses:
        with mock.patch(
            "bridge.get_session_status",
            return_value=status,
        ), mock.patch("bridge.get_sessions_list") as mock_sessions, mock.patch(
            "bridge.run_cli"
        ) as mock_cli:
            assert _run(ensure_conductor_running("ops", "work")) is True

        mock_sessions.assert_not_called()
        mock_cli.assert_not_called()


def test_same_title_in_different_profile_does_not_suppress_creation():
    with mock.patch(
        "bridge.get_session_status",
        side_effect=["unknown", "running"],
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=[{"title": "conductor-ops", "profile": "personal"}],
    ), mock.patch(
        "bridge.run_cli",
        side_effect=[_completed(1, "not found"), _completed(0), _completed(0)],
    ) as mock_cli:
        assert _run(ensure_conductor_running("ops", "work")) is True

    assert len(_calls_for(mock_cli, "add")) == 1


def test_existing_conductor_start_failure_returns_false_without_add():
    with mock.patch(
        "bridge.get_session_status",
        side_effect=["unknown", "unknown"],
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=[{"title": "conductor-ops", "profile": "work", "id": "existing"}],
    ), mock.patch(
        "bridge.run_cli",
        side_effect=[_completed(1, "not found"), _completed(1, "still not found")],
    ) as mock_cli:
        assert _run(ensure_conductor_running("ops", "work")) is False

    assert _calls_for(mock_cli, "add") == []
    assert len(_calls_for(mock_cli, "session", "start", "conductor-ops")) == 2
