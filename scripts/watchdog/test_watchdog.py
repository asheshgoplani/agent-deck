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


if __name__ == "__main__":
    unittest.main(verbosity=2)
