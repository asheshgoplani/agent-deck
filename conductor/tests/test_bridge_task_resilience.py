"""Regression tests for platform-task crash isolation in main()'s gather().

main() awaits every platform task (Telegram/Slack/Discord) via a single
asyncio.gather(). Previously, an uncaught exception from any one of them
(e.g. a TelegramNetworkError from a proxy timeout) killed gather() and thus
the whole bridge process; the OS service manager then respawned the process
in a tight loop, and every respawn re-ran the conductor pre-start step.
_run_platform_task must contain the failure and retry with backoff instead.
"""

from __future__ import annotations

import asyncio
import sys
import types
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent.parent))
try:
    import toml  # noqa: F401
except ModuleNotFoundError:
    sys.modules["toml"] = types.SimpleNamespace(load=lambda *_args, **_kwargs: {})

from bridge import _run_platform_task  # noqa: E402


def _run(coro):
    return asyncio.run(coro)


def test_failing_task_is_retried_instead_of_raising():
    attempts = []

    async def flaky():
        attempts.append(1)
        if len(attempts) < 3:
            raise RuntimeError("network timeout")
        return "done"

    async def scenario():
        with mock.patch("bridge.asyncio.sleep", new=mock.AsyncMock()):
            await _run_platform_task("test-task", flaky)

    _run(scenario())
    assert len(attempts) == 3


def test_successful_task_returns_without_retry():
    calls = []

    async def succeeds_immediately():
        calls.append(1)

    async def scenario():
        with mock.patch("bridge.asyncio.sleep", new=mock.AsyncMock()) as mock_sleep:
            await _run_platform_task("test-task", succeeds_immediately)
        return mock_sleep

    mock_sleep = _run(scenario())
    assert calls == [1]
    mock_sleep.assert_not_called()


def test_cancelled_error_propagates_without_retry():
    async def cancelled():
        raise asyncio.CancelledError()

    async def scenario():
        with mock.patch("bridge.asyncio.sleep", new=mock.AsyncMock()):
            await _run_platform_task("test-task", cancelled)

    with mock.patch("bridge.log"):
        try:
            _run(scenario())
            raised = False
        except asyncio.CancelledError:
            raised = True
    assert raised


def test_backoff_doubles_and_caps_at_max():
    delays = []

    async def sleep_recorder(seconds):
        delays.append(seconds)

    attempts = []

    async def flaky():
        attempts.append(1)
        if len(attempts) < 5:
            raise RuntimeError("still failing")

    async def scenario():
        with mock.patch("bridge.asyncio.sleep", new=sleep_recorder):
            await _run_platform_task("test-task", flaky, max_backoff=20)

    _run(scenario())
    assert delays == [5, 10, 20, 20]


def test_retry_never_overlaps_platform_task_instances():
    active = 0
    peak_active = 0
    attempts = 0

    async def flaky():
        nonlocal active, peak_active, attempts
        attempts += 1
        active += 1
        peak_active = max(peak_active, active)
        try:
            if attempts < 3:
                raise RuntimeError("temporary failure")
        finally:
            active -= 1

    async def scenario():
        with mock.patch("bridge.asyncio.sleep", new=mock.AsyncMock()):
            await _run_platform_task("test-task", flaky)

    _run(scenario())
    assert attempts == 3
    assert peak_active == 1


def test_retry_logs_each_failure_with_capped_delay():
    attempts = 0

    async def flaky():
        nonlocal attempts
        attempts += 1
        if attempts < 4:
            raise RuntimeError("network unavailable")

    async def scenario():
        with mock.patch("bridge.asyncio.sleep", new=mock.AsyncMock()), mock.patch(
            "bridge.log.error"
        ) as log_error:
            await _run_platform_task("Telegram polling", flaky, max_backoff=5)
        return log_error

    log_error = _run(scenario())
    assert log_error.call_count == 3
    assert all(call.args[-1] == 5 for call in log_error.call_args_list)
