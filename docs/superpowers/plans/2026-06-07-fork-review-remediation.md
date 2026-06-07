# Fork Review Remediation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the validated fork-review findings from `feat/v1.9.x-fork-defaults-and-config` without weakening the intended cross-tool fork parity or the `[fork].branch_prefix` feature.

**Architecture:** Keep TUI quick fork and Shift+F comprehensive by default, with `[fork]` resolving branch/worktree/state/Docker defaults. Treat Web/API fork as plain tool-native cross-tool fork parity for this remediation; Web comprehensive fork defaults require a separate async workflow design. Deduplicate cross-tool fork dispatch so TUI, CLI, and Web route Claude/OpenCode/Pi/Codex through the same session-layer helper.

**Tech Stack:** Go, Bubble Tea TUI, Agent Deck session model, WebUI Preact/htm client, Go tests, eval smoke tests.

---

## Validation Summary

All five prior findings are still valid on this branch.

| Finding | Severity | Validated Evidence | Effect |
|---|---:|---|---|
| Web fork dispatcher is stale | High | `internal/ui/web_mutator.go:243-251` only switches OpenCode/Pi/default Claude; `internal/web/handlers_sessions.go:203-204` calls that mutator | Web/API Codex-compatible fork routes to Claude fork builder and fails or starts wrong command |
| Web UI only shows fork for Claude | Medium | `internal/web/static/app/AppShell.js:84` and `Sidebar.js:113` gate on `tool === "claude"` | Even after backend parity, users cannot discover OpenCode/Pi/Codex fork in WebUI unless the UI is updated |
| Shift+F ignores `[fork].branch_prefix` | Medium | `internal/session/userconfig.go:1922-1955` defines `branch_prefix`; `internal/ui/home.go:9141` uses it for quick fork; `internal/ui/forkdialog.go:226` hardcodes `fork/` | `f` and `Shift+F` disagree, and configured branch prefixes are not honored in the dialog |
| User-set session IDs feed unquoted shell | High | `internal/session/mutators.go:255-263` accepts raw values; OpenCode shell script interpolates ID/path at `instance.go:6326-6353`; Codex command interpolates ID at `instance.go:6513` | Invalid or malicious session IDs and unusual paths can break generated commands or execute unintended shell fragments |
| Real-binary evals can pass after fork command failure | High | `tests/eval/session/fork_*_test.go` uses `runBinTry`; `assertForkWorktreeBranch` at `fork_pi_test.go:95-104` ignores `forkErr` when the worktree exists | Tests can prove worktree creation while missing failed `codex fork`, `opencode export/import`, or `pi --fork` failures |
| Docs/help stale | Low | `cmd/agent-deck/session_cmd.go:100,617,639`; `README.md:103,783`; `tests/web/PARITY_MATRIX.md:28-29` overstates Web fork/dialog parity | Users and future implementers get the wrong capability and Web-scope model |

Verification already run during validation:

```bash
git diff --check main...HEAD
GOCACHE=/private/tmp/agent-deck-validate-go-cache GOTOOLCHAIN=go1.25.11 go test ./internal/session ./internal/ui ./cmd/agent-deck -run 'Fork|ForkSettings|SetField_OpenCode|SetField_Codex|Codex|OpenCode|SettingsPanel_Fork|ForkDialog' -count=1
```

Expected current result before implementing this plan: both commands pass, but they do not cover the validated gaps above.

## Web Fork Scope Decision

For this remediation, Web/API fork means **plain cross-tool native fork**:

- It creates a forked Agent Deck instance with tool-native conversation context.
- It does not apply `[fork]` worktree/state/Docker defaults.
- It does not expose Shift+F custom title/group/branch controls.
- It should support all tools that backend `CanFork()` supports: Claude-compatible, OpenCode, Pi, and Codex-compatible.
- It should expose a WebUI fork affordance only when the backend says the session can fork.

Implication: fixing Web parity is mostly backend dispatch plus a `canFork` UI flag. Web comprehensive fork defaults should remain a future design because it needs async worktree creation, state materialization, rollback, branch conflict handling, and degradation notices.

---

### Task 1: Document Web Scope and Validated Severity

**Severity:** Medium. This prevents scope drift and avoids silently promising comprehensive Web fork behavior.

**Files:**
- Modify: `docs/superpowers/specs/2026-06-06-comprehensive-quick-fork-design.md`
- Modify: `docs/superpowers/plans/2026-06-06-fork-defaults-and-config.md`
- Modify: `tests/web/PARITY_MATRIX.md`

- [ ] **Step 1: Update the spec with explicit Web scope**

Add this subsection under Decision 10 in `docs/superpowers/specs/2026-06-06-comprehensive-quick-fork-design.md`:

```markdown
### Web/API fork scope

The Web/API `POST /api/sessions/{id}/fork` endpoint is plain cross-tool
native fork parity in this iteration. It must route Claude-compatible,
OpenCode, Pi, and Codex-compatible sessions through the same tool-specific
fork builders as TUI/CLI, but it does not apply `[fork]` worktree/state/Docker
defaults and does not expose Shift+F title/group/branch controls.

Comprehensive Web fork defaults require an async workflow with branch conflict
handling, worktree/state materialization, rollback, and user-visible
degradation notices. That is intentionally deferred.
```

- [ ] **Step 2: Update the implementation plan scope**

Add this paragraph near the cross-tool parity section in `docs/superpowers/plans/2026-06-06-fork-defaults-and-config.md`:

```markdown
**Web scope clarification:** Web/API fork is plain cross-tool native fork parity
for this branch. It should share the session-layer tool dispatch and WebUI
should render a fork action only for sessions the backend marks forkable.
Worktree/state/Docker defaults remain TUI quick/dialog scope and are not added
to Web in this remediation.
```

- [ ] **Step 3: Correct the Web parity matrix**

Change `tests/web/PARITY_MATRIX.md` rows 28-29 to:

```markdown
| Fork session | `internal/ui/home.go` (`f` key, quick) | POST `/api/sessions/{id}/fork` | `ForkSession` | `handlers_sessions_test.go`, WebUI action tests | Web creates a plain tool-native fork; TUI quick fork also applies `[fork]` defaults |
| Fork with dialog | `internal/ui/home.go` (`F`/`shift+f`) | Not equivalent | N/A | N/A | Shift+F title/group/branch/worktree controls are TUI-only until Web gets a dedicated async fork workflow |
```

- [ ] **Step 4: Verify**

Run:

```bash
rg -n "Web/API fork scope|plain cross-tool native fork|Not equivalent" docs/superpowers tests/web/PARITY_MATRIX.md
```

Expected: all three newly documented scope statements appear.

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/specs/2026-06-06-comprehensive-quick-fork-design.md docs/superpowers/plans/2026-06-06-fork-defaults-and-config.md tests/web/PARITY_MATRIX.md
git commit -m "docs: clarify web fork parity scope"
```

---

### Task 2: Add a Shared Cross-Tool Fork Dispatcher

**Severity:** High. This removes the root cause of TUI/CLI/Web switch drift.

**Files:**
- Modify: `internal/session/instance.go`
- Create: `internal/session/instance_fork_dispatch_test.go`
- Modify: `internal/ui/home.go`
- Modify: `cmd/agent-deck/session_cmd.go`
- Modify: `internal/ui/web_mutator.go`
- Create: `internal/ui/web_mutator_fork_test.go`

- [ ] **Step 1: Write session-layer dispatcher tests**

Create `internal/session/instance_fork_dispatch_test.go`:

```go
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateForkedInstanceForTool_OpenCodeUsesWorktreeDir(t *testing.T) {
	parent := NewInstanceWithTool("oc parent", "/tmp/original", "opencode")
	parent.OpenCodeSessionID = "ses_parent"
	parent.OpenCodeDetectedAt = time.Now()

	opts := &ClaudeOptions{
		WorkDir:          "/tmp/original-wt",
		WorktreePath:     "/tmp/original-wt",
		WorktreeRepoRoot: "/tmp/original",
		WorktreeBranch:   "fork/oc-parent",
	}

	forked, _, err := parent.CreateForkedInstanceForTool("oc fork", "", opts)
	if err != nil {
		t.Fatalf("CreateForkedInstanceForTool: %v", err)
	}
	if forked.Tool != "opencode" {
		t.Fatalf("forked tool = %q, want opencode", forked.Tool)
	}
	if forked.ProjectPath != "/tmp/original-wt" {
		t.Fatalf("ProjectPath = %q, want worktree dir", forked.ProjectPath)
	}
	if forked.WorktreePath != "/tmp/original-wt" || forked.WorktreeRepoRoot != "/tmp/original" || forked.WorktreeBranch != "fork/oc-parent" {
		t.Fatalf("worktree metadata not copied: %+v", forked)
	}
}

func TestCreateForkedInstanceForTool_CodexCompatibleUsesCodexFork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sid := "11111111-2222-3333-4444-555555555555"
	dir := filepath.Join(home, "sessions", "2026", "06", "07")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-20260607T000000-"+sid+".jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	parent := NewInstanceWithTool("cx parent", "/tmp/original", "codex")
	parent.CodexSessionID = sid
	parent.CodexDetectedAt = time.Now()

	forked, cmd, err := parent.CreateForkedInstanceForTool("cx fork", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedInstanceForTool: %v", err)
	}
	if forked.Tool != "codex" {
		t.Fatalf("forked tool = %q, want codex", forked.Tool)
	}
	if !forked.IsForkAwaitingStart || forked.ForkStartCommand == "" {
		t.Fatal("codex fork must use deferred ForkStartCommand")
	}
	if !strings.Contains(cmd, " fork "+sid) {
		t.Fatalf("codex fork command missing parent sid: %s", cmd)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test ./internal/session -run 'TestCreateForkedInstanceForTool' -count=1
```

Expected: FAIL because `CreateForkedInstanceForTool` does not exist.

- [ ] **Step 3: Implement `CreateForkedInstanceForTool`**

Add this method near the existing fork builders in `internal/session/instance.go`:

```go
// CreateForkedInstanceForTool creates a forked instance using the correct
// tool-specific fork implementation. opts is the shared fork carrier for
// worktree fields; non-Claude tool options continue to come from global config.
func (i *Instance) CreateForkedInstanceForTool(newTitle, newGroupPath string, opts *ClaudeOptions) (*Instance, string, error) {
	switch {
	case i.Tool == "opencode":
		workDir := i.ProjectPath
		repoRoot := ""
		branch := ""
		if opts != nil && opts.WorkDir != "" {
			workDir = opts.WorkDir
			repoRoot = opts.WorktreeRepoRoot
			branch = opts.WorktreeBranch
		}
		return i.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(newTitle, newGroupPath, nil, workDir, repoRoot, branch)
	case i.Tool == "pi":
		return i.CreateForkedPiInstanceWithOptions(newTitle, newGroupPath, opts)
	case IsCodexCompatible(i.Tool):
		return i.CreateForkedCodexInstanceWithOptions(newTitle, newGroupPath, opts)
	default:
		return i.CreateForkedInstanceWithOptions(newTitle, newGroupPath, opts)
	}
}
```

- [ ] **Step 4: Route TUI and CLI through the helper**

In `internal/ui/home.go`, replace the body of `defaultForkInstanceDeps().createInstance` with:

```go
createInstance: func(source *session.Instance, title, groupPath string, opts *session.ClaudeOptions) (*session.Instance, error) {
	inst, _, err := source.CreateForkedInstanceForTool(title, groupPath, opts)
	return inst, err
},
```

In `cmd/agent-deck/session_cmd.go`, replace the tool-specific creation switch with:

```go
forkedInst, _, err = inst.CreateForkedInstanceForTool(forkTitle, forkGroup, opts)
```

Keep the existing forkability gate before creation and the existing `worktreeType` assignment after creation.

- [ ] **Step 5: Add a Web mutator structural guard**

Create `internal/ui/web_mutator_fork_test.go`:

```go
package ui

import (
	"os"
	"strings"
	"testing"
)

func TestWebMutatorForkSessionUsesSharedToolDispatcher(t *testing.T) {
	src, err := os.ReadFile("web_mutator.go")
	if err != nil {
		t.Fatalf("read web_mutator.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "CreateForkedInstanceForTool") {
		t.Fatal("WebMutator.ForkSession must use the shared cross-tool fork dispatcher")
	}
	if strings.Contains(body, "switch parent.Tool") {
		t.Fatal("WebMutator.ForkSession must not maintain a separate stale tool switch")
	}
}
```

- [ ] **Step 6: Route Web mutator through the helper**

In `internal/ui/web_mutator.go`, replace the `switch parent.Tool` block with:

```go
forked, _, err := parent.CreateForkedInstanceForTool(parent.Title+" (fork)", parent.GroupPath, nil)
```

- [ ] **Step 7: Verify**

Run:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test ./internal/session ./internal/ui ./cmd/agent-deck -run 'TestCreateForkedInstanceForTool|TestWebMutatorForkSessionUsesSharedToolDispatcher|TestSessionFork_AdmitsOpenCode|TestCodexFork' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/session/instance.go internal/session/instance_fork_dispatch_test.go internal/ui/home.go internal/ui/web_mutator.go internal/ui/web_mutator_fork_test.go cmd/agent-deck/session_cmd.go
git commit -m "fix(fork): share cross-tool fork dispatch"
```

---

### Task 3: Expose Web Forkability and Update WebUI Affordance

**Severity:** Medium. Backend parity is incomplete if users cannot discover the action, and showing the action for non-forkable sessions creates bad UX.

**Files:**
- Modify: `internal/web/session_data_service.go`
- Modify: `internal/web/menu_session_fields_test.go`
- Modify: `internal/web/static/app/AppShell.js`
- Modify: `internal/web/static/app/Sidebar.js`
- Modify: `tests/web/e2e/parity-actions.spec.js`

- [ ] **Step 1: Add a failing MenuSession field test**

In `internal/web/menu_session_fields_test.go`, extend `TestToMenuSessionMapsInstanceFields` with:

```go
if !ms.CanFork {
	t.Fatal("MenuSession.CanFork should mirror Instance.CanFork for forkable sessions")
}
```

In the fixture for that test, make the instance forkable. The fixture already sets
`inst.ClaudeSessionID = "claude-1"` and an existing assertion checks
`ms.ClaudeSessionID == "claude-1"`, so do **NOT** overwrite the session ID —
Claude's `CanFork()` only additionally needs a recent `ClaudeDetectedAt`. Add just:

```go
inst.ClaudeDetectedAt = time.Now()
```

(ensure `time` is imported in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test ./internal/web -run 'TestToMenuSessionMapsInstanceFields' -count=1
```

Expected: FAIL because `MenuSession.CanFork` does not exist.

- [ ] **Step 3: Add `CanFork` to `MenuSession`**

In `internal/web/session_data_service.go`, add the field:

```go
CanFork bool `json:"canFork"`
```

In `toMenuSession`, set:

```go
CanFork: inst.CanFork(),
```

- [ ] **Step 4: Update WebUI fork buttons**

In `internal/web/static/app/AppShell.js`, replace:

```js
${session.tool === 'claude' && html`<button class="btn" onClick=${() => action('fork')}><${Icon} d=${ICONS.fork} size=${12}/>Fork</button>`}
```

with:

```js
${session.canFork && html`<button class="btn" onClick=${() => action('fork')}><${Icon} d=${ICONS.fork} size=${12}/>Fork</button>`}
```

In `internal/web/static/app/Sidebar.js`, replace:

```js
${s.tool === 'claude' && html`<button class="mini fork" title="Fork" onClick=${() => doAction('fork', s)}><${Icon} d=${ICONS.fork} size=${12}/></button>`}
```

with:

```js
${s.canFork && html`<button class="mini fork" title="Fork" onClick=${() => doAction('fork', s)}><${Icon} d=${ICONS.fork} size=${12}/></button>`}
```

- [ ] **Step 5: Add WebUI behavior coverage**

In `tests/web/e2e/parity-actions.spec.js`, add or update a test fixture assertion so a forkable non-Claude session with `canFork: true` renders the fork action and a non-forkable session with `canFork: false` does not.

Use this assertion pattern in the relevant e2e test:

```js
await expect(page.locator('[title="Fork"]').first()).toBeVisible()
```

and for non-forkable:

```js
await expect(page.locator('[title="Fork"]')).toHaveCount(0)
```

- [ ] **Step 6: Verify**

Run:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test ./internal/web -run 'TestToMenuSessionMapsInstanceFields|TestMenuSession' -count=1
```

Expected: PASS.

If web dependencies are installed locally, also run:

```bash
npm test -- --run tests/web
```

Expected: PASS or existing unrelated environment failure documented with exact output.

- [ ] **Step 7: Commit**

```bash
git add internal/web/session_data_service.go internal/web/menu_session_fields_test.go internal/web/static/app/AppShell.js internal/web/static/app/Sidebar.js tests/web/e2e/parity-actions.spec.js
git commit -m "fix(web): show fork action from backend forkability"
```

---

### Task 4: Make Shift+F Honor `[fork].branch_prefix`

**Severity:** Medium. This is direct user-visible inconsistency between quick fork and the dialog.

**Files:**
- Modify: `internal/ui/forkdialog.go`
- Modify: `internal/ui/forkdialog_fork_defaults_test.go`

- [ ] **Step 1: Write the failing dialog prefix test**

Append to `internal/ui/forkdialog_fork_defaults_test.go`:

```go
func TestForkDialog_Show_UsesForkBranchPrefix(t *testing.T) {
	repo := forkDefaultsGitRepo(t)
	cfg := &session.UserConfig{Fork: session.ForkSettings{BranchPrefix: "wip/"}}
	if err := session.SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	session.ClearUserConfigCache()

	d := NewForkDialog()
	d.ShowWithParentSandboxed("Fix Bug", repo, "grp", nil, "", false)
	_, _, branch, _ := d.GetValuesWithWorktree()
	if branch != "wip/fix-bug" {
		t.Fatalf("branch = %q, want wip/fix-bug", branch)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test ./internal/ui -run 'TestForkDialog_Show_UsesForkBranchPrefix' -count=1
```

Expected: FAIL with branch `fork/fix-bug`.

- [ ] **Step 3: Implement prefix-aware dialog seeding**

In `internal/ui/forkdialog.go`, compute the prefix before setting `branchInput`:

```go
forkSettings := session.ForkSettings{}
var loadedConfig *session.UserConfig
if config, err := session.LoadUserConfig(); err == nil {
	loadedConfig = config
	forkSettings = config.Fork
}

slug := git.SanitizeBranchName(strings.ToLower(strings.TrimSpace(originalName)))
if slug == "" {
	slug = "fork"
}
d.branchInput.SetValue(forkSettings.GetBranchPrefix() + slug)

if loadedConfig != nil {
	d.optionsPanel.SetDefaults(loadedConfig)
	plan := loadedConfig.Fork.Resolve(parentSandboxed)
	d.worktreeEnabled = d.worktreeCapable && plan.Worktree
	d.withStateEnabled = d.worktreeEnabled && plan.WithState
	d.withStateAndGitignored = d.withStateEnabled && plan.WithIgnored
	d.sandboxEnabled = plan.Sandbox
}
```

Remove the old hardcoded line:

```go
d.branchInput.SetValue("fork/" + git.SanitizeBranchName(strings.ToLower(originalName)))
```

- [ ] **Step 4: Verify**

Run:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test ./internal/ui -run 'TestForkDialog_Show_UsesForkBranchPrefix|TestQuickForkInputs_BranchPrefixOverride|TestForkDialog_Show_SeedsComprehensiveWithStateDefault' -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/forkdialog.go internal/ui/forkdialog_fork_defaults_test.go
git commit -m "fix(ui): honor fork branch prefix in dialog"
```

---

### Task 5: Validate Session IDs and Quote Generated Fork Commands

**Severity:** High. This is command safety and robustness for user-editable fields.

**Files:**
- Modify: `internal/session/mutators.go`
- Modify: `internal/session/mutators_test.go`
- Modify: `internal/session/instance.go`
- Modify: `internal/session/instance_codex_fork_test.go`
- Modify: `internal/session/opencode_test.go` or create `internal/session/instance_opencode_fork_test.go`

- [ ] **Step 1: Add mutator validation tests**

Append to `internal/session/mutators_test.go`:

```go
func TestSetField_OpenCodeSessionID_RejectsShellMeta(t *testing.T) {
	inst := NewInstanceWithTool("oc", "/tmp/p", "opencode")
	if _, _, err := SetField(inst, FieldOpenCodeSessionID, "ses_bad;touch /tmp/pwned", nil); err == nil {
		t.Fatal("expected invalid opencode session id error")
	}
	if inst.OpenCodeSessionID != "" {
		t.Fatalf("OpenCodeSessionID mutated on invalid input: %q", inst.OpenCodeSessionID)
	}
}

func TestSetField_CodexSessionID_RejectsNonUUID(t *testing.T) {
	inst := NewInstanceWithTool("cx", "/tmp/p", "codex")
	if _, _, err := SetField(inst, FieldCodexSessionID, "not-a-uuid;touch /tmp/pwned", nil); err == nil {
		t.Fatal("expected invalid codex session id error")
	}
	if inst.CodexSessionID != "" {
		t.Fatalf("CodexSessionID mutated on invalid input: %q", inst.CodexSessionID)
	}
}

func TestSetField_SessionID_ClearStillAllowed(t *testing.T) {
	oc := NewInstanceWithTool("oc", "/tmp/p", "opencode")
	oc.OpenCodeSessionID = "ses_existing"
	if _, _, err := SetField(oc, FieldOpenCodeSessionID, "", nil); err != nil {
		t.Fatalf("clear opencode session id: %v", err)
	}
	if oc.OpenCodeSessionID != "" {
		t.Fatalf("OpenCodeSessionID = %q, want empty", oc.OpenCodeSessionID)
	}

	cx := NewInstanceWithTool("cx", "/tmp/p", "codex")
	cx.CodexSessionID = "11111111-2222-3333-4444-555555555555"
	if _, _, err := SetField(cx, FieldCodexSessionID, "", nil); err != nil {
		t.Fatalf("clear codex session id: %v", err)
	}
	if cx.CodexSessionID != "" {
		t.Fatalf("CodexSessionID = %q, want empty", cx.CodexSessionID)
	}
}
```

- [ ] **Step 2: Run validation tests to verify failure**

Run:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test ./internal/session -run 'TestSetField_(OpenCodeSessionID_RejectsShellMeta|CodexSessionID_RejectsNonUUID|SessionID_ClearStillAllowed)' -count=1
```

Expected: first two tests FAIL because invalid values are accepted.

- [ ] **Step 3: Add validation helpers**

In `internal/session/mutators.go`, add imports and helpers:

```go
var (
	openCodeSessionIDRE = regexp.MustCompile(`^ses_[A-Za-z0-9_-]+$`)
	codexSessionIDRE    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

func validateToolSessionID(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	switch field {
	case FieldOpenCodeSessionID:
		if !openCodeSessionIDRE.MatchString(value) {
			return fmt.Errorf("opencode-session-id must match %s", openCodeSessionIDRE.String())
		}
	case FieldCodexSessionID:
		if !codexSessionIDRE.MatchString(value) {
			return fmt.Errorf("codex-session-id must be a UUID")
		}
	}
	return nil
}
```

Add `regexp` to the import list.

Then update `SetField` cases:

```go
case FieldOpenCodeSessionID:
	if err := validateToolSessionID(field, value); err != nil {
		return "", nil, &MutationError{Field: field, Msg: err.Error()}
	}
	oldValue = inst.OpenCodeSessionID
	inst.OpenCodeSessionID = strings.TrimSpace(value)
	inst.OpenCodeDetectedAt = time.Now()

case FieldCodexSessionID:
	if err := validateToolSessionID(field, value); err != nil {
		return "", nil, &MutationError{Field: field, Msg: err.Error()}
	}
	oldValue = inst.CodexSessionID
	inst.CodexSessionID = strings.TrimSpace(value)
	inst.CodexDetectedAt = time.Now()
```

- [ ] **Step 4: Add OpenCode quoting test**

Create `internal/session/instance_opencode_fork_test.go`:

```go
package session

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeForkScriptQuotesWorkDir(t *testing.T) {
	parent := NewInstanceWithTool("oc", `/tmp/project with "quote"`, "opencode")
	parent.OpenCodeSessionID = "ses_parent_123"
	parent.OpenCodeDetectedAt = time.Now()

	cmd, err := parent.ForkOpenCodeWithOptions("oc fork", "", nil)
	if err != nil {
		t.Fatalf("ForkOpenCodeWithOptions: %v", err)
	}
	scriptPath := strings.TrimPrefix(strings.TrimSuffix(cmd, "'"), "bash '")
	body, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(scriptPath) })

	if strings.Contains(string(body), `cd "/tmp/project with "quote""`) {
		t.Fatalf("workDir is embedded unsafely:\n%s", body)
	}
	// shellescape.Quote leaves quote-free strings bare, and validateToolSessionID
	// guarantees the id has no shell metacharacters, so assert the bare value
	// (the workDir-with-quotes check above is what proves the quoting is applied).
	if !strings.Contains(string(body), `opencode export ses_parent_123`) {
		t.Fatalf("opencode session id should flow through shellescape.Quote in the export command:\n%s", body)
	}
}
```

- [ ] **Step 5: Implement command quoting**

In `internal/session/instance.go`, update OpenCode flag construction:

```go
for _, arg := range opts.ToArgsForFork() {
	extraFlags += " " + shellescape.Quote(arg)
}
```

and in the default options branch:

```go
for _, arg := range defaultOpts.ToArgsForFork() {
	extraFlags += " " + shellescape.Quote(arg)
}
```

At the top of `writeOpenCodeForkScript`, add:

```go
quotedWorkDir := shellescape.Quote(workDir)
quotedSessionID := shellescape.Quote(i.OpenCodeSessionID)
```

Then change the script template opening and export line to:

```go
cd %s || { echo "cd failed to: %s"; exit 1; }
...
opencode export %s 2>/dev/null > "$tmpfile"
```

and pass:

```go
quotedWorkDir, workDir, envPrefix, quotedSessionID,
i.OpenCodeSessionID, i.OpenCodeSessionID, extraFlags
```

In `buildCodexForkCommandForTarget`, change the final return to:

```go
return envPrefix + fmt.Sprintf("%s%s%s fork %s", command, yoloFlag, modelFlag, shellescape.Quote(i.CodexSessionID)), nil
```

- [ ] **Step 6: Verify**

Run:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test ./internal/session -run 'TestSetField_(OpenCodeSessionID|CodexSessionID|SessionID)|TestOpenCodeForkScriptQuotesWorkDir|TestCreateForkedCodexInstance' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/session/mutators.go internal/session/mutators_test.go internal/session/instance.go internal/session/instance_opencode_fork_test.go internal/session/instance_codex_fork_test.go
git commit -m "fix(fork): validate and quote tool session ids"
```

---

### Task 6: Make Real-Binary Fork Evals Fail on Tool Fork Failure

**Severity:** High. These evals currently allow the main behavior under test to fail.

**Files:**
- Modify: `tests/eval/session/fork_pi_test.go`
- Modify: `tests/eval/session/fork_opencode_test.go`
- Modify: `tests/eval/session/fork_codex_test.go`

- [ ] **Step 1: Add a helper that requires tool binaries**

In `tests/eval/session/fork_pi_test.go`, add:

```go
func requireForkTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not on PATH", name)
	}
}
```

- [ ] **Step 2: Require each tool in its eval**

In `TestEval_SessionForkPi_RealBinary`, add after the git check:

```go
requireForkTool(t, "pi")
```

In `TestEval_SessionForkOpenCode_RealBinary`, add after the git check:

```go
requireForkTool(t, "opencode")
```

In `TestEval_SessionForkCodex_RealBinary`, add after the git check:

```go
requireForkTool(t, "codex")
```

- [ ] **Step 3: Make `assertForkWorktreeBranch` fail on fork errors**

At the top of `assertForkWorktreeBranch`, before `worktreePathForBranch`, add:

```go
if forkErr != nil {
	t.Fatalf("session fork failed before tool fork completed.\nerr: %v\noutput:\n%s", forkErr, forkOut)
}
```

- [ ] **Step 4: Verify skipped/missing-tool behavior**

Run in an environment without at least one tool installed:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test -tags eval_smoke ./tests/eval/session -run 'TestEval_SessionFork(Pi|OpenCode|Codex)_RealBinary' -count=1
```

Expected: tests for missing tools are SKIP, not PASS-after-error.

- [ ] **Step 5: Verify real-tool behavior**

Run in an environment with `pi`, `opencode`, and `codex` available:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test -tags eval_smoke ./tests/eval/session -run 'TestEval_SessionFork(Pi|OpenCode|Codex)_RealBinary' -count=1
```

Expected: PASS only when each fork command exits successfully and the destination branch exists.

- [ ] **Step 6: Commit**

```bash
git add tests/eval/session/fork_pi_test.go tests/eval/session/fork_opencode_test.go tests/eval/session/fork_codex_test.go
git commit -m "test(eval): require successful real tool forks"
```

---

### Task 7: Update CLI Help, README, and Capability Copy

**Severity:** Low. This prevents user confusion and stale executor assumptions.

**Files:**
- Modify: `cmd/agent-deck/session_cmd.go`
- Modify: `README.md`
- Modify: `docs/status/capability-e2e-manifest.json`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Update CLI help strings**

In `cmd/agent-deck/session_cmd.go`, change:

```go
fmt.Println("  fork <id>               Fork Claude or Pi session with context")
```

to:

```go
fmt.Println("  fork <id>               Fork Claude, OpenCode, Pi, or Codex session with context")
```

Change the comment:

```go
// handleSessionFork forks a Claude or Pi session
```

to:

```go
// handleSessionFork forks a supported tool session with conversation context.
```

Change:

```go
fmt.Println("Fork a Claude or Pi session with conversation context.")
```

to:

```go
fmt.Println("Fork a Claude, OpenCode, Pi, or Codex session with conversation context.")
```

- [ ] **Step 2: Update README fork copy**

Change README fork section line 103 to:

```markdown
Try different approaches without losing context. Fork Claude, OpenCode, Pi, and Codex sessions instantly. Each fork inherits the parent conversation history through the tool's native fork support.
```

Change quick start line 783 to:

```markdown
agent-deck session fork my-proj   # Fork a supported session with context
```

- [ ] **Step 3: Update capability manifest wording**

In `docs/status/capability-e2e-manifest.json`, update fork/fork-context text from Claude/Pi-only wording to supported-tool wording:

```json
"how_we_test": "Forking is only valid for supported tools with live context. We confirm unsupported sessions are cleanly refused and real supported-tool fork evals run under eval_smoke/nightly coverage."
```

and:

```json
"how_we_test": "We fork live supported-tool sessions and confirm the child inherits context with a distinct id and fork worktree/branch where requested. This needs real tool session data and tool binaries, so it runs under eval_smoke/nightly coverage."
```

- [ ] **Step 4: Add changelog note**

Add under the current unreleased section:

```markdown
- Fix fork parity follow-up: Web/API now routes Claude/OpenCode/Pi/Codex through the shared tool-specific fork dispatcher, Shift+F honors `[fork].branch_prefix`, tool session-id mutators validate IDs before generated shell commands, and real-binary fork evals now fail on tool fork errors instead of only checking worktree creation.
```

- [ ] **Step 5: Verify stale wording is gone**

Run:

```bash
rg -n "Claude/Pi|Claude or Pi|forks a Claude or Pi|Fork Claude, Codex, and Pi" README.md cmd/agent-deck/session_cmd.go docs/status/capability-e2e-manifest.json
```

Expected: no stale Claude/Pi-only fork wording remains.

- [ ] **Step 6: Commit**

```bash
git add cmd/agent-deck/session_cmd.go README.md docs/status/capability-e2e-manifest.json CHANGELOG.md
git commit -m "docs: update cross-tool fork wording"
```

---

### Task 8: Final Verification

**Severity:** Release gate. This verifies the remediation as a whole.

**Files:**
- No code changes expected.

- [ ] **Step 1: Run whitespace check**

Run:

```bash
git diff --check main...HEAD
```

Expected: no output, exit 0.

- [ ] **Step 2: Run focused fork packages**

Run:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test ./internal/session ./internal/ui ./internal/web ./cmd/agent-deck -run 'Fork|ForkSettings|SetField_OpenCode|SetField_Codex|Codex|OpenCode|SettingsPanel_Fork|ForkDialog|MenuSession|WebMutator' -count=1
```

Expected: PASS.

- [ ] **Step 3: Run eval smoke where tools exist**

Run:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test -tags eval_smoke ./tests/eval/session -run 'TestEval_SessionFork(Pi|OpenCode|Codex)_RealBinary' -count=1
```

Expected: PASS for installed tools, SKIP for missing tools. Any non-skip failure is a blocker.

- [ ] **Step 4: Run broader package tests when environment permits**

Run:

```bash
GOCACHE=/private/tmp/agent-deck-fork-remediation-go-cache GOTOOLCHAIN=go1.25.11 go test ./internal/session ./internal/ui ./internal/web ./cmd/agent-deck -count=1
```

Expected: PASS in a normal local environment. If sandbox blocks localhost binds or network-backed subprocess builds, record exact failures and rerun with the needed local permissions or warmed caches before merge.

- [ ] **Step 5: Review final diff**

Run:

```bash
git diff --stat main...HEAD
git diff --name-status main...HEAD
```

Expected: changes are limited to fork dispatch, fork tests/evals, web fork affordance, docs/help, and this remediation plan.
