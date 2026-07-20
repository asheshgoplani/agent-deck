"""Conductor sessions honor meta.json's "agent" field when auto-created.

The bridge previously hardcoded `-c claude` in ensure_conductor_running's
create path; a conductor declaring "agent": "omp" (or any agent-deck tool)
must be created with that tool, and conductors without the field keep the
claude default.
"""

from __future__ import annotations

import asyncio
import json
import subprocess
import sys
import types
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent.parent))
try:
    import toml  # noqa: F401
except ModuleNotFoundError:
    sys.modules["toml"] = types.SimpleNamespace(load=lambda *_args, **_kwargs: {})

import bridge  # noqa: E402
from bridge import (  # noqa: E402
    discover_conductors,
    ensure_conductor_running,
    session_in_conductor_scope,
)


async def _no_sleep(_seconds: float) -> None:
    return None


def _completed(returncode: int = 0, stderr: str = "") -> subprocess.CompletedProcess:
    return subprocess.CompletedProcess(["agent-deck"], returncode, "", stderr)


def _run(coro):
    with mock.patch("bridge.asyncio.sleep", new=_no_sleep):
        return asyncio.run(coro)


def _add_calls(mock_cli: mock.Mock) -> list[tuple]:
    return [
        call.args
        for call in mock_cli.call_args_list
        if call.args and call.args[0] == "add"
    ]


def _ensure_with_fresh_create(agent: str | None) -> mock.Mock:
    """Drive ensure_conductor_running down the create path, return the CLI mock."""
    with mock.patch(
        "bridge.get_session_status",
        side_effect=["unknown", "running"],
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=[],
    ), mock.patch(
        "bridge.run_cli",
        side_effect=[_completed(1, "not found"), _completed(0), _completed(0)],
    ) as mock_cli:
        if agent is None:
            assert _run(ensure_conductor_running("ops", "work")) is True
        else:
            assert _run(ensure_conductor_running("ops", "work", agent=agent)) is True
    return mock_cli


def test_ensure_conductor_running_uses_agent_param():
    mock_cli = _ensure_with_fresh_create("omp")
    adds = _add_calls(mock_cli)
    assert adds, "expected an 'add' CLI call on the fresh-create path"
    add = adds[0]
    ci = add.index("-c")
    assert add[ci + 1] == "omp"


def test_ensure_conductor_running_defaults_to_claude():
    mock_cli = _ensure_with_fresh_create(None)
    add = _add_calls(mock_cli)[0]
    ci = add.index("-c")
    assert add[ci + 1] == "claude"


def test_discover_conductors_normalizes_agent(tmp_path, monkeypatch):
    cdir = tmp_path / "legacy"
    cdir.mkdir()
    (cdir / "meta.json").write_text(
        json.dumps({"name": "legacy", "profile": "default"})
    )
    cdir2 = tmp_path / "omperator"
    cdir2.mkdir()
    (cdir2 / "meta.json").write_text(
        json.dumps({"name": "omperator", "profile": "default", "agent": "omp"})
    )
    monkeypatch.setattr(bridge, "CONDUCTOR_DIR", tmp_path)
    metas = {m["name"]: m for m in discover_conductors()}
    assert metas["legacy"]["agent"] == "claude"
    assert metas["omperator"]["agent"] == "omp"


GROUPED = {"name": "ops", "profile": "default"}
ALL_SCOPE = {"name": "ops", "profile": "default", "scope": "all"}


def test_scope_default_matches_own_group_only():
    assert session_in_conductor_scope(GROUPED, {"title": "a", "group": "ops"})
    assert session_in_conductor_scope(GROUPED, {"title": "b", "group": "ops/sub"})
    assert not session_in_conductor_scope(GROUPED, {"title": "c", "group": "prod"})
    assert not session_in_conductor_scope(GROUPED, {"title": "d", "group": ""})
    # "opsx" must not match via prefix confusion
    assert not session_in_conductor_scope(GROUPED, {"title": "e", "group": "opsx"})


def test_scope_all_matches_every_group():
    assert session_in_conductor_scope(ALL_SCOPE, {"title": "a", "group": "prod"})
    assert session_in_conductor_scope(ALL_SCOPE, {"title": "b", "group": ""})
    assert session_in_conductor_scope(ALL_SCOPE, {"title": "c", "group": "experimental"})


def test_scope_always_excludes_conductor_sessions():
    for meta in (GROUPED, ALL_SCOPE):
        assert not session_in_conductor_scope(
            meta, {"title": "conductor-omp-canary", "group": "conductor"}
        )
        assert not session_in_conductor_scope(
            meta, {"title": "conductor-ops", "group": "ops"}
        )
