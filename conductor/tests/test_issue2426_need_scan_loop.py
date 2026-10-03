"""Regression tests for issue #2426.

With the OS heartbeat daemon installed (the default: `conductor setup`
installs systemd/launchd timers), the bridge disabled its own heartbeat loop
to avoid double-triggering — but that loop was the only code path that read
conductor replies and forwarded `NEED:` lines to Slack/Telegram/Discord.
Result: on a default install, proactive conductor alerts were silently
dropped forever.

The fix: in OS-heartbeat mode the bridge runs a scan-only loop that sends no
ticks and reads each conductor's last reply (read-only). Each NEW reply goes
through the same filter_need_lines (#971) the in-process loop uses: first
sight forwards, repeats escalate and then retire, and an entry is forgotten
once its line disappears so a NEED that recurs later alerts again. An
unchanged reply is not processed twice. Per-conductor counts persist to
CONDUCTOR_DIR/need-scan-state.json so a bridge restart does not re-alert.
"""

from __future__ import annotations

import asyncio
import importlib.util
import json
import subprocess
import sys
from pathlib import Path
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

import bridge  # noqa: E402  pylint: disable=wrong-import-position

_CANONICAL = (
    Path(__file__).resolve().parents[2] / "internal" / "session" / "conductor_bridge.py"
)

NEED = "NEED: TM-5430 pick the MR target branch"


def _run(coro):
    return asyncio.run(coro)


def _config(telegram_user=None):
    return {
        "heartbeat_interval": 15,
        "telegram": {
            "configured": telegram_user is not None,
            "user_id": telegram_user,
        },
    }


def _conductors():
    return [{"name": "jenkins", "profile": "default", "heartbeat_enabled": True}]


def _slack_app():
    app = mock.MagicMock()
    app.client.chat_postMessage = mock.AsyncMock()
    return app


def _slack_texts(slack_app):
    return [c.kwargs["text"] for c in slack_app.client.chat_postMessage.await_args_list]


def _scan(mod, state, reply, slack_app, save=None, config=None):
    """Drive one need_scan_cycle against a single conductor whose last reply
    is ``reply``."""
    with mock.patch.object(mod, "discover_conductors", return_value=_conductors()), \
         mock.patch.object(mod, "get_session_output", return_value=reply):
        _run(mod.need_scan_cycle(
            config or _config(), state, save or (lambda: None),
            telegram_bot=None, slack_app=slack_app, slack_channel_id="C123",
        ))


class TestNeedScanCycleForwards2426:
    """Pin #2426: OS-heartbeat mode must still forward fresh NEED: lines."""

    def test_fresh_need_forwarded_to_slack(self):
        slack_app = _slack_app()
        state: dict = {}
        saves = []

        _scan(bridge, state, "All quiet otherwise.\n" + NEED + "\n", slack_app,
              save=lambda: saves.append(1))

        slack_app.client.chat_postMessage.assert_awaited_once()
        text = slack_app.client.chat_postMessage.await_args.kwargs["text"]
        assert NEED in text
        # Single conductor: no name prefix needed (matches the main loop).
        assert "[jenkins]" not in text
        # Counted + persisted only after confirmed delivery.
        assert state["jenkins"]["counts"] == {NEED: 1}
        assert saves == [1]

    def test_fresh_need_forwarded_to_telegram(self):
        bot = mock.MagicMock()
        bot.send_message = mock.AsyncMock()
        state: dict = {}
        with mock.patch.object(bridge, "discover_conductors", return_value=_conductors()), \
             mock.patch.object(bridge, "get_session_output", return_value=NEED):
            _run(bridge.need_scan_cycle(
                _config(telegram_user=42), state, lambda: None, telegram_bot=bot,
            ))

        bot.send_message.assert_awaited_once()
        assert bot.send_message.await_args.args[0] == 42
        assert "TM-5430" in bot.send_message.await_args.args[1]

    def test_multi_conductor_alerts_carry_name_prefix(self):
        slack_app = _slack_app()
        state: dict = {}
        two = _conductors() + [
            {"name": "sanity", "profile": "default", "heartbeat_enabled": True}
        ]
        outputs = {
            "conductor-jenkins": NEED + "\n",
            "conductor-sanity": "NEED: frel-1 sanity red on t268\n",
        }
        with mock.patch.object(bridge, "discover_conductors", return_value=two), \
             mock.patch.object(bridge, "get_session_output",
                               side_effect=lambda title, profile=None: outputs[title]):
            _run(bridge.need_scan_cycle(
                _config(), state, lambda: None,
                telegram_bot=None, slack_app=slack_app, slack_channel_id="C123",
            ))

        texts = _slack_texts(slack_app)
        assert any(t.startswith("[jenkins]") for t in texts)
        assert any(t.startswith("[sanity]") for t in texts)

    def test_unchanged_reply_not_forwarded_twice(self):
        slack_app = _slack_app()
        state: dict = {}
        saves = []

        reply = "status ok\n" + NEED + "\n"
        _scan(bridge, state, reply, slack_app, save=lambda: saves.append(1))
        # Next scans, identical reply (no new heartbeat turn yet): the reply was
        # already processed, so nothing is forwarded and nothing is rewritten.
        _scan(bridge, state, reply, slack_app, save=lambda: saves.append(1))
        _scan(bridge, state, reply, slack_app, save=lambda: saves.append(1))

        assert slack_app.client.chat_postMessage.await_count == 1
        assert state["jenkins"]["counts"] == {NEED: 1}
        assert saves == [1]

    def test_recurring_need_after_it_disappeared_alerts_again(self):
        """#971 semantics: the entry retires once the line is gone, so the same
        NEED text appearing later (a new deploy to approve) alerts again."""
        slack_app = _slack_app()
        state: dict = {}

        _scan(bridge, state, "Monday.\n" + NEED, slack_app)
        _scan(bridge, state, "Tuesday: all clear, nothing pending.", slack_app)
        assert state["jenkins"]["counts"] == {}
        _scan(bridge, state, "Friday.\n" + NEED, slack_app)

        texts = _slack_texts(slack_app)
        assert len(texts) == 2
        assert all(NEED in t for t in texts)

    def test_repeated_need_across_new_replies_escalates_then_retires(self):
        """Same rules as the in-process loop (filter_need_lines): forwarded on
        each new reply below the threshold, one STILL BLOCKED escalation at the
        threshold, then silence while it keeps repeating."""
        slack_app = _slack_app()
        state: dict = {}

        for i in range(bridge.NEED_RETIRE_THRESHOLD + 2):
            _scan(bridge, state, f"[STATUS] cycle {i}\n{NEED}", slack_app)

        texts = _slack_texts(slack_app)
        assert len(texts) == bridge.NEED_RETIRE_THRESHOLD
        assert "STILL BLOCKED" in texts[-1]
        assert all("STILL BLOCKED" not in t for t in texts[:-1])
        assert state["jenkins"]["counts"] == {NEED: bridge.NEED_RETIRE_THRESHOLD + 2}

    def test_counts_pruned_to_lines_in_current_reply(self):
        slack_app = _slack_app()
        state: dict = {}

        _scan(bridge, state, "NEED: a\nNEED: b", slack_app)
        _scan(bridge, state, "later\nNEED: b\nNEED: c", slack_app)

        assert state["jenkins"]["counts"] == {"NEED: b": 2, "NEED: c": 1}

    def test_removed_conductor_state_is_dropped(self):
        state: dict = {
            "gone": {"reply": "x", "counts": {"NEED: old": 1}},
        }
        saves = []
        _scan(bridge, state, "no needs here", _slack_app(), save=lambda: saves.append(1))
        assert "gone" not in state
        assert saves == [1]

    def test_failed_send_retries_next_scan(self):
        slack_app = _slack_app()
        slack_app.client.chat_postMessage = mock.AsyncMock(
            side_effect=[RuntimeError("slack down"), None],
        )
        state: dict = {}

        _scan(bridge, state, NEED + "\n", slack_app)  # first attempt: send fails
        # Failure must NOT consume the reply: the line retries next scan.
        assert "jenkins" not in state
        _scan(bridge, state, NEED + "\n", slack_app)  # second attempt: delivers

        assert slack_app.client.chat_postMessage.await_count == 2
        assert state["jenkins"]["counts"] == {NEED: 1}

    def test_output_read_error_keeps_counts(self):
        """A failed `session output` read must not look like a reply without
        NEED lines, or the next real reply would re-alert."""
        slack_app = _slack_app()
        state: dict = {}

        _scan(bridge, state, NEED, slack_app)
        _scan(bridge, state, "[Error getting output: tmux not running]", slack_app)
        assert state["jenkins"]["counts"] == {NEED: 1}

    def test_scan_is_read_only(self):
        """The scan must only read (`session output`); it never sends to the
        conductor, because the OS heartbeat already drives the ticks."""
        slack_app = _slack_app()
        calls = []

        def fake_run_cli(*args, profile=None, timeout=120, input_text=None):
            calls.append(args)
            if args[:2] == ("conductor", "tier-filter"):
                return subprocess.CompletedProcess(["agent-deck"], 1, "", "old binary")
            return subprocess.CompletedProcess(
                ["agent-deck"], 0, json.dumps({"content": NEED}), "",
            )

        with mock.patch.object(bridge, "discover_conductors", return_value=_conductors()), \
             mock.patch.object(bridge, "run_cli", side_effect=fake_run_cli), \
             mock.patch.object(bridge, "send_to_conductor",
                               side_effect=AssertionError("scan must not send")):
            _run(bridge.need_scan_cycle(
                _config(), {}, lambda: None,
                telegram_bot=None, slack_app=slack_app, slack_channel_id="C123",
            ))

        slack_app.client.chat_postMessage.assert_awaited_once()
        assert calls
        # Reads the reply and tiers it locally (#2469); never `session send`.
        assert all(c[:2] in (("session", "output"), ("conductor", "tier-filter")) for c in calls)


def _load_bridge_process(module_name):
    """Import a fresh copy of the bridge, as a newly started bridge process
    would: CONDUCTOR_DIR is resolved at import time from the environment."""
    spec = importlib.util.spec_from_file_location(module_name, _CANONICAL)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class TestNeedScanStateFollowsConductorDir2426:
    """The scan state lives at CONDUCTOR_DIR/need-scan-state.json, so it
    follows AGENT_DECK_CONDUCTOR_DIR (and the legacy layout) instead of a
    hardcoded ~/.local/share path, and survives a bridge restart."""

    def test_state_under_relocated_conductor_dir_survives_restart(self, tmp_path, monkeypatch):
        home = tmp_path / "home"
        home.mkdir()
        relocated = tmp_path / "relocated" / "conductors"
        monkeypatch.setenv("HOME", str(home))
        for key in ("XDG_DATA_HOME", "XDG_CONFIG_HOME"):
            monkeypatch.delenv(key, raising=False)
        monkeypatch.setenv("AGENT_DECK_CONDUCTOR_DIR", str(relocated))

        # Two bridge processes, before and after a restart.
        first = _load_bridge_process("bridge_2426_first")
        second = _load_bridge_process("bridge_2426_second")
        assert first.CONDUCTOR_DIR == relocated
        relocated.mkdir(parents=True)

        slack_app = _slack_app()

        def run_bridge(mod):
            # OS heartbeat present: heartbeat_loop routes to the scan loop,
            # which scans once on entry; the first sleep ends this "process".
            with mock.patch.object(mod, "_os_heartbeat_daemon_installed", return_value=True), \
                 mock.patch.object(mod, "discover_conductors", return_value=_conductors()), \
                 mock.patch.object(mod, "get_session_output", return_value="ok\n" + NEED), \
                 mock.patch.object(mod.asyncio, "sleep",
                                   new=mock.AsyncMock(side_effect=RuntimeError("stop"))):
                with pytest.raises(RuntimeError, match="stop"):
                    _run(mod.heartbeat_loop(
                        _config(), slack_app=slack_app, slack_channel_id="C123",
                    ))

        run_bridge(first)
        assert slack_app.client.chat_postMessage.await_count == 1

        state_file = relocated / "need-scan-state.json"
        assert state_file.is_file()
        assert NEED in state_file.read_text()
        assert not (home / ".local").exists()

        run_bridge(second)  # restarted bridge, same reply: no re-alert
        assert slack_app.client.chat_postMessage.await_count == 1


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
