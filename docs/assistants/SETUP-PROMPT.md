# Connect yourself to agent-deck

**For the human.** Paste this one line into your assistant (Hermes, OpenClaw, Claude, Codex, Grok, a bot, anything):

> Read https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/docs/assistants/SETUP-PROMPT.md and follow it to connect yourself to agent-deck on my machine. Ask me before each step that changes anything.

The assistant does the rest. It gets **read-only** access unless you later allow more.

---

**For the assistant.** You are going to connect yourself to agent-deck, a terminal session manager for AI coding agents
(Claude Code, Codex, Gemini, OpenCode and others). Each session is one agent working in its own terminal. When you are done,
you can list those sessions and read what they said; sending, approving and launching come later, and only if the user allows them.

Setup rules (keep them for the whole setup):

- Ask the user before every step that installs, writes a file, starts a server or exposes anything. Say in one line what you will do.
- Never show the token in the chat. Keep it in the token file, an environment variable or your config, nothing else.
- Never open a tunnel or listen on a public address unless the user says yes to exactly that.
- Start read-only. Never run `agent-deck connect ... --scopes` yourself: only the user raises scopes.

If you cannot run commands on the user's machine (you are a cloud API such as Grok or the OpenAI Responses API), skip to
[Cloud assistants](#cloud-assistants-grok-openai-responses-api) and give the user the steps to run instead.

## 1. Check agent-deck

Run `agent-deck version`.

If it is missing, ask the user, then run the official installer:

```bash
curl -fsSL https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/install.sh | bash
```

(macOS alternative: `brew install asheshgoplani/tap/agent-deck`.)

## 2. Get your connection

Pick your kind from the table (use `mcp` if you are another MCP client, `http` if you can only make HTTP calls), then run:

```bash
agent-deck connect <kind> --json
```

| You are | kind | Best way in |
|---|---|---|
| Hermes Agent | `hermes` | stdio MCP |
| OpenClaw | `openclaw` | stdio MCP |
| Claude Code / Claude Desktop | `claude` | stdio MCP |
| Codex CLI | `codex` | stdio MCP |
| ChatGPT | `chatgpt` | through Codex CLI or the Responses API (the ChatGPT app cannot send a fixed token yet) |
| Grok (xAI API) | `grok` | HTTP MCP, needs a public HTTPS address |
| A Slack or Telegram bot | `slack` / `telegram` | plain HTTP API |
| Any other MCP client | `mcp` | stdio MCP, else HTTP MCP |
| Anything else | `http` | plain HTTP API |

This creates a **read-only** token. Running it again rotates the token for that name. The JSON has everything you need:

| Field | What it is |
|---|---|
| `token_file` | 0600 file with the token. Prefer configs that point here. |
| `token` | the token itself, only for configs that cannot read a file. Never print it. |
| `mcp_stdio` | `{command, args}` to start the MCP server over stdio |
| `mcp_http` | `{url, headers}` for MCP over HTTP |
| `http_api` | `{base, headers}` for the plain JSON API |
| `config_snippet` | ready config in your own format, with real paths filled in |
| `config_path_hint` | where that config goes |
| `test_command` | a command that checks the connection |
| `context_prompt` | the usage guide and rules to keep (step 6) |
| `skill_path`, `skill_content`, `skill_url` | where your skill file goes, the ready file itself, and its web copy (only for assistants with skills) |
| `raise_scope_hint` | how the user allows more |

## 3. Choose how to connect

Take the first one that fits:

1. **stdio MCP**: you run on the same machine as agent-deck and you support MCP servers. Best choice: you start
   `agent-deck connect serve --token-file TOKEN_FILE` yourself, no server or port is needed, and the token never leaves its file.
2. **HTTP MCP**: you support remote MCP servers with a header, but you run somewhere else. Needs `agent-deck web` running
   (`agent-deck web --no-tui`, default `http://127.0.0.1:8420`). Endpoint: `mcp_http.url`, header `Authorization: Bearer <token>`.
   If you run in a vendor's cloud, localhost does not reach the user's machine: see [Cloud assistants](#cloud-assistants-grok-openai-responses-api).
3. **Plain HTTP**: no MCP at all. Call `http_api.base` with the same header (routes below).

## 4. Write your config

Required: setup is not done until this config is saved. Use `config_snippet` from the JSON (it is already in your format) and put it where `config_path_hint` says. The per-assistant
reference below shows the same thing with placeholders: `AGENT_DECK` = `mcp_stdio.command` (absolute path), `TOKEN_FILE` =
`token_file`, `URL` = the agent-deck web address.

## 5. Test it

Test through your own config: call your `agentdeck` tools `whoami`, then `list_sessions` (a new chat or a tool reload may be
needed first), or your own check command (`hermes mcp test agentdeck`, `openclaw mcp probe`, `claude mcp list`). The
`test_command` from the JSON (for example `agent-deck connect test --name hermes`) only proves the token works, not your config.
Tell the user: "Connected to agent-deck read-only, N sessions." If it fails, see [Troubleshooting](#troubleshooting).

## 6. Keep the guide

Write `skill_content` to `skill_path` as it is (it is the ready skill file), or save `context_prompt` where you keep
long-term instructions, so every future chat has it:

| Assistant | Where |
|---|---|
| Hermes | `~/.hermes/skills/agent-deck/SKILL.md` |
| OpenClaw | `~/.openclaw/workspace/skills/agent-deck/SKILL.md` (or the workspace `AGENTS.md`) |
| Claude Code | `~/.claude/skills/agent-deck/SKILL.md` (or `CLAUDE.md`) |
| Codex CLI | `~/.agents/skills/agent-deck/SKILL.md` (or `AGENTS.md`) |
| Claude Desktop | the project instructions |
| Grok / Responses API | the system message (`instructions`) of your requests |
| Others | your memory, system prompt or standing instructions |

The rules in it are not optional: list first, read before acting, ask the user before `send_message`, `approve` or
`launch_session`, never stop, delete, restart or kill sessions, never touch credentials, tokens or agent-deck's own files, and
when a tool says a scope is missing, tell the user the exact command instead of working around it.

## 7. Tell the user what changed

In a few short lines:

- what you can do now (read: list sessions, check status, read output);
- how to allow more: `agent-deck connect <kind> --scopes read,send` (add `approve`, `launch` only if wanted). This rotates the token;
  a stdio config picks the new one up from the token file, an HTTP config needs the new token;
- how to cut you off: `agent-deck connect revoke <name>`; what you did: `agent-deck connect audit`.

---

## Per-assistant reference

### Hermes Agent

```bash
hermes mcp add agentdeck --command AGENT_DECK --args connect serve --token-file TOKEN_FILE && hermes config set mcp_servers.agentdeck.trust untrusted   # --args must be the last option of mcp add
hermes mcp test agentdeck
```

`hermes mcp add` asks one question ("Enable all 7 tools? [Y/n/select]"); without a terminal, pipe the answer: `printf 'y\n' | hermes mcp add ...`.
It has no trust option, and Hermes runs every tool of a server without a `trust` key without asking, so always run the
`hermes config set ... trust untrusted` part too: then Hermes asks before send_message, approve and launch_session.

or in `~/.hermes/config.yaml`:

```yaml
mcp_servers:
  agentdeck:
    command: "AGENT_DECK"
    args: ["connect", "serve", "--token-file", "TOKEN_FILE"]
    trust: untrusted        # asks before any tool that is not read-only
```

Remote instead: `url: "URL/mcp"` and `headers: {Authorization: "Bearer ${AGENTDECK_ASSISTANT_TOKEN}"}`, with the variable in
`~/.hermes/.env`. Skill: `~/.hermes/skills/agent-deck/SKILL.md`
([ready file](https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/docs/assistants/skills/hermes/SKILL.md)).

### OpenClaw

```bash
openclaw mcp set agentdeck '{"command":"AGENT_DECK","args":["connect","serve","--token-file","TOKEN_FILE"]}'
openclaw mcp probe
```

Saved under `mcp.servers` in `~/.openclaw/openclaw.json`. Remote instead:
`openclaw mcp add agentdeck-http --url URL/mcp --header "Authorization=Bearer ${AGENTDECK_ASSISTANT_TOKEN}"`.
Skill: `~/.openclaw/workspace/skills/agent-deck/SKILL.md`
([ready file](https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/docs/assistants/skills/openclaw/SKILL.md)).

### Claude Code

```bash
claude mcp add -s user agentdeck -- AGENT_DECK connect serve --token-file TOKEN_FILE
claude mcp list
```

Skill: `~/.claude/skills/agent-deck/SKILL.md`
([ready file](https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/docs/assistants/skills/claude/SKILL.md)).

### Claude Desktop

Merge into `claude_desktop_config.json` (macOS `~/Library/Application Support/Claude/`, Windows `%APPDATA%\Claude\`,
Linux `~/.config/Claude/`), then restart the app. Use the absolute path; Desktop does not use the shell PATH. Stdio only:
its Connectors screen has no field for a fixed token.

```json
{ "mcpServers": { "agentdeck": { "command": "AGENT_DECK", "args": ["connect", "serve", "--token-file", "TOKEN_FILE"] } } }
```

### Codex CLI

```bash
codex mcp add agentdeck -- AGENT_DECK connect serve --token-file TOKEN_FILE
codex mcp list
```

or in `~/.codex/config.toml`:

```toml
[mcp_servers.agentdeck]
command = "AGENT_DECK"
args = ["connect", "serve", "--token-file", "TOKEN_FILE"]
```

Remote instead: `url = "URL/mcp"` and `bearer_token_env_var = "AGENTDECK_ASSISTANT_TOKEN"`.
Skill: `~/.agents/skills/agent-deck/SKILL.md`
([ready file](https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/docs/assistants/skills/codex/SKILL.md)).

### ChatGPT

The ChatGPT app's developer mode only supports OAuth or no login for MCP servers, so it cannot use the agent-deck token yet.
Use **Codex CLI** (it signs in with the same ChatGPT account, see above) or the Responses API (next section).

### Cloud assistants (Grok, OpenAI Responses API)

The vendor calls the MCP URL from its cloud, so `127.0.0.1` does not work. Only with the user's consent:

1. Start the web server with an owner token so the owner pages stay locked:
   `agent-deck web --no-tui --token-file OWNER_TOKEN_FILE --allowed-host HOST`
2. Expose it over HTTPS, for example `tailscale funnel 8420` (HOST is then `<machine>.<tailnet>.ts.net`).
3. Re-run `agent-deck connect grok --url https://HOST --json` (or `chatgpt`) and put `config_snippet` in the `tools` array:

```json
{ "type": "mcp", "server_url": "https://HOST/mcp", "server_label": "agentdeck",
  "allowed_tools": ["whoami", "list_sessions", "session_status", "read_output"], "authorization": "TOKEN" }
```

xAI (`POST https://api.x.ai/v1/responses`) has no ask-first switch, so the rules and the read-only scope are the only guard.
The Responses API asks for approval by default; the snippet lets the read tools through without asking. Send the token with every
request (it is not stored). No public address possible? Use plain HTTP from your own code instead.

### Slack / Telegram bots and other HTTP clients

Scopes limit what the bot can do through these routes; they are not a sandbox. A web server started without an owner token
answers its owner API to any local caller, with no token at all, so run the server the bot talks to with an owner token:
`agent-deck web --no-tui --token-file OWNER_TOKEN_FILE`. Tell the user this before you connect an HTTP bot.

Put `AGENTDECK_URL` (for example `http://127.0.0.1:8420`) and `AGENTDECK_ASSISTANT_TOKEN` in the bot's environment and call:

| Call | Scope |
|---|---|
| `GET  /api/assistant/v1/whoami` | any |
| `GET  /api/assistant/v1/sessions[?group=G&status=S]` | read |
| `GET  /api/assistant/v1/sessions/ID` | read |
| `GET  /api/assistant/v1/sessions/ID/output` | read |
| `POST /api/assistant/v1/sessions/ID/send` `{"message": "..."}` | send |
| `POST /api/assistant/v1/sessions/ID/approve` `{"choice": "once"}` | approve |
| `POST /api/assistant/v1/sessions` `{"path", "title", "tool", "message"}` | launch |

```bash
curl -fsS -H "Authorization: Bearer $AGENTDECK_ASSISTANT_TOKEN" "$AGENTDECK_URL/api/assistant/v1/sessions"
```

No bot yet? agent-deck's conductor has ready Telegram, Slack, Discord and Mattermost bridges: `agent-deck conductor setup NAME`.

### Any other MCP client

```json
{ "mcpServers": { "agentdeck": { "command": "AGENT_DECK", "args": ["connect", "serve", "--token-file", "TOKEN_FILE"] } } }
```

Streamable HTTP instead: `{"type": "http", "url": "URL/mcp", "headers": {"Authorization": "Bearer TOKEN"}}`.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `agent-deck: command not found` from your MCP client | use the absolute path from `mcp_stdio.command` |
| `401` / "invalid token" | the token was rotated or revoked: run `agent-deck connect <kind> --json` again |
| `403` / "scope missing" | tell the user the command in the answer (`agent-deck connect <kind> --scopes ...`) |
| `429` | rate limit (120 reads, 10 writes per minute): wait a minute |
| HTTP connection refused | `agent-deck web --no-tui` is not running, or listens on another address |
| cloud assistant cannot reach the URL | it needs a public HTTPS address, see Cloud assistants |
| a tool asks for approval every time | expected for send, approve and launch; read tools are marked read-only |

More: [README](README.md).
