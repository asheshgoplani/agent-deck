# Connect an assistant to agent-deck

Let an outside assistant (Hermes, OpenClaw, Claude, Codex, Grok, a Slack or Telegram bot, any MCP or HTTP client) see your
agent-deck sessions and, only if you allow it, drive them. Every way in ends in the same thing: one scoped token and the same
seven tools. **Read-only by default.**

## Three ways in

1. **Paste one line into your assistant** (recommended). It checks agent-deck, connects itself read-only, writes its own
   config, tests it and keeps the rules. It asks you before each step that changes anything.

   > Read https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/docs/assistants/SETUP-PROMPT.md and follow it to connect yourself to agent-deck on my machine. Ask me before each step that changes anything.

   The prompt: [SETUP-PROMPT.md](SETUP-PROMPT.md).
2. **The form**: `agent-deck web`, open `/connect`. Tick what it may do (read is pre-ticked), Copy, Test.
3. **The command**: `agent-deck connect` prints one copy-paste block for any assistant. `agent-deck connect hermes` (or
   another name from the table) tailors the block to that assistant. Add `--json` for machine-readable output.

Each block has: the config with your real token file path and URL, where it goes, a test command, and a short guide with
the safety rules for the assistant to keep.

## Scopes

| Scope | Allows | Default |
|---|---|---|
| `read` | `whoami`, `list_sessions`, `session_status`, `read_output` | yes |
| `send` | `send_message` | no |
| `approve` | `approve` (answer an approval prompt) | no |
| `launch` | `launch_session` (start a new session with a known agent, never a free-form command) | no |

Allow more: `agent-deck connect <kind> --scopes read,send` (this rotates the token; stdio configs read the new one from the
token file, HTTP configs need the new token). A call without the scope is refused with that exact command in the answer.

There is **no** stop, delete, restart or kill tool.

## Tools

| Tool | Scope | What it does |
|---|---|---|
| `whoami` | any | connection name, scopes, limits |
| `list_sessions {group?, status?}` | read | id, title, tool, status, group, path of each session |
| `session_status {session}` | read | details of one session |
| `read_output {session}` | read | the last answer of that session's agent |
| `send_message {session, message}` | send | type a message into a session |
| `approve {session, choice?}` | approve | answer the visible approval prompt |
| `launch_session {path, title, tool?, message?}` | launch | start a new session |

Read tools carry the MCP hint `readOnlyHint: true`, so clients such as Hermes and OpenClaw run them without asking and ask
you before the others.

Transports: MCP over stdio (`agent-deck connect serve --token-file F`), MCP over HTTP (`POST /mcp` on `agent-deck web`), and a
plain JSON API (`/api/assistant/v1/...`, routes in [SETUP-PROMPT.md](SETUP-PROMPT.md#slack--telegram-bots-and-other-http-clients)).
HTTP routes take only assistant tokens, as `Authorization: Bearer adk_...`; assistant tokens work nowhere else.

## Revoke, list, audit

```bash
agent-deck connect list              # names, kinds, scopes
agent-deck connect revoke hermes     # cut it off; effective on its next call
agent-deck connect audit -n 50       # who called what, allowed or not (never message text)
```

Limits per token: 120 read and 10 write calls per minute. Tokens are stored only as a hash in `assistants.json`; the token
itself is in `assistants/<name>.token` (0600) in the agent-deck data directory.

## Per-assistant table

| Assistant | `connect` kind | How it connects | Config | Guide / skill |
|---|---|---|---|---|
| Hermes Agent | `hermes` | stdio MCP | `hermes mcp add` or `~/.hermes/config.yaml` | [skills/hermes/SKILL.md](skills/hermes/SKILL.md) to `~/.hermes/skills/agent-deck/` |
| OpenClaw | `openclaw` | stdio MCP | `openclaw mcp set` or `~/.openclaw/openclaw.json` | [skills/openclaw/SKILL.md](skills/openclaw/SKILL.md) to `~/.openclaw/workspace/skills/agent-deck/` |
| Claude Code | `claude` | stdio MCP | `claude mcp add -s user` | [skills/claude/SKILL.md](skills/claude/SKILL.md) to `~/.claude/skills/agent-deck/` |
| Claude Desktop | `claude` | stdio MCP | `claude_desktop_config.json` | project instructions |
| Codex CLI | `codex` | stdio MCP | `codex mcp add` or `~/.codex/config.toml` | [skills/codex/SKILL.md](skills/codex/SKILL.md) to `~/.agents/skills/agent-deck/` |
| ChatGPT | `chatgpt` | via Codex CLI, or Responses API remote MCP | see block | Codex skill / request `instructions` |
| Grok (xAI API) | `grok` | remote MCP, public HTTPS | `tools` array of `/v1/responses` | system message |
| Slack / Telegram bot | `slack` / `telegram` | plain HTTP | bot environment | bot system prompt |
| Any MCP client | `mcp` | stdio or HTTP MCP | `mcpServers` JSON | client instructions |
| Any HTTP client | `http` | plain HTTP | environment | system prompt |

Not yet: the ChatGPT app itself (developer mode) only connects to MCP servers with OAuth or no login, so it cannot send the
agent-deck token. Use Codex CLI with the same ChatGPT account.

Cloud assistants (Grok, the OpenAI Responses API) call the server from the vendor's cloud and need a public HTTPS address.
Only if you want that: run `agent-deck web --no-tui --token-file OWNER_TOKEN_FILE --allowed-host HOST` (the owner token keeps
the owner pages locked), expose it with for example `tailscale funnel 8420`, and run `agent-deck connect grok --url https://HOST`.

Want a ready chat bot instead of your own? The conductor has Telegram, Slack, Discord and Mattermost bridges:
`agent-deck conductor setup NAME` ([docs/conductor](../conductor/README.md)).

## Generic MCP fallback

Any MCP client that can start a command:

```json
{ "mcpServers": { "agentdeck": { "command": "/absolute/path/to/agent-deck",
  "args": ["connect", "serve", "--token-file", "/path/from/agent-deck/connect"] } } }
```

Any client that speaks Streamable HTTP: URL `http://127.0.0.1:8420/mcp`, header `Authorization: Bearer adk_...`, with
`agent-deck web` running.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `agent-deck: command not found` from the client | use the absolute path the block prints |
| `401` | token rotated or revoked: run `agent-deck connect <kind>` again |
| `403` "scope missing" | run the command in the answer, only if you want that |
| `429` | rate limit, wait a minute |
| connection refused over HTTP | start `agent-deck web --no-tui` (default `127.0.0.1:8420`) |
| it worked, then stopped | someone revoked or rotated it; `agent-deck connect list` |
| check end to end | `agent-deck connect test --name <name>` (add `--url` for HTTP) |

## Credits

The bridge is new code in agent-deck (MIT). It follows the hand-written stdio MCP server already in this repo
(`internal/recall/mcp`) and reuses agent-deck's own CLI for every tool. No outside project was forked or copied: the tmux MCP
servers and the Go MCP SDKs were reviewed and not used, so there is no third-party licence to carry.
