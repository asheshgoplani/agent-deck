# CLI Commands - Deep Engineering Research

**Date:** 2026-01-15
**Agent:** Explore

## Executive Summary

The `cmd/agent-deck/` package implements Agent Deck's CLI with a sophisticated command dispatcher pattern supporting 5+ major command hierarchies (session, mcp, group, profile, etc). Core architectural elements include transparent session resolution (title/ID/path fuzzy matching), dual-output modes (human/JSON), and stateless command handlers that maintain profile isolation through storage abstraction. The design optimizes for scripting via structured JSON output while maintaining human-friendly CLI defaults.

---

## L2 Architecture: Command Structure & Data Flow

### Entry Point Architecture (main.go)

```
main.go:177-289
│
├─ extractProfileFlag() [291-322]
│  └─ Pre-processes global -p/--profile flag from args
│     Returns: (profile, remaining_args)
│
├─ Subcommand Switch [182-221]
│  ├─ add
│  ├─ list, ls
│  ├─ remove, rm
│  ├─ status
│  ├─ session (dispatches to session_cmd.go)
│  ├─ mcp (dispatches to mcp_cmd.go)
│  ├─ group (dispatches to group_cmd.go)
│  ├─ profile
│  ├─ update
│  ├─ uninstall
│  └─ version, help
│
├─ TUI Launch Path (no subcommand)
│  └─ acquireLock() [1368-1419]
│     └─ ui.NewHomeWithProfile() with Bubble Tea
│
└─ Lock Management
   ├─ acquireLock(): O_EXCL atomic file creation
   ├─ releaseLock(): cleanup on exit
   └─ Signal handling: SIGINT/SIGTERM
```

**Key Design Pattern**: Subcommands are fully isolated handlers. Each receives `(profile string, args []string)` and handles its own flag parsing, validation, and output.

### Subcommand Handler Hierarchy

**Session Commands (session_cmd.go)**
```
handleSession(profile, args)
├─ start       → handleSessionStart()
├─ stop        → handleSessionStop()
├─ restart     → handleSessionRestart()
├─ fork        → handleSessionFork()
├─ attach      → handleSessionAttach()
├─ show        → handleSessionShow()
├─ set         → handleSessionSet()
├─ send        → handleSessionSend()
├─ output      → handleSessionOutput()
├─ current     → handleSessionCurrent()
├─ set-parent  → handleSessionSetParent()
└─ unset-parent → handleSessionUnsetParent()
```

**MCP Commands (mcp_cmd.go)**
```
handleMCP(profile, args)
├─ list       → handleMCPList()
├─ attached   → handleMCPAttached()
├─ attach     → handleMCPAttach()
└─ detach     → handleMCPDetach()
```

**Group Commands (group_cmd.go)**
```
handleGroup(profile, args)
├─ list   → handleGroupList()
├─ create → handleGroupCreate()
├─ delete → handleGroupDelete()
└─ move   → handleGroupMove()
```

**Top-level Commands (main.go)**
- `handleAdd()` - Create session with flags, MCP attachment
- `handleList()` - All sessions or all profiles
- `handleRemove()` - Delete session + kill tmux
- `handleStatus()` - Status summary (verbose/quiet/JSON)
- `handleProfile()` - Profile management
- `handleUpdate()` - Update checking + auto-upgrade
- `handleUninstall()` - Comprehensive cleanup

### Data Flow: Load → Process → Save

**Standard Pattern:**
```go
func loadSessionData(profile string) (*Storage, []*Instance, []GroupData, error) {
    // 1. Load
    storage := NewStorageWithProfile(profile)
    instances, groupsData := storage.LoadWithGroups()
    // Reconnects stale tmux sessions automatically

    return storage, instances, groupsData, nil
}

// 2. Process
inst := ResolveSession(instances, identifier)
inst.SomeModification()

// 3. Save
groupTree := NewGroupTreeWithGroups(instances, groupsData)
storage.SaveWithGroups(instances, groupTree)
```

### Output Handling

```go
type CLIOutput struct {
   jsonMode  bool
   quietMode bool
}

// Unified interface
func (c *CLIOutput) Success(message string, data interface{}) {
    if c.quietMode { return }
    if c.jsonMode {
        json.Encode(data)
    } else {
        fmt.Println("✓", message)
    }
}

func (c *CLIOutput) Error(message string, code string) {
    if c.jsonMode {
        json.Encode({"success": false, "error": message, "code": code})
    } else {
        fmt.Fprintln(os.Stderr, "✗", message)
    }
}
```

---

## L3 Implementation: Critical Patterns

### Session Resolution (Fuzzy Find Logic)

**ResolveSession() (cli_utils.go)**

```go
func ResolveSession(instances []*Instance, identifier string) (*Instance, string, int) {
    // Matching Priority:

    // 1. Exact Title Match (highest priority)
    for _, inst := range instances {
        if inst.Title == identifier {
            return inst, "", 0
        }
    }

    // 2. ID Prefix Match (min 6 chars)
    var idMatches []*Instance
    for _, inst := range instances {
        if strings.HasPrefix(inst.ID, identifier) && len(identifier) >= 6 {
            idMatches = append(idMatches, inst)
        }
    }
    if len(idMatches) == 1 {
        return idMatches[0], "", 0
    }
    if len(idMatches) > 1 {
        return nil, "matches multiple sessions", ErrCodeAmbiguous
    }

    // 3. Path Match
    for _, inst := range instances {
        if inst.ProjectPath == identifier {
            return inst, "", 0
        }
    }

    // 4. Not Found
    return nil, "session not found", ErrCodeNotFound
}
```

**ResolveSessionOrCurrent():**
- Falls back to current session if identifier empty
- Extracts from `AGENTDECK_SESSION_ID` env var
- Parses `agentdeck_<title>_<id>` naming format

### Flag Parsing with Position Arguments

**Challenge**: Go's `flag` package stops at first non-flag argument

```bash
# Bad: -c not parsed
agent-deck add . -c claude

# Good: parsed correctly
agent-deck add -c claude .
```

**Solution: reorderArgsForFlagParsing()**

```go
func reorderArgsForFlagParsing(args []string) []string {
    var flags, positional []string

    // Known value flags
    valueFlags := map[string]bool{
        "-t": true, "--title": true,
        "-g": true, "--group": true,
        "-c": true, "--cmd": true,
        "-p": true, "--parent": true,
        "--mcp": true,
    }

    i := 0
    for i < len(args) {
        if strings.HasPrefix(args[i], "-") {
            flags = append(flags, args[i])
            if valueFlags[args[i]] && i+1 < len(args) {
                i++
                flags = append(flags, args[i])
            }
        } else {
            positional = append(positional, args[i])
        }
        i++
    }

    return append(flags, positional...)
}
```

### Session Lifecycle: Start with Capture-Resume

**handleSessionStart()**

```go
func handleSessionStart(profile string, args []string) {
    // 1. Parse flags
    fs := flag.NewFlagSet("session start", flag.ExitOnError)
    jsonOutput := fs.Bool("json", false, "")
    message := fs.String("m", "", "Initial message")
    fs.Parse(args)

    // 2. Resolve session
    storage, instances, _, _ := loadSessionData(profile)
    inst, errMsg, errCode := ResolveSession(instances, fs.Arg(0))
    if inst == nil {
        exitWithError(errMsg, errCode)
    }

    // 3. Check not already running
    if inst.Exists() {
        exitWithError("session already running", ErrCodeInvalidOperation)
    }

    // 4. Start (with optional message)
    if *message != "" {
        inst.StartWithMessage(*message)
    } else {
        inst.Start()
    }

    // 5. Save and output
    storage.SaveWithGroups(instances, groupTree)
    outputSuccess("Session started", inst)
}
```

### MCP Management: Dual Scope (Local/Global)

**Attach Flow (mcp_cmd.go)**

```go
func handleMCPAttach(profile string, args []string) {
    // 1. Parse flags
    global := fs.Bool("global", false, "Attach globally")
    restart := fs.Bool("restart", false, "Restart after attach")

    // 2. Verify MCP exists in config.toml
    availableMCPs := session.GetAvailableMCPs()
    if _, ok := availableMCPs[mcpName]; !ok {
        exitWithError("MCP not available", ErrCodeMCPNotAvailable)
    }

    // 3. Choose scope and write
    if *global {
        session.WriteGlobalMCP(mcpNames)  // Claude config
    } else {
        session.WriteMCPJsonFromConfig(projectPath, mcpNames)  // .mcp.json
    }

    // 4. Optional restart
    if *restart {
        inst.Restart()
        time.Sleep(2 * time.Second)
        // Auto-send "continue" to preserve conversation
    }

    outputSuccess("MCP attached", result)
}
```

**Scope Decision Tree:**
- `--global` flag → Update Claude/Gemini global config
- No flag → Update `.mcp.json` in project root
- Gemini: Always global (no project-level support)

### Profile Isolation

```go
extractProfileFlag(args)
   │
   └─ Returns (profile, remainingArgs)
      │
      └─ Passed to all handlers: handleAdd(profile, args)
         │
         ├─ NewStorageWithProfile(profile)
         │  └─ ~/.agent-deck/profiles/{profile}/
         │
         ├─ Session mutations scoped to profile
         │
         └─ SaveWithGroups() writes to profile's sessions.json
```

**Profile Detection (session current command):**
```go
// Priority chain
1. Explicit profileArg if provided
2. Auto-detect via profile.DetectCurrentProfile()
   ├─ Checks AGENTDECK_PROFILE env
   ├─ Parses CLAUDE_CONFIG_DIR (~/.claude-work → work)
   └─ Falls back to config default
3. Search all profiles if not found
```

### JSON Output Contract

**Success Response:**
```json
{
  "success": true,
  "id": "abc123...",
  "title": "My Project",
  "status": "running",
  "tmux": "agentdeck_my-project_abc123..."
}
```

**Error Response:**
```json
{
  "success": false,
  "error": "session not found",
  "code": "NOT_FOUND"
}
```

**Status Codes:**
- `NOT_FOUND`
- `ALREADY_EXISTS`
- `AMBIGUOUS`
- `INVALID_OPERATION`
- `GROUP_NOT_EMPTY`
- `MCP_NOT_AVAILABLE`

---

## L4 Scale & Performance: Scripting & Automation

### Automation Use Cases

**Batch Session Creation:**
```bash
for dir in ~/projects/*; do
  agent-deck add "$dir" -c claude
done
```

**Scripted Status Polling:**
```bash
# Quiet mode: just waiting count
waiting=$(agent-deck status -q)

# JSON mode: structured data
agent-deck status --json | jq '.waiting'
```

**MCP Attachment Workflow:**
```bash
# Attach MCPs to all sessions in a group
for session_id in $(agent-deck list --json | jq -r '.[] | select(.group=="research") | .id'); do
  agent-deck mcp attach "$session_id" sequential-thinking --restart
done
```

**Profile Switching:**
```bash
# Export sessions from one profile to another
agent-deck -p default list --json | jq -r '.[] | "\(.title) \(.path)"' | while read title path; do
  agent-deck -p work add -t "$title" "$path"
done
```

### JSON Output for Integration

```bash
# Find all sessions with Claude tool
agent-deck list --json | jq '.[] | select(.tool == "claude") | .id'

# Get MCP info for a session
agent-deck session show my-project --json | jq '.mcps'

# Check if session can fork (Claude-specific)
agent-deck session show my-project --json | jq '.can_fork'
```

**Error Handling in Scripts:**
```bash
# Exit codes matter
agent-deck session start nonexistent-session
echo $?  # 2 = NOT_FOUND, 1 = other error, 0 = success

# JSON error format
agent-deck session start nonexistent --json
# {"success":false, "error":"...", "code":"NOT_FOUND"}
```

### Performance Characteristics

**Session Resolution:**
- O(n) linear scan for exact title match
- O(n) for ID prefix (min 6 chars, early exit on ambiguous)
- O(1) failure if no match in all scans

**Batch Operations:**
- No transaction support: each save() is independent
- Recommend: load all → modify all → save once
- N sessions × log(N) load time due to tmux reconnection

---

## L5 Extension Points: Adding Commands

### Adding a New Subcommand (e.g., `session analyze`)

**Step 1: Add Handler in session_cmd.go**
```go
// In handleSession() dispatcher
case "analyze":
    handleSessionAnalyze(profile, args[1:])

// New handler function
func handleSessionAnalyze(profile string, args []string) {
    fs := flag.NewFlagSet("session analyze", flag.ExitOnError)
    jsonOutput := fs.Bool("json", false, "Output as JSON")
    fs.Parse(args)

    // Load → Process → Save pattern
    storage, instances, _, err := loadSessionData(profile)
    // ... analyze

    out := NewCLIOutput(*jsonOutput, quietMode)
    out.Success(message, jsonData)
}
```

**Step 2: Add help text in printSessionHelp()**
```go
fmt.Println("  analyze <id>            Analyze session performance")
```

### Adding a New Top-Level Command (e.g., `import`)

**Step 1: Add case in main() dispatcher**
```go
case "import":
    handleImport(profile, args[1:])
    return
```

**Step 2: Implement handler**
```go
func handleImport(profile string, args []string) {
    fs := flag.NewFlagSet("import", flag.ExitOnError)
    source := fs.String("from", "", "Import source")

    fs.Parse(args)

    storage, instances, groups, _ := loadSessionData(profile)
    instances = append(instances, newSessions...)

    groupTree := session.NewGroupTreeWithGroups(instances, groups)
    storage.SaveWithGroups(instances, groupTree)

    fmt.Printf("✓ Imported %d sessions\n", len(newSessions))
}
```

**Step 3: Update help**
```go
// In printHelp()
fmt.Println("  import              Import sessions from external source")
```

### Adding Output Modes (e.g., CSV, YAML)

```go
type CLIOutput struct {
    jsonMode  bool
    quietMode bool
    csvMode   bool    // NEW
    yamlMode  bool    // NEW
}

func (c *CLIOutput) Print(humanOutput string, jsonData interface{}) {
    if c.quietMode { return }
    if c.csvMode {
        c.printCSV(jsonData)
        return
    }
    // ... existing modes
}
```

---

## File Structure Summary

```
cmd/agent-deck/
├── main.go (1805 lines)
│   ├─ Root dispatcher
│   ├─ Top-level handlers: add, list, remove, status
│   ├─ Profile management
│   ├─ Update & uninstall
│   ├─ Utilities: flag extraction, duplicates
│   └─ Lock management
│
├── session_cmd.go (1445 lines)
│   ├─ Dispatcher
│   ├─ Handlers: start, stop, restart, fork, attach
│   ├─ Handlers: show, set, output, current
│   ├─ Sub-session handlers: set-parent, unset-parent
│   ├─ Message sending & response extraction
│   └─ Helpers: loadSessionData, waitForAgentReady
│
├── mcp_cmd.go (616 lines)
│   ├─ Dispatcher
│   ├─ Handlers: list, attached, attach, detach
│   ├─ MCP scope management (local vs global)
│   └─ MCP cache invalidation
│
├── group_cmd.go (643 lines)
│   ├─ Dispatcher
│   ├─ Handlers: list, create, delete, move
│   ├─ Status aggregation (running/waiting/idle)
│   └─ Path utilities
│
└── cli_utils.go (250 lines)
    ├─ CLIOutput interface
    ├─ Session resolution
    ├─ Status & ID formatting
    └─ Error codes
```

---

## Summary Table: Architecture Layers

| Layer | Component | Pattern | Key Files |
|-------|-----------|---------|-----------|
| **L1** | Command Dispatch | Subcommand switch + handler isolation | main.go |
| **L2** | Data Flow | Load → Process → Save with validation | session_cmd.go, cli_utils.go |
| **L3** | Session Resolution | Fuzzy matching (title/ID/path) with priority | cli_utils.go |
| **L3** | Output Formatting | Dual human/JSON via CLIOutput interface | cli_utils.go |
| **L3** | Profile Isolation | All operations scoped to profile | main.go (extractProfileFlag) |
| **L4** | Scripting Support | Exit codes, JSON output, quiet mode | All handlers |
| **L5** | Extensibility | Handler template, flag parsing pattern | All files |
