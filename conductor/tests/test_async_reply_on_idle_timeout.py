"""Tests for the timeout -> async-reply fallback on the idle send path.

Background: when the conductor is IDLE on arrival, the handler delivers the
message with a blocking ``session send --wait --timeout {RESPONSE_TIMEOUT}s``.
If that single turn outruns the timeout, the CLI exits non-zero with stderr
like ``timeout waiting for completion: agent still running after 5m0s``. The
message was already delivered and the conductor keeps working — only the
synchronous reply is lost. Previously this surfaced as a FALSE
"[Failed to send message to conductor]".

The fix:
  * ``send_to_conductor`` distinguishes this case and returns
    ``(False, "", True)`` (still_running) instead of ``(False, "", False)``.
  * the handler registers a reply-only watcher (``_watch_pending_reply``) that
    polls until the turn finishes and delivers the captured output via the
    reply_callback — WITHOUT re-sending the message (no double-processing).
"""

from __future__ import annotations

import asyncio
import subprocess
import sys
import types
from collections import deque
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent.parent))
try:
    import toml  # noqa: F401
except ModuleNotFoundError:
    sys.modules["toml"] = types.SimpleNamespace(load=lambda *_a, **_k: {})

import bridge  # noqa: E402
from bridge import (  # noqa: E402
    _is_still_running_timeout,
    _is_unconfirmed_submission,
    _pane_reports_active,
    _drain_queue,
    _message_queue,
    _register_pending_reply,
    _watch_pending_reply,
    get_session_status,
    send_to_conductor,
)


def _completed(returncode: int = 0, stdout: str = "", stderr: str = ""):
    return subprocess.CompletedProcess(["agent-deck"], returncode, stdout, stderr)


async def _no_sleep(_seconds: float) -> None:
    return None


def _run(coro):
    with mock.patch("bridge.asyncio.sleep", new=_no_sleep):
        return asyncio.run(coro)


# --- stderr classification -------------------------------------------------

def test_is_still_running_timeout_matches_cli_phrasings():
    assert _is_still_running_timeout(
        "timeout waiting for completion: agent still running after 5m0s"
    )
    assert _is_still_running_timeout("agent still running after 5m0s")
    assert _is_still_running_timeout("TIMEOUT WAITING FOR COMPLETION")


def test_is_still_running_timeout_rejects_genuine_failures():
    assert not _is_still_running_timeout("session not found")
    assert not _is_still_running_timeout("connection refused")
    assert not _is_still_running_timeout("")


def test_is_unconfirmed_submission_matches_only_1793_verdict():
    assert _is_unconfirmed_submission(
        "message reached the pane but submission was never confirmed after 10 "
        "checks (issue #1793): the agent never began processing it"
    )
    assert not _is_unconfirmed_submission("session not found")
    assert not _is_unconfirmed_submission("timeout waiting for completion")


def test_pane_reports_active_from_recent_codex_interrupt_affordance():
    assert _pane_reports_active("\n".join([
        "old output",
        "• Working (13s • esc to interrupt)",
        "› Use /skills to list available skills",
    ]))
    assert not _pane_reports_active("\n".join(
        ["• Working (old • esc to interrupt)"] + ["settled output"] * 40
    ))


def test_get_session_status_promotes_codex_waiting_from_live_pane():
    with mock.patch(
        "bridge.run_cli",
        side_effect=[
            _completed(0, stdout='{"status":"waiting","tool":"codex"}'),
            _completed(0, stdout="• Working (3s • esc to interrupt)\n"),
        ],
    ):
        assert get_session_status("conductor-ops", profile="work") == "active"


def test_get_session_status_does_not_probe_non_codex_pane():
    with mock.patch(
        "bridge.run_cli",
        return_value=_completed(0, stdout='{"status":"waiting","tool":"claude"}'),
    ) as run_cli:
        assert get_session_status("conductor-ops", profile="work") == "waiting"
    run_cli.assert_called_once()


# --- send_to_conductor wait-path signalling --------------------------------

def test_wait_timeout_still_running_signals_third_tuple():
    with mock.patch(
        "bridge.run_cli",
        return_value=_completed(
            1, stderr="timeout waiting for completion: agent still running after 5m0s"
        ),
    ):
        ok, response, still_running = send_to_conductor(
            "conductor-ops", "hi", profile="work", wait_for_reply=True,
        )
    assert ok is False
    assert response == ""
    assert still_running is True


def test_wait_genuine_failure_is_not_still_running():
    with mock.patch(
        "bridge.run_cli", return_value=_completed(1, stderr="session not found"),
    ):
        ok, response, still_running = send_to_conductor(
            "conductor-ops", "hi", profile="work", wait_for_reply=True,
        )
    assert ok is False
    assert still_running is False


def test_wait_1793_false_negative_with_live_codex_turn_becomes_pending_reply():
    failure = _completed(
        1,
        stderr=(
            "message reached the pane but submission was never confirmed after "
            "10 checks (issue #1793): the agent never began processing it"
        ),
    )
    with mock.patch("bridge.run_cli", return_value=failure), mock.patch(
        "bridge.get_session_status", return_value="active",
    ):
        ok, response, still_running = send_to_conductor(
            "conductor-ops", "hi", profile="work", wait_for_reply=True,
        )
    assert (ok, response, still_running) == (False, "", True)


def test_wait_1793_without_live_activity_remains_hard_failure():
    failure = _completed(
        1,
        stderr=(
            "message reached the pane but submission was never confirmed after "
            "10 checks (issue #1793): the agent never began processing it"
        ),
    )
    with mock.patch("bridge.run_cli", return_value=failure), mock.patch(
        "bridge.get_session_status", return_value="waiting",
    ):
        ok, response, still_running = send_to_conductor(
            "conductor-ops", "hi", profile="work", wait_for_reply=True,
        )
    assert (ok, response, still_running) == (False, "", False)


def test_wait_success_returns_output():
    with mock.patch(
        "bridge.run_cli", return_value=_completed(0, stdout="the answer\n"),
    ):
        ok, response, still_running = send_to_conductor(
            "conductor-ops", "hi", profile="work", wait_for_reply=True,
        )
    assert (ok, response, still_running) == (True, "the answer", False)


# --- queued delivery with delayed confirmation -----------------------------

def test_queue_drain_1793_with_live_turn_switches_to_reply_watcher():
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    failure = _completed(
        1,
        stderr=(
            "message reached the pane but submission was never confirmed after "
            "10 checks (issue #1793): the agent never began processing it"
        ),
    )

    async def driver() -> None:
        _message_queue.clear()
        _message_queue["conductor-ops"] = deque([("hi", "work", cb)])
        try:
            with mock.patch(
                "bridge.get_session_status", side_effect=["waiting", "active"],
            ), mock.patch(
                "bridge.run_cli", return_value=failure,
            ), mock.patch(
                "bridge._register_pending_reply",
            ) as register:
                await _drain_queue()
            register.assert_called_once_with("conductor-ops", "work", cb)
            assert "conductor-ops" not in _message_queue
        finally:
            _message_queue.clear()

    _run(driver())
    assert delivered == []  # the watcher, not the queue, owns final delivery


def test_queue_drain_1793_without_live_turn_fails_closed():
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    failure = _completed(
        1,
        stderr=(
            "message reached the pane but submission was never confirmed after "
            "10 checks (issue #1793): the agent never began processing it"
        ),
    )

    async def driver() -> None:
        _message_queue.clear()
        _message_queue["conductor-ops"] = deque([("hi", "work", cb)])
        try:
            with mock.patch(
                "bridge.get_session_status", side_effect=["waiting", "waiting"],
            ), mock.patch("bridge.run_cli", return_value=failure):
                await _drain_queue()
        finally:
            _message_queue.clear()

    _run(driver())
    assert len(delivered) == 1
    assert "send failed" in delivered[0].lower()


# --- the reply-only watcher ------------------------------------------------

def test_watcher_waits_then_delivers_output_without_resending():
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    # busy on first poll, then finished; output fetched once.
    with mock.patch(
        "bridge.get_session_status", side_effect=["running", "waiting"],
    ) as status, mock.patch(
        "bridge.get_session_output", return_value="investigation result",
    ) as output, mock.patch("bridge.run_cli") as run_cli:
        _run(_watch_pending_reply("conductor-ops", "work", cb))

    assert delivered == ["investigation result"]
    assert status.call_count == 2
    output.assert_called_once()
    # Critically: the watcher must NOT re-send the message (no double-process).
    run_cli.assert_not_called()


def test_watcher_handles_race_already_idle_on_first_poll():
    """Conductor finishes between the timeout and the first status poll."""
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    with mock.patch(
        "bridge.get_session_status", return_value="idle",
    ), mock.patch(
        "bridge.get_session_output", return_value="done already",
    ), mock.patch("bridge.run_cli") as run_cli:
        _run(_watch_pending_reply("conductor-ops", "work", cb))

    assert delivered == ["done already"]
    run_cli.assert_not_called()


def test_watcher_empty_output_uses_placeholder():
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    with mock.patch(
        "bridge.get_session_status", return_value="waiting",
    ), mock.patch(
        "bridge.get_session_output", return_value="   ",
    ):
        _run(_watch_pending_reply("conductor-ops", "work", cb))

    assert delivered == ["[No output from conductor.]"]


def test_watcher_gives_up_after_ceiling_and_notifies():
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    # Always busy -> exhaust max_polls, then notify the user it gave up.
    with mock.patch(
        "bridge.get_session_status", return_value="running",
    ), mock.patch(
        "bridge.get_session_output",
    ) as output, mock.patch.object(bridge, "PENDING_REPLY_MAX_WAIT", 0):
        _run(_watch_pending_reply("conductor-ops", "work", cb))

    assert len(delivered) == 1
    assert "still working" in delivered[0].lower()
    output.assert_not_called()  # never finished -> nothing to fetch


def test_register_pending_reply_schedules_and_fires():
    """End-to-end: registering a watcher eventually fires the reply_callback
    exactly once and never re-sends the message."""
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    async def driver() -> None:
        with mock.patch(
            "bridge.get_session_status", side_effect=["running", "waiting"],
        ), mock.patch(
            "bridge.get_session_output", return_value="late answer",
        ), mock.patch("bridge.run_cli") as run_cli:
            _register_pending_reply("conductor-ops", "work", cb)
            # Drain scheduled tasks until the watcher completes.
            for _ in range(10):
                pending = [
                    t for t in asyncio.all_tasks()
                    if t is not asyncio.current_task()
                ]
                if not pending:
                    break
                await asyncio.gather(*pending)
            run_cli.assert_not_called()

    _run(driver())
    assert delivered == ["late answer"]


def test_register_pending_reply_no_event_loop_is_safe():
    """Called from a bare sync context it logs and returns without raising."""
    async def cb(_text: str) -> None:  # pragma: no cover - never invoked
        pass

    # No running loop here -> must not raise.
    _register_pending_reply("conductor-ops", "work", cb)
