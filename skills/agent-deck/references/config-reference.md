# Configuration Reference

All options for `~/.agent-deck/config.toml`.

## Table of Contents

- [Top-Level](#top-level)
- [[claude] Section](#claude-section)
- [[codex] Section](#codex-section)
- [[logs] Section](#logs-section)
- [[updates] Section](#updates-section)
- [[global_search] Section](#global_search-section)
- [[mcp_pool] Section](#mcp_pool-section)
- [[mcps.*] Section](#mcps-section)
- [[tools.*] Section](#tools-section)

## Top-Level

```toml
default_tool = "claude"   # Pre-selected tool when creating sessions
```

## [claude] Section

Claude Code integration settings.

```toml
[claude]
config_dir = "~/.claude-work"   # Path to Claude config directory
dangerous_mode = true           # Enable --dangerously-skip-permissions
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `config_dir` | string | `~/.claude` | Claude config directory. Override with `CLAUDE_CONFIG_DIR` env. |
| `dangerous_mode` | bool | `false` | Skip Claude permission dialogs. Required for automation. |

## [codex] Section

Codex CLI integration settings.

```toml
[codex]
yolo_mode = true   # Enable --yolo (bypass approvals and sandbox)
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `yolo_mode` | bool | `false` | Maps to `codex --yolo` (`--dangerously-bypass-approvals-and-sandbox`). Can be overridden per-session. |

## [logs] Section

Session log file management.

```toml
[logs]
max_size_mb = 10        # Max size before truncation
max_lines = 10000       # Lines to keep when truncating
remove_orphans = true   # Delete logs for removed sessions
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `max_size_mb` | int | `10` | Max log file size in MB. |
| `max_lines` | int | `10000` | Lines to keep after truncation. |
| `remove_orphans` | bool | `true` | Clean up logs for deleted sessions. |

**Logs location:** `~/.agent-deck/logs/agentdeck_<session>_<id>.log`

## [updates] Section

Auto-update settings.

```toml
[updates]
auto_update = false           # Auto-install updates
check_enabled = true          # Check on startup
check_interval_hours = 24     # Check frequency
notify_in_cli = true          # Show in CLI commands
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `auto_update` | bool | `false` | Install updates without prompting. |
| `check_enabled` | bool | `true` | Enable startup update checks. |
| `check_interval_hours` | int | `24` | Hours between checks. |
| `notify_in_cli` | bool | `true` | Show updates in CLI (not just TUI). |

## [global_search] Section

Search across all Claude conversations.

```toml
[global_search]
enabled = true              # Enable global search
tier = "auto"               # "auto", "instant", "balanced"
memory_limit_mb = 100       # Max RAM for index
recent_days = 90            # Limit to last N days (0 = all)
index_rate_limit = 20       # Files/second for indexing
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | bool | `true` | Enable `G` key global search. |
| `tier` | string | `"auto"` | Strategy: `instant` (fast, more RAM), `balanced` (LRU cache). |
| `memory_limit_mb` | int | `100` | Max memory for balanced tier. |
| `recent_days` | int | `90` | Only search recent conversations. |
| `index_rate_limit` | int | `20` | Indexing speed (reduce for less CPU). |

## [mcp_pool] Section

Share MCP processes across sessions via Unix sockets.

```toml
[mcp_pool]
enabled = false             # Enable socket pooling
auto_start = true           # Start pool on launch
pool_all = false            # Pool ALL MCPs
exclude_mcps = []           # Exclude from pool_all
fallback_to_stdio = true    # Fallback if socket fails
show_pool_status = true     # Show 🔌 indicator
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | bool | `false` | Master switch for pooling. |
| `pool_all` | bool | `false` | Pool all available MCPs. |
| `exclude_mcps` | array | `[]` | MCPs to exclude when `pool_all=true`. |
| `fallback_to_stdio` | bool | `true` | Use stdio if socket unavailable. |

**Benefits:** 30 sessions x 5 MCPs = 150 processes -> 5 shared processes (90% memory savings).

**Socket location:** `/tmp/agentdeck-mcp-{name}.sock`

## [mcps.*] Section

Define MCP servers. One section per MCP.

### STDIO MCPs (Local)

```toml
[mcps.exa]
command = "npx"
args = ["-y", "exa-mcp-server"]
env = { EXA_API_KEY = "your-key" }
description = "Web search via Exa AI"
```

| Key | Type | Required | Description |
|-----|------|----------|-------------|
| `command` | string | Yes | Executable (npx, docker, node, python). |
| `args` | array | No | Command arguments. |
| `env` | map | No | Environment variables. |
| `description` | string | No | Help text in MCP Manager. |

### HTTP/SSE MCPs (Remote)

```toml
[mcps.remote]
url = "https://api.example.com/mcp"
transport = "http"   # or "sse"
headers = { Authorization = "Bearer token" }  # Optional auth headers
description = "Remote MCP server"
```

| Key | Type | Required | Description |
|-----|------|----------|-------------|
| `url` | string | Yes | HTTP/SSE endpoint URL. |
| `transport` | string | No | "http" (default) or "sse". |
| `headers` | map | No | HTTP headers (e.g., Authorization). |
| `description` | string | No | Help text in MCP Manager. |

### HTTP MCPs with Auto-Start Server

For MCPs that require a local server process (e.g., `piekstra/slack-mcp-server`), add a `[mcps.NAME.server]` block:

```toml
[mcps.slack]
url = "http://localhost:30000/mcp/"
transport = "http"
description = "Slack 23+ tools"
[mcps.slack.headers]
  Authorization = "Bearer xoxb-token"
[mcps.slack.server]
  command = "uvx"
  args = ["--python", "3.12", "slack-mcp-server", "--port", "30000"]
  startup_timeout = 5000
  health_check = "http://localhost:30000/health"
  [mcps.slack.server.env]
    SLACK_API_TOKEN = "xoxb-token"
```

| Key | Type | Required | Description |
|-----|------|----------|-------------|
| `command` | string | Yes | Server executable. |
| `args` | array | No | Command arguments. |
| `env` | map | No | Server environment variables. |
| `startup_timeout` | int | No | Timeout in ms (default: 5000). |
| `health_check` | string | No | Health endpoint URL (defaults to main URL). |

**How it works:**
- Agent-deck starts the server automatically when the MCP is attached
- If the URL is already reachable (external server), uses it without spawning
- Health monitor restarts failed servers automatically
- CLI: `agent-deck mcp server status/start/stop`

### Common MCP Examples

```toml
# Web search
[mcps.exa]
command = "npx"
args = ["-y", "@anthropics/exa-mcp"]
env = { EXA_API_KEY = "xxx" }

# GitHub
[mcps.github]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-github"]
env = { GITHUB_TOKEN = "ghp_xxx" }

# Filesystem
[mcps.filesystem]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-filesystem", "/path"]

# Sequential thinking
[mcps.thinking]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-sequential-thinking"]

# Playwright
[mcps.playwright]
command = "npx"
args = ["-y", "@anthropics/playwright-mcp"]

# Memory
[mcps.memory]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-memory"]
```

## [tools.*] Section

Define custom AI tools.

```toml
[tools.my-ai]
command = "my-ai-assistant"
icon = "🧠"
busy_patterns = ["thinking...", "processing..."]
```

| Key | Type | Required | Description |
|-----|------|----------|-------------|
| `command` | string | Yes | Command to run. |
| `icon` | string | No | Emoji for TUI (default: 🐚). |
| `busy_patterns` | array | No | Strings indicating busy state. |

**Built-in icons:** claude=🤖, gemini=✨, opencode=🌐, codex=💻, cursor=📝, shell=🐚

## Complete Example

```toml
default_tool = "claude"

[claude]
config_dir = "~/.claude-work"
dangerous_mode = true

[codex]
yolo_mode = false

[logs]
max_size_mb = 10
max_lines = 10000
remove_orphans = true

[updates]
check_enabled = true
check_interval_hours = 24

[global_search]
enabled = true
tier = "auto"
recent_days = 90

[mcp_pool]
enabled = false

[mcps.exa]
command = "npx"
args = ["-y", "exa-mcp-server"]
env = { EXA_API_KEY = "your-key" }
description = "Web search"

[mcps.github]
command = "npx"
args = ["-y", "@modelcontextprotocol/server-github"]
env = { GITHUB_TOKEN = "ghp_xxx" }
description = "GitHub access"
```

## Environment Variables

| Variable | Purpose |
|----------|---------|
| `AGENTDECK_PROFILE` | Override default profile |
| `CLAUDE_CONFIG_DIR` | Override Claude config dir |
| `AGENTDECK_DEBUG=1` | Enable debug logging |
