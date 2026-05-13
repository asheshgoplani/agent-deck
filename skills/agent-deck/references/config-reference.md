# Configuration Reference

All options for `~/.agent-deck/config.toml`.

## Table of Contents

- [Top-Level](#top-level)
- [[shell] Section](#shell-section)
- [[claude] Section](#claude-section)
- [[gemini] Section](#gemini-section)
- [[opencode] Section](#opencode-section)
- [[codex] Section](#codex-section)
- [[copilot] Section](#copilot-section)
- [[hermes] Section](#hermes-section)
- [[docker] Section](#docker-section)
- [[worktree] Section](#worktree-section)
- [[logs] Section](#logs-section)
- [[updates] Section](#updates-section)
- [[display] Section](#display-section)
- [[global_search] Section](#global_search-section)
- [Skills Registry (Outside config.toml)](#skills-registry-outside-configtoml)
- [[mcp_pool] Section](#mcp_pool-section)
- [[mcps.*] Section](#mcps-section)
- [[tools.*] Section](#tools-section)
- [Path Resolution](#path-resolution)

## Top-Level

```toml
default_tool = "claude"   # Pre-selected tool when creating sessions
```

## [shell] Section

Shell environment configuration applied to all sessions.

```toml
[shell]
env_files = ["~/.agent-deck.env", ".env"]   # .env files to source for ALL sessions
init_script = "~/.agent-deck/init.sh"       # Script or command to run before each session
ignore_missing_env_files = true             # Silently skip missing .env files (default: true)
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `env_files` | array of strings | `[]` | List of .env files to source for ALL sessions, in order. Later files override earlier ones. See [Path Resolution](#path-resolution). |
| `init_script` | string | `""` | Shell script or inline command to run before each session. Useful for direnv, nvm, pyenv, etc. File paths (starting with `/`, `~/`, `./`, `../`) are sourced; anything else is treated as an inline command. |
| `ignore_missing_env_files` | bool | `true` | When `true`, missing .env files are silently skipped using `[ -f file ] && source file`. When `false`, sessions will error if an env file doesn't exist. |

### Sourcing order

Environment sources are applied in this order (later overrides earlier):

1. Global `[shell].env_files` (in order)
2. `[shell].init_script`
3. Tool-specific `env_file` (`[claude].env_file`, `[gemini].env_file`, `[tools.X].env_file`)
4. Inline env vars from `[tools.X].env` (highest priority)

## [claude] Section

Claude Code integration settings.

```toml
[claude]
config_dir = "~/.claude"           # Path to Claude config directory
dangerous_mode = true              # Enable --dangerously-skip-permissions
auto_mode = false                  # Enable --permission-mode auto (classifier-based)
allow_dangerous_mode = false       # Enable --allow-dangerously-skip-permissions
use_chrome = false                 # Enable --chrome
use_teammate_mode = false          # Enable --teammate-mode tmux
extra_args = ["--agent", "reviewer"] # Extra Claude CLI flags
env_file = "~/.claude.env"         # .env file specific to Claude sessions

[profiles.work.claude]
config_dir = "~/.claude-work"      # Optional override for profile "work"
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `config_dir` | string | `~/.claude` | Claude config directory. Override with `CLAUDE_CONFIG_DIR` env. |
| `profiles.<name>.claude.config_dir` | string | none | Profile-specific Claude config directory. Takes precedence over `[claude].config_dir` when that profile is active. |
| `dangerous_mode` | bool | `false` | Adds `--dangerously-skip-permissions`. Forces bypass on. Takes precedence over `auto_mode` and `allow_dangerous_mode`. |
| `auto_mode` | bool | `false` | Adds `--permission-mode auto`. A classifier model auto-approves safe operations while blocking risky ones. Ignored when `dangerous_mode` is true. |
| `allow_dangerous_mode` | bool | `false` | Adds `--allow-dangerously-skip-permissions`. Unlocks bypass as an option without activating it. Ignored when `dangerous_mode` or `auto_mode` is true. |
| `use_chrome` | bool | `false` | Adds `--chrome` to Claude sessions and is remembered from the New Session dialog. |
| `use_teammate_mode` | bool | `false` | Adds `--teammate-mode tmux` to Claude sessions and is remembered from the New Session dialog. |
| `extra_args` | array of strings | `[]` | Extra Claude CLI flags remembered from the New Session dialog and appended to new/restarted Claude sessions. Do not store secrets here. |
| `env_file` | string | `""` | A .env file sourced for Claude sessions only. Sourced after global `[shell].env_files`. See [Path Resolution](#path-resolution). |
| `command` | string | `"claude"` | Override the binary/invocation (e.g., `"cdw"` for a wrapper that sets `CLAUDE_CONFIG_DIR`). |

Config resolution order for Claude config dir:
1. `CLAUDE_CONFIG_DIR` env var
2. `[profiles.<active-profile>.claude].config_dir`
3. `[claude].config_dir`
4. `~/.claude`

### Multiple Claude accounts (per profile)

Use a global default, then override only profiles that need a different Claude account/config:

```toml
[claude]
config_dir = "~/.claude"             # Global default (personal)

[profiles.work.claude]
config_dir = "~/.claude-work"        # Work account

[profiles.clientx.claude]
config_dir = "~/.claude-clientx"     # Client account
```

Launch each profile normally:

```bash
agent-deck               # Uses default profile -> global [claude].config_dir
agent-deck -p work       # Uses [profiles.work.claude].config_dir
agent-deck -p clientx    # Uses [profiles.clientx.claude].config_dir
```

Verify the effective Claude config path:

```bash
agent-deck hooks status
agent-deck hooks status -p work
agent-deck hooks status -p clientx
```

## [gemini] Section

Gemini CLI integration settings.

```toml
[gemini]
yolo_mode = true                    # Enable --yolo (auto-approve all actions)
default_model = "gemini-2.5-flash"  # Model override
env_file = "~/.gemini.env"          # .env file for Gemini sessions
command = "gemini"                   # Binary/invocation override
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `yolo_mode` | bool | `false` | Maps to Gemini `--yolo`. |
| `default_model` | string | `""` | Model to use (e.g., `"gemini-2.5-flash"`). Empty uses Gemini's default. |
| `env_file` | string | `""` | A .env file sourced for Gemini sessions only. See [Path Resolution](#path-resolution). |
| `command` | string | `"gemini"` | Override the binary/invocation. Supports flags. |

## [opencode] Section

OpenCode CLI integration settings.

```toml
[opencode]
default_model = "anthropic/claude-sonnet-4-5-20250929"
default_agent = ""
env_file = "~/.opencode.env"
command = "opencode"
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `default_model` | string | `""` | Model in `provider/model` format. |
| `default_agent` | string | `""` | Agent to use. Empty uses OpenCode's default. |
| `env_file` | string | `""` | A .env file sourced for OpenCode sessions only. See [Path Resolution](#path-resolution). |
| `command` | string | `"opencode"` | Override the binary/invocation. |

## [codex] Section

Codex CLI integration settings.

```toml
[codex]
command = "codex"  # Codex CLI command or alias
yolo_mode = true   # Enable --yolo (bypass approvals and sandbox)
env_file = "~/.codex.env"
command = "codex"
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `command` | string | `codex` | Codex CLI command or alias to launch built-in Codex sessions. Examples: `codex-v2`, `CODEX_HOME=~/.codex-work codex`. |
| `yolo_mode` | bool | `false` | Maps to `codex --yolo` (`--dangerously-bypass-approvals-and-sandbox`). Can be overridden per-session. |
| `env_file` | string | `""` | A .env file sourced for Codex sessions only. See [Path Resolution](#path-resolution). |
| `command` | string | `"codex"` | Override the binary/invocation. |

## [copilot] Section

GitHub Copilot CLI integration settings.

```toml
[copilot]
env_file = "~/.copilot.env"
command = "copilot"
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `env_file` | string | `""` | A .env file sourced for Copilot sessions only. See [Path Resolution](#path-resolution). |
| `command` | string | `"copilot"` | Override the binary/invocation. |

## [hermes] Section

Hermes Agent CLI integration settings ([NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent)).

```toml
[hermes]
command = "hermes --model gpt-5.5-pro --provider openai"
env_file = "~/.hermes.env"
yolo_mode = false
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `command` | string | `"hermes"` | Override the binary/invocation. Supports flags (e.g., model/provider). |
| `env_file` | string | `""` | A .env file sourced for Hermes sessions only. See [Path Resolution](#path-resolution). |
| `yolo_mode` | bool | `false` | Maps to `hermes --yolo` (auto-approve all tool calls). |

Status detection: process-alive/dead only. Content-sniffing planned for future release.

When using a different Codex home, prefer an inline command such as `CODEX_HOME=~/.codex-work codex` or export `CODEX_HOME` before starting agent-deck. Shell aliases are allowed, but agent-deck cannot infer `CODEX_HOME` hidden inside an alias for resume-file discovery.

## [docker] Section

Docker sandbox settings. Run sessions inside isolated containers. Toggle per-session when creating, or set defaults here. Access in TUI via `S` (Settings).

```toml
[docker]
default_enabled = false        # Check "sandbox" by default in new session dialog
default_image = ""             # Custom Docker image (default: built-in Ubuntu)
cpu_limit = ""                 # CPU limit, e.g. "2.0"
memory_limit = ""              # Memory limit, e.g. "4g"
mount_ssh = false              # Mount ~/.ssh read-only into container
auto_cleanup = true            # Remove containers on session kill
environment = []               # Host env vars to pass into container
volume_ignores = []            # Directories to exclude from project mount
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `default_enabled` | bool | `false` | Pre-check sandbox checkbox when creating sessions. |
| `default_image` | string | `""` | Custom Docker image. Empty uses the built-in image. |
| `cpu_limit` | string | `""` | Container CPU limit (e.g. `"2.0"` for 2 cores). |
| `memory_limit` | string | `""` | Container memory limit (e.g. `"4g"`). |
| `mount_ssh` | bool | `false` | Bind-mount `~/.ssh` read-only for git access inside containers. |
| `auto_cleanup` | bool | `true` | Remove sandbox containers when sessions are killed. |
| `environment` | array | `[]` | Host environment variable names to forward into containers. |
| `volume_ignores` | array | `[]` | Directories to exclude from the project bind mount (e.g. `["node_modules", ".git"]`). |

## [worktree] Section

Git worktree settings. Worktrees allow creating isolated working directories for branches, so each session gets its own checkout.

```toml
[worktree]
default_enabled = false                              # Pre-check "Create in worktree" in dialogs
default_location = "sibling"                         # "sibling", "subdirectory", or custom path
path_template = "~/.agent-deck/worktrees/{repo-name}/{branch}"  # Custom path (overrides default_location)
branch_prefix = "feature/"                           # Prefix for branch names ("" to disable)
auto_cleanup = true                                  # Remove worktree when session is deleted
setup_timeout_seconds = 60                           # Timeout for .agent-deck/worktree-setup.sh
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `default_enabled` | bool | `false` | Pre-check "Create in worktree" in new-session and fork dialogs. |
| `default_location` | string | `"sibling"` | Where to create worktrees: `"sibling"` (next to repo), `"subdirectory"` (inside `.worktrees/`), or a custom path (e.g., `"~/worktrees"`) creating `<path>/<repo_name>/<branch>`. Ignored when `path_template` is set. |
| `path_template` | string | none | Custom path template. Overrides `default_location`. Variables: `{repo-name}`, `{repo-root}`, `{session-id}`, `{branch}` (sanitized, human-friendly), `{branch-escaped}` (URL-escaped, collision-resistant). |
| `branch_prefix` | string | `"feature/"` | Prefix prepended to branch names. Supports environment variable expansion (e.g., `"$USER/"`). Set to `""` to disable. Won't double-prepend if the branch already starts with the prefix. |
| `auto_cleanup` | bool | `false` | Remove worktree directory when the session is deleted. |
| `setup_timeout_seconds` | int | `60` | Max seconds for `.agent-deck/worktree-setup.sh` to run. Set to `0` for unlimited. |

### Path template examples

```toml
# Sibling directories (default behavior)
path_template = "../worktrees/{repo-name}/{branch}"

# Central location under home
path_template = "~/.agent-deck/worktrees/{repo-name}/{branch}"

# Collision-resistant (useful with many similar branch names)
path_template = "~/.agent-deck/worktrees/{repo-name}/{branch-escaped}"
```

### Branch prefix examples

```toml
# Default: prefix with "feature/"
branch_prefix = "feature/"        # "my-session" -> "feature/my-session"

# Username prefix (env var expansion)
branch_prefix = "$USER/"          # "my-session" -> "dani/my-session"

# No prefix (just the session name)
branch_prefix = ""                # "my-session" -> "my-session"
```

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

## [display] Section

Rendering and display settings.

```toml
[display]
full_repaint = false                              # Force full screen clear every render (for terminals with grapheme issues)
default_filter = "active"                         # Initial status filter: "", "active", "running", "waiting", "idle", "error"
active_filter_label = "Open"                      # Label for the active filter pill (default: "Open")
active_filter_excludes = ["error", "stopped"]     # Statuses the % "Open" filter hides (default: ["error", "stopped"])
```

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `full_repaint` | bool | `false` | Force full redraws (fix for Ghostty 1.3+ drift). Also via `AGENTDECK_REPAINT=full`. |
| `default_filter` | string | `""` | Status filter applied on TUI startup. `"active"` engages the configurable Open filter. Auto-clears if no sessions match. |
| `active_filter_label` | string | `"Open"` | Label shown on the filter pill when active filter is engaged (e.g., "Active", "Live", "Open"). |
| `active_filter_excludes` | []string | `["error", "stopped"]` | Statuses hidden when the `%` "Open" filter is engaged. Default matches the original hardcoded behavior. Valid values: `running`, `waiting`, `idle`, `error`, `starting`, `stopped`. Unknown entries are dropped silently; if the resulting list is empty the default applies. **Set to `["error"]`** to keep stopped/closed sessions visible while still hiding errors — fixes the over-broad "Open" semantics where closed sessions disappeared from view. Extend with `idle` for an aggressive "show only running/waiting" definition of open. |

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

## Skills Registry (Outside config.toml)

Skill source discovery and project attachment state are not stored in `~/.agent-deck/config.toml`.

**Global source registry:**
- `~/.agent-deck/skills/sources.toml`
- Includes default sources:
  - `pool` -> `~/.agent-deck/skills/pool`
  - `claude-global` -> `~/.claude/skills` (or active Claude config dir)

**Project attachment state:**
- `<project>/.agent-deck/skills.toml` (managed manifest)
- `<project>/.claude/skills` (materialized links/copies for Claude-compatible sessions)
- `<project>/.agents/skills` (materialized links/copies for Gemini, Codex, and Pi sessions)

**Manage via CLI:**
```bash
agent-deck skill source list
agent-deck skill source add team ~/src/team-skills
agent-deck skill source remove team
```

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
env_file = "~/.my-ai.env"
env = { API_KEY = "token", BASE_URL = "https://api.example.com" }
```

| Key | Type | Required | Description |
|-----|------|----------|-------------|
| `command` | string | Yes | Command to run. |
| `icon` | string | No | Emoji for TUI (default: 🐚). |
| `busy_patterns` | array | No | Strings indicating busy state. |
| `env_file` | string | No | A .env file sourced for this tool only. Sourced after global `[shell].env_files`. See [Path Resolution](#path-resolution). |
| `env` | map | No | Inline environment variables exported for this tool. These take highest priority, overriding both `[shell].env_files` and `env_file`. Values are single-quoted to prevent shell expansion. |

**Built-in icons:** claude=🤖, gemini=✨, opencode=🌐, codex=💻, copilot=🐙, hermes=☤, cursor=📝, shell=🐚

## Path Resolution

All `env_file` and `env_files` path values support the following formats:

| Format | Example | Resolves to |
|--------|---------|-------------|
| Absolute path | `/etc/agent-deck/.env` | Used as-is |
| `~` (tilde) | `~/.claude.env` | Expanded to home directory (e.g., `/home/user/.claude.env`) |
| Environment variables | `$HOME/.claude.env` | Expanded via `os.ExpandEnv` (e.g., `/home/user/.claude.env`) |
| `${VAR}` syntax | `${XDG_CONFIG_HOME}/env` | Expanded via `os.ExpandEnv` |
| Relative path | `.env`, `config/.env` | Resolved relative to the session's working directory |

Environment variable expansion (`$HOME`, `$USER`, `${VAR}`, etc.) is applied before determining whether a path is absolute or relative. This means `$HOME/.env` correctly resolves to an absolute path rather than being treated as relative.

## Complete Example

```toml
default_tool = "claude"

[shell]
env_files = ["~/.agent-deck.env"]
init_script = "~/.agent-deck/init.sh"
ignore_missing_env_files = true

[claude]
config_dir = "~/.claude"
dangerous_mode = true
env_file = "~/.claude.env"

[profiles.work.claude]
config_dir = "~/.claude-work"

[gemini]
yolo_mode = true
env_file = "~/.gemini.env"

[opencode]
env_file = "~/.opencode.env"

[codex]
command = "codex"
yolo_mode = false
env_file = "~/.codex.env"

[copilot]
env_file = "~/.copilot.env"

[hermes]
command = "hermes --model gpt-5.5-pro --provider openai"
env_file = "~/.hermes.env"
yolo_mode = false

[docker]
default_enabled = false
mount_ssh = true

[worktree]
default_location = "sibling"
auto_cleanup = true
branch_prefix = "$USER/"

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
