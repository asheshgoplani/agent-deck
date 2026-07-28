#!/usr/bin/env python3
"""Unit tests for watchdog v2 — rate-limit + atomic-cascade + critical-filter + Telegram stub."""

import json
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent))
import watchdog as wd_mod

_ISOLATION = {}

# Every module-level name this suite overrides, so tearDownModule can put the
# real ones back and nothing leaks between test runs in the same interpreter.
_ISOLATED_NAMES = (
    "AGENT_DECK_BIN", "WATCHDOG_DIR", "AUTORESTART_DIR", "ESCALATIONS_LOG",
    "RESTART_LOG", "LIVENESS_LOG", "ESCALATE_SCRIPT",
    "MIN_GLOBAL_RESTART_INTERVAL_S", "LIVENESS_CONFIRM_GAP_S",
)


def setUpModule():
    """Keep the suite off the live agent-deck install, and make it fast.

    watchdog.py resolves its log paths under AGENT_DECK_ROOT (default
    ~/.agent-deck, which on a maintainer machine is the LIVE data directory) and
    shells out to AGENT_DECK_BIN. Unguarded, a test run appends to the real
    watchdog logs and can invoke the real CLI against real sessions. Redirect
    both, point the binary at a path that cannot exist, and drop the two sleeps
    (inter-restart spacing, liveness sample gap) so the suite finishes in seconds
    instead of minutes — which is what lets it run in CI at all.
    """
    _ISOLATION["tmp"] = tempfile.TemporaryDirectory()
    root = Path(_ISOLATION["tmp"].name)
    (root / "watchdog" / "autorestart").mkdir(parents=True, exist_ok=True)
    _ISOLATION["saved"] = {name: getattr(wd_mod, name) for name in _ISOLATED_NAMES}

    wd_mod.AGENT_DECK_BIN = str(root / "no-such-agent-deck-binary")
    wd_mod.WATCHDOG_DIR = root / "watchdog"
    wd_mod.AUTORESTART_DIR = root / "watchdog" / "autorestart"
    wd_mod.ESCALATIONS_LOG = root / "watchdog" / "escalations.log"
    wd_mod.RESTART_LOG = root / "watchdog" / "restart.log"
    wd_mod.LIVENESS_LOG = root / "watchdog" / "liveness.log"
    wd_mod.ESCALATE_SCRIPT = root / "watchdog" / "escalate.sh"  # deliberately absent
    wd_mod.MIN_GLOBAL_RESTART_INTERVAL_S = 0
    wd_mod.LIVENESS_CONFIRM_GAP_S = 0.0


def tearDownModule():
    for name, value in _ISOLATION.get("saved", {}).items():
        setattr(wd_mod, name, value)
    tmp = _ISOLATION.pop("tmp", None)
    if tmp is not None:
        tmp.cleanup()


def make_sess(sid="c-1", title="conductor-travel", group="", profile="personal",
              status="error", is_conductor=False):
    return {
        "id": sid,
        "title": title,
        "group": group,
        "profile": profile,
        "status": status,
        "tool": "claude",
        "is_conductor": is_conductor,
    }


class TestIsCritical(unittest.TestCase):
    def test_conductor_title_prefix(self):
        self.assertTrue(wd_mod.is_critical(make_sess(title="conductor-travel")))

    def test_regular_session_not_critical(self):
        self.assertFalse(wd_mod.is_critical(make_sess(title="my-project")))

    def test_watchers_group(self):
        self.assertTrue(wd_mod.is_critical(make_sess(title="random", group="watchers")))

    def test_conductor_group(self):
        self.assertTrue(wd_mod.is_critical(make_sess(title="anything", group="conductor")))

    def test_is_conductor_flag(self):
        self.assertTrue(wd_mod.is_critical(make_sess(title="x", is_conductor=True)))

    def test_exact_title_agent_deck(self):
        self.assertTrue(wd_mod.is_critical(make_sess(title="agent-deck")))

    def test_exact_title_meeting_watcher(self):
        self.assertTrue(wd_mod.is_critical(make_sess(title="meeting-watcher")))

    def test_exact_title_gmail_watcher(self):
        self.assertTrue(wd_mod.is_critical(make_sess(title="gmail-watcher")))

    def test_empty_or_none(self):
        self.assertFalse(wd_mod.is_critical({}))
        self.assertFalse(wd_mod.is_critical(None))

    def test_autorestart_marker(self):
        with tempfile.TemporaryDirectory() as td:
            original = wd_mod.AUTORESTART_DIR
            try:
                wd_mod.AUTORESTART_DIR = Path(td)
                (Path(td) / "opt-in-id").touch()
                self.assertTrue(wd_mod.is_critical(make_sess(sid="opt-in-id", title="ordinary")))
                self.assertFalse(wd_mod.is_critical(make_sess(sid="other-id", title="ordinary")))
            finally:
                wd_mod.AUTORESTART_DIR = original


class TestRateLimit(unittest.TestCase):
    def setUp(self):
        self.wd = wd_mod.Watchdog(dry_run=True)

    def test_first_three_allowed_then_blocked(self):
        sess = make_sess()
        with mock.patch.object(wd_mod, "show_session", return_value=sess), \
             tempfile.TemporaryDirectory() as td:
            old = wd_mod.ESCALATIONS_LOG
            wd_mod.ESCALATIONS_LOG = Path(td) / "esc.log"
            try:
                for _ in range(wd_mod.RATE_LIMIT_MAX):
                    self.wd.cooldown_until.pop(sess["id"], None)
                    self.wd.maybe_restart(sess)
                before = len(self.wd.restart_history[sess["id"]])
                self.wd.cooldown_until.pop(sess["id"], None)
                with mock.patch.object(wd_mod, "telegram_send", return_value=True):
                    self.wd.maybe_restart(sess)
                after = len(self.wd.restart_history[sess["id"]])
                self.assertEqual(before, after, "rate limit should block further attempts")
            finally:
                wd_mod.ESCALATIONS_LOG = old

    def test_rate_limit_window_is_600s(self):
        self.assertEqual(wd_mod.RATE_LIMIT_WINDOW_S, 600)

    def test_cooldown_blocks_immediate_retry(self):
        sess = make_sess()
        with mock.patch.object(wd_mod, "show_session", return_value=sess):
            self.wd.maybe_restart(sess)
            before = len(self.wd.restart_history[sess["id"]])
            self.wd.maybe_restart(sess)
            after = len(self.wd.restart_history[sess["id"]])
            self.assertEqual(before, after, "cooldown should block immediate retry")

    def test_prune_history_after_window(self):
        self.wd.restart_history["old-id"] = wd_mod.deque([time.time() - 1000])
        self.wd._prune_history(time.time())
        self.assertEqual(len(self.wd.restart_history["old-id"]), 0)

    def test_status_not_error_skips_restart(self):
        sess = make_sess(status="running")
        with mock.patch.object(wd_mod, "show_session", return_value=sess):
            self.wd.maybe_restart(sess)
            self.assertNotIn(sess["id"], self.wd.restart_history)

    def test_rate_limit_sends_telegram_escalation(self):
        sess = make_sess()
        with tempfile.TemporaryDirectory() as td:
            old = wd_mod.ESCALATIONS_LOG
            wd_mod.ESCALATIONS_LOG = Path(td) / "esc.log"
            try:
                with mock.patch.object(wd_mod, "show_session", return_value=sess):
                    for _ in range(wd_mod.RATE_LIMIT_MAX):
                        self.wd.cooldown_until.pop(sess["id"], None)
                        self.wd.maybe_restart(sess)
                    self.wd.cooldown_until.pop(sess["id"], None)
                    with mock.patch.object(wd_mod, "telegram_send", return_value=True) as tg:
                        self.wd.maybe_restart(sess)
                        tg.assert_called_once()
                        args, kwargs = tg.call_args
                        self.assertIn("keeps crashing", args[0])
            finally:
                wd_mod.ESCALATIONS_LOG = old


class TestAtomicCascade(unittest.TestCase):
    def setUp(self):
        self.wd = wd_mod.Watchdog(dry_run=True)

    def test_below_threshold_does_not_trigger(self):
        fewer = [(f"id-{i}", f"conductor-x{i}", "personal") for i in range(wd_mod.CASCADE_THRESHOLD - 1)]
        with mock.patch.object(self.wd, "_scan_critical_error_sessions", return_value=fewer):
            fired = self.wd._maybe_trigger_cascade()
        self.assertFalse(fired)
        self.assertEqual(self.wd.cascade_settling_until, 0.0)

    def test_at_threshold_triggers_settle_window(self):
        many = [(f"id-{i}", f"conductor-x{i}", "personal") for i in range(wd_mod.CASCADE_THRESHOLD)]
        with tempfile.TemporaryDirectory() as td:
            old = wd_mod.ESCALATIONS_LOG
            wd_mod.ESCALATIONS_LOG = Path(td) / "esc.log"
            try:
                with mock.patch.object(self.wd, "_scan_critical_error_sessions", return_value=many), \
                     mock.patch.object(wd_mod, "telegram_send", return_value=True):
                    fired = self.wd._maybe_trigger_cascade()
                self.assertTrue(fired)
                self.assertGreater(self.wd.cascade_settling_until, time.time())
                self.assertIn("cascade-detected", Path(wd_mod.ESCALATIONS_LOG).read_text())
            finally:
                wd_mod.ESCALATIONS_LOG = old

    def test_settling_window_blocks_individual_restarts(self):
        sess = make_sess()
        self.wd.cascade_settling_until = time.time() + 100
        with mock.patch.object(wd_mod, "show_session", return_value=sess):
            self.wd.maybe_restart(sess)
        self.assertNotIn(sess["id"], self.wd.restart_history)


class TestContinuityMessage(unittest.TestCase):
    def test_dry_run_does_not_call_send(self):
        with mock.patch.object(wd_mod, "run_cmd") as rc:
            ok = wd_mod.send_continuity_message("c-1", "personal", dry_run=True)
        self.assertTrue(ok)
        rc.assert_not_called()

    def test_live_mode_invokes_send_no_wait(self):
        with mock.patch.object(wd_mod, "run_cmd", return_value=(0, "", "")) as rc:
            ok = wd_mod.send_continuity_message("c-1", "personal", dry_run=False)
        self.assertTrue(ok)
        args = rc.call_args[0][0]
        self.assertIn("session", args)
        self.assertIn("send", args)
        self.assertIn("--no-wait", args)
        self.assertIn("c-1", args)


class TestIsEscalationCritical(unittest.TestCase):
    def test_conductor_title_prefix_is_escalation_critical(self):
        self.assertTrue(wd_mod.is_escalation_critical(make_sess(title="conductor-travel")))

    def test_watchers_group_is_escalation_critical(self):
        self.assertTrue(wd_mod.is_escalation_critical(make_sess(title="x", group="watchers")))

    def test_is_conductor_flag_is_escalation_critical(self):
        self.assertTrue(wd_mod.is_escalation_critical(make_sess(title="x", is_conductor=True)))

    def test_group_conductor_NOT_escalation_critical_without_flag(self):
        # Workers in group=conductor (e.g. test-visual) should restart but NOT telegram
        self.assertFalse(wd_mod.is_escalation_critical(make_sess(title="test-visual", group="conductor")))

    def test_exact_title_agent_deck_NOT_escalation_critical(self):
        # 'agent-deck' is in the restart allow-list but not real conductor — no telegram
        self.assertFalse(wd_mod.is_escalation_critical(make_sess(title="agent-deck")))

    def test_regular_session_NOT_escalation_critical(self):
        self.assertFalse(wd_mod.is_escalation_critical(make_sess(title="my-project")))


class TestEscalationDedup(unittest.TestCase):
    def test_same_sid_severity_dedup_within_window(self):
        wd = wd_mod.Watchdog(dry_run=True)
        with tempfile.TemporaryDirectory() as td, \
             mock.patch.object(wd_mod, "telegram_send", return_value=True) as tg:
            old = wd_mod.ESCALATIONS_LOG
            wd_mod.ESCALATIONS_LOG = Path(td) / "esc.log"
            try:
                wd.escalate("rate-limit", "msg1", telegram=True, sid="c-1")
                wd.escalate("rate-limit", "msg2", telegram=True, sid="c-1")
                wd.escalate("rate-limit", "msg3", telegram=True, sid="c-1")
                self.assertEqual(tg.call_count, 1, "second+third should be deduped")
            finally:
                wd_mod.ESCALATIONS_LOG = old

    def test_different_sid_not_deduped(self):
        wd = wd_mod.Watchdog(dry_run=True)
        with tempfile.TemporaryDirectory() as td, \
             mock.patch.object(wd_mod, "telegram_send", return_value=True) as tg:
            old = wd_mod.ESCALATIONS_LOG
            wd_mod.ESCALATIONS_LOG = Path(td) / "esc.log"
            try:
                wd.escalate("rate-limit", "A", telegram=True, sid="a-1")
                wd.escalate("rate-limit", "B", telegram=True, sid="b-1")
                self.assertEqual(tg.call_count, 2)
            finally:
                wd_mod.ESCALATIONS_LOG = old

    def test_different_severity_not_deduped(self):
        wd = wd_mod.Watchdog(dry_run=True)
        with tempfile.TemporaryDirectory() as td, \
             mock.patch.object(wd_mod, "telegram_send", return_value=True) as tg:
            old = wd_mod.ESCALATIONS_LOG
            wd_mod.ESCALATIONS_LOG = Path(td) / "esc.log"
            try:
                wd.escalate("rate-limit", "r", telegram=True, sid="c-1")
                wd.escalate("restart-failed", "f", telegram=True, sid="c-1")
                self.assertEqual(tg.call_count, 2)
            finally:
                wd_mod.ESCALATIONS_LOG = old


class TestWorkerNoTelegram(unittest.TestCase):
    """Verify that rate-limit on a non-escalation-critical session does NOT fire telegram."""

    def test_worker_rate_limit_local_only(self):
        wd = wd_mod.Watchdog(dry_run=True)
        # test-visual style: group=conductor but not a real conductor
        sess = make_sess(sid="test-v", title="test-visual", group="conductor", profile="default", status="error")
        with tempfile.TemporaryDirectory() as td, \
             mock.patch.object(wd_mod, "show_session", return_value=sess), \
             mock.patch.object(wd_mod, "telegram_send", return_value=True) as tg:
            old = wd_mod.ESCALATIONS_LOG
            wd_mod.ESCALATIONS_LOG = Path(td) / "esc.log"
            try:
                # Burn through the rate-limit
                for _ in range(wd_mod.RATE_LIMIT_MAX):
                    wd.cooldown_until.pop("test-v", None)
                    wd.maybe_restart(sess)
                wd.cooldown_until.pop("test-v", None)
                wd.maybe_restart(sess)  # this should hit rate limit
                tg.assert_not_called()  # worker → local log only
                content = Path(wd_mod.ESCALATIONS_LOG).read_text()
                self.assertIn("rate-limit", content)
            finally:
                wd_mod.ESCALATIONS_LOG = old

    def test_conductor_rate_limit_does_telegram(self):
        wd = wd_mod.Watchdog(dry_run=True)
        sess = make_sess(sid="cond-1", title="conductor-travel", group="conductor", profile="personal", status="error")
        with tempfile.TemporaryDirectory() as td, \
             mock.patch.object(wd_mod, "show_session", return_value=sess), \
             mock.patch.object(wd_mod, "telegram_send", return_value=True) as tg:
            old = wd_mod.ESCALATIONS_LOG
            wd_mod.ESCALATIONS_LOG = Path(td) / "esc.log"
            try:
                for _ in range(wd_mod.RATE_LIMIT_MAX):
                    wd.cooldown_until.pop("cond-1", None)
                    wd.maybe_restart(sess)
                wd.cooldown_until.pop("cond-1", None)
                wd.maybe_restart(sess)
                tg.assert_called_once()  # real conductor → telegram
            finally:
                wd_mod.ESCALATIONS_LOG = old


class TestStaleCleanup(unittest.TestCase):
    def test_stale_non_critical_gets_removed(self):
        wd = wd_mod.Watchdog(dry_run=True)
        # worker-ish session in error state
        sess = make_sess(sid="test-v", title="test-visual", group="conductor", profile="default", status="error")
        wd.first_error_seen_at["test-v"] = time.time() - (wd_mod.STALE_ERROR_CLEANUP_S + 10)
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
             mock.patch.object(wd_mod, "show_session", return_value=sess), \
             mock.patch.object(wd_mod, "run_cmd", return_value=(0, "", "")) as rc:
            wd._stale_cleanup_scan()
        # Should have called `agent-deck ... remove test-v`
        call_args = [c[0][0] for c in rc.call_args_list]
        self.assertTrue(any("remove" in args and "test-v" in args for args in call_args),
                        f"expected remove call, got: {call_args}")
        self.assertIn("test-v", wd.removed_ids)

    def test_stale_critical_NOT_removed(self):
        wd = wd_mod.Watchdog(dry_run=True)
        sess = make_sess(sid="cond-1", title="conductor-travel", group="conductor", profile="personal", status="error")
        wd.first_error_seen_at["cond-1"] = time.time() - (wd_mod.STALE_ERROR_CLEANUP_S + 10)
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
             mock.patch.object(wd_mod, "show_session", return_value=sess), \
             mock.patch.object(wd_mod, "run_cmd", return_value=(0, "", "")) as rc:
            wd._stale_cleanup_scan()
        # No `remove` call — real conductors never auto-removed
        call_args = [c[0][0] for c in rc.call_args_list]
        self.assertFalse(any("remove" in args for args in call_args),
                         f"should NOT remove critical, got: {call_args}")
        self.assertNotIn("cond-1", wd.removed_ids)

    def test_non_stuck_session_not_removed(self):
        wd = wd_mod.Watchdog(dry_run=True)
        sess = make_sess(sid="test-v", title="test-visual", group="conductor", status="error")
        # only been errored for 5s, not stale
        wd.first_error_seen_at["test-v"] = time.time() - 5
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
             mock.patch.object(wd_mod, "show_session", return_value=sess), \
             mock.patch.object(wd_mod, "run_cmd", return_value=(0, "", "")) as rc:
            wd._stale_cleanup_scan()
        self.assertEqual(rc.call_count, 0)
        self.assertNotIn("test-v", wd.removed_ids)


class TestEscalation(unittest.TestCase):
    def test_writes_log(self):
        wd = wd_mod.Watchdog(dry_run=True)
        with tempfile.TemporaryDirectory() as td:
            old = wd_mod.ESCALATIONS_LOG
            wd_mod.ESCALATIONS_LOG = Path(td) / "esc.log"
            try:
                wd.escalate("test-sev", "hello")
                content = Path(wd_mod.ESCALATIONS_LOG).read_text()
                self.assertIn("test-sev", content)
                self.assertIn("hello", content)
            finally:
                wd_mod.ESCALATIONS_LOG = old

    def test_telegram_flag_invokes_telegram_send(self):
        wd = wd_mod.Watchdog(dry_run=True)
        with tempfile.TemporaryDirectory() as td:
            old = wd_mod.ESCALATIONS_LOG
            wd_mod.ESCALATIONS_LOG = Path(td) / "esc.log"
            try:
                with mock.patch.object(wd_mod, "telegram_send", return_value=True) as tg:
                    wd.escalate("cascade-detected", "msg", telegram=True)
                    tg.assert_called_once()
            finally:
                wd_mod.ESCALATIONS_LOG = old


class TestRestartFailureEscalation(unittest.TestCase):
    def test_two_consecutive_failures_escalate(self):
        wd = wd_mod.Watchdog(dry_run=False)
        sess = make_sess()
        with tempfile.TemporaryDirectory() as td:
            old_log = wd_mod.ESCALATIONS_LOG
            wd_mod.ESCALATIONS_LOG = Path(td) / "esc.log"
            try:
                with mock.patch.object(wd_mod, "show_session", return_value=sess), \
                     mock.patch.object(wd_mod, "run_cmd", return_value=(1, "", "boom")), \
                     mock.patch.object(wd_mod, "telegram_send", return_value=True):
                    wd.maybe_restart(sess)
                    wd.cooldown_until.pop(sess["id"], None)
                    wd.maybe_restart(sess)
                content = Path(wd_mod.ESCALATIONS_LOG).read_text()
                self.assertIn("restart-failed", content)
            finally:
                wd_mod.ESCALATIONS_LOG = old_log


class TestPollerExistence(unittest.TestCase):
    """Capability A (v1.7.63): detect missing bun-telegram pollers and trigger
    `agent-deck session restart` exactly once per conductor per hour."""

    def setUp(self):
        self.wd = wd_mod.Watchdog(dry_run=True)

    def _make_conductor(self, sid, env_file=None, has_telegram=True, profile="personal"):
        return {
            "id": sid,
            "title": f"conductor-{sid}",
            "group": "conductor",
            "profile": profile,
            "status": "running",
            "is_conductor": True,
            "channels": [wd_mod.TELEGRAM_CHANNEL_NAME] if has_telegram else [],
            "env_file": str(env_file) if env_file else None,
        }

    def test_poller_running_no_restart(self):
        with tempfile.TemporaryDirectory() as td:
            env_path = Path(td) / ".envrc"
            state_dir = Path(td) / "state"
            env_path.write_text(f"export TELEGRAM_STATE_DIR={state_dir}\n")
            sess = self._make_conductor("c-1", env_file=env_path)
            with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
                 mock.patch.object(wd_mod, "bun_telegram_state_dirs", return_value={str(state_dir)}), \
                 mock.patch.object(wd_mod, "run_cmd") as rc:
                restarted = self.wd.check_poller_existence()
        self.assertEqual(restarted, [])
        rc.assert_not_called()

    def test_poller_missing_triggers_restart(self):
        with tempfile.TemporaryDirectory() as td:
            env_path = Path(td) / ".envrc"
            state_dir = Path(td) / "state"
            env_path.write_text(f"export TELEGRAM_STATE_DIR={state_dir}\n")
            sess = self._make_conductor("c-2", env_file=env_path)
            with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
                 mock.patch.object(wd_mod, "bun_telegram_state_dirs", return_value=set()):
                restarted = self.wd.check_poller_existence()
        self.assertEqual(restarted, ["c-2"])
        self.assertIn("c-2", self.wd.last_poller_restart_at)

    def test_session_without_telegram_channel_ignored(self):
        with tempfile.TemporaryDirectory() as td:
            env_path = Path(td) / ".envrc"
            state_dir = Path(td) / "state"
            env_path.write_text(f"TELEGRAM_STATE_DIR={state_dir}\n")
            sess = self._make_conductor("c-3", env_file=env_path, has_telegram=False)
            with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
                 mock.patch.object(wd_mod, "bun_telegram_state_dirs", return_value=set()):
                restarted = self.wd.check_poller_existence()
        self.assertEqual(restarted, [])

    def test_dedup_within_one_hour(self):
        with tempfile.TemporaryDirectory() as td:
            env_path = Path(td) / ".envrc"
            state_dir = Path(td) / "state"
            env_path.write_text(f"TELEGRAM_STATE_DIR={state_dir}\n")
            sess = self._make_conductor("c-4", env_file=env_path)
            with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
                 mock.patch.object(wd_mod, "bun_telegram_state_dirs", return_value=set()):
                t0 = 100000.0
                self.wd.check_poller_existence(now=t0)
                restarted = self.wd.check_poller_existence(now=t0 + 1800)  # +30 min
        self.assertEqual(restarted, [])

    def test_dedup_window_expires_after_one_hour(self):
        with tempfile.TemporaryDirectory() as td:
            env_path = Path(td) / ".envrc"
            state_dir = Path(td) / "state"
            env_path.write_text(f"TELEGRAM_STATE_DIR={state_dir}\n")
            sess = self._make_conductor("c-5", env_file=env_path)
            with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
                 mock.patch.object(wd_mod, "bun_telegram_state_dirs", return_value=set()):
                t0 = 200000.0
                self.wd.check_poller_existence(now=t0)
                restarted = self.wd.check_poller_existence(now=t0 + 3660)  # +61 min
        self.assertEqual(restarted, ["c-5"])

    def test_env_file_with_quoted_path(self):
        with tempfile.TemporaryDirectory() as td:
            env_path = Path(td) / ".envrc"
            state_dir = Path(td) / "state with spaces"
            env_path.write_text(f'export TELEGRAM_STATE_DIR="{state_dir}"\n')
            sess = self._make_conductor("c-6", env_file=env_path)
            with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
                 mock.patch.object(wd_mod, "bun_telegram_state_dirs", return_value={str(state_dir)}):
                restarted = self.wd.check_poller_existence()
        self.assertEqual(restarted, [])

    def test_missing_env_file_skipped(self):
        sess = self._make_conductor("c-7", env_file="/does/not/exist/.envrc")
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
             mock.patch.object(wd_mod, "bun_telegram_state_dirs", return_value=set()):
            restarted = self.wd.check_poller_existence()
        self.assertEqual(restarted, [])

    def test_envrc_without_state_dir_skipped(self):
        with tempfile.TemporaryDirectory() as td:
            env_path = Path(td) / ".envrc"
            env_path.write_text("export SOMETHING_ELSE=value\n")
            sess = self._make_conductor("c-8", env_file=env_path)
            with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
                 mock.patch.object(wd_mod, "bun_telegram_state_dirs", return_value=set()):
                restarted = self.wd.check_poller_existence()
        self.assertEqual(restarted, [])

    def test_live_mode_invokes_session_restart(self):
        with tempfile.TemporaryDirectory() as td:
            env_path = Path(td) / ".envrc"
            state_dir = Path(td) / "state"
            env_path.write_text(f"TELEGRAM_STATE_DIR={state_dir}\n")
            sess = self._make_conductor("c-9", env_file=env_path)
            wd = wd_mod.Watchdog(dry_run=False)
            with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
                 mock.patch.object(wd_mod, "bun_telegram_state_dirs", return_value=set()), \
                 mock.patch.object(wd_mod, "run_cmd", return_value=(0, "", "")) as rc:
                restarted = wd.check_poller_existence()
        self.assertEqual(restarted, ["c-9"])
        call_args = [c[0][0] for c in rc.call_args_list]
        self.assertTrue(any("restart" in args and "c-9" in args for args in call_args),
                        f"expected restart call for c-9, got: {call_args}")

    def test_multi_conductor_only_missing_ones_restart(self):
        with tempfile.TemporaryDirectory() as td:
            env_a = Path(td) / "a.envrc"
            env_b = Path(td) / "b.envrc"
            env_c = Path(td) / "c.envrc"
            state_a = Path(td) / "state-a"
            state_b = Path(td) / "state-b"
            state_c = Path(td) / "state-c"
            env_a.write_text(f"TELEGRAM_STATE_DIR={state_a}\n")
            env_b.write_text(f"TELEGRAM_STATE_DIR={state_b}\n")
            env_c.write_text(f"TELEGRAM_STATE_DIR={state_c}\n")
            sessions = [
                self._make_conductor("cond-a", env_file=env_a),
                self._make_conductor("cond-b", env_file=env_b),
                self._make_conductor("cond-c", env_file=env_c),
            ]
            # Only cond-a's poller is running
            with mock.patch.object(wd_mod, "list_all_sessions", return_value=sessions), \
                 mock.patch.object(wd_mod, "bun_telegram_state_dirs",
                                   return_value={str(state_a)}):
                restarted = self.wd.check_poller_existence()
        self.assertEqual(sorted(restarted), ["cond-b", "cond-c"])


class TestBunTelegramStateDirs(unittest.TestCase):
    """Unit tests for the helper that extracts TELEGRAM_STATE_DIR from running
    bun processes via /proc/PID/environ."""

    def test_no_matching_processes_returns_empty(self):
        with mock.patch.object(wd_mod, "run_cmd", return_value=(1, "", "")):
            self.assertEqual(wd_mod.bun_telegram_state_dirs(), set())

    def test_extracts_from_proc_environ(self):
        with tempfile.TemporaryDirectory() as td:
            procdir = Path(td) / "12345"
            procdir.mkdir()
            env_bytes = b"FOO=bar\x00TELEGRAM_STATE_DIR=/fake/state/conductor-x\x00BAZ=qux\x00"
            (procdir / "environ").write_bytes(env_bytes)
            with mock.patch.object(wd_mod, "run_cmd",
                                   return_value=(0, "12345 bun telegram start\n", "")):
                dirs = wd_mod.bun_telegram_state_dirs(proc_root=td)
        self.assertEqual(dirs, {"/fake/state/conductor-x"})

    def test_multiple_bun_processes_distinct_dirs(self):
        with tempfile.TemporaryDirectory() as td:
            for pid, sdir in [("111", "/s/one"), ("222", "/s/two")]:
                procdir = Path(td) / pid
                procdir.mkdir()
                (procdir / "environ").write_bytes(
                    f"TELEGRAM_STATE_DIR={sdir}\x00".encode())
            with mock.patch.object(wd_mod, "run_cmd",
                                   return_value=(0, "111 bun telegram\n222 bun telegram\n", "")):
                dirs = wd_mod.bun_telegram_state_dirs(proc_root=td)
        self.assertEqual(dirs, {"/s/one", "/s/two"})

    def test_unreadable_environ_skipped(self):
        with tempfile.TemporaryDirectory() as td:
            # PID with no environ file
            (Path(td) / "999").mkdir()
            with mock.patch.object(wd_mod, "run_cmd",
                                   return_value=(0, "999 bun telegram\n", "")):
                dirs = wd_mod.bun_telegram_state_dirs(proc_root=td)
        self.assertEqual(dirs, set())


class TestWaitingTooLong(unittest.TestCase):
    """Capability B (v1.7.63): nudge child sessions that have been stuck in
    'waiting' state with no pane activity change for > 10 min."""

    def setUp(self):
        self.wd = wd_mod.Watchdog(dry_run=True)

    def _make_child(self, sid="child-1", status="waiting", parent="p-1", profile="personal"):
        return {
            "id": sid,
            "title": "child-work",
            "parent_session_id": parent,
            "profile": profile,
            "status": status,
        }

    def test_first_observation_does_not_nudge(self):
        sess = self._make_child()
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
             mock.patch.object(wd_mod, "fetch_session_output", return_value="hello"):
            nudged = self.wd.check_waiting_too_long(now=1000.0)
        self.assertEqual(nudged, [])
        self.assertIn("child-1", self.wd.waiting_tracker)

    def test_pane_changed_resets_timer(self):
        sess = self._make_child()
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]):
            with mock.patch.object(wd_mod, "fetch_session_output", return_value="pane-A"):
                self.wd.check_waiting_too_long(now=1000.0)
            with mock.patch.object(wd_mod, "fetch_session_output", return_value="pane-B"):
                nudged = self.wd.check_waiting_too_long(now=1000.0 + 660)  # +11 min, but pane changed
        self.assertEqual(nudged, [])

    def test_waiting_over_10min_unchanged_nudges(self):
        sess = self._make_child(sid="child-2")
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
             mock.patch.object(wd_mod, "fetch_session_output", return_value="frozen-pane"):
            self.wd.check_waiting_too_long(now=2000.0)
            nudged = self.wd.check_waiting_too_long(now=2000.0 + 660)
        self.assertEqual(nudged, ["child-2"])

    def test_no_nudge_within_threshold(self):
        sess = self._make_child(sid="child-3")
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
             mock.patch.object(wd_mod, "fetch_session_output", return_value="stable"):
            self.wd.check_waiting_too_long(now=3000.0)
            nudged = self.wd.check_waiting_too_long(now=3000.0 + 300)  # only 5 min
        self.assertEqual(nudged, [])

    def test_no_nudge_for_non_child(self):
        sess = {
            "id": "std-1", "title": "standalone",
            "parent_session_id": "",
            "status": "waiting", "profile": "default",
        }
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
             mock.patch.object(wd_mod, "fetch_session_output", return_value="output"):
            self.wd.check_waiting_too_long(now=4000.0)
            nudged = self.wd.check_waiting_too_long(now=4000.0 + 660)
        self.assertEqual(nudged, [])

    def test_no_nudge_when_status_not_waiting(self):
        sess = self._make_child(sid="child-4", status="running")
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
             mock.patch.object(wd_mod, "fetch_session_output", return_value="output"):
            self.wd.check_waiting_too_long(now=5000.0)
            nudged = self.wd.check_waiting_too_long(now=5000.0 + 660)
        self.assertEqual(nudged, [])

    def test_dedup_1h_per_session(self):
        sess = self._make_child(sid="child-5")
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
             mock.patch.object(wd_mod, "fetch_session_output", return_value="frozen"):
            self.wd.check_waiting_too_long(now=6000.0)
            self.wd.check_waiting_too_long(now=6000.0 + 660)  # nudges
            # 30 min after nudge, still unchanged → dedup blocks
            nudged = self.wd.check_waiting_too_long(now=6000.0 + 660 + 1800)
        self.assertEqual(nudged, [])

    def test_dedup_window_expires(self):
        sess = self._make_child(sid="child-6")
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
             mock.patch.object(wd_mod, "fetch_session_output", return_value="frozen"):
            self.wd.check_waiting_too_long(now=7000.0)
            self.wd.check_waiting_too_long(now=7000.0 + 660)
            # 70 min after first nudge → can re-nudge
            nudged = self.wd.check_waiting_too_long(now=7000.0 + 660 + 4200)
        self.assertEqual(nudged, ["child-6"])

    def test_tracker_cleared_when_session_leaves_waiting(self):
        sess_waiting = self._make_child(sid="child-7")
        sess_running = self._make_child(sid="child-7", status="running")
        with mock.patch.object(wd_mod, "fetch_session_output", return_value="output"):
            with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess_waiting]):
                self.wd.check_waiting_too_long(now=8000.0)
            self.assertIn("child-7", self.wd.waiting_tracker)
            with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess_running]):
                self.wd.check_waiting_too_long(now=8000.0 + 60)
        self.assertNotIn("child-7", self.wd.waiting_tracker)

    def test_live_mode_invokes_session_send(self):
        sess = self._make_child(sid="child-8")
        wd = wd_mod.Watchdog(dry_run=False)
        with mock.patch.object(wd_mod, "list_all_sessions", return_value=[sess]), \
             mock.patch.object(wd_mod, "fetch_session_output", return_value="stuck"), \
             mock.patch.object(wd_mod, "run_cmd", return_value=(0, "", "")) as rc:
            wd.check_waiting_too_long(now=9000.0)
            nudged = wd.check_waiting_too_long(now=9000.0 + 660)
        self.assertEqual(nudged, ["child-8"])
        call_args = [c[0][0] for c in rc.call_args_list]
        found_send = any(
            "session" in args and "send" in args and "child-8" in args
            and wd_mod.WAITING_PATROL_NUDGE_TEXT in args
            for args in call_args
        )
        self.assertTrue(found_send, f"expected session send call, got: {call_args}")


class TestLivenessConfirmation(unittest.TestCase):
    """Issue #1705: a conductor was restarted mid-turn while its agent was alive
    and working. A restart tears the pane down, so the last thing the watchdog does
    before firing one is confirm — right then — that the session really is stuck."""

    def setUp(self):
        self.wd = wd_mod.Watchdog(dry_run=True)
        self.sess = make_sess(sid="cond-live", title="conductor-bruce")

    def _liveness_records(self):
        path = Path(wd_mod.LIVENESS_LOG)
        if not path.exists():
            return []
        return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]

    def _restart_records(self):
        path = Path(wd_mod.RESTART_LOG)
        if not path.exists():
            return []
        return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]

    def test_moving_pane_vetoes_the_restart(self):
        """The #1705 case: status says error, but the pane keeps producing output.
        An agent that is still emitting output is not dead, whatever the status
        reading says — and re-reading the status cannot see this, because a
        content-derived verdict does not move while the agent works."""
        with mock.patch.object(wd_mod, "show_session", return_value=self.sess), \
             mock.patch.object(wd_mod, "fetch_pane_snapshot",
                               side_effect=["…curl running", "…curl finished, next tool"]):
            proceed, record = self.wd._confirm_restart_needed("cond-live", "conductor-bruce", "personal")

        self.assertFalse(proceed, "a pane that is still moving must not be restarted")
        self.assertEqual(record["reason"], "pane_active")
        self.assertTrue(record["pane"]["changed"])

    def test_recovery_during_the_serialization_wait_vetoes_the_restart(self):
        """Restarts are serialized (and cascades revive serially), so the decision
        can execute long after the sample that justified it. By then the session may
        have recovered on its own — the stale half of #1705."""
        recovered = make_sess(sid="cond-live", title="conductor-bruce", status="running")
        with mock.patch.object(wd_mod, "show_session", return_value=recovered), \
             mock.patch.object(wd_mod, "fetch_pane_snapshot", return_value="frozen"):
            proceed, record = self.wd._confirm_restart_needed("cond-live", "conductor-bruce", "personal")

        self.assertFalse(proceed)
        self.assertEqual(record["reason"], "recovered_before_restart")

    def test_frozen_pane_and_two_agreeing_reads_confirm_the_restart(self):
        """The genuine death this watchdog exists for still restarts."""
        with mock.patch.object(wd_mod, "show_session", return_value=self.sess), \
             mock.patch.object(wd_mod, "fetch_pane_snapshot", return_value=""):
            proceed, record = self.wd._confirm_restart_needed("cond-live", "conductor-bruce", "personal")

        self.assertTrue(proceed)
        self.assertEqual(record["reason"], "confirmed_dead")
        self.assertEqual(len(record["reads"]), 2, "the decision must rest on two status reads")

    def test_unreadable_status_is_not_treated_as_death(self):
        with mock.patch.object(wd_mod, "show_session", return_value=None), \
             mock.patch.object(wd_mod, "fetch_pane_snapshot", return_value="x"):
            proceed, record = self.wd._confirm_restart_needed("cond-live", "conductor-bruce", "personal")

        self.assertFalse(proceed, "if we cannot see the session we cannot claim it is dead")
        self.assertEqual(record["reason"], "status_unreadable")

    def test_shutdown_mid_sample_leaves_the_session_alone(self):
        self.wd.stop_event.set()
        try:
            with mock.patch.object(wd_mod, "show_session", return_value=self.sess), \
                 mock.patch.object(wd_mod, "fetch_pane_snapshot", return_value="frozen"):
                proceed, record = self.wd._confirm_restart_needed(
                    "cond-live", "conductor-bruce", "personal")
        finally:
            self.wd.stop_event.clear()

        self.assertFalse(proceed, "shutting down is the worst time to be wrong about a pane")
        self.assertEqual(record["reason"], "shutting_down")

    def test_auth_held_session_is_left_alone_and_escalated(self):
        """Merged auth-hold policy: a restart cannot fix a credential, and each
        doomed boot races the shared rotating refresh token."""
        held = dict(self.sess)
        held["substate"] = "auth-401"
        held["auth_hold"] = {
            "reason": "auth_death",
            "remedy": "run /login, then restart it explicitly",
            "evidence": "API Error: 401 <pane tail>",
        }
        with mock.patch.object(wd_mod, "show_session", return_value=held), \
             mock.patch.object(wd_mod, "fetch_pane_snapshot", return_value="") as pane, \
             mock.patch.object(wd_mod, "telegram_send", return_value=True) as tg:
            proceed, record = self.wd._confirm_restart_needed(
                "cond-live", "conductor-bruce", "personal", escalate_critical=True)

        self.assertFalse(proceed)
        self.assertEqual(record["reason"], "auth_hold")
        self.assertEqual(record["auth_hold_reason"], "auth_death")
        pane.assert_not_called()
        tg.assert_called_once()
        self.assertIn("auth-hold", Path(wd_mod.ESCALATIONS_LOG).read_text())

    def test_record_carries_digests_not_pane_text(self):
        """The record is written to be read by humans and pasted into bug reports,
        so it must never carry pane content, prompts or secrets."""
        private = "DO-NOT-LOG-THIS-PANE-TEXT and this conversation content"
        with mock.patch.object(wd_mod, "show_session", return_value=self.sess), \
             mock.patch.object(wd_mod, "fetch_pane_snapshot", side_effect=[private, private + "!"]):
            _, record = self.wd._confirm_restart_needed("cond-live", "conductor-bruce", "personal")

        serialized = json.dumps(record)
        self.assertNotIn("DO-NOT-LOG-THIS-PANE-TEXT", serialized)
        self.assertNotIn("conversation content", serialized)
        self.assertEqual(len(record["pane"]["first"]), 16)
        self.assertNotEqual(record["pane"]["first"], record["pane"]["second"])

        persisted = self._liveness_records()[-1]
        self.assertEqual(persisted["reason"], "pane_active", "every decision is recorded")
        self.assertEqual(persisted["pane"], record["pane"])
        self.assertNotIn("DO-NOT-LOG-THIS-PANE-TEXT", json.dumps(persisted))

    def test_skipped_restart_does_not_burn_the_rate_limit_budget(self):
        """A restart that never happened is not an attempt: otherwise a live
        session the watchdog correctly declined to touch would exhaust its
        3-per-10-min budget and escalate as "keeps crashing"."""
        moving = ["frame-%d" % i for i in range(40)]
        with mock.patch.object(wd_mod, "show_session", return_value=self.sess), \
             mock.patch.object(wd_mod, "fetch_pane_snapshot", side_effect=moving), \
             mock.patch.object(wd_mod, "telegram_send", return_value=True):
            for _ in range(wd_mod.RATE_LIMIT_MAX + 1):
                self.wd.cooldown_until.pop("cond-live", None)
                self.wd.maybe_restart(self.sess)

        self.assertEqual(len(self.wd.restart_history.get("cond-live", [])), 0)
        skipped = [r for r in self._restart_records() if r.get("action") == "skipped-alive"]
        self.assertTrue(skipped, "the skip is auditable from the restart log too")
        self.assertNotIn("keeps crashing", Path(wd_mod.ESCALATIONS_LOG).read_text())

    def test_repeated_alive_skips_escalate_as_a_classification_problem(self):
        moving = ["frame-%d" % i for i in range(40)]
        with mock.patch.object(wd_mod, "show_session", return_value=self.sess), \
             mock.patch.object(wd_mod, "fetch_pane_snapshot", side_effect=moving), \
             mock.patch.object(wd_mod, "telegram_send", return_value=True) as tg:
            for _ in range(wd_mod.LIVENESS_SKIP_ESCALATE_AFTER):
                self.wd._confirm_restart_needed(
                    "cond-live", "conductor-bruce", "personal", escalate_critical=True)

        tg.assert_called_once()
        self.assertIn("liveness-mismatch", Path(wd_mod.ESCALATIONS_LOG).read_text())

    def test_auth_hold_is_reported_once_per_episode_not_once_per_poll(self):
        """A hold only a human can clear is observed on every poll for as long as it
        lasts. Repeating the alert every few minutes is how a channel stops being
        read — so it fires once, and again only after the session is seen healthy."""
        held = dict(self.sess)
        held["auth_hold"] = {"reason": "auth_death", "remedy": "run /login"}
        healthy = make_sess(sid="cond-live", title="conductor-bruce", status="running")

        with mock.patch.object(wd_mod, "telegram_send", return_value=True) as tg:
            with mock.patch.object(wd_mod, "show_session", return_value=held):
                for _ in range(4):
                    self.wd._confirm_restart_needed(
                        "cond-live", "conductor-bruce", "personal", escalate_critical=True)
            self.assertEqual(tg.call_count, 1, "one alert per episode")

            # Seen healthy → episode over, counter cleared.
            with mock.patch.object(wd_mod, "show_session", return_value=healthy):
                self.wd.maybe_restart(healthy)
            self.assertNotIn("cond-live", self.wd.liveness_skips)

            # It comes back: that is a new episode and does speak up again.
            self.wd.last_escalation_at.clear()  # past the 5-min escalation dedup
            with mock.patch.object(wd_mod, "show_session", return_value=held):
                self.wd._confirm_restart_needed(
                    "cond-live", "conductor-bruce", "personal", escalate_critical=True)
            self.assertEqual(tg.call_count, 2)

    def test_confirmation_runs_after_the_serialization_wait(self):
        """Ordering is the whole point: confirming before the wait would confirm a
        session that has been sitting in a restart queue ever since."""
        events = []
        wd = wd_mod.Watchdog(dry_run=True)

        def _tracking_wait(timeout=None):
            events.append(("wait", timeout))
            return False  # never "stopped"; return immediately instead of sleeping

        def _tracking_sample(sid, profile):
            events.append(("sample", None))
            return "frozen"

        saved_interval = wd_mod.MIN_GLOBAL_RESTART_INTERVAL_S
        wd_mod.MIN_GLOBAL_RESTART_INTERVAL_S = 30
        try:
            wd.last_global_restart_at = time.time()
            with mock.patch.object(wd.stop_event, "wait", side_effect=_tracking_wait), \
                 mock.patch.object(wd_mod, "show_session", return_value=self.sess), \
                 mock.patch.object(wd_mod, "fetch_pane_snapshot", side_effect=_tracking_sample):
                wd._do_restart("cond-live", "conductor-bruce", "personal")
        finally:
            wd_mod.MIN_GLOBAL_RESTART_INTERVAL_S = saved_interval

        self.assertTrue(events, "expected the restart path to both wait and sample")
        kind, timeout = events[0]
        self.assertEqual(kind, "wait")
        self.assertGreater(timeout, 1.0, "the first wait must be the serialization wait")
        self.assertIn(
            "sample", [k for k, _ in events],
            "the liveness sample must be taken after the serialization wait, not before",
        )


class TestCliRefusedRestart(unittest.TestCase):
    """A guard inside agent-deck (auth hold, or the #30 freshness guard) answers
    `session restart` with success + skipped=true. Falling through to
    `session start` would defeat exactly the guard that just fired."""

    def test_refusal_does_not_fall_back_to_session_start(self):
        wd = wd_mod.Watchdog(dry_run=False)
        sess = make_sess(sid="cond-refused", title="conductor-bruce")
        refusal = json.dumps({
            "success": True, "skipped": True,
            "reason": "the agent exited because it could not authenticate — "
                      "automatic restarts are held (use --force to restart anyway)",
            "id": "cond-refused", "title": "conductor-bruce",
        })

        calls = []

        def _run_cmd(args, timeout=None):
            calls.append(list(args))
            if "restart" in args:
                return 0, refusal, ""
            return 0, "", ""

        with mock.patch.object(wd_mod, "show_session", return_value=sess), \
             mock.patch.object(wd_mod, "fetch_pane_snapshot", return_value=""), \
             mock.patch.object(wd_mod, "fetch_session_output", return_value=""), \
             mock.patch.object(wd_mod, "run_cmd", side_effect=_run_cmd), \
             mock.patch.object(wd_mod, "telegram_send", return_value=True):
            ok = wd._do_restart("cond-refused", "conductor-bruce", "personal")

        self.assertFalse(ok)
        self.assertFalse(
            any("start" in args for args in calls),
            f"a deliberate refusal must not be escalated to `session start`: {calls}",
        )
        self.assertFalse(
            any("send" in args for args in calls),
            "no continuity message for a session that was never restarted",
        )
        self.assertEqual(wd.consecutive_failures.get("cond-refused", 0), 0,
                         "someone else's guard firing is not our restart failing")


if __name__ == "__main__":
    unittest.main(verbosity=2)
