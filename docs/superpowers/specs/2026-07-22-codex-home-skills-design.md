# Codex Home-Scoped Group Skills Design

## Goal

Store declarative `[groups.<path>.codex].skills` once in the group's resolved
`CODEX_HOME/skills` instead of materializing the same links into every session
repository. Keep explicit `agent-deck skill attach` project-scoped.

## Behavior

- Codex group skills resolve through the existing ancestor union.
- `ApplyConfiguredLoadout` materializes those skills under
  `<CODEX_HOME>/skills/<entry>` and records ownership in
  `<CODEX_HOME>/.agent-deck/skills.toml`.
- Claude group skills and all explicit `skill attach` operations keep their
  current project-scoped behavior.
- Codex MCPs and plugins keep their existing home-scoped behavior.
- Reconciliation remains an attach-only floor: missing managed links heal,
  healthy links are no-ops, foreign targets are never overwritten, and removing
  config does not silently delete an installed skill.

## Shared-home safety

A child group may inherit the skills declared by the group that supplies its
`config_dir`. If a descendant declares additional Codex skills while inheriting
that same home, agent-deck refuses the child-only additions and warns that the
child needs its own `config_dir`. Explicit group homes that resolve to the same
filesystem path must also resolve to the same skill set; divergent sets are
rejected. This prevents one child loadout from leaking into siblings.

## Migration

The runtime does not delete existing project manifests because old entries do
not record whether they came from declarative provisioning or a deliberate
manual attach. New starts stop creating repo-local entries. Existing redundant
managed links can be detached explicitly after the home copy is verified.

## Error handling and security

- A Codex group with skills but no resolved `config_dir` emits a warning and
  does not touch the repository.
- Home targets are constrained to the managed `skills` directory before any
  removal or replacement.
- Source resolution, symlink validation, copy fallback, and foreign-target
  refusal reuse the existing skill catalog rules.

## Tests

- A Codex group loadout creates `<CODEX_HOME>/skills` and leaves the project
  without `.agents` or `.agent-deck` state.
- Reapplying heals a missing managed home link.
- A foreign home target is preserved with a warning.
- Explicit Codex `skill attach` still writes `.agents/skills` in the project.
- Child-only skills sharing an inherited home are rejected.
- Different explicit groups sharing one home with divergent skills are rejected.
- Existing MCP and plugin behavior remains covered by focused regression tests.
