"""Regression tests for the conductor-session title-drift bug.

Without --title-lock, agent-deck's title-sync overwrites a freshly created
conductor session's title with the agent's own session name on its first
hook event. The bridge's exact-title lookups (get_session_status and
_find_session_by_title's dedupe) then stop matching that session on every
later call, so ensure_conductor_running concludes it's "not running" and
creates a brand new one -- forever, once per restart.
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

from bridge import CONDUCTOR_DIR, ensure_conductor_running, get_sessions_list  # noqa: E402


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
        if call.args[: len(prefix)] == prefix
    ]


def test_conductor_creation_passes_title_lock():
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
        assert _run(ensure_conductor_running("monitor", "default")) is True

    add_calls = _calls_for(mock_cli, "add")
    assert len(add_calls) == 1
    assert "--title-lock" in add_calls[0]


def test_drifted_title_reuses_unique_canonical_path_by_stable_id():
    session_path = str(CONDUCTOR_DIR / "monitor")
    sessions = [{
        "id": "stable-session-id",
        "title": "drifted-agent-title",
        "path": session_path,
        "profile": "default",
    }]
    with mock.patch(
        "bridge.get_session_status",
        side_effect=["unknown", "running"],
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=sessions,
    ), mock.patch(
        "bridge.run_cli",
        side_effect=[_completed(1, "not found"), _completed(0), _completed(0)],
    ) as mock_cli:
        assert _run(ensure_conductor_running("monitor", "default")) is True

    assert _calls_for(
        mock_cli, "session", "set", "stable-session-id", "title", "conductor-monitor"
    )
    assert _calls_for(mock_cli, "session", "start", "stable-session-id")
    assert not _calls_for(mock_cli, "add")


def test_ambiguous_canonical_path_fails_closed_without_add():
    session_path = str(CONDUCTOR_DIR / "monitor")
    sessions = [
        {"id": "one", "title": "first-drift", "path": session_path},
        {"id": "two", "title": "second-drift", "path": session_path},
    ]
    with mock.patch(
        "bridge.get_session_status",
        return_value="unknown",
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=sessions,
    ), mock.patch(
        "bridge.run_cli",
        return_value=_completed(1, "not found"),
    ) as mock_cli:
        assert _run(ensure_conductor_running("monitor", "default")) is False

    assert not _calls_for(mock_cli, "add")
    assert not _calls_for(mock_cli, "session", "set")


def test_drifted_path_and_different_exact_title_fail_closed():
    sessions = [
        {
            "id": "drifted-id",
            "title": "drifted-agent-title",
            "path": str(CONDUCTOR_DIR / "monitor"),
            "profile": "default",
        },
        {
            "id": "exact-id",
            "title": "conductor-monitor",
            "path": "/tmp/unrelated-project",
            "profile": "default",
        },
    ]
    with mock.patch(
        "bridge.get_session_status",
        return_value="unknown",
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=sessions,
    ), mock.patch(
        "bridge.run_cli",
        return_value=_completed(1, "not found"),
    ) as mock_cli:
        assert _run(ensure_conductor_running("monitor", "default")) is False

    assert not _calls_for(mock_cli, "add")
    assert not _calls_for(mock_cli, "session", "set")


def test_exact_title_path_retries_by_current_title_without_add():
    sessions = [{
        "id": "canonical-id",
        "title": "conductor-monitor",
        "path": str(CONDUCTOR_DIR / "monitor"),
        "profile": "default",
    }]
    with mock.patch(
        "bridge.get_session_status",
        side_effect=["unknown", "running"],
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=sessions,
    ), mock.patch(
        "bridge.run_cli",
        side_effect=[_completed(1, "not found"), _completed(0)],
    ) as mock_cli:
        assert _run(ensure_conductor_running("monitor", "default")) is True

    assert len(_calls_for(mock_cli, "session", "start", "conductor-monitor")) == 2
    assert not _calls_for(mock_cli, "add")


def test_list_failure_fails_closed_without_add():
    with mock.patch(
        "bridge.get_sessions_list",
        return_value=None,
    ), mock.patch(
        "bridge.run_cli", return_value=_completed(1, "not found")
    ) as mock_cli:
        assert _run(ensure_conductor_running("monitor", "default")) is False

    assert len(_calls_for(mock_cli, "session", "start", "conductor-monitor")) == 1
    assert not _calls_for(mock_cli, "add")


def test_verified_empty_profile_allows_first_conductor_creation():
    empty = _completed(0)
    empty.stdout = "No sessions found in profile 'default'.\n"
    with mock.patch("bridge.run_cli", return_value=empty):
        assert get_sessions_list("default", fail_closed=True) == []


def test_malformed_list_json_fails_closed():
    malformed = _completed(0)
    malformed.stdout = "unexpected output"
    with mock.patch("bridge.run_cli", return_value=malformed):
        assert get_sessions_list("default", fail_closed=True) is None


def test_malformed_list_json_schema_fails_closed():
    malformed = _completed(0)
    malformed.stdout = '{"sessions": "not-a-list"}'
    with mock.patch("bridge.run_cli", return_value=malformed):
        assert get_sessions_list("default", fail_closed=True) is None
