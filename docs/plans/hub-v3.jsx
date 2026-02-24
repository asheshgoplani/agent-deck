import { useState, useEffect, useRef } from "react";

// ─── Data Models ───────────────────────────────────────────────

const KANBAN_COLUMNS = ["backlog", "planning", "running", "review", "done"];

// Agent status — derived from tmux output parsing (Agent Deck pattern)
const AGENT_STATUS = {
  thinking: { icon: "●", color: "#e8a932", label: "thinking", pulse: true },
  waiting:  { icon: "◐", color: "#f59e0b", label: "waiting", pulse: true },
  running:  { icon: "⟳", color: "#e8a932", label: "running", pulse: false },
  idle:     { icon: "○", color: "#4a5368", label: "idle", pulse: false },
  error:    { icon: "✕", color: "#f06060", label: "error", pulse: false },
  complete: { icon: "✓", color: "#2dd4a0", label: "complete", pulse: false },
};

// Sessions no longer store message arrays — tmux scrollback IS the conversation.
// Sessions track metadata: phase, duration, artifact, Claude session UUID.
const TASKS = [
  {
    id: "t-007", project: "web-app", msg: "Fix auth token expiry",
    status: "running", time: "2m ago", branch: "claude/fix-auth",
    phase: "execute", skills: ["superpowers", "code-review"],
    mcps: ["github", "web-search"],
    tmuxSession: "ad-web-app-fix-auth",
    agentStatus: "thinking",
    sessions: [
      { id: "s-007-1", phase: "brainstorm", status: "complete", duration: "4m", artifact: "docs/designs/fix-auth-token-expiry.md", claudeSessionId: "a1b2c3d4-...", summary: "Root cause: refreshToken() missing expiry validation" },
      { id: "s-007-2", phase: "plan", status: "complete", duration: "2m", artifact: "docs/plans/fix-auth-plan.md", claudeSessionId: "e5f6g7h8-...", summary: "3 tasks: isTokenExpired utility, wire refresh, mutex" },
      { id: "s-007-3", phase: "execute", status: "active", duration: "3m...", artifact: null, claudeSessionId: "24d59a28-f02d-4557-be35-5ae804f1df91", summary: "Task 1/3 complete. Task 2 in progress." },
    ],
    // Simulated tmux pane output for preview
    tmuxOutput: [
      "  Dispatching Task 1 to fresh subagent: Create isTokenExpired utility + tests...",
      "",
      "  ✓ RED: Writing failing test for token expiry...",
      "    Created src/auth/__tests__/token-expiry.test.ts (+18 lines)",
      "    ✗ 1 test failed (expected)",
      "",
      "  ✓ GREEN: Implementing isTokenExpired...",
      "    Created src/auth/utils.ts (+12 lines)",
      "    ✓ 24 tests passed (1 new)",
      "",
      "  ✓ Code review: Task 1 passes spec compliance ✓ and code quality ✓",
      "",
      "  Task 1 complete. Dispatching Task 2: Wire into refreshToken...",
      "",
      "  RED: Writing failing test for refresh pre-check...",
      "    Modified src/auth/__tests__/refresh.test.ts (+14 lines)",
    ],
    tmuxPrompt: "thinking", // "thinking" | "waiting" | "prompt"
  },
  {
    id: "t-006", project: "web-app", msg: "Refactor nav component to use slots",
    status: "review", time: "14m ago", branch: "claude/refactor-nav",
    phase: "review", skills: ["superpowers", "feature-dev"],
    mcps: ["github"],
    diff: { files: 4, add: 47, del: 12 },
    tmuxSession: "ad-web-app-refactor-nav",
    agentStatus: "complete",
    sessions: [
      { id: "s-006-1", phase: "brainstorm", status: "complete", duration: "3m", artifact: "docs/designs/nav-slots.md", claudeSessionId: "x1...", summary: "Redesign nav to use slot pattern" },
      { id: "s-006-2", phase: "plan", status: "complete", duration: "2m", artifact: "docs/plans/nav-slots-plan.md", claudeSessionId: "x2...", summary: "5 tasks: extract slot interface, migrate, test" },
      { id: "s-006-3", phase: "execute", status: "complete", duration: "8m", artifact: null, claudeSessionId: "x3...", summary: "All 5 tasks complete. 47 added, 12 removed." },
      { id: "s-006-4", phase: "review", status: "active", duration: "—", artifact: null, claudeSessionId: "x4...", summary: "Awaiting human review. 4 files changed." },
    ],
    tmuxOutput: [
      "  All 5 tasks implemented and reviewed. Summary:",
      "  • NavHeader now accepts headerSlot prop",
      "  • NavFooter now accepts footerSlot prop",
      "  • 12 tests added, all passing",
      "  • Storybook stories updated with slot examples",
      "",
      "  Ready for human review.",
    ],
    tmuxPrompt: "prompt",
  },
  {
    id: "t-005", project: "api-service", msg: "Add /users CRUD endpoints",
    status: "planning", time: "15m ago", branch: null,
    phase: "plan", skills: ["superpowers"], mcps: [],
    tmuxSession: "ad-api-svc-users-crud",
    agentStatus: "waiting",
    askQuestion: "What authentication model? JWT, API key, or session-based?",
    sessions: [
      { id: "s-005-1", phase: "brainstorm", status: "complete", duration: "6m", artifact: "docs/designs/users-crud.md", claudeSessionId: "y1...", summary: "REST API design with pagination, filtering, soft delete" },
      { id: "s-005-2", phase: "plan", status: "active", duration: "1m...", artifact: null, claudeSessionId: "y2...", summary: "Building plan... waiting for input" },
    ],
    tmuxOutput: [
      "  Design loaded from docs/designs/users-crud.md",
      "",
      "  Breaking into implementation tasks...",
      "  Before I create the plan, I need to confirm a few things:",
      "",
      "  What authentication model should I use? The design mentions JWT from the",
      "  token work, but I want to confirm:",
      "  - JWT (you mentioned existing middleware)",
      "  - API key (simpler for service-to-service)",
      "  - Session-based (traditional, but less common for APIs)",
    ],
    tmuxPrompt: "waiting",
  },
  {
    id: "t-004", project: "docs-site", msg: "Generate API reference from OpenAPI spec",
    status: "done", time: "1h ago", branch: "claude/api-docs", phase: "done",
    skills: ["documents-manager"], mcps: ["github"],
    tmuxSession: "ad-docs-api-ref",
    agentStatus: "complete",
    sessions: [
      { id: "s-004-1", phase: "brainstorm", status: "complete", duration: "2m", artifact: "docs/designs/api-ref.md", claudeSessionId: "z1...", summary: "Auto-generate from openapi.yaml using redoc" },
      { id: "s-004-2", phase: "plan", status: "complete", duration: "1m", artifact: null, claudeSessionId: "z2...", summary: "2 tasks" },
      { id: "s-004-3", phase: "execute", status: "complete", duration: "4m", artifact: null, claudeSessionId: "z3...", summary: "API reference live at /api-docs" },
    ],
    tmuxOutput: ["  ✓ API reference generated and deployed to /api-docs"],
    tmuxPrompt: "prompt",
  },
  {
    id: "t-003", project: "web-app", msg: "Update deps, fix breaking changes",
    status: "done", time: "3h ago", branch: "claude/deps-update", phase: "done",
    skills: ["testing-expert"], mcps: [],
    tmuxSession: "ad-web-app-deps",
    agentStatus: "complete",
    sessions: [
      { id: "s-003-1", phase: "execute", status: "complete", duration: "12m", artifact: null, claudeSessionId: "w1...", summary: "Updated 14 packages, fixed 3 breaking changes" },
    ],
    tmuxOutput: ["  ✓ 14 packages updated, 3 breaking changes resolved"],
    tmuxPrompt: "prompt",
  },
  {
    id: "t-008", project: "api-service", msg: "Migrate auth to OAuth2 flow",
    status: "backlog", time: "20m ago", branch: null, phase: "backlog",
    skills: [], mcps: [], sessions: [],
    tmuxSession: null, agentStatus: "idle",
    tmuxOutput: [], tmuxPrompt: null,
  },
  {
    id: "t-009", project: "web-app", msg: "Add dark mode toggle to settings",
    status: "backlog", time: "25m ago", branch: null, phase: "backlog",
    skills: [], mcps: [], sessions: [],
    tmuxSession: null, agentStatus: "idle",
    tmuxOutput: [], tmuxPrompt: null,
  },
];

// Workspaces — OpenTofu provisioned (replaces PROJECTS/devcontainers)
const WORKSPACES = [
  { name: "web-app", desc: "Next.js frontend", template: "claude-sandbox", status: "running", path: "/workspace/web-app", cpu: "2.0", mem: "2GB", container: "coder-james-web-app" },
  { name: "api-service", desc: "FastAPI backend", template: "claude-sandbox", status: "running", path: "/workspace/api-service", cpu: "2.0", mem: "2GB", container: "coder-james-api-service" },
  { name: "infra", desc: "OpenTofu + Docker", template: "claude-sandbox", status: "stopped", path: "/workspace/infra", cpu: "1.0", mem: "1GB", container: "coder-james-infra" },
  { name: "docs-site", desc: "Documentation", template: "claude-sandbox", status: "running", path: "/workspace/docs-site", cpu: "1.0", mem: "1GB", container: "coder-james-docs-site" },
];

// Conductor — separate long-running Claude instance
const CONDUCTOR = {
  name: "conductor-ops",
  status: "connected",
  tmuxSession: "ad-conductor-ops",
  claudeSessionId: "cond-9a8b7c6d-...",
  heartbeatInterval: "60s",
  autoApprove: ["docs-site"],
  monitoredSessions: 5,
  mcps: ["github"],
};

const CONDUCTOR_LOG = [
  { time: "14:32", type: "check", msg: "Heartbeat: 3 agents healthy, 0 errors" },
  { time: "14:20", type: "ask", msg: "t-005 waiting: Agent asked 'What authentication model?'" },
  { time: "14:17", type: "action", msg: "Auto-approved t-004 (docs-site) — tests pass, diff clean" },
  { time: "14:15", type: "alert", msg: "t-006 review ready — 4 files, needs human approval (auth-adjacent)" },
  { time: "14:02", type: "route", msg: "Routed 'Add /users CRUD' → api-service (keyword: CRUD, endpoints)" },
  { time: "13:45", type: "spawn", msg: "Started agent for t-007 in web-app/claude/fix-auth worktree" },
];

// ─── Style Constants ───────────────────────────────────────────

const C = {
  bg: "#0b0d11", bgCard: "#10131a", bgPanel: "#0e1018",
  border: "#1a1e2a", borderLight: "#222838",
  text: "#dce4f0", textMid: "#8b95aa", textDim: "#4a5368",
  amber: "#e8a932", amberDim: "#c48a1a",
  green: "#2dd4a0", red: "#f06060", purple: "#8b8cf8",
  blue: "#4ca8e8", orange: "#f59e0b",
  termBg: "#080a0e", termText: "#c8d0dc",
};

const phaseColors = {
  brainstorm: "#c084fc", plan: "#8b8cf8", execute: "#e8a932", review: "#4ca8e8", done: "#2dd4a0",
};

const statusMap = {
  backlog: { color: C.textDim, icon: "○", label: "backlog" },
  planning: { color: "#8b8cf8", icon: "◈", label: "planning" },
  running: { color: C.amber, icon: "⟳", label: "running" },
  review: { color: C.blue, icon: "◉", label: "review" },
  done: { color: C.green, icon: "✓", label: "done" },
};

const mono = "'JetBrains Mono', 'Fira Code', monospace";
const sans = "'IBM Plex Sans', -apple-system, sans-serif";

// ─── Small Components ──────────────────────────────────────────

const Badge = ({ status, small }) => {
  const s = statusMap[status] || statusMap.backlog;
  return (
    <span style={{
      display: "inline-flex", alignItems: "center", gap: 3,
      fontSize: small ? 9 : 10, fontFamily: mono, color: s.color,
      textTransform: "uppercase", letterSpacing: "0.06em",
    }}>
      <span style={{ fontSize: small ? 9 : 11, ...(status === "running" ? { animation: "spin 2s linear infinite" } : {}) }}>{s.icon}</span>
      {s.label}
    </span>
  );
};

const AgentStatusBadge = ({ agentStatus, small }) => {
  const s = AGENT_STATUS[agentStatus] || AGENT_STATUS.idle;
  return (
    <span style={{
      display: "inline-flex", alignItems: "center", gap: 3,
      fontSize: small ? 9 : 10, fontFamily: mono, color: s.color,
      textTransform: "uppercase", letterSpacing: "0.06em",
      ...(s.pulse ? { animation: "pulse 1.5s ease-in-out infinite" } : {}),
    }}>
      <span style={{ fontSize: small ? 9 : 11 }}>{s.icon}</span>
      {s.label}
    </span>
  );
};

// ─── Session Chain ─────────────────────────────────────────────

const SessionChain = ({ task, activeSessionId, onSelectSession }) => {
  if (!task.sessions || task.sessions.length === 0) return null;

  return (
    <div style={{
      display: "flex", alignItems: "center", gap: 0, padding: "8px 12px",
      borderBottom: `1px solid ${C.border}`, background: C.bgPanel,
      overflowX: "auto",
    }}>
      {task.sessions.map((session, i) => {
        const isActive = session.id === activeSessionId;
        const color = phaseColors[session.phase] || C.textDim;
        return (
          <div key={session.id} style={{ display: "flex", alignItems: "center" }}>
            {i > 0 && (
              <div style={{
                width: 20, height: 1, background: session.status === "complete" ? C.green + "60" : C.border,
              }} />
            )}
            <button onClick={() => onSelectSession(session.id)} style={{
              display: "flex", flexDirection: "column", alignItems: "center", gap: 2,
              padding: "4px 10px", borderRadius: 5, cursor: "pointer",
              background: isActive ? color + "18" : "transparent",
              border: `1px solid ${isActive ? color + "50" : "transparent"}`,
              transition: "all 0.15s", minWidth: 70,
            }}>
              <div style={{ display: "flex", alignItems: "center", gap: 4 }}>
                <div style={{
                  width: 8, height: 8, borderRadius: "50%",
                  background: session.status === "complete" ? color : "transparent",
                  border: `2px solid ${color}`,
                  ...(session.status === "active" ? { boxShadow: `0 0 6px ${color}60` } : {}),
                }} />
                <span style={{ fontFamily: mono, fontSize: 10, color: isActive ? color : C.textMid, fontWeight: isActive ? 600 : 400 }}>
                  {session.phase}
                </span>
              </div>
              <span style={{ fontFamily: mono, fontSize: 8, color: C.textDim }}>
                {session.duration}
              </span>
              {session.artifact && (
                <span style={{ fontFamily: mono, fontSize: 7, color: C.textDim, maxWidth: 80, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  {session.artifact.split("/").pop()}
                </span>
              )}
            </button>
          </div>
        );
      })}
    </div>
  );
};

// ─── tmux Preview Pane ─────────────────────────────────────────
// Replaces V3's SessionViewer. Shows live terminal output from the
// agent's tmux session, matching Agent Deck's preview pattern.

const TmuxPreview = ({ task, session }) => {
  if (!task) return (
    <div style={{ flex: 1, display: "flex", alignItems: "center", justifyContent: "center" }}>
      <div style={{ textAlign: "center" }}>
        <div style={{ fontFamily: mono, fontSize: 28, color: C.textDim, marginBottom: 8 }}>⟐</div>
        <div style={{ fontFamily: mono, fontSize: 11, color: C.textDim }}>Select an agent to preview</div>
      </div>
    </div>
  );

  const agentSt = AGENT_STATUS[task.agentStatus] || AGENT_STATUS.idle;
  const workspace = WORKSPACES.find(w => w.name === task.project);
  const outputLines = task.tmuxOutput || [];
  const hasScrollback = outputLines.length > 12;
  const visibleLines = hasScrollback ? outputLines.slice(-12) : outputLines;

  return (
    <div style={{ flex: 1, display: "flex", flexDirection: "column", overflow: "hidden" }}>
      {/* ── PREVIEW header ── */}
      <div style={{
        padding: "8px 14px", borderBottom: `1px solid ${C.border}`,
        background: C.bgPanel, fontFamily: mono, fontSize: 10,
      }}>
        <div style={{
          fontSize: 8, color: C.textDim, textTransform: "uppercase",
          letterSpacing: "0.1em", marginBottom: 6,
        }}>Preview</div>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
          <div>
            <span style={{ fontSize: 13, fontWeight: 600, color: C.blue }}>{task.project}</span>
            <span style={{ color: agentSt.color, marginLeft: 8, ...(agentSt.pulse ? { animation: "pulse 1.5s ease-in-out infinite" } : {}) }}>
              {agentSt.icon} {agentSt.label}
            </span>
          </div>
          {task.agentStatus === "waiting" && task.askQuestion && (
            <span style={{
              padding: "2px 8px", borderRadius: 3,
              background: C.orange + "20", border: `1px solid ${C.orange}40`,
              color: C.orange, fontSize: 9, fontWeight: 600,
            }}>NEEDS INPUT</span>
          )}
        </div>
        <div style={{ color: C.textDim, fontSize: 9, marginTop: 2 }}>
          📁 {workspace?.path || `/workspace/${task.project}`}
          <span style={{ marginLeft: 8 }}>⏱ active {task.time}</span>
        </div>
        {task.skills.length > 0 && (
          <div style={{ display: "flex", gap: 4, marginTop: 4, flexWrap: "wrap" }}>
            {task.skills.map(s => (
              <span key={s} style={{
                padding: "1px 6px", borderRadius: 2, fontSize: 8,
                background: C.purple + "15", color: C.purple,
              }}>{s}</span>
            ))}
          </div>
        )}
      </div>

      {/* ── Claude metadata section ── */}
      <div style={{
        padding: "6px 14px", borderBottom: `1px solid ${C.border}`,
        background: C.bgPanel, fontFamily: mono, fontSize: 9, color: C.textDim,
      }}>
        <div style={{ display: "flex", flexWrap: "wrap", gap: 12 }}>
          <span>
            <span style={{ color: C.textDim }}>Status: </span>
            <span style={{ color: task.tmuxSession ? C.green : C.red }}>
              {task.tmuxSession ? "● Connected" : "○ Disconnected"}
            </span>
          </span>
          {session?.claudeSessionId && (
            <span>
              <span style={{ color: C.textDim }}>Session: </span>
              <span style={{ color: C.textMid }}>{session.claudeSessionId}</span>
            </span>
          )}
        </div>
        {task.mcps.length > 0 && (
          <div style={{ marginTop: 2 }}>
            <span style={{ color: C.textDim }}>MCPs: </span>
            {task.mcps.map(m => (
              <span key={m} style={{ color: C.textMid, marginRight: 6 }}>{m} ×</span>
            ))}
          </div>
        )}
        <div style={{ marginTop: 2, color: C.textDim }}>
          Fork: <span style={{ color: C.textMid }}>f quick fork, F fork with options</span>
        </div>
      </div>

      {/* ── Terminal output section ── */}
      <div style={{
        flex: 1, overflowY: "auto", background: C.termBg,
        fontFamily: mono, fontSize: 11, lineHeight: 1.6,
        padding: 0,
      }}>
        {/* Output header */}
        <div style={{
          textAlign: "center", padding: "6px 0",
          borderBottom: `1px solid ${C.border}`,
          color: C.textDim, fontSize: 9, letterSpacing: "0.08em",
        }}>Output</div>

        {/* Scrollback indicator */}
        {hasScrollback && (
          <div style={{
            padding: "4px 14px", fontStyle: "italic", color: C.textDim, fontSize: 9,
          }}>
            ⋮ {outputLines.length - 12} more lines above
          </div>
        )}

        {/* Terminal lines */}
        <div style={{ padding: "4px 14px" }}>
          {visibleLines.map((line, i) => {
            // Colour code certain patterns
            let color = C.termText;
            if (line.includes("✓")) color = C.green;
            if (line.includes("✗")) color = C.red;
            if (line.includes("Created") || line.includes("Modified")) color = C.blue;
            if (line.includes("Task") && line.includes("complete")) color = C.green;
            if (line.includes("Dispatching") || line.includes("Breaking")) color = C.amber;
            if (line.includes("review") || line.includes("Review")) color = C.blue;
            if (line.includes("?")) color = C.orange;
            if (line.trim() === "") return <div key={i} style={{ height: 8 }} />;
            return (
              <div key={i} style={{ color, whiteSpace: "pre-wrap" }}>{line}</div>
            );
          })}
        </div>

        {/* Prompt state */}
        <div style={{ padding: "8px 14px", borderTop: `1px solid ${C.border}15` }}>
          {task.tmuxPrompt === "thinking" && (
            <div style={{ color: C.amber }}>
              <span style={{ marginRight: 6 }}>·</span>
              Whirring… <span style={{ color: C.textDim }}>(esc to interrupt)</span>
            </div>
          )}
          {task.tmuxPrompt === "waiting" && (
            <div>
              <div style={{ color: C.orange, marginBottom: 4 }}>
                {task.askQuestion || "Waiting for input..."}
              </div>
              <span style={{ color: C.textDim }}>{">"} </span>
              <span style={{ borderLeft: `2px solid ${C.orange}`, animation: "blink 1s step-end infinite", marginLeft: 2 }}>&nbsp;</span>
            </div>
          )}
          {task.tmuxPrompt === "prompt" && (
            <div>
              <span style={{ color: C.textDim }}>{">"} </span>
              <span style={{ borderLeft: `2px solid ${C.textDim}40`, marginLeft: 2 }}>&nbsp;</span>
            </div>
          )}
        </div>

        {/* Permission mode indicator */}
        <div style={{
          padding: "4px 14px 8px",
          display: "flex", justifyContent: "flex-end",
        }}>
          <span style={{
            fontSize: 8, color: C.textDim, fontFamily: mono,
          }}>▸▸ bypass permissions on <span style={{ color: C.textDim }}>(shift+tab to cycle)</span></span>
        </div>
      </div>
    </div>
  );
};

// ─── Agent Card ────────────────────────────────────────────────

const AgentCard = ({ task, isActive, onClick, compact }) => {
  const isWaiting = task.agentStatus === "waiting";
  return (
    <div onClick={onClick} style={{
      padding: compact ? "6px 10px" : "10px 14px",
      borderLeft: `3px solid ${(statusMap[task.status] || statusMap.backlog).color}`,
      background: isActive ? C.bgCard : "transparent",
      cursor: "pointer", transition: "background 0.1s",
      borderBottom: `1px solid ${C.border}`,
      ...(isWaiting ? { borderLeftColor: C.orange } : {}),
    }}>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 3 }}>
        <span style={{ fontFamily: mono, fontSize: 10, color: C.blue }}>{task.project}</span>
        <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
          {isWaiting && (
            <span style={{
              padding: "1px 5px", borderRadius: 2, fontSize: 8, fontWeight: 600,
              background: C.orange + "20", color: C.orange,
              animation: "pulse 1.5s ease-in-out infinite",
            }}>◐ INPUT</span>
          )}
          <AgentStatusBadge agentStatus={task.agentStatus} small />
        </div>
      </div>
      <div style={{
        fontFamily: sans, fontSize: compact ? 11 : 12, color: C.text,
        lineHeight: 1.3, marginBottom: 4,
      }}>{task.msg}</div>
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
        <span style={{ fontFamily: mono, fontSize: 9, color: C.textDim }}>{task.id} · {task.time}</span>
        {/* Mini session chain */}
        {task.sessions.length > 0 && (
          <div style={{ display: "flex", gap: 2 }}>
            {task.sessions.map(s => (
              <div key={s.id} style={{
                width: 12, height: 3, borderRadius: 1,
                background: s.status === "complete"
                  ? C.green
                  : s.status === "active" ? phaseColors[s.phase] : C.border,
              }} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
};

// ─── Context-Aware Chat Input ──────────────────────────────────
// V4: Brainstorm mode removed (backlogged). Dual slash command palette.

const CHAT_MODES = {
  reply:     { icon: "↩", color: "#e8a932", label: "Reply" },
  new:       { icon: "+", color: "#4ca8e8", label: "New task" },
  conductor: { icon: "◎", color: "#8b8cf8", label: "Conductor" },
};

// Dual command palette: Hub commands + Claude Code passthrough + Skills
const HUB_COMMANDS = [
  { cmd: "/new", desc: "Create new task (override reply)", group: "Hub" },
  { cmd: "/fork", desc: "Fork → new sibling task", group: "Hub" },
  { cmd: "/diff", desc: "View git diff for task", group: "Hub" },
  { cmd: "/approve", desc: "Approve and merge", group: "Hub" },
  { cmd: "/reject", desc: "Reject task changes", group: "Hub" },
  { cmd: "/status", desc: "All agent statuses", group: "Hub" },
  { cmd: "/sessions", desc: "List sessions for task", group: "Hub" },
  { cmd: "/conductor", desc: "Message conductor", group: "Hub" },
];

const CLAUDE_COMMANDS = [
  { cmd: "/compact", desc: "Compact conversation context", group: "Claude Code" },
  { cmd: "/permissions", desc: "Toggle bypass mode", group: "Claude Code" },
  { cmd: "/memory", desc: "View/edit CLAUDE.md", group: "Claude Code" },
  { cmd: "/cost", desc: "Token usage this session", group: "Claude Code" },
  { cmd: "/clear", desc: "Clear conversation", group: "Claude Code" },
];

const SKILL_COMMANDS = [
  { cmd: "/test", desc: "Run test suite", group: "Skills (web-app)" },
  { cmd: "/lint", desc: "Run linter", group: "Skills (web-app)" },
  { cmd: "/deploy", desc: "Deploy to staging", group: "Skills (web-app)" },
];

const SmartChatInput = ({ context, workspaces, onSend }) => {
  const [value, setValue] = useState("");
  const [showSlash, setShowSlash] = useState(false);
  const [showOverrideMenu, setShowOverrideMenu] = useState(false);
  const [manualOverride, setManualOverride] = useState(null);

  const contextKey = `${context.mode}-${context.taskId || ""}-${context.project || ""}`;
  const [lastContextKey, setLastContextKey] = useState(contextKey);
  if (contextKey !== lastContextKey) {
    if (manualOverride) setManualOverride(null);
    setLastContextKey(contextKey);
  }

  const active = manualOverride || context;
  const modeInfo = CHAT_MODES[active.mode] || CHAT_MODES.new;

  // Build the grouped command list — show all groups when in reply/project mode
  const isProjectMode = active.mode === "reply" || (active.mode === "new" && active.target);
  const allCommands = [
    ...HUB_COMMANDS,
    ...(isProjectMode ? CLAUDE_COMMANDS : []),
    ...(isProjectMode ? SKILL_COMMANDS : []),
  ];

  const overrideOptions = [];
  if (context.mode === "reply") {
    overrideOptions.push({ mode: "new", target: context.project, label: `+ New in ${context.project}`, desc: "New task, same project" });
  }
  overrideOptions.push({ mode: "new", target: null, label: "+ New (auto-route)", desc: "Conductor picks project" });
  workspaces.forEach(w => {
    if (w.name !== context.project) {
      overrideOptions.push({ mode: "new", target: w.name, label: `+ New in ${w.name}`, desc: w.desc });
    }
  });
  overrideOptions.push({ mode: "conductor", target: null, label: "◎ Message conductor", desc: "Orchestration commands" });
  if (manualOverride) {
    overrideOptions.unshift({ mode: "__auto__", target: null, label: `← Back to: ${context.label}`, desc: "Use auto-detected context" });
  }

  // AskUserQuestion surfacing in placeholder
  const placeholder = (() => {
    if (active.mode === "reply" && context.askQuestion) {
      return `Answer: ${context.askQuestion}`;
    }
    if (active.mode === "reply") return `Reply to ${active.taskId || "agent"} / ${active.sessionPhase || "session"}...`;
    if (active.mode === "new" && active.target) return `New task in ${active.target}...`;
    if (active.mode === "new") return "Describe a task (conductor will route)...";
    if (active.mode === "conductor") return "Message conductor...";
    return "Type a message...";
  })();

  return (
    <div style={{ position: "relative" }}>
      {/* Dual slash command palette */}
      {showSlash && (
        <div style={{
          position: "absolute", bottom: "100%", left: 0, right: 0,
          background: C.bgCard, border: `1px solid ${C.borderLight}`,
          borderRadius: "6px 6px 0 0", borderBottom: "none", maxHeight: 280, overflowY: "auto",
          zIndex: 10,
        }}>
          {(() => {
            const filtered = allCommands.filter(c => c.cmd.includes(value.toLowerCase()));
            const groups = [...new Set(filtered.map(c => c.group))];
            return groups.map(group => (
              <div key={group}>
                <div style={{
                  padding: "5px 14px 3px", fontFamily: mono, fontSize: 8,
                  color: C.textDim, textTransform: "uppercase", letterSpacing: "0.08em",
                  borderBottom: `1px solid ${C.border}`,
                  background: C.bgPanel,
                }}>
                  {group}
                </div>
                {filtered.filter(c => c.group === group).map(c => (
                  <button key={c.cmd} onClick={() => { setValue(c.cmd + " "); setShowSlash(false); }} style={{
                    display: "flex", justifyContent: "space-between", width: "100%",
                    padding: "6px 14px", background: "none", border: "none",
                    borderBottom: `1px solid ${C.border}`, cursor: "pointer", fontFamily: mono, fontSize: 11,
                  }}>
                    <span style={{ color: group === "Hub" ? C.amber : group.startsWith("Claude") ? C.purple : C.green }}>{c.cmd}</span>
                    <span style={{ color: C.textDim, fontSize: 9 }}>{c.desc}</span>
                  </button>
                ))}
              </div>
            ));
          })()}
        </div>
      )}

      {/* Override menu */}
      {showOverrideMenu && (
        <div style={{
          position: "absolute", bottom: "100%", left: 0, width: 260,
          background: C.bgCard, border: `1px solid ${C.borderLight}`,
          borderRadius: "6px 6px 0 0", borderBottom: "none", maxHeight: 280, overflowY: "auto",
          zIndex: 10,
        }}>
          <div style={{ padding: "6px 12px", borderBottom: `1px solid ${C.border}`, fontFamily: mono, fontSize: 9, color: C.textDim, textTransform: "uppercase", letterSpacing: "0.08em" }}>
            Switch context
          </div>
          {overrideOptions.map((opt, i) => (
            <button key={i} onClick={() => {
              if (opt.mode === "__auto__") setManualOverride(null);
              else setManualOverride({ mode: opt.mode, target: opt.target, label: opt.label });
              setShowOverrideMenu(false);
            }} style={{
              display: "flex", alignItems: "center", gap: 8, width: "100%",
              padding: "7px 14px", background: "none",
              border: "none", borderBottom: `1px solid ${C.border}`,
              cursor: "pointer", textAlign: "left",
            }}>
              <span style={{ fontFamily: mono, fontSize: 11, color: (CHAT_MODES[opt.mode] || CHAT_MODES.new).color, minWidth: 12, textAlign: "center" }}>
                {opt.mode === "__auto__" ? "←" : (CHAT_MODES[opt.mode] || {}).icon || "+"}
              </span>
              <div>
                <div style={{ fontFamily: mono, fontSize: 11, color: C.text }}>{opt.label}</div>
                <div style={{ fontFamily: mono, fontSize: 9, color: C.textDim }}>{opt.desc}</div>
              </div>
            </button>
          ))}
        </div>
      )}

      <div style={{ padding: "10px 12px", borderTop: `1px solid ${C.border}`, background: C.bg }}>
        {/* AskUserQuestion banner when agent is waiting */}
        {active.mode === "reply" && context.askQuestion && (
          <div style={{
            padding: "6px 10px", marginBottom: 6, borderRadius: 4,
            background: C.orange + "12", border: `1px solid ${C.orange}30`,
            fontFamily: mono, fontSize: 10, color: C.orange,
            display: "flex", alignItems: "center", gap: 6,
          }}>
            <span>◐</span>
            <span>Agent is asking: <span style={{ color: C.text }}>{context.askQuestion}</span></span>
          </div>
        )}
        <div style={{
          display: "flex", gap: 6, alignItems: "center",
          background: C.bgCard, borderRadius: 6,
          border: `1px solid ${C.borderLight}`,
          padding: "4px 4px 4px 4px",
        }}>
          <button onClick={() => { setShowOverrideMenu(!showOverrideMenu); if (showSlash) setShowSlash(false); }} style={{
            padding: "4px 8px", borderRadius: 4, cursor: "pointer",
            background: modeInfo.color + "12",
            border: `1px solid ${modeInfo.color}30`,
            display: "flex", alignItems: "center", gap: 4,
            fontFamily: mono, fontSize: 10, color: modeInfo.color,
            whiteSpace: "nowrap", flexShrink: 0,
            transition: "all 0.15s",
          }}>
            <span>{modeInfo.icon}</span>
            <span>{active.label || modeInfo.label}</span>
            {manualOverride && <span style={{ fontSize: 7, opacity: 0.6 }}>✎</span>}
            <span style={{ fontSize: 8, opacity: 0.5 }}>▾</span>
          </button>

          <input value={value}
            onChange={e => {
              setValue(e.target.value);
              setShowSlash(e.target.value.startsWith("/"));
              if (showOverrideMenu) setShowOverrideMenu(false);
            }}
            onKeyDown={e => {
              if (e.key === "Enter" && value.trim()) {
                onSend(value, active);
                setValue(""); setShowSlash(false);
              }
            }}
            placeholder={placeholder}
            style={{
              flex: 1, background: "none", border: "none", outline: "none",
              color: C.text, fontSize: 12, fontFamily: sans, minWidth: 0,
            }}
          />
          <button onClick={() => {
            if (value.trim()) { onSend(value, active); setValue(""); setShowSlash(false); }
          }} style={{
            padding: "6px 14px", borderRadius: 5, border: "none",
            background: value.trim() ? C.amber : C.border,
            color: value.trim() ? C.bg : C.textDim,
            fontFamily: mono, fontSize: 10, fontWeight: 600,
            cursor: value.trim() ? "pointer" : "default",
            transition: "all 0.15s", flexShrink: 0,
          }}>Send</button>
        </div>
        <div style={{ fontFamily: mono, fontSize: 8, color: C.textDim, marginTop: 3, paddingLeft: 2 }}>
          via tmux send-keys → {active.mode === "conductor" ? CONDUCTOR.tmuxSession : (context.tmuxSession || "new session")}
        </div>
      </div>
    </div>
  );
};

// ─── Sidebar ───────────────────────────────────────────────────

const Sidebar = ({ view, setView, isMobile }) => {
  const views = [
    { id: "agents", icon: "⟐", label: "Agents" },
    { id: "kanban", icon: "▦", label: "Kanban" },
    { id: "conductor", icon: "◎", label: "Conductor" },
    { id: "workspaces", icon: "▣", label: "Workspaces" },
    { id: "brainstorm", icon: "◇", label: "Brainstorm", backlog: true },
  ];

  return (
    <div style={{
      width: isMobile ? "100%" : 56,
      background: C.bgPanel, borderRight: isMobile ? "none" : `1px solid ${C.border}`,
      display: "flex", flexDirection: isMobile ? "row" : "column",
      ...(isMobile ? { borderBottom: `1px solid ${C.border}`, justifyContent: "space-around" } : {}),
    }}>
      <div style={{
        display: "flex", flexDirection: isMobile ? "row" : "column",
        gap: isMobile ? 0 : 2, padding: isMobile ? 0 : "8px 4px",
        ...(isMobile ? { flex: 1, justifyContent: "space-around" } : {}),
      }}>
        {views.map(v => (
          <button key={v.id} onClick={() => setView(v.id)} title={v.label} style={{
            display: "flex", flexDirection: "column", alignItems: "center", gap: 2,
            padding: isMobile ? "8px 12px" : "8px 4px",
            background: view === v.id ? C.amber + "12" : "transparent",
            border: "none", borderRadius: 4, cursor: "pointer",
            transition: "background 0.1s",
            opacity: v.backlog ? 0.3 : 1,
          }}>
            <span style={{
              fontSize: isMobile ? 16 : 18,
              color: view === v.id ? C.amber : C.textDim,
            }}>{v.icon}</span>
            <span style={{
              fontFamily: mono, fontSize: 7, color: view === v.id ? C.amber : C.textDim,
              textTransform: "uppercase", letterSpacing: "0.06em",
            }}>{v.label}</span>
          </button>
        ))}
      </div>
      {!isMobile && (
        <div style={{ marginTop: "auto", padding: "12px 8px", textAlign: "center" }}>
          <div style={{ fontFamily: mono, fontSize: 8, color: C.green }}>● nuc</div>
          <div style={{ fontFamily: mono, fontSize: 8, color: C.textDim }}>
            {TASKS.filter(t => t.tmuxSession && !["done", "backlog"].includes(t.status)).length} agents
          </div>
        </div>
      )}
    </div>
  );
};

// ─── Conductor View ────────────────────────────────────────────
// Conductor = separate long-running Claude instance (Agent Deck pattern)

const ConductorView = () => {
  const logTypeStyles = {
    check: { icon: "✓", color: C.green },
    action: { icon: "→", color: C.amber },
    alert: { icon: "⚠", color: C.red },
    route: { icon: "◈", color: C.purple },
    spawn: { icon: "⊕", color: C.blue },
    ask: { icon: "◐", color: C.orange },
  };

  return (
    <div style={{ flex: 1, overflowY: "auto", padding: 12 }}>
      {/* Conductor identity */}
      <div style={{
        display: "flex", alignItems: "center", gap: 12, marginBottom: 16,
        padding: 14, borderRadius: 8, background: C.bgCard, border: `1px solid ${C.border}`,
      }}>
        <div style={{
          width: 40, height: 40, borderRadius: "50%", display: "flex",
          alignItems: "center", justifyContent: "center", fontSize: 20,
          background: `linear-gradient(135deg, ${C.purple}40, ${C.blue}40)`,
        }}>◎</div>
        <div>
          <div style={{ fontFamily: mono, fontSize: 13, color: C.text, fontWeight: 600 }}>
            {CONDUCTOR.name}
            <span style={{
              marginLeft: 8, fontSize: 9, color: CONDUCTOR.status === "connected" ? C.green : C.red,
            }}>● {CONDUCTOR.status}</span>
          </div>
          <div style={{ fontFamily: mono, fontSize: 9, color: C.textDim, marginTop: 2 }}>
            Separate Claude instance · tmux: {CONDUCTOR.tmuxSession}
          </div>
          <div style={{ fontFamily: mono, fontSize: 9, color: C.textDim, display: "flex", gap: 12, marginTop: 2 }}>
            <span>heartbeat: {CONDUCTOR.heartbeatInterval}</span>
            <span>auto-approve: {CONDUCTOR.autoApprove.join(", ")}</span>
            <span>monitoring: {CONDUCTOR.monitoredSessions} sessions</span>
          </div>
        </div>
      </div>

      {/* Activity log */}
      <div style={{ fontFamily: mono, fontSize: 9, color: C.textDim, textTransform: "uppercase", marginBottom: 8, letterSpacing: "0.08em" }}>
        Activity Log
      </div>
      {CONDUCTOR_LOG.map((entry, i) => {
        const style = logTypeStyles[entry.type] || logTypeStyles.check;
        return (
          <div key={i} style={{
            padding: "8px 12px", marginBottom: 4, borderRadius: 4,
            background: C.bgCard, border: `1px solid ${C.border}`,
            display: "flex", alignItems: "flex-start", gap: 10,
            fontFamily: mono, fontSize: 11,
          }}>
            <span style={{ color: C.textDim, flexShrink: 0, fontSize: 10 }}>{entry.time}</span>
            <span style={{ color: style.color, flexShrink: 0 }}>{style.icon}</span>
            <span style={{ color: C.text, lineHeight: 1.4 }}>{entry.msg}</span>
          </div>
        );
      })}
    </div>
  );
};

// ─── Filter Bar (shared by Agents + Kanban) ────────────────────

const FilterBar = ({ projectFilter, setProjectFilter, groupByProject, setGroupByProject, projectNames, taskCount }) => (
  <div style={{
    display: "flex", alignItems: "center", gap: 6, padding: "6px 8px",
    borderBottom: `1px solid ${C.border}`, overflowX: "auto",
    flexShrink: 0,
  }}>
    <button onClick={() => setProjectFilter("all")} style={{
      padding: "3px 8px", borderRadius: 3, border: `1px solid ${C.border}`,
      background: projectFilter === "all" ? C.amber + "20" : "transparent",
      color: projectFilter === "all" ? C.amber : C.textDim,
      fontFamily: mono, fontSize: 9, cursor: "pointer",
      fontWeight: projectFilter === "all" ? 600 : 400,
      whiteSpace: "nowrap", flexShrink: 0,
    }}>All ({taskCount})</button>
    {projectNames.map(p => (
      <button key={p} onClick={() => setProjectFilter(projectFilter === p ? "all" : p)} style={{
        padding: "3px 8px", borderRadius: 3, border: `1px solid ${C.border}`,
        background: projectFilter === p ? C.amber + "20" : "transparent",
        color: projectFilter === p ? C.amber : C.textDim,
        fontFamily: mono, fontSize: 9, cursor: "pointer",
        fontWeight: projectFilter === p ? 600 : 400,
        whiteSpace: "nowrap", flexShrink: 0,
      }}>{p}</button>
    ))}
    <div style={{ flex: 1 }} />
    <button onClick={() => setGroupByProject(!groupByProject)} style={{
      padding: "3px 8px", borderRadius: 3, border: `1px solid ${C.border}`,
      background: groupByProject ? C.purple + "15" : "transparent",
      color: groupByProject ? C.purple : C.textDim,
      fontFamily: mono, fontSize: 9, cursor: "pointer",
      display: "flex", alignItems: "center", gap: 4,
      whiteSpace: "nowrap", flexShrink: 0,
    }}>
      <span>{groupByProject ? "▤" : "▥"}</span>
      Group
    </button>
  </div>
);

// ─── Kanban View ───────────────────────────────────────────────

const KanbanView = ({ tasks, activeTask, setActiveTask, projectFilter, setProjectFilter, groupByProject, setGroupByProject, projectNames }) => {
  const kanbanStatusMap = { backlog: "backlog", planning: "planning", running: "running", review: "review", done: "done" };

  return (
    <div style={{ flex: 1, display: "flex", flexDirection: "column", overflow: "hidden" }}>
      <FilterBar projectFilter={projectFilter} setProjectFilter={setProjectFilter}
        groupByProject={groupByProject} setGroupByProject={setGroupByProject}
        projectNames={projectNames} taskCount={tasks.length} />
      <div style={{ flex: 1, display: "flex", overflowX: "auto", gap: 1 }}>
        {KANBAN_COLUMNS.map(col => {
          const colTasks = tasks.filter(t => t.status === col);
          return (
            <div key={col} style={{
              flex: 1, minWidth: 160, display: "flex", flexDirection: "column",
              borderRight: `1px solid ${C.border}`,
            }}>
              <div style={{
                padding: "8px 10px", borderBottom: `1px solid ${C.border}`,
                display: "flex", alignItems: "center", gap: 6,
                fontFamily: mono, fontSize: 9, textTransform: "uppercase",
                letterSpacing: "0.06em", color: (statusMap[col] || statusMap.backlog).color,
              }}>
                <span>{(statusMap[col] || statusMap.backlog).icon}</span>
                {col}
                <span style={{ color: C.textDim, fontSize: 8 }}>{colTasks.length}</span>
              </div>
              <div style={{ flex: 1, overflowY: "auto", padding: 4 }}>
                {colTasks.length === 0 && (
                  <div style={{ textAlign: "center", padding: 12, color: C.textDim, fontFamily: mono, fontSize: 10 }}>—</div>
                )}
                {groupByProject ? (() => {
                  const projects = [...new Set(colTasks.map(t => t.project))];
                  return projects.map(p => (
                    <div key={p}>
                      <div style={{ padding: "4px 8px", fontFamily: mono, fontSize: 8, color: C.amber, fontWeight: 600 }}>▣ {p}</div>
                      {colTasks.filter(t => t.project === p).map(t => (
                        <AgentCard key={t.id} task={t} compact isActive={activeTask?.id === t.id} onClick={() => setActiveTask(t)} />
                      ))}
                    </div>
                  ));
                })() : colTasks.map(t => (
                  <AgentCard key={t.id} task={t} compact isActive={activeTask?.id === t.id} onClick={() => setActiveTask(t)} />
                ))}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
};

// ─── Main App ──────────────────────────────────────────────────

export default function SaltboxHubV4() {
  const [view, setView] = useState("agents");
  const [activeTask, setActiveTask] = useState(null);
  const [activeSession, setActiveSession] = useState(null);
  const [isMobile, setIsMobile] = useState(false);
  const [projectFilter, setProjectFilter] = useState("all");
  const [groupByProject, setGroupByProject] = useState(true);
  const [showSidebar, setShowSidebar] = useState(false);

  const projectNames = [...new Set(TASKS.map(t => t.project))];
  const filteredTasks = projectFilter === "all" ? TASKS : TASKS.filter(t => t.project === projectFilter);

  useEffect(() => {
    const check = () => setIsMobile(window.innerWidth < 768);
    check(); window.addEventListener("resize", check);
    return () => window.removeEventListener("resize", check);
  }, []);

  const handleTaskSelect = (task) => {
    setActiveTask(task);
    if (task.sessions.length > 0) {
      setActiveSession(task.sessions[task.sessions.length - 1].id);
    }
  };

  const currentSession = activeTask?.sessions?.find(s => s.id === activeSession) || null;

  // ─── Compute chat context from navigation state ───
  const computeChatContext = () => {
    if ((view === "agents" || view === "kanban") && activeTask) {
      const latestSession = activeTask.sessions?.[activeTask.sessions.length - 1];
      if (latestSession && latestSession.status === "active") {
        return {
          mode: "reply",
          project: activeTask.project,
          taskId: activeTask.id,
          sessionPhase: latestSession.phase,
          tmuxSession: activeTask.tmuxSession,
          askQuestion: activeTask.askQuestion || null,
          label: `${activeTask.id} / ${latestSession.phase}`,
        };
      }
      return { mode: "new", target: activeTask.project, label: activeTask.project };
    }
    if ((view === "agents" || view === "kanban") && projectFilter !== "all") {
      return { mode: "new", target: projectFilter, label: projectFilter };
    }
    if (view === "agents" || view === "kanban") {
      return { mode: "new", target: null, label: "auto-route" };
    }
    if (view === "conductor") {
      return { mode: "conductor", label: CONDUCTOR.name };
    }
    return { mode: "new", target: null, label: "auto-route" };
  };

  const chatContext = computeChatContext();

  return (
    <div style={{
      height: "100vh", width: "100vw", display: "flex", flexDirection: "column",
      background: C.bg, color: C.text, fontFamily: sans,
      overflow: "hidden",
    }}>
      {/* Global styles */}
      <style>{`
        @keyframes spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
        @keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.5; } }
        @keyframes blink { 50% { opacity: 0; } }
        * { box-sizing: border-box; scrollbar-width: thin; scrollbar-color: ${C.border} transparent; }
        ::-webkit-scrollbar { width: 4px; height: 4px; }
        ::-webkit-scrollbar-track { background: transparent; }
        ::-webkit-scrollbar-thumb { background: ${C.border}; border-radius: 2px; }
      `}</style>

      <div style={{ flex: 1, display: "flex", overflow: "hidden" }}>
        {/* Sidebar */}
        {(!isMobile || showSidebar) && (
          <Sidebar view={view} setView={(v) => { setView(v); if (isMobile) setShowSidebar(false); }} isMobile={isMobile} />
        )}

        <div style={{ flex: 1, display: "flex", flexDirection: "column", overflow: "hidden" }}>
          {/* Top bar */}
          <div style={{
            display: "flex", alignItems: "center", justifyContent: "space-between",
            padding: "6px 14px", borderBottom: `1px solid ${C.border}`,
            background: C.bgPanel, flexShrink: 0,
          }}>
            <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
              {isMobile && (
                <button onClick={() => setShowSidebar(!showSidebar)} style={{
                  background: "none", border: "none", color: C.textDim, fontSize: 16, cursor: "pointer", padding: "2px 4px",
                }}>☰</button>
              )}
              <span style={{ fontFamily: mono, fontSize: 10, color: C.textDim, textTransform: "uppercase", letterSpacing: "0.08em" }}>
                {view}
              </span>
              {activeTask && (view === "agents" || view === "kanban") && (
                <>
                  <span style={{ color: C.textDim, fontSize: 10 }}>/</span>
                  <span style={{ fontFamily: mono, fontSize: 10, color: C.blue }}>{activeTask.project}</span>
                  <span style={{ color: C.textDim, fontSize: 10 }}>/</span>
                  <span style={{ fontFamily: mono, fontSize: 10, color: C.textMid }}>{activeTask.id}</span>
                  <span style={{ color: C.textDim, fontSize: 10 }}>/</span>
                  <span style={{
                    fontFamily: mono, fontSize: 9, padding: "1px 6px", borderRadius: 3,
                    background: (phaseColors[activeTask.phase] || C.textDim) + "20",
                    color: phaseColors[activeTask.phase] || C.textDim,
                  }}>{activeTask.phase}</span>
                </>
              )}
            </div>

            <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
              {activeTask && activeTask.tmuxSession && !isMobile && (
                <>
                  <button title={`Attach to ${activeTask.tmuxSession}`} style={{
                    padding: "2px 8px", borderRadius: 3, border: `1px solid ${C.green}40`,
                    background: C.green + "10", color: C.green, cursor: "pointer",
                    fontFamily: mono, fontSize: 9, fontWeight: 600,
                  }}>▶ Attach</button>
                  <button title={`SSH into ${activeTask.project}`} style={{
                    padding: "2px 8px", borderRadius: 3, border: `1px solid ${C.border}`,
                    background: "transparent", color: C.textDim, cursor: "pointer", fontFamily: mono, fontSize: 9,
                  }}>⊞ SSH</button>
                  <button title={`Open ${activeTask.project} in code-server`} style={{
                    padding: "2px 8px", borderRadius: 3, border: `1px solid ${C.border}`,
                    background: "transparent", color: C.textDim, cursor: "pointer", fontFamily: mono, fontSize: 9,
                  }}>⟨⟩ IDE</button>
                </>
              )}
              <span style={{ fontFamily: mono, fontSize: 9, color: C.green }}>● {TASKS.filter(t => t.tmuxSession && !["done","backlog"].includes(t.status)).length}</span>
            </div>
          </div>

          {/* Main content */}
          <div style={{ flex: 1, display: "flex", overflow: "hidden" }}>
            {/* ═══ AGENTS VIEW ═══ */}
            {view === "agents" && (
              <>
                {/* Task list panel */}
                <div style={{
                  width: isMobile ? "100%" : 280, flexShrink: 0,
                  borderRight: `1px solid ${C.border}`,
                  display: "flex", flexDirection: "column", overflow: "hidden",
                  ...(isMobile && activeTask ? { display: "none" } : {}),
                }}>
                  <FilterBar projectFilter={projectFilter} setProjectFilter={setProjectFilter}
                    groupByProject={groupByProject} setGroupByProject={setGroupByProject}
                    projectNames={projectNames} taskCount={filteredTasks.length} />
                  <div style={{ flex: 1, overflowY: "auto" }}>
                    {(() => {
                      const activeTasks = filteredTasks.filter(t => !["done"].includes(t.status));
                      const completedTasks = filteredTasks.filter(t => t.status === "done");

                      const renderGroup = (tasks) => {
                        if (groupByProject) {
                          const projects = [...new Set(tasks.map(t => t.project))];
                          return projects.map(p => (
                            <div key={p}>
                              <div style={{
                                padding: "6px 10px", fontFamily: mono, fontSize: 9, color: C.amber,
                                fontWeight: 600, borderLeft: `2px solid ${C.amber}30`,
                                display: "flex", alignItems: "center", gap: 6,
                              }}>
                                <span>▣ {p}</span>
                                <span style={{ color: C.textDim }}>· {tasks.filter(t => t.project === p).length}</span>
                              </div>
                              {tasks.filter(t => t.project === p).map(t => (
                                <AgentCard key={t.id} task={t} isActive={activeTask?.id === t.id} onClick={() => handleTaskSelect(t)} />
                              ))}
                            </div>
                          ));
                        }
                        return tasks.map(t => (
                          <AgentCard key={t.id} task={t} isActive={activeTask?.id === t.id} onClick={() => handleTaskSelect(t)} />
                        ));
                      };

                      return (
                        <>
                          <div style={{ padding: "6px 6px 4px", fontFamily: mono, fontSize: 9, color: C.textDim, textTransform: "uppercase", letterSpacing: "0.08em" }}>
                            Active · {activeTasks.length}
                          </div>
                          {renderGroup(activeTasks)}
                          {completedTasks.length > 0 && (
                            <>
                              <div style={{ padding: "12px 6px 4px", fontFamily: mono, fontSize: 9, color: C.textDim, textTransform: "uppercase", letterSpacing: "0.08em" }}>
                                Completed · {completedTasks.length}
                              </div>
                              {renderGroup(completedTasks)}
                            </>
                          )}
                        </>
                      );
                    })()}
                  </div>
                </div>

                {/* Detail / preview panel */}
                {(!isMobile || activeTask) && (
                  <div style={{ flex: 1, display: "flex", flexDirection: "column", overflow: "hidden" }}>
                    {isMobile && (
                      <button onClick={() => setActiveTask(null)} style={{
                        padding: "6px 12px", background: C.bgPanel, border: "none",
                        borderBottom: `1px solid ${C.border}`, cursor: "pointer",
                        fontFamily: mono, fontSize: 10, color: C.textDim, textAlign: "left",
                      }}>← Back</button>
                    )}

                    {/* Task header */}
                    {activeTask && (
                      <div style={{ padding: "10px 16px", borderBottom: `1px solid ${C.border}` }}>
                        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 4 }}>
                          <span style={{ fontFamily: sans, fontSize: 13, color: C.text, fontWeight: 500 }}>{activeTask.msg}</span>
                          <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
                            <Badge status={activeTask.status} />
                            {!isMobile && activeTask.tmuxSession && (
                              <button title={`Attach to ${activeTask.tmuxSession}`} style={{
                                padding: "2px 6px", borderRadius: 3, border: `1px solid ${C.green}40`,
                                background: C.green + "10", color: C.green, cursor: "pointer",
                                fontFamily: mono, fontSize: 9, fontWeight: 600,
                              }}>▶ Attach</button>
                            )}
                          </div>
                        </div>
                        <div style={{ display: "flex", alignItems: "center", gap: 6, fontFamily: mono, fontSize: 10, color: C.textDim, flexWrap: "wrap" }}>
                          <span>{activeTask.project} / {activeTask.id}</span>
                          {activeTask.branch && <span>→ {activeTask.branch}</span>}
                          {activeTask.tmuxSession && (
                            <span style={{ color: C.textDim, background: C.bgPanel, padding: "1px 6px", borderRadius: 2, fontSize: 8 }}>
                              tmux: {activeTask.tmuxSession}
                            </span>
                          )}
                          {activeTask.skills.map(s => (
                            <span key={s} style={{ color: C.purple, background: C.purple + "12", padding: "1px 6px", borderRadius: 2, fontSize: 9 }}>{s}</span>
                          ))}
                        </div>
                      </div>
                    )}

                    {/* Session chain */}
                    {activeTask && (
                      <SessionChain
                        task={activeTask}
                        activeSessionId={activeSession}
                        onSelectSession={setActiveSession}
                      />
                    )}

                    {/* tmux preview pane (replaces V3 SessionViewer) */}
                    <TmuxPreview task={activeTask} session={currentSession} />

                    {/* Diff panel for review tasks */}
                    {activeTask?.status === "review" && activeTask.diff && currentSession?.phase === "review" && (
                      <div style={{
                        padding: "8px 12px", borderTop: `1px solid ${C.border}`,
                        display: "flex", alignItems: "center", justifyContent: "space-between",
                      }}>
                        <div style={{ fontFamily: mono, fontSize: 10, color: C.textDim }}>
                          <span style={{ color: C.text }}>{activeTask.diff.files} files</span>
                          <span style={{ color: C.green, marginLeft: 8 }}>+{activeTask.diff.add}</span>
                          <span style={{ color: C.red, marginLeft: 4 }}>-{activeTask.diff.del}</span>
                        </div>
                        <div style={{ display: "flex", gap: 6 }}>
                          <button style={{
                            padding: "4px 12px", borderRadius: 4, border: `1px solid ${C.border}`,
                            background: "transparent", color: C.textMid, cursor: "pointer",
                            fontFamily: mono, fontSize: 9,
                          }}>Full Diff</button>
                          <button style={{
                            padding: "4px 12px", borderRadius: 4, border: `1px solid ${C.red}40`,
                            background: C.red + "10", color: C.red, cursor: "pointer",
                            fontFamily: mono, fontSize: 9,
                          }}>Reject</button>
                          <button style={{
                            padding: "4px 12px", borderRadius: 4, border: "none",
                            background: C.green, color: C.bg, cursor: "pointer",
                            fontFamily: mono, fontSize: 9, fontWeight: 600,
                          }}>Approve</button>
                        </div>
                      </div>
                    )}
                  </div>
                )}
              </>
            )}

            {/* ═══ BRAINSTORM VIEW — BACKLOG ═══ */}
            {view === "brainstorm" && (
              <div style={{ flex: 1, display: "flex", alignItems: "center", justifyContent: "center" }}>
                <div style={{ textAlign: "center", maxWidth: 320, padding: 24 }}>
                  <div style={{ fontSize: 36, marginBottom: 12, opacity: 0.3 }}>◇</div>
                  <div style={{ fontFamily: mono, fontSize: 13, color: C.textMid, marginBottom: 8 }}>Brainstorm View</div>
                  <div style={{ fontFamily: sans, fontSize: 12, color: C.textDim, lineHeight: 1.5, marginBottom: 16 }}>
                    Pre-project ideation threads — Claude Desktop-style project conversations. Not yet implemented.
                  </div>
                  <div style={{
                    display: "inline-block", padding: "4px 12px", borderRadius: 4,
                    background: C.purple + "12", border: `1px solid ${C.purple}30`,
                    fontFamily: mono, fontSize: 10, color: C.purple,
                  }}>Backlog</div>
                  <div style={{ fontFamily: sans, fontSize: 11, color: C.textDim, lineHeight: 1.5, marginTop: 16 }}>
                    For now, use the conductor or Claude Desktop for pre-project ideation.
                  </div>
                </div>
              </div>
            )}

            {/* ═══ KANBAN VIEW ═══ */}
            {view === "kanban" && <KanbanView tasks={filteredTasks} activeTask={activeTask} setActiveTask={handleTaskSelect}
              projectFilter={projectFilter} setProjectFilter={setProjectFilter}
              groupByProject={groupByProject} setGroupByProject={setGroupByProject}
              projectNames={projectNames} />}

            {/* ═══ CONDUCTOR VIEW ═══ */}
            {view === "conductor" && <ConductorView />}

            {/* ═══ WORKSPACES VIEW (OpenTofu) ═══ */}
            {view === "workspaces" && (
              <div style={{ flex: 1, overflowY: "auto", padding: 12 }}>
                <div style={{ fontFamily: mono, fontSize: 10, color: C.textDim, textTransform: "uppercase", marginBottom: 12, letterSpacing: "0.08em" }}>
                  Workspaces · OpenTofu
                </div>
                {WORKSPACES.map(w => {
                  const agents = TASKS.filter(t => t.project === w.name && t.tmuxSession && !["done", "backlog"].includes(t.status)).length;
                  return (
                    <div key={w.name} style={{
                      padding: 14, marginBottom: 8, borderRadius: 6,
                      background: C.bgCard, border: `1px solid ${C.border}`,
                    }}>
                      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", marginBottom: 6 }}>
                        <span style={{ fontFamily: mono, fontSize: 13, color: C.text, fontWeight: 600 }}>{w.name}</span>
                        <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
                          <Badge status={w.status === "running" ? "running" : "backlog"} small />
                          {w.status === "running" ? (
                            <button style={{
                              padding: "2px 8px", borderRadius: 3, border: `1px solid ${C.red}40`,
                              background: C.red + "10", color: C.red, cursor: "pointer",
                              fontFamily: mono, fontSize: 8,
                            }}>Stop</button>
                          ) : (
                            <button style={{
                              padding: "2px 8px", borderRadius: 3, border: `1px solid ${C.green}40`,
                              background: C.green + "10", color: C.green, cursor: "pointer",
                              fontFamily: mono, fontSize: 8,
                            }}>Start</button>
                          )}
                        </div>
                      </div>
                      <div style={{ fontFamily: sans, fontSize: 11, color: C.textDim }}>{w.desc}</div>
                      <div style={{ display: "flex", gap: 12, fontFamily: mono, fontSize: 9, color: C.textDim, marginTop: 6 }}>
                        <span>template: {w.template}</span>
                        <span>cpu: {w.cpu}</span>
                        <span>mem: {w.mem}</span>
                      </div>
                      <div style={{ fontFamily: mono, fontSize: 9, color: C.textDim, marginTop: 2 }}>
                        container: {w.container} · path: {w.path}
                      </div>
                      {agents > 0 && (
                        <div style={{ fontFamily: mono, fontSize: 9, color: C.amber, marginTop: 4 }}>
                          {agents} active tmux session{agents > 1 ? "s" : ""}
                        </div>
                      )}
                    </div>
                  );
                })}

                {/* Provision new workspace */}
                <button style={{
                  width: "100%", padding: 14, marginTop: 4, borderRadius: 6,
                  background: "transparent", border: `1px dashed ${C.border}`,
                  cursor: "pointer", fontFamily: mono, fontSize: 10, color: C.textDim,
                  textAlign: "center",
                }}>
                  + Provision new workspace (tofu apply)
                </button>
              </div>
            )}
          </div>

          {/* Chat input — context-aware */}
          <SmartChatInput
            context={chatContext}
            workspaces={WORKSPACES}
            onSend={(msg, ctx) => console.log(`[${ctx.mode}:${ctx.target || ctx.taskId || "auto"}] tmux send-keys → ${ctx.tmuxSession || "new"}:`, msg)}
          />
        </div>
      </div>
    </div>
  );
}
