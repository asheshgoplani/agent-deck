"""Routing regression tests for issue #2547.

Before this fix, any message without a `name:` conductor prefix was routed to
`discover_conductors()[0]` — the alphabetically first conductor. The user never
chose it, and which conductor it is silently changes when conductors are added
or renamed. Worse, a reply *inside a thread the bridge itself created for a
conductor* (a NEED alert, a queued or late response, an outbox forward) also
landed on `[0]`: routing ignored `thread_ts` entirely.

The fix resolves in this order — `name:` prefix, thread affinity, the
configured `[conductor.slack] default_conductor` (name -> chosen default,
`""` -> no default, absent -> legacy `[0]` for back-compat), and never falls
back to `[0]` for a *configured but unknown* name. These tests exercise
`resolve_slack_routing` (pure, so each case is exact) and the bounded
thread-affinity store; the original code has no such function, so every test
fails on it by construction.
"""

from __future__ import annotations

from collections import OrderedDict

import bridge


def conductors(*names: str) -> list[dict]:
    return [{"name": n, "profile": "default"} for n in names]


def test_prefix_routes_and_strips_message():
    cs = conductors("alpha", "beta")
    target, msg, reason = bridge.resolve_slack_routing(
        "beta: run the sweep", None, ["alpha", "beta"], cs, None,
    )
    assert (target["name"], msg, reason) == ("beta", "run the sweep", "prefix")


def test_absent_key_keeps_legacy_first_conductor():
    cs = conductors("alpha", "beta")
    target, _, reason = bridge.resolve_slack_routing("status", None, ["alpha", "beta"], cs, None)
    assert (target["name"], reason) == ("alpha", "legacy")


def test_empty_string_means_no_default():
    cs = conductors("alpha", "beta")
    target, _, reason = bridge.resolve_slack_routing("status", None, ["alpha", "beta"], cs, "")
    assert target is None and reason == "no_default"


def test_explicit_name_is_the_default():
    cs = conductors("alpha", "beta")
    target, _, reason = bridge.resolve_slack_routing("status", None, ["alpha", "beta"], cs, "beta")
    assert (target["name"], reason) == ("beta", "default")


def test_unknown_configured_name_never_falls_back_to_first():
    # The old silent surprise: a typo'd default still routed to [0]. Fail
    # loud instead — the caller prompts with the conductor list.
    cs = conductors("alpha", "beta")
    target, _, reason = bridge.resolve_slack_routing("status", None, ["alpha", "beta"], cs, "nope")
    assert target is None and reason == "misconfigured"


def test_no_conductors_is_its_own_reason():
    target, _, reason = bridge.resolve_slack_routing("hi", None, [], [], None)
    assert target is None and reason == "none"


def test_thread_affinity_routes_without_prefix():
    cs = conductors("alpha", "beta")
    affinity: OrderedDict[str, str] = OrderedDict([("1234.0001", "beta")])
    target, _, reason = bridge.resolve_slack_routing(
        "where is this session?", "1234.0001", ["alpha", "beta"], cs, "alpha", affinity,
    )
    assert (target["name"], reason) == ("beta", "affinity")


def test_prefix_wins_over_thread_affinity():
    cs = conductors("alpha", "beta")
    affinity: OrderedDict[str, str] = OrderedDict([("1234.0001", "beta")])
    target, msg, reason = bridge.resolve_slack_routing(
        "alpha: actually this one", "1234.0001", ["alpha", "beta"], cs, "beta", affinity,
    )
    assert (target["name"], msg, reason) == ("alpha", "actually this one", "prefix")


def test_affinity_to_deleted_conductor_falls_through():
    # The conductor a thread pointed at was removed: never crash, never
    # route to a ghost — resolve to the configured default instead.
    cs = conductors("alpha")
    affinity: OrderedDict[str, str] = OrderedDict([("1234.0001", "beta")])
    target, _, reason = bridge.resolve_slack_routing("hi", "1234.0001", ["alpha"], cs, "alpha", affinity)
    assert (target["name"], reason) == ("alpha", "default")


def test_remember_thread_conductor_is_bounded_fifo():
    store: OrderedDict[str, str] = OrderedDict()
    for i in range(bridge._THREAD_AFFINITY_CAP + 5):
        bridge.remember_thread_conductor(f"t{i}", f"c{i}", store)
    assert len(store) == bridge._THREAD_AFFINITY_CAP
    assert "t0" not in store and "t4" not in store  # oldest evicted
    assert store[f"t{bridge._THREAD_AFFINITY_CAP + 4}"] == f"c{bridge._THREAD_AFFINITY_CAP + 4}"


def test_remember_thread_conductor_ignores_missing_parts():
    store: OrderedDict[str, str] = OrderedDict()
    bridge.remember_thread_conductor(None, "c", store)
    bridge.remember_thread_conductor("t", "", store)
    assert len(store) == 0