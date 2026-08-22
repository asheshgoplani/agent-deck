#!/usr/bin/env python3
"""Regression tests for issue #1981.

A routine/heartbeat ``session send`` types text and presses Enter. When the
target Claude Code pane is mid-interaction that Enter is destructive:

  (a) an open AskUserQuestion picker resolves to its highlighted default — the
      model receives an answer the user never gave; and
  (b) a composer holding the user's half-typed input gets overwritten.

Both states still report status ``waiting``, so status alone cannot gate the
send. ``heartbeat_loop`` now captures the pane and skips the cycle when
``_pane_blocks_automated_send`` reports either state; any capture/parse failure
fails OPEN (the send proceeds) so heartbeats are never permanently blocked.

These are pure function tests on synthetic pane strings — no agent-deck runtime
and no third-party packages required.
"""

from __future__ import annotations

import sys
import types
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

# The bridge's only unconditional third-party import; stub it when absent so the
# test needs no packages (mirrors test_async_reply_on_idle_timeout.py).
try:
    import toml  # noqa: F401
except ModuleNotFoundError:
    sys.modules["toml"] = types.SimpleNamespace(load=lambda *_a, **_k: {})

# conftest.py registers the canonical bridge as module "bridge" under pytest;
# when this file is run directly there is no conftest, so load it by path.
try:
    import bridge  # noqa: E402
except ModuleNotFoundError:
    import importlib.util

    _canon = Path(__file__).resolve().parents[2] / "internal" / "session" / "conductor_bridge.py"
    _spec = importlib.util.spec_from_file_location("bridge", _canon)
    bridge = importlib.util.module_from_spec(_spec)
    sys.modules["bridge"] = bridge
    _spec.loader.exec_module(bridge)

ESC = "\x1b"
RULE = "─" * 48  # a composer box border


def _pane(*lines: str) -> str:
    return "\n".join(lines) + "\n"


# A real, bright (non-dim) user draft sitting unsent in the composer.
REAL_DRAFT = _pane(
    "  Assistant: finished the last task.",
    RULE,
    " ❯ can you also update the readme before we ship",
    RULE,
    "  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt",
)

# Only a dim (SGR 2) Claude-suggested ghost prompt — clobberable, not a draft.
GHOST_ONLY = _pane(
    "  Assistant: finished the last task.",
    RULE,
    f" ❯ {ESC}[2mTry running the test suite next{ESC}[22m",
    RULE,
    "  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt",
)

# An empty composer (bare prompt).
EMPTY_COMPOSER = _pane(
    "  Assistant: finished the last task.",
    RULE,
    " ❯ ",
    RULE,
    "  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt",
)

# An open AskUserQuestion option-picker.
OPEN_PICKER = _pane(
    " ☐ Sequencing",
    "❯ 1. Identity fix first",
    "  2. One combined build",
    RULE,
    "Enter to select · ↑/↓ to navigate · Esc to cancel",
)

# Prose that merely mentions picker words but has no numbered options — must not
# be mistaken for an open picker (else heartbeats would starve).
PROSE_MENTIONING_PICKER = _pane(
    "  Assistant: press Enter to select the default in most editors.",
    RULE,
    " ❯ ",
    RULE,
    "  ⏵⏵ bypass permissions on (shift+tab to cycle) · esc to interrupt",
)


def _skips(pane: str) -> bool:
    """True if the heartbeat would SKIP this cycle for this pane."""
    return bridge._pane_blocks_automated_send(pane) is not None


class TestPaneBlocksAutomatedSend(unittest.TestCase):
    def test_real_draft_skips(self):
        self.assertEqual(
            bridge._pane_blocks_automated_send(REAL_DRAFT),
            "composer-holds-unsent-input",
        )

    def test_ghost_suggestion_sends(self):
        # A dim ghost suggestion is not a real draft — the send is allowed.
        self.assertIsNone(bridge._pane_blocks_automated_send(GHOST_ONLY))

    def test_empty_composer_sends(self):
        self.assertIsNone(bridge._pane_blocks_automated_send(EMPTY_COMPOSER))

    def test_open_picker_skips(self):
        self.assertEqual(
            bridge._pane_blocks_automated_send(OPEN_PICKER),
            "askuserquestion-picker-open",
        )

    def test_empty_capture_sends_fail_open(self):
        # capture_pane returns "" on failure -> send is allowed (fail open).
        self.assertIsNone(bridge._pane_blocks_automated_send(""))

    def test_unparseable_pane_sends_fail_open(self):
        garbage = f"garbage {ESC}[ truncated pane with no structure at all"
        self.assertIsNone(bridge._pane_blocks_automated_send(garbage))

    def test_prose_mentioning_picker_sends(self):
        self.assertFalse(_skips(PROSE_MENTIONING_PICKER))


class TestComponentDetectors(unittest.TestCase):
    def test_ghost_line_is_ghost(self):
        self.assertTrue(
            bridge._post_prompt_is_ghost(f" ❯ {ESC}[2mghost text{ESC}[22m")
        )

    def test_bright_line_is_not_ghost(self):
        self.assertFalse(bridge._post_prompt_is_ghost(" ❯ real user text"))

    def test_picker_detector_true_and_false(self):
        self.assertTrue(bridge._pane_has_open_picker(OPEN_PICKER))
        self.assertFalse(bridge._pane_has_open_picker(PROSE_MENTIONING_PICKER))
        self.assertFalse(bridge._pane_has_open_picker(EMPTY_COMPOSER))

    def test_draft_detector_true_and_false(self):
        self.assertTrue(bridge._composer_has_unsent_draft(REAL_DRAFT))
        self.assertFalse(bridge._composer_has_unsent_draft(EMPTY_COMPOSER))
        self.assertFalse(bridge._composer_has_unsent_draft(GHOST_ONLY))


if __name__ == "__main__":
    unittest.main(verbosity=2)
