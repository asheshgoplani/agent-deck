#!/usr/bin/env python3
"""Regression tests for the #2080 review of the #1981 heartbeat guard.

PR #2080 bounded the heartbeat's consecutive interactive-state skips, but its
interactive-state DETECTION still inferred an open AskUserQuestion picker
purely from a raw tmux pane-text capture. A real busy/interactive signal
already exists: the same fresh hook-driven "running"/"starting" status the
Go send path treats as authoritative after the queued-delivery fix (PR
#2273, ``hookDrivenBusy`` / ``send.StatusIsBusy``) — a picker's PreToolUse
event writes "running" and nothing advances it to Stop/PostToolUse until the
human answers, so it reads busy even while the derived "status" field still
shows "waiting".

``_pane_blocks_automated_send`` now takes that hook verdict and gates on it
whenever it is known; pane-text picker detection runs ONLY as a fallback when
the hook signal is unknown, and that fallback verdict is reported with an
"unknown:" prefix rather than as confirmed evidence.

These are pure function tests on synthetic hook/pane inputs — no agent-deck
runtime and no third-party packages required.
"""

from __future__ import annotations

import sys
import types
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))
# Also make sibling test modules in this directory importable directly (for a
# standalone `python3 -m unittest` run, without relying on pytest's own
# per-file sys.path insertion).
sys.path.insert(0, str(Path(__file__).parent))

try:
    import toml  # noqa: F401
except ModuleNotFoundError:
    sys.modules["toml"] = types.SimpleNamespace(load=lambda *_a, **_k: {})

try:
    import bridge  # noqa: E402
except ModuleNotFoundError:
    import importlib.util

    _canon = Path(__file__).resolve().parents[2] / "internal" / "session" / "conductor_bridge.py"
    _spec = importlib.util.spec_from_file_location("bridge", _canon)
    bridge = importlib.util.module_from_spec(_spec)
    sys.modules["bridge"] = bridge
    _spec.loader.exec_module(bridge)

# A pane that is completely neutral: no picker markers, no composer footer, no
# draft — nothing a pane-text heuristic could ever flag.
NEUTRAL_PANE = "  Assistant: finished the last task.\nSome ordinary transcript line.\n"


class TestHookGatesOverPaneText(unittest.TestCase):
    def test_hook_interactive_blocks_even_on_neutral_pane_text(self):
        # THE headline case: pane text gives zero evidence of interactivity,
        # but the hook-driven signal says the target is mid-turn (open
        # picker or otherwise busy). The guard must still block.
        reason = bridge._pane_blocks_automated_send(
            NEUTRAL_PANE, hook_known=True, hook_interactive=True
        )
        self.assertEqual(reason, "hook-busy-interactive")

    def test_hook_known_not_interactive_ignores_stale_pane_picker_shape(self):
        # The hook is authoritative when known: even a pane capture that LOOKS
        # like an open picker must not override a hook that confidently says
        # "not interactive" (e.g. a picker glyph pattern left over in a
        # scrollback frame that has already advanced).
        from test_issue1981_heartbeat_send_guard import OPEN_PICKER  # noqa: PLC0415

        reason = bridge._pane_blocks_automated_send(
            OPEN_PICKER, hook_known=True, hook_interactive=False
        )
        self.assertIsNone(reason)

    def test_hook_known_not_interactive_still_catches_unsent_draft(self):
        # The composer-draft check is orthogonal to turn state and always
        # runs off pane text, regardless of what the hook reports.
        from test_issue1981_heartbeat_send_guard import REAL_DRAFT  # noqa: PLC0415

        reason = bridge._pane_blocks_automated_send(
            REAL_DRAFT, hook_known=True, hook_interactive=False
        )
        self.assertEqual(reason, "composer-holds-unsent-input")

    def test_hook_unknown_falls_back_to_pane_text_as_unknown_confidence(self):
        # No trustworthy hook signal (hooks never fired / stale / CLI read
        # failed): fall back to the pane-text heuristic, but the verdict must
        # be distinguishable from confirmed hook evidence.
        from test_issue1981_heartbeat_send_guard import OPEN_PICKER  # noqa: PLC0415

        reason = bridge._pane_blocks_automated_send(
            OPEN_PICKER, hook_known=False, hook_interactive=False
        )
        self.assertEqual(reason, "unknown:askuserquestion-picker-open")

    def test_hook_unknown_and_pane_neutral_sends(self):
        reason = bridge._pane_blocks_automated_send(
            NEUTRAL_PANE, hook_known=False, hook_interactive=False
        )
        self.assertIsNone(reason)

    def test_default_call_matches_pre_2080_pane_only_behavior(self):
        # Backward compatible default: a caller that never learned about the
        # hook signal gets exactly the old pane-only verdicts.
        from test_issue1981_heartbeat_send_guard import OPEN_PICKER, REAL_DRAFT  # noqa: PLC0415

        self.assertEqual(
            bridge._pane_blocks_automated_send(OPEN_PICKER),
            "unknown:askuserquestion-picker-open",
        )
        self.assertEqual(
            bridge._pane_blocks_automated_send(REAL_DRAFT),
            "composer-holds-unsent-input",
        )
        self.assertIsNone(bridge._pane_blocks_automated_send(NEUTRAL_PANE))


class TestHookDrivenInteractive(unittest.TestCase):
    """hook_driven_interactive() reads session show --json's hook_status /
    hook_status_fresh fields (#2080) and must fail to known=False, never
    silently to "not interactive", on any CLI or parse failure."""

    def _stub_run_cli(self, monkeypatch, returncode=0, stdout="", exc=None):
        def _fake_run_cli(*args, **kwargs):
            if exc is not None:
                raise exc
            return types.SimpleNamespace(returncode=returncode, stdout=stdout, stderr="")

        monkeypatch.setattr(bridge, "run_cli", _fake_run_cli)

    def test_fresh_running_is_interactive_and_known(self):
        import unittest.mock as mock  # noqa: PLC0415

        with mock.patch.object(
            bridge,
            "run_cli",
            return_value=types.SimpleNamespace(
                returncode=0,
                stdout='{"hook_status": "running", "hook_status_fresh": true}',
                stderr="",
            ),
        ):
            interactive, known = bridge.hook_driven_interactive("conductor-x")
        self.assertTrue(interactive)
        self.assertTrue(known)

    def test_fresh_waiting_is_not_interactive_but_known(self):
        import unittest.mock as mock  # noqa: PLC0415

        with mock.patch.object(
            bridge,
            "run_cli",
            return_value=types.SimpleNamespace(
                returncode=0,
                stdout='{"hook_status": "waiting", "hook_status_fresh": true}',
                stderr="",
            ),
        ):
            interactive, known = bridge.hook_driven_interactive("conductor-x")
        self.assertFalse(interactive)
        self.assertTrue(known)

    def test_stale_hook_status_is_unknown(self):
        import unittest.mock as mock  # noqa: PLC0415

        with mock.patch.object(
            bridge,
            "run_cli",
            return_value=types.SimpleNamespace(
                returncode=0,
                stdout='{"hook_status": "running", "hook_status_fresh": false}',
                stderr="",
            ),
        ):
            interactive, known = bridge.hook_driven_interactive("conductor-x")
        self.assertFalse(interactive)
        self.assertFalse(known)

    def test_cli_failure_is_unknown_not_not_interactive(self):
        import unittest.mock as mock  # noqa: PLC0415

        with mock.patch.object(
            bridge,
            "run_cli",
            return_value=types.SimpleNamespace(returncode=1, stdout="", stderr="boom"),
        ):
            interactive, known = bridge.hook_driven_interactive("conductor-x")
        self.assertFalse(interactive)
        self.assertFalse(known)

    def test_unparseable_json_is_unknown(self):
        import unittest.mock as mock  # noqa: PLC0415

        with mock.patch.object(
            bridge,
            "run_cli",
            return_value=types.SimpleNamespace(returncode=0, stdout="not json", stderr=""),
        ):
            interactive, known = bridge.hook_driven_interactive("conductor-x")
        self.assertFalse(interactive)
        self.assertFalse(known)


if __name__ == "__main__":
    unittest.main(verbosity=2)
