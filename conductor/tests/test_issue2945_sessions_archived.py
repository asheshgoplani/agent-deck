"""Issue #2945: the bridge's sessions command must not list archived sessions.

`agent-deck list --json` keeps normal archives and flags them "archived": true.
Telegram /sessions, Slack and Discord /ad-sessions and Mattermost !sessions all
print get_sessions_list_all, so that is where they are left out.
"""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))

import bridge  # noqa: E402

LIVE = {"title": "live-worker", "tool": "claude", "status": "idle", "archived": False}
ARCHIVED = {"title": "archived-worker", "tool": "claude", "status": "idle", "archived": True}
# Older agent-deck builds print no "archived" field at all.
NO_FIELD = {"title": "old-build-worker", "tool": "claude", "status": "idle"}


def _serve_lists(monkeypatch, by_profile):
    def fake_run_cli(*args, profile=None, **_kwargs):
        assert args == ("list", "--json")
        stdout = json.dumps({"sessions": by_profile[profile]})
        return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")

    monkeypatch.setattr(bridge, "run_cli", fake_run_cli)
    monkeypatch.setattr(
        bridge,
        "discover_conductors",
        lambda: [{"name": p, "profile": p} for p in by_profile],
    )


def test_list_all_leaves_out_archived_in_every_profile(monkeypatch):
    _serve_lists(monkeypatch, {"a": [LIVE, ARCHIVED], "b": [ARCHIVED, NO_FIELD]})
    got = [(p, s["title"]) for p, s in bridge.get_sessions_list_all(["a", "b"])]
    assert got == [("a", "live-worker"), ("b", "old-build-worker")]


def test_sessions_reply_leaves_out_archived(monkeypatch):
    _serve_lists(monkeypatch, {"default": [LIVE, ARCHIVED]})
    reply = bridge.mattermost_command_reply("sessions", "")
    assert "live-worker" in reply
    assert "archived-worker" not in reply


def test_sessions_reply_when_every_session_is_archived(monkeypatch):
    _serve_lists(monkeypatch, {"default": [ARCHIVED]})
    assert bridge.mattermost_command_reply("sessions", "") == "No sessions found."


def test_sessions_list_keeps_archived_rows_for_conductor_dedupe(monkeypatch):
    # ensure_conductor_running reads this list to avoid creating a second
    # conductor at a path an archived one already holds.
    _serve_lists(monkeypatch, {"default": [LIVE, ARCHIVED]})
    assert bridge.get_sessions_list("default", fail_closed=True) == [LIVE, ARCHIVED]
