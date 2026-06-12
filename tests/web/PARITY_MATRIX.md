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
| Close session | `internal/ui/home.go:6318` (`D` key) | POST `/api/sessions/{id}/close` | `CloseSession` | `handlers_sessions_test.go`, `tests/web/e2e/close-undo.spec.js` | Non-destructive close (stops process, keeps metadata); Shift+D in web UI |
| Archive session | `internal/ui/home.go` (`A` key) | POST `/api/sessions/{id}/archive` | `ArchiveSession` | `handlers_sessions_test.go`, `tests/e2e/session-lifecycle.spec.ts` | Stops tmux/agent then hides from active lists (`archived_at`); web sidebar ⌂ button |
| Unarchive session | `internal/ui/home.go` (`shift+u` in archived view) | POST `/api/sessions/{id}/unarchive` | `UnarchiveSession` | `handlers_sessions_test.go`, `tests/e2e/session-lifecycle.spec.ts` | Clears `archived_at`; does not auto-start |
| View archived sessions | `internal/ui/home.go` (`^` filter) | GET `/api/sessions/archived` | `LoadArchivedMenuSnapshot` | `handlers_sessions_test.go`, `tests/e2e/session-lifecycle.spec.ts` | Web Archived tab + TUI archived-only list |
| Fork session | `internal/ui/home.go` (`f` key, quick) | POST `/api/sessions/{id}/fork` | `ForkSession` | `handlers_sessions_test.go`, WebUI action tests | Web creates a plain tool-native fork; TUI quick fork also applies `[fork]` defaults |
| Fork with dialog | `internal/ui/home.go` (`F`/`shift+f`) | MISSING | N/A | N/A | Shift+F title/group/branch/worktree controls are TUI-only until Web gets a dedicated async fork workflow |
| Rename session | `internal/ui/home.go:6119` (`r` key) | MISSING | N/A | N/A | Title edit via GroupDialog |
| Undo delete | `internal/ui/home.go:6572` (`ctrl+z`) | POST `/api/sessions/undelete` | `UndoDelete` | `handlers_sessions_test.go`, `tests/web/e2e/close-undo.spec.js` | Chrome-style undo within 30s window (web.DefaultUndoWindow); Ctrl+Z in web UI |
| **GROUP OPERATIONS** |
| Create group | `internal/ui/home.go:6094` (`g` key) | POST `/api/groups` | `CreateGroup` | `handlers_groups_test.go` | Root or as subgroup |
| Rename group | `internal/ui/home.go:6119` (`r` key, group) | PATCH `/api/groups/{path}` | `RenameGroup` | `handlers_groups_test.go` | Via GroupDialog |
| Delete group | `internal/ui/home.go:6302` (`d` key, group) | DELETE `/api/groups/{path}` | `DeleteGroup` | `handlers_groups_test.go` | Moves children to default group |
| Move session to group | `internal/ui/home.go:6028` (`M`/`shift+m`) | MISSING | N/A | N/A | TUI-only via GroupDialog move mode |
| **MCP MANAGEMENT** |
| Attach MCP | `internal/ui/home.go:5965` (`m` key → MCPDialog) | POST `/api/sessions/{id}/mcps/{name}` | `MCPManager.Attach` | `handlers_mcps_test.go` | Body `{scope?}`; default scope=local; writes `.mcp.json` via session helpers |
| Detach MCP | `internal/ui/home.go:5965` (`m` key → MCPDialog) | DELETE `/api/sessions/{id}/mcps/{name}` | `MCPManager.Detach` | `handlers_mcps_test.go` | Body `{scope?}`; scope auto-detected if omitted |
| List MCPs | `internal/ui/home.go:5965` (`m` key → MCPDialog) | GET `/api/sessions/{id}/mcps` | `MCPManager.ListAttached` | `handlers_mcps_test.go` | Returns `{local,global,user}`; catalog at GET `/api/mcps` |
| Toggle pooled ↔ local | `internal/ui/home.go:5965` (`m` key → MCPDialog) | PATCH `/api/sessions/{id}/mcps/{name}` | `MCPManager.Move` | `handlers_mcps_test.go` | Body `{scope}` or `{pooled:bool}`; pooled=true→global, pooled=false→local |
| **SKILLS MANAGEMENT** |
| Attach skill | `internal/ui/home.go:6015` (`s` key → SkillDialog) | POST `/api/sessions/{id}/skills/{name}` | `apiFetch('POST', …)` from `SkillsPane.js` | `tests/web/e2e/skills.spec.js` | Wired via `web.SkillsService`; writes project config |
| Detach skill | `internal/ui/home.go:6015` (`s` key → SkillDialog) | DELETE `/api/sessions/{id}/skills/{name}` | `apiFetch('DELETE', …)` from `SkillsPane.js` | `tests/web/e2e/skills.spec.js` | Wired via `web.SkillsService` |
| List skills (catalog) | `internal/ui/home.go:6015` (`s` key → SkillDialog) | GET `/api/skills` | `SkillsPane.js` catalog column | `tests/web/e2e/skills.spec.js` | Mirrors `session.ListAvailableSkills` |
| List skills (attached) | `internal/ui/home.go:6015` (`s` key → SkillDialog) | GET `/api/sessions/{id}/skills` | `SkillsPane.js` attached column | `tests/web/e2e/skills.spec.js` | Mirrors `session.GetAttachedProjectSkills(projectPath)` |
| **SETTINGS & DISPLAY** |
| Edit session settings | `internal/ui/home.go:5953` (`P`/`shift+p` → EditSessionDialog) | PATCH `/api/sessions/{id}` | `UpdateSession` (delegates to `session.SetField`) | `handlers_sessions_test.go` + `tests/web/e2e/edit-session.spec.js` | Title, color, notes, tool, extra-args, plugins, channels, skip-permissions, auto-mode. Returns `restartRequired` for restart-policy fields. Web UI: `EditSessionDialog.js` + Sidebar Edit button. |
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
| Finish worktree | `internal/ui/home.go:6038` (`W`/`shift+w`) | POST `/api/sessions/{id}/worktree/finish` | `FinishWorktree` | `issue1126_worktree_finish_test.go`, `tests/web/e2e/worktree-finish.spec.js` | Merge + cleanup; body accepts `into`, `noMerge`, `keepBranch`, `force` (mirrors CLI flags). Issue #1126. |
| **COST TRACKING** |
| View costs dashboard | `internal/ui/home.go` (TUI only) | GET `/api/costs/summary` | N/A | `handlers_costs_test.go` | Sessions cost aggregation. **e2e parity: degraded-only** — fixture omits the SQLite cost store, so the e2e probe asserts the documented 503 `UNAVAILABLE` response. Happy-path (200 + payload) coverage is `parity-test-deferred` to PR-B fixture wiring. |
| Cost export | N/A | GET `/api/costs/export` | N/A | `handlers_costs_test.go` | Web-only; CSV/JSON export. **e2e parity: degraded-only** (503 without cost store). Happy-path `parity-test-deferred` to PR-B. |
| **PUSH NOTIFICATIONS** |
| Subscribe to push | `internal/ui/home.go` (TUI none) | POST `/api/push/subscribe` | N/A | `handlers_push_test.go` | Web browser push only. **e2e parity: degraded-only** — fixture has no push service (no VAPID keys + subscription db), so the probe asserts 503 `PUSH_NOT_CONFIGURED`. Happy-path `parity-test-deferred` to PR-B. |
| Unsubscribe push | `internal/ui/home.go` (TUI none) | POST `/api/push/unsubscribe` | N/A | `handlers_push_test.go` | Web browser push only. **e2e parity: degraded-only** (503 without push service). Happy-path `parity-test-deferred` to PR-B. |
| Update push presence | `internal/ui/home.go` (TUI none) | POST `/api/push/presence` | N/A | `handlers_push_test.go` | Web browser focus tracking. **e2e parity: degraded-only** (503 without push service). Happy-path `parity-test-deferred` to PR-B. |

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
| `archived_at` | Archived list / filter | `MenuSession.archivedAt` | ✅ Present; non-zero when session is archived (omitempty on active menu) |
| **RELATIONSHIPS** |
| `parent_session_id` | Sub-session indicator | `MenuSession.parentSessionId` + `GET /api/sessions/{id}/children` | ✅ Present; tree endpoint surfaces full conductor child topology in the right-rail Children card (`internal/web/handlers_children.go`, `tests/web/e2e/children-panel.spec.js`) |
| `is_conductor` | (Not shown in TUI) | `MenuSession.isConductor` | ✅ Present; conductor metadata. Tree topology also surfaced at `GET /api/sessions/{id}/children` (kind derived UI-side from title/groupPath in `dataModel.js`) |
| **PROCESS STATE** |
| `tmux_session` | Internal reference | `MenuSession.tmuxSession` | ✅ Present (tmux session name) |
| `tmux_socket_name` | (Internal) | `MenuSession.tmuxSocketName` | ✅ Present; issue #687 |
| **TOOL-SPECIFIC** |
| `claude_session_id` | (Tooltip, not prominent) | `MenuSession.claudeSessionId` | ✅ Present |
| `gemini_session_id` | (Tooltip, not prominent) | `MenuSession.geminiSessionId` | ✅ Present |
| `gemini_model` | (Not shown) | `MenuSession.geminiModel` | ✅ Present; active Gemini model |
| `gemini_yolo_mode` | (Toggle via `y` key) | `MenuSession.geminiYoloMode` | ✅ Present; *bool, `&false` marshals as `false` |
| `codex_session_id` | (Not shown) | `MenuSession.codexSessionId` | ✅ Present |
| `opencode_session_id` | (Not shown) | `MenuSession.opencodeSessionId` | ✅ Present |
| **CONTENT** |
| `latest_prompt` | (Not shown in TUI) | `MenuSession.latestPrompt` | ✅ Present; last user input |
| `notes` | Preview pane (if enabled) | `MenuSession.notes` | ✅ Present |
| **APPEARANCE** |
| `color` | Row background tint | `MenuSession.color` | ✅ Present; lipgloss color spec |
| **CONFIGURATION** |
| `command` | (Edit dialog) | `MenuSession.command` | ✅ Present |
| `wrapper` | (Edit dialog) | `MenuSession.wrapper` | ✅ Present |
| `channels` | (Edit dialog) | `MenuSession.channels` | ✅ Present; Claude plugin channel ids |
| `extra_args` | (Edit dialog) | `MenuSession.extraArgs` | ✅ Present |
| `tool_options_json` | (Edit dialog) | `MenuSession.toolOptions` | ✅ Present; raw JSON tool-specific options |
| **SANDBOX & REMOTE** |
| `sandbox` | (Edit dialog) | `MenuSession.sandbox` | ✅ Present; Docker sandbox config |
| `sandbox_container` | (Not shown) | `MenuSession.sandboxContainer` | ✅ Present |
| `ssh_host` | (Not shown) | `MenuSession.sshHost` | ✅ Present |
| `ssh_remote_path` | (Not shown) | `MenuSession.sshRemotePath` | ✅ Present |
| **MULTIREPO** |
| `multi_repo_enabled` | (Not shown) | `MenuSession.multiRepoEnabled` | ✅ Present |
| `additional_paths` | (Edit dialog) | `MenuSession.additionalPaths` | ✅ Present |
| `multi_repo_temp_dir` | (Not shown) | `MenuSession.multiRepoTempDir` | ✅ Present |
| `multi_repo_worktrees` | (Not shown) | `MenuSession.multiRepoWorktrees` | ✅ Present |
| **WORKTREE** |
| `worktree_path` | (Edit dialog) | `MenuSession.worktreePath` | ✅ Present |
| `worktree_repo_root` | (Edit dialog) | `MenuSession.worktreeRepoRoot` | ✅ Present |
| `worktree_branch` | (Edit dialog) | `MenuSession.worktreeBranch` | ✅ Present |
| **PERSISTENCE & FLAGS** |
| `order` | Row position in group | `MenuSession.order` | ✅ Present |
| `title_locked` | (Not shown) | `MenuSession.titleLocked` | ✅ Present |
| `no_transition_notify` | (Not shown) | `MenuSession.noTransitionNotify` | ✅ Present |
| **MCP & LIFECYCLE** |
| `loaded_mcp_names` | (MCP dialog) | `MenuSession.loadedMcpNames` | ✅ Present |
| `is_fork_awaiting_start` | (Internal) | MISSING | Transient `json:"-"` field on Instance, not persisted |
| `skip_mcp_regenerate` | (Internal) | MISSING | Transient `json:"-"` field on Instance, not persisted |
| **ANALYTICS (Conditional)** |
| `claude_analytics` | Cost/token panel | MISSING | No `ClaudeAnalytics` struct on `*session.Instance` today |
| `gemini_analytics` | Cost/token panel | `MenuSession.geminiAnalytics` | ✅ Present |

---

## Behavioral Coverage Status (PR-A)

Every IMPLEMENTED row above is exercised by either the Playwright e2e suite
(`tests/web/e2e/parity-actions.spec.js`), the Go runtime parity test
(`internal/web/parity_test.go`), or both. Rows split into three coverage
tiers:

- **Happy-path** (web mutation + state observation): session lifecycle
  (create/start/stop/restart/delete/fork), group ops (create/rename/delete),
  `GET /api/settings`. Go parity test additionally pins web↔direct-mutator
  parity for create/start/stop/delete sessions and create/rename/delete
  groups.
- **Degraded-only** (503 + documented error code): cost endpoints
  (`/api/costs/summary`, `/api/costs/export`) and push endpoints
  (`/api/push/{subscribe,unsubscribe,presence}`). The fixture binary
  intentionally omits the SQLite cost store and the push service; happy-path
  coverage requires fixture wiring deferred to PR-B.
- **MISSING-stays-missing** (regression guard, 404/405 expected): 15 of the
  30 MISSING actions have plausible URL patterns probed by
  `inferMissingProbe()` in `tests/web/helpers/parity-matrix.js`. The other
  15 are TUI-UX-only (search, copy, jump, help, …) where no plausible web
  endpoint exists — those rows are matrix-tracked but not URL-probed.

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
- **Web JSON fields:** 42
- **MISSING web fields:** 3 (~7% gap) — two transients (`is_fork_awaiting_start`, `skip_mcp_regenerate`) and one not-yet-modeled (`claude_analytics`)
- **Remaining gaps:**
  - `is_fork_awaiting_start`, `skip_mcp_regenerate`: `json:"-"` on `*session.Instance`; nothing to surface
  - `claude_analytics`: no `ClaudeAnalytics` struct on the Instance yet (gemini-only today)

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
