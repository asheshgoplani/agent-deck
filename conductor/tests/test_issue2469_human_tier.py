"""Regression tests for issue #2469 (conductor -> human tier).

The bridge used to forward only heartbeat NEED: lines, with retire counters
held in memory. Now:
  * heartbeat / scanned replies go through `agent-deck conductor tier-filter`
    (urgent now, info queued, retire counts on disk), falling back to the
    in-process filter_need_lines when the CLI is unavailable;
  * a 5 s loop forwards what a conductor queued with `conductor notify`:
    urgent at once as "[<name>] <text>", info as one digest when due or with
    the next urgent, and acks ids only after a channel accepted the message.
"""

from __future__ import annotations

import asyncio
import json
import subprocess
import sys
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent.parent))

import bridge  # noqa: E402  pylint: disable=wrong-import-position

NEED = "NEED: api-fix - staging or prod?"


def _ok(payload) -> subprocess.CompletedProcess:
    return subprocess.CompletedProcess(["agent-deck"], 0, json.dumps(payload), "")


def _fail() -> subprocess.CompletedProcess:
    return subprocess.CompletedProcess(["agent-deck"], 1, "", "not found")


class FakeCLI:
    """Stands in for run_cli: answers outbox / tier-filter and records acks."""

    def __init__(self, items=None, tier_filter=None, ack_ok=True):
        self.items = items or []
        self.tier_filter = tier_filter
        self.ack_ok = ack_ok
        self.calls: list[tuple] = []
        self.acked: list[str] = []

    def __call__(self, *args, profile=None, timeout=120, input_text=None):
        self.calls.append(args)
        if args[:2] == ("conductor", "outbox") and "--ack" in args:
            if not self.ack_ok:
                return _fail()
            ids = [args[i + 1] for i, a in enumerate(args) if a == "--ack"]
            self.acked += ids
            return _ok({"acked": len(ids)})
        if args[:2] == ("conductor", "outbox"):
            return _ok(self.items)
        if args[:2] == ("conductor", "tier-filter"):
            return _ok(self.tier_filter) if self.tier_filter is not None else _fail()
        return _fail()


def _run(coro):
    return asyncio.run(coro)


class TestTierFilterReply2469:
    def test_cli_result_wins_and_digest_is_returned(self):
        cli = FakeCLI(tier_filter={
            "send_now": [NEED], "queued": 1, "digest_due": True,
            "digest": [{"id": "i1", "tier": "info", "text": "lane C merged"}],
        })
        with mock.patch.object(bridge, "run_cli", cli):
            out = bridge.tier_filter_reply("ops", "default", "[STATUS] x\n" + NEED + "\n[info] lane C merged", {})
        assert out["lines"] == [NEED]
        assert [d["id"] for d in out["digest"]] == ["i1"]
        # The fallback counts stay current even when the CLI answered.
        assert out["counts"] == {NEED: 1}

    def test_info_lines_are_not_forwarded_by_the_cli_path(self):
        cli = FakeCLI(tier_filter={"send_now": [], "queued": 1, "digest_due": False, "digest": []})
        with mock.patch.object(bridge, "run_cli", cli):
            out = bridge.tier_filter_reply("ops", "default", "[info] progress only", {})
        assert out["lines"] == [] and out["digest"] == []

    def test_cli_failure_falls_back_to_filter_need_lines(self):
        with mock.patch.object(bridge, "run_cli", FakeCLI()):
            out = bridge.tier_filter_reply("ops", "default", NEED, {NEED: 2}, threshold=3)
        assert out["lines"] == [f"STILL BLOCKED (3 cycles, no reply): {NEED}"]


class TestHumanOutboxMessage2469:
    def test_urgent_now_info_waits(self):
        items = [
            {"id": "u1", "tier": "urgent", "text": "prod is down"},
            {"id": "i1", "tier": "info", "text": "lane C merged"},
        ]
        msg, ids = bridge.build_human_outbox_message("ops", items[1:], digest_due=False)
        assert (msg, ids) == ("", [])
        msg, ids = bridge.build_human_outbox_message("ops", items, digest_due=False)
        assert msg.startswith("[ops] prod is down")
        assert "Digest (1 update):\n- lane C merged" in msg  # rides with the urgent
        assert ids == ["u1", "i1"]

    def test_digest_alone_when_due(self):
        items = [{"id": "i1", "tier": "info", "text": "a"}, {"id": "i2", "tier": "info", "text": "b"}]
        msg, ids = bridge.build_human_outbox_message("ops", items, digest_due=True)
        assert msg == "[ops] Digest (2 updates):\n- a\n- b"
        assert ids == ["i1", "i2"]


class TestHumanOutboxCycle2469:
    conductors = [{"name": "ops", "profile": "work"}]

    def _cycle(self, cli, delivered=True, state=None, now=1000.0):
        sent: list[str] = []

        async def deliver(text):
            sent.append(text)
            return delivered

        with mock.patch.object(bridge, "run_cli", cli), \
             mock.patch.object(bridge, "_human_outbox_signature", return_value=(1, 1)):
            _run(bridge.human_outbox_cycle(self.conductors, {} if state is None else state, deliver, now=now))
        return sent

    def test_urgent_forwarded_then_acked(self):
        cli = FakeCLI(items=[{"id": "u1", "tier": "urgent", "text": "<b>prod</b> down"}])
        sent = self._cycle(cli)
        assert sent == ["[ops] <b>prod</b> down"]  # HTML escaping happens in _deliver_need_alert
        assert cli.acked == ["u1"]

    def test_failed_send_keeps_items_queued(self):
        cli = FakeCLI(items=[{"id": "u1", "tier": "urgent", "text": "prod down"}])
        self._cycle(cli, delivered=False)
        assert cli.acked == []

    def test_info_held_until_digest_due(self):
        items = [{"id": "i1", "tier": "info", "text": "progress"}]
        cli = FakeCLI(items=items, tier_filter={"send_now": [], "queued": 0, "digest_due": False, "digest": []})
        assert self._cycle(cli) == [] and cli.acked == []
        cli = FakeCLI(items=items, tier_filter={"send_now": [], "queued": 0, "digest_due": True, "digest": items})
        assert self._cycle(cli) == ["[ops] Digest (1 update):\n- progress"]
        assert cli.acked == ["i1"]

    def test_unchanged_outbox_skips_cli_until_idle_poll(self):
        state: dict = {}
        cli = FakeCLI(items=[])
        self._cycle(cli, state=state, now=1000.0)
        self._cycle(cli, state=state, now=1005.0)
        assert len(cli.calls) == 1
        self._cycle(cli, state=state, now=1000.0 + bridge.HUMAN_OUTBOX_IDLE_POLL_SECONDS)
        assert len(cli.calls) == 2


class TestNeedScanUsesTierFilter2469:
    def test_digest_rides_scan_alert_and_is_acked(self):
        slack_app = mock.MagicMock()
        slack_app.client.chat_postMessage = mock.AsyncMock()
        cli = FakeCLI(tier_filter={
            "send_now": [NEED], "queued": 0, "digest_due": True,
            "digest": [{"id": "i9", "tier": "info", "text": "docs merged"}],
        })
        with mock.patch.object(bridge, "discover_conductors",
                               return_value=[{"name": "ops", "profile": "default", "heartbeat_enabled": True}]), \
             mock.patch.object(bridge, "get_session_output", return_value=NEED), \
             mock.patch.object(bridge, "run_cli", cli):
            _run(bridge.need_scan_cycle(
                {"heartbeat_interval": 15, "telegram": {"configured": False, "user_id": None}},
                {}, lambda: None, slack_app=slack_app, slack_channel_id="C1",
            ))
        text = slack_app.client.chat_postMessage.await_args.kwargs["text"]
        assert text == "Conductor alert:\n" + NEED + "\n\nDigest (1 update):\n- docs merged"
        assert cli.acked == ["i9"]
