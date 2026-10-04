"""Regression tests for queued conductor sends using accepted-turn ownership."""

from __future__ import annotations

import asyncio
from collections import deque
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
