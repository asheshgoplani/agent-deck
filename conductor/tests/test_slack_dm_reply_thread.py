"""Which thread the Slack bridge replies in.

In a channel the bridge threads every reply under the user's message to keep
the channel clean. In a direct message there is no channel to keep clean, so
a top-level DM is answered top-level, and a reply inside a DM thread stays in
that thread.
"""

from __future__ import annotations

import bridge


def test_top_level_dm_is_answered_top_level():
    event = {"channel_type": "im", "ts": "1.1"}
    assert bridge.slack_reply_thread_ts(event) is None


def test_reply_inside_a_dm_thread_stays_in_the_thread():
    event = {"channel_type": "im", "ts": "1.2", "thread_ts": "1.1"}
    assert bridge.slack_reply_thread_ts(event) == "1.1"


def test_channel_message_is_answered_in_a_thread_under_it():
    event = {"channel_type": "channel", "ts": "2.1"}
    assert bridge.slack_reply_thread_ts(event) == "2.1"


def test_channel_thread_reply_stays_in_the_thread():
    event = {"channel_type": "channel", "ts": "2.2", "thread_ts": "2.1"}
    assert bridge.slack_reply_thread_ts(event) == "2.1"


def test_event_without_channel_type_keeps_threading():
    # app_mention events carry no channel_type; they keep the old behavior.
    assert bridge.slack_reply_thread_ts({"ts": "3.1"}) == "3.1"
