#!/usr/bin/env python3
"""
Test suite for Discord message enrichment in conductor bridge.

Tests build_discord_context_tag(), which tags messages with
[from:name (ID)] plus [channel:#name (ID)] / [thread:#name (ID) in #parent] /
[dm] before relaying to the conductor. Mirrors the Slack tagging convention
covered by test_slack_message_enrichment.py.

The bridge defines build_discord_context_tag as a closure inside
create_discord_bot, so — as with the Slack tests — this exercises a faithful
mirror of that logic rather than importing it directly.
"""

import unittest
from types import SimpleNamespace


class _ChannelType:
    """Minimal stand-in for discord.ChannelType (distinct sentinels)."""

    text = "text"
    private = "private"
    public_thread = "public_thread"
    private_thread = "private_thread"
    news_thread = "news_thread"


discord = SimpleNamespace(ChannelType=_ChannelType)  # referenced by the mirror below


def build_discord_context_tag(message) -> str:
    """Mirror of the bridge's build_discord_context_tag (see conductor_bridge.py)."""
    author = message.author
    author_name = getattr(author, "display_name", None) or getattr(author, "name", "?")
    from_tag = f"[from:{author_name} ({author.id})]"
    channel = message.channel
    ct = getattr(channel, "type", None)
    thread_types = (
        getattr(discord.ChannelType, "public_thread", None),
        getattr(discord.ChannelType, "private_thread", None),
        getattr(discord.ChannelType, "news_thread", None),
    )
    if ct == getattr(discord.ChannelType, "private", None):
        chan_tag = "[dm]"
    elif ct in thread_types:
        parent = getattr(channel, "parent", None)
        parent_name = f"#{parent.name}" if parent and getattr(parent, "name", None) else "?"
        chan_tag = f"[thread:#{channel.name} ({channel.id}) in {parent_name}]"
    else:
        chan_name = getattr(channel, "name", "?")
        chan_tag = f"[channel:#{chan_name} ({channel.id})]"
    return f"{from_tag} {chan_tag}"


def _msg(author, channel):
    return SimpleNamespace(author=author, channel=channel)


class TestBuildDiscordContextTag(unittest.TestCase):
    """Exact-format assertions for each channel/author branch."""

    def test_regular_channel(self):
        """A message in a normal text channel is tagged [channel:#name (id)]."""
        m = _msg(
            SimpleNamespace(display_name="alice", name="alice_x", id=987654321),
            SimpleNamespace(type=discord.ChannelType.text, name="general", id=456, parent=None),
        )
        self.assertEqual(
            build_discord_context_tag(m),
            "[from:alice (987654321)] [channel:#general (456)]",
        )

    def test_public_thread_includes_parent(self):
        """A public thread is tagged [thread:#name (id) in #parent]."""
        m = _msg(
            SimpleNamespace(display_name="alice", name="alice_x", id=987654321),
            SimpleNamespace(
                type=discord.ChannelType.public_thread,
                name="deploy-thread",
                id=789,
                parent=SimpleNamespace(name="general"),
            ),
        )
        self.assertEqual(
            build_discord_context_tag(m),
            "[from:alice (987654321)] [thread:#deploy-thread (789) in #general]",
        )

    def test_private_thread_includes_parent(self):
        """Private threads use the same [thread:...] shape as public threads."""
        m = _msg(
            SimpleNamespace(display_name="alice", name="alice_x", id=987654321),
            SimpleNamespace(
                type=discord.ChannelType.private_thread,
                name="secret",
                id=791,
                parent=SimpleNamespace(name="ops"),
            ),
        )
        self.assertEqual(
            build_discord_context_tag(m),
            "[from:alice (987654321)] [thread:#secret (791) in #ops]",
        )

    def test_news_thread_includes_parent(self):
        """Announcement (news) threads use the same [thread:...] shape."""
        m = _msg(
            SimpleNamespace(display_name="alice", name="alice_x", id=987654321),
            SimpleNamespace(
                type=discord.ChannelType.news_thread,
                name="release-notes",
                id=792,
                parent=SimpleNamespace(name="announcements"),
            ),
        )
        self.assertEqual(
            build_discord_context_tag(m),
            "[from:alice (987654321)] [thread:#release-notes (792) in #announcements]",
        )

    def test_thread_missing_parent_falls_back_to_question_mark(self):
        """When a thread's parent can't be resolved, parent renders as '?'."""
        m = _msg(
            SimpleNamespace(display_name="alice", name="alice_x", id=987654321),
            SimpleNamespace(
                type=discord.ChannelType.public_thread, name="orphan", id=790, parent=None
            ),
        )
        self.assertEqual(
            build_discord_context_tag(m),
            "[from:alice (987654321)] [thread:#orphan (790) in ?]",
        )

    def test_direct_message(self):
        """A DM is tagged [dm] with no channel name."""
        m = _msg(
            SimpleNamespace(display_name="alice", name="alice_x", id=987654321),
            SimpleNamespace(type=discord.ChannelType.private, name=None, id=111, parent=None),
        )
        self.assertEqual(
            build_discord_context_tag(m),
            "[from:alice (987654321)] [dm]",
        )

    def test_author_falls_back_to_username(self):
        """When display_name is absent, the author's username is used."""
        m = _msg(
            SimpleNamespace(name="bob", id=222),  # no display_name attribute
            SimpleNamespace(type=discord.ChannelType.text, name="random", id=333, parent=None),
        )
        self.assertEqual(
            build_discord_context_tag(m),
            "[from:bob (222)] [channel:#random (333)]",
        )

    def test_author_falls_back_to_question_mark(self):
        """When neither display_name nor name is present, author renders as '?'."""
        m = _msg(
            SimpleNamespace(id=444),  # no display_name, no name
            SimpleNamespace(type=discord.ChannelType.text, name="random", id=333, parent=None),
        )
        self.assertEqual(
            build_discord_context_tag(m),
            "[from:? (444)] [channel:#random (333)]",
        )

    def test_channel_name_falls_back_to_question_mark(self):
        """When a channel has no resolvable name, it renders as #?."""
        m = _msg(
            SimpleNamespace(display_name="alice", name="alice_x", id=987654321),
            SimpleNamespace(type=discord.ChannelType.text, id=333, parent=None),  # no name
        )
        self.assertEqual(
            build_discord_context_tag(m),
            "[from:alice (987654321)] [channel:#? (333)]",
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
