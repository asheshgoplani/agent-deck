"""Tests for conductor/bridge.py core helpers."""

from __future__ import annotations

import json
import sys
from pathlib import Path
from unittest import mock

import pytest

# Make bridge importable without its optional runtime deps (aiogram, slack_bolt).
sys.path.insert(0, str(Path(__file__).parent.parent))

# Stub optional heavy deps before importing bridge so the module loads cleanly
# in a minimal test environment (no Telegram/Slack credentials required).
for _mod in ("aiogram", "aiogram.filters", "acp"):
    if _mod not in sys.modules:
        sys.modules[_mod] = mock.MagicMock()

import bridge  # noqa: E402  (must be after sys.path + stub setup)


# ---------------------------------------------------------------------------
# Fixtures
# ---------------------------------------------------------------------------


@pytest.fixture(autouse=True)
def reset_bridge_state():
    """Clear module-level mutable state before every test."""
    bridge._acp_connections.clear()
    bridge._acp_sessions.clear()
    yield
    bridge._acp_connections.clear()
    bridge._acp_sessions.clear()


@pytest.fixture
def conductor_dir(tmp_path, monkeypatch):
    """Point CONDUCTOR_DIR at a temp directory and return it."""
    monkeypatch.setattr(bridge, "CONDUCTOR_DIR", tmp_path)
    return tmp_path


def _write_meta(conductor_dir: Path, name: str, **extra) -> Path:
    """Create a conductor meta.json and return its path."""
    meta = {"name": name, "profile": "default", **extra}
    d = conductor_dir / name
    d.mkdir(parents=True, exist_ok=True)
    p = d / "meta.json"
    p.write_text(json.dumps(meta))
    return p


# ---------------------------------------------------------------------------
# is_acp_conductor
# ---------------------------------------------------------------------------


class TestIsAcpConductor:
    def test_copilot_agent_returns_true(self):
        assert bridge.is_acp_conductor({"agent": "copilot"}) is True

    def test_copilot_case_insensitive(self):
        assert bridge.is_acp_conductor({"agent": "Copilot"}) is True

    def test_claude_agent_returns_false(self):
        assert bridge.is_acp_conductor({"agent": "claude"}) is False

    def test_missing_agent_returns_false(self):
        assert bridge.is_acp_conductor({}) is False


# ---------------------------------------------------------------------------
# _get_acp_conductor_meta
# ---------------------------------------------------------------------------


class TestGetAcpConductorMeta:
    def test_returns_meta_for_copilot_conductor(self, conductor_dir):
        _write_meta(conductor_dir, "my-copilot", agent="copilot")
        result = bridge._get_acp_conductor_meta("my-copilot")
        assert result is not None
        assert result["name"] == "my-copilot"

    def test_returns_none_for_non_acp_conductor(self, conductor_dir):
        _write_meta(conductor_dir, "claude-bot", agent="claude")
        assert bridge._get_acp_conductor_meta("claude-bot") is None

    def test_returns_none_when_no_meta_file(self, conductor_dir):
        (conductor_dir / "ghost").mkdir()
        assert bridge._get_acp_conductor_meta("ghost") is None

    def test_returns_none_on_invalid_json(self, conductor_dir):
        d = conductor_dir / "broken"
        d.mkdir()
        (d / "meta.json").write_text("{not valid json")
        assert bridge._get_acp_conductor_meta("broken") is None


# ---------------------------------------------------------------------------
# discover_conductors
# ---------------------------------------------------------------------------


class TestDiscoverConductors:
    def test_empty_dir_returns_empty_list(self, conductor_dir):
        assert bridge.discover_conductors() == []

    def test_single_conductor_is_discovered(self, conductor_dir):
        _write_meta(conductor_dir, "alpha")
        conductors = bridge.discover_conductors()
        assert len(conductors) == 1
        assert conductors[0]["name"] == "alpha"

    def test_multiple_conductors_sorted_by_name(self, conductor_dir):
        _write_meta(conductor_dir, "zebra")
        _write_meta(conductor_dir, "alpha")
        _write_meta(conductor_dir, "mango")
        names = [c["name"] for c in bridge.discover_conductors()]
        assert names == ["alpha", "mango", "zebra"]

    def test_skips_invalid_json(self, conductor_dir):
        _write_meta(conductor_dir, "good")
        bad_dir = conductor_dir / "bad"
        bad_dir.mkdir()
        (bad_dir / "meta.json").write_text("{bad json")
        conductors = bridge.discover_conductors()
        assert len(conductors) == 1
        assert conductors[0]["name"] == "good"

    def test_non_existent_dir_returns_empty_list(self, monkeypatch, tmp_path):
        monkeypatch.setattr(bridge, "CONDUCTOR_DIR", tmp_path / "does-not-exist")
        assert bridge.discover_conductors() == []


# ---------------------------------------------------------------------------
# conductor_session_title / get_conductor_names / get_default_conductor
# ---------------------------------------------------------------------------


class TestConductorHelpers:
    def test_session_title_format(self):
        assert bridge.conductor_session_title("my-bot") == "conductor-my-bot"

    def test_get_conductor_names(self, conductor_dir):
        _write_meta(conductor_dir, "a")
        _write_meta(conductor_dir, "b")
        assert sorted(bridge.get_conductor_names()) == ["a", "b"]

    def test_get_default_conductor_returns_first_alphabetically(self, conductor_dir):
        _write_meta(conductor_dir, "zebra")
        _write_meta(conductor_dir, "alpha")
        default = bridge.get_default_conductor()
        assert default is not None
        assert default["name"] == "alpha"

    def test_get_default_conductor_empty(self, conductor_dir):
        assert bridge.get_default_conductor() is None

    def test_get_unique_profiles(self, conductor_dir):
        _write_meta(conductor_dir, "a", profile="work")
        _write_meta(conductor_dir, "b", profile="personal")
        _write_meta(conductor_dir, "c", profile="work")
        profiles = bridge.get_unique_profiles()
        assert profiles == ["personal", "work"]


# ---------------------------------------------------------------------------
# _conductor_name_from_session
# ---------------------------------------------------------------------------


class TestConductorNameFromSession:
    def test_strips_prefix(self):
        assert bridge._conductor_name_from_session("conductor-my-bot") == "my-bot"

    def test_no_prefix_unchanged(self):
        assert bridge._conductor_name_from_session("raw-name") == "raw-name"


# ---------------------------------------------------------------------------
# parse_conductor_prefix
# ---------------------------------------------------------------------------


class TestParseConductorPrefix:
    def test_exact_match_at_start(self):
        name, msg = bridge.parse_conductor_prefix("alpha: hello there", ["alpha", "beta"])
        assert name == "alpha"
        assert msg == "hello there"

    def test_strips_leading_whitespace_from_message(self):
        _, msg = bridge.parse_conductor_prefix("beta:  spaced", ["alpha", "beta"])
        assert msg == "spaced"

    def test_no_prefix_returns_none(self):
        name, msg = bridge.parse_conductor_prefix("no prefix here", ["alpha", "beta"])
        assert name is None
        assert msg == "no prefix here"

    def test_prefix_not_at_start_is_ignored(self):
        name, msg = bridge.parse_conductor_prefix("send to alpha: later", ["alpha"])
        assert name is None

    def test_empty_conductor_list(self):
        name, msg = bridge.parse_conductor_prefix("alpha: hi", [])
        assert name is None
        assert msg == "alpha: hi"


# ---------------------------------------------------------------------------
# split_message
# ---------------------------------------------------------------------------


class TestSplitMessage:
    def test_short_message_not_split(self):
        chunks = bridge.split_message("hello", max_len=100)
        assert chunks == ["hello"]

    def test_long_message_split_at_newline(self):
        text = "line one\n" + "x" * 100
        chunks = bridge.split_message(text, max_len=20)
        assert len(chunks) >= 2
        assert chunks[0] == "line one"

    def test_long_message_split_at_max_when_no_newline(self):
        text = "a" * 50
        chunks = bridge.split_message(text, max_len=20)
        for chunk in chunks:
            assert len(chunk) <= 20

    def test_exact_length_not_split(self):
        text = "a" * 100
        assert bridge.split_message(text, max_len=100) == [text]

    def test_rejoined_contains_all_content(self):
        text = "word1\nword2\nword3\nword4\nword5"
        chunks = bridge.split_message(text, max_len=10)
        rejoined = "\n".join(chunks)
        for word in ["word1", "word2", "word3", "word4", "word5"]:
            assert word in rejoined


# ---------------------------------------------------------------------------
# md_to_tg_html
# ---------------------------------------------------------------------------


class TestMdToTgHtml:
    def test_bold_conversion(self):
        assert "<b>hello</b>" in bridge.md_to_tg_html("**hello**")

    def test_italic_conversion(self):
        assert "<i>world</i>" in bridge.md_to_tg_html("*world*")

    def test_code_span_conversion(self):
        result = bridge.md_to_tg_html("`code`")
        assert "<code>code</code>" in result

    def test_html_special_chars_escaped(self):
        result = bridge.md_to_tg_html("<script>alert('xss')</script>")
        assert "<script>" not in result
        assert "&lt;script&gt;" in result

    def test_code_content_not_bold_converted(self):
        result = bridge.md_to_tg_html("`**not bold**`")
        assert "<b>" not in result
        assert "<code>" in result

    def test_plain_text_unchanged(self):
        result = bridge.md_to_tg_html("just plain text")
        assert result == "just plain text"

    def test_ampersand_escaped(self):
        result = bridge.md_to_tg_html("cats & dogs")
        assert "&amp;" in result


# ---------------------------------------------------------------------------
# send_to_conductor_async — routing logic (no real ACP/CLI calls)
# ---------------------------------------------------------------------------


class TestSendToConductorAsyncRouting:
    @pytest.mark.asyncio
    async def test_routes_to_acp_for_copilot_conductor(self, conductor_dir):
        _write_meta(conductor_dir, "my-copilot", agent="copilot")

        with mock.patch.object(bridge, "send_to_acp_conductor") as mock_acp:
            mock_acp.return_value = (True, "response")
            ok, text = await bridge.send_to_conductor_async("conductor-my-copilot", "hello")

        mock_acp.assert_called_once()
        assert ok is True
        assert text == "response"

    @pytest.mark.asyncio
    async def test_routes_to_cli_for_non_acp_conductor(self, conductor_dir):
        _write_meta(conductor_dir, "claude-bot", agent="claude")

        with mock.patch.object(bridge, "send_to_conductor") as mock_cli:
            mock_cli.return_value = (True, "cli-response")
            ok, text = await bridge.send_to_conductor_async("conductor-claude-bot", "hello")

        mock_cli.assert_called_once()
        assert ok is True
