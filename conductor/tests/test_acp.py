"""Tests for the ACP (Agent Client Protocol) client module."""

import asyncio
import json
import sys
from pathlib import Path
from unittest import mock

import pytest

# Import ACP module
sys.path.insert(0, str(Path(__file__).parent.parent))
from acp import (
    ACPConnection,
    AgentInfo,
    PromptResult,
    encode_message,
    decode_message,
    _make_request,
)


# ---------------------------------------------------------------------------
# NDJSON codec tests
# ---------------------------------------------------------------------------


class TestNDJSONCodec:
    """Test NDJSON encoding/decoding helpers."""

    def test_encode_message(self):
        req = _make_request("initialize", {"protocolVersion": 1}, 1)
        raw = encode_message(req)
        parsed = json.loads(raw.strip())
        assert parsed["jsonrpc"] == "2.0"
        assert parsed["id"] == 1
        assert parsed["method"] == "initialize"
        assert parsed["params"]["protocolVersion"] == 1
        assert raw.endswith(b"\n")

    def test_encode_notification(self):
        msg = {"jsonrpc": "2.0", "method": "session/cancel", "params": {"sessionId": "abc"}}
        raw = encode_message(msg)
        parsed = json.loads(raw.strip())
        assert parsed["jsonrpc"] == "2.0"
        assert "id" not in parsed
        assert parsed["method"] == "session/cancel"
        assert raw.endswith(b"\n")

    def test_decode_response(self):
        raw = b'{"jsonrpc": "2.0", "id": 1, "result": {"ok": true}}'
        msg = decode_message(raw)
        assert msg["id"] == 1
        assert msg["result"]["ok"] is True

    def test_decode_notification(self):
        raw = b'{"jsonrpc": "2.0", "method": "session/update", "params": {"type": "agent_message_chunk"}}'
        msg = decode_message(raw)
        assert msg["method"] == "session/update"
        assert "id" not in msg

    def test_decode_error_response(self):
        raw = b'{"jsonrpc": "2.0", "id": 2, "error": {"code": -32600, "message": "Invalid request"}}'
        msg = decode_message(raw)
        assert msg["id"] == 2
        assert msg["error"]["code"] == -32600

    def test_decode_empty_line(self):
        assert decode_message(b"") is None
        assert decode_message(b"   ") is None

    def test_decode_invalid_json(self):
        result = decode_message(b"not json at all")
        assert result is None


# ---------------------------------------------------------------------------
# Data class tests
# ---------------------------------------------------------------------------


class TestDataClasses:
    """Test AgentInfo and PromptResult."""

    def test_agent_info_defaults(self):
        info = AgentInfo()
        assert info.name == ""
        assert info.version == ""
        assert info.capabilities == {}

    def test_agent_info_with_values(self):
        info = AgentInfo(name="copilot-agent", version="1.0.0", capabilities={"streaming": True})
        assert info.name == "copilot-agent"
        assert info.version == "1.0.0"
        assert info.capabilities == {"streaming": True}

    def test_prompt_result(self):
        result = PromptResult(text="Hello world", stop_reason="end_turn")
        assert result.text == "Hello world"
        assert result.stop_reason == "end_turn"

    def test_prompt_result_defaults(self):
        result = PromptResult()
        assert result.text == ""
        assert result.stop_reason == "end_turn"
        assert result.tool_calls == []


# ---------------------------------------------------------------------------
# ACPConnection tests (with mock subprocess)
# ---------------------------------------------------------------------------


class TestACPConnection:
    """Test ACPConnection lifecycle with mocked subprocess."""

    def test_init_defaults(self):
        conn = ACPConnection()
        assert conn._command == ["copilot", "--acp", "--stdio"]
        assert not conn.is_running

    def test_init_custom_command(self):
        conn = ACPConnection(command=["my-agent", "--stdio"])
        assert conn._command == ["my-agent", "--stdio"]
        assert not conn.is_running

    @pytest.mark.asyncio
    async def test_start_sends_initialize(self):
        """Test that start() spawns process and attempts initialization."""
        conn = ACPConnection(command=["echo"])

        # We can't easily mock the full read loop, so just verify
        # that start() raises on timeout with a very short timeout
        # (proving it does attempt to connect)
        mock_process = mock.AsyncMock()
        mock_process.returncode = None
        mock_process.pid = 12345
        mock_process.stdout = mock.AsyncMock()
        mock_process.stdout.readline = mock.AsyncMock(return_value=b"")
        mock_process.stdout.at_eof = mock.Mock(return_value=True)
        mock_process.stdin = mock.AsyncMock()
        mock_process.stdin.write = mock.Mock()
        mock_process.stdin.drain = mock.AsyncMock()
        mock_process.stdin.close = mock.Mock()
        mock_process.stderr = mock.AsyncMock()
        mock_process.stderr.readline = mock.AsyncMock(return_value=b"")
        mock_process.stderr.at_eof = mock.Mock(return_value=True)
        mock_process.wait = mock.AsyncMock()

        with mock.patch("asyncio.create_subprocess_exec", return_value=mock_process):
            with pytest.raises(RuntimeError, match="timed out"):
                await conn.start(timeout=0.1)

        # Verify it tried to write the initialize request to stdin
        mock_process.stdin.write.assert_called_once()
        written = mock_process.stdin.write.call_args[0][0]
        parsed = json.loads(written.strip())
        assert parsed["method"] == "initialize"
        assert parsed["params"]["clientInfo"]["name"] == "agent-deck-conductor"

    @pytest.mark.asyncio
    async def test_stop_idempotent(self):
        """Test that stop() can be called multiple times safely."""
        conn = ACPConnection()
        conn._stopped = True
        # Should not raise
        await conn.stop()
        assert conn._stopped is True

    def test_request_id_increments(self):
        conn = ACPConnection()
        id1 = conn._next_id()
        id2 = conn._next_id()
        assert id2 == id1 + 1
