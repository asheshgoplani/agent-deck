"""Issue #3252: the bridge killed `session send --wait` at its outer deadline
and told the chat "[Failed to send message to conductor ...]" although Codex
had already accepted the message. A killed CLI printed no result, so nobody
knows whether the message landed: the bridge must say exactly that, never
call it a failure, never resend it and never attach a reply watcher to it.
"""

from __future__ import annotations

import subprocess
import sys
import types
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent.parent))
try:
    import toml  # noqa: F401
except ModuleNotFoundError:
    sys.modules["toml"] = types.SimpleNamespace(load=lambda *_a, **_k: {})

import bridge  # noqa: E402

UNCONFIRMED = "unconfirmed"


def _killed_at_outer_deadline():
    # What run_cli returns after it SIGKILLs a CLI that outlived its timeout.
    return subprocess.CompletedProcess(["agent-deck"], 1, "", "timeout")


def test_outer_deadline_kill_is_unconfirmed_not_failed():
    with mock.patch("bridge.run_cli", return_value=_killed_at_outer_deadline()) as cli:
        ok, response, pending = bridge.send_to_conductor(
            "conductor-x", "do the thing", wait_for_reply=True, claim_late_reply=True,
        )
    assert (ok, response) == (False, "")
    assert pending == UNCONFIRMED, f"want an explicit unconfirmed result, got {pending!r}"
    assert cli.call_count == 1  # never resent
    assert not bridge._wait_send_reservations


def test_genuine_cli_error_is_still_a_failure():
    result = subprocess.CompletedProcess(["agent-deck"], 1, "", "session not found")
    with mock.patch("bridge.run_cli", return_value=result):
        ok, _, pending = bridge.send_to_conductor(
            "conductor-x", "hi", wait_for_reply=True,
        )
    assert ok is False and pending is False


def test_unconfirmed_notice_tells_the_user_not_to_resend_blindly():
    text = bridge._unconfirmed_send_notice("alpha")
    assert "Failed" not in text
    assert "alpha" in text
    assert "unconfirmed" in text.lower()
    assert "not resent" in text.lower()
