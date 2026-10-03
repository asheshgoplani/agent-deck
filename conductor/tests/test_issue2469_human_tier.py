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
import hashlib
import json
import subprocess
import sys
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

import bridge  # noqa: E402  pylint: disable=wrong-import-position


@pytest.fixture(autouse=True)
def _fresh_send_locks():
    # Each test runs its own event loop; a lock is bound to the loop it waited in.
    bridge._HUMAN_SEND_LOCKS.clear()
    yield
    bridge._HUMAN_SEND_LOCKS.clear()

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
        self.reply_acks: list[str] = []

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
        if args[:2] == ("conductor", "tier-filter") and "--ack" in args:
            self.reply_acks.append(args[args.index("--ack") + 1])
            return _ok({"committed": True})
        if args[:2] == ("conductor", "tier-filter"):
            return _ok(self.tier_filter) if self.tier_filter is not None else _fail()
        return _fail()

    def filter_reply_ids(self) -> list[str]:
        """The --reply-id of every tier-filter (not --ack) call, in order."""
        return [c[c.index("--reply-id") + 1] for c in self.calls
                if c[:2] == ("conductor", "tier-filter") and "--ack" not in c and "--reply-id" in c]


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


class TestHumanDigestBatches2469:
    def test_batches_cap_items_and_size(self):
        items = [{"id": f"i{n}", "tier": "info", "text": "x"} for n in range(bridge.HUMAN_DIGEST_MAX_ITEMS + 1)]
        batches = bridge.human_digest_batches(items)
        assert [len(b) for b in batches] == [bridge.HUMAN_DIGEST_MAX_ITEMS, 1]
        big = [{"id": f"b{n}", "tier": "info", "text": "y" * 3000} for n in range(3)]
        assert [len(b) for b in bridge.human_digest_batches(big)] == [1, 1, 1]

    def test_digest_format(self):
        items = [{"id": "i1", "tier": "info", "text": "a"}, {"id": "i2", "tier": "info", "text": "b"}]
        assert bridge.format_human_digest(items) == "Digest (2 updates):\n- a\n- b"


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

    def test_urgent_items_go_one_message_each_and_info_follows(self):
        cli = FakeCLI(items=[
            {"id": "u1", "tier": "urgent", "text": "prod is down"},
            {"id": "i1", "tier": "info", "text": "lane C merged"},
            {"id": "u2", "tier": "urgent", "text": "need a key"},
        ])
        sent = self._cycle(cli)
        assert sent == ["[ops] prod is down", "[ops] need a key", "[ops] Digest (1 update):\n- lane C merged"]
        assert cli.acked == ["u1", "u2", "i1"]

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
        texts = [c.kwargs["text"] for c in slack_app.client.chat_postMessage.await_args_list]
        # The digest is its own message: one the platform refuses never holds back the alert.
        assert texts == ["Conductor alert:\n" + NEED, "Digest (1 update):\n- docs merged"]
        assert cli.acked == ["i9"]


SCAN_CONFIG = {"heartbeat_interval": 15, "telegram": {"configured": False, "user_id": None}}
OPS = [{"name": "ops", "profile": "default", "heartbeat_enabled": True}]


class TestNeedScanRetryIsOneCycle2469:
    def test_failed_sends_retry_with_the_same_reply_id(self):
        """Every retry of one reply carries the same --reply-id, and only the
        delivered attempt is acked, so the Go ledger never advances toward
        STILL BLOCKED/drop for an undelivered send."""
        slack_app = mock.MagicMock()
        slack_app.client.chat_postMessage = mock.AsyncMock(side_effect=RuntimeError("slack down"))
        cli = FakeCLI(tier_filter={"send_now": [NEED], "queued": 0, "digest_due": False, "digest": []})
        state: dict = {}
        with mock.patch.object(bridge, "discover_conductors", return_value=OPS), \
             mock.patch.object(bridge, "get_session_output", return_value=NEED), \
             mock.patch.object(bridge, "run_cli", cli):
            for _ in range(3):
                _run(bridge.need_scan_cycle(SCAN_CONFIG, state, lambda: None,
                                            slack_app=slack_app, slack_channel_id="C1"))
            assert state == {}  # nothing delivered: the reply stays due
            slack_app.client.chat_postMessage = mock.AsyncMock()
            _run(bridge.need_scan_cycle(SCAN_CONFIG, state, lambda: None,
                                        slack_app=slack_app, slack_channel_id="C1"))
        rid = hashlib.sha256(NEED.encode("utf-8")).hexdigest()
        assert cli.filter_reply_ids() == [rid] * 4
        assert slack_app.client.chat_postMessage.await_args.kwargs["text"] == "Conductor alert:\n" + NEED
        assert state["ops"]["reply"] == rid
        # Only the delivered attempt commits the retire count.
        assert cli.reply_acks == [rid]

    def test_heartbeat_reply_ids_are_fresh_per_tick(self):
        assert bridge.heartbeat_reply_id("ops", NEED) != bridge.heartbeat_reply_id("ops", NEED)


class TestRetireAdvancesOnlyWhenDelivered2469:
    """Review r2 #1: across an outage every heartbeat is a NEW reply. The
    bridge must commit a reply's retire counts (tier-filter --ack) only after
    a channel accepted it, never for an undelivered one."""

    def test_scan_acks_only_the_delivered_reply_across_distinct_replies(self):
        replies = [f"[STATUS] heartbeat {n}\n{NEED}" for n in range(1, 5)]
        slack_app = mock.MagicMock()
        slack_app.client.chat_postMessage = mock.AsyncMock(side_effect=RuntimeError("invalid_auth"))
        cli = FakeCLI(tier_filter={"send_now": [NEED], "queued": 0, "digest_due": False, "digest": []})
        state: dict = {}
        with mock.patch.object(bridge, "discover_conductors", return_value=OPS), \
             mock.patch.object(bridge, "get_session_output", side_effect=replies), \
             mock.patch.object(bridge, "run_cli", cli):
            for n in range(4):
                if n == 3:
                    slack_app.client.chat_postMessage = mock.AsyncMock()  # token fixed
                _run(bridge.need_scan_cycle(SCAN_CONFIG, state, lambda: None,
                                            slack_app=slack_app, slack_channel_id="C1"))
                if n < 3:
                    assert cli.reply_acks == [] and state == {}, (n, cli.reply_acks, state)
        rids = [hashlib.sha256(r.encode("utf-8")).hexdigest() for r in replies]
        assert cli.filter_reply_ids() == rids
        assert cli.reply_acks == [rids[3]]
        assert slack_app.client.chat_postMessage.await_args.kwargs["text"] == "Conductor alert:\n" + NEED

    def test_deliver_tiered_reply_acks_after_delivery_only(self):
        filtered = {"lines": [NEED], "digest": [{"id": "i1", "text": "x"}], "counts": {}, "reply_id": "r1"}

        async def go(ok):
            async def deliver(_text):
                return ok
            return await bridge.deliver_tiered_reply(
                asyncio.get_running_loop(), "ops", "default", filtered, "", deliver)

        cli = FakeCLI()
        with mock.patch.object(bridge, "run_cli", cli):
            assert _run(go(False)) is False
            assert cli.calls == []  # nothing acked for a failed send
            assert _run(go(True)) is True
        assert cli.acked == ["i1"] and cli.reply_acks == ["r1"]

        # Fallback result (CLI unavailable): no reply id, nothing to commit.
        cli = FakeCLI()
        with mock.patch.object(bridge, "run_cli", cli):
            filtered = dict(filtered, digest=[], reply_id=None)
            assert _run(go(True)) is True
        assert cli.calls == []

    def test_heartbeat_tick_acks_only_delivered_replies(self, monkeypatch, tmp_path):
        """Drive the bridge-tick heartbeat_loop: two ticks while Slack is
        down, then one that lands. Each tick filters under a fresh reply id,
        and only the delivered tick's id is acked."""

        class _Stop(Exception):
            pass

        ticks = iter([1, 2, 3])
        tick_no = {"n": 0}
        real_sleep = asyncio.sleep

        async def fake_sleep(_seconds):
            try:
                tick_no["n"] = next(ticks)
            except StopIteration:
                raise _Stop()
            await real_sleep(0)

        def sessions(_profile):  # a new waiting session each tick: never "unchanged"
            return [{"title": f"w{tick_no['n']}", "status": "waiting", "group": "ops", "path": "/p"}]

        async def running(_name, _profile):
            return True

        posts: list[str] = []

        async def post(channel, text):
            posts.append(text)
            if tick_no["n"] < 3:
                raise RuntimeError("invalid_auth")

        slack_app = mock.MagicMock()
        slack_app.client.chat_postMessage = mock.AsyncMock(side_effect=post)
        cli = FakeCLI(tier_filter={"send_now": [NEED], "queued": 0, "digest_due": False, "digest": []})
        monkeypatch.setattr(bridge, "CONDUCTOR_DIR", tmp_path)
        monkeypatch.setattr(bridge, "resolve_data_dir", lambda *_m: tmp_path)
        monkeypatch.setattr(bridge, "_os_heartbeat_daemon_installed", lambda: False)
        monkeypatch.setattr(bridge.asyncio, "sleep", fake_sleep)
        monkeypatch.setattr(bridge, "discover_conductors", lambda: [{"name": "ops", "profile": "default"}])
        monkeypatch.setattr(bridge, "get_sessions_list", sessions)
        monkeypatch.setattr(bridge, "invoke_hook", lambda *_a, **_k: None)
        monkeypatch.setattr(bridge, "ensure_conductor_running", running)
        monkeypatch.setattr(bridge, "get_session_status", lambda *_a, **_k: "idle")
        monkeypatch.setattr(bridge, "hook_driven_interactive", lambda *_a, **_k: (False, True))
        monkeypatch.setattr(bridge, "capture_pane", lambda *_a, **_k: "")
        monkeypatch.setattr(bridge, "send_to_conductor", lambda *_a, **_k: (True, "[STATUS] x\n" + NEED, None))
        monkeypatch.setattr(bridge, "run_cli", cli)
        with pytest.raises(_Stop):
            _run(bridge.heartbeat_loop(
                {"heartbeat_interval": 1, "telegram": {"configured": False}},
                slack_app=slack_app, slack_channel_id="C1",
            ))
        rids = cli.filter_reply_ids()
        assert len(posts) == 3 and len(rids) == 3 and len(set(rids)) == 3, (posts, rids)
        assert cli.reply_acks == [rids[2]]


class StatefulCLI:
    """A tiny in-memory outbox behind run_cli: list, ack, and a tier-filter
    whose digest is the unacked info items whenever there is an urgent line."""

    def __init__(self, items):
        self.items = [dict(i, acked=False) for i in items]

    def __call__(self, *args, profile=None, timeout=120, input_text=None):
        pending = [i for i in self.items if not i["acked"]]
        if args[:2] == ("conductor", "outbox") and "--ack" in args:
            ids = {args[i + 1] for i, a in enumerate(args) if a == "--ack"}
            for i in self.items:
                i["acked"] = i["acked"] or i["id"] in ids
            return _ok({"acked": len(ids)})
        if args[:2] == ("conductor", "outbox"):
            return _ok(pending)
        if args[:2] == ("conductor", "tier-filter"):
            send = [l for l in (input_text or "").splitlines() if l.startswith("NEED:")]
            info = [i for i in pending if i["tier"] == "info"]
            return _ok({"send_now": send, "queued": 0, "digest_due": bool(send and info),
                        "digest": info if send else []})
        return _fail()


class TestOneSenderPerConductor2469:
    def test_outbox_loop_and_scan_never_send_the_same_digest_twice(self):
        cli = StatefulCLI([
            {"id": "u1", "tier": "urgent", "text": "prod is down"},
            {"id": "i1", "tier": "info", "text": "lane C merged"},
        ])
        sent: list[str] = []

        async def slow_send(text):
            await asyncio.sleep(0.2)  # a slow platform: the other path runs meanwhile
            sent.append(text)
            return True

        async def post(channel, text):
            return await slow_send(text)

        slack_app = mock.MagicMock()
        slack_app.client.chat_postMessage = mock.AsyncMock(side_effect=post)

        async def both():
            await asyncio.gather(
                bridge.human_outbox_cycle([{"name": "ops", "profile": "default"}], {}, slow_send, now=1.0),
                bridge.need_scan_cycle(SCAN_CONFIG, {}, lambda: None,
                                       slack_app=slack_app, slack_channel_id="C1"),
            )

        with mock.patch.object(bridge, "discover_conductors", return_value=OPS), \
             mock.patch.object(bridge, "get_session_output", return_value="NEED: dup needs a key"), \
             mock.patch.object(bridge, "_human_outbox_signature", return_value=(1, 1)), \
             mock.patch.object(bridge, "run_cli", cli):
            _run(both())
        assert len(sent) == 3, sent  # the alert, the urgent item, one digest
        assert sum(t.count("lane C merged") for t in sent) == 1, sent
        assert all(i["acked"] for i in cli.items)


# ---------------------------------------------------------------------------
# Review r3 (HIGH): one message Telegram refuses must not block the queue.
# ---------------------------------------------------------------------------

UNBALANCED = "ran tests for **all *.go** files and *.ts"


class StrictTelegram:
    """A fake Bot that rejects HTML whose b/i/code tags are not properly
    nested, the way Telegram answers "can't parse entities"."""

    def __init__(self):
        self.messages: list[tuple[str, object]] = []

    async def send_message(self, chat_id, text, parse_mode=None):
        if parse_mode == "HTML":
            stack = []
            for m in __import__("re").finditer(r"<(/?)(b|i|code)>", text):
                if not m.group(1):
                    stack.append(m.group(2))
                elif not stack or stack.pop() != m.group(2):
                    raise RuntimeError(
                        "Telegram server says - Bad Request: can't parse entities: "
                        "Unmatched end tag at byte offset 18")
            if stack:
                raise RuntimeError("Bad Request: can't parse entities: Can't find end tag corresponding to start tag")
        self.messages.append((text, parse_mode))


class TestRefusedMessageNeverBlocks2469:
    def test_unbalanced_markdown_reaches_telegram_as_plain_text(self):
        assert bridge.md_to_tg_html(UNBALANCED).count("</b>") == 1  # mis-nested HTML
        bot = StrictTelegram()
        ok = _run(bridge._deliver_need_alert(UNBALANCED, 42, bot, None, None, None, None))
        assert ok is True
        assert bot.messages == [("ran tests for all .go files and .ts", None)]

    def test_other_telegram_errors_still_fail_the_send(self):
        bot = mock.MagicMock()
        bot.send_message = mock.AsyncMock(side_effect=RuntimeError("Forbidden: bot was blocked by the user"))
        assert _run(bridge._deliver_need_alert("NEED: x", 42, bot, None, None, None, None)) is False
        assert bot.send_message.await_count == 1

    def test_need_alert_with_unbalanced_digest_reaches_a_telegram_only_human(self):
        """E3: an info item queued with markdown Telegram cannot parse rode
        along with every NEED alert and blocked it forever."""
        bot = StrictTelegram()
        cli = StatefulCLI([{"id": "i1", "tier": "info", "text": UNBALANCED}])
        config = {"heartbeat_interval": 15, "telegram": {"configured": True, "user_id": 42}}
        with mock.patch.object(bridge, "discover_conductors", return_value=OPS), \
             mock.patch.object(bridge, "get_session_output", return_value=NEED), \
             mock.patch.object(bridge, "run_cli", cli):
            _run(bridge.need_scan_cycle(config, {}, lambda: None, telegram_bot=bot))
        texts = [t for t, _ in bot.messages]
        assert texts[0] == "Conductor alert:\n" + bridge.md_to_tg_html(NEED)
        assert texts[1] == "Digest (1 update):\n- " + bridge.tg_html_to_plain(bridge.md_to_tg_html(UNBALANCED))
        assert all(i["acked"] for i in cli.items)

    def test_refused_outbox_item_does_not_hold_back_the_next(self):
        """E4: an urgent item no channel accepts must not keep the next urgent
        item (prod is down) from reaching the human."""
        cli = FakeCLI(items=[
            {"id": "u1", "tier": "urgent", "text": "this one is refused"},
            {"id": "u2", "tier": "urgent", "text": "prod database is down, need your call"},
        ])
        sent: list[str] = []

        async def deliver(text):
            if "refused" in text:
                return False
            sent.append(text)
            return True

        with mock.patch.object(bridge, "run_cli", cli), \
             mock.patch.object(bridge, "_human_outbox_signature", return_value=(1, 1)):
            _run(bridge.human_outbox_cycle([{"name": "ops", "profile": "default"}], {}, deliver, now=1.0))
        assert sent == ["[ops] prod database is down, need your call"]
        assert cli.acked == ["u2"]

    def test_unbalanced_outbox_items_reach_telegram_end_to_end(self):
        bot = StrictTelegram()
        cli = StatefulCLI([
            {"id": "u1", "tier": "urgent", "text": UNBALANCED},
            {"id": "u2", "tier": "urgent", "text": "prod database is down, need your call"},
        ])

        async def deliver(text):
            return await bridge._deliver_need_alert(text, 42, bot, None, None, None, None)

        with mock.patch.object(bridge, "run_cli", cli), \
             mock.patch.object(bridge, "_human_outbox_signature", return_value=(1, 1)):
            _run(bridge.human_outbox_cycle([{"name": "ops", "profile": "default"}], {}, deliver, now=1.0))
        assert [m for m, _ in bot.messages] == [
            "[ops] ran tests for all .go files and .ts",
            "[ops] prod database is down, need your call",
        ]
        assert all(i["acked"] for i in cli.items)

    def test_urgent_items_per_poll_are_capped(self):
        items = [{"id": f"u{n}", "tier": "urgent", "text": f"alert {n}"}
                 for n in range(bridge.HUMAN_OUTBOX_MAX_URGENT_PER_POLL + 3)]
        cli = FakeCLI(items=items)
        sent: list[str] = []

        async def deliver(text):
            sent.append(text)
            return True

        with mock.patch.object(bridge, "run_cli", cli), \
             mock.patch.object(bridge, "_human_outbox_signature", return_value=(1, 1)):
            _run(bridge.human_outbox_cycle([{"name": "ops", "profile": "default"}], {}, deliver, now=1.0))
        assert len(sent) == bridge.HUMAN_OUTBOX_MAX_URGENT_PER_POLL
