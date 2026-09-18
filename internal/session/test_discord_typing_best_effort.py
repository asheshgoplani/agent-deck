#!/usr/bin/env python3
"""
Behavioral regression test for the Discord typing-indicator best-effort fix
(PR #2205, part of the #2204->#2208->#2206->#2207->#2205 stack).

CodeRabbit asked for a test proving that a typing-indicator failure no
longer aborts the reply path. Unlike the other Discord tests, this one does
NOT mirror the bridge: it loads the real conductor_bridge.py with a stub
`discord` module injected into sys.modules (discord.py is not installed in
the test environment), builds the real bot via create_discord_bot(), and
drives the real on_message handler with a message whose
`channel.typing()` raises. With the fix reverted (typing wrapped around the
delivery call), on_message raises and never sends the reply; with the fix,
delivery completes and the reply is sent.
"""

import asyncio
import importlib.util
import os
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock

BRIDGE_PATH = Path(__file__).with_name("conductor_bridge.py")


def _install_discord_stub():
    """Provide just enough of discord.py for create_discord_bot() to build
    a bot and register its handlers without a network or a token."""

    class _Intents:
        message_content = False

        @classmethod
        def default(cls):
            return cls()

    class _Client:
        def __init__(self, intents=None):
            self.intents = intents
            self.user = None
            self.events = {}

        def event(self, coro):
            self.events[coro.__name__] = coro
            return coro

    class _CommandTree:
        def __init__(self, client):
            self.client = client

        def command(self, **kwargs):
            return lambda fn: fn

        def copy_global_to(self, guild=None):
            pass

        async def sync(self, guild=None):
            pass

    discord = types.ModuleType("discord")
    discord.Intents = _Intents
    discord.Client = _Client
    discord.Object = lambda id: types.SimpleNamespace(id=id)
    discord.Message = object
    discord.Interaction = object
    discord.File = object
    discord.ChannelType = types.SimpleNamespace(
        text="text", private="private", public_thread="public_thread",
        private_thread="private_thread", news_thread="news_thread",
    )
    app_commands = types.ModuleType("discord.app_commands")
    app_commands.CommandTree = _CommandTree
    app_commands.describe = lambda **kwargs: (lambda fn: fn)
    discord.app_commands = app_commands
    sys.modules["discord"] = discord
    sys.modules["discord.app_commands"] = app_commands


def _load_bridge():
    """Import conductor_bridge.py from disk with its data dir pointed at an
    empty temp dir so importing never touches the real agent-deck state."""
    _install_discord_stub()
    os.environ["AGENT_DECK_CONDUCTOR_DIR"] = tempfile.mkdtemp(prefix="bridge-test-")
    spec = importlib.util.spec_from_file_location("conductor_bridge_under_test", BRIDGE_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


bridge = _load_bridge()

_CONFIG = {
    "discord": {
        "configured": True,
        "bot_token": "test-token",
        "guild_id": 1,
        "channel_id": 42,
        "user_id": 7,
        "listen_mode": "all",
    },
}
_CONDUCTOR = {"name": "main", "profile": "personal"}


class _RaisingTyping:
    """message.channel.typing(): __aenter__ raises like a Discord API
    failure (e.g. missing permissions) would."""

    async def __aenter__(self):
        raise RuntimeError("403 Forbidden: Missing Permissions")

    async def __aexit__(self, exc_type, exc, tb):
        return False


class _Channel:
    def __init__(self):
        self.id = 42
        self.name = "general"
        self.type = "text"
        self.typing_calls = 0
        self.sent = []

    def typing(self):
        self.typing_calls += 1
        return _RaisingTyping()

    async def send(self, text, **kwargs):
        self.sent.append(text)


def _message(channel):
    author = types.SimpleNamespace(id=7, bot=False, name="ashesh", display_name="ashesh")
    return types.SimpleNamespace(
        author=author, channel=channel, content="hello conductor", mentions=[], reference=None,
    )


class TestDiscordTypingBestEffort(unittest.TestCase):
    async def _run_on_message(self, channel):
        """Drive the real on_message with the conductor-side calls stubbed:
        one conductor discovered and already running, send_to_conductor
        returning a successful reply. Returns the (title, msg) pairs handed
        to send_to_conductor."""
        bot, _ = bridge.create_discord_bot(_CONFIG)
        on_message = bot.events["on_message"]
        sent = []

        def fake_send_to_conductor(session_title, msg, **kwargs):
            sent.append((session_title, msg))
            return True, "reply from conductor", False

        async def running(name, profile):
            return True

        with mock.patch.object(bridge, "discover_conductors", return_value=[_CONDUCTOR]), \
                mock.patch.object(bridge, "get_conductor_names", return_value=["main"]), \
                mock.patch.object(bridge, "get_default_conductor", return_value=_CONDUCTOR), \
                mock.patch.object(bridge, "ensure_conductor_running", running), \
                mock.patch.object(bridge, "send_to_conductor", fake_send_to_conductor):
            await on_message(_message(channel))
        return sent

    def test_typing_failure_does_not_abort_reply_path(self):
        """A typing-indicator failure must neither raise out of on_message
        nor stop the message from reaching the conductor and the reply from
        reaching the channel."""
        channel = _Channel()
        with self.assertLogs(bridge.log, level="WARNING") as logs:
            sent = asyncio.run(self._run_on_message(channel))

        self.assertEqual(channel.typing_calls, 1)
        self.assertEqual(len(sent), 1)
        self.assertIn("hello conductor", sent[0][1])
        self.assertEqual(channel.sent, ["reply from conductor"])
        self.assertTrue(
            any("Missing Permissions" in line for line in logs.output),
            f"typing failure must be logged, got {logs.output}",
        )

    def test_typing_task_does_not_leak_after_delivery(self):
        """Once the reply is delivered no typing task may still be pending on
        the loop (the fix cancels and awaits it in a finally clause)."""

        class _OkTyping:
            async def __aenter__(self):
                return self

            async def __aexit__(self, exc_type, exc, tb):
                return False

        channel = _Channel()
        channel.typing = lambda: _OkTyping()

        async def run():
            await self._run_on_message(channel)
            return [t for t in asyncio.all_tasks() if t is not asyncio.current_task() and not t.done()]

        pending_after = asyncio.run(run())
        self.assertEqual(channel.sent, ["reply from conductor"])
        self.assertEqual(pending_after, [], "typing task must be cancelled once delivery completes")


if __name__ == "__main__":
    unittest.main()
