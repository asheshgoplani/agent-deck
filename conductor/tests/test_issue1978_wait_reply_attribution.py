"""Issue #1978 / PR #2273 round 2: the bridge's wait path must return the reply
that ``session send --wait -q`` bound to THIS message.

``--wait -q`` prints exactly the turn-bound reply on stdout (the CLI binds it
to the transcript record of the submitted prompt). The bridge used to discard
that stdout and re-fetch ``session output``, i.e. whatever the LATEST reply in
the transcript was at that moment — which reopens the attribution race the
turn binding exists to close: a turn that started after ours, or a queued
message from another sender, could be handed back as our reply.
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
from bridge import send_to_conductor  # noqa: E402


def _completed(returncode: int = 0, stdout: str = "", stderr: str = ""):
    return subprocess.CompletedProcess(["agent-deck"], returncode, stdout, stderr)


def test_wait_reply_is_the_turn_bound_stdout_not_the_latest_output():
    calls = []

    def fake_run_cli(*args, **kwargs):
        calls.append(args)
        assert "--wait" in args, "the wait path must send with --wait"
        return _completed(0, stdout="TURN-BOUND REPLY\n")

    with mock.patch("bridge.run_cli", side_effect=fake_run_cli), mock.patch(
        "bridge.get_session_output", return_value="LATEST OTHER TURN"
    ) as latest:
        ok, reply, still_running = send_to_conductor(
            "conductor", "question", wait_for_reply=True, response_timeout=5
        )

    assert ok is True and still_running is False
    assert reply == "TURN-BOUND REPLY"
    assert latest.call_count == 0, "must not re-fetch the latest output for an attributed reply"
    assert len(calls) == 1


def test_empty_attributed_reply_is_preserved_never_substituted():
    """An empty end-of-turn record is a valid completed result. Substituting
    ``session output`` would hand back a later or unrelated reply as this
    turn's answer, which is the race the turn binding exists to close."""
    with mock.patch("bridge.run_cli", return_value=_completed(0, stdout="  \n")), mock.patch(
        "bridge.get_session_output", return_value="LATEST OTHER TURN"
    ) as latest:
        ok, reply, still_running = send_to_conductor(
            "conductor", "question", wait_for_reply=True, response_timeout=5
        )
    assert ok is True and still_running is False
    assert reply == ""
    assert latest.call_count == 0, "an empty attributed reply must not be replaced by the latest output"
