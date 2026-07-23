# Claude Home-Scoped Group Skills Design

## Goal

Stop declarative Claude group and conductor skills from creating duplicate
`.agents/skills` and `.agent-deck/skills.toml` artifacts in every repository.
Materialize those skills once in the Claude configuration home selected for the
session, while preserving explicit project attachments.

## Ownership Model

Declarative entries from `[groups.<path>.claude].skills` and
`[conductors.<name>.claude].skills` are owned by the effective
`CLAUDE_CONFIG_DIR`:

- skill targets: `<CLAUDE_CONFIG_DIR>/skills/<entry>`
- ownership manifest: `<CLAUDE_CONFIG_DIR>/.agent-deck/skills.toml`

`agent-deck skill attach` and `agent-deck skill detach` remain explicitly
project-scoped and continue to use `<project>/.agents/skills` plus
`<project>/.agent-deck/skills.toml`. Claude plugins and MCPs retain their
existing behavior; this design changes only declarative skill ownership.

## Resolution and Shared-Home Safety

The effective home comes from the existing instance-aware Claude resolver, in
its current precedence order: account, conductor, group, environment, profile,
global, then default. The desired skill set is the root-first union of the
instance group chain and conductor additions.

Any groups or conductors that can resolve to the same physical Claude home must
resolve the same declarative skill set. Divergence blocks launch with an error
that identifies the conflicting owners and tells the user to either standardize
their skills or assign a distinct `config_dir`. Physical-home comparison uses
the Codex safety rules already in production:

- existing paths use filesystem identity;
- symlink aliases resolve to one home;
- missing case-only variants below one existing ancestor are conservatively
  treated as one prospective home;
- paths containing a `..` component are rejected before cleaning;
- command, account, environment, or profile selection is checked against the
  actual home used for materialization.

The selected deployment keeps the authenticated global home
`~/.agent-deck/claude`. Top-level group declarations are standardized to the
same two shared skills, `shared/port-registry` and `claude-setup/web-perf`, so
the shared-home invariant holds without creating new Claude identities or
requiring reauthentication.

## Reconciliation

Claude home reconciliation reuses the hardened Codex mechanisms rather than
introducing a second filesystem implementation:

- in-process and cross-process locking around one home manifest;
- atomic manifest writes;
- relative symlink materialization with directory-copy fallback;
- missing managed targets heal on the next create/start/restart;
- foreign files, directories, and symlinks are never overwritten;
- repeated and concurrent reconciliation is idempotent.

The public Codex and Claude entry points remain tool-specific wrappers so call
sites cannot accidentally select the wrong home. Shared internal helpers own
manifest loading, target validation, materialization, and health checks.

Unsafe resolution is a launch error, not a warning followed by a contaminated
session. Ordinary source-resolution or foreign-target failures remain visible
loadout warnings and do not clobber user data.

## Runtime Integration

`ApplyConfiguredLoadout` resolves Claude home skills before processing plugins
and MCPs. Declarative Claude skills use the home attachment wrapper; explicit
project operations still use the project wrapper. Local Claude sessions no
longer require a project path merely to reconcile home skills, although
project-scoped plugins and MCPs keep their existing project requirements.

Create, start, restart, and start-with-message paths continue calling the same
loadout function, so configuration changes and missing-link healing take effect
at the current lifecycle boundaries. SSH sessions remain unchanged because the
local process cannot safely materialize a remote Claude home.

`group show --resolved --json` exposes the safe resolved Claude skill set and a
`config_error` when the shared-home invariant fails, matching the Codex
diagnostic surface.

## Live Migration and Cleanup

Migration is ordered to avoid a skill availability gap:

1. Standardize the tracked top-level Claude skill declarations.
2. Provision and verify the shared Claude-home manifest and both skill targets.
3. Verify a repeated reconciliation is a no-op and resolved group output is
   safe for every configured top-level group.
4. Remove only legacy project manifest entries whose IDs and source paths match
   the now-home-scoped declarative skills and whose targets are healthy
   agent-deck-managed symlinks.
5. Remove `.agents` or `.agent-deck` directories only when they become empty.

Manual directories, foreign symlinks, explicit attachments for other skills,
unrelated dirty files, and repositories without an ownership manifest are not
removed. The runtime itself does not perform broad historical cleanup; the live
migration is an audited one-time operation.

## Testing and Acceptance

Tests must first fail against the current project-scoped behavior and then
cover:

- initial Claude-home materialization without project artifacts;
- healing a missing managed target;
- preservation of a foreign target;
- repeated and concurrent writers preserving all manifest entries;
- group and conductor union semantics;
- divergent declarations sharing one home blocking launch;
- account, group, environment, profile, and global home resolution;
- symlink, case-alias, missing-case-alias, and parent-traversal safety;
- `group show --resolved --json` reporting unsafe configuration;
- explicit `skill attach` remaining project-scoped;
- existing Claude plugin and MCP behavior remaining unchanged.

Focused tests run under `-race`. The repository gate uses exactly:

```sh
HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...
```

Formatting, `go vet ./...`, a binary build, resolved-group CLI tests, and a
fresh reviewer pass are required before installation and live cleanup.
