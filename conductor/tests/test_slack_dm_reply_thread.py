"""Which thread the Slack bridge replies in.

In a channel the bridge threads every reply under the user's message to keep
the channel clean. In a direct message there is no channel to keep clean, so
a top-level DM is answered top-level, and a reply inside a DM thread stays in
that thread. Group DMs (mpim) are answered in a thread, like channels.
"""

from __future__ import annotations

from collections import OrderedDict

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


# Routing memory. A top-level DM is answered top-level, so there is no thread
# to tie a follow-up to; routing remembers the conductor per DM channel
# instead. Channels and group DMs (mpim) keep the per-thread key.


def test_top_level_dm_is_remembered_per_dm_channel():
    event = {"channel_type": "im", "channel": "D1", "ts": "1.1"}
    assert bridge.slack_affinity_key(event) == "im:D1"


def test_reply_inside_a_dm_thread_is_remembered_per_thread():
    event = {"channel_type": "im", "channel": "D1", "ts": "1.2", "thread_ts": "1.1"}
    assert bridge.slack_affinity_key(event) == "1.1"


def test_channel_and_group_dm_are_remembered_per_thread():
    for channel_type in ("channel", "mpim"):
        top = {"channel_type": channel_type, "channel": "C1", "ts": "2.1"}
        reply = dict(top, ts="2.2", thread_ts="2.1")
        assert bridge.slack_reply_thread_ts(top) == "2.1"
        assert bridge.slack_affinity_key(top) == "2.1"
        assert bridge.slack_affinity_key(reply) == "2.1"


def test_unprefixed_dm_follow_up_goes_to_the_last_addressed_conductor():
    cs = [{"name": n, "profile": "default"} for n in ("alpha", "beta")]
    names = ["alpha", "beta"]
    affinity: OrderedDict[str, str] = OrderedDict()
    key = bridge.slack_affinity_key({"channel_type": "im", "channel": "D1", "ts": "1.1"})

    def route(text):
        target, _, reason = bridge.resolve_slack_routing(text, key, names, cs, "alpha", affinity)
        bridge.remember_thread_conductor(key, target["name"], affinity)
        return target["name"], reason

    assert route("beta: status") == ("beta", "prefix")
    assert route("and the logs?") == ("beta", "affinity")
    assert route("alpha: status") == ("alpha", "prefix")
    assert route("and the logs?") == ("alpha", "affinity")
