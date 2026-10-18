#!/usr/bin/env python3
"""Tests for the opt-in goal progress features.

No-progress cap: a due idle wake without progress is an empty turn; after
no_progress_cap empty turns the goal escalates (or, with
no_progress_action "stop", ends as failed and stops the worker); each wake
waits a doubling backoff window. A receipt is always progress; with
worktree_progress a worktree content change is too.

Gate skip: done_cmd is not rerun while the worktree hash equals the one the
verifier last failed on, and with the cap on a wake is skipped after 2
delivered wakes over that same worktree.

A goal file without the opt-in keys runs the loop from before they existed.

Run with pytest or unittest; git must be on PATH.
"""
from __future__ import annotations

import json
import os
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))

import manager  # noqa: E402


def iso_ago(seconds: float) -> str:
    return (datetime.now(timezone.utc) - timedelta(seconds=seconds)).isoformat()


def git(cwd: Path, *args: str) -> str:
    r = subprocess.run(["git", "-C", str(cwd), *args], capture_output=True, text=True, check=True)
    return r.stdout


def init_repo(path: Path) -> Path:
    path.mkdir(parents=True)
    git(path, "init", "-q")
    git(path, "config", "user.email", "t@example.com")
    git(path, "config", "user.name", "t")
    (path / "main.txt").write_text("v1\n")
    git(path, "add", "main.txt")
    git(path, "commit", "-q", "-m", "init")
    return path


class GoalSandbox(unittest.TestCase):
    """Redirects the manager's goal dirs into a temp dir and counts calls."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="goal-progress-"))
        self.goals = self.tmp / "goals"
        self.goals.mkdir()
        self.counter = self.tmp / "done-runs"
        self._saved = (manager.GOALS_DIR, manager.ESCALATIONS_DIR, manager.HISTORY_DIR)
        manager.GOALS_DIR = self.goals
        manager.ESCALATIONS_DIR = self.goals / "escalations"
        manager.HISTORY_DIR = self.goals / "history"
        self.sends: list[str] = []
        self.stops: list[str] = []
        p1 = patch("manager.agent_deck_send", side_effect=lambda t, m, d, v: self.sends.append(m) or True)
        p2 = patch("manager.agent_deck_stop", side_effect=lambda t, d, v: self.stops.append(t) or True)
        p1.start()
        p2.start()
        self.addCleanup(p1.stop)
        self.addCleanup(p2.stop)

    def tearDown(self):
        manager.GOALS_DIR, manager.ESCALATIONS_DIR, manager.HISTORY_DIR = self._saved
        shutil.rmtree(self.tmp, ignore_errors=True)

    def counting_done_cmd(self, extra: str = "false") -> str:
        """A done_cmd that records each run outside the worktree."""
        return f"echo run >> {self.counter}; {extra}"

    def done_runs(self) -> int:
        return len(self.counter.read_text().splitlines()) if self.counter.exists() else 0

    def write_goal(self, *, workdir: Path, done_cmd: str | None = None, cap: int | None = 3,
                   base: int = 600, max_idle: int = 60, escalate_after: int = 99,
                   max_cycles: int = 99, gate: bool | None = True, idle_for: int = 600,
                   progress: bool | None = None, action: str | None = None,
                   state: dict | None = None) -> Path:
        schedule = {
            "check_interval_seconds": 60,
            "max_idle_seconds": max_idle,
            "max_cycles": max_cycles,
            "escalate_after_stuck_nudges": escalate_after,
            "no_progress_backoff_base_seconds": base,
        }
        if cap is not None:
            schedule["no_progress_cap"] = cap
        if gate is not None:
            schedule["worktree_gate"] = gate
        if progress is not None:
            schedule["worktree_progress"] = progress
        if action is not None:
            schedule["no_progress_action"] = action
        goal = {
            "id": "g",
            "goal": "make it pass",
            "done_cmd": done_cmd or self.counting_done_cmd(),
            "worker_session_title": "worker-g",
            "worker_session_id": "id-g",
            "workdir": str(workdir),
            "conductor": "test",
            "schedule": schedule,
            "state": {
                "status": "active",
                "created_at": iso_ago(idle_for),
                "last_verified_at": None,
                "last_receipt_seen_at": None,
                "last_receipt_text": None,
                "cycles_completed": 0,
                "nudges_sent": 0,
                "escalated_at": None,
                "ended_at": None,
                "ended_reason": None,
                **(state or {}),
            },
            "history": [],
        }
        path = self.goals / "g.json"
        path.write_text(json.dumps(goal))
        return path

    def tick(self, path: Path) -> dict:
        manager.walk_goal(path, dry_run=False, verbose=False)
        return json.loads(path.read_text())

    def expire_backoff(self, path: Path) -> None:
        """Fast-forward: the armed backoff window has elapsed."""
        goal = json.loads(path.read_text())
        if goal["state"].get("next_wake_at"):
            goal["state"]["next_wake_at"] = iso_ago(1)
            path.write_text(json.dumps(goal))

    def add_receipt(self, workdir: Path, n: int) -> None:
        ts = (datetime.now(timezone.utc) - timedelta(seconds=30) + timedelta(milliseconds=n)).isoformat()
        with (workdir / "task-log.md").open("a") as f:
            f.write(f"\n## {ts}\n- cycle: {n}\n- changed: nothing\n")

    def events(self, goal: dict) -> list[str]:
        return [e["event"] for e in goal["history"]]


class TestNoProgressCap(GoalSandbox):
    def test_three_empty_wake_turns_escalate_by_default(self):
        workdir = self.tmp / "plain"  # not a git repo: receipts are the progress signal
        workdir.mkdir()
        path = self.write_goal(workdir=workdir)
        for _ in range(2):
            self.tick(path)
            self.expire_backoff(path)
        g = self.tick(path)
        self.assertEqual(g["state"]["status"], "escalated")
        self.assertIsNone(g["state"]["ended_reason"])
        self.assertIn("no_progress_cap", self.events(g))
        self.assertEqual(self.stops, [], "escalating leaves the worker running")
        self.assertFalse((manager.HISTORY_DIR / "g-failed.md").exists())
        bundles = list(manager.ESCALATIONS_DIR.glob("g-*.md"))
        self.assertEqual(len(bundles), 1)
        self.assertIn("worker left running", bundles[0].read_text())

    def test_unknown_action_escalates(self):
        workdir = self.tmp / "plain"
        workdir.mkdir()
        path = self.write_goal(workdir=workdir, cap=1, action="kill")
        g = self.tick(path)
        self.assertEqual(g["state"]["status"], "escalated")
        self.assertEqual(self.stops, [])

    def test_three_empty_wake_turns_end_goal_with_stop_action(self):
        workdir = self.tmp / "plain"
        workdir.mkdir()
        path = self.write_goal(workdir=workdir, action="stop")

        g = self.tick(path)
        self.assertEqual(g["state"]["no_progress_streak"], 1)
        self.assertEqual(len(self.sends), 1)

        g = self.tick(path)  # backoff window not elapsed: no wake, no new turn
        self.assertEqual(g["state"]["no_progress_streak"], 1)
        self.assertEqual(len(self.sends), 1)

        self.expire_backoff(path)
        g = self.tick(path)
        self.assertEqual(g["state"]["no_progress_streak"], 2)
        self.assertEqual(len(self.sends), 2)

        self.expire_backoff(path)
        g = self.tick(path)
        self.assertEqual(g["state"]["status"], "failed")
        self.assertEqual(g["state"]["ended_reason"], "no_progress_cap")
        self.assertEqual(len(self.sends), 2, "the capping turn must not wake the worker")
        self.assertEqual(self.stops, ["worker-g"])
        self.assertTrue((manager.HISTORY_DIR / "g-failed.md").exists())
        bundles = list(manager.ESCALATIONS_DIR.glob("g-*.md"))
        self.assertEqual(len(bundles), 1, "the cap pages the user with an escalation bundle")
        self.assertIn("no_progress_cap", bundles[0].read_text())

    def test_backoff_doubles_before_each_wake(self):
        workdir = self.tmp / "plain"
        workdir.mkdir()
        path = self.write_goal(workdir=workdir, cap=5, base=600)
        windows = []
        for _ in range(3):
            before = datetime.now(timezone.utc)
            g = self.tick(path)
            nxt = datetime.fromisoformat(g["state"]["next_wake_at"])
            windows.append(round((nxt - before).total_seconds() / 60) * 60)
            self.expire_backoff(path)
        self.assertEqual(windows, [600, 1200, 2400])

    def test_backoff_base_defaults_to_max_idle(self):
        workdir = self.tmp / "plain"
        workdir.mkdir()
        path = self.write_goal(workdir=workdir, max_idle=900, idle_for=1000)
        goal = json.loads(path.read_text())
        del goal["schedule"]["no_progress_backoff_base_seconds"]
        path.write_text(json.dumps(goal))
        before = datetime.now(timezone.utc)
        g = self.tick(path)
        window = (datetime.fromisoformat(g["state"]["next_wake_at"]) - before).total_seconds()
        self.assertAlmostEqual(window, 900, delta=5)

    def test_receipt_resets_streak_without_git(self):
        workdir = self.tmp / "plain"
        workdir.mkdir()
        path = self.write_goal(workdir=workdir)
        self.tick(path)
        self.expire_backoff(path)
        g = self.tick(path)
        self.assertEqual(g["state"]["no_progress_streak"], 2)
        self.add_receipt(workdir, 1)
        g = self.tick(path)
        self.assertEqual(g["state"]["no_progress_streak"], 0)
        self.assertIsNone(g["state"]["next_wake_at"])
        self.assertEqual(g["state"]["status"], "active")

    def test_backoff_is_clamped(self):
        """Review N2: a long streak with the cap off must not overflow."""
        self.assertEqual(manager.wake_backoff_seconds(600, 1), 600)
        self.assertEqual(manager.wake_backoff_seconds(600, 3), 2400)
        self.assertEqual(manager.wake_backoff_seconds(600, 28), manager.MAX_WAKE_BACKOFF_SECONDS)
        self.assertEqual(manager.wake_backoff_seconds(600, 10_000), manager.MAX_WAKE_BACKOFF_SECONDS)
        workdir = self.tmp / "plain"
        workdir.mkdir()
        path = self.write_goal(workdir=workdir, cap=0)
        goal = json.loads(path.read_text())
        goal["state"]["no_progress_streak"] = 40
        path.write_text(json.dumps(goal))
        g = self.tick(path)
        self.assertEqual(g["state"]["no_progress_streak"], 41)

    def test_cap_zero_disables(self):
        workdir = self.tmp / "plain"
        workdir.mkdir()
        path = self.write_goal(workdir=workdir, cap=0)
        for _ in range(6):
            g = self.tick(path)
            self.expire_backoff(path)
        self.assertEqual(g["state"]["status"], "active")
        self.assertEqual(g["state"]["no_progress_streak"], 6)

    def test_goal_without_new_keys_runs_the_classic_loop(self):
        """Review major 2: a goal file without the opt-in keys behaves as before
        them: a nudge on every idle tick, escalation after 3 nudges, worker
        left running, no cap state written."""
        workdir = self.tmp / "plain"
        workdir.mkdir()
        path = self.write_goal(workdir=workdir, cap=None, gate=None, escalate_after=3)
        goal = json.loads(path.read_text())
        del goal["schedule"]["no_progress_backoff_base_seconds"]
        path.write_text(json.dumps(goal))
        for _ in range(3):
            g = self.tick(path)
        self.assertEqual(len(self.sends), 3)
        self.assertEqual(g["state"]["status"], "escalated")
        self.assertEqual(self.stops, [])
        self.assertEqual(self.done_runs(), 3, "done_cmd runs every tick")
        for key in ("no_progress_streak", "next_wake_at", "last_failed_tree_hash", "progress_tree_hash"):
            self.assertNotIn(key, g["state"])

    def test_receipts_count_as_progress_with_worktree_progress(self):
        """Review major 3: receipts without a tree change (waiting on CI, a
        push, a tag) still count; the tree hash is only an extra signal."""
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, progress=True, idle_for=0, max_idle=3600)
        self.tick(path)
        for n in range(1, 5):
            self.add_receipt(repo, n)
            g = self.tick(path)
        self.assertEqual(g["state"]["status"], "active")
        self.assertEqual(g["state"]["no_progress_streak"], 0)
        self.assertNotIn("no_progress_turn", self.events(g))

    def test_tree_change_is_progress_with_worktree_progress(self):
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, progress=True, idle_for=0, max_idle=3600,
                               state={"no_progress_streak": 2, "nudges_sent": 2})
        g = self.tick(path)  # baseline tick: records the tree, judges nothing
        self.assertEqual(g["state"]["no_progress_streak"], 2)
        (repo / "main.txt").write_text("v2\n")
        g = self.tick(path)
        self.assertEqual(g["state"]["no_progress_streak"], 0)
        self.assertEqual(g["state"]["nudges_sent"], 0, "tree progress resets the nudge count")
        self.assertIn("progress", self.events(g))

    def test_tree_change_is_not_progress_without_worktree_progress(self):
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, idle_for=0, max_idle=3600, state={"no_progress_streak": 2})
        self.tick(path)
        (repo / "main.txt").write_text("v2\n")
        g = self.tick(path)
        self.assertEqual(g["state"]["no_progress_streak"], 2)
        self.assertNotIn("progress", self.events(g))

    def test_receipt_on_the_baseline_tick_is_progress(self):
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, progress=True, idle_for=0, max_idle=3600,
                               state={"no_progress_streak": 2})
        self.add_receipt(repo, 1)
        g = self.tick(path)
        self.assertIsNotNone(g["state"]["progress_tree_hash"])
        self.assertEqual(g["state"]["no_progress_streak"], 0)
        self.assertIn("progress", self.events(g))

    def test_work_before_the_first_tick_counts_against_the_creation_baseline(self):
        repo = init_repo(self.tmp / "repo")
        created = manager.worktree_tree_hash(repo)
        path = self.write_goal(workdir=repo, progress=True, idle_for=0, max_idle=3600,
                               state={"progress_tree_hash": created, "no_progress_streak": 1})
        (repo / "main.txt").write_text("worked before the first tick\n")
        g = self.tick(path)
        self.assertEqual(g["state"]["no_progress_streak"], 0)
        self.assertIn("progress", self.events(g))

    def test_idle_is_measured_from_the_last_progress(self):
        workdir = self.tmp / "plain"
        workdir.mkdir()
        path = self.write_goal(workdir=workdir, idle_for=7200, max_idle=3600,
                               state={"last_progress_at": iso_ago(60)})
        g = self.tick(path)
        self.assertEqual(self.sends, [], "progress a minute ago: the worker is not idle")
        self.assertNotIn("no_progress_turn", self.events(g))

    def test_cap_rechecks_skipped_verifier_before_failing(self):
        repo = init_repo(self.tmp / "repo")
        marker = self.tmp / "released"
        path = self.write_goal(workdir=repo, done_cmd=self.counting_done_cmd(f"test -f {marker}"))
        self.tick(path)
        self.expire_backoff(path)
        self.tick(path)
        marker.write_text("external state flipped")
        self.expire_backoff(path)
        g = self.tick(path)
        self.assertEqual(g["state"]["status"], "done")


class TestUnchangedWorktreeGate(GoalSandbox):
    def test_verifier_not_rerun_on_unchanged_tree(self):
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, cap=0, idle_for=0, max_idle=3600)
        self.tick(path)
        self.assertEqual(self.done_runs(), 1)
        g = self.tick(path)
        g = self.tick(path)
        self.assertEqual(self.done_runs(), 1)
        self.assertIn("verifier_skipped", self.events(g))
        (repo / "new_untracked.txt").write_text("x\n")
        self.tick(path)
        self.assertEqual(self.done_runs(), 2, "an untracked file changes the tree")

    def test_new_receipt_gets_a_fresh_verdict(self):
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, cap=0, idle_for=0, max_idle=3600)
        self.tick(path)
        self.tick(path)
        self.assertEqual(self.done_runs(), 1, "a quiet tick on the failed tree is skipped")
        self.add_receipt(repo, 1)
        self.tick(path)
        self.assertEqual(self.done_runs(), 2, "a receipt is the worker's claim and is verified")

    def test_receipt_append_does_not_change_the_hash(self):
        repo = init_repo(self.tmp / "repo")
        h0 = manager.worktree_tree_hash(repo)
        self.add_receipt(repo, 1)
        self.assertEqual(h0, manager.worktree_tree_hash(repo))

    def test_wake_skipped_after_two_delivered_wakes_on_unchanged_failed_tree(self):
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, cap=5)
        self.tick(path)
        self.expire_backoff(path)
        g = self.tick(path)
        self.assertEqual(len(self.sends), 2, "two wakes on a failed tree are allowed")
        self.assertNotIn("wake_skipped", self.events(g))
        self.expire_backoff(path)
        g = self.tick(path)
        self.assertEqual(len(self.sends), 2)
        self.assertIn("wake_skipped", self.events(g))
        self.assertEqual(g["state"]["no_progress_streak"], 3, "a skipped wake still counts as an empty turn")
        self.assertEqual(self.done_runs(), 2)

    def test_failed_nudge_does_not_arm_the_wake_skip(self):
        """Review major 1: an undelivered wake must not count toward the skip."""
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, cap=6)
        delivered = iter([False, True, True])
        attempts: list[str] = []

        def send(_title, message, _dry_run, _verbose):
            attempts.append(message)
            return next(delivered, True)

        with patch("manager.agent_deck_send", side_effect=send):
            for _ in range(3):
                g = self.tick(path)
                self.expire_backoff(path)
            self.assertEqual(len(attempts), 3, "wakes after a failed one must still go out")
            self.assertNotIn("wake_skipped", self.events(g))
            g = self.tick(path)
        self.assertEqual(len(attempts), 3, "skipped after two delivered wakes")
        self.assertIn("wake_skipped", self.events(g))

    def test_silent_worker_with_failing_sends_escalates_without_a_stop(self):
        """Review major 1 scenario: gate and cap on, every nudge fails."""
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, cap=3)
        with patch("manager.agent_deck_send", return_value=False) as send:
            for _ in range(3):
                g = self.tick(path)
                self.expire_backoff(path)
        self.assertEqual(send.call_count, 2, "every wake before the cap is attempted")
        self.assertNotIn("wake_skipped", self.events(g))
        self.assertEqual(g["state"]["status"], "escalated")
        self.assertEqual(self.stops, [])

    def test_gate_off_reruns_every_tick(self):
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, cap=0, gate=False, idle_for=0, max_idle=3600)
        for _ in range(3):
            self.tick(path)
        self.assertEqual(self.done_runs(), 3)

    def test_external_done_is_seen_before_a_wake(self):
        """Review B1: done_cmd checks state outside the worktree (gh release)."""
        repo = init_repo(self.tmp / "repo")
        marker = self.tmp / "released"
        path = self.write_goal(workdir=repo, done_cmd=self.counting_done_cmd(f"test -f {marker}"),
                               idle_for=0, max_idle=3600)
        self.tick(path)  # fails on tree X
        marker.write_text("release published, tree unchanged")
        goal = json.loads(path.read_text())
        goal["state"]["created_at"] = iso_ago(7200)  # the worker went silent
        path.write_text(json.dumps(goal))
        g = self.tick(path)
        self.assertEqual(g["state"]["status"], "done")
        self.assertEqual(self.sends, [], "no NOT YET MET nudge once the goal is met")

    def test_every_sent_wake_follows_a_fresh_verdict(self):
        """Every sent wake has a failed done_cmd verdict from its own tick.

        Fresh means taken in the same tick, before the send, on the same
        worktree hash. On the first tick that verdict comes from Step 2,
        so other events (no_progress_turn) can sit between it and the send.
        """
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, cap=0)
        runs_at_send: list[int] = []

        def send(_title, message, _dry_run, _verbose):
            runs_at_send.append(self.done_runs())
            self.sends.append(message)
            return True

        with patch("manager.agent_deck_send", side_effect=send):
            for tick in range(4):
                runs_before = self.done_runs()
                seen = len(json.loads(path.read_text())["history"])
                g = self.tick(path)
                self.expire_backoff(path)
                history = g["history"][seen:]
                events = [e["event"] for e in history]
                self.assertEqual(events.count("nudge_sent"), 1, (tick, events))
                self.assertEqual(runs_at_send[-1], runs_before + 1,
                                 f"tick {tick}: done_cmd must run once in this tick, before the wake")
                self.assertEqual(events.count("verifier_check"), 1, (tick, events))
                check = events.index("verifier_check")
                self.assertLess(check, events.index("nudge_sent"), (tick, events))
                self.assertNotEqual(history[check]["detail"], "rc=0", (tick, events))
        self.assertEqual(len(runs_at_send), 4)

    def test_cap_off_never_skips_wakes(self):
        """Review N1: with the cap off a silent worker must still escalate."""
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, cap=0, escalate_after=3)
        for _ in range(6):
            g = self.tick(path)
            if g["state"]["status"] != "active":
                break
            self.expire_backoff(path)
        self.assertEqual(g["state"]["status"], "escalated")
        self.assertEqual(len(self.sends), 3)
        self.assertNotIn("wake_skipped", self.events(g))

    def test_legacy_goal_file_keeps_gate_off(self):
        """Goal files written before the gate existed keep running done_cmd every tick."""
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, cap=0, gate=None, idle_for=0, max_idle=3600)
        for _ in range(3):
            self.tick(path)
        self.assertEqual(self.done_runs(), 3)

    def test_timeout_does_not_arm_skip(self):
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, cap=0, idle_for=0, max_idle=3600)
        with patch("manager.run_done_cmd", return_value=(124, "verifier timed out")) as run:
            self.tick(path)
            self.tick(path)
        self.assertEqual(run.call_count, 2)


class TestWorktreeTreeHash(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="goal-hash-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)

    def test_none_outside_git(self):
        plain = self.tmp / "plain"
        plain.mkdir()
        self.assertIsNone(manager.worktree_tree_hash(plain))
        self.assertIsNone(manager.worktree_tree_hash(self.tmp / "missing"))

    def test_none_for_home(self):
        home = init_repo(self.tmp / "home")
        self.assertIsNotNone(manager.worktree_tree_hash(home))
        with patch("manager.Path.home", return_value=home):
            self.assertIsNone(manager.worktree_tree_hash(home))

    def test_stable_and_sensitive(self):
        repo = init_repo(self.tmp / "repo")
        h0 = manager.worktree_tree_hash(repo)
        self.assertIsNotNone(h0)
        self.assertEqual(h0, manager.worktree_tree_hash(repo))
        (repo / "main.txt").write_text("v2\n")
        h1 = manager.worktree_tree_hash(repo)
        self.assertNotEqual(h0, h1)
        (repo / "u.txt").write_text("a\n")
        h2 = manager.worktree_tree_hash(repo)
        self.assertNotEqual(h1, h2)
        (repo / "u.txt").write_text("b\n")
        self.assertNotEqual(h2, manager.worktree_tree_hash(repo), "untracked content counts")

    def test_task_log_and_ignored_files_excluded(self):
        repo = init_repo(self.tmp / "repo")
        (repo / ".gitignore").write_text("build/\n")
        h0 = manager.worktree_tree_hash(repo)
        (repo / "task-log.md").write_text("## 2026-01-01T00:00:00Z\n")
        (repo / "build").mkdir()
        (repo / "build" / "out.bin").write_text("x")
        self.assertEqual(h0, manager.worktree_tree_hash(repo))

    def test_subdirectory_workdir(self):
        repo = init_repo(self.tmp / "repo")
        sub = repo / "pkg"
        sub.mkdir()
        (sub / "a.txt").write_text("1\n")
        h0 = manager.worktree_tree_hash(sub)
        self.assertIsNotNone(h0)
        (sub / "a.txt").write_text("2\n")
        self.assertNotEqual(h0, manager.worktree_tree_hash(sub))

    def test_unborn_branch(self):
        repo = self.tmp / "fresh"
        repo.mkdir()
        git(repo, "init", "-q")
        (repo / "a.txt").write_text("1\n")
        self.assertIsNotNone(manager.worktree_tree_hash(repo))

    def test_does_not_refresh_a_stat_dirty_index(self):
        """`git status` refreshes a stat-dirty index unless optional locks are off."""
        repo = init_repo(self.tmp / "repo")
        main = repo / "main.txt"
        future = main.stat().st_mtime + 120
        os.utime(main, (future, future))  # same content, new stat info
        index = repo / ".git" / "index"
        before = (index.read_bytes(), index.stat().st_mtime_ns)
        manager.worktree_tree_hash(repo)
        self.assertEqual(before, (index.read_bytes(), index.stat().st_mtime_ns))

    def test_submodule_commit_changes_the_hash(self):
        sub = init_repo(self.tmp / "sub")
        repo = init_repo(self.tmp / "repo")
        subprocess.run(["git", "-C", str(repo), "-c", "protocol.file.allow=always", "submodule", "add", "-q",
                        str(sub), "dep"], check=True, capture_output=True)
        git(repo, "commit", "-q", "-m", "add dep")
        h0 = manager.worktree_tree_hash(repo)
        self.assertIsNotNone(h0)
        dep = repo / "dep"
        (dep / "main.txt").write_text("v2\n")
        git(dep, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-am", "move")
        h1 = manager.worktree_tree_hash(repo)
        self.assertNotEqual(h0, h1)
        (dep / "main.txt").write_text("uncommitted 1\n")
        h2 = manager.worktree_tree_hash(repo)
        self.assertNotEqual(h1, h2)
        (dep / "main.txt").write_text("uncommitted 2\n")
        self.assertNotEqual(h2, manager.worktree_tree_hash(repo), "edits inside a dirty submodule count")

    def test_large_untracked_file_hashed_by_size_and_mtime(self):
        repo = init_repo(self.tmp / "repo")
        big = repo / "big.bin"
        big.write_bytes(b"a" * (manager.MAX_HASHED_UNTRACKED_BYTES + 1))
        os.utime(big, (1_000_000, 1_000_000))
        h0 = manager.worktree_tree_hash(repo)
        with big.open("r+b") as f:
            f.write(b"b")  # same size
        os.utime(big, (1_000_000, 1_000_000))
        self.assertEqual(h0, manager.worktree_tree_hash(repo), "content above the cap is not read")
        os.utime(big, (2_000_000, 2_000_000))
        self.assertNotEqual(h0, manager.worktree_tree_hash(repo), "mtime above the cap counts")

    def test_read_only(self):
        repo = init_repo(self.tmp / "repo")
        (repo / "main.txt").write_text("dirty\n")
        (repo / "u.txt").write_text("u\n")
        index = (repo / ".git" / "index").read_bytes()
        objects = sorted(p.name for p in (repo / ".git" / "objects").rglob("*"))
        manager.worktree_tree_hash(repo)
        self.assertEqual(index, (repo / ".git" / "index").read_bytes())
        self.assertEqual(objects, sorted(p.name for p in (repo / ".git" / "objects").rglob("*")))


class TestNaiveTimestamps(unittest.TestCase):
    def test_naive_timestamp_is_local_time(self):
        local = datetime(2026, 1, 2, 3, 4, 5).astimezone()
        self.assertEqual(manager.parse_iso("2026-01-02T03:04:05"), local)
        self.assertEqual(manager.parse_iso("2026-01-02T03:04:05Z"),
                         datetime(2026, 1, 2, 3, 4, 5, tzinfo=timezone.utc))


STUB = """#!/bin/sh
case "$1 $2 $3" in
  "-p personal list") echo '[{"id":"sid-1","title":"g1"}]' ;;
esac
exit 0
"""


class TestGoalShDefaults(unittest.TestCase):
    def run_goal(self, workdir: Path, env: dict | None = None) -> dict:
        tmp = Path(tempfile.mkdtemp(prefix="goal-sh-"))
        self.addCleanup(shutil.rmtree, tmp, True)
        bindir = tmp / "bin"
        bindir.mkdir()
        stub = bindir / "agent-deck"
        stub.write_text(STUB)
        stub.chmod(stub.stat().st_mode | stat.S_IEXEC)
        full_env = {k: v for k, v in os.environ.items()
                    if not k.startswith(("NO_PROGRESS_", "WORKTREE_"))}
        full_env.update({"PATH": f"{bindir}:{os.environ['PATH']}", "HOME": str(tmp),
                         "GOALS_DIR": str(tmp / "goals"), "TMPDIR": str(tmp)})
        full_env.update(env or {})
        r = subprocess.run(["bash", str(HERE.parent / "goal.sh"), "g1", "goal", "true", "g1", str(workdir)],
                           capture_output=True, text=True, env=full_env, timeout=60)
        self.assertEqual(r.returncode, 0, r.stderr)
        return json.loads((tmp / "goals" / "g1.json").read_text())

    def test_defaults_keep_the_classic_loop(self):
        tmp = Path(tempfile.mkdtemp(prefix="goal-wd-"))
        self.addCleanup(shutil.rmtree, tmp, True)
        goal = self.run_goal(init_repo(tmp / "repo"))
        self.assertEqual(manager.progress_features(goal["schedule"]), (0, False, False))
        self.assertEqual(goal["schedule"]["no_progress_action"], "escalate")
        self.assertIsNone(goal["state"]["progress_tree_hash"])

    def test_opt_in_records_the_creation_baseline(self):
        tmp = Path(tempfile.mkdtemp(prefix="goal-wd-"))
        self.addCleanup(shutil.rmtree, tmp, True)
        repo = init_repo(tmp / "repo")
        goal = self.run_goal(repo, {"NO_PROGRESS_CAP": "3", "WORKTREE_PROGRESS": "1"})
        self.assertEqual(manager.progress_features(goal["schedule"]), (3, True, False))
        self.assertEqual(goal["state"]["progress_tree_hash"], manager.worktree_tree_hash(repo))


if __name__ == "__main__":
    unittest.main(verbosity=2)
