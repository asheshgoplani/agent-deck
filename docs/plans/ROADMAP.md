# Roadmap

Evolution plan for the Saltbox Claude Sandbox — from isolated dev environment to self-hosted Claude Code agent platform.

## Vision

A self-hosted alternative to cloud-hosted AI coding agents. You describe work in natural language from any device; the system routes it to the right project, runs Claude Code in an isolated container, and delivers results — all on hardware you control.

---

## ⚠️ Critical Issues (Pre-Phase 1 Blockers)

A critical teardown analysis (February 2026) identified several security vulnerabilities and architectural showstoppers that **must be addressed before proceeding with Phase 1**. See [Saltbox Agent Platform a critical teardown.md](Saltbox%20Agent%20Platform%20a%20critical%20teardown.md) for full analysis.

### Security Vulnerabilities

| Issue | Severity | Description | Mitigation |
|-------|----------|-------------|------------|
| **NET_ADMIN negates firewall** | 🔴 Critical | Containers with NET_ADMIN can flush their own iptables rules via `iptables -F` | Remove NET_ADMIN from containers; apply rules in host namespace (DOCKER-USER chain) |
| **Prompt injection → code execution** | 🔴 Critical | Malicious CLAUDE.md/repos can trigger arbitrary shell commands with `--dangerously-skip-permissions` | Input validation, sandboxed execution, consider removing `--dangerously-skip-permissions` |
| **SSH key exposure** | 🟠 High | Read-only mounts still allow `cat ~/.ssh/id_rsa` and exfiltration | Use SSH agent forwarding instead of key mounting |
| **Docker socket proxy limits** | 🟠 Medium-High | If CONTAINERS+POST enabled, compromised containers can create privileged siblings | Tighten socket proxy permissions; audit network access |
| **Domain iptables fragility** | 🟠 Medium-High | DNS rebinding, CDN rotation, DNS exfiltration bypass IP rules | Replace with HTTP/HTTPS proxy (Squid) for Host/SNI inspection |
| **runc CVEs (Nov 2025)** | 🟠 High | CVE-2025-31133, CVE-2025-52565, CVE-2025-52881 bypass AppArmor/SELinux | Ensure runc ≥ v1.2.8 on host |

### Architectural Showstoppers

| Issue | Severity | Description | Mitigation |
|-------|----------|-------------|------------|
| **tmux capture-pane scrollback bug** | 🔴 Critical | Claude Code's `autocompact`/`compact` clears tmux scrollback (GitHub #16310), blanking the web UI | Replace with `tmux pipe-pane` or Go pty library (`creack/pty`) |
| **Agent Deck bus factor = 1** | 🟠 High | 8 GitHub stars, 1 developer — critical dependency with no community | Fork immediately or build in-house replacement (1-2 weeks Go) |
| **Filesystem JSON concurrency** | 🟠 Medium | Race conditions with multiple agents writing same task file | Migrate to SQLite with WAL mode for ACID compliance |
| **SSE HTTP/1.1 connection limit** | 🟠 Medium | Browsers limit 6 SSE connections per domain, blocking 5+ agent dashboards | Require HTTP/2 (TLS) to enable ~100 multiplexed streams |

### Resource Constraints

| Constraint | Impact | Mitigation |
|------------|--------|------------|
| **Claude Code memory leaks** | Instances grow to 30-120GB (GitHub #9711, #4953, #11315) | Docker memory limits (`--memory=4g --memory-swap=6g`) + session recycling every 2-4 hours |
| **Realistic NUC concurrency** | 3-5 agents max on 64GB NUC (4-6GB each + 8-10GB for OS/platform) | Budget concurrency carefully; monitor memory usage |
| **API costs** | $6/agent/day average, $900-1,800/month for 5 agents | Deploy centralized API proxy for cost tracking and budget enforcement |
| **Rate limits** | Tier 2 (1,000 RPM) exhausted within seconds with 10 concurrent agents | Upgrade to Tier 3 ($200 deposit) for 5+ agents; coordinate rate limiting |

### Priority Fix Order (Pre-Phase 1)

1. **Remove NET_ADMIN** from agent containers; apply iptables in host namespace (DOCKER-USER chain)
2. **Replace tmux capture-pane** with `pipe-pane` or direct pty capture
3. **Fork Agent Deck** or begin in-house replacement
4. **Add Docker memory limits** with automatic session recycling
5. **Deploy centralized API proxy** for cost/rate/audit in one layer

```
Phase 0   (Foundation)        → Secure containerised dev environment           ✅ Delivered
Phase 0.5 (Coder Migration)   → Replace shell scripts with Coder + OpenTofu   🔧 In Progress
Phase 0.6 (Critical Fixes)    → Address security/architecture blockers         🚨 BLOCKING
Phase 1   (Agent Management)   → Agent Deck sessions, conductor, Telegram      📋 Planned
Phase 2   (Hub Web Layer)      → Mobile-first web UI for dispatch & monitoring  📋 Designed
Phase 3   (Git Automation)     → Branch management, diffs, approve/reject       📋 Designed
Phase 4   (External Triggers)  → Webhooks, GitHub integration, messaging bots   📋 Planned
Phase 5   (Orchestration)      → Cross-project tasks, pipelines, scheduling     📋 Future
Phase 6   (Agent Platform)     → Persistent memory, autonomous monitoring       📋 Future
```

---

## Phase 0 — Foundation ✅

Secure, browser-accessible development environment on a Saltbox NUC.

**Delivered:**
- code-server (browser IDE) behind Authelia SSO + Traefik reverse proxy
- Docker socket proxy (tecnativa/docker-socket-proxy) — filtered API allowing containers/images/volumes/networks/exec/build but blocking auth/secrets/services/swarm/nodes/plugins/system
- Sandbox image (`sandbox-image/Dockerfile`) — Node 22 + Python 3 + Claude Code + gh CLI + zsh + oh-my-zsh + ripgrep + fd + fzf + bat + htop + dev tools
- Container lifecycle via `sandbox.sh` — direct `docker run` with explicit security flags, completely ignoring any `.devcontainer/` in the repo
- Whitelist-only iptables firewall (`init-firewall.sh`) — default-deny outbound with domain-level whitelisting covering Anthropic API/auth/stats, npm registry, GitHub (web/API/raw/objects), PyPI, documentation sites (MDN, Node.js, Python, React, Next.js, Vue, Angular, Svelte, TypeScript, Jest, Playwright, Vitest), CDNs (jsdelivr, cdnjs, unpkg, esm.sh), and community platforms (dev.to, medium, hashnode, HN)
- Per-worktree named volume isolation — volumes scoped by sanitized workspace path: `sandbox-bashhistory-{scope}`, `sandbox-config-{scope}`, `sandbox-npm-global-{scope}`, `sandbox-pip-cache-{scope}`, `sandbox-node-modules-{scope}`
- Git bare-repo + worktree workflow via `project.sh` — clone as bare repo with `.bare` convention, worktree creation with branch tracking, cleanup that removes containers and associated Docker volumes
- Dotfiles pipeline: host bind-mount at `/etc/claude-dotfiles` (read-only) → `post-start.sh` syncs settings, commands, MCP config to `~/.claude/` on every container start
- Entrypoint-based container lifecycle (`entrypoint.sh`) — runs as root for firewall + permission fixes, then drops to `node` user for post-create (first start only) and post-start (every start), writes ready sentinel at `/tmp/.sandbox-ready`, then `exec sleep infinity`
- Network toggle for outer code-server container via unix socket daemon — `abc` user sends commands to root-owned daemon via socat, which manipulates iptables to block/allow public IPs while preserving Docker internal networking
- Resource limits (CPU, memory) configurable via `.env` (defaults: 2 cores, 2GB per sandbox)
- DooD (Docker-outside-of-Docker) path mapping — `setup.sh` creates `/workspace → ${WORKSPACE_PATH}` symlink on host so Docker daemon resolves paths, env vars `HOST_SSH_PATH`/`HOST_GITCONFIG_PATH`/`HOST_DOTFILES_PATH` pass host-absolute paths through docker-compose.yml into code-server for `sandbox.sh` to use as bind mounts

**Security model (seven layers):**

| # | Layer | Implementation | Status |
|---|-------|---------------|--------|
| 1 | Authelia SSO | External authentication before code-server via Traefik middleware | ✅ |
| 2 | code-server password | Second auth factor (`PASSWORD` env var in docker-compose.yml) | ✅ |
| 3 | Docker socket proxy | tecnativa/docker-socket-proxy — allows container/image/volume/network/exec/build ops, blocks privileged containers, host mounts, swarm, system-level calls. Read-only socket mount, all caps dropped, read-only filesystem. | ⚠️ Coarse-grained |
| 4 | Sandbox firewall | Default-deny outbound, whitelist-only (`init-firewall.sh`). DNS (53) and SSH (22) allowed by port. All other outbound resolved by domain → IPv4 address → iptables ACCEPT rules. IPv6 disabled via sysctl to prevent bypass. | 🔴 **BROKEN** — see Phase 0.6a |
| 5 | Volume isolation | Per-worktree named volumes scoped by sanitized workspace path. No cross-worktree volume sharing. | ✅ |
| 6 | Capability dropping | **code-server**: `cap_drop ALL`, `cap_add` CHOWN/DAC_OVERRIDE/FOWNER/NET_ADMIN/SETGID/SETUID, `no-new-privileges`. NET_ADMIN used by network-toggle daemon. **sandbox**: `--cap-drop ALL`, `--cap-add` NET_ADMIN/NET_RAW/SETUID/SETGID. NET_ADMIN/NET_RAW for firewall init, SETUID/SETGID for `su` in entrypoint. **socket-proxy**: `cap_drop ALL`, `no-new-privileges`, `read_only: true`. | 🔴 **NET_ADMIN must be removed** — see Phase 0.6a |
| 7 | Resource limits | CPU/memory caps from `.env` via docker-compose deploy resources (code-server) and `sandbox.sh` `--cpus`/`--memory` flags (sandbox containers). | ⚠️ Memory limits not enforced — see Phase 0.6e |

**⚠️ CRITICAL SECURITY NOTE:** The current security model has a fundamental flaw. Containers with NET_ADMIN capability can flush their own iptables rules (`iptables -F`), rendering the firewall useless. With `--dangerously-skip-permissions`, prompt injection can trigger this. **Phase 0.6a addresses this by moving firewall rules to the host namespace (DOCKER-USER chain) and removing NET_ADMIN from containers.**

**Current project structure:**

```
scripts/
  setup.sh              One-time NUC bootstrap (run with sudo on host)
  project.sh            Project/worktree manager (inside code-server terminal)
  sandbox.sh            Sandbox container lifecycle (up/exec/stop/rebuild/status/logs)
  network-toggle.sh     Client: sends commands to unix socket daemon
  network-toggle-daemon.sh  Reference copy (actual daemon embedded in setup.sh)
hooks/
  post-checkout         Git template hook (no-op placeholder, dotfiles mounted directly)
sandbox-image/
  Dockerfile            Node 22 + Python 3 + Claude Code + gh CLI + zsh + dev tools
  entrypoint.sh         Container entrypoint (firewall → post-create → post-start → sleep)
  init-firewall.sh      iptables whitelist-only outbound firewall
  post-create.sh        First-start setup (git config include, safe.directory, global gitignore)
  post-start.sh         Every-start setup (dotfile sync from /etc/claude-dotfiles → ~/.claude/)
claude-dotfiles/
  settings.json         User-scope Claude settings (plugin marketplace, enabled plugins)
  settings.local.json   User-scope local overrides (permissions, personal prefs)
  commands/             Custom slash commands distributed to all workspaces
  claude.json           User-scope MCP server definitions (optional)
coder-templates/
  claude-sandbox/
    main.tf             OpenTofu workspace definition (committed, not yet deployed)
    variables.tf        Template inputs (tokens, paths, resource limits)
    outputs.tf          Template outputs (workspace URL, container name)
Dockerfile              Extends linuxserver/code-server with iptables, socat
docker-compose.yml      Stack definition (code-server + socket-proxy + networks)
.env.example            Template for required environment variables
scripts/
  validate-security.sh  Coder migration validation — security layers
  validate-git.sh       Coder migration validation — git operations
  validate-all.sh       Coder migration validation — master runner
docs/
  coder-migration-plan.md
  security-assessment.md
  testing-guide.md
  plans/
    2026-02-16-coder-migration-design.md
    2026-02-16-coder-migration-implementation.md
  research/
    devcontainer-coexistence.md
```

**Architecture (current, running):**

```
Browser → Authelia → Traefik → code-server container ──→ Docker socket proxy ──→ Host Docker daemon
                                 (linuxserver/code-server)   (filtered API)         ↓
                                 sandbox.sh spawns ←─────────────────────────── Sandbox containers
                                 containers via proxy                            (sandbox-image)
```

**Sync chain:** `repo → setup.sh → ${WORKSPACE_PATH}/scripts/ + ${WORKSPACE_PATH}/sandbox-image/ (host) → /workspace/ (code-server mount)`. Changes to repo source files are NOT visible inside code-server until `setup.sh` copies them. Changes to `docker-compose.yml` or the outer `Dockerfile` only need `docker compose up -d`.

**What changed from original design:**
- `devcontainer-template/` replaced by `sandbox-image/` — devcontainer CLI and spec abandoned entirely due to the coexistence problem (repos with their own `.devcontainer/` clashing with sandbox security config). See `docs/research/devcontainer-coexistence.md`.
- `devcontainer up` replaced by `sandbox.sh up` — direct `docker run` with explicit security flags
- Post-checkout git hook replaced by direct bind-mount + `post-start.sh` sync (hook is now a no-op placeholder)
- Entrypoint pattern replaced devcontainer lifecycle hooks (postCreateCommand, postStartCommand)

---

## Phase 0.5 — Coder Migration 🔧

Replace shell scripts and the code-server outer container with Coder (open-source workspace platform) + OpenTofu templates.

**Status:** OpenTofu template written and committed (`coder-templates/claude-sandbox/`), validation scripts committed, design and implementation plans complete. Coder server not yet installed.

**What's done:**
- `coder-templates/claude-sandbox/main.tf` — full workspace definition (~200 lines HCL) with agent, apps, volumes, security config
- `coder-templates/claude-sandbox/variables.tf` — template inputs (tokens, paths, resource limits with defaults)
- `coder-templates/claude-sandbox/outputs.tf` — workspace URL, ID, container name
- `scripts/validate-security.sh` — automated security layer verification for Coder workspaces
- `scripts/validate-git.sh` — SSH auth, git config, clone testing
- `scripts/validate-all.sh` — master runner for all 6 validation criteria
- `docs/plans/2026-02-16-coder-migration-design.md` — approved 4-phase design
- `docs/plans/2026-02-16-coder-migration-implementation.md` — 21-task step-by-step plan

**What's NOT done (Phase 1 of implementation plan):**
- Install PostgreSQL container for Coder state
- Install Coder binary on host
- Create systemd service (`/etc/systemd/system/coder.service`)
- Configure Traefik routing (`/etc/traefik/dynamic/coder.yml`)
- Push template to Coder via `coder templates create`
- Create first workspace and run validation suite

**Architecture (target):**

```
Browser → Authelia → Traefik → Coder server ──→ Workspace containers
                                (systemd on host)  (OpenTofu-provisioned via Docker provider)
```

**Key template implementation details** (from `coder-templates/claude-sandbox/main.tf`):
- Replaces Coder's external HTTPS URL with Docker-internal `http://coder:7080` for agent init script, so the agent can reach the Coder server without punching through the sandbox firewall
- Adds iptables rules for Docker internal networks (172.16.0.0/12, 10.0.0.0/8, 192.168.0.0/16) inserted before init-firewall.sh's DROP rule
- Copies SSH keys from read-only host mount to writable `/home/node/.ssh` with correct permissions (Coder's gitssh wrapper can't write to read-only mounts)
- Writes Claude credentials from `CLAUDE_CODE_OAUTH_TOKEN` env var to `.credentials.json` then unsets the env var (Claude Code reads the env var as bearer token directly, overriding interactive `/login`)
- Container joins `saltbox` network for Coder agent communication
- Uses `data.coder_workspace.me.start_count` for workspace start/stop state management
- Volume scope by `data.coder_workspace.me.id` (survives workspace name changes)

**What gets replaced:**

| Current | Coder System |
|---------|-------------|
| code-server outer container | Coder server (systemd, includes browser IDE) |
| Docker socket proxy | Not needed — Coder on host as dedicated system user |
| `sandbox.sh` (~286 lines) | OpenTofu template (~200 lines HCL) |
| `project.sh` (worktree mgmt) | `coder create`, `coder list`, `coder ssh` |
| `network-toggle.sh` + daemon | Not needed (outer container eliminated) |
| `setup.sh` (host bootstrap) | Coder install + systemd + template push |
| DooD path symlinks + env passthrough | Not needed (Coder provisions directly on host) |

**What carries over unchanged:**
- `sandbox-image/Dockerfile` — container image definition (with tmux added — see Phase 1 sandbox changes)
- `sandbox-image/init-firewall.sh` — iptables whitelist-only firewall
- `sandbox-image/post-create.sh` — first-start git config setup
- `sandbox-image/post-start.sh` — dotfiles sync
- `claude-dotfiles/` — mounted from host, synced on startup
- Authelia + Traefik — Coder sits behind same SSO stack

**Security model mapping:**

| Layer | Current | Coder Template | Status |
|-------|---------|---------------|--------|
| 1. Authelia SSO | Traefik middleware on code-server | Traefik file provider routing to Coder | ✅ Preserved |
| 2. Second auth | code-server PASSWORD | Coder built-in auth + OIDC via Authelia | ⚠️ Changed (research task B) |
| 3. Socket proxy | tecnativa/docker-socket-proxy | Not needed — Coder on host, template validation replaces API filtering | ⚠️ Changed (research task A) |
| 4. Firewall | `init-firewall.sh` in entrypoint | `init-firewall.sh` in entrypoint + internal network rules added before DROP | ✅ Preserved |
| 5. Volume isolation | `sandbox.sh` `-v` flags with sanitized scope | `docker_volume` resources scoped by `workspace.id` | ✅ Preserved |
| 6. Capability dropping | `sandbox.sh --cap-drop ALL --cap-add NET_ADMIN/NET_RAW/SETUID/SETGID` | `capabilities { drop = ["ALL"], add = ["CHOWN", "DAC_OVERRIDE", "FOWNER", "NET_ADMIN", "NET_RAW", "SETUID", "SETGID"] }` | ⚠️ Broader — Coder template adds CHOWN/DAC_OVERRIDE/FOWNER for root operations in custom entrypoint |
| 7. Resource limits | `sandbox.sh --cpus --memory` from env vars | `cpu_shares` + `memory` in HCL from template variables | ✅ Preserved |

**New capabilities gained:**
- Workspace dashboard with status, start/stop, logs
- Audit logging of all workspace operations
- Idle shutdown (auto-stop inactive workspaces)
- Template versioning (roll back workspace definitions)
- Coder Tasks REST API — programmatic task creation and monitoring
- Coder Mux — chat interface for parallel agent management
- `coder ssh` — direct SSH into workspaces from any machine
- Prometheus metrics endpoint
- `coder_app` resources for Claude Code and Terminal web access

**Implementation approach:** Fresh start (no data migration — project is early stage). Install Coder in parallel with existing system, validate all security layers via `validate-all.sh`, switch over. 21-task plan across 4 phases, estimated 1-2 days.

**Research tasks still needed:**
- **Task A**: Does Coder provide equivalent Docker socket security? Can templates create privileged containers? (mitigated by single-user + template RBAC)
- **Task B**: OIDC integration with Authelia — does seamless SSO work?

---

## Phase 0.6 — Critical Fixes 🚨

**BLOCKING PHASE** — Must complete before Phase 1. Addresses critical security vulnerabilities and architectural showstoppers identified in the February 2026 teardown analysis.

**Prerequisite:** Phase 0.5 (Coder infrastructure in place)

### 0.6a. Firewall Architecture Overhaul

The current approach (iptables inside container with NET_ADMIN capability) is fundamentally broken — agents can flush their own rules.

**Changes required:**
1. Remove `NET_ADMIN` and `NET_RAW` capabilities from sandbox containers
2. Apply firewall rules in host namespace using DOCKER-USER chain
3. Create per-container firewall rules keyed by container ID/name
4. Update `init-firewall.sh` to configure host-side rules instead of container-side
5. Update OpenTofu template to remove NET_ADMIN from capabilities block

**New firewall architecture:**
```
Host Network Namespace (rules applied here, containers cannot modify)
└── DOCKER-USER chain
    ├── sandbox-web-app: whitelist rules (Anthropic, npm, GitHub, etc.)
    ├── sandbox-api-svc: whitelist rules
    └── DEFAULT DROP
```

**Validation:** Verify container cannot modify its own egress rules via `docker exec ... iptables -L` (should fail with "permission denied").

### 0.6b. SSH Key Security

Replace direct key mounting with SSH agent forwarding.

**Changes required:**
1. Remove SSH key bind mounts from OpenTofu template and `sandbox.sh`
2. Configure SSH agent socket forwarding into containers
3. Update documentation to require SSH agent on host
4. Test git operations work via forwarded agent

### 0.6c. Terminal Streaming Fix

Replace `tmux capture-pane` (polling, loses data on autocompact) with streaming approach.

**Options (choose one):**
- **Option A:** `tmux pipe-pane -o "cat >> /tmp/output.log"` — true streaming, append to file
- **Option B:** Go pty library (`github.com/creack/pty`) — direct PTY capture in Hub process
- **Option C:** Script wrapper that tees output to both tmux and a file

**Recommended:** Option A for simplicity. Hub reads `/tmp/output.log` via `docker exec` + SSE.

**Changes required:**
1. Update sandbox entrypoint to start tmux with `pipe-pane` configured
2. Update Hub SSE endpoint to read from output file instead of capture-pane
3. Implement log rotation to prevent unbounded growth
4. Test that autocompact no longer causes data loss

### 0.6d. Agent Deck Risk Mitigation

Single-maintainer dependency with 8 GitHub stars is unacceptable for core infrastructure.

**Immediate actions:**
1. Fork Agent Deck to organization repository
2. Document core functionality: session management, status detection, forking, conductor
3. Identify minimal subset needed for Saltbox (likely ~500 lines of Go)
4. Begin parallel development of in-house session manager as fallback

**Long-term:** Evaluate building session management directly into Hub binary, eliminating the external dependency entirely.

### 0.6e. Resource Containment

Protect host from Claude Code memory leaks.

**Changes required:**
1. Add `--memory=4g --memory-swap=6g` to container startup (OpenTofu template)
2. Implement session recycling daemon: restart containers every 2-4 hours of active use
3. Add memory monitoring with alerts at 80% container limit
4. Configure Docker OOM killer behavior (`--oom-kill-disable=false`)

### 0.6f. HTTP/2 Requirement

Ensure SSE works with 5+ agents.

**Changes required:**
1. Verify Traefik serves Hub over HTTP/2 (requires TLS)
2. Test SSE connection limit with 10 concurrent browser tabs
3. Document HTTP/2 requirement in deployment guide

### 0.6g. Missing Infrastructure

Deploy essential observability and governance capabilities.

**Centralized API proxy:**
- Route all Claude API calls through Hub backend
- Enables: cost tracking, rate limiting, audit logging, budget enforcement
- Implementation: HTTP middleware in Hub that proxies to Anthropic API

**Cost tracking:**
- Integrate Langfuse (MIT, self-hostable, 19K+ stars) for token-level tracking
- Per-agent cost attribution and budget alerts

**Backup strategy:**
- Configure `restic` backups to Backblaze B2
- Daily backups of: SQLite DB, task JSON, config files, Docker volumes
- Document recovery runbook with RPO/RTO targets

**Secret management:**
- Migrate from environment variables to Docker Compose secrets
- Secrets mounted as files at `/run/secrets/`

**Estimated effort:** 3-5 days for all 0.6 subtasks

---

## Phase 1 — Agent Management 📋

Adopt Agent Deck as the primary session manager for running Claude Code agents across Coder workspace containers.

**Prerequisites:**
- Phase 0.5 (Coder workspaces running)
- Phase 0.6 (Critical fixes applied — **BLOCKING**)

### 1a. Sandbox Image Changes — tmux Inside Container (Pattern B)

Claude Code and tmux run natively inside each sandbox container. The host (Agent Deck, Hub, SSH) connects via `docker exec` to interact with the container's tmux sessions. This inverts the original design where tmux ran on the host wrapping `docker exec`.

**Why Pattern B:**
- **Resilience:** Disconnecting SSH/Terminus/Hub doesn't interrupt the agent. tmux + Claude Code keep running inside the container.
- **Self-contained sandboxes:** Each container is a complete agent environment. Stop it, restart it — tmux session and scrollback survive (within container lifecycle).
- **Clean Agent Deck integration:** Agent Deck uses `docker exec` + tmux commands to dispatch and read output. No socket sharing, no host tmux ↔ container entanglement.
- **SSH indirection is acceptable:** SSH → host → `docker exec -it {sandbox} tmux attach` adds one hop but works cleanly.

**Sandbox image changes:**
- Add `tmux` package to `sandbox-image/Dockerfile`
- Update `entrypoint.sh` to start a tmux server and create a default session (e.g. `tmux new-session -d -s agent-0`)
- Claude Code launches inside that tmux session on container start
- Optional: configurable number of initial tmux sessions for multi-agent-per-container scenarios

**Execution chain:**
```
Agent Deck (on host)
  → docker exec -it sandbox-web-app tmux send-keys "Fix the auth bug" Enter     # dispatch
  → docker exec sandbox-web-app cat /tmp/agent-0.log | tail -n 100              # read output (via pipe-pane log)
  → docker exec -it sandbox-web-app tmux attach -t agent-0                       # interactive

Hub Web UI
  → SSE → docker exec sandbox-web-app tail -f /tmp/agent-0.log                   # stream to browser (true streaming)
  → POST → docker exec sandbox-web-app tmux send-keys "user reply" Enter         # send input

SSH (Terminus)
  → ssh nuc → docker exec -it sandbox-web-app tmux attach -t agent-0             # direct attach
```

**Note:** Uses `tmux pipe-pane` (not `capture-pane`) to avoid the scrollback-clearing bug (GitHub #16310). See Phase 0.6c.

**Validation tests (add to validate-all.sh):**
1. tmux server starts correctly inside container on boot
2. Claude Code launches and runs inside tmux session
3. `docker exec ... tmux send-keys` delivers input correctly
4. `docker exec ... tmux capture-pane -p` reads output correctly
5. Disconnecting `docker exec` does NOT kill the tmux session or Claude Code process
6. Multiple tmux sessions can run concurrently inside one container
7. Status detection (Agent Deck) works through `docker exec` + `tmux capture-pane`

### 1b. Agent Deck on Host (Containerise Later)

Install Agent Deck on the NUC host initially. Sessions target tmux inside Coder workspace containers.

**Why host first:**
- Simpler starting point — single Go binary + tmux, zero container config
- SSH → tmux workflow stays clean (Terminus direct to host)
- Desktop TUI accessible without an extra docker exec hop

**Future containerisation path:**
Agent Deck can be containerised as a "management plane" container similar to how code-server currently works. It would need Docker CLI access via the socket proxy (same DooD pattern) and persistent storage for `~/.agent-deck/` (sessions.json, config.toml, logs). With Pattern B (tmux inside sandbox containers), Agent Deck uses `docker exec` to interact regardless of where it runs — containerising it doesn't change the interaction pattern. The only change is SSH indirection: SSH → Agent Deck container → docker exec → sandbox tmux (one extra hop, acceptable). For the Hub web UI path, containerisation makes no difference.

**Design notes:**
- Agent Deck manages session metadata and status; tmux runs inside containers
- Each session wraps: `docker exec -it {container} tmux new-session -s {session-name} "claude --dangerously-skip-permissions"`
- Smart status detection: thinking (●), waiting (◐), running (⟳), idle (○), error (✕), complete (✓) — via `docker exec ... tmux capture-pane -p` output parsing
- Session forking creates new sibling tasks inheriting full conversation history
- Skills manager for custom Claude Code slash commands
- Claude Code hooks (`Stop` hook) fire instant status updates without polling
- TUI accessible via SSH + Terminus, or code-server terminal
- Fuzzy search across all sessions with status filters
- Global Claude conversation search across all sessions

**Open questions to test empirically:**
1. Does Agent Deck status detection work through `docker exec` → `tmux capture-pane`? (High impact — breaks core UX if wrong)
2. Can conductor monitor containerised agents via the same `docker exec` + tmux path? (Medium impact)
3. Does AskUserQuestion render correctly inside tmux inside a container with `--dangerously-skip-permissions`? (See §1d below)

### 1c. Conductor + Telegram

Set up the conductor (a separate long-running Claude Code instance in its own tmux session) and Telegram bridge for mobile notifications.

**Design notes:**
- Conductor heartbeat every 15 minutes: reads session states, auto-responds or escalates
- `NEED:` pattern in agent output → Telegram/Slack alert
- `bridge.py` daemon connects to Telegram Bot API — no public URL required
- Notification types: agent waiting (🔶), review ready (🔵), auto-approved (✅), error (❌), heartbeat (💚)
- Delivers mobile dispatch before the Hub web layer exists

**Deliverables:** Agent Deck configured (`config.toml` with MCP servers), conductor profile for "work" context, `bridge.py` daemon with Telegram bot token, status detection verified through `docker exec`.

**Estimated effort:** 1-2 days

### 1d. AskUserQuestion Validation

The AskUserQuestion tool has a known issue (GitHub #10400, still open as of 2026-02-23) where it returns empty responses when `--dangerously-skip-permissions` is enabled, silently skipping user prompts. A related issue (#15400) causes AskUserQuestion to trigger the PermissionRequest hook, auto-dismissing questions.

**Test protocol:**
1. Launch sandbox container with tmux inside (Pattern B)
2. Start Claude Code with `--dangerously-skip-permissions` inside tmux session
3. Instruct Claude to use AskUserQuestion tool
4. Verify: Does the question render in the tmux session? Does it wait for input? Does the response reach Claude?
5. Test via `docker exec ... tmux send-keys` — does the reply mechanism work end-to-end?

**If AskUserQuestion works:** Standard flow — Agent Deck detects waiting (◐) status → Hub shows notification → user replies via chat or tmux attach.

**If AskUserQuestion is broken:** Deploy fallback — custom MCP tool (`ask-human`) that writes questions to a file or SQLite row. Hub/Telegram polls for pending questions, presents to user, writes answer back. MCP tool returns answer to Claude. Works regardless of `--dangerously-skip-permissions` behaviour.

```
Claude calls ask_human MCP tool → MCP writes to /tmp/questions.json
Hub/Telegram polls /tmp/questions.json → shows to user → user replies
MCP reads reply → returns answer to Claude Code
```

**Decision:** Test first, build fallback only if needed. Either path is clean.

### 1e. MCP Server Strategy

**Now (Phase 1): Per-container MCP processes (Option C)**

Each sandbox container spawns its own MCP server instances when Claude Code starts. On a NUC with 32-64GB running 3-5 concurrent agents, the ~50-100MB overhead per agent for MCP processes is negligible. No shared infrastructure, no cross-container networking complexity.

Agent Deck's built-in MCP socket pooling (Unix sockets at `/tmp/agentdeck-mcp-{name}.sock`) does NOT work across Docker container boundaries — Unix sockets don't cross network namespaces. Accept per-container processes for now.

**Future (Phase 5+): Streamable HTTP MCP proxy (Option A)**

When agent count grows or memory becomes constrained, deploy a shared MCP proxy on the host:

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│ Sandbox A    │     │ Sandbox B    │     │ Sandbox C    │
│ Claude Code  │     │ Claude Code  │     │ Claude Code  │
│ MCP config:  │     │ MCP config:  │     │ MCP config:  │
│ type: http   │     │ type: http   │     │ type: http   │
│ url: proxy:  │     │ url: proxy:  │     │ url: proxy:  │
│   8500/mcp   │     │   8500/mcp   │     │   8500/mcp   │
└──────┬───────┘     └──────┬───────┘     └──────┬───────┘
       │                    │                    │
       └────────────┬───────┘────────────────────┘
                    │
          ┌─────────▼─────────┐
          │  MCP Proxy (host)  │
          │  mcp-proxy or      │
          │  tbxark/mcp-proxy  │
          │  port 8500         │
          │                    │
          │  ┌──────┐ ┌──────┐│
          │  │GitHub│ │ Exa  ││  ← stdio MCP servers
          │  │ MCP  │ │ MCP  ││    spawned once, shared
          │  └──────┘ └──────┘│
          └────────────────────┘
```

Implementation requirements for future migration:
- Run `mcp-proxy` (npm or Go version, e.g. `tbxark/mcp-proxy`) on host or in its own container
- Each MCP server spawned once as stdio subprocess, multiplexed to all clients via Streamable HTTP
- Sandbox `init-firewall.sh` whitelists the proxy's Docker network address (internal only, no external exposure)
- Claude Code `.mcp.json` in each sandbox configured with `"type": "http"` pointing to proxy
- Optional API key auth on the proxy to prevent cross-sandbox tool leakage
- Migration path: swap `.mcp.json` from stdio entries to http entries — no sandbox image changes needed beyond firewall whitelist update
- ~85-90% memory reduction per Agent Deck's benchmarks for stdio→socket pooling

---

## Phase 2 — Hub Web Layer 📋

Mobile-first web UI wrapping Agent Deck operations. Thin orchestration layer — Agent Deck does session management, Hub adds multi-project routing, web monitoring, and diff review.

**Prerequisite:** Phase 1 (Agent Deck + conductor running)

**Stack:** Go (`net/http` + `html/template` + htmx). SSE for streaming. Single static binary deployment. Single responsive breakpoint at 768px.

**Why Go (not Python/FastAPI):**
- Same language as Agent Deck, Claude Squad, OpenCode, agtx — the entire multi-agent terminal ecosystem is Go
- Option to eventually merge Agent Deck + Hub into a single `saltbox` binary (`saltbox tui` for desktop, `saltbox web` for Hub)
- Single static binary with zero runtime dependencies — no venv, no pip, no uvicorn
- Goroutines handle WebSocket streams and concurrent `docker exec` sessions naturally
- `net/http` + `html/template` + htmx gives server-rendered progressive enhancement — same pattern as FastAPI + htmx but with Go's deployment story
- Key libraries: `gorilla/websocket` (streaming), `mattn/go-sqlite3` (task DB), `os/exec` (Agent Deck CLI), Docker SDK for Go (container API)

**Hub ↔ Agent Deck integration (CLI automation, not fork):**

Agent Deck is a managed dependency, not forked. Hub shells out to `agent-deck` CLI with `--json` for all session operations:

| Hub Operation | Agent Deck CLI |
|---------------|---------------|
| Create task | `agent-deck add {dir} -c "docker exec -it {container} tmux new-session -s {name} claude --dangerously-skip-permissions" -t {tag} --json` |
| List tasks | `agent-deck list --json` |
| Read output | `docker exec {container} tmux capture-pane -p -t {session}` |
| Send input | `docker exec {container} tmux send-keys -t {session} "{input}" Enter` |
| Fork task | `agent-deck fork {session} --json` |
| Get status | Agent Deck status detection (parsed from `--json` list output) |

This gives TUI for desktop (Agent Deck native) + web UI for mobile (Hub) from the same session state.

### Five views:

1. **Agents (default)** — task list + tmux preview panel. Cards show project, status badge, phase, duration. Selected card shows live terminal output via `docker exec ... tmux capture-pane -p` → SSE → ANSI-to-HTML.
2. **Kanban** — Backlog → Planning → Running → Review → Done. Cards draggable (desktop), horizontal scroll (mobile).
3. **Conductor** — activity log + messaging interface for the conductor session.
4. **Workspaces** — Coder workspace management dashboard (start/stop, resource usage, template version).
5. **Brainstorm (backlog)** — future Claude Desktop-style project conversations.

### Key features:

- **Context-aware chat input** — auto-derives mode from navigation state: Reply (↩) when task selected, New (+) when no task, Conductor (◎) in Conductor view
- **Dual slash command palette** — Hub commands (/new, /fork, /sessions, /diff, /approve, /reject) + Claude Code native commands (/compact, /permissions, /memory, /cost, /doctor)
- **AskUserQuestion handling** — Agent Deck detects waiting status → Hub shows orange pulse on card → user replies via chat input → Hub sends via `docker exec ... tmux send-keys` into the container's tmux session
- **tmux preview pane** — read-only terminal capture from inside container; full interactive access via `docker exec ... tmux attach`
- **Diff viewer** — `git diff` rendered as HTML with file-level collapsible sections, approve/reject buttons

### Task data model (filesystem JSON):

One JSON file per task. tmux scrollback inside the container IS the conversation record — no custom message arrays.

```
Task {
  id, project, msg, status, time, branch, phase,
  skills, mcps, diff, container, tmuxSession, agentStatus,
  sessions[], parentTask
}
```

### Backend API:

- `GET/POST /tasks`, `PATCH /tasks/{id}`, `POST /tasks/{id}/fork`
- `POST /tasks/{id}/input` (`docker exec ... tmux send-keys`), `GET /tasks/{id}/preview` (SSE via `docker exec ... tmux capture-pane`)
- `GET/POST /workspaces` (Coder API proxy)
- `GET /conductor/log`, `POST /conductor/message`

**Estimated effort:** 1-2 weeks

---

## Phase 3 — Git Automation 📋

Automated git workflow around Claude Code's changes.

- **3a. Branch Management** — auto-create feature branches before Claude starts work, configurable naming per project in `projects.yaml` (e.g. `claude/fix-login-validation`), option to reuse existing branch for follow-ups
- **3b. Web Diff Viewer** — `git diff --stat` + `git diff` inside workspace container, rendered as HTML in Hub with file-level collapsible sections, mobile-friendly summary with expandable diffs, inline approve/reject buttons
- **3c. Commit + Push + PR** — auto-commit with Claude-generated message, push via SSH (keys mounted read-only), PR creation via `gh pr create` (already in sandbox image), Hub notifies on PR creation
- **3d. Approval Workflow** — for sensitive projects, hold commit locally and wait for explicit confirmation via Hub chat or Telegram before pushing

---

## Phase 4 — External Triggers 📋

Bring work to the platform from outside the browser.

- **4a. Webhook Endpoint** — Go HTTP route `POST /api/tasks` with bearer token auth (separate from Authelia — machine-to-machine), returns task ID immediately, processes async, optional callback URL for status updates
- **4b. GitHub Integration** — `issue.opened` → route by repo URL match, `issue_comment.created` with `@claude fix this` trigger, `pull_request.review_requested` → auto-review, requires GitHub App or PAT with repo scope
- **4c. Messaging Bot** — extends Phase 1c Telegram bridge with richer interaction (file attachments, multi-turn, inline diffs). Evaluate OpenClaw as batteries-included alternative once it stabilises.

---

## Phase 5 — Orchestration 📋

Coordinated multi-step workflows.

- **5a. Cross-Project Coordination** — conductor-first approach: the conductor sequences cross-project tasks conversationally (spawn task A, wait for completion, spawn task B with context). Formal SQLite-backed DAG scheduler only if deterministic, repeatable, YAML-defined pipelines needed (no human in the loop). The superpowers plugin handles intra-task phase orchestration (brainstorm → plan → execute → review); the DAG handles inter-task cross-project dependency ordering.
- **5b. Task Queue + History** — filesystem JSON for task records (consistent with Phase 2 data model), searchable in Hub, retry, filter by project/date/status/phase
- **5c. MCP Proxy Migration** — if memory constrained with many concurrent agents, deploy Streamable HTTP MCP proxy (see Phase 1e Option A architecture). Migration is config-only: swap `.mcp.json` entries from stdio to http.
- **5d. Pipeline Definitions (future)** — YAML-defined composable task sequences, only when conductor-first proves insufficient
- **5e. Scheduled Tasks** — cron-like recurring work (nightly code review, weekly dependency updates) via conductor heartbeat or Go cron library

---

## Phase 6 — Agent Platform 📋

Long-running agents with memory, autonomy, and proactive behaviour.

- **6a. Persistent Agent Memory** — project-level markdown memory files (`/workspace/{project}/.claude/memory.md`), Hub appends task summaries after each run, referenced via CLAUDE.md, eventually vector store for semantic search. Claude Code's built-in memory features may reduce need for custom implementation.
- **6b. Autonomous Monitoring** — conductor-driven scheduled checks (git status, test results, dependency updates), start notification-only, add auto-fix behind approval gate. File watcher on project directories for real-time change detection.
- **6c. Multi-Agent Coordination** — container-per-project with multiple agents on separate worktrees within the same workspace (multiple tmux sessions inside one container), Agent Deck manages all sessions, conductor orchestrates handoffs, conflict resolution via git branch merging with human approval
- **6d. Cost + Usage Dashboard** — API token counts from Claude Code hooks or stream-json events, workspace uptime via Coder's Prometheus metrics endpoint, task success/failure rates from task JSON history, dashboard in Hub UI or Grafana

---

## Key Architectural Decisions

| Decision | Choice | Rationale | Status |
|----------|--------|-----------|--------|
| Workspace provisioning | OpenTofu templates via Coder | Eliminates devcontainer coexistence problem, template committed to repo | ✅ |
| Agent session model | tmux inside container (Pattern B) | Claude Code + tmux run natively in sandbox. Self-contained, resilient to disconnects. Host connects via `docker exec`. | ✅ |
| tmux location | Inside sandbox container, not on host | Disconnecting SSH/Hub doesn't kill agents. Containers are self-contained. One hop of indirection is acceptable. | ✅ |
| Presentation layer | **tmux pipe-pane → file → SSE** | `pipe-pane` streams to log file; `tail -f` reads and SSE streams to browser. **Changed from capture-pane** due to GitHub #16310 (autocompact clears scrollback). | ⚠️ Phase 0.6c |
| Firewall architecture | **Host namespace (DOCKER-USER chain)** | Rules in host namespace; containers cannot modify. **Changed from in-container iptables** because NET_ADMIN allows agents to flush rules. | 🔴 Phase 0.6a |
| Container capabilities | **No NET_ADMIN/NET_RAW** | Only SETUID/SETGID for entrypoint. Firewall configured by host before container starts. | 🔴 Phase 0.6a |
| SSH key handling | **SSH agent forwarding** | Private keys never enter containers. **Changed from read-only mount** which still allowed exfiltration. | 🔴 Phase 0.6b |
| Memory limits | **4-6GB per container, enforced** | Docker `--memory` flag with session recycling every 2-4 hours. Mitigates Claude Code memory leaks (30-120GB observed). | ⚠️ Phase 0.6e |
| Conductor | Separate long-running Claude instance | Persistent monitoring over time, not ephemeral per-request | ✅ |
| Session persistence | **SQLite with WAL mode** | ACID compliance for concurrent access. **Changed from filesystem JSON** which has race conditions with multiple agents. | ⚠️ Phase 0.6 |
| Notifications | Telegram bridge (Agent Deck) | `bridge.py` daemon, no public URL needed | ✅ |
| Hub language | Go | Same ecosystem as Agent Deck/Claude Squad/OpenCode. Single binary deploy. Future merge path with Agent Deck. | ✅ |
| Hub framework | Go `net/http` + `html/template` + htmx | Server-rendered, SSE-friendly, no JS build step, static binary. Escape hatch: content negotiation for JSON+HTML enables future React migration. | ⚠️ Limitations noted |
| Agent Deck integration | **Fork + evaluate replacement** | Bus factor = 1 (8 stars, 1 dev). Fork immediately. Evaluate building session management into Hub directly. **Changed from CLI automation** due to dependency risk. | 🔴 Phase 0.6d |
| API proxy | **Centralized in Hub backend** | All Claude API calls route through Hub. Enables cost tracking, rate limiting, audit logging, budget enforcement in one layer. | 📋 Phase 0.6g |
| Container model | Container-per-project | Multiple agents share container via worktrees (multiple tmux sessions inside one container), not container-per-agent | ✅ |
| MCP strategy (now) | Per-container processes (Option C) | Simple, works. ~50-100MB per agent acceptable on NUC with 32-64GB RAM. | ✅ |
| MCP strategy (future) | Streamable HTTP proxy (Option A) | Share MCP servers across containers via HTTP. ~85-90% memory reduction. Config-only migration. | 📋 |
| Multi-user | Single-user | Personal infrastructure on a NUC | ✅ |
| Cost tracking | **Langfuse integration** | MIT licensed, self-hostable, 19K+ stars. Token-level tracking with per-agent attribution and budget alerts. | 📋 Phase 0.6g |
| Backup strategy | **restic → Backblaze B2** | Daily offsite backups with documented recovery runbook. RPO/RTO targets defined. | 📋 Phase 0.6g |
| Secret management | **Docker Compose secrets** | Mounted as files at `/run/secrets/`. **Changed from environment variables** which are visible in `/proc`, `docker inspect`, logs. | 📋 Phase 0.6g |
| HTTP version | **HTTP/2 required** | SSE connection limit under HTTP/1.1 (6 per domain) blocks 5+ agent dashboards. HTTP/2 enables ~100 multiplexed streams. | ⚠️ Phase 0.6f |
| Intra-task orchestration | Superpowers plugin | Phase progression (brainstorm → plan → execute → review) via Claude Code plugin + filesystem JSON | ✅ |
| Inter-task orchestration | Conductor-first, formal DAG later | Conversational sequencing covers most cases; DAG only for deterministic pipelines | ✅ |
| AskUserQuestion | Test empirically, fallback to MCP tool | GitHub #10400 still open. If broken with --dangerously-skip-permissions, deploy custom ask-human MCP server. | ✅ |

## Adopt / Build / Borrow

| Strategy | Component | Status |
|----------|-----------|--------|
| **ADOPT** | ~~Agent Deck~~ → **Fork Agent Deck** (sessions, conductor, Telegram, forking, status detection) | ⚠️ Forked due to bus factor |
| **ADOPT** | Coder Community (workspace lifecycle, dashboard, API, Tasks, Mux) | ✅ |
| **ADOPT** | Claude Code CLI with `--dangerously-skip-permissions` in sandbox | ⚠️ Security implications documented |
| **ADOPT** | Existing infrastructure: Authelia, Traefik, Docker, Saltbox | ✅ |
| **ADOPT** | **Langfuse** (cost tracking, observability) — MIT, self-hostable, 19K+ stars | 📋 Phase 0.6g |
| **ADOPT** | **restic** (backup to Backblaze B2) | 📋 Phase 0.6g |
| **BUILD** | Hub Web UI (Go + htmx + SSE) | 📋 |
| **BUILD** | Multi-project router (keyword match + Claude Agent SDK fallback) | 📋 |
| **BUILD** | SSE bridge (`tmux pipe-pane` → file → `tail -f` → browser) | 📋 Changed from capture-pane |
| **BUILD** | Workspace dashboard (Coder API wrapper) | 📋 |
| **BUILD** | Diff viewer + approve/reject workflow | 📋 |
| **BUILD** | Sandbox tmux integration (entrypoint changes, pipe-pane, multi-session) | 📋 |
| **BUILD** | **Centralized API proxy** (cost tracking, rate limiting, audit logging) | 📋 Phase 0.6g |
| **BUILD** | **Host-side firewall manager** (DOCKER-USER chain rules per container) | 🔴 Phase 0.6a |
| **BUILD** | **Session recycling daemon** (memory leak containment) | 📋 Phase 0.6e |
| **BORROW** | Workmux patterns (container isolation + coordinator orchestration) | ✅ |
| **BORROW** | Coder UI patterns (workspace management dashboard) | ✅ |
| **BORROW** | agtx patterns (kanban-style task layout) | ✅ |
| **BORROW** | Cursor 2.0 patterns (agent-as-managed-process sidebar) | ✅ |

## Competitive Landscape (Updated February 2026)

The competitive landscape shifted significantly in late 2025 / early 2026:

| Solution | Status | Overlap | Key Gap for Saltbox |
|----------|--------|---------|---------------------|
| **Coder Tasks + Mux** | Production | ~80% | Mobile support; requires Kubernetes expertise |
| **OpenHands** | Production (38K+ stars, $18.8M funding) | ~70% | No mobile-first interface; autonomous focus |
| **Ona (ex-Gitpod)** | Pivoting | ~60% | Self-hosting limited to AWS VPC |
| **Daytona.io** | $24M Series A | Infrastructure | Sandbox infrastructure, not orchestration UI |
| **Workmux** | Active | ~80% | No web UI or mobile access |
| **agtx** | Active | ~60% | No containers, no web |
| **Claude Squad** | Mature | ~55% | No container isolation |

**Saltbox's unique niche:** True on-prem simplicity (NUC-deployable, no Kubernetes), container isolation, multi-agent orchestration, AND mobile-first access. No competitor delivers all four. The window is narrowing — speed matters, but shipping the Phase 0.6 security fixes matters more.

---

## Design Principles

1. **Security first** — every new surface gets Authelia, every workspace gets least-privilege. All seven security layers preserved through every architecture change. New webhook endpoints get bearer token auth. Security assessment required before adding Claude/LLM to the outer layer (see `docs/security-assessment.md`). **Critical addition (Feb 2026):** Container capabilities must be minimal — NO NET_ADMIN/NET_RAW. Firewall rules live in host namespace only. SSH keys never mounted, only agent-forwarded. See Phase 0.6 for full security overhaul.

2. **Self-hosted everything** — no cloud dependencies beyond the Claude API. All state on your NUC, all traffic through your Traefik, all auth through your Authelia.

3. **Container isolation** — every project interaction happens inside an OpenTofu-provisioned Coder workspace (currently a `sandbox.sh`-managed Docker container). The Hub, Agent Deck, and bots are orchestrators that `docker exec` into containers, never run code directly. tmux runs inside containers — containers are self-contained agent environments.

4. **Mobile-first for interaction, desktop for development** — the Hub is essential (not optional) for making mobile a first-class experience. tmux on mobile (Terminus + SSH) is usable for dispatch and monitoring but painful for extended interaction — modifier key combos, small screen, no autocomplete. A mobile-optimised HTML interface with proper touch targets, chat input, and approve/reject buttons is dramatically better. Telegram bridge fills the gap before Hub exists. code-server (via Coder) is for full IDE work.

5. **Incremental adoption** — each phase is independently useful. Phase 0 works forever alone. Nothing breaks if you skip a phase.

6. **tmux is the truth** — agent session content lives in tmux scrollback inside the container, not custom message arrays or databases. The Hub reads from tmux via `docker exec`, never owns the conversation.

7. **Convention over configuration** — `projects.yaml` for routing, OpenTofu templates for workspace definitions, Docker Compose secrets for credentials (not `.env`). Sync chain documented: `repo → setup.sh → workspace → container`.

8. **One language** — Go across the stack. Hub, Agent Deck, and future tooling share a language, enabling code sharing and eventual binary consolidation.

9. **Observability by default** — All Claude API calls route through a centralized proxy for cost tracking, rate limiting, and audit logging. Token-level attribution per agent. Budget alerts at 50%/80%/100%.

10. **Resource containment** — Docker memory limits enforced (4-6GB per container). Session recycling every 2-4 hours to mitigate Claude Code memory leaks. Realistic concurrency: 3-5 agents on 64GB NUC.

11. **Minimal dependencies** — Fork or replace single-maintainer dependencies. Agent Deck (bus factor = 1) forked immediately; evaluate in-house replacement.
