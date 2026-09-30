"""Regression tests for issue #2426.

With the OS heartbeat daemon installed (the default: `conductor setup`
installs systemd/launchd timers), the bridge disabled its own heartbeat loop
to avoid double-triggering — but that loop was the only code path that read
conductor replies and forwarded `NEED:` lines to Slack/Telegram/Discord.
Result: on a default install, proactive conductor alerts were silently
dropped forever.

The fix: in OS-heartbeat mode the bridge runs a scan-only loop that sends no
ticks, reads each conductor's last response (read-only), and forwards only
never-before-seen NEED: lines (persisted seen-set survives restarts).
"""

from __future__ import annotations

import asyncio
import sys
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent.parent))

import bridge  # noqa: E402  pylint: disable=wrong-import-position


def _run(coro):
    return asyncio.run(coro)


def _config():
    return {
        "heartbeat_interval": 15,
        "telegram": {"configured": False, "user_id": None},
    }


def _conductors():
    return [{"name": "jenkins", "profile": "default", "heartbeat_enabled": True}]


def _slack_app():
    app = mock.MagicMock()
    app.client.chat_postMessage = mock.AsyncMock()
    return app


class TestNeedScanCycleForwards2426:
    """Pin #2426: OS-heartbeat mode must still forward fresh NEED: lines."""

    def test_fresh_need_forwarded_to_slack(self):
        slack_app = _slack_app()
        seen: dict = {}
        saves = []

        with mock.patch.object(bridge, "discover_conductors", return_value=_conductors()), \
             mock.patch.object(bridge, "get_session_output",
                               return_value="All quiet otherwise.\nNEED: TM-5430 pick the MR target branch\n"):
            _run(bridge.need_scan_cycle(
                _config(), seen, lambda: saves.append(1),
                telegram_bot=None, slack_app=slack_app, slack_channel_id="C123",
            ))

        slack_app.client.chat_postMessage.assert_awaited_once()
        text = slack_app.client.chat_postMessage.await_args.kwargs["text"]
        assert "NEED: TM-5430 pick the MR target branch" in text
        # Single conductor: no name prefix needed (matches the main loop).
        assert "[jenkins]" not in text
        # Marked seen + persisted only after confirmed delivery.
        assert "NEED: TM-5430 pick the MR target branch" in seen["jenkins"]
        assert saves == [1]

    def test_multi_conductor_alerts_carry_name_prefix(self):
        slack_app = _slack_app()
        seen: dict = {}
        two = _conductors() + [
            {"name": "sanity", "profile": "default", "heartbeat_enabled": True}
        ]
        outputs = {
            "conductor-jenkins": "NEED: TM-5430 pick the MR target branch\n",
            "conductor-sanity":  "NEED: frel-1 sanity red on t268\n",
        }
        with mock.patch.object(bridge, "discover_conductors", return_value=two), \
             mock.patch.object(bridge, "get_session_output",
                               side_effect=lambda title, profile=None: outputs[title]):
            _run(bridge.need_scan_cycle(
                _config(), seen, lambda: None,
                telegram_bot=None, slack_app=slack_app, slack_channel_id="C123",
            ))

        texts = [c.kwargs["text"] for c in slack_app.client.chat_postMessage.await_args_list]
        assert any(t.startswith("[jenkins]") for t in texts)
        assert any(t.startswith("[sanity]") for t in texts)

    def test_unchanged_reply_not_forwarded_twice(self):
        slack_app = _slack_app()
        seen: dict = {}

        reply = "status ok\nNEED: TM-5430 pick the MR target branch\n"
        with mock.patch.object(bridge, "discover_conductors", return_value=_conductors()), \
             mock.patch.object(bridge, "get_session_output", return_value=reply):
            _run(bridge.need_scan_cycle(
                _config(), seen, lambda: None,
                telegram_bot=None, slack_app=slack_app, slack_channel_id="C123",
            ))
            # Second interval, identical reply: nothing new to forward.
            _run(bridge.need_scan_cycle(
                _config(), seen, lambda: None,
                telegram_bot=None, slack_app=slack_app, slack_channel_id="C123",
            ))

        assert slack_app.client.chat_postMessage.await_count == 1

    def test_failed_send_retries_next_scan(self):
        slack_app = _slack_app()
        slack_app.client.chat_postMessage = mock.AsyncMock(
            side_effect=[RuntimeError("slack down"), None],
        )
        seen: dict = {}

        reply = "NEED: TM-5430 pick the MR target branch\n"
        with mock.patch.object(bridge, "discover_conductors", return_value=_conductors()), \
             mock.patch.object(bridge, "get_session_output", return_value=reply):
            _run(bridge.need_scan_cycle(  # first attempt: send fails
                _config(), seen, lambda: None,
                telegram_bot=None, slack_app=slack_app, slack_channel_id="C123",
            ))
            # Failure must NOT mark seen — the line retries next scan.
            assert not seen.get("jenkins")
            _run(bridge.need_scan_cycle(  # second attempt: delivers
                _config(), seen, lambda: None,
                telegram_bot=None, slack_app=slack_app, slack_channel_id="C123",
            ))

        assert slack_app.client.chat_postMessage.await_count == 2
        assert "NEED: TM-5430 pick the MR target branch" in seen["jenkins"]


class TestHeartbeatLoopOsModeRoutesToScan2426:
    """The pre-fix code returned early and never scanned; heartbeat_loop must
    now delegate to the scan-only NEED forwarder when an OS heartbeat daemon
    is installed."""

    def test_os_heartbeat_mode_enters_scan_loop(self):
        called = []

        async def fake_scan(config, *args, **kwargs):
            called.append(config)

        with mock.patch.object(bridge, "_os_heartbeat_daemon_installed", return_value=True), \
             mock.patch.object(bridge, "heartbeat_need_scan_loop", side_effect=fake_scan):
            _run(bridge.heartbeat_loop(_config()))

        assert called == [_config()]

    def test_no_os_daemon_keeps_legacy_loop(self):
        called = []

        async def fake_scan(config, *args, **kwargs):
            called.append(config)

        # The legacy loop never returns; make its first sleep raise to escape.
        with mock.patch.object(bridge, "_os_heartbeat_daemon_installed", return_value=False), \
             mock.patch.object(bridge, "heartbeat_need_scan_loop", side_effect=fake_scan), \
             mock.patch.object(bridge.asyncio, "sleep",
                               new=mock.AsyncMock(side_effect=RuntimeError("tick"))):
            try:
                _run(bridge.heartbeat_loop(_config()))
            except RuntimeError:
                pass

        assert called == []
