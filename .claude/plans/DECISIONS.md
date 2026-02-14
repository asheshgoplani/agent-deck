# Architecture Decisions

## 2026-02-14 - Vagrant Mode: Wrapper Command Approach (Approach 1)

**Context**: User wants a "Just do it" checkbox that spawns Claude Code in an isolated Vagrant VM with `--dangerously-skip-permissions` and sudo access. Three approaches evaluated by multi-perspective brainstorm.
**Decision**: Wrapper Command approach -- add checkbox, auto-manage VM lifecycle, wrap commands via `vagrant ssh -c`. No provider abstraction, no security hardening beyond VM isolation.
**Consequences**: Minimal complexity (4 modified + 3 new files). VirtualBox dependency. First boot latency (5-10 min). Bidirectional sync risk accepted as intentional.

## 2026-02-14 - Vagrant Mode: Force skip-permissions when vagrant mode enabled

**Context**: Should the user be able to disable `--dangerously-skip-permissions` while in vagrant mode?
**Decision**: Force it on automatically. The entire purpose of vagrant mode is unrestricted access in a safe sandbox.
**Consequences**: Simpler UX. Users who want restricted mode should not use vagrant mode.

## 2026-02-14 - MCP Compatibility: VM-Aware Config Generation

**Context**: MCP tools configured in agent-deck need to work inside the Vagrant VM, but `.mcp.json` references host-side resources (localhost URLs, Unix sockets, host commands).
**Decision**: Generate a VM-specific `.mcp.json` via `WriteMCPJsonForVagrant()` that rewrites HTTP URLs (`localhost` -> `10.0.2.2`), bypasses pool sockets (STDIO fallback), and provisions npm MCP packages in the Vagrantfile. Global/user Claude configs propagated via `SyncClaudeConfig()`.
**Consequences**: HTTP MCPs work out of the box. STDIO MCPs require npm packages installed in VM. Non-npm MCPs (python, custom binaries) need manual VM provisioning. Pool sockets always bypassed (higher memory usage per session).

## 2026-02-14 - Crash Recovery: VM Health Check in UpdateStatus()

**Context**: Vagrant VM can crash independently of agent-deck. Need to detect and surface VM failures.
**Decision**: Periodic health check (60s interval) piggybacked on existing `UpdateStatus()` polling. Uses `vagrant status --machine-readable` with in-memory caching. Contextual error messages per VM state. Press R triggers state-aware recovery (resume/destroy+up/reload).
**Consequences**: ~100-200ms overhead every 60s for vagrant sessions. Claude conversations survive VM destruction (session ID stored server-side). `vagrant resume` used for suspended VMs (5s vs 30-60s).

## 2026-02-14 - Crash Recovery: agent-deck Crash is Automatic

**Context**: What happens when agent-deck itself crashes while a vagrant session is running?
**Decision**: No special handling needed. tmux session survives, VM survives, Claude survives. On restart, `ReconnectSessionLazy()` reconnects to existing tmux session. `HealthCheck()` confirms VM state on next poll.
**Consequences**: Zero-effort recovery for agent-deck crashes. Only edge case: crash during `vagrant up` before Claude launches -- handled by restart flow detecting partial VM state.
