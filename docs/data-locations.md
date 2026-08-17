# Where agent-deck writes

Agent Deck writes to two places, and which one a file belongs in is decided by
one question:

> **If the user deleted this project tomorrow, would this file still mean
> anything?**

If yes, it is *machine state* and belongs in the global data directory. If no,
it is a *project artifact* and belongs in the project.

That question was not written down when the autonomous context-budget handoff
was built, which is how `PROMPT.md` — a wrap-up describing work in one
repository — ended up pooled in `~/.agent-deck/handoff/<session-id>/` with
nothing tying it back to the project it came from.

## Project-local: `<project-root>/.agent-deck/`

Resolved by `session.ProjectDataPath` / `session.EnsureProjectDataPath`.

`<project-root>` is the repository's **main worktree**, so a session running in
`.worktrees/feature-x` and one running in the main checkout share a single
`.agent-deck/` tree instead of scattering a run's artifacts across checkouts.
The brainstorming and orchestrate skills compute the same root; the Go side and
the skills must not disagree about where a run's artifacts live.

A working directory that is not in a git repository is its own root.

| Path | Written by |
| --- | --- |
| `.agent-deck/handoff/<session-id>/PROMPT.md` | The autonomous context-budget wrap-up (`internal/ui/context_budget_ui.go`), read by `agent-deck session handoff` |
| `.agent-deck/<run-id>/design/design.md` | The `brainstorming` skill |
| `.agent-deck/<run-id>/plan/`, `.agent-deck/<run-id>/orchestrate/` | The `orchestrate` skill |
| `.agent-deck/skills.toml` | Project skill attachments (per checkout, not folded to the main worktree — a checkout may legitimately attach different skills) |
| `.agent-deck/tmp/<session-id>/` | Per-session `TMPDIR` (per checkout, so scratch stays next to the code it belongs to) |
| `.agent-deck/worktree-setup.sh`, `.agent-deck/worktree-destruction.sh` | Authored by the user; run on worktree create/destroy |

`.agent-deck/` is kept out of `git status` through the user's global git
excludes file (`ensureRepositoryGitExclude`, `internal/session/session_temp.go`).
Agent Deck never writes a repository's tracked `.gitignore` — those are the
user's committed files.

## Machine-global: `$XDG_DATA_HOME/agent-deck/`

Resolved by `agentpaths.EffectiveDataPath` (legacy `~/.agent-deck/` is still
honored per-name when it already exists). Config lives under
`$XDG_CONFIG_HOME/agent-deck/`, disposable cache under `$XDG_CACHE_HOME/agent-deck/`.

Everything here describes the machine or a session's runtime, not a project:

- **Registry and config** — `profiles/<p>/state.db`, `config.toml`, `sessions.json`
- **Session runtime and IPC** — `hooks/`, `inboxes/`, `events/`, `locks/`,
  `runtime/` (auth-hold, completion-ledger, consumed-turns, queued-message,
  runtime-queue-*, spawn-failure), `badge-updates/`, `ack-signal`, `sockets/`
- **Logs** — `logs/`, plus rotated debug logs in the cache dir
- **Agent homes** — `worker-scratch/`, `claude/`, `codex/` (credentials and
  settings; per session or per group, never per project)
- **Long-lived services** — `conductor/`, `watcher/`, `triage/`, `watchdog/`
- **Cross-project by construction** — `multi-repo-worktrees/`, which holds one
  symlink tree spanning several repositories and so has no single project root

These are keyed by session id and read by the daemon, the hook handler, and the
CLI without a project checkout necessarily existing — a session's repository can
be deleted while its runtime state is still being drained.

## The rule is enforced

`TestNoProjectScopedNamesUnderGlobalDataDir`
(`internal/agentpaths/project_scoped_path_lint_test.go`) fails the build if a
project-scoped name (`handoff`, `orchestrate`, `evidence`, `design`, `plan`) is
passed to a global path resolver. Extend `projectScopedNames` when a new kind of
project artifact appears.
