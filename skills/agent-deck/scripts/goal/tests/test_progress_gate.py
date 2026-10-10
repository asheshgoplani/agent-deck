#!/usr/bin/env python3
"""Tests for the goal no-progress cap and the unchanged-worktree gate skip.

No-progress cap: a turn closes on a new receipt or a due idle wake; a turn
without progress is empty; 3 consecutive empty turns end the goal; each
wake waits a doubling backoff window.

Gate skip: done_cmd is not rerun, and the worker is not woken a second
time, while the worktree hash equals the one the verifier last failed on.

Run with pytest or unittest; git must be on PATH.
"""
from __future__ import annotations

import json
import shutil
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
                   max_cycles: int = 99, gate: bool | None = True, idle_for: int = 600) -> Path:
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
    def test_three_empty_wake_turns_end_goal(self):
        workdir = self.tmp / "plain"  # not a git repo: receipts are the progress signal
        workdir.mkdir()
        path = self.write_goal(workdir=workdir)

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

    def test_legacy_goal_without_new_keys_gets_default_cap(self):
        workdir = self.tmp / "plain"
        workdir.mkdir()
        path = self.write_goal(workdir=workdir, cap=None)
        for _ in range(3):
            g = self.tick(path)
            self.expire_backoff(path)
        self.assertEqual(g["state"]["status"], "failed")

    def test_metronome_receipts_without_tree_change_end_goal(self):
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, idle_for=0, max_idle=3600)
        self.tick(path)  # baseline tick records the starting tree
        for n in range(1, 4):
            self.add_receipt(repo, n)
            g = self.tick(path)
        self.assertEqual(g["state"]["status"], "failed")
        self.assertEqual(g["state"]["ended_reason"], "no_progress_cap")
        self.assertEqual(self.sends, [], "a receipt-writing worker is never woken")

    def test_tree_change_is_progress(self):
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, idle_for=0, max_idle=3600)
        self.tick(path)
        self.add_receipt(repo, 1)
        g = self.tick(path)
        self.assertEqual(g["state"]["no_progress_streak"], 1)
        (repo / "main.txt").write_text("v2\n")
        self.add_receipt(repo, 2)
        g = self.tick(path)
        self.assertEqual(g["state"]["no_progress_streak"], 0)
        self.assertIn("progress", self.events(g))

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

    def test_no_second_wake_on_unchanged_failed_tree(self):
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, cap=5)
        self.tick(path)
        self.assertEqual(len(self.sends), 1, "the first wake on a failed tree is allowed")
        self.expire_backoff(path)
        g = self.tick(path)
        self.assertEqual(len(self.sends), 1)
        self.assertIn("wake_skipped", self.events(g))
        self.assertEqual(g["state"]["no_progress_streak"], 2, "a skipped wake still counts as an empty turn")
        self.assertEqual(self.done_runs(), 1)

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
        repo = init_repo(self.tmp / "repo")
        path = self.write_goal(workdir=repo, cap=0)
        for _ in range(4):
            self.tick(path)
            self.expire_backoff(path)
        g = json.loads(path.read_text())
        events = self.events(g)
        for i, e in enumerate(events):
            if e == "nudge_sent":
                self.assertEqual(events[i - 1], "verifier_check", events)

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

    def test_read_only(self):
        repo = init_repo(self.tmp / "repo")
        (repo / "main.txt").write_text("dirty\n")
        (repo / "u.txt").write_text("u\n")
        index = (repo / ".git" / "index").read_bytes()
        objects = sorted(p.name for p in (repo / ".git" / "objects").rglob("*"))
        manager.worktree_tree_hash(repo)
        self.assertEqual(index, (repo / ".git" / "index").read_bytes())
        self.assertEqual(objects, sorted(p.name for p in (repo / ".git" / "objects").rglob("*")))


if __name__ == "__main__":
    unittest.main(verbosity=2)
