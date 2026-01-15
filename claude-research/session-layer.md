# Session Data Layer - Deep Engineering Research

**Date:** 2026-01-15
**Agent:** Explore

## Executive Summary

The session package manages the **full lifecycle** of terminal sessions running AI agents (Claude, Gemini, custom tools) with:

1. **Session Lifecycle Management** - Create, start, restart, fork, kill with intelligent command building
2. **Multi-Tool Support** - Claude/Gemini built-in, extensible framework for custom tools (Vibe, Aider, etc.)
3. **Crash-Safe Storage** - Atomic writes with 3-generation rolling backups and auto-recovery
4. **Hierarchical Groups** - Path-based organization with sub-sessions and parent-child relationships
5. **Reliable Session ID Capture** - Capture-resume pattern ensures we always track actual session IDs
6. **MCP Integration** - Tracks loaded MCPs for sync detection, supports socket pooling

---

## L2 Architecture: Core Data Structures

### Instance Struct (instance.go)

```go
type Instance struct {
    // Identity (immutable after creation)
    ID          string    // UUID, never changes
    Title       string    // User-visible name
    ProjectPath string    // Working directory
    GroupPath   string    // Hierarchical group (e.g., "projects/backend")

    // Configuration
    Command     string    // Full command or tool shorthand
    Tool        string    // Detected tool: claude, gemini, opencode, codex, custom
    Status      Status    // Current status (enum)

    // AI Integration
    ClaudeSessionID   string    // From CLAUDE_SESSION_ID tmux env
    GeminiSessionID   string    // From GEMINI_SESSION_ID tmux env
    ClaudeDetectedAt  time.Time // When session ID was detected
    GeminiDetectedAt  time.Time

    // Sub-Sessions
    ParentSessionID   string    // Parent session for hierarchical tasks

    // MCP Tracking
    LoadedMCPNames    []string  // MCPs active when session started

    // Internal State
    tmuxSession       *tmux.Session  // Cached tmux handle
    startGracePeriod  time.Time      // 5s grace after start
    lastErrorCheck    time.Time      // Ghost session optimization
    skipStatusUpdate  bool           // Temporary skip flag

    CreatedAt         time.Time
    UpdatedAt         time.Time
}
```

### Status Enum (5 states)

```go
type Status string

const (
    StatusRunning  Status = "running"   // GREEN - Agent actively processing
    StatusWaiting  Status = "waiting"   // YELLOW - Stopped, unacknowledged
    StatusIdle     Status = "idle"      // GRAY - Stopped, user acknowledged
    StatusError    Status = "error"     // RED - Session doesn't exist
    StatusStarting Status = "starting"  // 5s grace period during tmux init
)
```

### GroupTree (Hierarchical Organization)

```go
type GroupTree struct {
    groups    map[string]*Group  // path → Group
    instances []*Instance
}

type Group struct {
    Name     string   // Display name
    Path     string   // Full path (e.g., "projects/backend")
    Expanded bool     // UI state
    Order    int      // Sort order
}
```

**Path-based hierarchy:**
- `"projects"` - Root group
- `"projects/backend"` - Child group
- `"projects/backend/api"` - Grandchild group

**Key Methods:**
- `CreateGroup(name)` - Create root group
- `CreateSubgroup(parentPath, name)` - Create child group
- `DeleteGroup(path)` - Remove group (fails if has sessions)
- `RenameGroup(path, newName)` - Rename preserving path structure
- `Flatten()` - Convert tree to ordered list for UI rendering

### Storage System (Atomic Persistence)

```go
type Storage struct {
    profile     string
    storagePath string  // ~/.agent-deck/profiles/{profile}/sessions.json
}
```

**File Structure:**
```json
{
  "instances": [
    {
      "id": "abc123...",
      "title": "My Project",
      "project_path": "/path/to/project",
      "group_path": "projects",
      "command": "claude",
      "tool": "claude",
      "status": "waiting",
      "claude_session_id": "sess_...",
      "loaded_mcp_names": ["exa", "memory"]
    }
  ],
  "groups": [
    {"name": "Projects", "path": "projects", "expanded": true, "order": 0}
  ]
}
```

**Atomic Write Pattern:**
```
1. Write to sessions.json.tmp
2. fsync() - Ensure data on disk
3. Backup rotation: .bak → .bak.1 → .bak.2
4. Atomic rename: .tmp → sessions.json
```

**Recovery:**
```
Load() fails → Try .bak → Try .bak.1 → Try .bak.2 → Empty state
```

---

## L3 Implementation: Critical Algorithms

### Session Lifecycle: Start → UpdateStatus → Restart → Kill

#### Start() - Command Building & Execution

```go
func (i *Instance) Start() error {
    // 1. Determine tool type
    tool := i.DetectTool()

    // 2. Build command based on tool
    var cmd string
    switch tool {
    case "claude":
        cmd = i.buildClaudeCommand()
    case "gemini":
        cmd = i.buildGeminiCommand()
    default:
        cmd = i.buildGenericCommand()
    }

    // 3. Capture loaded MCPs for sync detection
    i.LoadedMCPNames = i.getCurrentMCPNames()

    // 4. Create tmux session
    tmux.NewSession(i.TmuxName(), i.ProjectPath, cmd)

    // 5. Enter grace period
    i.startGracePeriod = time.Now().Add(5 * time.Second)
    i.Status = StatusStarting
}
```

#### buildClaudeCommand() - Capture-Resume Pattern

**Why NOT `--session-id`?**

Claude's `--session-id` flag only works for RESUMING existing sessions. It does NOT work for creating NEW sessions - Claude ignores the passed ID.

**Solution: Capture-Resume Pattern**
```bash
# 1. Run Claude with minimal prompt to capture session ID
session_id=$(claude -p "." --output-format json 2>/dev/null | jq -r '.session_id')

# 2. Store in tmux environment (persistent)
if [ -n "$session_id" ] && [ "$session_id" != "null" ]; then
  tmux set-environment CLAUDE_SESSION_ID "$session_id"
  claude --resume "$session_id" --dangerously-skip-permissions
else
  # Fallback: start fresh (jq not installed or capture failed)
  claude --dangerously-skip-permissions
fi
```

**Key Insight:** tmux environment is the **single source of truth** for session ID. It persists across app restarts.

#### UpdateStatus() - Grace Period & Ghost Optimization

```go
func (i *Instance) UpdateStatus() {
    // 1. Grace period (5s after start)
    if time.Now().Before(i.startGracePeriod) {
        return  // Keep StatusStarting
    }

    // 2. Ghost session optimization (30s recheck)
    if i.Status == StatusError {
        if time.Since(i.lastErrorCheck) < 30*time.Second {
            return  // Skip expensive Exists() check
        }
        i.lastErrorCheck = time.Now()
    }

    // 3. Check tmux session exists
    if !i.Exists() {
        i.Status = StatusError
        return
    }

    // 4. Get status from tmux
    status := tmux.GetStatus(i.TmuxName())
    switch status {
    case "active":
        i.Status = StatusRunning
    case "waiting":
        i.Status = StatusWaiting
    case "idle":
        i.Status = StatusIdle
    }
}
```

**Ghost Optimization:** Before this, 28 ghost sessions caused 74 subprocess/sec. After: ~5 subprocess/sec (30s recheck interval).

#### Restart() - Intelligent Strategy Tiers

```go
func (i *Instance) Restart() error {
    // 1. Regenerate .mcp.json (account for socket pool status)
    i.regenerateMCPConfig()

    // 2. Try respawn-pane (atomic, preserves session)
    if i.canRespawnPane() {
        cmd := i.buildRestartCommand()
        return tmux.RespawnPane(i.TmuxName(), cmd)
    }

    // 3. Fallback: recreate session
    i.Kill()
    return i.Start()
}

func (i *Instance) canRespawnPane() bool {
    // Claude/Gemini with known session ID
    if i.Tool == "claude" && i.ClaudeSessionID != "" {
        return true
    }
    if i.Tool == "gemini" && i.GeminiSessionID != "" {
        return true
    }
    // Custom tool with resume support
    if i.hasResumeSupport() {
        return true
    }
    return false
}
```

### Session ID Detection (Single Source of Truth)

#### UpdateClaudeSession() - tmux Environment Only

```go
func (i *Instance) UpdateClaudeSession() {
    // ONLY read from tmux environment
    // Capture-resume pattern ALWAYS sets CLAUDE_SESSION_ID

    sessionID := i.GetSessionIDFromTmux()
    if sessionID != "" {
        i.ClaudeSessionID = sessionID
        i.ClaudeDetectedAt = time.Now()
    }
}

func (i *Instance) GetSessionIDFromTmux() string {
    return tmux.GetEnvironment(i.TmuxName(), "CLAUDE_SESSION_ID")
}
```

**Key Design Decision:** No file scanning fallback. tmux environment is authoritative.

#### WaitForClaudeSession() - Polling with Timeout

```go
func (i *Instance) WaitForClaudeSession(timeout time.Duration) string {
    deadline := time.Now().Add(timeout)
    for time.Now().Before(deadline) {
        if id := i.GetSessionIDFromTmux(); id != "" {
            return id
        }
        time.Sleep(50 * time.Millisecond)
    }
    return ""  // Timeout - start fresh session
}
```

### Gemini Integration: HashProjectPath()

```go
func HashProjectPath(projectPath string) string {
    // Resolve symlinks (macOS: /tmp → /private/tmp)
    absPath, _ := filepath.EvalSymlinks(projectPath)
    absPath, _ = filepath.Abs(absPath)

    // SHA256 hash matches Gemini CLI
    hash := sha256.Sum256([]byte(absPath))
    return hex.EncodeToString(hash[:])
}
```

**Session Discovery:**
```go
func ListGeminiSessions(projectPath string) []GeminiSession {
    hash := HashProjectPath(projectPath)
    chatDir := filepath.Join(os.Getenv("HOME"), ".gemini/tmp", hash, "chats")

    // Scan session-*.json files
    files, _ := filepath.Glob(filepath.Join(chatDir, "session-*.json"))
    // Parse JSON, extract metadata
}
```

### Custom Tool Support (buildGenericCommand)

```go
func (i *Instance) buildGenericCommand() string {
    toolDef := GetToolDefinition(i.Tool)

    // No resume support - just run command
    if toolDef.ResumeFlag == "" || toolDef.SessionIDEnv == "" {
        cmd := toolDef.Command
        if toolDef.DangerousMode && toolDef.DangerousFlag != "" {
            cmd += " " + toolDef.DangerousFlag
        }
        return cmd
    }

    // Capture-resume pattern (same as Claude)
    return fmt.Sprintf(`
        session_id=$(%s %s "." 2>/dev/null | jq -r '%s' 2>/dev/null) || session_id=""
        if [ -n "$session_id" ] && [ "$session_id" != "null" ]; then
            tmux set-environment %s "$session_id"
            %s %s "$session_id" %s
        else
            %s %s
        fi
    `, toolDef.Command, toolDef.OutputFormatFlag, toolDef.SessionIDJsonPath,
       toolDef.SessionIDEnv,
       toolDef.Command, toolDef.ResumeFlag, dangerousFlag,
       toolDef.Command, dangerousFlag)
}
```

---

## L4 Scale & Performance Analysis

| Metric | Value | Notes |
|--------|-------|-------|
| **Subprocess spawning** | 0.5/sec (optimized) | Was 30/sec, cache + 2s TTL |
| **Status update latency** | <100ms | Hash comparison, cached errors |
| **Start latency (Claude)** | 3-5s | Capture phase + tmux init |
| **Restart latency** | 1-2s | respawn-pane (atomic) |
| **Fork latency** | 10-15s | Capture + resume phases |
| **Ghost session recheck** | 30s interval | From 500ms to 30s |
| **Storage file size** | ~100MB max | Atomic write handles this |
| **Backup generations** | 3 (rolling) | Auto-recovery on corruption |
| **Profile overhead** | <1KB | Empty profile ~200 bytes |

### Key Optimization: Ghost Session Handling

**Before:** 28 ghost sessions = 74 subprocess/sec (expensive Exists() every tick)
**After:** 30s recheck interval = ~5 subprocess/sec
**Trade-off:** User recovers within 30s, minimal spam

---

## L5 Extension Points

### Adding New Tools (Like Vibe, Aider)

**Configuration (config.toml):**
```toml
[tools.vibe]
command = "vibe"
icon = "🎵"
busy_patterns = ["executing", "running tool"]
prompt_patterns = ["approve?", "[y/n]"]
detect_patterns = ["mistral vibe", "██████████"]
resume_flag = "--resume"
session_id_env = "VIBE_SESSION_ID"
session_id_json_path = ".session_id"
output_format_flag = "--output-format json"
dangerous_flag = "--auto-approve"
dangerous_mode = true
```

**No code changes required!** Framework handles:
- Capture-resume pattern
- Status detection (busy/prompt patterns)
- Restart with resume
- Session ID persistence

### Adding New Profiles

```bash
agent-deck -p research add -t "ML Exp" -c claude /tmp/ml
agent-deck -p work list
```

**Automatic:** Storage at `~/.agent-deck/profiles/{profile}/sessions.json`

### Sub-Sessions (Parent-Child)

```go
child := NewInstanceWithTool("Sub-task", parent.ProjectPath, "claude")
child.SetParent(parent.ID)
```

**UI:** ParentSessionID field, Flatten() handles indentation

### Custom Response Parsers

Add tool type to `GetLastResponse()`, implement parser for tool's output format.

---

## Design Decisions & Rationale

| Decision | Why | Trade-off |
|----------|-----|-----------|
| **Capture-Resume vs --session-id** | Claude ignores passed ID for new sessions | Requires 2-phase execution |
| **tmux Env for Session ID** | Persistent, atomic, single source of truth | Requires tmux integration |
| **Atomic Write Pattern** | Crash safety (fsync + rename) | Slower writes |
| **30s Ghost Recheck** | Balance: recover within 30s vs minimize CPU | Occasional stale status |
| **5s Startup Grace** | Smooth UX, prevent false negatives | Delays actual status by 5s |
| **Dedup Claude IDs** | Defensive for migrated sessions | Oldest session keeps ID |

---

## Key Files Reference

| File | LOC | Purpose |
|------|-----|---------|
| instance.go | 1,644 | Session struct, lifecycle, command building |
| groups.go | 400+ | GroupTree hierarchy, flatten algorithm |
| storage.go | 300+ | JSON persistence, atomic writes, backup rotation |
| config.go | 200+ | Profile management |
| userconfig.go | 400+ | TOML config, tool definitions |
| discovery.go | 150+ | Import existing tmux sessions |
| gemini.go | 300+ | Gemini CLI integration |
| claude.go | 200+ | Claude session ID detection |

---

## Design Patterns Summary

| Pattern | Where Used | Purpose |
|---------|------------|---------|
| **Capture-Resume** | Claude/Gemini/Generic start | Reliable session ID capture |
| **Atomic Write** | Storage.Save() | Crash-safe persistence |
| **Lazy Loading** | tmuxSession field | Avoid unnecessary subprocess calls |
| **Cache + TTL** | Ghost session recheck | Reduce polling overhead |
| **Tool Polymorphism** | buildXxxCommand() | Support any CLI tool |
| **Grace Period** | StatusStarting | Prevent UI flicker on start |
