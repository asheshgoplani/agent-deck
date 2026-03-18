"""Tests for the bridge.py hook system."""

import json
import os
import shutil
import stat
import tempfile
from pathlib import Path
from unittest import mock

import pytest

# Import hook functions from bridge
import sys
sys.path.insert(0, str(Path(__file__).parent.parent))
from bridge import resolve_hook, run_hook, invoke_hook, CONDUCTOR_DIR, DEFAULT_HOOK_TIMEOUT


FIXTURES_DIR = Path(__file__).parent / "fixtures" / "hooks"


@pytest.fixture
def hook_dirs(tmp_path):
    """Create temporary conductor directory structure with hooks dirs."""
    conductor_dir = tmp_path / "conductor"
    profile_hooks = conductor_dir / "work" / "hooks"
    global_hooks = conductor_dir / "hooks"
    profile_hooks.mkdir(parents=True)
    global_hooks.mkdir(parents=True)
    return conductor_dir, profile_hooks, global_hooks


# ---------------------------------------------------------------------------
# resolve_hook tests
# ---------------------------------------------------------------------------


class TestResolveHook:
    def test_profile_level_hook_found(self, hook_dirs):
        conductor_dir, profile_hooks, _ = hook_dirs
        hook = profile_hooks / "pre-heartbeat"
        shutil.copy(FIXTURES_DIR / "pass-through", hook)
        hook.chmod(hook.stat().st_mode | stat.S_IEXEC)

        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = resolve_hook("work", "pre-heartbeat")
        assert result == hook

    def test_global_fallback(self, hook_dirs):
        conductor_dir, _, global_hooks = hook_dirs
        hook = global_hooks / "pre-heartbeat"
        shutil.copy(FIXTURES_DIR / "pass-through", hook)
        hook.chmod(hook.stat().st_mode | stat.S_IEXEC)

        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = resolve_hook("work", "pre-heartbeat")
        assert result == hook

    def test_profile_overrides_global(self, hook_dirs):
        conductor_dir, profile_hooks, global_hooks = hook_dirs

        # Create both profile and global hooks
        for hooks_dir in [profile_hooks, global_hooks]:
            hook = hooks_dir / "pre-heartbeat"
            shutil.copy(FIXTURES_DIR / "pass-through", hook)
            hook.chmod(hook.stat().st_mode | stat.S_IEXEC)

        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = resolve_hook("work", "pre-heartbeat")
        # Should pick the profile-level one
        assert result == profile_hooks / "pre-heartbeat"

    def test_missing_hook_returns_none(self, hook_dirs):
        conductor_dir, _, _ = hook_dirs
        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = resolve_hook("work", "nonexistent")
        assert result is None

    def test_not_executable_returns_none(self, hook_dirs):
        conductor_dir, profile_hooks, _ = hook_dirs
        hook = profile_hooks / "pre-heartbeat"
        shutil.copy(FIXTURES_DIR / "not-executable", hook)
        # Ensure it's NOT executable
        hook.chmod(stat.S_IRUSR | stat.S_IWUSR)

        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = resolve_hook("work", "pre-heartbeat")
        assert result is None


# ---------------------------------------------------------------------------
# run_hook tests
# ---------------------------------------------------------------------------


class TestRunHook:
    def test_success_with_output(self):
        hook_path = FIXTURES_DIR / "transform"
        exit_code, stdout, stderr = run_hook(hook_path, {"profile": "work"})
        assert exit_code == 0
        assert stdout.strip() == "transformed message"
        assert stderr == ""

    def test_success_no_output(self):
        hook_path = FIXTURES_DIR / "pass-through"
        exit_code, stdout, stderr = run_hook(hook_path, {"profile": "work"})
        assert exit_code == 0
        assert stdout.strip() == ""

    def test_gate_exit_code(self):
        hook_path = FIXTURES_DIR / "gate"
        exit_code, stdout, stderr = run_hook(hook_path, {"profile": "work"})
        assert exit_code == 1
        assert "blocked by hook" in stderr

    def test_crash_exit_code(self):
        hook_path = FIXTURES_DIR / "crash"
        exit_code, stdout, stderr = run_hook(hook_path, {"profile": "work"})
        assert exit_code == 2
        assert "something went wrong" in stderr

    def test_timeout(self):
        hook_path = FIXTURES_DIR / "slow"
        exit_code, stdout, stderr = run_hook(hook_path, {"profile": "work"}, timeout=1)
        assert exit_code == 1
        assert stderr == "timeout"

    def test_stdin_json_passed(self):
        hook_path = FIXTURES_DIR / "echo-stdin"
        context = {"profile": "work", "waiting": 3, "sessions": [{"title": "s1"}]}
        exit_code, stdout, stderr = run_hook(hook_path, context)
        assert exit_code == 0
        parsed = json.loads(stdout)
        assert parsed["profile"] == "work"
        assert parsed["waiting"] == 3

    def test_env_vars_set(self):
        """Verify CONDUCTOR_PROFILE and CONDUCTOR_DIR env vars are set."""
        # Create a temp hook that prints env vars
        with tempfile.NamedTemporaryFile(mode="w", suffix=".sh", delete=False) as f:
            f.write("#!/bin/sh\necho $CONDUCTOR_PROFILE:$CONDUCTOR_DIR\n")
            f.flush()
            os.chmod(f.name, 0o755)
            try:
                exit_code, stdout, stderr = run_hook(
                    Path(f.name), {"profile": "test-profile"}
                )
                assert exit_code == 0
                parts = stdout.strip().split(":")
                assert parts[0] == "test-profile"
            finally:
                os.unlink(f.name)


# ---------------------------------------------------------------------------
# invoke_hook tests
# ---------------------------------------------------------------------------


class TestInvokeHook:
    def test_no_hook_returns_none(self, hook_dirs):
        conductor_dir, _, _ = hook_dirs
        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = invoke_hook("work", "nonexistent", {"profile": "work"})
        assert result is None

    def test_successful_hook(self, hook_dirs):
        conductor_dir, profile_hooks, _ = hook_dirs
        hook = profile_hooks / "pre-heartbeat"
        shutil.copy(FIXTURES_DIR / "transform", hook)
        hook.chmod(hook.stat().st_mode | stat.S_IEXEC)

        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = invoke_hook("work", "pre-heartbeat", {"profile": "work"})
        assert result is not None
        success, stdout = result
        assert success is True
        assert stdout == "transformed message"

    def test_gating_hook(self, hook_dirs):
        conductor_dir, profile_hooks, _ = hook_dirs
        hook = profile_hooks / "pre-heartbeat"
        shutil.copy(FIXTURES_DIR / "gate", hook)
        hook.chmod(hook.stat().st_mode | stat.S_IEXEC)

        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = invoke_hook("work", "pre-heartbeat", {"profile": "work"})
        assert result is not None
        success, stdout = result
        assert success is False

    def test_custom_timeout_from_meta(self, hook_dirs):
        conductor_dir, profile_hooks, _ = hook_dirs
        hook = profile_hooks / "pre-heartbeat"
        shutil.copy(FIXTURES_DIR / "slow", hook)
        hook.chmod(hook.stat().st_mode | stat.S_IEXEC)

        # Write meta.json with custom timeout
        meta_path = conductor_dir / "work" / "meta.json"
        meta_path.write_text(json.dumps({"hooks": {"timeout": 1}}))

        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = invoke_hook("work", "pre-heartbeat", {"profile": "work"})
        assert result is not None
        success, _ = result
        assert success is False  # timed out


# ---------------------------------------------------------------------------
# Integration tests: pre-heartbeat scenarios
# ---------------------------------------------------------------------------


class TestPreHeartbeatIntegration:
    """Test pre-heartbeat hook behavior in context."""

    def test_hook_transforms_message(self, hook_dirs):
        """pre-heartbeat stdout replaces the draft message."""
        conductor_dir, profile_hooks, _ = hook_dirs
        hook = profile_hooks / "pre-heartbeat"
        shutil.copy(FIXTURES_DIR / "transform", hook)
        hook.chmod(hook.stat().st_mode | stat.S_IEXEC)

        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = invoke_hook("work", "pre-heartbeat", {
                "profile": "work",
                "waiting": 1,
                "running": 0,
                "idle": 0,
                "error": 0,
                "sessions": [],
                "draft_message": "original heartbeat message",
            })

        assert result is not None
        success, stdout = result
        assert success is True
        assert stdout == "transformed message"
        # Caller would use stdout as heartbeat_msg since it's non-empty

    def test_hook_gates_heartbeat(self, hook_dirs):
        """pre-heartbeat non-zero exit skips the heartbeat cycle."""
        conductor_dir, profile_hooks, _ = hook_dirs
        hook = profile_hooks / "pre-heartbeat"
        shutil.copy(FIXTURES_DIR / "gate", hook)
        hook.chmod(hook.stat().st_mode | stat.S_IEXEC)

        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = invoke_hook("work", "pre-heartbeat", {
                "profile": "work",
                "waiting": 1,
                "running": 0,
                "idle": 0,
                "error": 0,
                "sessions": [],
                "draft_message": "original heartbeat message",
            })

        assert result is not None
        success, _ = result
        assert success is False
        # Caller would `continue` to skip this profile's heartbeat

    def test_no_hook_returns_none(self, hook_dirs):
        """When no hook exists, returns None (caller uses default behavior)."""
        conductor_dir, _, _ = hook_dirs
        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = invoke_hook("work", "pre-heartbeat", {
                "profile": "work",
                "waiting": 1,
                "running": 0,
                "idle": 0,
                "error": 0,
                "sessions": [],
                "draft_message": "original heartbeat message",
            })
        assert result is None

    def test_pass_through_keeps_original(self, hook_dirs):
        """Hook exits 0 with empty stdout, caller keeps original message."""
        conductor_dir, profile_hooks, _ = hook_dirs
        hook = profile_hooks / "pre-heartbeat"
        shutil.copy(FIXTURES_DIR / "pass-through", hook)
        hook.chmod(hook.stat().st_mode | stat.S_IEXEC)

        with mock.patch("bridge.CONDUCTOR_DIR", conductor_dir):
            result = invoke_hook("work", "pre-heartbeat", {
                "profile": "work",
                "waiting": 1,
                "running": 0,
                "idle": 0,
                "error": 0,
                "sessions": [],
                "draft_message": "original heartbeat message",
            })

        assert result is not None
        success, stdout = result
        assert success is True
        assert stdout == ""
        # Caller would keep the original draft_message since stdout is empty
