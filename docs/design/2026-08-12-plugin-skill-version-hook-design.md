# Automatically patch-bump marketplace versions for skill changes

## Motivation

The Agent Deck marketplace publishes the skills enumerated in
`.claude-plugin/marketplace.json`. A change anywhere within one of those
skill directories changes the plugin payload, but the marketplace metadata
version has historically relied on a contributor remembering a manual bump.
That omission can leave consumers without a distinct plugin update.

## Decision

Add a Lefthook `pre-commit` command that automatically increments the patch
component of `.claude-plugin/marketplace.json`'s `metadata.version` whenever
the staged index includes a change below a marketplace-published skill
directory.

The hook will determine published skill directories from the manifest's
`plugins[*].skills` entries. It will therefore cover every file below a listed
directory, including reference material and helper scripts, while excluding
unpublished `skills/` directories.

## Behavior

- If no staged file is below a listed skill directory, the hook leaves the
  manifest unchanged.
- If one or more such files are staged, the hook increments the patch version
  exactly once and stages the manifest update.
- If a contributor has already staged a higher valid semantic version, the
  hook preserves it instead of applying a second bump. This supports an
  intentional minor or major release.
- The hook fails before committing if the manifest cannot be parsed or its
  version is not a three-component numeric semantic version. Its error tells
  the contributor how to correct the manifest.

## Implementation

A small repository-owned script will be called from the existing Lefthook
`pre-commit` section. Python's standard JSON support will read and rewrite the
manifest; no package dependency or new configuration is introduced. The
script will inspect staged paths, not the worktree, so its decision exactly
matches the commit being created.

Tests will exercise the script in temporary Git repositories for ordinary
files, nested files in published skill directories, multiple skill changes,
manual higher-version bumps, and invalid manifests.

## Out of scope

- Automatically changing minor or major versions.
- Publishing or installing the marketplace plugin.
- Requiring bumps for unpublished skill directories.
- Validating versions at pre-push or CI in this change.
