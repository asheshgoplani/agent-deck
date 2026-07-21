# Agent-deck Claude and Codex Loadouts Design

## Goal

Make agent-deck the sole declarative owner of the user's Claude and Codex
skills, plugins, and MCP loadouts, while leaving the native Claude profile's
authentication and conversation history intact and preserving the existing
Codex configuration as the source of its marketplace setup.

## Architecture

Agent-deck will launch Claude with an isolated configuration directory at
`~/.agent-deck/claude`. Codex sessions will continue to use the existing
`~/.codex` home, which already contains the user's marketplace setup and
plugin configuration. The tracked source of truth remains
`/Users/doozyx/DoozyX/dotfiles/.agent-deck/config.toml`, which is already the
target of both active agent-deck configuration symlinks.

The configuration will define a shared plugin baseline for Claude and Codex,
then keep tool-specific integrations in their respective loadouts. A skill
source will point at the existing `claude-setup/skills` directory; the two
skills will be declared on the same root groups so agent-deck materializes
them only for managed projects. Project MCP definitions will become named MCP
catalog entries and be declared on the matching Claude and Codex groups.

Before native cleanup, the isolated Claude profile will register each required
marketplace from its durable local or GitHub source. The one GitKraken
marketplace whose only current source is inside the native cache will be copied
into the isolated profile first, so no declarative loadout depends on a native
Claude path after cleanup.

## Migration Boundaries

- Preserve `~/.claude` authentication and conversation history.
- Remove native global skill discovery by removing the `~/.claude/skills`
  symlink.
- Remove native plugin enablement, marketplace declarations, and cached plugin
  payloads from `~/.claude` after agent-deck can install and enable the catalog
  entries in its isolated profile.
- Preserve the native Codex home and its marketplace setup. Agent-deck will
  explicitly synchronize the configured Codex plugins into that home rather
  than reinstalling or replacing its marketplaces on session startup.
- Remove the three checked-in project `.mcp.json` files only after their
  definitions are present in the agent-deck catalog and assigned to matching
  Claude and Codex groups.
- Do not modify the unrelated untracked PDF in the `voice-chat` repository.

## Loadout Mapping

Every root group (`personal`, `doozyx`, `adaptam`, `fjordbyte`, and
`uniqcast`) receives this shared declarative plugin baseline:

- `agent-deck@agent-deck`
- `andrej-karpathy-skills@karpathy-skills`
- `frontend-design@claude-plugins-official`
- `superpowers@superpowers-dev`
- `tam-tools@tam-tools`

Claude additionally receives its active tool-specific integrations:

- `chrome-devtools-mcp@chrome-devtools-plugins`
- `gitkraken-hooks@gitkraken`
- `playwright@claude-plugins-official`

Codex additionally receives its active tool-specific plugins:

- `documents@openai-primary-runtime`
- `gmail@openai-curated`
- `template-creator@openai-primary-runtime`
- `visualize@openai-bundled`

The same groups receive `claude-setup/port-registry` and
`claude-setup/web-perf` from the agent-deck skill registry.

The MCP scope follows the existing project declarations:

- `doozyx/doozyx-apps`: `xcode`
- `fjordbyte/fjordbyte-lti`: `better-auth`, `inngest-dev`, `next-devtools`,
  `shadcn`, and `supabase`
- `doozyx/voice-chat`: `dart`

## Failure Handling and Verification

The isolated profile must be authenticated before its first managed session.
Catalog validation must parse the active configuration, list the Claude plugin
catalog and all seven MCPs, resolve each affected Claude and Codex group, and
materialize a test session's loadout. Codex plugin synchronization is an
explicit final command for each root group. Native Claude cleanup is last and
is verified by inspecting the removed declarations and confirming the native
profile still retains its credentials and project history directories.
