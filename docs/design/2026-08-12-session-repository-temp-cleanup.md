# Per-Repository Session Temporary Cleanup

## Motivation

Agent sessions can leave large binaries, logs, and tool scratch files in the
host system temporary directory after their work is finished. Agent Deck can
only remove such files safely when it establishes ownership before the session
creates them; filename prefixes such as `agent-deck-*` are not proof of
ownership.

Human request: "/private/tmp/agent-deck-task07-r2
/private/tmp/uss3682-e2e-task.log
/private/tmp/uss3682-e2e-task2.log can be cleanup and all other similar files in
this folder can they be auto removed when session finished?"

The user defined "finished" as explicit session removal and chose repository-
local storage with no fallback: each repository has its own `.agent-deck/`, the
whole directory is globally Git-excluded, and session startup must fail if its
temporary directory cannot be established safely.

## Decisions

1. A local session owns
   `<project-root>/.agent-deck/tmp/<session-id>/`. For a path inside a Git
   repository, `project-root` is that worktree's root. For a non-Git local
   project, it is the session's selected project directory.
2. Agent Deck idempotently adds `.agent-deck/` to Git's global excludes file.
   It does not create a shared or home-level `.agent-deck/tmp` directory.
   Ignore rules do not untrack files already committed by the user.
3. Session startup is fail-closed. If the global exclude or owned temporary
   directory cannot be created and validated, the session does not start.
   There is no system-temporary fallback.
4. Stop, archive, and restart preserve the temporary directory. Explicit
   session removal deletes it after the session process has terminated.
5. Arbitrary legacy paths and explicit writes outside the owned directory are
   never inferred from names and are not automatically deleted.

## Architecture

### Project-root resolution

For a normal Git checkout or linked worktree, Agent Deck resolves the root of
the current worktree rather than the shared main-worktree root. This keeps each
worktree session's disposable state beside that worktree. A local non-Git
project uses its canonical selected project directory. Remote SSH sessions do
not create a host-local repository temporary directory; remote temporary-file
lifecycle remains out of scope.

### Global Git exclusion

Before the first repository-local temporary root is created, Agent Deck resolves
Git's effective global excludes file: `core.excludesFile` when configured,
otherwise Git's standard XDG global ignore path. It appends a standalone
`.agent-deck/` rule only when an equivalent rule is absent, preserving all
existing content. Failure to locate, create, or update the file is a startup
error. Concurrent starts serialize this update and recheck under the lock so
the rule is never duplicated.

### Owned session root

Agent Deck creates `.agent-deck/tmp/<session-id>` with mode `0700` and writes a
regular-file ownership marker containing a schema version and the full session
ID. Creation rejects symlinked `.agent-deck`, `tmp`, session-root, or marker
components, and validates that the canonical session root remains beneath the
canonical project root.

For every native local launch and restart, the prepared command and tmux
session environment receive:

- `TMPDIR=<owned-root>`
- `TMP=<owned-root>`
- `TEMP=<owned-root>`
- `AGENT_DECK_SESSION_TMPDIR=<owned-root>`

The same stable path is reused for the lifetime of the Agent Deck session.
Sandbox launches expose the repository-local directory inside the sandbox and
set the four variables to its sandbox-visible path; inability to do so is a
startup error rather than a fallback.

### Removal

All single and bulk session-removal surfaces share one cleanup operation. They
first terminate and wait for the session process. Cleanup then verifies:

- the expected project root and session ID are non-empty;
- the `.agent-deck`, `tmp`, session-root, and marker paths are not symlinks;
- the canonical session root is exactly the expected direct child of
  `<project-root>/.agent-deck/tmp`;
- the marker schema and full session ID match;
- the root and marker are owned by the current user.

Only the verified session directory is recursively removed; sibling session
directories and the shared `.agent-deck/tmp` parent remain. Cleanup failure is
reported and makes the remove command fail instead of claiming complete
cleanup. The session registry row remains available so removal can be retried.

Transcripts, project files outside the owned root, worktrees, and already
tracked `.agent-deck` content are unaffected.

## Interfaces

This change adds no user-facing configuration or artifact-registration command.
The observable interface is the four environment variables and the fail-closed
startup/removal behavior. Existing `remove`, `session remove`, bulk errored
removal, cleanup, and web removal paths use the same ownership checks.

## Verification

Automated tests cover:

- root selection for Git worktrees and non-Git projects;
- idempotent global exclusion without overwriting existing rules;
- startup refusal when exclusion or root creation fails;
- mode, marker, and all four environment variables on launch and restart;
- preservation across stop, archive, and restart;
- successful single and bulk removal;
- refusal of foreign markers, wrong session IDs, wrong ownership where the
  platform permits, symlink substitution, and containment escapes;
- preservation of sibling sessions and unrelated repository files;
- removal failure leaving the registry row retriable;
- sandbox-visible path wiring;
- remote sessions avoiding host-local repository temporary roots.

An end-to-end test starts a local disposable session, writes a file through
`$AGENT_DECK_SESSION_TMPDIR`, stops and restarts the session to prove
preservation, then removes it and proves that only its marked directory is gone.

## Out of scope

- Sweeping legacy or unmarked `/private/tmp/agent-deck-*` paths.
- Deleting files explicitly written outside the owned session root.
- Managing temporary files on remote SSH hosts.
- Removing `.agent-deck/tmp` or `.agent-deck` when the last session disappears.
- Untracking `.agent-deck` files already committed by a repository.
- A user-configurable temporary-root location or cleanup-age policy.

## Principles pass

The design has one root resolver, one marker contract, one environment-injection
path, and one removal operation, avoiding duplicated ownership rules. It adds no
registry, daemon, filename sweeper, or configuration surface. Every component
exists for a stated requirement: repository-local containment, global Git
exclusion, fail-closed startup, restart persistence, or safe removal.
