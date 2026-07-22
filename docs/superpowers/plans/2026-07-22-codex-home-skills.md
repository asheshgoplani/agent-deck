# Codex Home-Scoped Group Skills Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Materialize declarative Codex group skills once in the resolved `CODEX_HOME` while preserving explicit project attachments.

**Architecture:** Add a focused home-skill reconciler that reuses skill-catalog candidate resolution and materialization but owns a manifest below `CODEX_HOME`. Resolve a safe home-level skill set before loadout application, rejecting descendant or sibling configurations that would leak different skills through one home.

**Tech Stack:** Go, BurntSushi TOML, existing agent-deck skill catalog and group-resolution packages.

## Global Constraints

- Declarative Codex group skills target `<CODEX_HOME>/skills`; explicit `skill attach` remains project-scoped.
- Existing repo-local attachments are not deleted automatically because their origin is ambiguous.
- Foreign targets are never overwritten or removed.
- Child-only skills require a distinct resolved `config_dir`.
- No new dependencies and no `CHANGELOG.md` edits.

---

### Task 1: Home skill materialization

**Files:**
- Create: `internal/session/codex_home_skills.go`
- Test: `internal/session/codex_home_skills_test.go`

**Interfaces:**
- Consumes: `ResolveSkillCandidate`, `validateAttachableSkillCandidate`, `materializeSkill`, `ProjectSkillAttachment`, `ProjectSkillsManifest`.
- Produces: `AttachSkillToCodexHome(codexHome, skillRef, sourceName string) (*ProjectSkillAttachment, error)` and `healthyManagedCodexHomeSkillAttachment(codexHome, skillID string) bool`.

- [ ] **Step 1: Write failing tests for initial attach, healing, and foreign-target refusal**

  Create real temporary skill stores and assert the attachment lands at
  `<home>/skills/<name>`, the manifest lands at
  `<home>/.agent-deck/skills.toml`, a deleted managed link is recreated, and a
  pre-existing foreign directory is preserved.

- [ ] **Step 2: Run the focused tests and verify RED**

  Run: `go test ./internal/session -run 'TestAttachSkillToCodexHome' -count=1`

  Expected: build failure because `AttachSkillToCodexHome` does not exist.

- [ ] **Step 3: Implement the minimal home reconciler**

  Resolve the candidate through the existing registry, validate an absolute
  non-root home, constrain targets below `<home>/skills`, persist a TOML
  manifest atomically, and reuse symlink/copy materialization.

- [ ] **Step 4: Run focused tests and verify GREEN**

  Run: `go test ./internal/session -run 'TestAttachSkillToCodexHome' -count=1`

  Expected: PASS.

### Task 2: Shared-home resolution guard

**Files:**
- Modify: `internal/session/group_codex_resolution.go`
- Test: `internal/session/groupcodex_overrides_test.go`

**Interfaces:**
- Consumes: `findGroupCodexSetting`, `GetGroupCodexSkills`, `getParentPath`.
- Produces: `ResolveGroupCodexHomeSkills(groupPath string) (string, []string, error)`.

- [ ] **Step 1: Write failing tests for inherited and divergent homes**

  Assert an ordinary child inherits the home owner's skills, a child declaring
  extra skills without a new home errors, and two explicit groups sharing one
  path with different effective skill sets error.

- [ ] **Step 2: Run focused tests and verify RED**

  Run: `go test ./internal/session -run 'TestResolveGroupCodexHomeSkills' -count=1`

  Expected: build failure because the resolver does not exist.

- [ ] **Step 3: Implement minimal validation**

  Resolve the home owner, reject skill declarations below that owner, compare
  effective sets for every explicit group resolving to the same cleaned home,
  and return actionable errors naming the conflicting groups.

- [ ] **Step 4: Run focused tests and verify GREEN**

  Run: `go test ./internal/session -run 'TestResolveGroupCodexHomeSkills' -count=1`

  Expected: PASS.

### Task 3: Loadout integration

**Files:**
- Modify: `internal/session/loadout.go`
- Modify: `internal/session/groupcodex_loadout_test.go`

**Interfaces:**
- Consumes: `ResolveGroupCodexHomeSkills`, `AttachSkillToCodexHome`, and `healthyManagedCodexHomeSkillAttachment`.
- Produces: Codex loadout behavior that leaves project skill roots untouched.

- [ ] **Step 1: Change the existing integration test first**

  Assert `ApplyConfiguredLoadout` creates
  `<home>/skills/alpha/SKILL.md`, writes the MCP to the same home's
  `config.toml`, and does not create project `.agents` or `.agent-deck` paths.

- [ ] **Step 2: Run the integration test and verify RED**

  Run: `go test ./internal/session -run 'TestApplyConfiguredLoadout_CodexGroup' -count=1`

  Expected: FAIL because current production code writes `.agents/skills`.

- [ ] **Step 3: Route only declarative Codex skills through the home reconciler**

  Keep the Claude branch and explicit project APIs unchanged. Convert resolver
  failures into existing sanitized loadout warnings and skip unsafe skills.

- [ ] **Step 4: Run loadout and project-attachment regression tests**

  Run: `go test ./internal/session -run 'TestApplyConfiguredLoadout_CodexGroup|TestAttachSkillToProject' -count=1`

  Expected: PASS.

### Task 4: Documentation and verification

**Files:**
- Modify: `README.md`
- Modify: `skills/agent-deck/references/config-reference.md`

**Interfaces:**
- Consumes: implemented runtime behavior.
- Produces: operator-facing scope and migration guidance.

- [ ] **Step 1: Document home scope and the child-home constraint**

  State that group skills live in `<CODEX_HOME>/skills`, explicit attachments
  still use project `.agents/skills`, and old project links require explicit
  cleanup.

- [ ] **Step 2: Format and run focused verification**

  Run: `gofmt -w internal/session/codex_home_skills.go internal/session/codex_home_skills_test.go internal/session/group_codex_resolution.go internal/session/groupcodex_overrides_test.go internal/session/loadout.go internal/session/groupcodex_loadout_test.go`

  Run: `go test ./internal/session -run 'TestAttachSkillToCodexHome|TestResolveGroupCodexHomeSkills|TestApplyConfiguredLoadout_CodexGroup|TestAttachSkillToProject' -count=1`

  Expected: PASS.

- [ ] **Step 3: Run repository checks in the required sandbox**

  Run: `go vet ./...`

  Run: `HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...`

  Expected: no new failures; compare any ambient tmux/socket failures against
  the recorded pre-change baseline.
