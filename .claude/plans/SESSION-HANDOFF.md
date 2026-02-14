# Session Handoff - 2026-02-14

## What Was Accomplished

- Diagnosed skill scoping issue (project-local vs global `~/.claude/skills/`)
- Copied 3 agentic-ai skills globally and pushed to skeleton repo on `feature/agentic-ai-skills` branch
- Ran full multi-perspective brainstorm (4 parallel agents) for Vagrant mode feature
- Wrote comprehensive design document (621 lines) at `docs/plans/2026-02-14-vagrant-mode-design.md`
- Design covers: UI checkbox, VM lifecycle, command wrapping, Vagrantfile generation, static skill
- Added MCP compatibility section: URL rewrite, STDIO provisioning, global config propagation
- Added crash recovery section: VM health check, restart flow, agent-deck crash recovery, contextual error messages
- Copied `restart.md` and `catchup.md` commands to `.claude/commands/`

## Current State

- Branch: `feature/teammate-mode`
- Feature: Vagrant Mode ("Just Do It") -- design phase complete
- Status: Design document finalized, ready for implementation planning

## Open Issues

- Skeleton repo PR not yet created (branch pushed, PR URL needs `gh pr create`)
- Design doc not yet committed to git

## Next Steps (in order)

1. Run `/catchup` to restore context in new session
2. Read the design document: `docs/plans/2026-02-14-vagrant-mode-design.md`
3. Create implementation plan using `agentic-ai-plan` skill (enriches design with agent orchestration)
4. Set up a git worktree for isolated implementation
5. Execute plan with `agentic-ai-implement` (parallel agent team)
6. Create PR for skeleton repo's `feature/agentic-ai-skills` branch

## Important Context

- Design doc is the source of truth: `docs/plans/2026-02-14-vagrant-mode-design.md`
- Decisions recorded in `.claude/plans/DECISIONS.md`
- The feature adds 3 new files: `internal/vagrant/manager.go`, `internal/vagrant/skill.go`, `internal/vagrant/mcp.go`
- The feature modifies 4 files: `claudeoptions.go`, `tooloptions.go`, `instance.go`, `userconfig.go`
- Agent-deck is a Go 1.24 TUI app using Bubble Tea, sessions are tmux-based
- MCP tools use three scopes: LOCAL (.mcp.json), GLOBAL (~/.claude/.claude.json), USER (~/.claude.json)
- Pool sockets always bypassed for vagrant sessions (STDIO fallback instead)
- VirtualBox NAT host gateway is `10.0.2.2` (configurable via `[vagrant] host_gateway_ip`)
- Claude conversations survive VM destruction (session ID stored server-side, `--resume` flag)

## Commands to Run First

```bash
# Check branch status
git status
git log --oneline -5

# Read the design doc
cat docs/plans/2026-02-14-vagrant-mode-design.md
```
