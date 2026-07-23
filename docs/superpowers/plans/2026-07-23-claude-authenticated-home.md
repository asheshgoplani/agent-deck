# Authenticated Claude Home and Shared Codex Status Bar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Agent Deck Claude launches reuse the already authenticated default Claude profile and give every isolated Codex home the same status bar.

**Architecture:** Point the global Agent Deck Claude configuration at `~/.claude`, then use the existing hardened Claude-home skill materializer to install the shared skill loadout there. Add optional `[codex.tui]` defaults and merge only those managed keys into each resolved Codex home during loadout reconciliation and explicit group sync.

**Tech Stack:** Agent Deck TOML configuration, Claude Code CLI, Agent Deck CLI

## Global Constraints

- Preserve existing Claude settings, credentials, plugins, conversations, and manually managed skills.
- Remove only artifacts proven by the Agent Deck skill manifest to be managed.
- Keep all groups sharing one Claude home on an identical declarative skill set.
- Use Trash for recoverable cleanup.
- Preserve unrelated `$CODEX_HOME/config.toml` keys and treat absent Codex TUI fields as unmanaged.

---

### Task 1: Switch and provision the authenticated Claude home

**Files:**
- Modify: `/Users/doozyx/DoozyX/dotfiles/.agent-deck/config.toml`
- Create: `/Users/doozyx/.claude/.agent-deck/skills.toml`
- Create: `/Users/doozyx/.claude/skills/port-registry`
- Create: `/Users/doozyx/.claude/skills/web-perf`

**Interfaces:**
- Consumes: `[claude].config_dir` and group Claude skill declarations
- Produces: one resolved authenticated Claude home with two managed skills

- [ ] **Step 1: Change the configured home**

Change `[claude].config_dir` from `~/.agent-deck/claude` to `~/.claude`.

- [ ] **Step 2: Verify resolved configuration**

Run:

```bash
agent-deck group show doozyx --resolved --json
```

Expected: `.claude.config_dir` is `/Users/doozyx/.claude` and no
`config_error` is present.

- [ ] **Step 3: Provision both managed skills**

Invoke `ResolveGroupClaudeHomeSkills("doozyx")` and
`AttachSkillToClaudeHome` for every resolved entry using a temporary Go helper.

- [ ] **Step 4: Verify authentication**

Run:

```bash
CLAUDE_CONFIG_DIR=/Users/doozyx/.claude claude auth status
```

Expected: JSON contains `"loggedIn": true`.

### Task 2: Retire the obsolete managed home and verify launch behavior

**Files:**
- Remove to Trash: `/Users/doozyx/.agent-deck/claude/.agent-deck/skills.toml`
- Remove to Trash: `/Users/doozyx/.agent-deck/claude/skills/port-registry`
- Remove to Trash: `/Users/doozyx/.agent-deck/claude/skills/web-perf`

**Interfaces:**
- Consumes: old and new Agent Deck skill manifests
- Produces: no duplicate managed skill attachment in the obsolete profile

- [ ] **Step 1: Validate old managed artifacts**

Require both old manifest entries to match the new manifest IDs and source
paths, and require both old targets to be symlinks resolving to those sources.

- [ ] **Step 2: Move validated artifacts to Trash**

Move only the two validated links and old manifest to unique Trash names.
Preserve every other file under `~/.agent-deck/claude`.

- [ ] **Step 3: Reproduce the original launch path**

Create a disposable session:

```bash
agent-deck add -t auth-debug-claude-home -g doozyx --no-parent -c claude /Users/doozyx/DoozyX/agent-deck --json
```

Expected: the session remains live and its pane shows the normal Claude prompt,
not `Let's get started`, theme selection, or login.

- [ ] **Step 4: Remove the disposable session**

```bash
agent-deck remove auth-debug-claude-home
```

- [ ] **Step 5: Commit configuration and documentation**

Commit the dotfiles configuration separately from the Agent Deck design and
plan documents, then confirm both worktrees are clean.

### Task 3: Add declarative Codex TUI defaults

**Files:**
- Modify: `internal/session/userconfig.go`
- Create: `internal/session/codex_tui.go`
- Create: `internal/session/codex_tui_test.go`
- Modify: `internal/session/loadout.go`
- Modify: `internal/session/codex_plugin_sync.go`
- Modify: `skills/agent-deck/references/config-reference.md`
- Modify: `/Users/doozyx/DoozyX/dotfiles/.agent-deck/config.toml`

**Interfaces:**
- Consumes: `UserConfig.Codex.TUI *CodexTUISettings`
- Produces: `ApplyCodexTUISettings(codexHome string, settings *CodexTUISettings) error`

- [ ] **Step 1: Write failing configuration and merge tests**

Cover TOML decoding of `[codex.tui]`, preservation of unrelated `[tui]`,
`[mcp_servers.*]`, and `[projects.*]` keys, explicit empty `status_line`, and a
Codex loadout with no skills or MCPs.

- [ ] **Step 2: Run the focused tests and verify RED**

```bash
go test ./internal/session -run 'Test(CodexTUI|ApplyConfiguredLoadoutCodexTUI)' -count=1
```

Expected: compilation or assertion failure because the configuration type and
merge function do not exist.

- [ ] **Step 3: Implement the minimal merge**

Add the optional nested configuration type, serialize writes through the
existing Codex config lock, parse before writing, mutate only configured TUI
keys, and save atomically with mode `0600`.

- [ ] **Step 4: Wire both reconciliation paths**

Call the merge from `ApplyConfiguredLoadout` for local Codex sessions before
the empty-loadout return, and from `SyncGroupCodexPlugins` before its
plugin-empty return.

- [ ] **Step 5: Run focused and safety gates**

```bash
go test -race ./internal/session -run 'Test(CodexTUI|ApplyConfiguredLoadoutCodexTUI)' -count=1
go vet ./...
HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...
```

- [ ] **Step 6: Install and apply**

Build and install Agent Deck with a fresh inode, add the approved status line
under `[codex.tui]`, run `agent-deck group codex sync` for every configured
Codex group, and verify every resulting `[tui]` table matches the default.
