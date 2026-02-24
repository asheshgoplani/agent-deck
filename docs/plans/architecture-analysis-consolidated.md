# Saltbox Hub — Architecture Analysis (Consolidated)

**Status:** Planning (Revised after February 2026 teardown)
**Date:** 2026-02-24 (originally 2026-02-23)
**Purpose:** Consolidated architecture analysis for the Saltbox Hub, synthesising research from architecture-analysis-part2 through part4, aligned to hub-requirements-v2 and the hub-v3 UI prototype. **Updated to incorporate critical findings from the February 2026 teardown analysis.**
**Cross-references:** `ROADMAP.md`, `hub-requirements-v2.md`, `hub-v3.jsx`, `Saltbox Agent Platform a critical teardown.md`

---

## 1. Executive Summary

The Saltbox Hub is a mobile-first web application that serves as the unified control plane for a self-hosted multi-agent coding platform running on a single NUC. It wraps Agent Deck's session management and tmux-based agent model with multi-project routing, streaming browser output, and visual task management.

After evaluating the full landscape of AI coding tools (Cursor 2.0, OpenCode, Claude Squad, Agent Deck, agtx, workmux, Coder, Mux), the architecture converges on a hybrid approach:

- **Fork Agent Deck** as the primary session manager, conductor orchestrator, and Telegram bridge (bus factor = 1 requires immediate fork)
- **Borrow from Workmux** for container isolation patterns and tmux-inside-container architecture (Pattern B)
- **Build the Hub** in Go (`net/http` + `html/template` + htmx) as a web layer providing mobile access, multi-project routing, and cross-project DAG scheduling

No existing tool combines container-level security isolation, web-based multi-agent orchestration, mobile accessibility, and self-hosted deployment on modest hardware. The Hub fills this gap.

### ⚠️ Critical Issues (February 2026 Teardown)

A critical teardown analysis identified several **blocking issues** that must be addressed before Phase 1:

| Category | Issue | Severity |
|----------|-------|----------|
| **Security** | NET_ADMIN capability allows agents to flush iptables firewall rules | 🔴 Critical |
| **Security** | SSH key read-only mounts still allow exfiltration | 🟠 High |
| **Architecture** | `tmux capture-pane` loses data when Claude Code runs autocompact (GitHub #16310) | 🔴 Critical |
| **Architecture** | Agent Deck has bus factor = 1 (8 stars, single maintainer) | 🟠 High |
| **Resources** | Claude Code memory leaks (30-120GB per instance documented) | 🟠 High |
| **Resources** | Realistic NUC concurrency: 3-5 agents max on 64GB | ⚠️ Medium |

See [Saltbox Agent Platform a critical teardown.md](Saltbox%20Agent%20Platform%20a%20critical%20teardown.md) and [ROADMAP.md](ROADMAP.md) Phase 0.6 for mitigations.

---

## 2. System Architecture

### 2.1 Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│                           USER ACCESS                               │
│                                                                     │
│  Browser (Desktop/Mobile)    Telegram (Mobile)    SSH + Terminus     │
└────────┬─────────────────────────┬──────────────────────┬───────────┘
         │                         │                      │
         ▼                         ▼                      ▼
┌─────────────────┐    ┌───────────────────┐    ┌─────────────────┐
│  Authelia SSO   │    │  Bridge Daemon    │    │  Agent Deck TUI │
│  + Traefik      │    │  (Telegram+Slack) │    │  (via terminal) │
└────────┬────────┘    └────────┬──────────┘    └────────┬────────┘
         │                      │                         │
         ▼                      ▼                         │
┌─────────────────────────────────────────────────────────┤──────────┐
│                      HUB LAYER (Go binary)              │          │
│                                                         │          │
│  Hub Web UI ─── Multi-Project ─── DAG Scheduler         │          │
│  (htmx+SSE)     Router              (SQLite)            │          │
│       │              │                   │              │          │
│       ▼              ▼                   ▼              │          │
│  SSE Bridge    projects.yaml    Task Store (JSON)       │          │
│  (docker exec                                           │          │
│   tmux capture                                          │          │
│   → browser)                                            │          │
└─────────┬───────────────────────────────┬───────────────┤──────────┘
          │                               │               │
          ▼                               ▼               ▼
┌──────────────────────────────────────────────────────────────────────┐
│                    AGENT DECK LAYER (Host)                            │
│                                                                      │
│  Sessions ──── Conductor(s) ──── Skills Manager                      │
│  (metadata;    (persistent       (per-session                        │
│   tmux lives    Claude session)    capability)                       │
│   in containers)                                                     │
└──────────┬───────────────────────────────┬───────────────────────────┘
           │ docker exec                   │ docker exec
           ▼                               ▼
┌──────────────────────┐    ┌──────────────────────┐    ┌──────────┐
│  web-app container   │    │  api-svc container   │    │  code-   │
│  (iptables firewall) │    │  (iptables firewall) │    │  server  │
│  tmux server inside  │    │  tmux server inside  │    │          │
│                      │    │                      │    │  (IDE)   │
│  worktree: main      │    │  worktree: main      │    │          │
│  worktree: fix-auth  │    │                      │    │          │
│    Agent A ●         │    │    Agent C ●         │    │          │
│  worktree: refactor  │    │                      │    │          │
│    Agent B ◐         │    │                      │    │          │
└──────────────────────┘    └──────────────────────┘    └──────────┘
```

### 2.2 Layer Responsibilities

| Layer | Technology | Responsibility |
|-------|-----------|----------------|
| **User Access** | Browser, Telegram, SSH | Entry points. Mobile dispatch via Hub or Telegram. Desktop deep-dive via SSH + Agent Deck TUI or code-server IDE. |
| **Auth & Routing** | Authelia SSO + Traefik | Single sign-on across all web endpoints. Shared session for Hub, code-server, ttyd. |
| **Hub** | Go `net/http` + `html/template` + htmx + SSE | Web UI, multi-project routing, cross-project DAG, streaming tmux output to browser, task CRUD, workspace management. Single static binary deployment. |
| **Agent Deck** | Go binary (host initially, containerisable later) | Session lifecycle, smart status detection, conductor orchestration, Telegram bridge, session forking, skills management. Communicates with sandbox tmux via `docker exec`. |
| **Project Containers** | Docker + iptables + OpenTofu + tmux | Isolated execution environments. One container per project with firewall, volume isolation, and resource limits. tmux + Claude Code run natively inside each container (Pattern B). Host connects via `docker exec`. |
| **IDE** | code-server | Browser-based VS Code for full development access. Single instance connecting to containers via SSH or docker exec. |

---

## 3. Agent Session Model

### 3.1 Core Principle: tmux Is the Truth

The fundamental architectural shift from early designs is recognising that **tmux IS the conversation record**. The Hub does not maintain its own message arrays or streaming protocol. Instead:

- Each agent runs in a tmux session **inside its sandbox container** (Pattern B)
- Agent Deck dispatches and reads output via `docker exec {container} tmux send-keys` / `tmux capture-pane`
- The Hub reads from container tmux pane buffers via `docker exec ... tmux capture-pane` for its preview display
- Users can attach to container tmux sessions for full interactive control: `docker exec -it {container} tmux attach -t agent-0`
- Agent status is derived from terminal output parsing (Agent Deck's smart detection)
- Disconnecting SSH/Hub does NOT interrupt agents — tmux + Claude Code keep running inside the container

This eliminates the need for a custom bidirectional WebSocket protocol, message parser, or React renderer. The implementation surface is: `tmux capture-pane` → SSE to browser, plus ANSI-to-HTML conversion.

### 3.2 Agent Lifecycle

```
agent-deck add {dir} -c "docker exec -it {container} tmux send-keys 'claude --dangerously-skip-permissions' Enter" -t {tag}
     │
     ▼
Container tmux session receives Claude Code launch command → Claude Code starts
     │
     ├── Status: thinking (●)  — Claude reasoning
     ├── Status: running (⟳)   — executing tools
     ├── Status: waiting (◐)   — AskUserQuestion, needs input
     ├── Status: idle (○)      — clean prompt, no activity
     ├── Status: error (✕)     — crash or error detected
     └── Status: complete (✓)  — task finished (Stop hook fires)
```

### 3.3 Status Detection

Agent Deck detects agent state via terminal output parsing, supplemented by Claude Code hooks for instant updates:

| Status | Detection Method | Hub Rendering |
|--------|-----------------|---------------|
| thinking | "Thinking..." output | Pulsing amber dot (●) |
| waiting | AskUserQuestion tool call or `>` prompt | Pulsing orange dot (◐) — highest priority visual signal |
| running | Tool execution output | Animated spinner (⟳) |
| idle | Clean prompt, no activity | Grey dot (○) |
| error | Error output or crash | Red cross (✕) |
| complete | Stop hook fires | Green check (✓) |

**Hook integration:** Claude Code's Stop hook (`~/.claude/settings.json`) fires when a session ends, providing instant status updates without polling.

### 3.4 Session Forking

Agent Deck's standout feature: forking creates a new Claude Code session that inherits the full conversation history from a parent session.

```
Parent: "Fix auth" (t-007)
├── Fork → "Try JWT approach"   (t-007-fork-1, inherits all context)
├── Fork → "Try OAuth approach" (t-007-fork-2, inherits all context)
```

In the Hub, forks create **new sibling tasks** rather than branches within the same task. Each fork appears independently in the Agents list and Kanban board, linked to its parent via `parentTask` field.

---

## 4. Task Data Model

### 4.1 Task Object

Every agent task is the central entity in the Hub. Defined in hub-requirements-v2 §4 and implemented in hub-v3.jsx:

```
Task {
  id:          string          // e.g. "t-007"
  project:     string          // project name, maps to a workspace
  msg:         string          // natural language task description
  status:      TaskStatus      // backlog → planning → running → review → done
  time:        string          // relative timestamp
  branch:      string | null   // git branch name
  phase:       WorkflowPhase   // brainstorm | plan | execute | review
  skills:      string[]        // Claude Code skill names
  mcps:        string[]        // MCP server names
  diff:        DiffSummary | null
  tmuxSession: string          // Agent Deck managed tmux session name
  agentStatus: AgentStatus     // derived from tmux output parsing
  sessions:    Session[]       // ordered list of context windows, one per phase
  parentTask:  string | null   // if forked, the parent task ID
}
```

### 4.2 Session Object

Sessions track metadata per workflow phase. Session **content** is the tmux scrollback buffer, not a custom message array.

```
Session {
  id:              string       // e.g. "s-007-1"
  phase:           WorkflowPhase
  status:          "active" | "complete"
  duration:        string
  artifact:        string | null   // file path produced
  summary:         string          // one-line summary
  tmuxSession:     string          // tmux session name for this phase
  claudeSessionId: string | null   // for resuming context via --resume
}
```

### 4.3 Workflow Phases

Each task progresses through up to four phases. Simple tasks may skip brainstorm and plan.

| Phase | Colour | Description | Produces |
|-------|--------|-------------|----------|
| brainstorm | violet | Root cause analysis, requirements gathering | Design document (markdown) |
| plan | indigo | Break design into implementation tasks with TDD specs | Implementation plan (markdown) |
| execute | amber | Code changes via subagents, each with fresh context | Code modifications, test results |
| review | blue | Human approval gate — diff review, approve/reject | Merge decision |

### 4.4 Persistence

**Filesystem JSON** for task and session state. One JSON file per task with embedded sessions array:

```
~/.hub/
  tasks/
    t-007.json          # Task object (includes sessions array)
    t-006.json
  conductor/
    log.json            # Conductor activity log
  config.json           # Hub configuration
```

**SQLite** reserved for the DAG scheduler (Phase 3) — task dependencies, execution order, cross-project coordination.

**tmux scrollback** is the conversation content. For completed sessions, a snapshot of final terminal output saved alongside the session JSON.

---

## 5. Hub Views and Navigation

Five primary views accessible from a persistent sidebar (narrow icon column on desktop, full-width overlay on mobile).

### 5.1 View Summary

| View | Icon | Purpose | Phase |
|------|------|---------|-------|
| Agents | ⟐ | Default. Task list + tmux preview panel. | MVP |
| Kanban | ▦ | Board: Backlog → Planning → Running → Review → Done. | MVP |
| Conductor | ◎ | Activity log + conductor messaging. | MVP |
| Workspaces | ▣ | OpenTofu workspace management dashboard. | MVP |
| Brainstorm | ◇ | Pre-project ideation (Claude Desktop-style). | Backlog |

### 5.2 Agents View (Default)

Two-panel layout from hub-v3.jsx:

**Left panel (280px desktop, full-width mobile):** Task list with AgentCard components. Sections: "Active" and "Completed" with counts. Each card shows project name, agent status badge, task description, ID, time, and mini session chain.

**Right panel:** Task detail with header (description, status, SSH/IDE/Attach buttons), session chain (phase pip navigator), tmux preview pane (live terminal output), and diff panel (review-phase only).

**Mobile:** Single-panel navigation. List OR detail, never both. "← Back" returns to list.

### 5.3 Kanban View

Five columns (Backlog → Planning → Running → Review → Done) with compact AgentCards. Same filter bar and group-by as Agents view. Columns scroll vertically independently; entire board scrolls horizontally on mobile.

### 5.4 Conductor View

Activity log of timestamped events from the conductor's monitoring:

| Type | Icon | Colour | Example |
|------|------|--------|---------|
| check | ✓ | green | "Heartbeat: 3 agents healthy, 0 errors" |
| action | → | amber | "Auto-approved t-004 — tests pass, diff clean" |
| alert | ⚠ | red | "t-006 review ready — 4 files, needs human approval" |
| route | ◈ | purple | "Routed 'Add /users CRUD' → api-service" |
| spawn | ⊕ | blue | "Started agent for t-007 in web-app worktree" |
| ask | ◐ | orange | "t-005 waiting: 'What authentication model?'" |

User can message the conductor directly via chat input in conductor mode.

### 5.5 Workspaces View

OpenTofu-provisioned workspace management. Cards showing project name, status (running/stopped/provisioning), template, resource allocation, and active agent count. Actions: start/stop, provision, SSH, IDE.

---

## 6. tmux Preview — The Presentation Layer

### 6.0 Critical Fix: pipe-pane Instead of capture-pane

**The original design used `tmux capture-pane` for terminal streaming. This has a confirmed showstopper bug (GitHub #16310): Claude Code's `autocompact`/`compact` operations clear the entire tmux scrollback buffer, blanking the web UI.**

**Revised approach:** Use `tmux pipe-pane` which streams output to a file in real-time:

```bash
# In container entrypoint, after creating tmux session:
tmux pipe-pane -o -t agent-0 "cat >> /tmp/agent-0.log"

# Hub reads via:
docker exec {container} tail -f /tmp/agent-0.log  # SSE stream
```

**Comparison:**

| Aspect | capture-pane (original) | pipe-pane (revised) |
|--------|------------------------|---------------------|
| Mode | Polling (point-in-time snapshot) | Streaming (continuous) |
| Autocompact bug | 🔴 Loses all data | ✅ Log file unaffected |
| Latency | 100-500ms polling intervals | Real-time |
| Scrollback | Limited to buffer size (default 2000 lines) | Unlimited (file-based, rotate as needed) |
| Implementation | `docker exec ... tmux capture-pane -p` | `docker exec ... tail -f /tmp/agent-0.log` |

### 6.1 Preview Pane Structure

Adapted from Agent Deck's preview pattern for the browser:

```
┌─ PREVIEW ──────────────────────────────────────────┐
│ web-app  ● thinking                                │
│ 📁 /workspace/web-app/claude/fix-auth              │
│ ⏱ active 3m                                       │
│ [claude] [development]                              │
│                                                    │
│ ──────────── Claude ───────────────                │
│ Status:  ● Connected                               │
│ Session: 24d59a28-f02d-4557-...                    │
│ MCPs:    github ✓, web-search ✓                    │
│                                                    │
│ ──────────── Output ───────────────                │
│ : 18 more lines above                              │
│                                                    │
│ ✓ GREEN: Implementing isTokenExpired...            │
│   Created src/auth/utils.ts (+12 lines)            │
│   ✓ 24 tests passed (1 new)                       │
│                                                    │
│ Task 1 complete. Dispatching Task 2...             │
│                                                    │
│ · Thinking... (esc to interrupt)                   │
└────────────────────────────────────────────────────┘
```

**Implementation (revised):** `docker exec {container} tail -f /tmp/{session}.log` → SSE to browser. ANSI-to-HTML conversion for terminal colours. Auto-scroll to bottom with "N more lines above" indicator. Read-only preview; attach via `docker exec -it {container} tmux attach` for full interactive access. Log rotation via `logrotate` prevents unbounded growth. See §6.0 for why `capture-pane` was replaced.

### 6.2 Attach Interaction

- **Desktop:** "Attach" button opens tmux session in new browser tab via ttyd or code-server terminal
- **Mobile:** "Attach" opens SSH connection targeting the specific tmux session (Terminus deeplink)
- **Detach:** Closing tab/SSH detaches. Agent continues running. Preview continues updating.

### 6.3 Comparison: Custom Streaming vs tmux Preview

| Concern | Custom streaming (early design) | tmux preview (revised — pipe-pane) |
|---------|-------------------------------|----------------------|
| Fidelity | Must parse/reformat output | Exact terminal output from container tmux |
| Interactivity | Custom WebSocket protocol | `docker exec -it ... tmux attach` = native terminal |
| Scrollback | Must store and paginate | Log file, unlimited (rotate as needed) |
| Status detection | Parse from message stream | Agent Deck parses log file output |
| Offline agent | Handle reconnection/replay | tmux + log file persist inside container |
| Autocompact bug | N/A | ✅ Log file unaffected (GitHub #16310 resolved) |
| Implementation cost | High: WebSocket + parser + renderer | Moderate: docker exec + tail -f → SSE + ANSI-to-HTML |

---

## 7. AskUserQuestion Handling

When Claude Code uses the `AskUserQuestion` tool, the agent pauses and the Hub must surface the prompt for response.

### 7.1 Known Issue

AskUserQuestion has a known issue (GitHub #10400, still open as of 2026-02-23) where it returns empty responses when `--dangerously-skip-permissions` is enabled, silently skipping user prompts. A related issue (#15400) causes AskUserQuestion to trigger the PermissionRequest hook, auto-dismissing questions. The tool gets silently bypassed — Claude thinks the user answered, but the response is empty.

### 7.2 Detection Chain (if working)

1. Claude Code invokes `AskUserQuestion` → terminal shows `>` prompt inside container tmux
2. Agent Deck detects `waiting` (◐) status via `docker exec ... tmux capture-pane` output parsing
3. Hub polls Agent Deck status or receives hook notification
4. Hub updates agent card: status changes to `◐ waiting` with orange pulse
5. Conductor sends Telegram notification: "t-007 asking: {question}"

### 7.3 Response Flow (if working)

1. User sees waiting indicator in Hub (or receives Telegram notification)
2. Chat input switches to reply mode: `↩ t-007 / plan`
3. Placeholder shows extracted question: `"Answer: What auth model? JWT, API key, or session?"`
4. User types response
5. Hub sends to container tmux: `docker exec {container} tmux send-keys -t {session} "{input}" Enter`
6. Agent receives input, status transitions back to thinking (●)

### 7.4 MCP Fallback (if broken)

If AskUserQuestion doesn't work with `--dangerously-skip-permissions` in the container+tmux configuration, deploy a custom MCP tool:

1. Build lightweight `ask-human` MCP server (runs on host or in Agent Deck container)
2. Claude calls `ask_human` tool → MCP writes question to file or SQLite row
3. Hub/Telegram polls for pending questions → presents to user → user replies
4. MCP reads reply → returns answer to Claude Code
5. Alternatively: instruct Claude via CLAUDE.md to never use AskUserQuestion and instead use the custom MCP tool

This works regardless of `--dangerously-skip-permissions` behaviour and provides a proper async notification pipeline.

### 7.5 Mobile Flow

Telegram → Hub → `docker exec ... tmux send-keys`. Or future: bidirectional Telegram (reply directly in Telegram → conductor routes to agent).

---

## 8. Context-Aware Chat Input

Global chat bar pinned to the bottom of every view. Auto-derives mode from navigation state.

### 8.1 Chat Modes

| Mode | Icon | Colour | Trigger | Behaviour |
|------|------|--------|---------|-----------|
| Reply | ↩ | amber | Task selected with active session | Send to tmux via `tmux send-keys` |
| New | + | blue | No task selected, or task is done | Create new task, route via conductor |
| Conductor | ◎ | indigo | In Conductor view | Send to conductor tmux session |

### 8.2 Auto-Detection Logic

The input mode is inferred from navigation state:

1. Agents view + active task + active session → Reply mode (`↩ t-007 / execute`)
2. Agents view + active task + no active session → New task in same project (`+ web-app`)
3. Agents view + no task + project filter active → New task in filtered project (`+ web-app`)
4. Agents view + no task + no filter → New task with auto-routing (`+ auto-route`)
5. Conductor view → Conductor mode (`◎ conductor-ops`)
6. Any override clears on navigation change

### 8.3 Slash Commands

Dual command palette (hub-requirements-v2 §8):

**Hub commands** (handled locally): `/new`, `/fork`, `/sessions`, `/conductor`, `/status`, `/diff`, `/approve`, `/reject`

**Claude Code commands** (passed through to tmux): `/compact`, `/permissions`, `/memory`, `/cost`, `/doctor`, `/clear`, `/login`

**Project skills** (queried from workspace): custom commands from `~/.claude/commands/`

---

## 9. Workspace Provisioning

### 9.1 OpenTofu Stack

```
OpenTofu template (HCL) → Docker container + iptables firewall + volume mounts + tmux server
    ↓
tmux sessions inside container (Claude Code runs natively within container tmux)
    ↓
Agent Deck on host dispatches via: docker exec {container} tmux send-keys "..." Enter
    ↓
Hub reads output via: docker exec {container} tmux capture-pane -p → SSE to browser
```

~150 lines of HCL replaces ~500 lines of shell scripting from the devcontainer approach.

### 9.2 Security Layers (Revised After Teardown)

**⚠️ CRITICAL:** The original security model has a fundamental flaw. Containers with NET_ADMIN capability can flush their own iptables rules (`iptables -F`), rendering the firewall useless. The revised model moves firewall rules to the host namespace.

| # | Layer | Original | Revised | Status |
|---|-------|----------|---------|--------|
| 1 | **Authelia SSO** | Hub, code-server, SSH endpoints | Unchanged | ✅ |
| 2 | **Firewall** | iptables inside container | **DOCKER-USER chain in host namespace** — containers cannot modify | 🔴 Breaking change |
| 3 | **Docker socket proxy** | tecnativa/docker-socket-proxy | Unchanged — coarse-grained but acceptable for single-user | ⚠️ |
| 4 | **Capabilities** | `cap_drop ALL`, add NET_ADMIN, NET_RAW, SETUID, SETGID | **`cap_drop ALL`, add SETUID, SETGID only** — no NET_ADMIN/NET_RAW | 🔴 Breaking change |
| 5 | **SSH keys** | Read-only bind mount | **SSH agent forwarding** — keys never enter container | 🔴 Breaking change |
| 6 | **Volume isolation** | Per-workspace named volumes | Unchanged | ✅ |
| 7 | **Resource limits** | CPU via OpenTofu | **CPU + memory (4-6GB hard limit) + session recycling** | ⚠️ |
| 8 | **IPv6 disabled** | sysctl | Unchanged | ✅ |

**New security additions:**
- Domain-based whitelisting replaced with HTTP/HTTPS proxy (Squid) for Host/SNI inspection (mitigates DNS rebinding, CDN rotation, DNS exfiltration)
- Secrets migrated from environment variables to Docker Compose secrets (`/run/secrets/`)
- runc version ≥ 1.2.8 required (mitigates CVE-2025-31133, CVE-2025-52565, CVE-2025-52881)

### 9.3 Container Isolation Model

The Saltbox model uses **container-per-project** with worktrees inside:

```
Project Container (web-app) — long-lived, iptables firewall, tmux server
├── tmux session: agent-0 (Claude Code running)
├── tmux session: agent-1 (Claude Code running)
├── worktree: main
├── worktree: claude/fix-auth (Agent A in agent-0)
├── worktree: claude/refactor (Agent B in agent-1)
```

This differs from workmux's container-per-worktree model. The project-level container provides the network security boundary (iptables) and hosts the tmux server; worktrees provide code isolation between agents within the same project. Agent Deck on the host interacts via `docker exec`.

---

## 10. Conductor Architecture

### 10.1 Conductor as Separate Process

The conductor is NOT a function within the Hub Go process. It is a persistent Claude Code session in its own tmux window, managed by Agent Deck:

```
~/.agent-deck/conductor/
├── CLAUDE.md            # Shared knowledge (CLI reference, protocols)
├── bridge.py            # Bridge daemon (Telegram/Slack)
├── ops/
│   ├── CLAUDE.md        # Identity: "You are ops, a conductor"
│   ├── meta.json        # Config
│   ├── state.json       # Runtime state
│   └── task-log.md      # Action log
```

### 10.2 How It Works

1. Conductor is a Claude Code session with a specific identity and scope
2. Heartbeat-driven monitoring: periodic check-in prompts (default every 15 minutes)
3. Reads session states, checks for waiting/errored sessions
4. Can auto-respond to simple questions or escalate complex ones
5. `NEED:` in conductor response → Telegram/Slack alert
6. User can message conductor directly from Hub chat input or Telegram

### 10.3 Conductor vs Coordinator

| Dimension | Agent Deck Conductor | Workmux /coordinator |
|-----------|---------------------|---------------------|
| Scope | Across sessions, over time | Within one task decomposition |
| Persistence | Long-running, persistent context | Ephemeral, per-invocation |
| Invocation | Automatic (heartbeat) + user message | User invokes `/coordinator` |
| Best for | Monitoring, alerting, auto-approval | Breaking down complex tasks |

The Hub adopts the conductor pattern. The coordinator pattern is borrowed for future cross-project DAG decomposition.

---

## 11. Notification System

### 11.1 Telegram Bridge

```
Conductor → bridge.py → Telegram Bot API → User's phone
```

| Event | Telegram Message |
|-------|-----------------|
| Agent waiting | "🔶 t-007 asking: What auth model?" |
| Review ready | "🔵 t-006 review ready — 4 files changed" |
| Auto-approved | "✅ t-004 auto-approved — tests pass" |
| Agent error | "❌ t-007 error: npm install failed" |
| Heartbeat | "💚 3 agents healthy, 1 waiting" |

### 11.2 Slack Socket Mode

Secondary channel. Socket Mode requires no public URL — critical for NUC behind NAT. Same `name: message` routing as Telegram.

### 11.3 Future: Bidirectional Telegram

Users reply to Telegram messages → conductor routes input to waiting agents. The ultimate mobile flow: notification → reply → agent continues.

---

## 12. Backend API Surface

### 12.1 Task Management

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/tasks` | GET | List tasks with status/project filters |
| `/tasks` | POST | Create new task → `agent-deck add` + `docker exec` |
| `/tasks/{id}` | GET | Task with session metadata |
| `/tasks/{id}` | PATCH | Update status (approve, reject) |
| `/tasks/{id}/fork` | POST | Fork → new sibling task via Agent Deck fork |

### 12.2 Agent Communication (via tmux)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/tasks/{id}/input` | POST | Send input to tmux session (`tmux send-keys`) |
| `/tasks/{id}/preview` | GET (SSE) | Stream tmux pane capture |
| `/tasks/{id}/status` | GET | Current agent status from Agent Deck |

### 12.3 Workspace Management

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/workspaces` | GET | List all OpenTofu workspaces |
| `/workspaces` | POST | Provision new workspace (`tofu apply`) |
| `/workspaces/{name}/start` | POST | Start container |
| `/workspaces/{name}/stop` | POST | Stop container |
| `/workspaces/{name}` | DELETE | Destroy workspace (`tofu destroy`) |

### 12.4 Conductor

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/conductor/log` | GET | Activity log (from conductor file-based log) |
| `/conductor/message` | POST | Send to conductor tmux session |
| `/conductor/status` | GET | Connection status |

### 12.5 Agent Deck CLI Mapping (Revised)

| Hub API | Agent Deck / docker exec |
|---------|--------------------------|
| `POST /tasks` | `agent-deck add {dir} -c "docker exec -it {container} tmux send-keys 'claude --dangerously-skip-permissions' Enter" -t {tag}` |
| `GET /tasks` | `agent-deck list --json` |
| `POST /tasks/{id}/fork` | `agent-deck fork {session}` |
| `GET /tasks/{id}/status` | Agent Deck status detection (parsed from log file) |
| `POST /tasks/{id}/input` | `docker exec {container} tmux send-keys -t {session} "{input}" Enter` |
| `GET /tasks/{id}/preview` | `docker exec {container} tail -f /tmp/{session}.log` → SSE |

**Note:** Preview uses `tail -f` on the pipe-pane log file instead of `capture-pane` to avoid the scrollback-clearing bug (GitHub #16310).

---

## 13. Competitive Landscape Summary (Updated February 2026)

### 13.1 Major Landscape Shifts

The competitive landscape shifted dramatically in late 2025 / early 2026:

| Solution | Status | Overlap | Key Differentiator | Gap for Saltbox |
|----------|--------|---------|-------------------|-----------------|
| **Coder Tasks + Mux** | Production | ~80% | Enterprise-grade, 1,624 commits | Mobile support; requires Kubernetes expertise |
| **OpenHands** | Production | ~70% | 38,800+ stars, $18.8M funding, hierarchical agents | No mobile-first interface; autonomous focus |
| **Ona (ex-Gitpod)** | Pivoting | ~60% | "Mission control for software engineering agents" | Self-hosting limited to AWS VPC |
| **Daytona.io** | $24M Series A | Infrastructure | Sub-90ms sandbox startup, forkable/snapshotable | Infrastructure layer, not orchestration UI |
| **Workmux** | Active | ~80% | Only tmux tool with Docker/Lima sandboxing | No web UI, no mobile access |
| **agtx** | Active | ~60% | Kanban-style TUI | No containers, no web |
| **Claude Squad** | Mature | ~55% | v1.0.10, popular for parallel sessions | No container isolation |
| **Agent Deck** | Active | ~70% | Conductor + Telegram + forking | Bus factor = 1 (8 stars), no container isolation |

### 13.2 The Gap That Remains

**Saltbox's unique combination remains unduplicated:**
1. True on-prem simplicity (NUC-deployable, no Kubernetes required)
2. Container-level security isolation with iptables firewall (host namespace)
3. Web-based UI accessible from mobile
4. Multi-project task routing
5. Streaming agent output to browser
6. Cross-project DAG orchestration
7. Persistent conductor monitoring

**The window is narrowing.** Coder's enterprise-backed solution covers most use cases for teams with Kubernetes. The competitive threat is real but not immediate — Coder targets teams with Kubernetes expertise, while Saltbox targets individual developers who want a plug-and-play NUC. The more existential risk is that OpenHands, Daytona, or Ona builds mobile access before Saltbox achieves production stability.

**Speed matters, but shipping the Phase 0.6 security fixes matters more** — a platform that can't contain its own agents isn't ready for users.

### 13.3 Adopt / Build / Borrow Matrix (Revised)

| Category | Components | Status |
|----------|-----------|--------|
| **ADOPT** | ~~Agent Deck~~ → **Fork Agent Deck** (sessions, conductor, Telegram, forking, skills) | ⚠️ Forked due to bus factor |
| **ADOPT** | Claude Code CLI, SQLite, existing infrastructure (Authelia, Traefik, volumes) | ✅ |
| **ADOPT** | **Langfuse** (cost tracking), **restic** (backups) | 📋 Phase 0.6g |
| **BUILD** | Hub Web UI (Go `net/http` + `html/template` + htmx + SSE) | 📋 |
| **BUILD** | SSE bridge (`tmux pipe-pane` → log file → `tail -f` → browser) | 📋 Changed from capture-pane |
| **BUILD** | Host-side firewall manager (DOCKER-USER chain rules per container) | 🔴 Phase 0.6a |
| **BUILD** | Centralized API proxy (cost tracking, rate limiting, audit logging) | 📋 Phase 0.6g |
| **BUILD** | Session recycling daemon (memory leak containment) | 📋 Phase 0.6e |
| **BORROW** | Workmux (container sandbox + tmux-inside-container patterns) | ✅ |
| **BORROW** | Coder (workspace UI design), agtx (kanban layout), Cursor 2.0 (agent-as-managed-process) | ✅ |

---

## 14. Container Integration: Agent Deck + Docker

### 14.0 ⚠️ Agent Deck Dependency Risk

**Critical finding from February 2026 teardown:** Agent Deck has a bus factor of 1 — **8 GitHub stars, 84 commits, single maintainer**. This is a personal project being used as core infrastructure.

**Mitigation (implemented):**
1. **Fork immediately** to organization repository
2. Document core functionality needed (~500 lines of Go): session management, status detection, forking, conductor
3. Evaluate building session management directly into Hub binary (eliminates dependency)
4. The functionality Agent Deck provides is implementable in 1-2 weeks of Go development

### 14.1 The Integration Pattern: tmux Inside Containers (Pattern B)

Agent Deck (forked) has zero built-in container isolation. The solution is **Agent Deck on host, tmux + Claude Code inside containers**. Agent Deck interacts with container tmux via `docker exec`:

```bash
# Dispatch a task
docker exec -it sandbox-web-app tmux send-keys "Fix the auth bug" Enter

# Read output (via pipe-pane log file — NOT capture-pane)
docker exec sandbox-web-app tail -f /tmp/agent-0.log

# Interactive attach
docker exec -it sandbox-web-app tmux attach -t agent-0
```

This is Pattern B (tmux inside container), chosen over Pattern A (tmux on host wrapping docker exec) for resilience — disconnecting SSH/Hub doesn't interrupt agents. Each container is a self-contained agent environment.

**Note:** Output reading uses `tail -f` on the pipe-pane log file, not `capture-pane`, due to the scrollback-clearing bug (GitHub #16310). See §6.0.

**Agent Deck containerisation (future):** Agent Deck can itself be containerised as a "management plane" container with Docker socket access (DooD pattern). It would use `docker exec` to interact with sandbox container tmux sessions. This adds one layer of SSH indirection for desktop (SSH → Agent Deck container → docker exec → sandbox tmux) but doesn't affect the Hub web path. Run on host initially for simplicity; containerise later for deployment consistency.

### 14.2 What Works Through docker exec (Pattern B — Revised)

| Feature | Works? | Notes |
|---------|--------|-------|
| Dispatch tasks | ✅ | `docker exec ... tmux send-keys "prompt" Enter` |
| Read output | ✅ | `docker exec ... tail -f /tmp/agent-0.log` (pipe-pane log) |
| Interactive attach | ✅ | `docker exec -it ... tmux attach -t agent-0` |
| Status detection | ⚠️ Test | Parse log file output; unaffected by autocompact |
| Session forking | ✅ | Creates new tmux session inside same container |
| Conductor monitoring | ⚠️ Test | Reads session state via docker exec; should work |
| MCP socket pooling | ❌ | Unix sockets can't cross container boundaries. Per-container MCP processes for now. |
| Agent resilience | ✅ | Disconnecting docker exec doesn't kill tmux/Claude inside container |
| Firewall modification | ❌ | **No longer possible** — NET_ADMIN removed, rules in host namespace |

### 14.3 Risk: Status Detection

The highest-risk integration point. Agent Deck parses terminal output to detect agent status. If docker exec adds buffering or modifies output format, status detection could break.

**Mitigation:** Test empirically in Phase 0.5. Fallback: install a thin status-reporting agent inside containers, or rely solely on Claude Code hooks for status updates.

### 14.4 MCP Strategy (Phased)

**Now (Phase 1): Per-container MCP processes (Option C)**

Each sandbox container spawns its own MCP server instances when Claude Code starts. On a NUC with 32-64GB running 3-5 concurrent agents, the ~50-100MB overhead per agent is negligible. Agent Deck's built-in MCP socket pooling (Unix sockets at `/tmp/agentdeck-mcp-{name}.sock`) does NOT work across Docker container boundaries.

**Future (Phase 5+): Streamable HTTP MCP proxy (Option A)**

When agent count grows or memory becomes constrained, deploy a shared MCP proxy on the host:

```
Sandbox containers (type: http, url: proxy:8500/mcp)
         │
         ▼
MCP Proxy (host or own container, port 8500)
├── GitHub MCP (stdio, spawned once)
├── Exa MCP (stdio, spawned once)
└── ... shared across all clients via Streamable HTTP
```

Migration is config-only: swap `.mcp.json` entries from stdio to http. Whitelist proxy's Docker network address in `init-firewall.sh`. ~85-90% memory reduction.

**Practical impact (now):** With 5 agents across 3 projects, each needing 2 MCP servers, that's 10 MCP processes instead of 2. On a NUC with 32GB RAM, this is manageable but worth monitoring.

### 14.5 Resource Constraints (Claude Code Memory Leaks)

**Critical finding from February 2026 teardown:** Claude Code has documented memory leaks that cause instances to grow from 1-2GB baseline to **30-120GB** (GitHub issues #9711, #4953, #11315) before OOM kills or system freezes.

**Realistic NUC concurrency:**

| NUC RAM | Agents | Memory per Agent | Reserved (OS + Platform) |
|---------|--------|------------------|--------------------------|
| 32GB | 2-3 | 4-6GB | 8-10GB |
| 64GB | 3-5 | 4-6GB | 8-10GB |

**Mitigations (Phase 0.6e):**
1. Docker memory limits: `--memory=4g --memory-swap=6g`
2. Session recycling daemon: restart containers every 2-4 hours of active use
3. Memory monitoring with alerts at 80% container limit
4. OOM killer enabled (`--oom-kill-disable=false`)

### 14.6 Missing Infrastructure Capabilities

Six capabilities identified in the teardown that mature platforms consider table stakes:

| Capability | Current State | Target State | Phase |
|------------|---------------|--------------|-------|
| **Cost tracking** | None | Langfuse (MIT, self-hostable, 19K+ stars) with per-agent attribution and budget alerts | 0.6g |
| **Rate limiting** | None | Centralized token bucket in Hub backend, tracking Anthropic's `anthropic-ratelimit-*` headers | 0.6g |
| **Audit logging** | None | Structured JSON logs with agent_id, session_id, timestamps, inputs/outputs | 0.6g |
| **Secret management** | Environment variables | Docker Compose secrets at `/run/secrets/` | 0.6g |
| **Backup/DR** | None | restic → Backblaze B2, documented recovery runbook with RPO/RTO | 0.6g |
| **API proxy** | Direct to Anthropic | Centralized proxy in Hub backend for all of the above | 0.6g |

**Key insight:** Routing all Claude API calls through the Hub backend as a centralized proxy enables cost tracking, rate limiting, audit logging, and budget enforcement in a single layer.

---

## 15. Responsive Design

### 15.1 Breakpoint

Single breakpoint at **768px**. Below = mobile layout. Above = desktop layout.

### 15.2 Layout Differences

| Component | Desktop | Mobile |
|-----------|---------|--------|
| Sidebar | Narrow icon column, always visible | Full-width overlay, hamburger toggle |
| Agents view | Two panels side by side | Single panel: list OR detail |
| Kanban | Columns side by side | Horizontal scroll |
| Task header buttons | SSH + IDE + Attach | Attach only |
| tmux preview | Full terminal view | Compact view |
| Filter bar | Full width | Horizontally scrollable pills |
| Session chain | Full width | Horizontally scrollable |

### 15.3 Offline Behaviour

Messages typed while offline queued in browser localStorage. On reconnection: review → send all / send individually / discard. Context checks verify target task state before sending.

---

## 16. Visual Design System

Dark theme throughout (hub-v3.jsx reference implementation):

| Token | Value | Usage |
|-------|-------|-------|
| Background | `#0e1117` | App background |
| Surface | `#161b22` | Cards, panels |
| Border | `#21262d` | Dividers, card borders |
| Text primary | `#e6edf3` | Main content |
| Text secondary | `#7d8590` | Labels, timestamps |
| Accent | `#e8a932` | Active states, running agents |
| Success | `#2dd4a0` | Complete, approved |
| Warning | `#f59e0b` | Waiting, needs input |
| Error | `#f06060` | Errors, rejected |
| Phases | violet/indigo/amber/blue | brainstorm/plan/execute/review |

Typography: system font stack. Terminal content: monospace. Single font size scale for consistency.

---

## 17. Implementation Phases

### Phase 0.6 — Critical Fixes (3–5 days) 🚨 BLOCKING

**Must complete before Phase 1. Addresses security vulnerabilities and architectural showstoppers from February 2026 teardown.**

```
□ 0.6a Firewall: Remove NET_ADMIN/NET_RAW from containers; apply rules in DOCKER-USER chain
□ 0.6b SSH keys: Replace bind mounts with SSH agent forwarding
□ 0.6c Terminal: Replace tmux capture-pane with pipe-pane streaming
□ 0.6d Agent Deck: Fork to organization repo; document core functionality
□ 0.6e Memory: Add Docker memory limits (4-6GB); implement session recycling
□ 0.6f HTTP/2: Verify Traefik serves Hub over HTTP/2; test 10 concurrent SSE connections
□ 0.6g Infrastructure: Deploy Langfuse for cost tracking; configure restic backups; migrate secrets
```

### Phase 1 — Agent Deck MVP (1–2 days)

**Prerequisites:** Phase 0.5 (Coder), Phase 0.6 (Critical Fixes)

```
□ Install forked Agent Deck on NUC host
□ Configure config.toml with MCP servers
□ Create test sessions with docker exec + tmux commands (Pattern B)
□ Verify status detection works through docker exec + log file reading
□ Test session forking
□ Configure Claude Code hooks for instant status updates
□ Set up Telegram bot + conductor profile
□ Test mobile dispatch via Telegram
□ Document workflow in CLAUDE.md per project
□ Test AskUserQuestion in sandbox (tmux + container + --dangerously-skip-permissions)
```

### Phase 2 — Hub Web Layer (1–2 weeks)

```
□ Go scaffold with net/http + html/template + htmx
□ projects.yaml registry
□ Keyword router → agent-deck add + docker exec
□ SSE endpoint: docker exec ... tmux capture-pane → server-sent events
□ ANSI-to-HTML terminal renderer
□ Mobile-responsive layout (Agents view + Kanban)
□ Context-aware chat input with auto-detection
□ AskUserQuestion handling (detect → surface → reply via tmux send-keys)
□ Basic diff viewer (git diff → HTML)
□ Conductor view with activity log
□ Workspace management UI
□ Authelia protection via Traefik
```

### Phase 3 — Git Automation (1 week)

```
□ Auto-branch creation before agent starts
□ Web-based diff viewer with approve/reject
□ Auto-commit with generated message
□ Push + PR creation via GitHub CLI
□ Approval workflow with hold-before-push option
```

### Phase 5 — Cross-Project Orchestration (1 week)

```
□ SQLite task/dependency schema (DAG model)
□ Multi-task parsing ("do X in project-a, then Y in project-b")
□ DAG scheduler: dispatch sub-tasks based on dependency resolution
□ DAG progress visualisation in Hub UI
□ Webhook endpoint for external triggers
□ Scheduled tasks via APScheduler
```

---

## 18. Open Questions (Revised)

### 18.1 Resolved by Teardown

| # | Question | Resolution |
|---|----------|------------|
| 1 | Does `tmux capture-pane` work reliably? | ❌ **No** — autocompact clears scrollback. Use `pipe-pane` instead. |
| 5 | tmux capture-pane polling rate? | N/A — replaced with streaming via `pipe-pane`. |

### 18.2 Still Open

| # | Question | Impact | Status |
|---|----------|--------|--------|
| 1 | Does Agent Deck status detection work through `docker exec` + log file reading? | High — core UX | Test in Phase 1 |
| 2 | Can conductor sessions effectively monitor containerized agents via docker exec? | Medium — orchestration quality | Test in Phase 1 |
| 3 | Is `agent-deck web` (:8420) sufficient as a lightweight dashboard? | Medium — determines Hub scope | Test in Phase 1 |
| 4 | Does AskUserQuestion work inside tmux inside container with --dangerously-skip-permissions? | Medium — determines MCP fallback need | Test in Phase 1 |
| 6 | What is the performance overhead of host-side DOCKER-USER iptables vs in-container rules? | Low — unlikely to matter | Test in Phase 0.6a |
| 7 | How does SSH agent forwarding work through docker exec? | Medium — key security layer | Test in Phase 0.6b |
| 8 | What log rotation strategy works best for pipe-pane output files? | Low — operational detail | Test in Phase 0.6c |

---

## 19. Risk Assessment (Revised After Teardown)

### 19.1 Critical Risks (Must Address Before Phase 1)

| Risk | Likelihood | Impact | Mitigation | Status |
|------|-----------|--------|------------|--------|
| **NET_ADMIN allows firewall bypass** | Certain | Critical | Remove NET_ADMIN; apply rules in host namespace (DOCKER-USER chain) | 🔴 Phase 0.6a |
| **tmux capture-pane loses data on autocompact** | Certain | Critical | Replace with pipe-pane streaming to log file | 🔴 Phase 0.6c |
| **Agent Deck bus factor = 1** | Certain | High | Fork immediately; evaluate in-house replacement | 🔴 Phase 0.6d |
| **Claude Code memory leaks (30-120GB)** | High | High | Docker memory limits (4-6GB) + session recycling every 2-4 hours | ⚠️ Phase 0.6e |
| **SSH key exfiltration via read-only mount** | High | High | Use SSH agent forwarding instead of key mounting | 🔴 Phase 0.6b |
| **Prompt injection → arbitrary code execution** | High | Critical | Fundamental risk of `--dangerously-skip-permissions`. Mitigated by container isolation + network controls. | ⚠️ Accepted |

### 19.2 Medium Risks

| Risk | Likelihood | Impact | Mitigation | Status |
|------|-----------|--------|------------|--------|
| Status detection fails through docker exec | Medium | High | Test immediately. Fallback: Claude Code hooks only. | ⚠️ Test |
| MCP socket pooling incompatible with containers | Certain | Medium | Per-container MCP processes now. HTTP proxy when needed. | ✅ Accepted |
| Conductor can't monitor containerized agents | Medium | Medium | Conductor reads log files via docker exec. Test heartbeat. | ⚠️ Test |
| Go `net/http` + htmx too limited for complex UI | Medium | Medium | Content negotiation escape hatch for future React migration. | ⚠️ |
| NUC resource constraints (3-5 agents max) | Certain | Medium | Memory limits per container. Realistic concurrency budgeting. | ⚠️ |
| AskUserQuestion broken with --dangerously-skip-permissions | Medium | Medium | Deploy custom ask-human MCP server as fallback. | ⚠️ Test |
| SSE connection limit under HTTP/1.1 (6 per domain) | Certain | Medium | Require HTTP/2 (TLS) — enables ~100 multiplexed streams. | ⚠️ Phase 0.6f |
| runc container escape (CVE-2025-*) | Medium | High | Ensure runc ≥ v1.2.8 on host. | ⚠️ |
| Domain-based iptables whitelisting fragile | Certain | Medium | Replace with HTTP/HTTPS proxy (Squid) for Host/SNI inspection. | 📋 |
| Secrets in environment variables | Certain | Medium | Migrate to Docker Compose secrets (`/run/secrets/`). | 📋 Phase 0.6g |

### 19.3 Competitive Risks

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Coder Tasks + Mux covers 80% of features | Certain | Medium | Differentiate on NUC simplicity + mobile-first. No Kubernetes required. |
| OpenHands adds mobile support | Medium | High | Ship Phase 0.6 fixes + Hub MVP before they do. Speed matters. |
| Ona/Daytona add on-prem self-hosting | Low | High | Window is narrowing. Execute quickly but don't ship broken security. |

---

## 20. Decision Log

Decisions made across the architecture analysis series (parts 2–4), hub requirements evolution, and February 2026 teardown:

### 20.1 Original Decisions (Unchanged)

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Agent runtime | Claude Code CLI | Comfortable with technology. Explore OpenCode only if it gains full CLI parity. |
| Hub language | Go (`net/http` + `html/template` + htmx) | Same ecosystem as Agent Deck. Single binary deploy. Goroutines handle concurrent docker exec + SSE naturally. |
| tmux location | Inside sandbox container (Pattern B) | Resilience: disconnecting SSH/Hub doesn't kill agents. Self-contained containers. |
| Container isolation model | Container-per-project | Project-level security boundary. Worktrees for code isolation within. |
| Workspace provisioning | OpenTofu templates | ~150 lines HCL vs ~500 lines shell. |
| Conductor model | Separate long-running Claude instance | Persistent context window. Monitors over time. |
| Mobile notifications | Telegram bridge | Already built in Agent Deck. No public URL needed. |
| Multi-user | Single-user | One user, one NUC. Simplifies everything. |
| MCP strategy (now) | Per-container processes | Socket pooling can't cross container boundaries. |
| MCP strategy (future) | Streamable HTTP proxy | Config-only migration. ~85-90% memory reduction. |

### 20.2 Revised Decisions (February 2026 Teardown)

| Decision | Original | Revised | Rationale |
|----------|----------|---------|-----------|
| **Firewall architecture** | iptables inside container with NET_ADMIN | **Host namespace (DOCKER-USER chain)** | Containers with NET_ADMIN can flush their own rules. Critical security flaw. |
| **Container capabilities** | NET_ADMIN, NET_RAW, SETUID, SETGID | **SETUID, SETGID only** | No NET_ADMIN/NET_RAW prevents firewall tampering. |
| **SSH key handling** | Read-only bind mount | **SSH agent forwarding** | Read-only mounts still allow `cat ~/.ssh/id_rsa` + exfiltration. |
| **Terminal streaming** | `tmux capture-pane` (polling) | **`tmux pipe-pane`** (streaming to file) | Claude Code's autocompact clears scrollback (GitHub #16310). |
| **Session persistence** | Filesystem JSON | **SQLite with WAL mode** | Race conditions with multiple agents writing same file. |
| **Agent Deck integration** | CLI automation (`--json`) | **Fork + evaluate replacement** | Bus factor = 1 (8 stars, 1 dev) is unacceptable for core infrastructure. |
| **Memory management** | CPU limits only | **CPU + memory (4-6GB) + session recycling** | Claude Code memory leaks (30-120GB documented). |
| **HTTP version** | HTTP/1.1 acceptable | **HTTP/2 required** | 6 SSE connections per domain under HTTP/1.1 blocks 5+ agent dashboard. |
| **Secret management** | Environment variables | **Docker Compose secrets** | Env vars visible in `/proc`, `docker inspect`, logs. |
| **Network filtering** | Domain-based iptables | **HTTP/HTTPS proxy (Squid)** | DNS rebinding, CDN rotation, DNS exfiltration bypass IP rules. |
| **Cost tracking** | Not addressed | **Langfuse integration** | Runaway agents can burn thousands in hours. Real-time attribution needed. |
| **Backup strategy** | Not addressed | **restic → Backblaze B2** | Single NUC = single point of failure. Offsite backups critical. |
| **htmx limitations** | Acceptable for v1 | **Escape hatch: content negotiation** | Structure endpoints to return JSON + HTML for future React migration. |

### 20.3 New Decisions (February 2026)

| Decision | Choice | Rationale |
|----------|--------|-----------|
| **Phase 0.6 blocking** | All critical fixes before Phase 1 | Shipping broken security is worse than shipping slow. |
| **Centralized API proxy** | Route all Claude API calls through Hub backend | Single chokepoint for cost tracking, rate limiting, audit logging, budget enforcement. |
| **Realistic concurrency** | 3-5 agents max on 64GB NUC | Memory leaks + 4-6GB per container + OS/platform overhead. |
| **runc version** | Require ≥ v1.2.8 | CVE-2025-31133, CVE-2025-52565, CVE-2025-52881 bypass AppArmor/SELinux. |
| **Competitive positioning** | NUC simplicity + mobile-first | Coder/OpenHands target Kubernetes teams. Saltbox targets personal DevOps. |
