# Current Tasks

## In Progress

- [ ] Vagrant Mode ("Just Do It") feature -- design complete, awaiting implementation

## Completed This Session

- [x] Investigated why `agentic-ai-brainstorming` skill was unavailable in agent-deck (project-scoped vs global)
- [x] Copied `agentic-ai-brainstorm`, `agentic-ai-implement`, `agentic-ai-plan` skills to `~/.claude/skills/` (global)
- [x] Pushed skills to skeleton repo (`git@github.com:jonnocraig/skeleton.git`) on `feature/agentic-ai-skills` branch
- [x] Multi-perspective brainstorm for Vagrant mode feature (Architect, Implementer, Devil's Advocate, Security Analyst)
- [x] Wrote design document: `docs/plans/2026-02-14-vagrant-mode-design.md`
- [x] Added MCP compatibility section (HTTP URL rewrite, STDIO provisioning, global/user config propagation)
- [x] Added crash recovery & resilience section (VM health check, restart flow, agent-deck crash recovery)
- [x] Updated error handling table with crash scenarios
- [x] Copied `restart.md` and `catchup.md` commands from supabase project to `.claude/commands/`

## Pending

- [ ] Create implementation plan using `agentic-ai-plan` skill (enriches design doc with agent orchestration metadata)
- [ ] Set up git worktree for implementation
- [ ] Execute plan with agent team using `agentic-ai-implement`
- [ ] Create PR for skeleton repo `feature/agentic-ai-skills` branch

## Blocked

- None
