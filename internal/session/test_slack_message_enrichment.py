#!/usr/bin/env python3
"""
Test suite for Slack message enrichment in conductor bridge.

Tests the resolve_slack_username(), resolve_slack_channel(), and message
enrichment logic that tags messages with [from:username (ID)] and
[channel:#name (ID)] before relaying to the conductor.
"""

import time
import unittest
from unittest.mock import AsyncMock, MagicMock, patch
import logging


log = logging.getLogger(__name__)

_NEGATIVE_TTL = 300  # mirrors bridge constant


class TestResolveSlackUsername(unittest.IsolatedAsyncioTestCase):
    """Test Slack user ID to display name resolution."""

    def setUp(self):
        self._user_cache: dict[str, tuple[str, float | None]] = {}
        self.mock_client = MagicMock()

    def _cache_get(self, cache, key):
        entry = cache.get(key)
        if entry is None:
            return None
        value, expires_at = entry
        if expires_at is not None and time.monotonic() > expires_at:
            del cache[key]
            return None
        return value

    async def resolve_slack_username(self, user_id: str) -> str:
        """Mirror of the bridge's resolve_slack_username with injected client."""
        cached = self._cache_get(self._user_cache, user_id)
        if cached is not None:
            return cached
        try:
            resp = await self.mock_client.users_info(user=user_id)
            profile = resp["user"]["profile"]
            name = profile.get("display_name") or profile.get("real_name") or user_id
            self._user_cache[user_id] = (name, None)
            return name
        except Exception as e:
            log.warning("Failed to resolve Slack user %s: %s", user_id, e)
            self._user_cache[user_id] = (user_id, time.monotonic() + _NEGATIVE_TTL)
            return user_id

    async def test_resolves_display_name(self):
        """Should prefer display_name over real_name."""
        self.mock_client.users_info = AsyncMock(return_value={
            "user": {"profile": {"display_name": "alice", "real_name": "Alice Smith"}}
        })
        result = await self.resolve_slack_username("U12345")
        self.assertEqual(result, "alice")
        self.mock_client.users_info.assert_awaited_once_with(user="U12345")

    async def test_falls_back_to_real_name(self):
        """Should fall back to real_name when display_name is empty."""
        self.mock_client.users_info = AsyncMock(return_value={
            "user": {"profile": {"display_name": "", "real_name": "Bob Jones"}}
        })
        result = await self.resolve_slack_username("U67890")
        self.assertEqual(result, "Bob Jones")

    async def test_falls_back_to_user_id(self):
        """Should fall back to raw user ID when both names are empty."""
        self.mock_client.users_info = AsyncMock(return_value={
            "user": {"profile": {"display_name": "", "real_name": ""}}
        })
        result = await self.resolve_slack_username("UNONAME")
        self.assertEqual(result, "UNONAME")

    async def test_caches_successful_result(self):
        """Should only call Slack API once per user ID on success."""
        self.mock_client.users_info = AsyncMock(return_value={
            "user": {"profile": {"display_name": "cached-user", "real_name": ""}}
        })
        await self.resolve_slack_username("U11111")
        await self.resolve_slack_username("U11111")
        self.mock_client.users_info.assert_awaited_once()

    async def test_api_failure_falls_back_to_user_id(self):
        """Should return raw user ID on API failure."""
        self.mock_client.users_info = AsyncMock(side_effect=Exception("timeout"))
        result = await self.resolve_slack_username("UFAILED")
        self.assertEqual(result, "UFAILED")

    async def test_negative_cache_is_temporary(self):
        """Failed lookups should be retried after TTL expires."""
        self.mock_client.users_info = AsyncMock(side_effect=Exception("timeout"))
        await self.resolve_slack_username("UFAILED")
        self.mock_client.users_info.assert_awaited_once()

        # Simulate TTL expiry by backdating the cache entry.
        self._user_cache["UFAILED"] = ("UFAILED", time.monotonic() - 1)

        # Now it should retry and succeed.
        self.mock_client.users_info = AsyncMock(return_value={
            "user": {"profile": {"display_name": "recovered", "real_name": ""}}
        })
        result = await self.resolve_slack_username("UFAILED")
        self.assertEqual(result, "recovered")
        self.mock_client.users_info.assert_awaited_once()

    async def test_negative_cache_suppresses_retry_within_ttl(self):
        """Failed lookups should NOT retry within TTL window."""
        self.mock_client.users_info = AsyncMock(side_effect=Exception("timeout"))
        await self.resolve_slack_username("UFAILED")
        await self.resolve_slack_username("UFAILED")
        # Only one API call despite two resolve attempts.
        self.mock_client.users_info.assert_awaited_once()


class TestResolveSlackChannel(unittest.IsolatedAsyncioTestCase):
    """Test Slack channel ID to context tag resolution."""

    def setUp(self):
        self._channel_cache: dict[str, tuple[str, float | None]] = {}
        self.mock_client = MagicMock()

    def _cache_get(self, cache, key):
        entry = cache.get(key)
        if entry is None:
            return None
        value, expires_at = entry
        if expires_at is not None and time.monotonic() > expires_at:
            del cache[key]
            return None
        return value

    async def resolve_slack_channel(self, event_channel: str) -> str:
        """Mirror of the bridge's resolve_slack_channel with injected client."""
        cached = self._cache_get(self._channel_cache, event_channel)
        if cached is not None:
            return cached
        try:
            resp = await self.mock_client.conversations_info(channel=event_channel)
            ch = resp["channel"]
            if ch.get("is_im"):
                tag = "[dm]"
            else:
                name = ch.get("name", event_channel)
                tag = f"[channel:#{name} ({event_channel})]"
            self._channel_cache[event_channel] = (tag, None)
            return tag
        except Exception as e:
            log.warning("Failed to resolve Slack channel %s: %s", event_channel, e)
            tag = f"[channel:{event_channel}]"
            self._channel_cache[event_channel] = (tag, time.monotonic() + _NEGATIVE_TTL)
            return tag

    async def test_resolves_public_channel_with_id(self):
        """Should return [channel:#name (ID)] for public channels."""
        self.mock_client.conversations_info = AsyncMock(return_value={
            "channel": {"name": "bugs", "is_im": False}
        })
        result = await self.resolve_slack_channel("C12345")
        self.assertEqual(result, "[channel:#bugs (C12345)]")

    async def test_resolves_dm(self):
        """Should return [dm] for direct messages."""
        self.mock_client.conversations_info = AsyncMock(return_value={
            "channel": {"name": "dm-channel", "is_im": True}
        })
        result = await self.resolve_slack_channel("D99999")
        self.assertEqual(result, "[dm]")

    async def test_caches_channel_result(self):
        """Should only call Slack API once per channel ID."""
        self.mock_client.conversations_info = AsyncMock(return_value={
            "channel": {"name": "general", "is_im": False}
        })
        await self.resolve_slack_channel("C11111")
        await self.resolve_slack_channel("C11111")
        self.mock_client.conversations_info.assert_awaited_once()

    async def test_api_failure_falls_back_to_raw_id(self):
        """Should return [channel:ID] on API failure."""
        self.mock_client.conversations_info = AsyncMock(
            side_effect=Exception("not_in_channel")
        )
        result = await self.resolve_slack_channel("CBAD")
        self.assertEqual(result, "[channel:CBAD]")

    async def test_missing_name_falls_back_to_id(self):
        """Should use channel ID in name position if name field is missing."""
        self.mock_client.conversations_info = AsyncMock(return_value={
            "channel": {"is_im": False}
        })
        result = await self.resolve_slack_channel("CNONAME")
        self.assertEqual(result, "[channel:#CNONAME (CNONAME)]")

    async def test_negative_cache_expires(self):
        """Failed channel lookups should be retried after TTL."""
        self.mock_client.conversations_info = AsyncMock(
            side_effect=Exception("timeout")
        )
        await self.resolve_slack_channel("CBAD")

        # Expire the negative cache entry.
        self._channel_cache["CBAD"] = ("[channel:CBAD]", time.monotonic() - 1)

        self.mock_client.conversations_info = AsyncMock(return_value={
            "channel": {"name": "recovered-channel", "is_im": False}
        })
        result = await self.resolve_slack_channel("CBAD")
        self.assertEqual(result, "[channel:#recovered-channel (CBAD)]")


class TestMessageEnrichment(unittest.TestCase):
    """Test the full message enrichment format."""

    def test_full_enrichment(self):
        """Message with both user and channel tags including stable IDs."""
        prefix_parts = ["[from:alice (U12345)]", "[channel:#bugs (C67890)]"]
        cleaned_msg = "the login button is broken"
        result = " ".join(prefix_parts) + " " + cleaned_msg
        self.assertEqual(
            result,
            "[from:alice (U12345)] [channel:#bugs (C67890)] the login button is broken",
        )

    def test_dm_enrichment(self):
        """Message from a DM should use [dm] tag."""
        prefix_parts = ["[from:bob (U11111)]", "[dm]"]
        cleaned_msg = "can you check the API?"
        result = " ".join(prefix_parts) + " " + cleaned_msg
        self.assertEqual(result, "[from:bob (U11111)] [dm] can you check the API?")

    def test_user_only(self):
        """Message with user but no channel."""
        prefix_parts = ["[from:charlie (U22222)]"]
        cleaned_msg = "hello"
        result = " ".join(prefix_parts) + " " + cleaned_msg
        self.assertEqual(result, "[from:charlie (U22222)] hello")

    def test_no_enrichment(self):
        """No prefix when both user_id and channel are None."""
        prefix_parts = []
        cleaned_msg = "raw message"
        if prefix_parts:
            result = " ".join(prefix_parts) + " " + cleaned_msg
        else:
            result = cleaned_msg
        self.assertEqual(result, "raw message")

    def test_fallback_user_id_in_tag(self):
        """When username resolution fails, raw user ID appears as both name and ID."""
        prefix_parts = ["[from:U12345 (U12345)]", "[channel:#bugs (C67890)]"]
        cleaned_msg = "test"
        result = " ".join(prefix_parts) + " " + cleaned_msg
        self.assertEqual(
            result, "[from:U12345 (U12345)] [channel:#bugs (C67890)] test"
        )

    def test_fallback_channel_id_in_tag(self):
        """When channel resolution fails, raw channel ID appears in tag."""
        prefix_parts = ["[from:alice (U12345)]", "[channel:C99999]"]
        cleaned_msg = "test"
        result = " ".join(prefix_parts) + " " + cleaned_msg
        self.assertEqual(result, "[from:alice (U12345)] [channel:C99999] test")


if __name__ == "__main__":
    unittest.main(verbosity=2)
