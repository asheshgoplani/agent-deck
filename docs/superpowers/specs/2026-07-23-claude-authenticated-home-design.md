# Authenticated Claude Home and Shared Codex Status Bar Design

## Goal

Agent Deck Claude sessions must use the same authenticated Claude profile as a
normal `claude` invocation while retaining centralized, home-scoped managed
skills. Every isolated Agent Deck Codex home must also receive one declarative
status-bar definition.

## Root Cause

The configured `~/.agent-deck/claude` directory is a distinct Claude profile.
Although Claude can discover the account through macOS credential storage,
that directory lacks the completed-onboarding state, normal settings, and
credential file present in `~/.claude`. Interactive launches therefore enter
first-run onboarding and may request authentication.

## Design

Set `[claude].config_dir` to `~/.claude`. Keep every group sharing that
physical home on the same declarative skill set, and materialize the managed
`port-registry` and `web-perf` links under `~/.claude/skills`.

Remove only Agent Deck-managed links and the Agent Deck manifest from the
obsolete `~/.agent-deck/claude` home. Preserve all other files there.

## Codex TUI Defaults

The Codex CLI reads `[tui].status_line` and `status_line_use_colors` from the
active `$CODEX_HOME/config.toml`. Agent Deck uses separate homes per group, so
the values in `~/.codex/config.toml` do not apply to those sessions.

Add an optional `[codex.tui]` block to Agent Deck configuration:

```toml
[codex.tui]
status_line = ["model-with-reasoning", "context-used", "git-branch", "branch-changes", "run-state", "permissions", "current-dir"]
status_line_use_colors = true
```

At Codex session loadout reconciliation and explicit `group codex sync`, merge
only these managed keys into the resolved group home’s `[tui]` table. Preserve
all unrelated Codex configuration, including MCP servers, plugins, project
trust, and internal tooltip state. An absent field is unmanaged; an explicit
empty `status_line` remains meaningful and hides the footer.

## Verification

- `claude auth status` with `CLAUDE_CONFIG_DIR=~/.claude` reports logged in.
- Every configured top-level group resolves Claude `config_dir` to
  `/Users/doozyx/.claude` without a configuration error.
- A disposable Claude session created through Agent Deck reaches the normal
  interactive prompt without first-run onboarding or login.
- The disposable session is removed after verification.
- Every configured Codex home contains the same managed status line and retains
  its pre-existing non-status configuration.
