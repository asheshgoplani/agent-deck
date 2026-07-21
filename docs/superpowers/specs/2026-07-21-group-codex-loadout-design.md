# Group-Scoped Codex Loadouts

## Problem

Agent Deck can declare Claude-specific group loadouts for skills, MCPs, and
plugins. Codex sessions support manually attached project skills, but do not
have a matching group configuration. Teams therefore cannot assign a stable
Codex home, shared project skills, or MCP set to every session in a group.

Codex native plugins must not be installed or updated as a side effect of
starting a session. Installation can require network access and changes state
outside the project, so a launch-time installer would make sessions
non-deterministic.

## Design

Add a `[groups."<path>".codex]` configuration block. It provides:

- `config_dir`: the group-specific `CODEX_HOME` directory;
- `command` and `env_file`: group-scoped Codex launch overrides;
- `skills`: entries from Agent Deck's skill-source registry;
- `mcps`: entries from the existing `[mcps.<name>]` catalog.

Group settings follow the existing ancestor rules. Scalar settings use the
nearest configured group. Skill and MCP lists are unioned from the root group
through the exact group, preserving order and deduplicating entries.

At Codex session creation and before every start or restart, Agent Deck
materializes configured skills into the project's `.agents/skills` directory.
It also adds configured MCPs to the resolved group
`CODEX_HOME/config.toml`, preserving unrelated TOML settings and existing MCP
entries. Both are attach-only floors: removing an entry from the Agent Deck
configuration does not delete an existing attachment or MCP entry, and
user-owned paths are never overwritten.

The resolved `config_dir` is exported as `CODEX_HOME` when the Codex session
starts. Each group owner pre-provisions that directory with the marketplaces
and native Codex plugins required for the group. Agent Deck only selects the
home and manages the explicitly configured MCP entries; it never calls
`codex plugin add`, upgrades a marketplace, or modifies a group's plugin
installation.

Extend `agent-deck group show --resolved` to report the effective Codex
configuration alongside the existing Claude result. This makes a missing
directory, parse error, or unexpected inheritance visible before a session is
started.

## Testing

Add focused tests covering:

1. TOML decoding and ancestor resolution for `[groups."work".codex]`.
2. Resolution precedence for the group Codex home, command, and environment
   file.
3. Root-first, deduplicated skill and MCP list inheritance.
4. A Codex session receiving the configured skill floor in `.agents/skills`
   and the configured MCP floor in its resolved `CODEX_HOME/config.toml`.
5. `group show --resolved` exposing the Codex configuration.

Run the focused package tests followed by the repository's standard Go test
suite.

## Success Criteria

- Given a Codex session in a configured group, when it starts, then it uses
  that group's resolved `CODEX_HOME` without modifying the home.
- Given nested group configuration, when a child omits a setting, then it
  inherits the nearest scalar setting and all ancestor skill/MCP entries.
- Given a configured skill, when a Codex session is created or restarted,
  then Agent Deck materializes it through the existing project attachment path
  without clobbering user-owned files.
- Given a configured MCP, when a Codex session is created or restarted, then
  Agent Deck adds it to the resolved group's `CODEX_HOME/config.toml` without
  removing unrelated settings or existing MCP entries.
- Given native Codex plugins already installed in a group home, when a session
  starts, then Codex sees that home; Agent Deck performs no plugin installation
  or update.
- Existing unrelated working-tree changes remain untouched and uncommitted.
