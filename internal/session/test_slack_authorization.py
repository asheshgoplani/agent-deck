#!/usr/bin/env python3
"""
Test suite for who may use the Slack bot in the conductor bridge.

Exercises slack_access_policy() from conductor_bridge.py: an empty or missing
allowed_user_ids refuses every Slack user unless allow_all_users = true is
set. The handler behavior (refusal reply, channel and team checks) is covered
from Go in conductor_bridge_slack_auth_test.go.
"""

import sys
import types
import unittest
from pathlib import Path

sys.modules.setdefault("toml", types.SimpleNamespace(load=lambda *a, **k: {}))
sys.path.insert(0, str(Path(__file__).parent))

import conductor_bridge as bridge  # noqa: E402


class TestSlackAccessPolicy(unittest.TestCase):
    """Test the [conductor.slack] access policy."""

    def tearDown(self):
        bridge.SLACK_LEGACY_OPEN_WHEN_ALLOWLIST_ABSENT = False

    def test_empty_allowed_users_refuses_all(self):
        self.assertEqual(bridge.slack_access_policy({"allowed_user_ids": []}), ([], False, False))

    def test_missing_allowed_users_refuses_all(self):
        self.assertEqual(bridge.slack_access_policy({}), ([], False, False))

    def test_allow_all_users_opt_in(self):
        self.assertEqual(bridge.slack_access_policy({"allow_all_users": True}), ([], True, False))

    def test_allow_all_users_must_be_boolean_true(self):
        self.assertEqual(bridge.slack_access_policy({"allow_all_users": "yes"}), ([], False, False))

    def test_allowed_users_are_kept(self):
        allowed, allow_all, _ = bridge.slack_access_policy(
            {"allowed_user_ids": ["U12345", " W67890 ", ""]},
        )
        self.assertEqual(allowed, ["U12345", "W67890"])
        self.assertFalse(allow_all)

    def test_legacy_switch_only_opens_tables_without_either_key(self):
        bridge.SLACK_LEGACY_OPEN_WHEN_ALLOWLIST_ABSENT = True
        self.assertEqual(bridge.slack_access_policy({}), ([], False, True))
        self.assertEqual(bridge.slack_access_policy({"allowed_user_ids": []}), ([], False, False))
        self.assertEqual(bridge.slack_access_policy({"allow_all_users": False}), ([], False, False))

    def test_refusal_reply_names_the_user(self):
        text = bridge.SLACK_REFUSAL_REPLY.format(user_id="U99999")
        self.assertIn("Your Slack member ID is U99999.", text)
        self.assertTrue(text.endswith('allowed_user_ids = ["U99999"]'))


if __name__ == "__main__":
    unittest.main(verbosity=2)
