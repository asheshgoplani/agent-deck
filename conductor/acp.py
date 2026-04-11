#!/usr/bin/env python3
"""
ACP Client: Pure Python Agent Client Protocol implementation over stdio.

Provides an async ACP client that manages a `copilot --acp --stdio` subprocess
and communicates via NDJSON (newline-delimited JSON-RPC 2.0) over stdin/stdout.

Usage:
    from acp import ACPConnection

    conn = ACPConnection(command=["copilot", "--acp", "--stdio"], cwd="/path/to/project")
    await conn.start()
    session_id = await conn.new_session(cwd="/path/to/project")
    response = await conn.prompt(session_id, "Hello, Copilot!")
    await conn.stop()
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
import signal
from dataclasses import dataclass, field
from typing import Any, Callable, Coroutine

log = logging.getLogger("acp")

# ACP protocol version (major only, per spec)
PROTOCOL_VERSION = 1

# Default timeouts
INITIALIZE_TIMEOUT = 30  # seconds
PROMPT_TIMEOUT = 300  # seconds


# ---------------------------------------------------------------------------
# Data types
# ---------------------------------------------------------------------------


@dataclass
class AgentInfo:
    """Information about the connected ACP agent."""
    name: str = ""
    title: str = ""
    version: str = ""
    protocol_version: int = 0
    capabilities: dict = field(default_factory=dict)


@dataclass
class PromptResult:
    """Result of a prompt turn."""
    stop_reason: str = "end_turn"
    text: str = ""
    tool_calls: list[dict] = field(default_factory=list)


# Permission callback type: receives permission request params, returns outcome dict
PermissionCallback = Callable[[dict], Coroutine[Any, Any, dict]]


# Session update callback type: receives session_id and update dict
UpdateCallback = Callable[[str, dict], Coroutine[Any, Any, None]]


# Default permission handler: deny all tool calls (safe default for conductor)
async def _deny_permission(_params: dict) -> dict:
    return {"outcome": {"outcome": "cancelled"}}


# Default update handler: no-op
async def _noop_update(_session_id: str, _update: dict) -> None:
    pass


# ---------------------------------------------------------------------------
# NDJSON codec
# ---------------------------------------------------------------------------


def encode_message(msg: dict) -> bytes:
    """Encode a JSON-RPC message as NDJSON (one line, newline terminated)."""
    return json.dumps(msg, separators=(",", ":")).encode("utf-8") + b"\n"


def decode_message(line: bytes) -> dict | None:
    """Decode a single NDJSON line into a dict. Returns None for empty/invalid lines."""
    stripped = line.strip()
    if not stripped:
        return None
    try:
        return json.loads(stripped)
    except json.JSONDecodeError as e:
        log.warning("Failed to decode NDJSON line: %s (error: %s)", stripped[:200], e)
        return None


# ---------------------------------------------------------------------------
# JSON-RPC helpers
# ---------------------------------------------------------------------------


def _make_request(method: str, params: dict, request_id: int) -> dict:
    """Build a JSON-RPC 2.0 request."""
    return {
        "jsonrpc": "2.0",
        "id": request_id,
        "method": method,
        "params": params,
    }


def _make_response(request_id: int, result: Any) -> dict:
    """Build a JSON-RPC 2.0 response (for client-side methods like request_permission)."""
    return {
        "jsonrpc": "2.0",
        "id": request_id,
        "result": result,
    }


def _is_request(msg: dict) -> bool:
    """Check if a message is a JSON-RPC request (has method and id)."""
    return "method" in msg and "id" in msg


def _is_notification(msg: dict) -> bool:
    """Check if a message is a JSON-RPC notification (has method, no id)."""
    return "method" in msg and "id" not in msg


def _is_response(msg: dict) -> bool:
    """Check if a message is a JSON-RPC response (has id, no method)."""
    return "id" in msg and "method" not in msg


# ---------------------------------------------------------------------------
# ACP Connection
# ---------------------------------------------------------------------------


class ACPConnection:
    """Manages a single ACP agent subprocess and its JSON-RPC communication.

    The connection lifecycle:
        1. start() — spawn subprocess, initialize protocol
        2. new_session() — create a conversation session
        3. prompt() — send prompts and collect responses
        4. stop() — terminate the subprocess

    Thread safety: This class is NOT thread-safe. All methods must be called
    from the same asyncio event loop.
    """

    def __init__(
        self,
        command: list[str] | None = None,
        cwd: str | None = None,
        env: dict[str, str] | None = None,
        on_permission: PermissionCallback | None = None,
        on_update: UpdateCallback | None = None,
    ):
        self._command = command or ["copilot", "--acp", "--stdio"]
        self._cwd = cwd or os.getcwd()
        self._env = env
        self._on_permission = on_permission or _deny_permission
        self._on_update = on_update or _noop_update

        self._process: asyncio.subprocess.Process | None = None
        self._request_id = 0
        self._pending: dict[int, asyncio.Future] = {}
        self._reader_task: asyncio.Task | None = None
        self._agent_info = AgentInfo()
        self._initialized = False
        self._stopped = False

    @property
    def agent_info(self) -> AgentInfo:
        """Information about the connected agent (available after start())."""
        return self._agent_info

    @property
    def is_running(self) -> bool:
        """Whether the subprocess is alive and the connection is initialized."""
        return (
            self._initialized
            and not self._stopped
            and self._process is not None
            and self._process.returncode is None
        )

    def _next_id(self) -> int:
        self._request_id += 1
        return self._request_id

    async def start(self, timeout: float = INITIALIZE_TIMEOUT) -> AgentInfo:
        """Spawn the ACP subprocess and initialize the protocol.

        Returns the agent's info (name, version, capabilities).
        Raises RuntimeError if initialization fails or times out.
        """
        if self._initialized:
            raise RuntimeError("Connection already initialized")

        proc_env = os.environ.copy()
        if self._env:
            proc_env.update(self._env)

        self._process = await asyncio.create_subprocess_exec(
            *self._command,
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            cwd=self._cwd,
            env=proc_env,
            start_new_session=True,
        )

        # Start background reader for incoming messages
        self._reader_task = asyncio.create_task(self._read_loop())

        # Send initialize request
        try:
            result = await asyncio.wait_for(
                self._send_request("initialize", {
                    "protocolVersion": PROTOCOL_VERSION,
                    "clientCapabilities": {},
                    "clientInfo": {
                        "name": "agent-deck-conductor",
                        "title": "Agent Deck Conductor",
                        "version": "1.0.0",
                    },
                }),
                timeout=timeout,
            )
        except asyncio.TimeoutError:
            await self.stop()
            raise RuntimeError(
                f"ACP initialization timed out after {timeout}s. "
                f"Is '{' '.join(self._command)}' installed and authenticated?"
            )

        self._agent_info = AgentInfo(
            name=result.get("agentInfo", {}).get("name", ""),
            title=result.get("agentInfo", {}).get("title", ""),
            version=result.get("agentInfo", {}).get("version", ""),
            protocol_version=result.get("protocolVersion", 0),
            capabilities=result.get("agentCapabilities", {}),
        )
        self._initialized = True
        log.info(
            "ACP initialized: %s v%s (protocol=%d)",
            self._agent_info.title or self._agent_info.name,
            self._agent_info.version,
            self._agent_info.protocol_version,
        )
        return self._agent_info

    async def new_session(
        self,
        cwd: str | None = None,
        mcp_servers: list[dict] | None = None,
        timeout: float = INITIALIZE_TIMEOUT,
    ) -> str:
        """Create a new ACP session. Returns the session ID.

        Args:
            cwd: Working directory for the session (defaults to connection cwd).
            mcp_servers: Optional list of MCP server configurations.
            timeout: Maximum time to wait for session creation.
        """
        self._require_running()
        try:
            result = await asyncio.wait_for(
                self._send_request("session/new", {
                    "cwd": cwd or self._cwd,
                    "mcpServers": mcp_servers or [],
                }),
                timeout=timeout,
            )
        except asyncio.TimeoutError:
            raise RuntimeError(f"session/new timed out after {timeout}s")

        session_id = result.get("sessionId", "")
        if not session_id:
            raise RuntimeError(f"session/new returned no sessionId: {result}")
        log.info("ACP session created: %s", session_id)
        return session_id

    async def prompt(
        self,
        session_id: str,
        text: str,
        timeout: float = PROMPT_TIMEOUT,
    ) -> PromptResult:
        """Send a text prompt and wait for the full response.

        Streams session/update notifications through the on_update callback.
        Collects all agent_message_chunk text into the returned PromptResult.

        Args:
            session_id: The session to send the prompt to.
            text: The prompt text.
            timeout: Maximum time to wait for the response.
        """
        self._require_running()

        # Track text chunks for this prompt
        collected_text: list[str] = []
        collected_tool_calls: list[dict] = []
        original_on_update = self._on_update

        async def _collecting_update(sid: str, update: dict) -> None:
            update_type = update.get("sessionUpdate", "")
            if update_type == "agent_message_chunk":
                content = update.get("content", {})
                if content.get("type") == "text":
                    collected_text.append(content.get("text", ""))
            elif update_type in ("tool_call", "tool_call_update"):
                collected_tool_calls.append(update)
            await original_on_update(sid, update)

        self._on_update = _collecting_update
        try:
            result = await asyncio.wait_for(
                self._send_request("session/prompt", {
                    "sessionId": session_id,
                    "prompt": [{"type": "text", "text": text}],
                }),
                timeout=timeout,
            )
        except asyncio.TimeoutError:
            raise RuntimeError(f"session/prompt timed out after {timeout}s")
        finally:
            self._on_update = original_on_update

        return PromptResult(
            stop_reason=result.get("stopReason", "end_turn"),
            text="".join(collected_text),
            tool_calls=collected_tool_calls,
        )

    async def cancel(self, session_id: str) -> None:
        """Cancel an ongoing prompt turn."""
        self._require_running()
        await self._send_notification("session/cancel", {
            "sessionId": session_id,
        })

    async def stop(self) -> None:
        """Terminate the ACP subprocess and clean up."""
        if self._stopped:
            return
        self._stopped = True

        # Cancel pending requests
        for fut in self._pending.values():
            if not fut.done():
                fut.cancel()
        self._pending.clear()

        # Cancel reader task
        if self._reader_task and not self._reader_task.done():
            self._reader_task.cancel()
            try:
                await self._reader_task
            except asyncio.CancelledError:
                pass

        # Terminate process
        if self._process and self._process.returncode is None:
            try:
                if self._process.stdin:
                    self._process.stdin.close()
                # Send SIGTERM to the process group
                pgid = os.getpgid(self._process.pid)
                os.killpg(pgid, signal.SIGTERM)
                try:
                    await asyncio.wait_for(self._process.wait(), timeout=5)
                except asyncio.TimeoutError:
                    os.killpg(pgid, signal.SIGKILL)
                    await self._process.wait()
            except (ProcessLookupError, OSError):
                pass

        self._initialized = False
        log.info("ACP connection stopped")

    # ------------------------------------------------------------------
    # Internal: message sending
    # ------------------------------------------------------------------

    async def _send_request(self, method: str, params: dict) -> dict:
        """Send a JSON-RPC request and wait for the response."""
        request_id = self._next_id()
        msg = _make_request(method, params, request_id)
        future: asyncio.Future[dict] = asyncio.get_running_loop().create_future()
        self._pending[request_id] = future
        self._write(msg)
        try:
            return await future
        finally:
            self._pending.pop(request_id, None)

    async def _send_notification(self, method: str, params: dict) -> None:
        """Send a JSON-RPC notification (no response expected)."""
        msg = {
            "jsonrpc": "2.0",
            "method": method,
            "params": params,
        }
        self._write(msg)

    def _write(self, msg: dict) -> None:
        """Write an NDJSON message to the subprocess stdin."""
        if not self._process or not self._process.stdin:
            raise RuntimeError("ACP process not running")
        data = encode_message(msg)
        self._process.stdin.write(data)

    # ------------------------------------------------------------------
    # Internal: message reading
    # ------------------------------------------------------------------

    async def _read_loop(self) -> None:
        """Background task: read NDJSON lines from subprocess stdout and dispatch."""
        assert self._process and self._process.stdout
        try:
            while True:
                line = await self._process.stdout.readline()
                if not line:
                    break  # EOF — process exited
                msg = decode_message(line)
                if msg is None:
                    continue
                await self._dispatch(msg)
        except asyncio.CancelledError:
            return
        except Exception as e:
            log.error("ACP read loop error: %s", e)
        finally:
            # Process exited — fail all pending requests
            for rid, fut in list(self._pending.items()):
                if not fut.done():
                    fut.set_exception(RuntimeError("ACP process exited unexpectedly"))
            self._pending.clear()

    async def _dispatch(self, msg: dict) -> None:
        """Route an incoming message to the appropriate handler."""
        if _is_response(msg):
            # Response to one of our requests
            rid = msg["id"]
            fut = self._pending.get(rid)
            if fut and not fut.done():
                if "error" in msg:
                    err = msg["error"]
                    fut.set_exception(RuntimeError(
                        f"ACP error {err.get('code', '?')}: {err.get('message', 'unknown')}"
                    ))
                else:
                    fut.set_result(msg.get("result", {}))
            return

        if _is_notification(msg):
            # Notification from the agent (e.g., session/update)
            method = msg["method"]
            params = msg.get("params", {})
            if method == "session/update":
                session_id = params.get("sessionId", "")
                update = params.get("update", {})
                try:
                    await self._on_update(session_id, update)
                except Exception as e:
                    log.error("Update callback error: %s", e)
            return

        if _is_request(msg):
            # Request from the agent to us (e.g., session/request_permission)
            method = msg["method"]
            params = msg.get("params", {})
            rid = msg["id"]

            if method == "session/request_permission":
                try:
                    result = await self._on_permission(params)
                except Exception as e:
                    log.error("Permission callback error: %s", e)
                    result = {"outcome": {"outcome": "cancelled"}}
                response = _make_response(rid, result)
                self._write(response)
            else:
                # Unknown client method — return error
                self._write({
                    "jsonrpc": "2.0",
                    "id": rid,
                    "error": {
                        "code": -32601,
                        "message": f"Method not found: {method}",
                    },
                })
            return

    # ------------------------------------------------------------------
    # Internal: validation
    # ------------------------------------------------------------------

    def _require_running(self) -> None:
        if not self.is_running:
            raise RuntimeError("ACP connection is not running")
