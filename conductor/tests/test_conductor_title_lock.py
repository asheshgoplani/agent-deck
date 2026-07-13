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

from bridge import ensure_conductor_running  # noqa: E402


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
