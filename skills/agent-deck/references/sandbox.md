# Docker Sandbox: Quick Reference

## Overview

Docker sandboxing runs your AI coding agents (Claude Code, OpenCode, Codex CLI, Gemini CLI) inside isolated Docker containers while maintaining access to your project files and credentials.

**Key Features:**
- One container per session
- Shared authentication across containers (no re-auth needed)
- Automatic container lifecycle management
- Full project access via volume mounts

## CLI vs TUI Behavior

| Feature | CLI | TUI |
|---------|-----|-----|
| Enable sandbox | `--sandbox` flag | Checkbox toggle |
| Custom image | `--sandbox-image <image>` | Not supported |
| Container cleanup | Automatic on remove | Automatic on remove |
| Settings | `~/.agent-deck/config.toml` | `S` (Settings panel) |

## One-Liner Commands

```bash
# Create sandboxed session
agent-deck add --sandbox .

# Create sandboxed session with custom image
agent-deck add --sandbox-image myregistry/custom:v1 .

# One-shot sandboxed task
agent-deck try "refactor the auth module"

# Remove session (auto-cleans container)
agent-deck remove <session>
```

**Note:** The TUI sandbox checkbox is always visible. Docker availability is checked at session start time — if Docker is not installed or the daemon is not running, the session will report an error.

## Precedence

CLI flags take priority over config file defaults:

| Setting | CLI Flag | Config Key | Default |
|---------|----------|------------|---------|
| Enable sandbox | `--sandbox` | `docker.default_enabled` | `false` |
| Sandbox image | `--sandbox-image` | `docker.default_image` | `""` (built-in) |

## Default Configuration

```toml
[docker]
default_enabled = false
default_image = ""
auto_cleanup = true
cpu_limit = ""
memory_limit = ""
mount_ssh = false
environment = []
volume_ignores = []
```

## Configuration Options

| Option | Default | Description |
|--------|---------|-------------|
| `default_enabled` | `false` | Pre-check sandbox for new sessions |
| `default_image` | `""` | Docker image (empty uses built-in `agent-deck-sandbox:latest`) |
| `auto_cleanup` | `true` | Remove containers when sessions are killed |
| `cpu_limit` | `""` | CPU limit (e.g. `"2.0"`) |
| `memory_limit` | `""` | Memory limit (e.g. `"4g"`) |
| `mount_ssh` | `false` | Mount `~/.ssh/` read-only into containers |
| `environment` | `[]` | Host env var names to pass into containers |
| `volume_ignores` | `[]` | Directories to exclude from project mount |

See the full [Configuration Reference](config-reference.md) for details.

## Volume Mounts

### Automatic Mounts

| Host Path | Container Path | Mode | Purpose |
|-----------|----------------|------|---------|
| Project directory | `/workspace` | RW | Your code |

## Shared Sandbox Directories

Agent Deck automatically shares your host tool credentials with sandboxed containers so agents can authenticate without re-login. This works for all supported tools: Claude Code, Codex, Gemini, and OpenCode.

For each tool whose host config directory exists (e.g. `~/.claude/`, `~/.codex/`, `~/.gemini/`, `~/.local/share/opencode/`), Agent Deck syncs credential files into a **shared sandbox directory** and bind-mounts it into containers:

1. On every session start, host config files are synced into `~/<tool>/sandbox/`.
2. **Seed files** (e.g. onboarding flags) use write-once semantics — they are only written if absent, preserving any state accumulated by the container.
3. The sandbox directory is mounted read-write into the container at the expected path.
4. The container can read credentials and write runtime state freely without affecting your host config.
5. Tools whose config directory doesn't exist on the host are simply skipped.

### What gets synced

- **Top-level files** from each tool's config directory (auth tokens, credentials, config files). Subdirectories are skipped by default to keep the sandbox small.
- **Specific subdirectories** listed per tool (e.g. Claude Code's `plugins/` and `skills/` are copied recursively so extensions work inside the container).
- **Seed files** where needed (e.g. Claude Code gets a minimal `hasCompletedOnboarding` flag to skip the first-run wizard). These are only written once — container modifications are preserved across restarts.

### Platform-specific authentication

- **Linux:** Credential files (e.g. `.credentials.json`) live directly in the tool's config directory and are synced automatically.
- **macOS:** Some tools store credentials in the macOS Keychain rather than on disk. Agent Deck extracts these at sync time and writes them as files in the sandbox directory so the container can authenticate. For example, Claude Code OAuth tokens are extracted from the Keychain and written as `.credentials.json`. If no Keychain entry is found (e.g. you authenticate via `ANTHROPIC_API_KEY`), pass your API key via the `environment` config.

### Sandbox refresh

Sandbox directories are refreshed every time a session starts (not just on first creation). If you re-authenticate on the host or update credentials, the next session start picks up the changes. Container-written files (including modified seed files) are preserved across refreshes.

### Sandbox directory location

Each tool's sandbox directory lives inside that tool's own config directory:

```
~/.claude/sandbox/                 # Claude Code sandbox
~/.codex/sandbox/                  # Codex sandbox
~/.gemini/sandbox/                 # Gemini sandbox
~/.local/share/opencode/sandbox/   # OpenCode sandbox
```

These directories persist on the host and are shared across all containers. Deleting a tool's config directory (e.g. `rm -rf ~/.codex/`) removes everything related to that tool, including its sandbox directory. The `sandbox/` subdirectories are safe to delete manually if you want to reset state.

## Container Naming

Containers are named: `agent-deck-{session_title}-{session_id_first_8_chars}`

The session title is sanitized for Docker (special characters stripped, spaces become hyphens, truncated to 30 chars). If the title is empty, only the ID prefix is used.

Example: `agent-deck-my-refactor-a1b2c3d4`

## How It Works

1. **Session Creation:** When you create a sandboxed session, Agent Deck records the sandbox configuration.
2. **Container Start:** When the session starts, Agent Deck syncs host tool configs into shared sandbox directories and creates/starts the Docker container with bind mounts.
3. **tmux + docker exec:** The host tmux session runs `docker exec -it <container> <tool>` (claude, opencode, codex, or gemini).
4. **Container Shell:** Press `E` on a sandboxed session to exec a shell inside the container.
5. **Cleanup:** When you remove the session, the container is automatically deleted. Sandbox directories persist for reuse.

## Environment Variables

These terminal-related variables are always passed through for proper UI/theming:
- `TERM`, `COLORTERM`, `FORCE_COLOR`, `NO_COLOR`

Pass additional variables (like API keys) through to containers by adding them to config:

```toml
[docker]
environment = ["ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY"]
```

These variables are read from your host environment and passed to containers (in addition to the terminal defaults above).

## Custom Docker Images

The default sandbox image (`agent-deck-sandbox:latest`) includes the supported AI tools and basic development utilities. For projects requiring additional dependencies, extend the base image.

### Create a Dockerfile

```dockerfile
FROM agent-deck-sandbox:latest

# Example: Add Python for a data science project
RUN apt-get update && apt-get install -y \
    python3 \
    python3-pip \
    python3-venv \
    && rm -rf /var/lib/apt/lists/*
```

### Build and configure

```bash
# Build locally
docker build -t my-sandbox:latest .
```

```toml
# Set as default in ~/.agent-deck/config.toml
[docker]
default_image = "my-sandbox:latest"
```

Or use per-session via CLI:

```bash
agent-deck add --sandbox-image my-sandbox:latest .
```

## Security Model

Sandboxed containers are hardened to limit the blast radius of agent actions:

- **No capabilities:** Containers start with `--cap-drop=ALL`, removing all Linux capabilities (no `chown`, `net_raw`, `sys_admin`, etc.).
- **No privilege escalation:** `--security-opt=no-new-privileges` prevents processes from gaining additional privileges via setuid binaries or similar mechanisms.
- **Read-only filesystem:** The root filesystem is mounted read-only (`--read-only`). Only `/tmp`, `/var/tmp`, and tool cache directories are writable via tmpfs mounts.
- **Process limit:** `--pids-limit=4096` prevents fork bombs from consuming host resources.
- **No Docker socket:** The Docker socket is never mounted, so agents cannot control the host Docker daemon.
- **Volume restrictions:** User-configured extra volumes are validated against blocked lists. Host paths are resolved through symlinks before checking, preventing symlink-based bypass. Paths like `/etc`, `/proc`, `/sys`, the Docker socket, and home-relative secret directories (`.gnupg`, `.aws`, `.azure`, `.config`) are rejected.
- **Symlink boundary enforcement:** When syncing host config files into the sandbox, symlinks that resolve outside the source directory are rejected to prevent credential exfiltration.
- **Credential cleanup:** On macOS, plaintext credentials extracted from the Keychain for sandbox use are removed from the host filesystem when the session ends.

## Troubleshooting

### Container killed due to memory (OOM)

**Symptoms:** Your sandboxed session exits unexpectedly, the container disappears, or you see "Killed" in the output.

**Cause:** On macOS, Docker runs inside a Linux VM with a fixed memory ceiling. Docker Desktop defaults to 2 GB for the entire VM.

**Fix:**

1. Increase Docker Desktop VM memory: **Settings > Resources > Advanced**, increase the **Memory** slider (8 GB+ recommended).

2. Set a per-container memory limit in config:

   ```toml
   [docker]
   memory_limit = "8g"
   ```

3. Verify: `docker stats --no-stream` — check the `MEM LIMIT` column.

### Resetting sandbox state

If you need to reset the shared sandbox directories (e.g. to clear stale credentials), delete them manually:

```bash
rm -rf ~/.claude/sandbox/
rm -rf ~/.codex/sandbox/
rm -rf ~/.gemini/sandbox/
rm -rf ~/.local/share/opencode/sandbox/
```

They will be re-created automatically on the next session start.
