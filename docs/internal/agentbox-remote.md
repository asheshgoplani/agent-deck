# Agentbox remote implementation note

Date: July 22, 2026

## What this adds

Agent Deck now supports two remote kinds:

- `ssh`: the existing remote `agent-deck` binary model
- `agentbox`: an HTTP API model that talks directly to Agentbox Workspace routes

The Agentbox path is intentionally small and Warp-oriented:

- register a remote with `kind = "agentbox"` plus `url` and optional `token`
- list Workspaces via `/v1/workspaces`
- create Workspaces via `/v1/workspaces`
- start/stop/destroy via the Workspace lifecycle routes
- attach via `/v1/workspaces/:id/attach`

No `agent-deck` binary is installed or required inside an Agentbox Workspace.

## Important behavior choices

- Agentbox create requires explicit `name`, `orchestrator`, `agent`, `model`, and `runtime`.
  - We do not invent defaults in Agent Deck because those values are part of the user’s intentional workspace contract.
- Attach preserves stopped-before-attach semantics.
  - Agent Deck does not auto-start a stopped Workspace during attach.
  - When Agentbox returns `workspace_not_running` or `workspace_not_attachable`, the error is translated into a clearer user-facing message.
- When the configured Agentbox URL resolves to localhost/the local machine, Agent Deck prefers `localAttachCommand`.
  - Otherwise it uses the regular remote attach command.

## Current non-goals

- Agentbox remote preview/output scraping is not implemented.
  - Existing SSH remotes still support remote preview and insert-mode key streaming.
- The legacy TUI remote-create dialog is not yet Agentbox-aware.
  - It does not collect `orchestrator`, `agent`, `model`, or `runtime`.
  - The supported minimal Agentbox create flow is the CLI/Warp path (`agent-deck remote create ...`).

## Files to read first

- `internal/session/agentbox.go`
- `cmd/agent-deck/remote_cmd.go`
- `internal/web/handlers_sessions.go`
- `internal/ui/home.go`
