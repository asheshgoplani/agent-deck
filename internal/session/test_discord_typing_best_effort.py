#!/usr/bin/env python3
"""
Behavioral regression test for the Discord typing-indicator best-effort fix
(PR #2205, part of the #2204->#2208->#2206->#2207->#2205 stack).

CodeRabbit asked for a test proving that a typing-indicator failure no
longer aborts the reply path. discord.py isn't installed in this
environment (see test_discord_message_enrichment.py for the same
constraint), so this mirrors the exact on_message pattern from
conductor_bridge.py (see the block starting at `_best_effort_typing`)
with a fake `message.channel.typing()` that raises, and asserts the
"delivery" work still runs to completion and the exception never
propagates out of the reply path.
"""

import asyncio
import contextlib
import unittest


class _RaisingTyping:
    """Mirrors message.channel.typing(): its __aenter__ raises, like a
    Discord API failure (e.g. missing permissions) would."""

    async def __aenter__(self):
        raise RuntimeError("403 Forbidden: Missing Permissions")

    async def __aexit__(self, exc_type, exc, tb):
        return False


class _Channel:
    def __init__(self):
        self.typing_calls = 0

    def typing(self):
        self.typing_calls += 1
        return _RaisingTyping()


async def _best_effort_typing(channel, warnings):
    """Verbatim mirror of conductor_bridge.py's _best_effort_typing closure."""
    try:
        async with channel.typing():
            while True:
                await asyncio.sleep(5)
    except Exception as exc:
        warnings.append(str(exc))


async def _deliver(channel, warnings, delivery_result):
    """Mirrors the on_message block: run typing as a cancellable background
    task, await the blocking delivery call, then cancel/await the typing
    task in a finally clause."""
    typing_task = asyncio.create_task(_best_effort_typing(channel, warnings))
    try:
        # Stand-in for `await loop.run_in_executor(None, lambda: send_to_conductor(...))`.
        await asyncio.sleep(0)
        result = delivery_result()
    finally:
        typing_task.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await typing_task
    return result


class TestDiscordTypingBestEffort(unittest.TestCase):
    def test_typing_failure_does_not_abort_reply_path(self):
        """A typing-indicator failure must not raise out of the delivery
        path, and delivery's own result must be returned unchanged."""
        channel = _Channel()
        warnings = []
        delivered = {"ok": True, "response": "hello", "still_running": False}

        result = asyncio.run(
            _deliver(channel, warnings, lambda: delivered)
        )

        self.assertEqual(result, delivered)
        self.assertEqual(channel.typing_calls, 1)
        self.assertEqual(len(warnings), 1)
        self.assertIn("Missing Permissions", warnings[0])

    def test_typing_task_is_cancelled_after_delivery(self):
        """The background typing task must be cancelled (not leaked) once
        delivery completes, even when typing never raises."""

        class _OkTyping:
            async def __aenter__(self):
                return self

            async def __aexit__(self, exc_type, exc, tb):
                return False

        class _OkChannel:
            def typing(self):
                return _OkTyping()

        async def run():
            channel = _OkChannel()
            warnings = []
            typing_task = asyncio.create_task(_best_effort_typing(channel, warnings))
            try:
                await asyncio.sleep(0)
                result = "delivered"
            finally:
                typing_task.cancel()
                with contextlib.suppress(asyncio.CancelledError):
                    await typing_task
            return result, typing_task

        result, typing_task = asyncio.run(run())
        self.assertEqual(result, "delivered")
        self.assertTrue(typing_task.done())
        self.assertTrue(typing_task.cancelled())


if __name__ == "__main__":
    unittest.main()
