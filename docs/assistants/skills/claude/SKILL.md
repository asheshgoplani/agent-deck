---
name: agent-deck
description: See and, when allowed, drive the user's agent-deck sessions (AI coding agents in terminals) through the agentdeck MCP tools. Use when the user asks about their agent-deck sessions or coding agents, what is running or waiting, or wants to message, approve or launch a session.
---

# agent-deck

You reach agent-deck through the MCP server "agentdeck" (your client may prefix the tool names). Call whoami to see your connection name and scopes.
agent-deck is a terminal session manager for AI coding agents (Claude Code, Codex, Gemini, OpenCode and others): each session is one agent working in its own terminal. Through the agent-deck tools you can see those sessions and, when allowed, drive them.

Tools:
- whoami: your name, scopes and limits
- list_sessions {group?, status?}: all sessions with id, title, tool, status, group, path (read)
- session_status {session}: one session's details (read)
- read_output {session}: the last answer of that session's agent (read)
- send_message {session, message}: type a message into a session (send)
- approve {session, choice?}: answer a visible approval prompt (approve)
- launch_session {path, title, tool?, message?}: start a new session (launch)

Rules:
1. Call list_sessions first and refer to sessions by id.
2. Read before acting: check session_status or read_output before you send or approve anything.
3. ASK THE USER and wait for a clear yes before send_message, approve or launch_session; show the exact text you will send.
4. Never stop, delete, restart or kill sessions. There is no tool for it; do not use a shell or any other way to do it.
5. Never read, print or change credentials, tokens or agent-deck's own files (~/.agent-deck, ~/.local/share/agent-deck).
6. If a tool answers "scope missing", do not work around it: tell the user the exact command from the answer (for example: agent-deck connect claude --scopes read,send).
7. Keep reports short: one line per session (title, status, what it is waiting for).

Setup, scopes and troubleshooting: https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/docs/assistants/README.md
