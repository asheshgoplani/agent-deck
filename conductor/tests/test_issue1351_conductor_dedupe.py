"""Regression tests for issue #1351.

Restarting the conductor bridge must not register a duplicate conductor row
when a same-title conductor already exists in the requested profile.
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
from bridge import ensure_conductor_running  # noqa: E402


_MISSING = object()


async def _no_sleep(_seconds: float) -> None:
    return None


def _completed(returncode: int = 0, stderr: str = "") -> subprocess.CompletedProcess:
    return subprocess.CompletedProcess(["agent-deck"], returncode, "", stderr)


def _run(coro):
    with mock.patch("bridge.asyncio.sleep", new=_no_sleep):
        return asyncio.run(coro)


def _calls_for(mock_cli: mock.Mock, *prefix: str) -> list[tuple]:
    return [
        call.args
        for call in mock_cli.call_args_list
        if call.args[:len(prefix)] == prefix
    ]


def _write_conductor_meta(
    tmp_path: Path,
    monkeypatch,
    *,
    name: str = "ops",
    profile: str = "work",
    agent: object = _MISSING,
    raw: str | None = None,
) -> Path:
    """Point the bridge at an isolated conductor root and write one meta.json."""
    root = tmp_path / "conductor"
    conductor_dir = root / name
    conductor_dir.mkdir(parents=True, exist_ok=True)
    if raw is None:
        meta = {"name": name, "profile": profile}
        if agent is not _MISSING:
            meta["agent"] = agent
        raw = json.dumps(meta)
    (conductor_dir / "meta.json").write_text(raw)
    monkeypatch.setattr(bridge, "CONDUCTOR_DIR", root)
    return root


def _point_at_absent_meta(tmp_path: Path, monkeypatch, name: str = "ops") -> Path:
    root = tmp_path / "conductor"
    (root / name).mkdir(parents=True)
    monkeypatch.setattr(bridge, "CONDUCTOR_DIR", root)
    return root


def _run_valid_fresh_recovery():
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
        ok = _run(ensure_conductor_running("ops", "work"))
    return ok, mock_cli


def _assert_valid_fresh_recovery_uses(expected_agent: str) -> None:
    ok, mock_cli = _run_valid_fresh_recovery()
    assert ok is True
    add = _calls_for(mock_cli, "add")[0]
    assert add[add.index("-c") + 1] == expected_agent


def test_existing_conductor_title_retries_start_without_add():
    with mock.patch(
        "bridge.get_session_status",
        side_effect=["unknown", "running"],
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=[{"title": "conductor-ops", "profile": "work", "id": "existing"}],
    ) as mock_sessions, mock.patch(
        "bridge.run_cli",
        side_effect=[_completed(1, "not found"), _completed(0)],
    ) as mock_cli:
        assert _run(ensure_conductor_running("ops", "work")) is True

    mock_sessions.assert_called_once_with(profile="work", fail_closed=True)
    assert _calls_for(mock_cli, "add") == []
    assert len(_calls_for(mock_cli, "session", "start", "conductor-ops")) == 2


def test_fresh_setup_creates_session_then_starts_it(tmp_path, monkeypatch):
    root = _write_conductor_meta(tmp_path, monkeypatch)
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
        assert _run(ensure_conductor_running("ops", "work")) is True

    commands = [call.args[:3] for call in mock_cli.call_args_list]
    assert commands == [
        ("session", "start", "conductor-ops"),
        ("add", str(root / "ops"), "-t"),
        ("session", "start", "conductor-ops"),
    ]
    assert len(_calls_for(mock_cli, "add")) == 1


def test_fresh_recovery_uses_codex_runtime_from_meta(tmp_path, monkeypatch):
    _write_conductor_meta(tmp_path, monkeypatch, agent="codex")
    _assert_valid_fresh_recovery_uses("codex")


def test_fresh_recovery_accepts_case_variant_codex_agent(tmp_path, monkeypatch):
    _write_conductor_meta(tmp_path, monkeypatch, raw='{"Agent":"codex"}')
    _assert_valid_fresh_recovery_uses("codex")


def test_fresh_recovery_last_non_null_case_variant_agent_wins(
    tmp_path, monkeypatch,
):
    for raw, expected_agent in (
        ('{"agent":"claude","Agent":"codex"}', "codex"),
        ('{"agent":"codex","agent":null}', "codex"),
        ('{"agent":"claude","Agent":"codex","agent":"hermes"}', "hermes"),
    ):
        _write_conductor_meta(tmp_path, monkeypatch, raw=raw)
        _assert_valid_fresh_recovery_uses(expected_agent)


def test_fresh_recovery_missing_agent_defaults_to_claude(tmp_path, monkeypatch):
    _write_conductor_meta(tmp_path, monkeypatch)
    _assert_valid_fresh_recovery_uses("claude")


def test_fresh_recovery_empty_agent_defaults_to_claude(tmp_path, monkeypatch):
    _write_conductor_meta(tmp_path, monkeypatch, agent="  ")
    _assert_valid_fresh_recovery_uses("claude")


def test_fresh_recovery_null_agent_defaults_to_claude(tmp_path, monkeypatch):
    _write_conductor_meta(tmp_path, monkeypatch, agent=None)
    _assert_valid_fresh_recovery_uses("claude")


def _assert_invalid_meta_fails_closed():
    with mock.patch(
        "bridge.get_session_status",
        return_value="unknown",
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=[],
    ), mock.patch(
        "bridge.run_cli",
        return_value=_completed(1, "not found"),
    ) as mock_cli:
        assert _run(ensure_conductor_running("ops", "work")) is False

    assert not _calls_for(mock_cli, "add")


def test_fresh_recovery_unsupported_agent_fails_closed_without_add(
    tmp_path, monkeypatch,
):
    _write_conductor_meta(tmp_path, monkeypatch, agent="not-a-conductor-runtime")
    _assert_invalid_meta_fails_closed()


def test_fresh_recovery_case_variant_unsupported_agent_fails_closed_without_add(
    tmp_path, monkeypatch,
):
    _write_conductor_meta(
        tmp_path,
        monkeypatch,
        raw='{"Agent":"not-a-conductor-runtime"}',
    )
    _assert_invalid_meta_fails_closed()


def test_fresh_recovery_malformed_metadata_fails_closed_without_add(
    tmp_path, monkeypatch,
):
    _write_conductor_meta(tmp_path, monkeypatch, raw='{"agent":')
    _assert_invalid_meta_fails_closed()


def test_fresh_recovery_non_object_metadata_fails_closed_without_add(
    tmp_path, monkeypatch,
):
    _write_conductor_meta(tmp_path, monkeypatch, raw="null")
    _assert_invalid_meta_fails_closed()


def test_fresh_recovery_malformed_agent_type_fails_closed_without_add(
    tmp_path, monkeypatch,
):
    for raw in ('{"agent":42}', '{"agent":42,"Agent":"codex"}'):
        _write_conductor_meta(tmp_path, monkeypatch, raw=raw)
        _assert_invalid_meta_fails_closed()


def test_fresh_recovery_absent_metadata_fails_closed_without_add(
    tmp_path, monkeypatch,
):
    _point_at_absent_meta(tmp_path, monkeypatch)
    _assert_invalid_meta_fails_closed()


def test_fresh_recovery_unreadable_metadata_fails_closed_without_add(
    tmp_path, monkeypatch,
):
    root = _point_at_absent_meta(tmp_path, monkeypatch)
    (root / "ops" / "meta.json").mkdir()
    _assert_invalid_meta_fails_closed()


def test_running_status_fast_path_has_no_cli_mutations():
    running_statuses = ("waiting", "running", "idle", "active", "starting")

    for status in running_statuses:
        with mock.patch(
            "bridge.get_session_status",
            return_value=status,
        ), mock.patch("bridge.get_sessions_list") as mock_sessions, mock.patch(
            "bridge.run_cli"
        ) as mock_cli:
            assert _run(ensure_conductor_running("ops", "work")) is True

        mock_sessions.assert_not_called()
        mock_cli.assert_not_called()


def test_same_title_in_different_profile_does_not_suppress_creation(
    tmp_path, monkeypatch,
):
    _write_conductor_meta(tmp_path, monkeypatch)
    with mock.patch(
        "bridge.get_session_status",
        side_effect=["unknown", "running"],
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=[{"title": "conductor-ops", "profile": "personal"}],
    ), mock.patch(
        "bridge.run_cli",
        side_effect=[_completed(1, "not found"), _completed(0), _completed(0)],
    ) as mock_cli:
        assert _run(ensure_conductor_running("ops", "work")) is True

    assert len(_calls_for(mock_cli, "add")) == 1


def test_existing_conductor_start_failure_returns_false_without_add():
    with mock.patch(
        "bridge.get_session_status",
        side_effect=["unknown", "unknown"],
    ), mock.patch(
        "bridge.get_sessions_list",
        return_value=[{"title": "conductor-ops", "profile": "work", "id": "existing"}],
    ), mock.patch(
        "bridge.run_cli",
        side_effect=[_completed(1, "not found"), _completed(1, "still not found")],
    ) as mock_cli:
        assert _run(ensure_conductor_running("ops", "work")) is False

    assert _calls_for(mock_cli, "add") == []
    assert len(_calls_for(mock_cli, "session", "start", "conductor-ops")) == 2
