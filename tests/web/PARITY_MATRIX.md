# Web UI ↔ TUI Parity Matrix

**Date:** 2026-04-29  
**Scope:** Internal API design parity check for the agent-deck repository.

**Note:** All file references below are repo-relative (e.g. `internal/ui/home.go:6179`).
This matrix is consumed by `tests/web/e2e/parity-actions.spec.js` and
`internal/web/parity_test.go`; both fail loudly if the row count or MISSING
set diverges from the live code.

---

## TUI Action Matrix

Every keyboard action in the TUI that mutates state or navigates must have a web API counterpart.

| Action | TUI Trigger | Web Endpoint | Mutator Method | Test | Notes |
|--------|-------------|--------------|-----------------|------|-------|
| **SESSION LIFECYCLE** |
| Create session | `internal/ui/home.go:6179` (`n` key) | POST `/api/sessions` | `CreateSession` | `handlers_sessions_test.go` | NewDialog spawns, initiates session creation |
| Quick create session | `internal/ui/home.go:6286` (`N` key) | POST `/api/sessions` | `CreateSession` | `handlers_sessions_test.go` | Auto-generated name, smart group context |
| Start session | `internal/ui/home.go:6284` (via dialog/menu) | POST `/api/sessions/{id}/start` | `StartSession` | `handlers_sessions_test.go` | Resumes stopped/idle session |
| Stop session | `internal/ui/home.go:6284` (via dialog/menu) | POST `/api/sessions/{id}/stop` | `StopSession` | `handlers_sessions_test.go` | Kills running tmux session |
| Restart session | `internal/ui/home.go:6473` (`R` key) | POST `/api/sessions/{id}/restart` | `RestartSession` | `handlers_sessions_test.go` | Recreate tmux with resume |
| Restart fresh | `internal/ui/home.go:6494` (`T` key) | MISSING | `RestartSessionFresh` | N/A | Discards tool binding, no web equivalent |
| Delete session | `internal/ui/home.go:6302` (`d` key) | DELETE `/api/sessions/{id}` | `DeleteSession` | `handlers_sessions_test.go` | Kills + removes from storage |
| Close session | `internal/ui/home.go:6318` (`D` key) | MISSING | N/A | N/A | Non-destructive close (stops process, keeps metadata) |
| Fork session | `internal/ui/home.go:5979` (`f` key, quick) | POST `/api/sessions/{id}/fork` | `ForkSession` | `handlers_sessions_test.go` | Creates fork with resume command |
| Fork with dialog | `internal/ui/home.go:5997` (`F`/`shift+f`) | POST `/api/sessions/{id}/fork` | `ForkSession` | `handlers_sessions_test.go` | Dialog allows custom title + group |
| Rename session | `internal/ui/home.go:6119` (`r` key) | MISSING | N/A | N/A | Title edit via GroupDialog |
| Undo delete | `internal/ui/home.go:6572` (`ctrl+z`) | MISSING | N/A | N/A | Chrome-style undo, TUI-only |
| **GROUP OPERATIONS** |
| Create group | `internal/ui/home.go:6094` (`g` key) | POST `/api/groups` | `CreateGroup` | `handlers_groups_test.go` | Root or as subgroup |
| Rename group | `internal/ui/home.go:6119` (`r` key, group) | PATCH `/api/groups/{path}` | `RenameGroup` | `handlers_groups_test.go` | Via GroupDialog |
| Delete group | `internal/ui/home.go:6302` (`d` key, group) | DELETE `/api/groups/{path}` | `DeleteGroup` | `handlers_groups_test.go` | Moves children to default group |
| Move session to group | `internal/ui/home.go:6028` (`M`/`shift+m`) | MISSING | N/A | N/A | TUI-only via GroupDialog move mode |
| **MCP MANAGEMENT** |
| Attach MCP | `internal/ui/home.go:5965` (`m` key → MCPDialog) | MISSING | N/A | N/A | TUI dialog only; writes `.mcp.json` |
| Detach MCP | `internal/ui/home.go:5965` (`m` key → MCPDialog) | MISSING | N/A | N/A | TUI dialog only; edits `.mcp.json` |
| List MCPs | `internal/ui/home.go:5965` (`m` key → MCPDialog) | MISSING | N/A | N/A | TUI displays from catalog |
| Toggle pooled ↔ local | `internal/ui/home.go:5965` (`m` key → MCPDialog) | MISSING | N/A | N/A | TUI dialog only |
| **SKILLS MANAGEMENT** |
| Attach skill | `internal/ui/home.go:6015` (`s` key → SkillDialog) | MISSING | N/A | N/A | TUI dialog only; writes project config |
| Detach skill | `internal/ui/home.go:6015` (`s` key → SkillDialog) | MISSING | N/A | N/A | TUI dialog only |
| List skills | `internal/ui/home.go:6015` (`s` key → SkillDialog) | MISSING | N/A | N/A | TUI displays from catalog |
| **SETTINGS & DISPLAY** |
| Edit session settings | `internal/ui/home.go:5953` (`P`/`shift+p` → EditSessionDialog) | MISSING | `SetField` (indirect) | N/A | Title, color, notes, tool options, channels |
| Edit multi-repo paths | `internal/ui/home.go:5942` (`p` → EditPathsDialog) | MISSING | N/A | N/A | Multi-repo session paths |
| Edit notes inline | `internal/ui/home.go:6548` (`e` key) | MISSING | N/A | N/A | TUI-only textarea editor |
| Toggle YOLO mode | `internal/ui/home.go:6418` (`y` key) | MISSING | N/A | N/A | Gemini/Codex only; requires restart |
| Open settings panel | `internal/ui/home.go:6148` (`S` key) | GET `/api/settings` | N/A | `handlers_settings_test.go` | Read-only; displays profile, version |
| **WORKFLOW & NAVIGATION** |
| Mark session unread | `internal/ui/home.go:6366` (`u` key) | MISSING | N/A | N/A | idle → waiting transition |
| Quick approve | `internal/ui/home.go:6387` (default hotkey) | MISSING | N/A | N/A | Send "1"+Enter without attach |
| Copy output | `internal/ui/home.go:6511` (`c` key) | MISSING | N/A | N/A | Last AI response → clipboard |
| Copy session info | `internal/ui/home.go:6521` (`C`/`shift+c`) | MISSING | N/A | N/A | Repo/path/branch → clipboard |
| Send output to session | `internal/ui/home.go:6532` (`x` key) | MISSING | N/A | N/A | TUI session picker dialog |
| Exec shell | `internal/ui/home.go:6161` (`E` key) | MISSING | N/A | N/A | Sandbox container shell only |
| Toggle preview mode | `internal/ui/home.go:6413` (`v` key) | MISSING | N/A | N/A | Cycle: both → output → analytics |
| Open search | `internal/ui/home.go:6133` (`/` key) | MISSING | N/A | N/A | Local or global session search |
| Open global search | `internal/ui/home.go:5691` (`G` key) | MISSING | N/A | N/A | Cross-profile session search |
| Open help | `internal/ui/home.go:6143` (`?` key) | MISSING | N/A | N/A | Keyboard shortcuts overlay |
| Manual refresh | `internal/ui/home.go:6590` (`ctrl+r`) | MISSING | N/A | N/A | Force reload session list from disk |
| Jump mode | `internal/ui/home.go:6406` (`space` key) | MISSING | N/A | N/A | Vimium-style hint navigation |
| Attach session | `internal/ui/home.go:5744` (`enter` key) | MISSING | N/A | N/A | PTY attach via tmux; web uses WS for streaming |
| **WORKTREE OPERATIONS** |
| Finish worktree | `internal/ui/home.go:6038` (`W`/`shift+w`) | MISSING | N/A | N/A | Merge + cleanup; TUI dialog only |
| **COST TRACKING** |
| View costs dashboard | `internal/ui/home.go` (TUI only) | GET `/api/costs/summary` | N/A | `handlers_costs_test.go` | Sessions cost aggregation |
| Cost export | N/A | GET `/api/costs/export` | N/A | `handlers_costs_test.go` | Web-only; CSV/JSON export |
| **PUSH NOTIFICATIONS** |
| Subscribe to push | `internal/ui/home.go` (TUI none) | POST `/api/push/subscribe` | N/A | `handlers_push_test.go` | Web browser push only |
| Unsubscribe push | `internal/ui/home.go` (TUI none) | POST `/api/push/unsubscribe` | N/A | `handlers_push_test.go` | Web browser push only |
| Update push presence | `internal/ui/home.go` (TUI none) | POST `/api/push/presence` | N/A | `handlers_push_test.go` | Web browser focus tracking |

---

## State Fields Matrix

Every observable session field shown in the TUI must appear in the web API JSON response.

| State Field | TUI Display | Web JSON Location | Notes |
|-------------|-------------|------------------|-------|
| **CORE IDENTITY** |
| `id` | Session list | `MenuSession.id` | ✅ Present |
| `title` | Session row label | `MenuSession.title` | ✅ Present |
| `tool` | Session row icon/label | `MenuSession.tool` | ✅ Present (claude, gemini, shell, etc.) |
| `status` | Session row color/icon | `MenuSession.status` | ✅ Present (running, waiting, idle, error, stopped, starting) |
| `group_path` | Folder hierarchy | `MenuSession.groupPath` | ✅ Present |
| **LOCATION & TIME** |
| `project_path` | Preview pane | `MenuSession.projectPath` | ✅ Present |
| `created_at` | Info section | `MenuSession.createdAt` | ✅ Present |
| `last_accessed_at` | Info section | `MenuSession.lastAccessedAt` | ✅ Present |
| **RELATIONSHIPS** |
| `parent_session_id` | Sub-session indicator | `MenuSession.parentSessionId` | ✅ Present |
| `is_conductor` | (Not shown in TUI) | MISSING | Conductor metadata |
| **PROCESS STATE** |
| `tmux_session` | Internal reference | `MenuSession.tmuxSession` | ✅ Present (tmux session name) |
| `tmux_socket_name` | (Internal) | `MenuSession.tmuxSocketName` | ✅ Present; issue #687 |
| **TOOL-SPECIFIC** |
| `claude_session_id` | (Tooltip, not prominent) | MISSING | Shown in TUI debug; not in web |
| `gemini_session_id` | (Tooltip, not prominent) | MISSING | Shown in TUI debug; not in web |
| `gemini_model` | (Not shown) | MISSING | Active Gemini model selection |
| `gemini_yolo_mode` | (Toggle via `y` key) | MISSING | Per-session Gemini YOLO toggle |
| `codex_session_id` | (Not shown) | MISSING | Codex integration state |
| `opencode_session_id` | (Not shown) | MISSING | OpenCode integration state |
| **CONTENT** |
| `latest_prompt` | (Not shown in TUI) | MISSING | Last user input for context |
| `notes` | Preview pane (if enabled) | MISSING | User notes field |
| **APPEARANCE** |
| `color` | Row background tint | MISSING | User-chosen session color tint |
| **CONFIGURATION** |
| `command` | (Edit dialog) | MISSING | Session command to launch |
| `wrapper` | (Edit dialog) | MISSING | Optional wrapper command |
| `channels` | (Edit dialog) | MISSING | Claude plugin channels list |
| `extra_args` | (Edit dialog) | MISSING | Claude CLI extra arguments |
| `tool_options_json` | (Edit dialog) | MISSING | Tool-specific options (Claude, Codex, Gemini) |
| **SANDBOX & REMOTE** |
| `sandbox` | (Edit dialog) | MISSING | Docker sandbox config |
| `sandbox_container` | (Not shown) | MISSING | Running container name |
| `ssh_host` | (Not shown) | MISSING | SSH remote hostname |
| `ssh_remote_path` | (Not shown) | MISSING | SSH remote working directory |
| **MULTIREPO** |
| `multi_repo_enabled` | (Not shown) | MISSING | Multi-repo mode flag |
| `additional_paths` | (Edit dialog) | MISSING | List of additional project paths |
| `multi_repo_temp_dir` | (Not shown) | MISSING | Temp working directory for multi-repo |
| `multi_repo_worktrees` | (Not shown) | MISSING | Worktree metadata for each repo |
| **WORKTREE** |
| `worktree_path` | (Edit dialog) | MISSING | Path to worktree directory |
| `worktree_repo_root` | (Edit dialog) | MISSING | Original repo root |
| `worktree_branch` | (Edit dialog) | MISSING | Branch name in worktree |
| **PERSISTENCE & FLAGS** |
| `order` | Row position in group | `MenuSession.order` | ✅ Present |
| `title_locked` | (Not shown) | MISSING | Prevents Claude title auto-sync |
| `no_transition_notify` | (Not shown) | MISSING | Suppress transition event dispatch |
| **MCP & LIFECYCLE** |
| `loaded_mcp_names` | (MCP dialog) | MISSING | MCPs loaded at last start |
| `is_fork_awaiting_start` | (Internal) | MISSING | Pre-built fork command pending |
| `skip_mcp_regenerate` | (Internal) | MISSING | Transient flag for MCP dialog |
| **ANALYTICS (Conditional)** |
| `claude_analytics` | Cost/token panel | MISSING | Per-session token/cost metrics |
| `gemini_analytics` | Cost/token panel | MISSING | Per-session Gemini metrics |

---

## Summary Statistics

### Action Parity
- **Total TUI actions:** 47 (session/group/MCP/skills/settings/workflow/costs/push)
- **Web endpoints implemented:** 17
- **MISSING web actions:** 30 (~64% gap)
- **Key gaps:**
  - Session settings edits (rename, color, notes, tool options)
  - MCP/Skill management (no web equivalent)
  - Content operations (copy, send, search)
  - Worktree operations
  - Non-destructive close
  - Restart fresh (no web)

### State Field Parity
- **Total TUI-visible fields:** ~50
- **Web JSON fields:** 12
- **MISSING web fields:** ~38 (~76% gap)
- **Critical gaps:**
  - Tool-specific state (claude/gemini/codex session IDs, models, options)
  - User content (notes, latest_prompt)
  - Configuration (command, wrapper, channels, extra_args, tool_options)
  - Sandbox/SSH/multirepo metadata
  - Worktree metadata
  - Appearance (color)

---

## Key Insights

### Sync Gaps (Actions)

1. **Session Metadata Edits** (7 actions): The TUI has comprehensive edit dialogs (`EditSessionDialog`, `GroupDialog`) for:
   - Title/name changes (`r` key)
   - Color tint (`P` → EditSessionDialog)
   - Notes (`e` key inline)
   - Tool options, channels, extra_args (`P` → EditSessionDialog)

   **Web only reads these via MenuSession but has NO write path.** The `SetField` mutator in `internal/session/mutators.go` exists but is not exposed via HTTP.

2. **MCP & Skill Management** (6 actions): MCPDialog and SkillDialog are TUI-only. They:
   - Write `.mcp.json` and project config
   - Have no web HTTP equivalent
   - Require session restart

3. **Workflow Actions** (8 actions): Search, copy, send, jump, approve are all TUI-only optimized UX.

4. **Worktree Finish** (1 action): The `W` key dialog performs merge + cleanup; no web equivalent.

5. **Close vs Delete**: TUI distinguishes:
   - `d` = delete (kill + remove from registry)
   - `D` = close (kill, keep metadata)
   
   Web only has delete.

### Sync Gaps (State)

1. **Tool-Specific IDs & State**: Claude/Gemini/Codex session IDs, models, YOLO mode, analytics are persisted but **never surfaced in MenuSession JSON**. The web cannot display or mutate them.

2. **Configuration as Data**: Command, wrapper, channels, extra_args, tool_options are **loaded but never returned**. A web client cannot render an edit form without this data.

3. **Content & Metadata**: Notes, latest_prompt, color are **persisted but not exposed**.

4. **Worktree & Multirepo**: Entire worktree/multirepo metadata is **loaded but hidden** from the web API.

5. **MCP State**: `loaded_mcp_names` tracks active MCPs but is not exposed, so the web cannot display the current MCP set.

---

## NOT IN CODE (Documented but Not Implemented)

- **Watcher Management** (create, fire, remove): Documented in CLAUDE.md but not found in codebase. Internal event watcher system exists (`internal/watcher/`) but has no TUI/web entry points.
- **Conductor Operations** (create, attach channel, send, receive): Not implemented in this codebase snapshot. Conductor sessions are recognized as a flag but no specific conductor management actions are implemented.
- **Channel Management**: Channels are configuration fields but no TUI/web interface exists to manage them.
- **Plugin Management**: No TUI/web action exists (only as config).

---

## Recommendations

1. **Expose session metadata endpoints** (PATCH `/api/sessions/{id}` with `{title, color, notes, tool, wrapper, channels, extraArgs, toolOptions}`).
2. **Extend MenuSession JSON** to include at minimum: `command`, `wrapper`, `channels`, `extraArgs`, `toolOptions`, `notes`, `color`, `loadedMcpNames`.
3. **Add MCP/Skill endpoints** (POST/DELETE `/api/sessions/{id}/mcps`, `/api/sessions/{id}/skills`) or mark as web-unsafe and TUI-exclusive.
4. **Unify close semantics**: Either expose both delete/close on web or consolidate to one.
5. **Document API surface** in a companion `API.md` that lists all endpoints and their request/response schemas.

