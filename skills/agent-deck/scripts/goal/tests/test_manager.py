#!/usr/bin/env python3
"""Unit tests for the goal manager.

Run from anywhere:
    python3 -m unittest discover -s tests -v

Or from this directory:
    python3 -m unittest test_manager -v

Tests cover each path in the manager loop:
  - parse_task_log_tail: receipts in / out of expected format
  - newer_than: ISO timestamp comparison
  - build_nudge: nudge text generation with edge cases (no prior receipt)
  - run_done_cmd: verifier exit codes
  - walk_goal: all four state transitions
      a. verifier passes → finalize as done
      b. new receipt → record cycle, reset nudge counter
      c. idle past max_idle → nudge attempt counted
      d. nudge count reaches escalate_after → escalation bundle written
  - STUCK marker in receipt → immediate escalation (no further nudging)

External dependencies (agent-deck CLI) are stubbed by patching the
subprocess wrappers.
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import patch

# Make the manager importable. tests/ is a sibling of manager.py
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))

import manager  # noqa: E402


def iso_minutes_ago(n: int) -> str:
    return (datetime.now(timezone.utc) - timedelta(minutes=n)).isoformat()


def make_goal(
    *,
    pid: str = "test",
    done_cmd: str = "false",
    workdir: str | None = None,
    max_idle: int = 60,
    escalate_after: int = 2,
    max_cycles: int = 5,
    created_minutes_ago: int = 10,
    last_receipt_at: str | None = None,
    last_receipt_text: str | None = None,
    nudges_sent: int = 0,
    cycles_completed: int = 0,
    status: str = "active",
) -> dict:
    # Default workdir is a unique non-existent path so parse_task_log_tail
    # finds no receipts. Tests that DO need a real task-log.md pass workdir
    # explicitly (set to a real directory with the file inside).
    if workdir is None:
        workdir = f"/tmp/test-no-tasklog-{pid}-{uuid.uuid4().hex[:8]}"
    return {
        "id": pid,
        "goal": f"test goal {pid}",
        "done_cmd": done_cmd,
        "worker_session_title": f"worker-{pid}",
        "worker_session_id": f"id-{pid}",
        "workdir": workdir,
        "conductor": "test",
        "schedule": {
            "check_interval_seconds": 60,
            "max_idle_seconds": max_idle,
            "max_cycles": max_cycles,
            "escalate_after_stuck_nudges": escalate_after,
        },
        "state": {
            "status": status,
            "created_at": iso_minutes_ago(created_minutes_ago),
            "last_verified_at": None,
            "last_receipt_seen_at": last_receipt_at,
            "last_receipt_text": last_receipt_text,
            "cycles_completed": cycles_completed,
            "nudges_sent": nudges_sent,
            "escalated_at": None,
            "ended_at": None,
            "ended_reason": None,
        },
        "history": [],
    }


class TestReceiptParser(unittest.TestCase):
    """parse_task_log_tail correctly extracts receipts from a real-looking task-log.md."""

    def test_empty_file(self):
        with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as fh:
            fh.write("")
            path = Path(fh.name)
        self.addCleanup(path.unlink)
        receipts = manager.parse_task_log_tail(path)
        self.assertEqual(receipts, [])

    def test_missing_file_returns_empty(self):
        receipts = manager.parse_task_log_tail(Path("/tmp/no-such-file-xyz.md"))
        self.assertEqual(receipts, [])

    def test_single_well_formed_receipt(self):
        body = (
            "# Task log\n\n"
            "## 2026-05-15T10:30:00Z\n"
            "- cycle: 1\n"
            "- changed: added empty task-log\n"
            "- next: file the first PR\n"
            "- blockers: none\n"
        )
        with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as fh:
            fh.write(body)
            path = Path(fh.name)
        self.addCleanup(path.unlink)
        receipts = manager.parse_task_log_tail(path)
        self.assertEqual(len(receipts), 1)
        self.assertEqual(receipts[0].ts, "2026-05-15T10:30:00Z")
        self.assertIn("cycle: 1", receipts[0].body)

    def test_multiple_receipts_in_order(self):
        body = (
            "## 2026-05-15T10:00:00Z\n- cycle: 1\n- changed: a\n\n"
            "## 2026-05-15T10:30:00Z\n- cycle: 2\n- changed: b\n\n"
            "## 2026-05-15T11:00:00Z\n- cycle: 3\n- changed: c\n"
        )
        with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as fh:
            fh.write(body)
            path = Path(fh.name)
        self.addCleanup(path.unlink)
        receipts = manager.parse_task_log_tail(path)
        self.assertEqual(len(receipts), 3)
        self.assertEqual([r.ts for r in receipts], [
            "2026-05-15T10:00:00Z",
            "2026-05-15T10:30:00Z",
            "2026-05-15T11:00:00Z",
        ])

    def test_drift_in_heading_silently_dropped(self):
        # If the worker drifts to ### or wrong format, we want to know it
        # silently drops — this test documents that behavior. The fix
        # belongs in the worker contract, not the parser.
        body = (
            "### 2026-05-15T10:30:00Z\n- cycle: 1\n"
            "##  2026-05-15T11:00:00Z\n- cycle: 2\n"  # two spaces after ##
        )
        with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as fh:
            fh.write(body)
            path = Path(fh.name)
        self.addCleanup(path.unlink)
        receipts = manager.parse_task_log_tail(path)
        # Strict regex: neither matches. Documents that drift = silent skip.
        self.assertEqual(receipts, [])

    def test_stuck_marker_preserved_in_body(self):
        body = (
            "## 2026-05-15T11:00:00Z\n"
            "- STUCK: workspace-mcp returns 401 even with refreshed token\n"
            "- context: see /tmp/mcp-debug.log\n"
        )
        with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False) as fh:
            fh.write(body)
            path = Path(fh.name)
        self.addCleanup(path.unlink)
        receipts = manager.parse_task_log_tail(path)
        self.assertEqual(len(receipts), 1)
        self.assertIn("STUCK:", receipts[0].body)


class TestNewerThan(unittest.TestCase):
    def test_ts_a_newer(self):
        self.assertTrue(manager.newer_than("2026-05-15T11:00:00Z", "2026-05-15T10:00:00Z"))

    def test_ts_a_older(self):
        self.assertFalse(manager.newer_than("2026-05-15T10:00:00Z", "2026-05-15T11:00:00Z"))

    def test_ts_b_none(self):
        self.assertTrue(manager.newer_than("2026-05-15T10:00:00Z", None))

    def test_ts_equal(self):
        ts = "2026-05-15T10:00:00Z"
        self.assertFalse(manager.newer_than(ts, ts))


class TestBuildNudge(unittest.TestCase):
    def test_no_prior_receipt_handles_none(self):
        p = make_goal(last_receipt_text=None)
        text = manager.build_nudge(p, idle_minutes=120)
        self.assertIn("no prior receipt", text.lower())
        self.assertIn("120 minutes", text)
        self.assertIn("test goal test", text)

    def test_includes_three_options(self):
        p = make_goal(last_receipt_text="changed: applied patch X")
        text = manager.build_nudge(p, idle_minutes=65)
        self.assertIn("a)", text)
        self.assertIn("b)", text)
        self.assertIn("c)", text)
        # The (c) option must mention STUCK so the worker has an out
        self.assertIn("STUCK", text)


class TestRunDoneCmd(unittest.TestCase):
    def test_passes_exit_zero(self):
        rc, out = manager.run_done_cmd("true")
        self.assertEqual(rc, 0)

    def test_fails_non_zero(self):
        rc, out = manager.run_done_cmd("false")
        self.assertNotEqual(rc, 0)

    def test_captures_output(self):
        rc, out = manager.run_done_cmd("echo hello world")
        self.assertEqual(rc, 0)
        self.assertIn("hello world", out)

    def test_timeout(self):
        rc, out = manager.run_done_cmd("sleep 5", timeout_s=1)
        self.assertEqual(rc, 124)
        self.assertIn("timed out", out)


class TestWalkGoal(unittest.TestCase):
    """Integration tests that exercise walk_goal end-to-end.

    External commands (agent-deck send/stop) are patched so tests don't
    need a real agent-deck binary or workers.
    """

    def setUp(self):
        # Per-test sandbox under /tmp
        self.tmpdir = Path(tempfile.mkdtemp(prefix="goal-test-"))
        self.goals_dir = self.tmpdir / "goals"
        self.goals_dir.mkdir()
        # Redirect manager's module globals to point at our sandbox
        self._orig_GOALS = manager.GOALS_DIR
        self._orig_ESCALATIONS = manager.ESCALATIONS_DIR
        self._orig_HISTORY = manager.HISTORY_DIR
        manager.GOALS_DIR = self.goals_dir
        manager.ESCALATIONS_DIR = self.goals_dir / "escalations"
        manager.HISTORY_DIR = self.goals_dir / "history"

    def tearDown(self):
        import shutil
        shutil.rmtree(self.tmpdir, ignore_errors=True)
        manager.GOALS_DIR = self._orig_GOALS
        manager.ESCALATIONS_DIR = self._orig_ESCALATIONS
        manager.HISTORY_DIR = self._orig_HISTORY

    def _write_goal(self, p: dict) -> Path:
        path = self.goals_dir / f"{p['id']}.json"
        path.write_text(json.dumps(p, indent=2))
        return path

    def _load(self, p_id: str) -> dict:
        return json.loads((self.goals_dir / f"{p_id}.json").read_text())

    @patch("manager.agent_deck_stop", return_value=True)
    def test_verifier_pass_finalizes_done(self, mock_stop):
        path = self._write_goal(make_goal(pid="vp", done_cmd="true"))
        manager.walk_goal(path, dry_run=False, verbose=False)
        loaded = self._load("vp")
        self.assertEqual(loaded["state"]["status"], "done")
        self.assertEqual(loaded["state"]["ended_reason"], "verifier passed")
        mock_stop.assert_called_once()
        # history artifact written
        self.assertTrue((manager.HISTORY_DIR / "vp-done.md").exists())

    @patch("manager.agent_deck_send", return_value=True)
    @patch("manager.agent_deck_stop", return_value=True)
    def test_idle_triggers_nudge_attempt(self, _mock_stop, mock_send):
        p = make_goal(
            pid="idle",
            done_cmd="false",
            max_idle=10,         # 10s idle threshold
            escalate_after=99,    # don't escalate yet
            created_minutes_ago=10,
        )
        path = self._write_goal(p)
        manager.walk_goal(path, dry_run=False, verbose=False)
        loaded = self._load("idle")
        self.assertEqual(loaded["state"]["nudges_sent"], 1)
        mock_send.assert_called_once()

    @patch("manager.agent_deck_send", return_value=False)  # nudge can't send
    @patch("manager.agent_deck_stop", return_value=True)
    def test_failed_nudges_still_count_and_escalate(self, _stop, _send):
        p = make_goal(
            pid="esc",
            done_cmd="false",
            max_idle=10,
            escalate_after=2,
            nudges_sent=1,        # one already attempted
            created_minutes_ago=10,
        )
        path = self._write_goal(p)
        manager.walk_goal(path, dry_run=False, verbose=False)
        loaded = self._load("esc")
        self.assertEqual(loaded["state"]["status"], "escalated")
        self.assertEqual(loaded["state"]["nudges_sent"], 2)
        # bundle on disk
        bundles = list(manager.ESCALATIONS_DIR.glob("esc-*.md"))
        self.assertEqual(len(bundles), 1)
        text = bundles[0].read_text()
        self.assertIn("Goal escalation", text)
        self.assertIn("test goal esc", text)

    @patch("manager.agent_deck_send", return_value=True)
    @patch("manager.agent_deck_stop", return_value=True)
    def test_new_receipt_resets_nudge_counter(self, _stop, _send):
        # Worker has produced a receipt newer than what manager last saw
        receipt_ts = iso_minutes_ago(1)
        body = (
            "# task log\n\n"
            f"## {receipt_ts}\n"
            "- cycle: 1\n"
            "- changed: applied patch\n"
            "- next: run tests\n"
            "- blockers: none\n"
        )
        workdir = self.tmpdir / "workdir"
        workdir.mkdir()
        (workdir / "task-log.md").write_text(body)

        p = make_goal(
            pid="recv",
            done_cmd="false",
            workdir=str(workdir),
            last_receipt_at=iso_minutes_ago(60),  # pre-existing "old" receipt mark
            nudges_sent=1,                          # nudge already sent
            max_idle=99999,
        )
        path = self._write_goal(p)
        manager.walk_goal(path, dry_run=False, verbose=False)
        loaded = self._load("recv")
        self.assertEqual(loaded["state"]["cycles_completed"], 1)
        self.assertEqual(loaded["state"]["nudges_sent"], 0)  # reset!
        self.assertIn("applied patch", loaded["state"]["last_receipt_text"])

    @patch("manager.agent_deck_send", return_value=True)
    @patch("manager.agent_deck_stop", return_value=True)
    def test_stuck_marker_in_receipt_escalates_immediately(self, _stop, _send):
        receipt_ts = iso_minutes_ago(1)
        body = (
            f"## {receipt_ts}\n"
            "- STUCK: workspace-mcp returns 401 even after token refresh\n"
            "- context: see /tmp/mcp-debug.log lines 240-310\n"
        )
        workdir = self.tmpdir / "workdir"
        workdir.mkdir()
        (workdir / "task-log.md").write_text(body)

        p = make_goal(
            pid="stuck",
            done_cmd="false",
            workdir=str(workdir),
            last_receipt_at=iso_minutes_ago(60),
            max_idle=99999,
        )
        path = self._write_goal(p)
        manager.walk_goal(path, dry_run=False, verbose=False)
        loaded = self._load("stuck")
        # STUCK detection should escalate immediately, regardless of nudge count
        self.assertEqual(loaded["state"]["status"], "escalated")
        # Reason should reference the worker's STUCK line
        self.assertIn("STUCK", loaded["state"]["ended_reason"])
        bundles = list(manager.ESCALATIONS_DIR.glob("stuck-*.md"))
        self.assertEqual(len(bundles), 1)

    @patch("manager.agent_deck_send", return_value=True)
    @patch("manager.agent_deck_stop", return_value=True)
    def test_max_cycles_exceeded_fails(self, _stop, _send):
        # cycles_completed already at the cap; next run should fail
        receipt_ts = iso_minutes_ago(1)
        body = f"## {receipt_ts}\n- cycle: 6\n- changed: nothing useful\n"
        workdir = self.tmpdir / "workdir"
        workdir.mkdir()
        (workdir / "task-log.md").write_text(body)

        p = make_goal(
            pid="cap",
            done_cmd="false",
            workdir=str(workdir),
            max_cycles=2,
            cycles_completed=1,        # one less than cap; this run pushes us over
            last_receipt_at=iso_minutes_ago(60),
            max_idle=99999,
        )
        path = self._write_goal(p)
        manager.walk_goal(path, dry_run=False, verbose=False)
        loaded = self._load("cap")
        self.assertEqual(loaded["state"]["status"], "failed")
        self.assertEqual(loaded["state"]["ended_reason"], "max_cycles_exceeded")


class TestSkipInactive(unittest.TestCase):
    def setUp(self):
        self.tmpdir = Path(tempfile.mkdtemp(prefix="goal-inactive-"))
        self.goals_dir = self.tmpdir / "goals"
        self.goals_dir.mkdir()
        self._orig = manager.GOALS_DIR
        manager.GOALS_DIR = self.goals_dir

    def tearDown(self):
        import shutil
        shutil.rmtree(self.tmpdir, ignore_errors=True)
        manager.GOALS_DIR = self._orig

    def test_done_goal_not_walked(self):
        p = make_goal(pid="already-done", status="done")
        path = self.goals_dir / "already-done.json"
        path.write_text(json.dumps(p))
        # Should not raise even with a bogus done_cmd
        manager.walk_goal(path, dry_run=False, verbose=False)
        loaded = json.loads(path.read_text())
        self.assertEqual(loaded["state"]["status"], "done")


class TestScheduleWakeup_RetryOn5xx_LogsAndBackoffs_RegressionFor976(unittest.TestCase):
    """Regression for issue #976.

    The autonomous ScheduleWakeup loop silently stalled for hours when a
    transient upstream failure (Anthropic API 5xx) hit the wake-up that
    drives the worker. agent_deck_send is the in-repo seam that delivers
    that wake-up: if it can't reach the worker on the first try, the loop
    needs to retry with exponential backoff and log every attempt so the
    failure is observable instead of silent.

    Contract under test:
      - 3 attempts total before giving up
      - exponential backoff between attempts: BACKOFF_SECONDS = (1, 5, 30)
      - every attempt logged with the reason for that attempt
      - returns True once a retry succeeds (no spurious failure surface)
    """

    def setUp(self):
        self._sleeps: list[float] = []
        self._sleep_patch = patch("manager.time.sleep", side_effect=self._sleeps.append)
        self._sleep_patch.start()

    def tearDown(self):
        self._sleep_patch.stop()

    @staticmethod
    def _fail(stderr: str) -> subprocess.CompletedProcess:
        return subprocess.CompletedProcess(args=[], returncode=1, stdout="", stderr=stderr)

    @staticmethod
    def _ok() -> subprocess.CompletedProcess:
        return subprocess.CompletedProcess(args=[], returncode=0, stdout="ok\n", stderr="")

    def test_retries_three_times_with_backoff_and_logs(self):
        attempts = [
            self._fail("upstream returned HTTP 503"),
            self._fail("upstream returned HTTP 502"),
            self._ok(),
        ]
        with patch("manager.subprocess.run", side_effect=attempts) as mock_run, \
             patch("manager.log_attempt") as mock_log:
            result = manager.agent_deck_send(
                "worker-session", "wake up", dry_run=False, verbose=False
            )

        self.assertTrue(result, "third attempt succeeded; agent_deck_send must return True")
        self.assertEqual(mock_run.call_count, 3, "should retry until success (max 3 attempts)")
        # Backoff before retries — two sleeps total (after attempt 1 and after attempt 2)
        self.assertEqual(
            self._sleeps,
            [manager.BACKOFF_SECONDS[0], manager.BACKOFF_SECONDS[1]],
            "exponential backoff must use [1s, 5s] before retries",
        )
        self.assertEqual(manager.BACKOFF_SECONDS, (1, 5, 30),
                         "backoff schedule must be the documented [1s, 5s, 30s]")
        # Every attempt is logged with a reason — that's the observability fix.
        self.assertEqual(mock_log.call_count, 3, "every attempt must be logged")
        reasons = [c.kwargs.get("reason") or c.args[-1] for c in mock_log.call_args_list]
        self.assertIn("HTTP 503", reasons[0])
        self.assertIn("HTTP 502", reasons[1])

    def test_gives_up_after_three_failures_and_logs_each(self):
        attempts = [self._fail(f"HTTP 50{i}") for i in (0, 2, 3)]
        with patch("manager.subprocess.run", side_effect=attempts) as mock_run, \
             patch("manager.log_attempt") as mock_log:
            result = manager.agent_deck_send(
                "worker-session", "wake up", dry_run=False, verbose=False
            )

        self.assertFalse(result, "all 3 attempts failed; must surface failure")
        self.assertEqual(mock_run.call_count, 3, "must not exceed 3 attempts")
        self.assertEqual(mock_log.call_count, 3, "every attempt logged, even on terminal failure")
        # Backoff applied between attempts 1->2 and 2->3, but not after the last fail.
        self.assertEqual(self._sleeps, [manager.BACKOFF_SECONDS[0], manager.BACKOFF_SECONDS[1]])


if __name__ == "__main__":
    unittest.main(verbosity=2)
