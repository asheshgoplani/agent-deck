# Troubleshooting Guide

Common issues and solutions for agent-deck.

## Quick Fixes

| Issue | Solution |
|-------|----------|
| Session shows `✕` error | `agent-deck session start <name>` |
| MCPs not loading | `agent-deck session restart <name>` |
| CLI changes not in TUI | Press `Ctrl+R` to refresh |
| Flag not working | Put flags BEFORE arguments |
| Fork fails | Check Claude session has a valid session ID, or Pi session has JSONL history under Agent Deck's Pi session dir |
| Status stuck | Wait 2 seconds or press `u` to mark unread |

## Common Issues

### Cannot Select or Copy Terminal Text

On the agent-deck home screen, select a local session and press `V` to copy its
current visible terminal text, including links, as plain text.

When attached to a session, tmux mouse mode owns normal drag gestures. Hold
Option while dragging in iTerm2. Hold Shift while dragging in most Linux
terminals and Windows Terminal, including WSL2. This bypasses application mouse
reporting and lets the terminal perform native selection.

If your terminal has no selection bypass, disable mouse mode for new and
reconnected sessions:

```toml
[tmux]
mouse = false
```

This restores native drag selection, but disables tmux mouse scrolling, pane
resizing, and mouse copy mode.

### Flags Ignored

**Problem:** Flags after positional arguments are silently ignored.

```bash
# WRONG - message not sent
agent-deck session start my-project -m "Hello"

# CORRECT
agent-deck session start -m "Hello" my-project
```

### MCP Not Available

1. Check if attached: `agent-deck mcp attached <session>`
2. Restart session: `agent-deck session restart <session>`
3. Verify in config: `agent-deck mcp list`

### Session ID Not Detected

Claude session ID needed for fork/resume. Check:

```bash
agent-deck session show <name> --json | jq '.claude_session_id'
```

If null, restart session and interact with Claude.

### Conductor Keeps Asking for Permissions

If a conductor repeatedly pauses on permission prompts, set Claude permission mode
explicitly in `~/.agent-deck/config.toml` and restart the conductor session:

```toml
[claude]
# Safer default for automation-heavy conductors:
allow_dangerous_mode = true

# Or fully non-interactive (least safe):
# dangerous_mode = true
```

Then restart the conductor:

```bash
agent-deck session restart conductor-<name>
```

If you use multiple profiles, set the same under the profile override:

```toml
[profiles.work.claude]
allow_dangerous_mode = true
```

### Atuin Pty-Proxy Incompatibility

**Problem:** TUI shows a blank screen or fails to render when `eval "$(atuin pty-proxy init zsh)"` is in `.zshrc`.

**Cause:** Atuin pty-proxy acts as a PTY MITM between the terminal and the shell. Agent Deck's Bubble Tea TUI requires direct terminal access for alternate screen mode, mouse tracking, and raw-mode I/O. These all break when stdin/stdout are proxied pipes.

**Fix:** Replace the pty-proxy init line with standard atuin init:

```bash
# REMOVE this line:
eval "$(atuin pty-proxy init zsh)"

# REPLACE with this:
eval "$(atuin init zsh)"
```

For bash:
```bash
eval "$(atuin init bash)"
```

For fish:
```fish
atuin init fish | source
```

Atuin pty-proxy is only needed for the atuin TUI overlay feature and is not required for normal shell history functionality. Agent Deck works fine with standard `atuin init`.

### High CPU Usage

**With many sessions:** Normal if batched updates. Check:
```bash
agent-deck status  # Should show ~0.5% CPU when idle
```

**With active session:** Normal (live preview updates).

### Log Files Too Large

Add to `~/.agent-deck/config.toml`:
```toml
[logs]
max_size_mb = 1
max_lines = 2000
```

### Global Search Not Working

Check config:
```toml
[global_search]
enabled = true
```

Also verify `~/.claude/projects/` exists and has content.

### Shift+Enter Submits Instead of Inserting a Newline (Kitty)

In **kitty**, Shift+Enter (and other modified keys) may submit immediately
inside agent-deck even though they insert a newline when the agent runs
natively. This is a kitty-specific quirk: tmux negotiates extended keys with
the outer terminal using xterm's *modifyOtherKeys* protocol, which kitty does
not implement (kitty only speaks its own CSI-u keyboard protocol). So kitty
keeps sending a bare carriage return and the agent submits.

agent-deck already sets `extended-keys-format csi-u` on its tmux sessions so
that, *once the terminal sends a distinct Shift+Enter*, it reaches the agent in
the form Claude Code understands. The remaining piece must be set in kitty
itself — make kitty emit the CSI-u Shift+Enter unconditionally:

```conf
# ~/.config/kitty/kitty.conf
map shift+enter send_text all \x1b[13;2u
```

Reload kitty's config (`Ctrl+Shift+F5`) and Shift+Enter will insert a newline,
both natively and inside agent-deck. iTerm2, Ghostty, WezTerm and xterm honor
modifyOtherKeys and do not need this mapping.

## Debugging

Enable debug logging:
```bash
AGENTDECK_DEBUG=1 agent-deck
```

Check session logs:
```bash
tail -100 ~/.agent-deck/logs/agentdeck_<session>_*.log
```

## Report a Bug

If something isn't working, please create a GitHub issue with all relevant context.

### Step 1: Gather Information

Run these commands and save output:

```bash
# Version info
agent-deck version

# Current status
agent-deck status --json

# Session details (if session-related)
agent-deck session show <session-name> --json

# Config (sanitized - removes secrets)
cat ~/.agent-deck/config.toml | grep -v "KEY\|TOKEN\|SECRET\|PASSWORD"

# Recent logs (if error occurred)
tail -100 ~/.agent-deck/logs/agentdeck_<session>_*.log 2>/dev/null

# System info
uname -a
echo "tmux: $(tmux -V 2>/dev/null || echo 'not installed')"
```

### Step 2: Describe the Issue

Prepare clear answers to:

1. **What did you try?** (exact command or TUI action)
2. **What happened?** (error message, unexpected behavior)
3. **What did you expect?** (correct behavior)
4. **Can you reproduce it?** (steps to trigger)

### Step 3: Create GitHub Issue

Go to: **https://github.com/asheshgoplani/agent-deck/issues/new**

Use this template:

```markdown
## Description

[Brief description of the issue]

## Steps to Reproduce

1. [First step]
2. [Second step]
3. [What happened]

## Expected Behavior

[What should have happened]

## Environment

- agent-deck version: [output of `agent-deck version`]
- OS: [macOS/Linux/WSL]
- tmux version: [output of `tmux -V`]

## Debug Output

<details>
<summary>Status JSON</summary>

```json
[paste agent-deck status --json]
```

</details>

<details>
<summary>Config (sanitized)</summary>

```toml
[paste sanitized config]
```

</details>

<details>
<summary>Logs</summary>

```
[paste relevant log lines]
```

</details>
```

### Step 4: Follow Up

- Check for responses on your issue
- Test any suggested fixes
- Update issue with results
- Join [Discord](https://discord.gg/e4xSs6NBN8) for quick help and community support

## Recovery

### Session Metadata Lost

Data stored in SQLite:
```bash
~/.agent-deck/profiles/default/state.db
```

Note: new installs store profiles under `$XDG_DATA_HOME/agent-deck/profiles/` (default `~/.local/share/agent-deck/profiles/`); a legacy `~/.agent-deck/` directory is still honored when present.

Recovery (if state.db is corrupted):
```bash
# If sessions.json.migrated still exists, delete state.db and restart.
# agent-deck will auto-migrate from the .migrated file.
rm ~/.agent-deck/profiles/default/state.db
mv ~/.agent-deck/profiles/default/sessions.json.migrated \
   ~/.agent-deck/profiles/default/sessions.json
# Restart agent-deck to trigger auto-migration into a fresh state.db
```

### tmux Sessions Lost

Session logs preserved:
```bash
tail -500 ~/.agent-deck/logs/agentdeck_<session>_*.log
```

### Profile Corrupted

Create fresh:
```bash
agent-deck profile create fresh
agent-deck profile default fresh
```

## Uninstalling

Remove agent-deck from your system:

```bash
agent-deck uninstall              # Interactive uninstall
agent-deck uninstall --dry-run    # Preview what would be removed
agent-deck uninstall --keep-data  # Remove binary only, keep sessions
```

Or use the standalone script:
```bash
curl -fsSL https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/uninstall.sh | bash
```

**What gets removed:**
- **Binary:** `~/.local/bin/agent-deck` or `/usr/local/bin/agent-deck`
- **Homebrew:** `agent-deck` package (if installed via brew)
- **tmux config:** The `# agent-deck configuration` block in `~/.tmux.conf`
- **Data directory:** `~/.agent-deck/` (sessions, logs, config)

Use `--keep-data` to preserve your sessions and configuration.

## Critical Warnings

**NEVER run these commands - they destroy ALL agent-deck sessions:**

```bash
# DO NOT RUN
tmux kill-server
tmux ls | grep agentdeck | xargs tmux kill-session
```

**Recovery impossible** - metadata backups exist but tmux sessions are gone.
