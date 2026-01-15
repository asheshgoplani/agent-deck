# Agent Deck - Quick Reference Cheatsheet

## CLI Commands

### Session Management
```bash
agent-deck add /path -t "Title" -g "group" -c claude  # Create session
agent-deck session start <id|title>                    # Start session
agent-deck session stop <id>                           # Stop session
agent-deck session restart <id>                        # Restart (resumes with updated MCPs)
agent-deck session fork <id> -t "New Title"            # Fork (Claude only)
agent-deck session attach <id>                         # Attach to session
agent-deck session show <id> --json                    # Session details
agent-deck session current -q                          # Current session name
agent-deck session set <id> title "New Name"           # Update property
agent-deck session output <id>                         # Get last response
```

### MCP Management
```bash
agent-deck mcp list                         # Available MCPs
agent-deck mcp attached <id>                # MCPs for session
agent-deck mcp attach <id> exa --restart    # Attach MCP
agent-deck mcp detach <id> exa --global     # Detach globally
```

### Group Management
```bash
agent-deck group list                       # List groups
agent-deck group create "Projects"          # Create root group
agent-deck group create "Backend" --parent "Projects"  # Subgroup
agent-deck group move <id> "Projects/Backend"          # Move session
agent-deck group delete "Old" --force       # Delete with sessions
```

### Profiles
```bash
agent-deck -p work list                     # Use work profile
agent-deck -p research add /path -c claude  # Create in research profile
```

### Status & Info
```bash
agent-deck status                 # Status summary
agent-deck status -q              # Just waiting count
agent-deck status --json          # JSON output
agent-deck list --json            # All sessions as JSON
agent-deck version                # Version info
```

---

## TUI Keyboard Shortcuts

### Navigation
| Key | Action |
|-----|--------|
| `j`/`↓` | Move down |
| `k`/`↑` | Move up |
| `h`/`←` | Collapse group |
| `l`/`→`/`Tab` | Toggle expand |
| `Enter` | Attach to session |

### Session Actions
| Key | Action |
|-----|--------|
| `n` | New session |
| `r`/`R` | Restart session |
| `f` | Fork (Claude) |
| `F` | Fork with dialog |
| `e` | Rename |
| `m` | Move to group |
| `d` | Delete |
| `u` | Mark unread |
| `M` | MCP Manager |

### Quick Filters
| Key | Filter |
|-----|--------|
| `0` | All |
| `!` | Running |
| `@` | Waiting |
| `#` | Idle |
| `$` | Error |

### Global
| Key | Action |
|-----|--------|
| `/` | Search |
| `G` | Global search |
| `?` | Help |
| `i` | Import tmux |
| `Ctrl+Q` | Detach |
| `q` | Quit |

---

## Status Indicators

| Symbol | Color | Status | Meaning |
|--------|-------|--------|---------|
| `●` | Green | Running | Agent actively processing |
| `◐` | Yellow | Waiting | Needs attention |
| `○` | Gray | Idle | Acknowledged, stopped |
| `✕` | Red | Error | Session doesn't exist |

---

## Configuration

### config.toml Location
```
~/.agent-deck/config.toml
```

### Claude Configuration
```toml
[claude]
config_dir = "~/.claude-work"   # Custom Claude profile
dangerous_mode = true           # Skip permissions
```

### Custom Tool
```toml
[tools.vibe]
command = "vibe"
icon = "🎵"
busy_patterns = ["executing", "running tool"]
prompt_patterns = ["approve?", "[y/n]"]
resume_flag = "--resume"
session_id_env = "VIBE_SESSION_ID"
dangerous_flag = "--auto-approve"
```

### MCP Server
```toml
[mcps.exa]
command = "npx"
args = ["-y", "exa-mcp-server"]
env = { EXA_API_KEY = "your-key" }
description = "Web search"
```

### MCP Pooling
```toml
[mcp_pool]
enabled = true
pool_all = true
exclude_mcps = ["slow-mcp"]
fallback_to_stdio = true
```

### Log Management
```toml
[logs]
max_size_mb = 1
max_lines = 2000
remove_orphans = true
```

---

## File Locations

| Path | Purpose |
|------|---------|
| `~/.agent-deck/config.toml` | User configuration |
| `~/.agent-deck/profiles/default/sessions.json` | Session storage |
| `~/.agent-deck/profiles/{profile}/sessions.json` | Profile storage |
| `~/.agent-deck/logs/` | Session logs |
| `/tmp/agentdeck-mcp-*.sock` | MCP socket files |
| `{project}/.mcp.json` | Project-local MCPs |

---

## Scripting Examples

### Batch Create Sessions
```bash
for dir in ~/projects/*; do
  agent-deck add "$dir" -c claude -g "Projects"
done
```

### Find Waiting Sessions
```bash
agent-deck list --json | jq '.[] | select(.status=="waiting") | .title'
```

### Attach MCP to All
```bash
agent-deck list --json | jq -r '.[].id' | while read id; do
  agent-deck mcp attach "$id" memory --restart
done
```

### Export Profile
```bash
agent-deck -p default list --json > backup.json
```

### Get Current Session in Script
```bash
SESSION=$(agent-deck session current -q)
PROFILE=$(agent-deck session current --json | jq -r '.profile')
```

---

## Troubleshooting

### Session stuck in error
```bash
# Check if tmux session exists
tmux has-session -t agentdeck_name_id

# Force refresh
agent-deck session restart <id>
```

### MCP not loading
```bash
# Check socket pool status
ls -la /tmp/agentdeck-mcp-*.sock

# Check MCP logs
tail ~/.agent-deck/logs/mcppool/*.log
```

### High CPU
```bash
# Check for runaway logs
du -sh ~/.agent-deck/logs/*

# Force cleanup
AGENTDECK_DEBUG=1 agent-deck status
```

### Profile issues
```bash
# Check profile storage
cat ~/.agent-deck/profiles/default/sessions.json | jq '.instances | length'

# Verify backup integrity
cat ~/.agent-deck/profiles/default/sessions.json.bak | jq '.'
```

---

## Development

### Build
```bash
make build
# Output: ./build/agent-deck
```

### Test
```bash
make test
go test ./internal/session/... -v
go test ./internal/ui/... -v -run TestSpecific
```

### Debug Mode
```bash
AGENTDECK_DEBUG=1 agent-deck
```

### Common Test Issues
- Missing `testmain_test.go` → Test data overwrites production
- Profile not `_test` → Cross-profile contamination
