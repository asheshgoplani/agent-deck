#!/usr/bin/env python3
"""Run budgets for goals (C9, prime-agent adoption).

The manager stops a goal whose worker spent its token cap or ran past its
time cap before it wakes the worker again, and a nudge that `session send`
refuses for a spent [costs.budgets] limit ends the goal without retries.
goal.sh takes --max-tokens/--max-turns/--max-minutes and falls back to
[costs.run_budget] defaults read with `agent-deck config get`.
"""
from __future__ import annotations

import json
import os
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
sys.path.insert(0, str(HERE))

import manager  # noqa: E402
from test_manager import iso_minutes_ago, make_goal  # noqa: E402


def costs_rows(session_id: str, tokens: int) -> str:
    return json.dumps([
        {"session_id": "someone-else", "input_tokens": 10**9, "output_tokens": 0, "cache_read": 0, "cache_write": 0},
        {"session_id": session_id, "input_tokens": tokens // 2, "output_tokens": tokens - tokens // 2,
         "cache_read": 0, "cache_write": 0},
    ])


class BudgetCase(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        for name in ("GOALS_DIR", "ESCALATIONS_DIR", "HISTORY_DIR"):
            p = patch.object(manager, name, self.dir / name.lower())
            p.start()
            self.addCleanup(p.stop)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def walk(self, goal: dict) -> dict:
        path = self.dir / f"{goal['id']}.json"
        path.write_text(json.dumps(goal))
        manager.walk_goal(path, dry_run=False, verbose=False)
        return json.loads(path.read_text())


class TestWorkerTokens(BudgetCase):
    def run_with(self, stdout: str, rc: int = 0):
        done = subprocess.CompletedProcess([], rc, stdout=stdout, stderr="")
        with patch("manager.subprocess.run", return_value=done) as run:
            return manager.worker_tokens({"worker_session_id": "w1", "profile": "p"}), run

    def test_sums_the_worker_row(self):
        used, run = self.run_with(costs_rows("w1", 1234))
        self.assertEqual(used, 1234)
        rows = json.dumps([{"session_id": "w1", "input_tokens": 10, "output_tokens": 20, "cache_read": 10**6, "cache_write": 5}])
        self.assertEqual(self.run_with(rows)[0], 35, "cache reads are not counted")
        self.assertEqual(run.call_args[0][0][:3], ["agent-deck", "-p", "p"])

    def test_no_events_yet_is_zero(self):
        self.assertEqual(self.run_with("[]")[0], 0)

    def test_unavailable_is_none(self):
        self.assertIsNone(self.run_with("cost tracking is off", rc=1)[0])
        self.assertIsNone(self.run_with("not json")[0])


@patch("manager.agent_deck_stop", return_value=True)
@patch("manager.agent_deck_send", return_value=True)
class TestWalkGoalBudgets(BudgetCase):
    def idle_goal(self, **schedule) -> dict:
        goal = make_goal(pid="b", max_idle=60, created_minutes_ago=10)
        goal["schedule"].update(schedule)
        return goal

    def test_no_budget_configured_is_unchanged(self, send, stop):
        with patch("manager.worker_tokens") as tokens:
            data = self.walk(self.idle_goal())
        tokens.assert_not_called()
        send.assert_called_once()
        stop.assert_not_called()
        self.assertEqual(data["state"]["status"], "active")

    def test_under_token_budget_wakes(self, send, stop):
        with patch("manager.worker_tokens", return_value=999):
            data = self.walk(self.idle_goal(max_tokens=1000))
        send.assert_called_once()
        stop.assert_not_called()
        self.assertEqual(data["state"]["status"], "active")
        self.assertEqual(data["state"]["tokens_used"], 999)

    def test_over_token_budget_stops_before_waking(self, send, stop):
        with patch("manager.worker_tokens", return_value=1000):
            data = self.walk(self.idle_goal(max_tokens=1000))
        send.assert_not_called()
        stop.assert_called_once()
        self.assertEqual(data["state"]["status"], "failed")
        self.assertEqual(data["state"]["ended_reason"], "budget_exceeded: max_tokens: used 1000 of 1000 tokens")
        self.assertIn("budget_exceeded", [e["event"] for e in data["history"]])

    def test_unmeasured_is_recorded_once_per_change(self, send, stop):
        """Review nit: budget_unmeasured once per state change, not every tick."""
        goal = self.idle_goal(max_tokens=1000, escalate_after_stuck_nudges=99)
        path = self.dir / f"{goal['id']}.json"
        path.write_text(json.dumps(goal))
        for used in (None, None, None, 5, None):
            with patch("manager.worker_tokens", return_value=used):
                manager.walk_goal(path, dry_run=False, verbose=False)
        events = [e["event"] for e in json.loads(path.read_text())["history"]]
        self.assertEqual(events.count("budget_unmeasured"), 2)

    def test_max_cycles_zero_is_no_cap(self, send, stop):
        """Review m4: --max-turns 0 / MAX_CYCLES=0 is no cap."""
        goal = self.idle_goal()
        goal["schedule"]["max_cycles"] = 0
        data = self.walk(goal)
        self.assertEqual(data["state"]["status"], "active")
        stop.assert_not_called()

    def test_unmeasurable_tokens_do_not_stop(self, send, stop):
        with patch("manager.worker_tokens", return_value=None):
            data = self.walk(self.idle_goal(max_tokens=1000))
        send.assert_called_once()
        self.assertEqual(data["state"]["status"], "active")
        self.assertIn("budget_unmeasured", [e["event"] for e in data["history"]])

    def test_time_budget(self, send, stop):
        data = self.walk(self.idle_goal(max_minutes=60))
        self.assertEqual(data["state"]["status"], "active")
        goal = self.idle_goal(max_minutes=5)
        goal["state"]["created_at"] = iso_minutes_ago(6)
        data = self.walk(goal)
        self.assertEqual(data["state"]["status"], "failed")
        self.assertTrue(data["state"]["ended_reason"].startswith("budget_exceeded: max_minutes: ran 6 of 5 minutes"))
        send.assert_called_once()  # only the first, in-budget goal was woken

    def test_verifier_pass_wins_over_spent_budget(self, send, stop):
        goal = self.idle_goal(max_tokens=1)
        goal["done_cmd"] = "true"
        with patch("manager.worker_tokens", return_value=5):
            data = self.walk(goal)
        self.assertEqual(data["state"]["status"], "done")


class TestWalkGoalBudgetsWithProgressFeatures(TestWalkGoalBudgets):
    """The same budgets on a goal that opted into the no-progress cap, which
    runs the progress loop instead of the classic one."""

    def idle_goal(self, **schedule) -> dict:
        return super().idle_goal(no_progress_cap=3, **schedule)


class TestBudgetRefusedNudge(BudgetCase):
    def test_refused_send_is_not_retried(self):
        payload = json.dumps({"success": False, "code": "BUDGET_EXCEEDED", "resets_at": "2026-10-19T00:00:00+02:00",
                              "error": "budget exceeded: daily global limit exceeded ($5.00 used of $1.00); nothing was sent to 'w'"})
        refused = subprocess.CompletedProcess([], 1, stdout=payload, stderr="")
        with patch("manager.subprocess.run", return_value=refused) as run, patch("manager.time.sleep") as sleep:
            with self.assertRaises(manager.BudgetRefused) as caught:
                manager.agent_deck_send("w", "nudge", dry_run=False, verbose=False)
        self.assertEqual(run.call_count, 1)
        self.assertIn("--json", run.call_args[0][0])
        self.assertEqual(caught.exception.resets_at, "2026-10-19T00:00:00+02:00")
        sleep.assert_not_called()

    def test_refusal_is_detected_by_code_not_text(self):
        """Review m8: only code BUDGET_EXCEEDED is a budget refusal."""
        other = subprocess.CompletedProcess([], 1, stdout="", stderr="Error: worker said budget exceeded in its reply")
        with patch("manager.subprocess.run", return_value=other) as run, patch("manager.time.sleep"):
            self.assertFalse(manager.agent_deck_send("w", "nudge", dry_run=False, verbose=False))
        self.assertEqual(run.call_count, len(manager.BACKOFF_SECONDS), "an ordinary failure is retried")

    @patch("manager.agent_deck_stop", return_value=True)
    def test_refused_nudge_ends_goal(self, stop):
        """A limit that does not reset (session lifetime) ends the goal."""
        goal = make_goal(pid="r", max_idle=60, created_minutes_ago=10)
        with patch("manager.agent_deck_send", side_effect=manager.BudgetRefused("budget exceeded: session lifetime")):
            data = self.walk(goal)
        stop.assert_called_once()
        self.assertEqual(data["state"]["status"], "failed")
        self.assertEqual(data["state"]["ended_reason"], "budget_exceeded: budget exceeded: session lifetime")

    @patch("manager.agent_deck_stop", return_value=True)
    def test_daily_refusal_pauses_until_the_window_resets(self, stop):
        """Review m5: a daily limit pauses the goal, worker untouched."""
        resets = (datetime.now(timezone.utc) + timedelta(hours=3)).isoformat()
        goal = make_goal(pid="d", max_idle=60, created_minutes_ago=10)
        path = self.dir / "d.json"
        with patch("manager.agent_deck_send", side_effect=manager.BudgetRefused("budget exceeded: daily", resets)):
            data = self.walk(goal)
        stop.assert_not_called()
        self.assertEqual(data["state"]["status"], "active")
        self.assertEqual(manager.parse_iso(data["state"]["budget_paused_until"]), manager.parse_iso(resets))
        self.assertIn("budget_paused", [e["event"] for e in data["history"]])

        # During the pause nothing is sent; only done_cmd runs.
        with patch("manager.agent_deck_send") as send:
            manager.walk_goal(path, dry_run=False, verbose=False)
            send.assert_not_called()
        self.assertEqual(json.loads(path.read_text())["state"]["status"], "active")

        # After the reset the goal runs again and wakes the worker.
        data = json.loads(path.read_text())
        data["state"]["budget_paused_until"] = (datetime.now(timezone.utc) - timedelta(seconds=1)).isoformat()
        path.write_text(json.dumps(data))
        with patch("manager.agent_deck_send", return_value=True) as send:
            manager.walk_goal(path, dry_run=False, verbose=False)
            send.assert_called_once()
        data = json.loads(path.read_text())
        self.assertNotIn("budget_paused_until", data["state"])
        self.assertIn("budget_resumed", [e["event"] for e in data["history"]])

    @patch("manager.agent_deck_stop", return_value=True)
    def test_done_during_a_budget_pause_finishes(self, stop):
        goal = make_goal(pid="p", max_idle=60, created_minutes_ago=10, done_cmd="true")
        goal["state"]["budget_paused_until"] = (datetime.now(timezone.utc) + timedelta(hours=1)).isoformat()
        data = self.walk(goal)
        self.assertEqual(data["state"]["status"], "done")

    @patch("manager.agent_deck_stop", return_value=True)
    def test_refused_nudge_ends_goal_with_progress_features(self, stop):
        goal = make_goal(pid="r", max_idle=60, created_minutes_ago=10)
        goal["schedule"]["no_progress_cap"] = 3
        with patch("manager.agent_deck_send", side_effect=manager.BudgetRefused("budget exceeded: daily")):
            data = self.walk(goal)
        stop.assert_called_once()
        self.assertEqual(data["state"]["status"], "failed")
        self.assertEqual(data["state"]["ended_reason"], "budget_exceeded: budget exceeded: daily")


STUB = r"""#!/usr/bin/env bash
echo "$*" >> "$STUB_LOG"
args=("$@")
[[ "${args[0]}" == "-p" ]] && args=("${args[@]:2}")
case "${args[0]} ${args[1]:-}" in
  "config get")
    key="${args[2]##*.}"; var="STUB_CFG_${key}"; [[ -n "${!var:-}" ]] && echo "${!var}" || echo 0 ;;
  "add "*) echo '{"id":"sid-1"}' ;;
  "list --json") echo '[{"id":"sid-1","title":"'"$STUB_TITLE"'"}]' ;;
esac
exit 0
"""


class TestGoalShRunBudget(unittest.TestCase):
    def run_goal(self, *flags: str, env: dict | None = None) -> dict:
        tmp = Path(tempfile.mkdtemp())
        bindir = tmp / "bin"
        bindir.mkdir()
        stub = bindir / "agent-deck"
        stub.write_text(STUB)
        stub.chmod(stub.stat().st_mode | stat.S_IEXEC)
        full_env = {k: v for k, v in os.environ.items() if not k.startswith(("MAX_", "STUB_"))}
        full_env.update({
            "PATH": f"{bindir}:{os.environ['PATH']}", "HOME": str(tmp), "GOALS_DIR": str(tmp / "goals"),
            "STUB_LOG": str(tmp / "calls.log"), "STUB_TITLE": "g1", "TMPDIR": str(tmp),
        })
        full_env.update(env or {})
        r = subprocess.run(["bash", str(HERE.parent / "goal.sh"), *flags, "g1", "goal", "true"],
                           capture_output=True, text=True, env=full_env, timeout=60)
        self.assertEqual(r.returncode, 0, r.stderr)
        return json.loads((tmp / "goals" / "g1.json").read_text())

    def test_defaults_unchanged_without_budget(self):
        sched = self.run_goal()["schedule"]
        self.assertEqual((sched["max_tokens"], sched["max_cycles"], sched["max_minutes"]), (0, 24, 0))

    def test_config_defaults_apply(self):
        sched = self.run_goal(env={"STUB_CFG_max_tokens": "80000", "STUB_CFG_max_turns": "12", "STUB_CFG_max_minutes": "30"})["schedule"]
        self.assertEqual((sched["max_tokens"], sched["max_cycles"], sched["max_minutes"]), (80000, 12, 30))

    def test_env_beats_config(self):
        """Review nit: the environment wins over [costs.run_budget]."""
        sched = self.run_goal(env={"MAX_TOKENS": "700", "STUB_CFG_max_tokens": "80000",
                                   "MAX_CYCLES": "0", "STUB_CFG_max_turns": "12"})["schedule"]
        self.assertEqual((sched["max_tokens"], sched["max_cycles"]), (700, 0))

    def test_flags_override_config(self):
        goal = self.run_goal("--max-tokens", "500", "--max-turns", "3", "--max-minutes", "7",
                             env={"STUB_CFG_max_tokens": "80000"})
        sched = goal["schedule"]
        self.assertEqual((sched["max_tokens"], sched["max_cycles"], sched["max_minutes"]), (500, 3, 7))
        self.assertEqual(goal["profile"], "personal")

    def test_bad_value_is_refused(self):
        tmp = Path(tempfile.mkdtemp())
        r = subprocess.run(["bash", str(HERE.parent / "goal.sh"), "--max-tokens", "lots", "g1", "goal", "true"],
                           capture_output=True, text=True, env={**os.environ, "HOME": str(tmp), "GOALS_DIR": str(tmp / "g"),
                                                                "PATH": "/usr/bin:/bin"}, timeout=60)
        self.assertEqual(r.returncode, 2)
        self.assertIn("non-negative integer", r.stderr)


if __name__ == "__main__":
    unittest.main()
