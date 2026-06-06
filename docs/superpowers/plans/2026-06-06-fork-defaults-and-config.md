# Comprehensive Quick Fork — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the TUI quick fork (`f`) comprehensive by default (new worktree+branch, carry tracked+gitignored state, match parent Docker, inherit parent Claude opts), configurable via a new `[fork]` config section, with the `Shift+F` dialog seeded from the same defaults.

**Architecture:** A new `ForkSettings` config section resolves (via a pure `Resolve` method) into a `ResolvedForkPlan` of structural toggles. `quickForkSession` consults this plan plus `source.GetClaudeOptions()`, computes a branch, emits synchronous degradation notices, and dispatches through a newly-extracted `buildForkCmd` helper shared with the dialog path. `ForkDialog.Show` seeds its checkboxes from the same `[fork]` defaults.

**Tech Stack:** Go 1.25.11 (`export GOTOOLCHAIN=go1.25.11`), BurntSushi/toml, testify/assert, Bubble Tea TUI.

**Baseline note:** Run all commands with `export GOTOOLCHAIN=go1.25.11`. Spec: `docs/superpowers/specs/2026-06-06-comprehensive-quick-fork-design.md`.

---

## File Structure

- `internal/session/userconfig.go` — add `Fork ForkSettings` field, `ForkSettings` struct, getters, `ResolvedForkPlan`, `Resolve`. (Config + pure precedence logic — unit-testable without UI.)
- `internal/session/userconfig_fork_test.go` — **create.** Tests for getters + `Resolve`.
- `internal/ui/home.go` — extract `buildForkCmd` helper from `handleForkDialogKey`; rewrite `quickForkSession`; add pure `quickForkInputs` seam.
- `internal/ui/fork_quick_test.go` — **create.** Tests for `quickForkInputs`.
- `internal/ui/forkdialog.go` — seed `Show` from `[fork]` defaults.
- `tests/eval/cases/` — **create** an eval-smoke case for the user-observable behavior change (per CLAUDE.local.md mandate).

---

## Task 1: `ForkSettings` config struct + getters

**Files:**
- Modify: `internal/session/userconfig.go` (UserConfig fields near `Docker DockerSettings` at line ~174; add struct near `DockerSettings` at line ~1857)
- Test: `internal/session/userconfig_fork_test.go` (create)

- [ ] **Step 1: Write the failing tests**

Create `internal/session/userconfig_fork_test.go`:

```go
package session

import (
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
)

func decodeForkConfig(t *testing.T, doc string) UserConfig {
	t.Helper()
	var cfg UserConfig
	if _, err := toml.Decode(doc, &cfg); err != nil {
		t.Fatalf("toml.Decode: %v", err)
	}
	return cfg
}

func TestForkSettings_StructuralDefaults_WhenSectionAbsent(t *testing.T) {
	cfg := decodeForkConfig(t, ``)
	assert.True(t, cfg.Fork.GetWorktree(), "worktree default ON when unset")
	assert.True(t, cfg.Fork.GetWithState(), "with_state default ON when unset")
	assert.True(t, cfg.Fork.GetWithIgnored(), "with_ignored default ON when unset")
	assert.Equal(t, "auto", cfg.Fork.GetDocker(), "docker default 'auto' when unset")
	assert.Equal(t, "fork/", cfg.Fork.GetBranchPrefix(), "branch_prefix default when unset")
	assert.False(t, cfg.Fork.InheritFromParent, "inherit_from_parent default false")
}

func TestForkSettings_ExplicitFalseHonored(t *testing.T) {
	cfg := decodeForkConfig(t, "[fork]\nworktree = false\nwith_state = false\nwith_ignored = false\n")
	assert.False(t, cfg.Fork.GetWorktree())
	assert.False(t, cfg.Fork.GetWithState())
	assert.False(t, cfg.Fork.GetWithIgnored())
}

func TestForkSettings_GetDocker_Canonicalizes(t *testing.T) {
	cases := map[string]string{
		`[fork]` + "\n" + `docker = "ON"`:      "on",
		`[fork]` + "\n" + `docker = " Off "`:   "off",
		`[fork]` + "\n" + `docker = "auto"`:    "auto",
		`[fork]` + "\n" + `docker = "bogus"`:   "auto", // unknown -> default
	}
	for doc, want := range cases {
		cfg := decodeForkConfig(t, doc)
		assert.Equal(t, want, cfg.Fork.GetDocker(), "doc=%q", doc)
	}
}

func TestForkSettings_GetBranchPrefix_Override(t *testing.T) {
	cfg := decodeForkConfig(t, "[fork]\nbranch_prefix = \"wip/\"\n")
	assert.Equal(t, "wip/", cfg.Fork.GetBranchPrefix())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/session/ -run 'TestForkSettings' -count=1`
Expected: FAIL — `cfg.Fork undefined` / `GetWorktree undefined`.

- [ ] **Step 3: Add the `Fork` field to `UserConfig`**

In `internal/session/userconfig.go`, immediately after the `Docker DockerSettings` field (line ~174):

```go
	// Fork defines quick-fork (f) and fork-dialog (Shift+F) default behavior.
	Fork ForkSettings `toml:"fork"`
```

- [ ] **Step 4: Add the `ForkSettings` struct + getters**

In `internal/session/userconfig.go`, after the `DockerSettings` struct block (after line ~1877+; place just before its closing-related helpers or at end of the settings structs region):

```go
// ForkSettings controls quick-fork (f) and fork-dialog (Shift+F) defaults.
// Unset structural toggles default to the comprehensive built-in (ON); these
// defaults are independent of [worktree]/[docker] default_enabled, which govern
// non-fork session creation. *bool is required so "absent" reads as ON.
type ForkSettings struct {
	// InheritFromParent, when true, makes the fork mirror the parent session and
	// ignores the structural keys below. See Resolve.
	InheritFromParent bool `toml:"inherit_from_parent"`

	// Worktree creates a new worktree + branch. nil => true.
	Worktree *bool `toml:"worktree"`
	// WithState carries the parent's tracked uncommitted changes. nil => true.
	WithState *bool `toml:"with_state"`
	// WithIgnored also copies gitignored files (implies WithState). nil => true.
	WithIgnored *bool `toml:"with_ignored"`
	// Docker selects sandbox behavior: "auto" (match parent) | "on" | "off".
	// nil/unknown => "auto". Mirrors the [tmux].launch_as string-enum convention.
	Docker *string `toml:"docker"`
	// BranchPrefix is the auto branch-name prefix. "" => "fork/".
	BranchPrefix string `toml:"branch_prefix"`
}

// GetWorktree reports whether forks create a worktree (default ON).
func (f ForkSettings) GetWorktree() bool { return f.Worktree == nil || *f.Worktree }

// GetWithState reports whether forks carry tracked state (default ON).
func (f ForkSettings) GetWithState() bool { return f.WithState == nil || *f.WithState }

// GetWithIgnored reports whether forks copy gitignored files (default ON).
func (f ForkSettings) GetWithIgnored() bool { return f.WithIgnored == nil || *f.WithIgnored }

// GetDocker returns the canonical docker mode: "auto" | "on" | "off".
// Mirrors GetLaunchAs: lowercase/trim, unknown/nil -> "auto".
func (f ForkSettings) GetDocker() string {
	if f.Docker == nil {
		return "auto"
	}
	switch v := strings.ToLower(strings.TrimSpace(*f.Docker)); v {
	case "auto", "on", "off":
		return v
	default:
		return "auto"
	}
}

// GetBranchPrefix returns the auto branch-name prefix (default "fork/").
func (f ForkSettings) GetBranchPrefix() string {
	if f.BranchPrefix == "" {
		return "fork/"
	}
	return f.BranchPrefix
}
```

(`strings` is already imported in `userconfig.go` — it is used by `GetLaunchAs`.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/session/ -run 'TestForkSettings' -count=1`
Expected: PASS (4 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/session/userconfig.go internal/session/userconfig_fork_test.go
git commit -m "feat(session): add [fork] config section with comprehensive defaults"
```

---

## Task 2: `ForkSettings.Resolve` precedence logic

**Files:**
- Modify: `internal/session/userconfig.go` (after the getters from Task 1)
- Test: `internal/session/userconfig_fork_test.go` (append)

- [ ] **Step 1: Write the failing tests**

Append to `internal/session/userconfig_fork_test.go`:

```go
func TestForkSettings_Resolve_ComprehensiveDefault_DockerAuto(t *testing.T) {
	cfg := decodeForkConfig(t, ``) // all defaults
	// parent NOT sandboxed -> auto resolves sandbox off
	p := cfg.Fork.Resolve(false)
	assert.Equal(t, ResolvedForkPlan{Worktree: true, WithState: true, WithIgnored: true, Sandbox: false}, p)
	// parent sandboxed -> auto resolves sandbox on
	p = cfg.Fork.Resolve(true)
	assert.True(t, p.Sandbox, "docker=auto with sandboxed parent -> sandbox on")
}

func TestForkSettings_Resolve_DockerOnOff_OverrideParent(t *testing.T) {
	on := decodeForkConfig(t, "[fork]\ndocker = \"on\"\n").Fork.Resolve(false)
	assert.True(t, on.Sandbox, "docker=on forces sandbox even if parent not sandboxed")
	off := decodeForkConfig(t, "[fork]\ndocker = \"off\"\n").Fork.Resolve(true)
	assert.False(t, off.Sandbox, "docker=off forces no sandbox even if parent sandboxed")
}

func TestForkSettings_Resolve_InheritFromParent_OverridesStructuralKeys(t *testing.T) {
	// Even with structural keys turned off, inherit_from_parent forces the
	// comprehensive worktree+state mapping and matches parent docker.
	cfg := decodeForkConfig(t, "[fork]\ninherit_from_parent = true\nworktree = false\nwith_state = false\nwith_ignored = false\ndocker = \"off\"\n")
	p := cfg.Fork.Resolve(true) // parent sandboxed
	assert.Equal(t, ResolvedForkPlan{Worktree: true, WithState: true, WithIgnored: true, Sandbox: true}, p)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/session/ -run 'TestForkSettings_Resolve' -count=1`
Expected: FAIL — `ResolvedForkPlan undefined` / `Resolve undefined`.

- [ ] **Step 3: Add `ResolvedForkPlan` + `Resolve`**

In `internal/session/userconfig.go`, after the Task 1 getters:

```go
// ResolvedForkPlan is the effective set of structural fork toggles after
// applying [fork] config + parent context.
type ResolvedForkPlan struct {
	Worktree    bool
	WithState   bool
	WithIgnored bool
	Sandbox     bool
}

// Resolve turns ForkSettings + the parent's Docker state into a concrete plan.
// parentSandboxed is source.IsSandboxed(). When InheritFromParent is set, the
// fork mirrors the parent: worktree+state+gitignored ON (the parent is a real
// working tree) and Sandbox matches the parent, ignoring the structural keys.
func (f ForkSettings) Resolve(parentSandboxed bool) ResolvedForkPlan {
	if f.InheritFromParent {
		return ResolvedForkPlan{Worktree: true, WithState: true, WithIgnored: true, Sandbox: parentSandboxed}
	}
	sandbox := parentSandboxed
	switch f.GetDocker() {
	case "on":
		sandbox = true
	case "off":
		sandbox = false
	}
	return ResolvedForkPlan{
		Worktree:    f.GetWorktree(),
		WithState:   f.GetWithState(),
		WithIgnored: f.GetWithIgnored(),
		Sandbox:     sandbox,
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/session/ -run 'TestForkSettings' -count=1`
Expected: PASS (7 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/session/userconfig.go internal/session/userconfig_fork_test.go
git commit -m "feat(session): ForkSettings.Resolve precedence (inherit + docker auto/on/off)"
```

---

## Task 3: Extract `buildForkCmd` helper, refactor dialog to use it

**Files:**
- Modify: `internal/ui/home.go` (`handleForkDialogKey` enter-branch, lines ~8581-8621; add `buildForkCmd` near `forkSessionCmdWithOptions` ~9566)

This is a pure refactor — no behavior change. Existing fork-dialog tests are the safety net.

- [ ] **Step 1: Add the `buildForkCmd` helper**

In `internal/ui/home.go`, immediately before `forkSessionCmdWithOptions` (line ~9566):

```go
// buildForkCmd resolves the worktree target (when requested + git-capable),
// populates the worktree fields on opts, builds WorktreeStateOptions, and
// returns the async fork command. Shared by the dialog (Shift+F) and quick
// fork (f). When worktree is requested on a non-git dir, resolveWorktreeTarget
// reports fallback and the fork proceeds without a worktree. explicitWorktree
// is forwarded to resolveWorktreeTarget's #1185 fallback gate.
func (h *Home) buildForkCmd(
	source *session.Instance,
	title, groupPath, branchName string,
	worktreeEnabled, withState, withIgnored, sandboxEnabled, explicitWorktree bool,
	opts *session.ClaudeOptions,
	parentSessionID, parentProjectPath string,
) (tea.Cmd, bool) {
	worktreeApplied := false
	if worktreeEnabled && branchName != "" {
		worktreePath, repoRoot, fallback, errMsg := resolveWorktreeTarget(source.ProjectPath, branchName, explicitWorktree)
		if errMsg != "" {
			h.setError(fmt.Errorf("%s", errMsg))
			return nil, false
		}
		if !fallback {
			if opts == nil {
				opts = &session.ClaudeOptions{}
			}
			opts.WorkDir = worktreePath
			opts.WorktreePath = worktreePath
			opts.WorktreeRepoRoot = repoRoot
			opts.WorktreeBranch = branchName
			worktreeApplied = true
		}
	}
	forkState := git.WorktreeStateOptions{WithState: withState, WithIgnored: withIgnored}
	if !worktreeApplied {
		// State materialization requires a freshly created worktree.
		forkState = git.WorktreeStateOptions{}
	}
	return h.forkSessionCmdWithOptions(source, title, groupPath, opts, sandboxEnabled, forkState, parentSessionID, parentProjectPath), worktreeApplied
}
```

- [ ] **Step 2: Refactor `handleForkDialogKey` to call it**

In `internal/ui/home.go`, replace the enter-branch body (lines ~8596-8621, from the `if worktreeEnabled && branchName != "" {` block through the `return h, h.forkSessionCmdWithOptions(...)` line) with:

```go
				parentID := h.forkDialog.GetParentSessionID()
				parentPath := h.forkDialog.GetParentProjectPath()
				cmd, _ := h.buildForkCmd(
					source, title, groupPath, branchName,
					worktreeEnabled,
					h.forkDialog.IsWithStateEnabled(),
					h.forkDialog.IsWithStateAndGitignoredEnabled(),
					h.forkDialog.IsSandboxEnabled(),
					h.forkDialog.IsWorktreeExplicit(),
					opts,
					parentID, parentPath,
				)
				if cmd == nil {
					return h, nil // error already surfaced by buildForkCmd
				}
				h.forkDialog.Hide()
				return h, cmd
```

(Keep the surrounding `if item.Type == ... source := item.Session` framing and the `opts := h.forkDialog.GetOptions()` line above it unchanged.)

- [ ] **Step 3: Build + run the existing fork-dialog tests**

Run: `export GOTOOLCHAIN=go1.25.11 && go build ./... && go test ./internal/ui/ -run 'Fork' -count=1`
Expected: PASS (no behavior change; existing fork tests still green).

- [ ] **Step 4: Commit**

```bash
git add internal/ui/home.go
git commit -m "refactor(tui): extract buildForkCmd shared by fork dialog and quick fork"
```

---

## Task 4: Comprehensive `quickForkSession` (+ pure `quickForkInputs` seam)

**Files:**
- Modify: `internal/ui/home.go` (`quickForkSession` ~9120; add `quickForkInputs`)
- Test: `internal/ui/fork_quick_test.go` (create)

- [ ] **Step 1: Write the failing test for the pure seam**

Create `internal/ui/fork_quick_test.go`:

```go
package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/assert"
)

func TestQuickForkInputs_DefaultsAndBranchSlug(t *testing.T) {
	src := session.NewInstanceWithTool("My Feature", "/tmp/proj", "claude")
	src.GroupPath = "team/x"
	fork := session.ForkSettings{} // comprehensive defaults

	in := quickForkInputs(src, fork, false /* parentSandboxed */)

	assert.Equal(t, "My Feature (fork)", in.Title)
	assert.Equal(t, "team/x", in.GroupPath)
	assert.Equal(t, "fork/my-feature", in.Branch)
	assert.True(t, in.Plan.Worktree)
	assert.True(t, in.Plan.WithState)
	assert.True(t, in.Plan.WithIgnored)
	assert.False(t, in.Plan.Sandbox)
}

func TestQuickForkInputs_BranchPrefixOverride(t *testing.T) {
	src := session.NewInstanceWithTool("Fix Bug", "/tmp/proj", "claude")
	prefix := "wip/"
	fork := session.ForkSettings{BranchPrefix: prefix}
	in := quickForkInputs(src, fork, false)
	assert.Equal(t, "wip/fix-bug", in.Branch)
}

func TestQuickForkInputs_DockerAutoMatchesSandboxedParent(t *testing.T) {
	src := session.NewInstanceWithTool("svc", "/tmp/proj", "claude")
	in := quickForkInputs(src, session.ForkSettings{}, true /* parentSandboxed */)
	assert.True(t, in.Plan.Sandbox, "docker=auto + sandboxed parent -> sandbox on")
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/ui/ -run 'TestQuickForkInputs' -count=1`
Expected: FAIL — `quickForkInputs undefined`.

- [ ] **Step 3: Implement `quickForkInputs` + rewrite `quickForkSession`**

In `internal/ui/home.go`, replace `quickForkSession` (lines ~9120-9128) with:

```go
// quickForkSpec is the resolved input set for a comprehensive quick fork.
type quickForkSpec struct {
	Title     string
	GroupPath string
	Branch    string
	Plan      session.ResolvedForkPlan
}

// quickForkInputs computes the comprehensive quick-fork spec from the source
// session and [fork] config. Pure: no side effects, no UI, no I/O — the wiring
// (Claude-opts inheritance, degradation notices, dispatch) lives in
// quickForkSession. parentSandboxed is source.IsSandboxed().
func quickForkInputs(source *session.Instance, fork session.ForkSettings, parentSandboxed bool) quickForkSpec {
	slug := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(source.Title), " ", "-"))
	return quickForkSpec{
		Title:     source.Title + " (fork)",
		GroupPath: source.GroupPath,
		Branch:    fork.GetBranchPrefix() + slug,
		Plan:      fork.Resolve(parentSandboxed),
	}
}

// quickForkSession performs a comprehensive quick fork: new worktree+branch,
// carry tracked+gitignored state, match parent Docker, inherit the parent's
// Claude launch options, sibling placement. Defaults come from [fork] config
// (comprehensive when unset). Degrades best-effort with a brief notice when a
// capability is unavailable (non-git dir, Docker absent).
func (h *Home) quickForkSession(source *session.Instance) tea.Cmd {
	if source == nil {
		return nil
	}
	cfg, _ := session.LoadUserConfig()
	fork := session.ForkSettings{}
	if cfg != nil {
		fork = cfg.Fork
	}
	in := quickForkInputs(source, fork, source.IsSandboxed())

	// Docker degradation: requested but unavailable -> drop sandbox + notify.
	sandbox := in.Plan.Sandbox
	if sandbox && !docker.IsDockerAvailable() {
		sandbox = false
		h.setError(fmt.Errorf("forked without Docker: docker not available"))
	}

	// Inherit the parent's persisted Claude launch options (transient worktree
	// fields are json:"-" so they are never carried over). nil falls back to
	// global config downstream, as before.
	opts := source.GetClaudeOptions()

	cmd, worktreeApplied := h.buildForkCmd(
		source, in.Title, in.GroupPath, in.Branch,
		in.Plan.Worktree, in.Plan.WithState, in.Plan.WithIgnored, sandbox,
		false, // quick fork worktree is config-default, not an explicit toggle (#1185)
		opts,
		source.ParentSessionID, source.ParentProjectPath,
	)
	// Worktree degradation: requested but the dir is not git-capable.
	if in.Plan.Worktree && !worktreeApplied {
		h.setError(fmt.Errorf("forked without worktree: not a git repo"))
	}
	return cmd
}
```

- [ ] **Step 4: Add imports**

Ensure `internal/ui/home.go` imports `"github.com/asheshgoplani/agent-deck/internal/docker"` (add to the import block if absent). `strings`, `fmt`, `git`, and `session` are already imported.

- [ ] **Step 5: Run tests + build**

Run: `export GOTOOLCHAIN=go1.25.11 && go build ./... && go test ./internal/ui/ -run 'TestQuickForkInputs' -count=1`
Expected: PASS (3 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/ui/home.go internal/ui/fork_quick_test.go
git commit -m "feat(tui): comprehensive quick fork (worktree+state+docker+opts inherit)"
```

---

## Task 5: `ForkDialog.Show` seeds from `[fork]` defaults

**Files:**
- Modify: `internal/ui/forkdialog.go` (`Show`, lines ~201-229)
- Test: `internal/ui/forkdialog_test.go` (append, or create `internal/ui/forkdialog_fork_defaults_test.go` if no suitable file)

- [ ] **Step 1: Write the failing test**

Create `internal/ui/forkdialog_fork_defaults_test.go`:

```go
package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// With no [fork] config present, the dialog opens reflecting the comprehensive
// built-in defaults: with-state ON and (in a git repo) gitignored ON.
func TestForkDialog_Show_SeedsComprehensiveWithStateDefault(t *testing.T) {
	d := NewForkDialog()
	// projectPath need not be a git repo for the with-state toggles, which are
	// independent of worktreeCapable in the dialog state.
	d.Show("My Session", t.TempDir(), "grp", nil, "")
	assert.True(t, d.IsWithStateEnabled(), "with_state seeded ON from [fork] comprehensive default")
	assert.True(t, d.IsWithStateAndGitignoredEnabled(), "with_ignored seeded ON from [fork] comprehensive default")
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/ui/ -run 'TestForkDialog_Show_SeedsComprehensive' -count=1`
Expected: FAIL — `with_state` currently seeded OFF (`d.withStateEnabled = false`).

- [ ] **Step 3: Seed the dialog from `[fork]`**

In `internal/ui/forkdialog.go` `Show`, replace the config-defaults block (lines ~224-229, the `if config, err := session.LoadUserConfig(); err == nil { ... }` block) with:

```go
	// Initialize options + structural toggles from [fork] defaults so the dialog
	// opens "comprehensive, tweak down" — matching quick fork (f).
	if config, err := session.LoadUserConfig(); err == nil {
		d.optionsPanel.SetDefaults(config)
		plan := config.Fork.Resolve(false) // dialog seeds from config, not a live parent
		d.withStateEnabled = plan.WithState
		d.withStateAndGitignored = d.withStateEnabled && plan.WithIgnored
		d.sandboxEnabled = plan.Sandbox
		d.worktreeEnabled = d.worktreeCapable && plan.Worktree
	}
```

(The earlier unconditional resets at lines ~202-206 remain; this block overrides them when config loads. `withStateAndGitignored` stays gated on `withStateEnabled` to preserve the nesting invariant.)

- [ ] **Step 4: Run the test + existing dialog tests**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/ui/ -run 'ForkDialog' -count=1`
Expected: PASS (new test + existing dialog tests).

- [ ] **Step 5: Commit**

```bash
git add internal/ui/forkdialog.go internal/ui/forkdialog_fork_defaults_test.go
git commit -m "feat(tui): seed ForkDialog from [fork] comprehensive defaults"
```

---

## Task 6: Eval-smoke case (CLAUDE.local.md mandate)

The dialog now opens with comprehensive defaults **already checked** (no keystrokes) — a user-visible disclosure exactly in the class the harness exists for (cf. the v1.7.37 "TUI disclosure missing" bug). Pure unit tests assert getter state (Task 5); this eval asserts the rendered `View()`. It mirrors the existing `internal/ui/forkdialog_eval_test.go` idiom (`//go:build eval_smoke`, `NewForkDialog`→`Show`→assert `View()`).

The worktree+state materialization machinery itself is already eval-covered end-to-end by `tests/eval/session/fork_with_state_test.go` (shared `forkSessionCmdWithOptions` path), so this case targets the genuinely-new surface: the dialog's seeded defaults being visible.

**Files:**
- Create: `internal/ui/quick_fork_defaults_eval_test.go`

- [ ] **Step 1: Write the eval case**

Create `internal/ui/quick_fork_defaults_eval_test.go`:

```go
//go:build eval_smoke

package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestEval_ForkDialog_ComprehensiveDefaultsVisibleOnOpen proves that, with NO
// [fork] config present, the fork dialog opens on a git project with the
// comprehensive defaults (worktree + carry-state + gitignored) ALREADY checked
// — i.e. the user SEES "comprehensive, tweak down" without pressing a key.
// This is the disclosure-visible contract that pure getter tests can't express.
func TestEval_ForkDialog_ComprehensiveDefaultsVisibleOnOpen(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	// Scratch HOME so the developer's real ~/.agent-deck/config.toml (which may
	// carry a [fork] section) can't perturb the default under test.
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.ClearUserConfigCache()
	t.Cleanup(func() { session.ClearUserConfigCache() })

	// Real git repo so git.IsGitRepoOrBareProjectRoot() -> worktreeCapable=true,
	// which lets the worktree + nested with-state rows render.
	repo := filepath.Join(home, "proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	d := NewForkDialog()
	d.SetSize(90, 40)
	d.Show("Eval Parent", repo, "", nil, "")

	// State getters: comprehensive defaults seeded with zero interaction.
	if !d.IsWorktreeEnabled() {
		t.Error("worktree must default ON in a git repo with no [fork] config")
	}
	if !d.IsWithStateEnabled() {
		t.Error("carry-parent-state must default ON with no [fork] config")
	}
	if !d.IsWithStateAndGitignoredEnabled() {
		t.Error("include-gitignored must default ON with no [fork] config")
	}

	// Rendered, user-visible disclosure: the checked boxes appear on open.
	view := d.View()
	for _, want := range []string{"[x] Carry parent state", "[x] Include gitignored files"} {
		if !strings.Contains(view, want) {
			t.Errorf("dialog must render %q checked on open; view:\n%s", want, view)
		}
	}
}
```

- [ ] **Step 2: Run the eval-smoke suite**

Run: `export GOTOOLCHAIN=go1.25.11 && go test -tags eval_smoke ./internal/ui/... -run 'TestEval_ForkDialog_ComprehensiveDefaultsVisibleOnOpen' -count=1`
Expected: PASS. (If the rendered label strings differ from `forkdialog_eval_test.go`'s — `"[x] Carry parent state"`, `"[x] Include gitignored files"` — match the dialog's actual `View()` labels, which that existing eval pins.)

- [ ] **Step 3: Full eval-smoke suite**

Run: `export GOTOOLCHAIN=go1.25.11 && go test -tags eval_smoke ./tests/eval/... ./internal/ui/...`
Expected: PASS including the new case.

- [ ] **Step 4: Commit**

```bash
git add internal/ui/quick_fork_defaults_eval_test.go
git commit -m "test(eval): fork dialog renders comprehensive defaults checked on open"
```

---

## Final Verification

- [ ] **Full mandated suites + build:**

```bash
export GOTOOLCHAIN=go1.25.11
go build ./...
go test -run TestPersistence_ ./internal/session/... -race -count=1
go test ./internal/session/... -run 'Fork|TestForkSettings' -race -count=1
go test ./internal/ui/... -run 'Fork|Watcher' -race -count=1
go test -tags eval_smoke ./tests/eval/... ./internal/ui/...
```

Expected: all PASS. (Persistence suite is green per the macOS fixture fix already committed.)

- [ ] **Manual smoke (optional, real TUI):** launch agent-deck on a git project, press `f` on a Claude session, confirm a `(fork)` session appears in a new worktree on a `fork/<slug>` branch; on a non-git dir confirm the "forked without worktree" notice and a plain fork.

---

## Spec ↔ Plan coverage

| Spec item | Task |
|---|---|
| `[fork]` section, bare keys, `*bool` nil=ON, `GetDocker` like `GetLaunchAs` | 1 |
| Precedence: `[fork]` wins → comprehensive; globals ignored | 1, 2 |
| `inherit_from_parent` mapping | 2 |
| Docker `auto`/`on`/`off`, auto = `IsSandboxed()` | 2, 4 |
| Worktree+state+gitignored ON by default | 1, 4 |
| Shared helper extraction | 3 |
| Comprehensive `f`: branch, opts inherit, sibling placement | 4 |
| Graceful degradation + brief notice (synchronous via setError) | 4 |
| Dialog seeded from `[fork]` | 5 |
| Eval case (user-observable mandate) | 6 |
