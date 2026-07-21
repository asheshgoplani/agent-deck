# Group-Scoped Codex Loadouts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add group-specific Codex homes, skills, and MCP loadouts without installing or updating native plugins.

**Architecture:** Add a Codex group-settings sibling to the Claude settings, preserving existing scalar and list-inheritance rules. A selected group home supplies pre-provisioned plugins; Agent Deck materializes skills in `.agents/skills` and appends declared MCPs to the selected home’s `config.toml`.

**Tech Stack:** Go and BurntSushi TOML.

## Global Constraints

- Work only in this isolated worktree.
- Never execute a Codex plugin installation or marketplace update at session startup.
- Preserve unrelated `CODEX_HOME/config.toml` settings and MCP entries.
- Follow red-green-refactor for each production change.

---

### Task 1: Add group Codex configuration and resolution

**Files:**

- Modify: `internal/session/userconfig.go`
- Create: `internal/session/group_codex_resolution.go`
- Create: `internal/session/groupcodex_overrides_test.go`

**Interfaces:**

- Produces `GroupCodexSettings` and `ResolveGroupCodex(groupPath string)`.
- Consumes the existing group ancestor traversal and global/profile Codex settings.

- [ ] **Step 1: Write a failing test for an inherited group Codex configuration.**

```go
func TestResolveGroupCodex_InheritsAndPrefersNearestScalar(t *testing.T) {
    // Root: command, skill and MCP. Child: config_dir, another skill and MCP.
    // Assert child config_dir, root command, and root-first deduplicated lists.
}
```

- [ ] **Step 2: Verify the test fails because the group Codex API is missing.**

Run: `go test ./internal/session -run TestResolveGroupCodex_InheritsAndPrefersNearestScalar -count=1`

- [ ] **Step 3: Add `[groups."<path>".codex]` decoding and resolution.**

```go
type GroupCodexSettings struct {
    ConfigDir string   `toml:"config_dir,omitempty"`
    EnvFile   string   `toml:"env_file,omitempty"`
    Command   string   `toml:"command,omitempty"`
    Skills    []string `toml:"skills,omitempty"`
    MCPs      []string `toml:"mcps,omitempty"`
}
```

- [ ] **Step 4: Run focused configuration tests.**

Run: `go test ./internal/session -run 'TestResolveGroupCodex|TestUserConfig_GroupCodex' -count=1`

### Task 2: Apply the group home and loadout

**Files:**

- Modify: `internal/session/loadout.go`
- Modify: the existing Codex launch resolver in `internal/session/instance.go`
- Create: `internal/session/groupcodex_loadout_test.go`
- Modify: `internal/session/codex_mcp_test.go`
- Modify: `internal/session/instance_test.go`

**Interfaces:**

- Consumes resolved Codex group configuration and lists.
- Produces a Codex launch command with the resolved `CODEX_HOME`.

- [ ] **Step 1: Write failing tests for skill/MCP materialization and group-home precedence.**

```go
func TestApplyConfiguredLoadout_CodexGroupUsesAgentsSkillsAndGroupHomeMCP(t *testing.T) {}
func TestBuildCodexCommand_UsesGroupCodexConfigDirAsCodexHome(t *testing.T) {}
```

- [ ] **Step 2: Verify they fail before implementation.**

Run: `go test ./internal/session -run 'TestApplyConfiguredLoadout_CodexGroup|TestBuildCodexCommand_UsesGroupCodex' -count=1`

- [ ] **Step 3: Apply Codex group skills/MCPs while retaining Claude-only plugin/trust behavior.**

Use the existing Codex MCP writer so the selected `CODEX_HOME/config.toml` retains unrelated keys and existing MCP entries.

- [ ] **Step 4: Run focused runtime tests.**

Run: `go test ./internal/session -run 'TestApplyConfiguredLoadout_CodexGroup|TestBuildCodexCommand_UsesGroupCodex|TestCodexMCP' -count=1`

### Task 3: Expose and document the resolved configuration

**Files:**

- Modify: `cmd/agent-deck/group_cmd.go`
- Modify: `cmd/agent-deck/group_show_test.go`
- Modify: `README.md`
- Modify: `skills/agent-deck/references/config-reference.md`
- Modify: `docs/superpowers/specs/2026-07-21-group-codex-loadout-design.md`

- [ ] **Step 1: Write a failing group-show JSON test.**

```go
func TestGroupShow_ResolvedCodexJSON(t *testing.T) {}
```

- [ ] **Step 2: Verify it fails before the CLI change.**

Run: `go test ./cmd/agent-deck -run TestGroupShow_ResolvedCodexJSON -count=1`

- [ ] **Step 3: Return the resolved Codex configuration and document the pre-provisioned plugin boundary.**

- [ ] **Step 4: Run focused and full verification.**

Run: `go test ./cmd/agent-deck -run TestGroupShow_ResolvedCodexJSON -count=1 && go test ./...`

- [ ] **Step 5: Commit only the scoped feature and documentation files.**
