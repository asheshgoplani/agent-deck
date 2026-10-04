"""Regression tests for queued conductor sends using accepted-turn ownership."""

from __future__ import annotations

import asyncio
import json
import subprocess
from collections import deque
from pathlib import Path
from unittest import mock

import bridge


async def _no_sleep(_seconds: float) -> None:
    return None


async def _finish_scheduled_tasks() -> None:
    for _ in range(10):
        pending = [
            task for task in asyncio.all_tasks() if task is not asyncio.current_task()
        ]
        if not pending:
            return
        await asyncio.gather(*pending)


def _run(coro):
    with mock.patch("bridge.asyncio.sleep", new=_no_sleep):
        return asyncio.run(coro)


def test_queue_drain_returns_structured_wait_response_without_stale_output_read():
    delivered: list[str] = []

    async def callback(text: str) -> None:
        delivered.append(text)

    async def driver() -> None:
        bridge._message_queue.clear()
        bridge._message_queue["conductor-ops"] = deque(
            [
                ("status?", "work", callback),
            ]
        )
        with (
            mock.patch(
                "bridge.get_session_status",
                return_value="waiting",
            ),
            mock.patch(
                "bridge.send_to_conductor",
                return_value=(True, "exact reply", False),
            ) as send,
            mock.patch("bridge.get_session_output") as output,
            mock.patch(
                "bridge.run_cli",
            ) as cli,
        ):
            await bridge._drain_queue()
            await _finish_scheduled_tasks()

        assert send.call_args.args == ("conductor-ops", "status?")
        assert send.call_args.kwargs == {
            "profile": "work",
            "wait_for_reply": True,
            "response_timeout": bridge.RESPONSE_TIMEOUT,
            "claim_late_reply": True,
        }
        output.assert_not_called()
        cli.assert_not_called()

    try:
        _run(driver())
        assert delivered == ["exact reply"]
    finally:
        bridge._message_queue.clear()


def test_queue_drain_transfers_accepted_timeout_to_reply_only_watcher():
    async def callback(_text: str) -> None:
        raise AssertionError("the watcher, not the drain, owns the late callback")

    async def driver() -> None:
        bridge._message_queue.clear()
        bridge._message_queue["conductor-ops"] = deque(
            [
                ("long job", "work", callback),
            ]
        )
        with (
            mock.patch(
                "bridge.get_session_status",
                return_value="waiting",
            ),
            mock.patch(
                "bridge.send_to_conductor",
                return_value=(False, "", True),
            ) as send,
            mock.patch(
                "bridge._register_pending_reply",
                return_value=True,
            ) as register,
            mock.patch("bridge.run_cli") as cli,
        ):
            await bridge._drain_queue()

        send.assert_called_once()
        register.assert_called_once_with(
            "conductor-ops",
            "work",
            None,
            callback,
        )
        cli.assert_not_called()
        assert "conductor-ops" not in bridge._message_queue

    try:
        _run(driver())
    finally:
        bridge._message_queue.clear()


def test_queue_drain_routes_actual_claude_timeout_payload_to_late_reply():
    delivered: list[str] = []
    payload = (
        Path(__file__).parent / "fixtures" / "claude_completion_timeout.json"
    ).read_text()

    async def callback(text: str) -> None:
        delivered.append(text)

    async def fake_watcher(session, profile, receipt, reply_callback) -> None:
        assert (session, profile, receipt) == ("conductor-claude", "work", None)
        await reply_callback("late Claude reply")

    async def driver() -> None:
        bridge._message_queue.clear()
        bridge._message_queue["conductor-claude"] = deque(
            [("long job", "work", callback)]
        )
        with (
            mock.patch("bridge.get_session_status", return_value="waiting"),
            mock.patch(
                "bridge.run_cli",
                return_value=subprocess.CompletedProcess(
                    ["agent-deck"],
                    1,
                    payload,
                    "",
                ),
            ) as cli,
            mock.patch("bridge._watch_pending_reply", new=fake_watcher),
        ):
            await bridge._drain_queue()
            await _finish_scheduled_tasks()

        cli.assert_called_once()
        assert "conductor-claude" not in bridge._message_queue

    try:
        _run(driver())
        assert delivered == ["late Claude reply"]
    finally:
        bridge._message_queue.clear()


def test_target_busy_before_submission_keeps_message_queued():
    payload = json.dumps(
        {
            "success": False,
            "error": "message not delivered: target send lock remained busy",
            "code": "DELIVERY_FAILED",
            "delivery": "target_busy",
            "submitted": False,
            "confirmation": "failed",
        }
    )
    with mock.patch(
        "bridge.run_cli",
        return_value=subprocess.CompletedProcess(["agent-deck"], 1, payload, ""),
    ):
        assert bridge.send_to_conductor(
            "conductor-ops",
            "hi",
            profile="work",
            wait_for_reply=True,
        ) == (False, "", bridge._WAIT_SEND_QUEUE_REQUIRED)


def test_queue_drain_keeps_item_when_another_sender_owns_the_turn():
    class StopAfterOneCycle(Exception):
        pass

    async def callback(_text: str) -> None:
        raise AssertionError("an undelivered queue item must not fire its callback")

    async def driver() -> None:
        bridge._message_queue.clear()
        bridge._message_queue["conductor-ops"] = deque(
            [("second request", "work", callback)]
        )
        sleeps = 0

        async def one_cycle(_seconds: float) -> None:
            nonlocal sleeps
            sleeps += 1
            if sleeps > 1:
                raise StopAfterOneCycle

        with (
            mock.patch("bridge.asyncio.sleep", new=one_cycle),
            mock.patch("bridge.get_session_status", return_value="waiting"),
            mock.patch(
                "bridge.send_to_conductor",
                return_value=(False, "", bridge._WAIT_SEND_QUEUE_REQUIRED),
            ) as send,
        ):
            try:
                await bridge._drain_queue()
            except StopAfterOneCycle:
                pass

        send.assert_called_once()
        assert list(bridge._message_queue["conductor-ops"]) == [
            ("second request", "work", callback)
        ]

    try:
        asyncio.run(driver())
    finally:
        bridge._message_queue.clear()


def test_queue_drain_overflow_does_not_evict_inflight_or_next_unsent_item():
    delivered: list[str] = []

    async def inflight_callback(text: str) -> None:
        delivered.append(text)

    async def waiting_callback(_text: str) -> None:
        raise AssertionError("the next unsent item must remain queued")

    async def new_callback(_text: str) -> None:
        raise AssertionError("the newly queued item must remain queued")

    async def driver() -> None:
        bridge._message_queue.clear()
        bridge._message_queue["conductor-ops"] = deque(
            [
                ("in flight", "work", inflight_callback),
                ("already waiting", "work", waiting_callback),
            ]
        )
        sleeps = 0

        async def one_cycle(_seconds: float) -> None:
            nonlocal sleeps
            sleeps += 1
            if sleeps > 1:
                raise StopAsyncIteration

        def send_while_another_message_arrives(*_args, **_kwargs):
            bridge._enqueue_message(
                "conductor-ops",
                "new arrival",
                "work",
                new_callback,
            )
            return True, "in-flight reply", False

        with (
            mock.patch.object(bridge, "MAX_QUEUE_DEPTH", 2),
            mock.patch("bridge.asyncio.sleep", new=one_cycle),
            mock.patch("bridge.get_session_status", return_value="waiting"),
            mock.patch(
                "bridge.send_to_conductor",
                side_effect=send_while_another_message_arrives,
            ),
        ):
            try:
                await bridge._drain_queue()
            except StopAsyncIteration:
                pass
            await _finish_scheduled_tasks()

        assert [item[0] for item in bridge._message_queue["conductor-ops"]] == [
            "already waiting",
            "new arrival",
        ]

    try:
        asyncio.run(driver())
        assert delivered == ["in-flight reply"]
    finally:
        bridge._message_queue.clear()
