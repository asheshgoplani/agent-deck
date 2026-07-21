# Agent-deck Claude and Codex Loadouts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make agent-deck the declarative owner of matching Claude and Codex plugin, skill, and MCP loadouts, then remove the native Claude declarations.

**Architecture:** The tracked dotfiles `config.toml` defines the plugin and MCP catalog plus group-scoped Claude and Codex loadouts. Claude uses an isolated agent-deck profile; Codex keeps its existing home so the current marketplace configuration remains available for explicit group plugin synchronization.

**Tech Stack:** TOML, JSON, agent-deck CLI, Claude Code, Codex CLI.

## Global Constraints

- Keep the existing `~/.claude` authentication and conversation history.
- Do not modify the unrelated untracked PDF in `voice-chat`.
- Do not remove a project `.mcp.json` until the matching named MCP entry resolves from agent-deck.
- Keep shared plugins synchronized while retaining tool-specific integrations.

---

### Task 1: Define tracked plugin and MCP catalogs

**Files:**
- Modify: `/Users/doozyx/DoozyX/dotfiles/.agent-deck/config.toml`
- Test: `agent-deck mcp list --json` and `agent-deck plugin list --json`

**Interfaces:**
- Consumes: current enabled Claude and Codex plugin inventories plus the three project `.mcp.json` definitions.
- Produces: named `[plugins.*]` and `[mcps.*]` catalog entries that group loadouts can reference.

- [ ] **Step 1: Add the Claude plugin catalog**

Add eight catalog entries: `agent-deck`, `karpathy-guidelines`,
`frontend-design`, `superpowers`, `tam-tools`, `chrome-devtools`,
`gitkraken-hooks`, and `playwright`. Each entry uses the matching installed
plugin name and marketplace source; its description identifies whether it is
shared or Claude-only.

- [ ] **Step 2: Add the MCP catalog**

Copy the `xcode`, `better-auth`, `inngest-dev`, `next-devtools`, `shadcn`,
`supabase`, and `dart` transport definitions into matching `[mcps.<name>]`
tables. Preserve the Supabase environment-variable references literally and
do not insert a secret value.

- [ ] **Step 3: Validate catalog parsing**

Run: `agent-deck mcp list --json && agent-deck plugin list --json`

Expected: seven MCP entries and eight Claude plugin catalog entries, with no
TOML parse error.

### Task 2: Apply synchronized group loadouts

**Files:**
- Modify: `/Users/doozyx/DoozyX/dotfiles/.agent-deck/config.toml`
- Modify: `/Users/doozyx/.agent-deck/skills/sources.toml`
- Test: `agent-deck group show <group> --resolved --json`

**Interfaces:**
- Consumes: plugin and MCP catalog names from Task 1 and skills from
  `/Users/doozyx/DoozyX/claude-setup/skills`.
- Produces: inherited root-group plugin/skill loadouts plus project-group MCP
  assignments for both Claude and Codex.

- [ ] **Step 1: Register the skill source**

Run: `agent-deck skill source add claude-setup /Users/doozyx/DoozyX/claude-setup/skills`

Expected: `agent-deck skill source list --json` reports the enabled
`claude-setup` source containing `port-registry` and `web-perf`.

- [ ] **Step 2: Configure root-group shared and tool-specific plugins**

For each root group (`personal`, `doozyx`, `adaptam`, `fjordbyte`,
`uniqcast`), add a `.claude` loadout with the eight catalog names and both
skills. Add a `.codex` loadout with `config_dir = "~/.codex"`, the shared
five plugin selectors, and the Codex-only selectors `documents`, `gmail`,
`template-creator`, and `visualize`.

- [ ] **Step 3: Configure project MCP loadouts**

Add the same `.claude.mcps` and `.codex.mcps` assignments for
`doozyx/doozyx-apps` (`xcode`), `fjordbyte/fjordbyte-lti` (`better-auth`,
`inngest-dev`, `next-devtools`, `shadcn`, `supabase`), and
`doozyx/voice-chat` (`dart`).

- [ ] **Step 4: Verify resolved loadouts**

Run: `agent-deck group show doozyx/doozyx-apps --resolved --json`,
`agent-deck group show fjordbyte/fjordbyte-lti --resolved --json`, and
`agent-deck group show doozyx/voice-chat --resolved --json`.

Expected: each result includes inherited shared plugins, its tool-specific
plugins, and only the MCP names assigned to that project group.

### Task 3: Isolate Claude and remove native declarations

**Files:**
- Modify: `/Users/doozyx/DoozyX/dotfiles/.agent-deck/config.toml`
- Modify: `/Users/doozyx/.claude/settings.json`
- Delete: `/Users/doozyx/.claude/skills`
- Delete: `/Users/doozyx/.claude/plugins`
- Delete: `/Users/doozyx/DoozyX/doozyx-apps/.mcp.json`
- Delete: `/Users/doozyx/DoozyX/Fjordbyte/Fjordbyte-LTI/.mcp.json`
- Delete: `/Users/doozyx/DoozyX/voice-chat/.mcp.json`
- Test: `agent-deck hooks status`, Claude-profile checks, and project status checks

**Interfaces:**
- Consumes: validated loadouts from Tasks 1–2.
- Produces: a clean native Claude declaration surface and an isolated
  `~/.agent-deck/claude` runtime profile.

- [ ] **Step 1: Set the isolated Claude profile**

Set `[claude].config_dir = "~/.agent-deck/claude"` in the tracked config.
Create the profile directory with a credentials symlink to
`~/.claude/.credentials.json`; this preserves the existing login without
copying credentials or conversation history.

- [ ] **Step 2: Bootstrap isolated Claude marketplaces and plugins**

With `CLAUDE_CONFIG_DIR=~/.agent-deck/claude`, add the six durable sources:
the local `agent-deck`, `superpowers`, and `tam-tools` paths plus the
`ChromeDevTools/chrome-devtools-mcp`,
`anthropics/claude-plugins-official`, and
`forrestchang/andrej-karpathy-skills` GitHub sources. Copy the current
GitKraken marketplace directory into the isolated profile and register that
copy as the seventh source. Then install the eight configured Claude plugins.

Expected: `CLAUDE_CONFIG_DIR=~/.agent-deck/claude claude plugin list` reports
every configured Claude plugin as installed; no marketplace source points
under `~/.claude/plugins`.

- [ ] **Step 3: Remove only native declarations**

Delete `enabledPlugins` and `extraKnownMarketplaces` from
`~/.claude/settings.json`, remove the global skills symlink, and remove the
native Claude plugin cache. Leave every other setting, credentials, and
project history untouched.

- [ ] **Step 4: Remove superseded project MCP files**

Delete the three project `.mcp.json` files after Task 2 has verified each
matching group resolution.

- [ ] **Step 5: Synchronize Codex plugins explicitly**

Run once for each root group:

```sh
agent-deck group codex sync personal
agent-deck group codex sync doozyx
agent-deck group codex sync adaptam
agent-deck group codex sync fjordbyte
agent-deck group codex sync uniqcast
```

Expected: each command completes successfully using `~/.codex`; repeated
runs are safe.

- [ ] **Step 6: Verify end state**

Run: `agent-deck hooks status`, `agent-deck mcp list --json`,
`agent-deck plugin list --json`, `agent-deck skill source list --json`, and
the three resolved-group commands from Task 2.

Expected: agent-deck resolves every loadout, native Claude no longer has
global skills/plugins/marketplaces, and `~/.claude/.credentials.json` plus
the native `projects` history directory still exist.
