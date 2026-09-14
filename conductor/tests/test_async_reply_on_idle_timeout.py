"""Tests for the timeout -> async-reply fallback on the idle send path.

Background: when the conductor is IDLE on arrival, the handler delivers the
message with a blocking ``session send --wait --timeout {RESPONSE_TIMEOUT}s``.
If that single turn outruns the timeout, the CLI exits non-zero with stderr
like ``timeout waiting for completion: agent still running after 5m0s``. The
message was already delivered and the conductor keeps working — only the
synchronous reply is lost. Previously this surfaced as a FALSE
"[Failed to send message to conductor]".

The fix requires a structured accepted-turn receipt. The reply-only watcher
then waits for a new matching Codex completion and fresh output before calling
the origin callback, without re-sending the message.
"""

from __future__ import annotations

import asyncio
import concurrent.futures
import json
import subprocess
import sys
import threading
import types
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent.parent))
try:
    import toml  # noqa: F401
except ModuleNotFoundError:
    sys.modules["toml"] = types.SimpleNamespace(load=lambda *_a, **_k: {})

import bridge  # noqa: E402
from bridge import (  # noqa: E402
    _register_pending_reply,
    _watch_pending_reply,
    send_to_conductor,
)


def _completed(returncode: int = 0, stdout: str = "", stderr: str = ""):
    return subprocess.CompletedProcess(["agent-deck"], returncode, stdout, stderr)


async def _no_sleep(_seconds: float) -> None:
    return None


def _receipt(**overrides):
    receipt = {
        "receipt_id": "receipt-1",
        "instance_id": "instance-1",
        "codex_session_id": "thread-1",
        "turn_generation": "thread-1:turn-new",
        "accepted_at": "2026-09-14T15:00:00Z",
    }
    receipt.update(overrides)
    return receipt


def _run(coro):
    with mock.patch("bridge.asyncio.sleep", new=_no_sleep):
        return asyncio.run(coro)


# --- send_to_conductor wait-path signalling --------------------------------

def test_wait_timeout_preserves_machine_readable_accepted_turn_receipt():
    payload = (Path(__file__).parent / "fixtures" / "issue2278_codex_timeout.json").read_text()
    receipt = json.loads(payload)["accepted_turn"]
    with mock.patch("bridge.run_cli", return_value=_completed(1, stdout=payload)) as cli:
        ok, response, pending = send_to_conductor(
            "conductor-ops", "hi", profile="work", wait_for_reply=True,
        )
    assert ok is False
    assert response == ""
    assert pending == receipt
    assert "--json" in cli.call_args.args
    assert "-q" not in cli.call_args.args


def test_exact_output_delay_preserves_receipt_for_late_watcher():
    payload = (
        Path(__file__).parent
        / "fixtures"
        / "issue2278_codex_output_pending.json"
    ).read_text()
    receipt = json.loads(payload)["accepted_turn"]
    with mock.patch("bridge.run_cli", return_value=_completed(1, stdout=payload)):
        assert send_to_conductor(
            "conductor-ops", "hi", profile="work", wait_for_reply=True,
        ) == (False, "", receipt)


def test_wait_genuine_failure_is_not_still_running():
    with mock.patch(
        "bridge.run_cli", return_value=_completed(1, stderr="session not found"),
    ):
        ok, response, pending = send_to_conductor(
            "conductor-ops", "hi", profile="work", wait_for_reply=True,
        )
    assert ok is False
    assert pending is False


def test_wait_unverified_timeout_does_not_acquire_reply_ownership():
    payload = json.dumps({
        "success": False,
        "completion": "timeout",
        "delivery": "unverified",
        "submitted": False,
        "accepted_turn_kind": "codex_rollout",
    })
    with mock.patch("bridge.run_cli", return_value=_completed(1, stdout=payload)):
        assert send_to_conductor(
            "conductor-ops", "hi", profile="work", wait_for_reply=True,
        ) == (False, "", False)


def test_non_codex_submitted_timeout_preserves_legacy_async_signal():
    payload = json.dumps({
        "success": False,
        "completion": "timeout",
        "delivery": "submitted",
        "submitted": True,
    })
    with mock.patch("bridge.run_cli", return_value=_completed(1, stdout=payload)):
        assert send_to_conductor(
            "conductor-legacy", "hi", profile="work", wait_for_reply=True,
        ) == (False, "", True)


def test_codex_timeout_without_exact_receipt_fails_closed():
    payload = json.dumps({
        "success": False,
        "completion": "timeout",
        "delivery": "submitted",
        "submitted": True,
        "accepted_turn_kind": "codex_rollout",
    })
    with mock.patch("bridge.run_cli", return_value=_completed(1, stdout=payload)):
        assert send_to_conductor(
            "conductor-codex", "hi", profile="work", wait_for_reply=True,
        ) == (False, "", False)


def test_wait_success_returns_output():
    with mock.patch(
        "bridge.run_cli", return_value=_completed(0, stdout=json.dumps({
            "success": True, "completion": "complete", "content": "the answer",
        })),
    ) as cli, mock.patch("bridge.get_session_output") as output:
        ok, response, pending = send_to_conductor(
            "conductor-ops", "hi", profile="work", wait_for_reply=True,
        )
    assert (ok, response, pending) == (True, "the answer", False)
    cli.assert_called_once()
    output.assert_not_called()


# --- the reply-only watcher ------------------------------------------------

def test_watcher_waits_then_delivers_output_without_resending():
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    receipt = _receipt()
    # A prior response remains visible after the completion hook until the new
    # rollout record flushes; only the exact accepted generation is delivered.
    with mock.patch(
        "bridge.get_session_output_state", side_effect=[
            ("previous answer", "thread-1:turn-previous"),
            ("investigation result", "thread-1:turn-new"),
        ],
    ) as output, mock.patch("bridge.run_cli") as run_cli:
        _run(_watch_pending_reply("conductor-ops", "work", receipt, cb))

    assert delivered == ["investigation result"]
    assert output.call_count == 2
    # Critically: the watcher must NOT re-send the message (no double-process).
    run_cli.assert_not_called()


def test_watcher_delivers_identical_reply_from_new_turn():
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    with mock.patch(
        "bridge.get_session_output_state",
        return_value=("OK", "thread-1:turn-new"),
    ):
        _run(_watch_pending_reply("conductor-ops", "work", _receipt(), cb))

    assert delivered == ["OK"]


def test_legacy_watcher_remains_available_for_receiptless_tools():
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    with mock.patch(
        "bridge.get_session_status", side_effect=["running", "waiting"],
    ), mock.patch("bridge.get_session_output", return_value="legacy answer"):
        _run(_watch_pending_reply("conductor-legacy", "work", None, cb))

    assert delivered == ["legacy answer"]


def test_watcher_handles_race_already_idle_on_first_poll():
    """The accepted turn completes between timeout and the first poll."""
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    with mock.patch(
        "bridge.get_session_output_state", return_value=(
            "done already", "thread-1:turn-new",
        ),
    ), mock.patch("bridge.run_cli") as run_cli:
        _run(_watch_pending_reply("conductor-ops", "work", _receipt(), cb))

    assert delivered == ["done already"]
    run_cli.assert_not_called()


def test_watcher_empty_output_uses_placeholder():
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    with mock.patch(
        "bridge.get_session_output_state", return_value=("   ", "thread-1:turn-new"),
    ):
        _run(_watch_pending_reply("conductor-ops", "work", _receipt(), cb))

    assert delivered == ["[No output from conductor.]"]


def test_watcher_gives_up_after_ceiling_and_notifies():
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    # No matching generation -> exhaust max_polls, then notify the user.
    with mock.patch(
        "bridge.get_session_output_state", return_value=("", ""),
    ) as output, mock.patch.object(bridge, "PENDING_REPLY_MAX_WAIT", 0):
        _run(_watch_pending_reply("conductor-ops", "work", _receipt(), cb))

    assert len(delivered) == 1
    assert "still working" in delivered[0].lower()
    output.assert_called_once()


def test_register_pending_reply_schedules_and_fires():
    """End-to-end: registering a watcher eventually fires the reply_callback
    exactly once and never re-sends the message."""
    delivered: list[str] = []

    async def cb(text: str) -> None:
        delivered.append(text)

    async def driver() -> None:
        with mock.patch(
            "bridge.get_session_output_state", return_value=(
                "late answer", "thread-1:turn-new",
            ),
        ), mock.patch("bridge.run_cli") as run_cli:
            assert _register_pending_reply("conductor-ops", "work", _receipt(), cb)
            assert not _register_pending_reply("conductor-ops", "work", _receipt(), cb)
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


def test_wait_send_refuses_second_owner_without_submitting():
    async def owner() -> None:
        key = ("work", "conductor-ops")
        with bridge._reply_owner_lock:
            bridge._pending_reply_tasks[key] = asyncio.current_task()
        try:
            with mock.patch("bridge.run_cli") as cli:
                assert send_to_conductor(
                    "conductor-ops", "second", profile="work", wait_for_reply=True,
                ) == (False, "", bridge._WAIT_SEND_QUEUE_REQUIRED)
                cli.assert_not_called()
        finally:
            with bridge._reply_owner_lock:
                bridge._pending_reply_tasks.pop(key, None)

    asyncio.run(owner())


def test_concurrent_wait_sends_reserve_one_owner_and_queue_the_other():
    payload = (Path(__file__).parent / "fixtures" / "issue2278_codex_timeout.json").read_text()
    entered = threading.Event()
    release = threading.Event()
    calls = 0

    def blocking_cli(*_args, **_kwargs):
        nonlocal calls
        calls += 1
        entered.set()
        assert release.wait(timeout=2)
        return _completed(1, stdout=payload)

    def send(message):
        return send_to_conductor(
            "conductor-race", message, profile="work", wait_for_reply=True,
            claim_late_reply=True,
        )

    with mock.patch("bridge.run_cli", side_effect=blocking_cli):
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            first = pool.submit(send, "first")
            assert entered.wait(timeout=2)
            second = pool.submit(send, "second")
            assert second.result(timeout=2) == (
                False, "", bridge._WAIT_SEND_QUEUE_REQUIRED,
            )
            release.set()
            first_result = first.result(timeout=2)

    assert calls == 1
    assert isinstance(first_result[2], dict)
    bridge._release_late_reply_claim("conductor-race", "work", first_result[2])


def test_register_pending_reply_no_event_loop_is_safe():
    """Called from a bare sync context it logs and returns without raising."""
    async def cb(_text: str) -> None:  # pragma: no cover - never invoked
        pass

    # No running loop here -> must not raise.
    assert not _register_pending_reply("conductor-ops", "work", _receipt(), cb)
