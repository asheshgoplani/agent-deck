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
  - The CLI, web create route, and remote TUI dialog all enforce those required fields.
- Agent Deck preserves the full `POST /v1/workspaces` create response.
  - `agent-deck remote create <agentbox-remote> ...` prints attachability plus the exact returned remote/local attach commands without doing a follow-up list request.
  - The TUI immediate create-and-attach path also uses those returned commands directly instead of doing a redundant follow-up `/v1/workspaces/:id/attach` lookup.
- Attach preserves stopped-before-attach semantics.
  - Agent Deck does not auto-start a stopped Workspace during attach.
  - When Agentbox returns `workspace_not_running` or `workspace_not_attachable`, the error is translated into a clearer user-facing message.
- When the configured Agentbox URL resolves to localhost/the local machine, Agent Deck prefers `localAttachCommand`.
  - Otherwise it uses the regular remote attach command.
- Shift+Enter / “open in new terminal” for a running Agentbox remote resolves attach through the authoritative `/v1/workspaces/:id/attach` endpoint at launch time.
  - This avoids launching from stale cached list data when the Workspace stopped, was destroyed, or became otherwise unattached elsewhere.
  - SSH remotes keep the existing `agent-deck session attach ...` SSH launcher path unchanged.
- Web remote-create errors preserve upstream meaning instead of collapsing to generic 500s.
  - `invalid_request` → 400
  - `workspace_root_conflict` / `workspace_disk_exhausted` / `invalid_state` → 409/507 as appropriate
  - `workspace_root_unconfigured` / `workspace_unavailable` / `workspace_runtime_unavailable` → 503

## Current non-goals

- Agentbox remote preview/output scraping is not implemented.
  - Existing SSH remotes still support remote preview and insert-mode key streaming.

## Files to read first

- `internal/session/agentbox.go`
- `cmd/agent-deck/remote_cmd.go`
- `internal/web/handlers_sessions.go`
- `internal/ui/home.go`
- `internal/ui/newdialog.go`
