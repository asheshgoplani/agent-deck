# Claude Home-Scoped Group Skills Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Materialize declarative Claude group and conductor skills once in the effective `CLAUDE_CONFIG_DIR`, preserve explicit project attachments, and safely remove verified legacy project duplicates.

**Architecture:** Extract the hardened Codex home-skill filesystem logic into tool-neutral private helpers, retain tool-specific public wrappers, and add Claude-specific group/instance resolution that enforces one skill set per physical home. Route declarative Claude skills through the new home wrapper while leaving project MCPs, plugins, and explicit `skill attach` unchanged.

**Tech Stack:** Go, BurntSushi TOML, existing `atomicfile` and config-lock helpers, table-driven `testing`, race detector, agent-deck TOML configuration.

## Global Constraints

- Declarative Claude targets are `<CLAUDE_CONFIG_DIR>/skills/<entry>`.
- Their manifest is `<CLAUDE_CONFIG_DIR>/.agent-deck/skills.toml`.
- Explicit `agent-deck skill attach` remains project-scoped.
- Claude plugins and MCPs retain their existing behavior.
- Existing homes use filesystem identity; symlink and missing case aliases cannot bypass shared-home checks.
- A home containing a `..` path component is rejected before cleaning.
- Unsafe shared-home resolution blocks launch.
- Foreign files, directories, and symlinks are never overwritten.
- No new dependencies and no `CHANGELOG.md` edits.
- Tests use an isolated home; the full gate is `HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...`.
- Preserve the existing untracked `agent-deck/.agents/` tree and unrelated `dotfiles/.config/dev-ports.json` modification.

---

### Task 1: Generalize the Hardened Home-Skill Store

**Files:**
- Create: `internal/session/agent_home_skills.go`
- Modify: `internal/session/codex_home_skills.go`
- Create: `internal/session/claude_home_skills.go`
- Create: `internal/session/claude_home_skills_test.go`
- Test: `internal/session/codex_home_skills_test.go`

**Interfaces:**
- Consumes: `ProjectSkillsManifest`, `ProjectSkillAttachment`, `ResolveSkillCandidate`, `acquireCodexConfigLock`, `atomicfile.WriteFile`.
- Produces:
  - `func AttachSkillToClaudeHome(claudeHome, skillRef, sourceName string) (*ProjectSkillAttachment, error)`
  - `func healthyManagedClaudeHomeSkillAttachment(claudeHome, skillID string) bool`
  - Existing `AttachSkillToCodexHome` and `healthyManagedCodexHomeSkillAttachment` signatures remain unchanged.
  - Private `homeSkillStore` methods own validation, manifest IO, target validation, materialization, and health checks.

- [ ] **Step 1: Write the failing Claude-home materialization tests**

Add tests that use the existing isolated skill-source helpers and assert that the new public wrapper writes only below the Claude home:

```go
func TestAttachSkillToClaudeHomeMaterializesAndRecords(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, "")
	store := t.TempDir()
	writeSkillDir(t, store, "alpha", "alpha", "test skill")
	if err := SaveSkillSources(map[string]SkillSourceDef{
		"store": {Path: store, Enabled: boolPtr(true)},
	}); err != nil {
		t.Fatal(err)
	}

	claudeHome := filepath.Join(home, ".claude-shared")
	got, err := AttachSkillToClaudeHome(claudeHome, "store/alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetPath != "skills/alpha" {
		t.Fatalf("target=%q", got.TargetPath)
	}
	if _, err := os.Stat(filepath.Join(claudeHome, "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(claudeHome, ".agent-deck", "skills.toml")); err != nil {
		t.Fatal(err)
	}
}
```

Add separate tests for:

- healing a missing manifest-managed target;
- preserving a foreign target;
- rejecting a relative home and filesystem root;
- 16 concurrent writers preserving every manifest entry under `go test -race`.

- [ ] **Step 2: Run the Claude wrapper tests and verify RED**

Run:

```sh
go test ./internal/session -run 'TestAttachSkillToClaudeHome' -count=1
```

Expected: build failure because `AttachSkillToClaudeHome` does not exist.

- [ ] **Step 3: Extract private tool-neutral storage helpers**

Move the filesystem implementation from `codex_home_skills.go` into:

```go
type homeSkillStore struct {
	home  string
	label string
}

func newHomeSkillStore(home, label string) (*homeSkillStore, error)
func (s *homeSkillStore) manifestPath() string
func (s *homeSkillStore) target(targetRel string) (string, error)
func (s *homeSkillStore) loadManifest() (*ProjectSkillsManifest, error)
func (s *homeSkillStore) saveManifest(*ProjectSkillsManifest) error
func (s *homeSkillStore) attach(skillRef, sourceName string) (*ProjectSkillAttachment, error)
func (s *homeSkillStore) healthy(skillID string) bool
```

Keep these invariants in the extracted code:

```go
const agentHomeSkillsDir = "skills"

func (s *homeSkillStore) manifestPath() string {
	return filepath.Join(s.home, projectSkillsDirName, projectSkillsManifest)
}
```

Use `s.label` only in errors, so Codex errors still say `Codex home` and Claude errors say `Claude home`. Continue locking on `s.manifestPath()` and writing mode `0o600`.

- [ ] **Step 4: Rebuild tool-specific wrappers**

Keep `codex_home_skills.go` as thin compatibility wrappers:

```go
func AttachSkillToCodexHome(home, skillRef, sourceName string) (*ProjectSkillAttachment, error) {
	store, err := newHomeSkillStore(home, "Codex")
	if err != nil {
		return nil, err
	}
	return store.attach(skillRef, sourceName)
}
```

Add the corresponding Claude wrappers in `claude_home_skills.go`, plus both health wrappers. Do not export the generic store.

- [ ] **Step 5: Run home-store tests GREEN**

Run:

```sh
go test -race ./internal/session -run 'Test(AttachSkillToCodexHome|AttachSkillToClaudeHome)' -count=1
```

Expected: PASS, including existing Codex behavior.

- [ ] **Step 6: Commit the home-store refactor**

```sh
git add internal/session/agent_home_skills.go internal/session/codex_home_skills.go internal/session/claude_home_skills.go internal/session/claude_home_skills_test.go internal/session/codex_home_skills_test.go
git commit -m "refactor: share agent home skill storage"
```

---

### Task 2: Resolve Safe Claude Home Skill Sets

**Files:**
- Create: `internal/session/group_claude_skills_resolution.go`
- Create: `internal/session/group_claude_skills_resolution_test.go`
- Modify: `internal/session/group_claude_resolution.go`
- Modify: `internal/session/groupclaude_overrides_test.go`
- Modify: `internal/session/instance.go`

**Interfaces:**
- Consumes: `GetClaudeConfigDirForGroup`, `GetClaudeConfigDirForInstance`, `GetGroupClaudeSkills`, `GetConductorClaudeSkills`, `conductorNameFromInstance`, and the existing physical-path helpers.
- Produces:
  - `func ResolveGroupClaudeHomeSkills(groupPath string) (string, []string, error)`
  - `func ResolveInstanceClaudeHomeSkills(inst *Instance) (string, []string, error)`
  - Tool-neutral private `sameAgentHomePath` and `canonicalAgentHomePath`, replacing the Codex-named equivalents without changing behavior.
  - `ResolveGroupClaude` sets `ConfigError` and omits unsafe skills.

- [ ] **Step 1: Write RED tests for shared-home safety**

Cover these real configuration cases:

```go
func TestResolveGroupClaudeHomeSkillsRejectsDivergentSharedHome(t *testing.T) {
	withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[groups.alpha.claude]
skills = ["store/alpha"]
[groups.beta.claude]
skills = ["store/beta"]
`)
	_, _, err := ResolveGroupClaudeHomeSkills("alpha")
	if err == nil || !strings.Contains(err.Error(), "beta") {
		t.Fatalf("expected beta conflict, got %v", err)
	}
}
```

Add independent tests for:

- identical group sets sharing the global home;
- a descendant adding skills while inheriting the same home;
- an isolated group `config_dir` allowing a different set;
- conductor additions requiring a distinct conductor home unless they equal the group set;
- account-selected and profile-selected homes;
- symlink aliases;
- existing case aliases;
- both case variants initially absent;
- `..` rejection;
- `ResolveGroupClaude(...).ConfigError`;
- `prepareCommand` refusing an unsafe Claude launch.

- [ ] **Step 2: Run resolver tests and verify RED**

Run:

```sh
go test ./internal/session -run 'TestResolve(Group|Instance)ClaudeHomeSkills|TestPrepareCommandRejectsUnsafeClaudeHomeSkills' -count=1
```

Expected: build failure because the new resolver functions do not exist.

- [ ] **Step 3: Rename physical-home helpers without behavior drift**

In `group_codex_resolution.go`, rename:

```go
sameCodexHomePath      -> sameAgentHomePath
canonicalCodexHomePath -> canonicalAgentHomePath
```

Keep `hasParentPathComponent` shared. Update all Codex call sites and rerun the full Codex resolver test subset before adding Claude logic:

```sh
go test -race ./internal/session -run 'TestResolve(Group|Instance)CodexHomeSkills' -count=1
```

Expected: PASS.

- [ ] **Step 4: Implement group-level Claude resolution**

`ResolveGroupClaudeHomeSkills` must:

1. load `config.toml`;
2. resolve the group home through `GetClaudeConfigDirForGroup`;
3. reject raw selected home values containing `..`;
4. obtain `GetGroupClaudeSkills(groupPath)`;
5. compare every declared group with non-empty skills whose effective home is the same physical home;
6. reject different sets and name both groups;
7. allow different sets only when their physical homes differ.

Use stable sorted group paths so diagnostics and tests are deterministic.

- [ ] **Step 5: Implement instance-level Claude resolution**

`ResolveInstanceClaudeHomeSkills` must:

```go
func ResolveInstanceClaudeHomeSkills(inst *Instance) (string, []string, error)
```

- return no work for nil or non-Claude instances;
- start with the safe group set;
- union conductor skills;
- use `GetClaudeConfigDirForInstance(inst)` as the actual target;
- reject `..`;
- reject a conductor that adds skills while sharing its group home unless the resulting set is unchanged;
- permit an isolated conductor home;
- reject an account/environment/profile override to a shared home when configured declarations prove divergent;
- return the actual home and final skill set.

- [ ] **Step 6: Surface errors in diagnostics and launch**

Change `ResolveGroupClaude` to call `ResolveGroupClaudeHomeSkills`. On error:

```go
res.ConfigError = skillsErr.Error()
res.Skills = nil
```

In `Instance.prepareCommand`, add the Claude gate beside the Codex gate:

```go
if IsClaudeCompatible(i.Tool) {
	if _, _, err := ResolveInstanceClaudeHomeSkills(i); err != nil {
		return "", "", fmt.Errorf("unsafe Claude group skill loadout: %w", err)
	}
}
```

- [ ] **Step 7: Run resolver and launch tests GREEN**

Run:

```sh
go test -race ./internal/session -run 'Test(ResolveGroupClaude|ResolveInstanceClaudeHomeSkills|PrepareCommandRejectsUnsafeClaudeHomeSkills|ResolveGroupCodexHomeSkills|ResolveInstanceCodexHomeSkills)' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit Claude resolution safety**

```sh
git add internal/session/group_codex_resolution.go internal/session/group_claude_skills_resolution.go internal/session/group_claude_skills_resolution_test.go internal/session/group_claude_resolution.go internal/session/groupclaude_overrides_test.go internal/session/instance.go
git commit -m "feat: validate Claude home skill loadouts"
```

---

### Task 3: Route Declarative Claude Skills to the Home

**Files:**
- Modify: `internal/session/loadout.go`
- Modify: `internal/session/loadout_test.go`
- Create: `internal/session/groupclaude_loadout_test.go`

**Interfaces:**
- Consumes: `ResolveInstanceClaudeHomeSkills`, `AttachSkillToClaudeHome`, `healthyManagedClaudeHomeSkillAttachment`.
- Produces: `ApplyConfiguredLoadout` materializes declarative Claude skills in the selected home and never creates project skill state; plugin and MCP branches remain project-scoped.

- [ ] **Step 1: Replace the old project-materialization assertion with RED home assertions**

Add an integration test parallel to the Codex test:

```go
func TestApplyConfiguredLoadoutClaudeGroupUsesHomeSkillsAndProjectMCP(t *testing.T) {
	home := withIsolatedHomeAndConfig(t, `
[claude]
config_dir = "~/.claude-shared"
[mcps.memory]
command = "echo"
[groups.work.claude]
skills = ["store/alpha"]
mcps = ["memory"]
`)
	setupLoadoutStore(t, home)
	project := t.TempDir()
	inst := NewInstanceWithGroupAndTool("claude-loadout", project, "work", "claude")

	if warnings := ApplyConfiguredLoadout(inst); len(warnings) != 0 {
		t.Fatalf("warnings=%v", warnings)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude-shared", "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(project, ".agents")); !os.IsNotExist(err) {
		t.Fatalf("declarative Claude skills created project state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".agent-deck", "skills.toml")); !os.IsNotExist(err) {
		t.Fatalf("declarative Claude skills created project manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".mcp.json")); err != nil {
		t.Fatalf("Claude MCP must remain project-scoped: %v", err)
	}
}
```

Keep a separate existing test that calls explicit `AttachSkillToProject` and proves it still writes project state.

- [ ] **Step 2: Run the integration test and verify RED**

Run:

```sh
go test ./internal/session -run 'TestApplyConfiguredLoadoutClaudeGroupUsesHomeSkillsAndProjectMCP' -count=1
```

Expected: FAIL because the skill is still under the project and absent from the Claude home.

- [ ] **Step 3: Split skill home resolution from project requirements**

Refactor the start of `ApplyConfiguredLoadout` so:

- SSH remains a complete no-op;
- Claude/Codex home skill reconciliation can run without a project path;
- plugin and MCP project mutations are skipped when `ProjectPath == ""`;
- the existing behavior for ordinary project-backed sessions remains unchanged.

Track `skillHome` and `skillTool` explicitly rather than overloading the Codex variable:

```go
var skillHome string
switch {
case IsClaudeCompatible(inst.Tool):
	skillHome, skills, skillResolutionErr = ResolveInstanceClaudeHomeSkills(inst)
case IsCodexCompatible(inst.Tool):
	skillHome, skills, skillResolutionErr = ResolveInstanceCodexHomeSkills(inst)
}
```

- [ ] **Step 4: Route Claude declarations through the home wrapper**

In the skill loop:

```go
switch {
case IsClaudeCompatible(inst.Tool):
	attachment, err = AttachSkillToClaudeHome(skillHome, entry, "")
case IsCodexCompatible(inst.Tool):
	attachment, err = AttachSkillToCodexHome(skillHome, entry, "")
}
```

Use the matching home health wrapper. Remove the declarative call to `AttachSkillToProject`; do not change the explicit CLI attach path.

Only run project trust seeding when the existing plugin path requires it; a home skill alone must not create or trust a project.

- [ ] **Step 5: Update existing loadout tests**

Change tests that currently assert `.claude/skills` or `.agents/skills` under the project to assert `<isolated-home>/.claude-shared/skills`. Keep tests for:

- idempotence;
- deleted-target healing;
- foreign-target preservation;
- missing skill warnings;
- plugins and MCPs;
- config cache refresh;
- SSH no-op;
- non-Claude/Codex no-op.

- [ ] **Step 6: Run loadout tests GREEN**

Run:

```sh
go test -race ./internal/session -run 'Test(ApplyConfiguredLoadout|Loadout_)' -count=1
```

Expected: PASS and no project skill artifacts from declarative Claude loadouts.

- [ ] **Step 7: Commit runtime routing**

```sh
git add internal/session/loadout.go internal/session/loadout_test.go internal/session/groupclaude_loadout_test.go
git commit -m "feat: scope Claude group skills to home"
```

---

### Task 4: Document the Claude Ownership Boundary

**Files:**
- Modify: `README.md`
- Modify: `skills/agent-deck/references/config-reference.md`
- Modify: `docs/superpowers/specs/2026-07-22-claude-home-skills-design.md` only if implementation reveals a contradiction.

**Interfaces:**
- Consumes: final resolver and runtime behavior.
- Produces: user-facing documentation that distinguishes declarative home skills from explicit project attachments.

- [ ] **Step 1: Update README examples and ownership text**

Document:

```text
Declarative Claude group/conductor skills are reconciled into
$CLAUDE_CONFIG_DIR/skills. Explicit `agent-deck skill attach` remains
project-scoped. Groups sharing one physical Claude config directory must use
the same declarative skill set or select distinct config_dir values.
```

Remove statements that say Claude declarative skills use project attachment machinery.

- [ ] **Step 2: Update the config reference**

Update the loadout and per-group Claude sections with:

- home manifest path;
- shared-home rejection;
- account/conductor/group precedence;
- explicit attachment boundary;
- `..`, symlink, and case-alias behavior;
- migration note that old project manifests are not automatically deleted.

- [ ] **Step 3: Verify documentation consistency**

Run:

```sh
rg -n 'Claude skills use the project|Claude.*project attachment|\\.agents/skills' README.md skills/agent-deck/references/config-reference.md
git diff --check
```

Expected: any remaining `.agents/skills` reference explicitly describes manual/project attachment, not declarative Claude groups.

- [ ] **Step 4: Commit docs**

```sh
git add README.md skills/agent-deck/references/config-reference.md
git commit -m "docs: explain Claude home-scoped skills"
```

---

### Task 5: Verify, Review, Install, and Migrate Live State

**Files:**
- Modify: `/Users/doozyx/DoozyX/dotfiles/.agent-deck/config.toml`
- Runtime state: `/Users/doozyx/.agent-deck/claude/skills`
- Runtime state: `/Users/doozyx/.agent-deck/claude/.agent-deck/skills.toml`
- Audited cleanup targets: existing repo-local `.agent-deck/skills.toml` and matching managed skill symlinks.

**Interfaces:**
- Consumes: compiled `agent-deck`, `group show --resolved --json`, home wrappers reached through normal lifecycle or an audited temporary reconciliation command.
- Produces: installed local binary, uniform live Claude skill declarations, verified shared home, and removal of only proven legacy duplicates.

- [ ] **Step 1: Run focused race, vet, build, and CLI gates**

```sh
go test -race ./internal/session -run 'Test(AttachSkillToClaudeHome|ResolveGroupClaude|ResolveInstanceClaudeHomeSkills|PrepareCommandRejectsUnsafeClaudeHomeSkills|ApplyConfiguredLoadoutClaude)' -count=1
go vet ./...
go build -o /tmp/agent-deck-claude-home ./cmd/agent-deck
go test ./cmd/agent-deck -run 'TestGroupShow_ResolvedJSON' -count=1
```

Expected: PASS.

- [ ] **Step 2: Run the exact sandboxed full suite**

```sh
HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...
```

Record every failure. Treat known tmux/socket/transcript failures as baseline only after confirming no failure mentions the new Claude home tests or changed code paths.

- [ ] **Step 3: Perform a fresh reviewer pass**

Review the complete diff for:

- home identity aliases;
- account/conductor/group precedence;
- concurrent first-start behavior;
- foreign target safety;
- launch gates on every start/restart path;
- project plugin/MCP regressions;
- accidental edits to the pre-existing `.agents/` tree.

Fix Important or Critical findings with a new failing test before code changes, rerun focused gates, and commit each correction.

- [ ] **Step 4: Standardize tracked Claude declarations**

In `/Users/doozyx/DoozyX/dotfiles/.agent-deck/config.toml`, make every top-level group that uses the shared global Claude home declare:

```toml
skills = ["shared/port-registry", "claude-setup/web-perf"]
```

Do not modify plugin or MCP lists. Preserve the unrelated dirty `dev-ports.json`.

- [ ] **Step 5: Install with rollback protection**

Build the final binary, retain `/Users/doozyx/.local/bin/agent-deck.pre-claude-home`, replace the installed binary with a fresh inode, and verify:

```sh
/Users/doozyx/.local/bin/agent-deck --version
agent-deck group show doozyx --resolved --json
agent-deck group show adaptam --resolved --json
agent-deck group show uniqcast --resolved --json
agent-deck group show fjordbyte --resolved --json
agent-deck group show personal --resolved --json
```

Every shared-home group must show the same two skills and no `config_error`.

- [ ] **Step 6: Provision and verify the shared Claude home**

Use the production resolver and `AttachSkillToClaudeHome` through a normal lifecycle call or a temporary in-repo reconciliation command that is deleted immediately afterward. Verify:

```sh
test -f /Users/doozyx/.agent-deck/claude/skills/port-registry/SKILL.md
test -f /Users/doozyx/.agent-deck/claude/skills/web-perf/SKILL.md
test -f /Users/doozyx/.agent-deck/claude/.agent-deck/skills.toml
```

Run reconciliation twice; the second run must produce no warning and no manifest duplication.

- [ ] **Step 7: Audit and remove only proven legacy duplicates**

For every repo manifest under `/Users/doozyx/DoozyX`, excluding `.worktrees` and `node_modules`:

1. parse each attachment;
2. require ID `shared/port-registry` or `claude-setup/web-perf`;
3. require the recorded source path to match the live home manifest source;
4. require the target to be a symlink resolving to that same source;
5. remove only that symlink and manifest entry;
6. atomically rewrite a non-empty manifest;
7. move an empty manifest to Trash;
8. remove `.agents`, `.claude`, or `.agent-deck` directories only when empty.

Print the exact removed paths and Trash destinations. Do not touch a real directory, foreign symlink, unknown attachment, repository without a manifest, or unrelated dirty file.

- [ ] **Step 8: Final readback**

Run:

```sh
git status --short
git -C /Users/doozyx/DoozyX/dotfiles status --short
find /Users/doozyx/DoozyX -path '*/.worktrees/*' -prune -o -path '*/node_modules/*' -prune -o -path '*/.agent-deck/skills.toml' -print
find /Users/doozyx/.agent-deck/claude/skills -maxdepth 1 -mindepth 1 -print
```

Report:

- implementation commit range;
- installed version/build;
- focused and full-suite results;
- reviewer verdict;
- live resolved group homes and skill sets;
- each removed legacy artifact;
- every dirty file deliberately preserved.
