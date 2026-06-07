# Comprehensive Quick Fork — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the TUI quick fork (`f`) comprehensive by default (new worktree+branch, carry tracked+gitignored state, match parent Docker, inherit parent Claude opts), configurable via a new `[fork]` config section, with the `Shift+F` dialog seeded from the same defaults.

**Architecture:** A new `ForkSettings` config section resolves (via a pure `Resolve` method) into a `ResolvedForkPlan` of structural toggles. `quickForkSession` consults this plan plus `source.GetClaudeOptions()` for Claude-compatible sessions, computes a sanitized branch via the existing git sanitizer, and dispatches through a newly-extracted `buildForkCmd` helper shared with the dialog path. Non-fatal degradation notices flow through `sessionForkedMsg.notice` after the async fork succeeds, and `ForkDialog` seeds from the same `[fork]` defaults using the selected parent's sandbox state so Docker `"auto"` matches quick fork.

**Tech Stack:** Go 1.25.11 (`export GOTOOLCHAIN=go1.25.11`), BurntSushi/toml, testify/assert, Bubble Tea TUI.

**Baseline note:** Run all commands with `export GOTOOLCHAIN=go1.25.11`. Spec: `docs/superpowers/specs/2026-06-06-comprehensive-quick-fork-design.md`.

---

## File Structure

- `internal/session/userconfig.go` — add `Fork ForkSettings` field, `ForkSettings` struct, getters, `ResolvedForkPlan`, `Resolve`. (Config + pure precedence logic — unit-testable without UI.)
- `internal/session/userconfig_fork_test.go` — **create.** Tests for getters + `Resolve`.
- `internal/ui/settings_panel.go` — preserve `[fork]` in direct `SettingsPanel.GetConfig()` output, matching existing hidden-section pass-through guards.
- `internal/ui/settings_panel_test.go` — append a `[fork]` hidden-section preservation test.
- `internal/ui/home.go` — extract `buildForkCmd` helper from `handleForkDialogKey`; rewrite `quickForkSession`; add pure `quickForkInputs` seam.
- `internal/ui/fork_quick_test.go` — **create.** Tests for `quickForkInputs`.
- `internal/ui/forkdialog.go` — seed `Show` from `[fork]` defaults.
- `internal/session/instance.go` — ensure OpenCode fork creation consumes the resolved worktree target, not only Claude/Pi.
- `internal/ui/quick_fork_defaults_eval_test.go` — **create** an eval-smoke case for the user-observable behavior change (per `tests/eval/README.md` and existing fork-dialog eval pattern).

### Cross-tool parity (Tasks 7–10)

- `internal/session/mutators.go` — add `opencode-session-id` / `codex-session-id` setter fields (Task 7).
- `cmd/agent-deck/session_cmd.go` — admit opencode (Task 8) + codex (Task 9) at the `session fork` gate + dispatch.
- `internal/session/instance.go` — `CanForkCodex`, `buildCodexForkCommandForTarget`, `CreateForkedCodexInstanceWithOptions`; `CanFork` codex branch (Task 9).
- `internal/ui/home.go` — `defaultForkInstanceDeps` codex case (Task 9).
- `internal/session/instance_codex_fork_test.go`, `tests/eval/session/fork_{pi,opencode,codex}_test.go` — **create** (Tasks 9, 10).
- `internal/session/fork_start_dispatch_test.go` — extend the existing fork-start sentinel structural test for Codex (Task 9).

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

func TestForkSettings_Resolve_WithIgnoredImpliesWithState(t *testing.T) {
	cfg := decodeForkConfig(t, "[fork]\nwith_state = false\nwith_ignored = true\n")
	p := cfg.Fork.Resolve(false)
	assert.True(t, p.WithState, "with_ignored must imply with_state")
	assert.True(t, p.WithIgnored)
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
	withIgnored := f.GetWithIgnored()
	withState := f.GetWithState() || withIgnored
	return ResolvedForkPlan{
		Worktree:    f.GetWorktree(),
		WithState:   withState,
		WithIgnored: withIgnored,
		Sandbox:     sandbox,
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/session/ -run 'TestForkSettings' -count=1`
Expected: PASS (8 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/session/userconfig.go internal/session/userconfig_fork_test.go
git commit -m "feat(session): ForkSettings.Resolve precedence (inherit + docker auto/on/off)"
```

---

## Task 2A: Preserve `[fork]` through direct SettingsPanel config output

**Files:**
- Modify: `internal/ui/settings_panel.go` (`GetConfig` hidden-section pass-through near line ~464)
- Test: `internal/ui/settings_panel_test.go` (append near the Worktree/Tmux preservation tests)

`home.go` saves Settings output through `session.MergePanelConfigOntoDisk`, which starts from disk and already preserves new top-level sections. This guard still matters because `SettingsPanel.GetConfig()` has direct hidden-section pass-through tests for `Worktree` and `Tmux`, and future direct consumers should not silently zero `[fork]`.

- [ ] **Step 1: Write the failing preservation test**

Append to `internal/ui/settings_panel_test.go` near `TestSettingsPanel_Worktree_GetConfigPreservesHiddenFields`:

```go
func TestSettingsPanel_Fork_GetConfigPreservesHiddenFields(t *testing.T) {
	panel := NewSettingsPanel()

	worktree := false
	withState := false
	withIgnored := false
	dockerMode := "off"
	original := &session.UserConfig{
		Fork: session.ForkSettings{
			InheritFromParent: true,
			Worktree:          &worktree,
			WithState:         &withState,
			WithIgnored:       &withIgnored,
			Docker:            &dockerMode,
			BranchPrefix:      "wip/",
		},
	}
	panel.LoadConfig(original)
	panel.originalConfig = original

	config := panel.GetConfig()

	if !config.Fork.InheritFromParent {
		t.Fatal("Fork.InheritFromParent should be preserved")
	}
	if config.Fork.Worktree == nil || *config.Fork.Worktree {
		t.Fatalf("Fork.Worktree should preserve explicit false, got %v", config.Fork.Worktree)
	}
	if config.Fork.WithState == nil || *config.Fork.WithState {
		t.Fatalf("Fork.WithState should preserve explicit false, got %v", config.Fork.WithState)
	}
	if config.Fork.WithIgnored == nil || *config.Fork.WithIgnored {
		t.Fatalf("Fork.WithIgnored should preserve explicit false, got %v", config.Fork.WithIgnored)
	}
	if config.Fork.Docker == nil || *config.Fork.Docker != "off" {
		t.Fatalf("Fork.Docker should preserve off, got %v", config.Fork.Docker)
	}
	if config.Fork.BranchPrefix != "wip/" {
		t.Fatalf("Fork.BranchPrefix = %q, want %q", config.Fork.BranchPrefix, "wip/")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/ui/ -run 'TestSettingsPanel_Fork_GetConfigPreservesHiddenFields' -count=1`
Expected: FAIL — `config.Fork` is zero-valued because `GetConfig` does not copy it from `originalConfig`.

- [ ] **Step 3: Preserve Fork in `SettingsPanel.GetConfig`**

In `internal/ui/settings_panel.go`, inside the `if s.originalConfig != nil` hidden-section preservation block, add this beside `config.Worktree = s.originalConfig.Worktree` and `config.Tmux = s.originalConfig.Tmux`:

```go
			// Fork settings are not exposed in SettingsPanel; preserve the whole
			// [fork] table so saving visible settings cannot reset quick-fork defaults.
			config.Fork = s.originalConfig.Fork
```

- [ ] **Step 4: Run to verify it passes**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/ui/ -run 'TestSettingsPanel_Fork_GetConfigPreservesHiddenFields' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/settings_panel.go internal/ui/settings_panel_test.go
git commit -m "fix(settings): preserve fork defaults through settings panel config"
```

---

## Task 3: Extract `buildForkCmd` helper, refactor dialog to use it

**Files:**
- Modify: `internal/ui/home.go` (`handleForkDialogKey` enter-branch, lines ~8581-8621; add `buildForkCmd` near `forkSessionCmdWithOptions` ~9566)

This is mostly a refactor. The only surface addition is the `notice` plumbing that the spec requires for later graceful-degradation tasks; existing fork-dialog tests are the safety net.

- [ ] **Step 1: Add `sessionForkedMsg.notice` and success handling**

In `internal/ui/home.go`, extend `sessionForkedMsg` (line ~725):

```go
type sessionForkedMsg struct {
	instance *session.Instance
	sourceID string // ID of the source session that was forked (for cleanup)
	notice   string // non-fatal degradation notice shown after a successful fork
	err      error
}
```

In the `case sessionForkedMsg:` success branch, immediately before the existing `return h, h.fetchPreview(msg.instance, msg.instance.ID, -1)`:

```go
				if msg.notice != "" {
					h.setError(fmt.Errorf("%s", msg.notice))
				}
```

Update `forkSessionCmd` to pass an empty notice once `forkSessionCmdWithOptions` gains the extra parameter in Step 2:

```go
func (h *Home) forkSessionCmd(source *session.Instance, title, groupPath, parentSessionID, parentProjectPath string) tea.Cmd {
	return h.forkSessionCmdWithOptions(source, title, groupPath, nil, false, git.WorktreeStateOptions{}, parentSessionID, parentProjectPath, "")
}
```

- [ ] **Step 2: Add the `buildForkCmd` helper**

In `internal/ui/home.go`, immediately before `forkSessionCmdWithOptions` (line ~9566):

```go
type forkBuildResult struct {
	cmd             tea.Cmd
	worktreeApplied bool
	notice          string
	errMsg          string
}

// buildForkCmd resolves the worktree target (when requested + git-capable),
// populates the worktree fields on opts, builds WorktreeStateOptions, and
// returns the async fork command plus any non-fatal success notice. Shared by
// the dialog (Shift+F) and quick fork (f). Fatal validation text is returned to
// the caller so Shift+F can keep using ForkDialog.SetError while quick fork uses
// Home.setError. explicitWorktree is forwarded to resolveWorktreeTarget's #1185
// fallback gate.
func (h *Home) buildForkCmd(
	source *session.Instance,
	title, groupPath, branchName string,
	worktreeEnabled, withState, withIgnored, sandboxEnabled, explicitWorktree bool,
	opts *session.ClaudeOptions,
	parentSessionID, parentProjectPath string,
) forkBuildResult {
	worktreeApplied := false
	notice := ""
	if worktreeEnabled && branchName != "" {
		worktreePath, repoRoot, fallback, errMsg := resolveWorktreeTarget(source.ProjectPath, branchName, explicitWorktree)
		if errMsg != "" {
			return forkBuildResult{errMsg: errMsg}
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
		} else {
			notice = "forked without worktree: not a git repo"
		}
	}
	forkState := git.WorktreeStateOptions{WithState: withState, WithIgnored: withIgnored}
	if !worktreeApplied {
		// State materialization requires a freshly created worktree.
		forkState = git.WorktreeStateOptions{}
	}
	return forkBuildResult{
		cmd:             h.forkSessionCmdWithOptions(source, title, groupPath, opts, sandboxEnabled, forkState, parentSessionID, parentProjectPath, notice),
		worktreeApplied: worktreeApplied,
		notice:          notice,
	}
}
```

- [ ] **Step 3: Refactor `forkSessionCmdWithOptions` to carry notices and async Docker fallback**

Change the signature to add `notice string`:

```go
func (h *Home) forkSessionCmdWithOptions(
	source *session.Instance,
	title, groupPath string,
	opts *session.ClaudeOptions,
	sandboxEnabled bool,
	forkState git.WorktreeStateOptions,
	parentSessionID, parentProjectPath string,
	notice string,
) tea.Cmd {
```

Inside the returned `tea.Cmd`, before worktree creation, degrade Docker if requested but unavailable. This check runs inside the async command so the TUI does not block on Docker daemon probing:

```go
			effectiveSandbox := sandboxEnabled
			forkNotice := notice
			if effectiveSandbox {
				if err := docker.CheckAvailability(context.Background()); err != nil {
					effectiveSandbox = false
					forkNotice = joinForkNotices(forkNotice, "forked without Docker: not available")
				}
			}
```

Then pass `effectiveSandbox` to `completeFork`, and include `forkNotice` in the success message:

```go
			inst, err := completeFork(source, title, groupPath, opts, effectiveSandbox, parentSessionID, parentProjectPath, withStateWorktreeCreated, defaultForkInstanceDeps())
			if err != nil {
				return sessionForkedMsg{err: err, sourceID: sourceID}
			}

			return sessionForkedMsg{instance: inst, sourceID: sourceID, notice: forkNotice}
```

Add this small helper near `buildForkCmd`:

```go
func joinForkNotices(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}
```

Ensure `internal/ui/home.go` imports `context` if it is not already present, plus `github.com/asheshgoplani/agent-deck/internal/docker`.

- [ ] **Step 4: Refactor `handleForkDialogKey` to call it**

In `internal/ui/home.go`, replace the enter-branch body (lines ~8596-8621, from the `if worktreeEnabled && branchName != ""` block through the direct `h.forkSessionCmdWithOptions` return) with:

```go
					parentID := h.forkDialog.GetParentSessionID()
					parentPath := h.forkDialog.GetParentProjectPath()
					result := h.buildForkCmd(
						source, title, groupPath, branchName,
						worktreeEnabled,
						h.forkDialog.IsWithStateEnabled(),
						h.forkDialog.IsWithStateAndGitignoredEnabled(),
						h.forkDialog.IsSandboxEnabled(),
						h.forkDialog.IsWorktreeExplicit(),
						opts,
						parentID, parentPath,
					)
					if result.errMsg != "" {
						h.forkDialog.SetError(result.errMsg)
						return h, nil
					}
					if result.cmd == nil {
						return h, nil
					}
					h.forkDialog.Hide()
					return h, result.cmd
```

(The surrounding selected-session framing and the `opts := h.forkDialog.GetOptions()` line remain unchanged.)

- [ ] **Step 5: Replace the obsolete source-introspection test**

The refactor routes the dialog submit through `buildForkCmd`, so the literal-string
assertions in `TestForkDialogSubmitCapturesWithStateBeforeHide`
(`internal/ui/fork_state_submit_test.go`) — which search for the standalone
`sandboxEnabled := h.forkDialog.IsSandboxEnabled()` statement and the 8-arg
`h.forkSessionCmdWithOptions(source, title, groupPath, opts, sandboxEnabled, forkState, parentID, parentPath)`
call — no longer match and will FAIL (`captureSandbox` and `call` both go to -1).
Replace that single test with one that matches the new structure (dialog state read
into `buildForkCmd` before `Hide()`):

```go
func TestForkDialogSubmitCapturesStateBeforeHide(t *testing.T) {
	srcBytes, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}
	src := string(srcBytes)

	// The dialog submit must read its toggle state (passed as args to
	// buildForkCmd) and dispatch the fork BEFORE Hide() resets the dialog.
	build := strings.Index(src, "result := h.buildForkCmd(")
	if build < 0 {
		t.Fatal("submit handler must dispatch through h.buildForkCmd")
	}
	after := src[build:]
	if !strings.Contains(after, "h.forkDialog.IsWithStateEnabled()") {
		t.Fatal("submit handler must pass dialog with-state into buildForkCmd")
	}
	if !strings.Contains(after, "h.forkDialog.IsSandboxEnabled()") {
		t.Fatal("submit handler must pass dialog sandbox into buildForkCmd")
	}
	if !strings.Contains(after, "h.forkDialog.Hide()") {
		t.Fatal("submit handler must Hide() after building the fork command")
	}
}
```

(The other introspection test in the file, `TestForkSessionCmdWithOptions_AcceptsForkState`,
still passes: `forkState git.WorktreeStateOptions` remains in the
`forkSessionCmdWithOptions` signature and `git.WorktreeStateOptions{}` remains in
`buildForkCmd`'s `!worktreeApplied` fallback.)

- [ ] **Step 6: Build + run the fork tests**

Run: `export GOTOOLCHAIN=go1.25.11 && go build ./... && go test ./internal/ui/ -run 'Fork' -count=1`
Expected: PASS (refactor is behavior-preserving; the replaced introspection test now matches the new structure).

- [ ] **Step 7: Commit**

```bash
git add internal/ui/home.go internal/ui/fork_state_submit_test.go
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

func TestQuickForkInputs_BranchSlugUsesGitSanitizer(t *testing.T) {
	src := session.NewInstanceWithTool("Fix: Bug? 101", "/tmp/proj", "claude")
	in := quickForkInputs(src, session.ForkSettings{}, false)
	assert.Equal(t, "fork/fix-bug-101", in.Branch)
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
	slug := git.SanitizeBranchName(strings.ToLower(strings.TrimSpace(source.Title)))
	if slug == "" {
		slug = "fork"
	}
	return quickForkSpec{
		Title:     source.Title + " (fork)",
		GroupPath: source.GroupPath,
		Branch:    fork.GetBranchPrefix() + slug,
		Plan:      fork.Resolve(parentSandboxed),
	}
}

// quickForkSession performs a comprehensive quick fork: new worktree+branch,
// carry tracked+gitignored state, match parent Docker, inherit the parent's
// Claude launch options for Claude-compatible sessions, and keep sibling
// placement. Defaults come from [fork] config (comprehensive when unset).
// Non-fatal degradation notices are reported after the async fork succeeds.
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

	// Inherit the parent's persisted Claude launch options (transient worktree
	// fields are json:"-" so they are never carried over). nil falls back to
	// global config downstream, as before.
	opts := source.GetClaudeOptions()

	result := h.buildForkCmd(
		source, in.Title, in.GroupPath, in.Branch,
		in.Plan.Worktree, in.Plan.WithState, in.Plan.WithIgnored, in.Plan.Sandbox,
		false, // quick fork worktree is config-default, not an explicit toggle (#1185)
		opts,
		source.ParentSessionID, source.ParentProjectPath,
	)
	if result.errMsg != "" {
		h.setError(fmt.Errorf("%s", result.errMsg))
		return nil
	}
	return result.cmd
}
```

Then **delete the now-orphaned `forkSessionCmd` wrapper** (the ~3-line method near
`home.go:9560` that Task 3 Step 1 updated to pass `""`). Its only caller was the old
`quickForkSession`; after this rewrite it has zero callers. The repo's golangci-lint
`unused` linter is active by default (`.golangci.yml` sets no `linters.default: none`),
so leaving a dead unexported method fails the `golangci-lint` CI job (U1000). Removing
it is safe: `TestForkSessionCmdWithOptions_AcceptsForkState` still finds
`git.WorktreeStateOptions{}` in `buildForkCmd`'s fallback branch, and no other code or
test calls `forkSessionCmd`.

- [ ] **Step 4: Add imports**

Ensure `internal/ui/home.go` imports `github.com/asheshgoplani/agent-deck/internal/git` if it is not already present. `strings`, `fmt`, and `session` are already imported; `docker` and `context` were handled in Task 3.

- [ ] **Step 5: Run tests + build**

Run: `export GOTOOLCHAIN=go1.25.11 && go build ./... && go test ./internal/ui/ -run 'TestQuickForkInputs' -count=1`
Expected: PASS (4 tests).

- [ ] **Step 6: Commit**

```bash
git add internal/ui/home.go internal/ui/fork_quick_test.go
git commit -m "feat(tui): comprehensive quick fork (worktree+state+docker+opts inherit)"
```

---

## Task 4A: Apply resolved worktree targets to OpenCode forks

**Files:**
- Modify: `internal/session/instance.go` (`ForkOpenCodeWithOptions`, `CreateForkedOpenCodeInstanceWithOptions`)
- Modify: `internal/ui/home.go` (`defaultForkInstanceDeps`)
- Test: `internal/ui/fork_quick_test.go` (append, or create `internal/ui/fork_opencode_test.go`)

The shared worktree helper uses `session.ClaudeOptions` as the existing transient carrier for `WorkDir`/`WorktreePath`/`WorktreeRepoRoot`/`WorktreeBranch`. Pi already consumes those fields through `CreateForkedPiInstanceWithOptions`; OpenCode currently ignores them, so a comprehensive OpenCode fork can create a worktree and then start the child in the original project path. This task closes that codebase mismatch.

- [ ] **Step 1: Write the failing OpenCode workdir test**

Append to `internal/ui/fork_quick_test.go`:

```go
func TestForkInstanceDeps_OpenCodeUsesResolvedWorktreeDir(t *testing.T) {
	source := session.NewInstanceWithTool("oc parent", "/tmp/original", "opencode")
	source.OpenCodeSessionID = "ses_parent"
	source.OpenCodeDetectedAt = time.Now()

	opts := &session.ClaudeOptions{
		WorkDir:          "/tmp/original-wt",
		WorktreePath:     "/tmp/original-wt",
		WorktreeRepoRoot: "/tmp/original",
		WorktreeBranch:   "fork/oc-parent",
	}

	// Exercise the deps.createInstance wiring directly — this is the exact seam
	// Step 4 changes. Calling createInstance (not completeFork) keeps the test
	// lean: no DetectOpenCodeSession goroutine and no start/multi-repo machinery.
	// writeOpenCodeForkScript writes via os.CreateTemp, which works under any HOME.
	deps := defaultForkInstanceDeps()
	inst, err := deps.createInstance(source, "oc parent (fork)", "", opts)
	if err != nil {
		t.Fatalf("createInstance: %v", err)
	}
	if inst.ProjectPath != "/tmp/original-wt" {
		t.Fatalf("OpenCode fork ProjectPath = %q, want resolved worktree dir", inst.ProjectPath)
	}
	if inst.WorktreePath != "/tmp/original-wt" || inst.WorktreeRepoRoot != "/tmp/original" || inst.WorktreeBranch != "fork/oc-parent" {
		t.Fatalf("OpenCode fork worktree metadata not copied: %+v", inst)
	}
}
```

Add `time` to the test imports.

- [ ] **Step 2: Run to verify it fails**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/ui/ -run 'TestForkInstanceDeps_OpenCodeUsesResolvedWorktreeDir' -count=1`
Expected: FAIL — the OpenCode branch ignores `opts.WorkDir`, so the fork's `ProjectPath == "/tmp/original"` with no worktree metadata.

- [ ] **Step 3: Add OpenCode workdir-aware fork helpers**

In `internal/session/instance.go`, refactor `ForkOpenCodeWithOptions` through a private workdir-aware helper:

```go
func (i *Instance) ForkOpenCodeWithOptions(newTitle, newGroupPath string, opts *OpenCodeOptions) (string, error) {
	return i.forkOpenCodeWithOptionsInWorkDir(newTitle, newGroupPath, opts, i.ProjectPath)
}

func (i *Instance) forkOpenCodeWithOptionsInWorkDir(newTitle, newGroupPath string, opts *OpenCodeOptions, workDir string) (string, error) {
	if !i.CanForkOpenCode() {
		return "", fmt.Errorf("cannot fork: no active OpenCode session")
	}
	if strings.TrimSpace(workDir) == "" {
		workDir = i.ProjectPath
	}

	envPrefix := i.buildEnvSourceCommand()

	var extraFlags string
	if opts != nil {
		for _, arg := range opts.ToArgsForFork() {
			extraFlags += " " + arg
		}
	} else if config, err := LoadUserConfig(); err == nil && config != nil {
		defaultOpts := NewOpenCodeOptions(config)
		for _, arg := range defaultOpts.ToArgsForFork() {
			extraFlags += " " + arg
		}
	}

	scriptPath, err := i.writeOpenCodeForkScript(workDir, envPrefix, extraFlags)
	if err != nil {
		return "", fmt.Errorf("failed to create fork script: %w", err)
	}

	return fmt.Sprintf("bash '%s'", scriptPath), nil
}
```

The helper code above intentionally keeps the existing `extraFlags` construction and `writeOpenCodeForkScript(workDir, envPrefix, extraFlags)` call intact; only the working directory becomes a parameter.

Then add a workdir-aware create helper while preserving the existing public method:

```go
func (i *Instance) CreateForkedOpenCodeInstanceWithOptions(
	newTitle, newGroupPath string,
	opts *OpenCodeOptions,
) (*Instance, string, error) {
	return i.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(newTitle, newGroupPath, opts, i.ProjectPath, "", "")
}

func (i *Instance) CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(
	newTitle, newGroupPath string,
	opts *OpenCodeOptions,
	workDir, worktreeRepoRoot, worktreeBranch string,
) (*Instance, string, error) {
	if strings.TrimSpace(workDir) == "" {
		workDir = i.ProjectPath
	}
	cmd, err := i.forkOpenCodeWithOptionsInWorkDir(newTitle, newGroupPath, opts, workDir)
	if err != nil {
		return nil, "", err
	}

	forked := NewInstance(newTitle, workDir)
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	forked.Command = cmd
	forked.Tool = "opencode"
	if worktreeRepoRoot != "" {
		forked.WorktreePath = workDir
		forked.WorktreeRepoRoot = worktreeRepoRoot
		forked.WorktreeBranch = worktreeBranch
	}
	if opts != nil {
		if err := forked.SetOpenCodeOptions(opts); err != nil {
			sessionLog.Warn("set_opencode_options_failed", slog.String("error", err.Error()))
		}
	}
	return forked, cmd, nil
}
```

- [ ] **Step 4: Wire `defaultForkInstanceDeps` for OpenCode**

In `internal/ui/home.go`, change the OpenCode branch in `defaultForkInstanceDeps.createInstance`:

```go
			case "opencode":
				workDir := source.ProjectPath
				repoRoot := ""
				branch := ""
				if opts != nil && opts.WorkDir != "" {
					workDir = opts.WorkDir
					repoRoot = opts.WorktreeRepoRoot
					branch = opts.WorktreeBranch
				}
				inst, _, err = source.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(title, groupPath, nil, workDir, repoRoot, branch)
```

- [ ] **Step 5: Run to verify it passes**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/ui/ -run 'TestForkInstanceDeps_OpenCodeUsesResolvedWorktreeDir' -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/session/instance.go internal/ui/home.go internal/ui/fork_quick_test.go
git commit -m "fix(tui): apply quick-fork worktree target to opencode forks"
```

---

## Task 5: `ForkDialog.Show` seeds from `[fork]` defaults

**Files:**
- Modify: `internal/ui/forkdialog.go` (`Show`, lines ~201-229)
- Modify: `internal/ui/home.go` (`forkSessionWithDialog`, line ~9398)
- Test: `internal/ui/forkdialog_test.go` (append, or create `internal/ui/forkdialog_fork_defaults_test.go`)

- [ ] **Step 1: Write the failing test**

Create `internal/ui/forkdialog_fork_defaults_test.go`:

```go
package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/stretchr/testify/assert"
)

func forkDefaultsGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	repo := filepath.Join(home, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return repo
}

// With no [fork] config present, the dialog opens reflecting the comprehensive
// built-in defaults: with-state ON and (in a git repo) gitignored ON.
func TestForkDialog_Show_SeedsComprehensiveWithStateDefault(t *testing.T) {
	repo := forkDefaultsGitRepo(t)

	d := NewForkDialog()
	d.ShowWithParentSandboxed("My Session", repo, "grp", nil, "", false)

	assert.True(t, d.IsWorktreeEnabled(), "worktree seeded ON in a git repo")
	assert.True(t, d.IsWithStateEnabled(), "with_state seeded ON from [fork] comprehensive default")
	assert.True(t, d.IsWithStateAndGitignoredEnabled(), "with_ignored seeded ON from [fork] comprehensive default")
}

func TestForkDialog_Show_DockerAutoMatchesSandboxedParent(t *testing.T) {
	repo := forkDefaultsGitRepo(t)

	d := NewForkDialog()
	d.ShowWithParentSandboxed("My Session", repo, "grp", nil, "", true)

	assert.True(t, d.IsSandboxEnabled(), "docker=auto should seed ON for sandboxed parent")
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/ui/ -run 'TestForkDialog_Show_SeedsComprehensive' -count=1`
Expected: FAIL — `with_state` currently seeded OFF (`d.withStateEnabled = false`).

- [ ] **Step 3: Replace the obsolete old-default test**

In `internal/ui/forkdialog_test.go`, replace `TestForkDialog_WithState_DefaultsFalseAfterShow` with a non-git invariant test so the suite no longer asserts the old default:

```go
func TestForkDialog_WithState_HiddenWhenWorktreeUnavailable(t *testing.T) {
	d := NewForkDialog()
	d.Show("Test", t.TempDir(), "group", nil, "")
	if d.IsWorktreeEnabled() {
		t.Fatal("worktree should be disabled when the project is not git-capable")
	}
	if d.IsWithStateEnabled() {
		t.Error("with-state should stay off when no worktree can be created")
	}
	if d.IsWithStateAndGitignoredEnabled() {
		t.Error("gitignored should stay off when no worktree can be created")
	}
}
```

- [ ] **Step 4: Seed the dialog from `[fork]` and parent sandbox state**

In `internal/ui/forkdialog.go`, rename the current `Show` body to `ShowWithParentSandboxed` with the same arguments plus `parentSandboxed bool`. Then add this compatibility wrapper above it:

```go
func (d *ForkDialog) Show(originalName, projectPath, groupPath string, conductors []*session.Instance, suggestedParentID string) {
	d.ShowWithParentSandboxed(originalName, projectPath, groupPath, conductors, suggestedParentID, false)
}
```

Inside the renamed method body, replace the config-defaults block (lines ~224-229, the `if config, err := session.LoadUserConfig(); err == nil` block) with:

```go
		// Initialize options + structural toggles from [fork] defaults so the dialog
		// opens "comprehensive, tweak down" — matching quick fork (f).
		if config, err := session.LoadUserConfig(); err == nil {
			d.optionsPanel.SetDefaults(config)
			plan := config.Fork.Resolve(parentSandboxed)
			d.worktreeEnabled = d.worktreeCapable && plan.Worktree
			d.withStateEnabled = d.worktreeEnabled && plan.WithState
			d.withStateAndGitignored = d.withStateEnabled && plan.WithIgnored
			d.sandboxEnabled = plan.Sandbox
		}
```

(The earlier unconditional resets at lines ~202-206 remain; this block overrides them when config loads. `withStateAndGitignored` stays gated on `withStateEnabled` to preserve the nesting invariant.)

Also, in the same `Show`/`ShowWithParentSandboxed` body, align the branch auto-suggest
(currently `forkdialog.go:220-222`) with quick fork's sanitizer so both paths produce
the same valid branch name. Replace:

```go
	// Auto-suggest branch name based on fork title
	sanitized := strings.ToLower(originalName)
	sanitized = strings.ReplaceAll(sanitized, " ", "-")
	d.branchInput.SetValue("fork/" + sanitized)
```

with:

```go
	// Auto-suggest branch name based on fork title. Use the git sanitizer (same
	// as quick fork's quickForkInputs) so titles with ':' '?' etc. don't produce
	// an invalid branch like "fork/fix:-bug".
	d.branchInput.SetValue("fork/" + git.SanitizeBranchName(strings.ToLower(originalName)))
```

(`git` is already imported in `forkdialog.go` — `Show` calls `git.IsGitRepoOrBareProjectRoot`.)

- [ ] **Step 5: Pass source sandbox state from production call site**

In `internal/ui/home.go`, update `forkSessionWithDialog`:

```go
	h.forkDialog.ShowWithParentSandboxed(source.Title, source.ProjectPath, source.GroupPath, conductors, suggestedParentID, source.IsSandboxed())
```

- [ ] **Step 6: Run the test + existing dialog tests**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/ui/ -run 'ForkDialog' -count=1`
Expected: PASS (new test + existing dialog tests).

- [ ] **Step 7: Commit**

```bash
git add internal/ui/forkdialog.go internal/ui/home.go internal/ui/forkdialog_test.go internal/ui/forkdialog_fork_defaults_test.go
git commit -m "feat(tui): seed ForkDialog from [fork] comprehensive defaults"
```

---

## Task 6: Eval-smoke case (tracked eval-harness mandate)

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

# Cross-tool fork parity (OpenCode, Codex, Pi)

> Goal: every locally-forkable tool reaches the same end-to-end fork coverage as
> Claude. Pi is already code-complete (TUI + CLI) — it only needs an eval. OpenCode
> needs CLI `session fork` support (Task 8). Codex has **no** fork support and is
> built from scratch (Task 9). All three get real-binary evals (Task 10). Mutator
> fields (Task 7) unblock the evals.

**Web scope clarification:** Web/API fork is plain cross-tool native fork parity
for this branch. It should share the session-layer tool dispatch and WebUI
should render a fork action only for sessions the backend marks forkable.
Worktree/state/Docker defaults remain TUI quick/dialog scope and are not added
to Web in this remediation.

## Task 7: `session set opencode-session-id` / `codex-session-id` mutator fields

**Files:**
- Modify: `internal/session/mutators.go` (field consts ~26, `ValidMutableFields` ~36, `SetField` switch ~229)
- Test: `internal/session/mutators_test.go` (append)

These let users (and the Task 10 evals) satisfy `CanForkOpenCode`/`CanForkCodex` the
same way `claude-session-id` satisfies the Claude path, stamping the matching
`*DetectedAt`.

- [ ] **Step 1: Write the failing test**

Append to `internal/session/mutators_test.go`:

```go
func TestSetField_OpenCodeSessionID_StampsDetectedAt(t *testing.T) {
	inst := NewInstanceWithTool("oc", "/tmp/p", "opencode")
	if _, _, err := SetField(inst, FieldOpenCodeSessionID, "ses_abc", nil); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if inst.OpenCodeSessionID != "ses_abc" {
		t.Fatalf("OpenCodeSessionID = %q, want ses_abc", inst.OpenCodeSessionID)
	}
	if inst.OpenCodeDetectedAt.IsZero() {
		t.Fatal("OpenCodeDetectedAt must be stamped so CanForkOpenCode's recency gate passes")
	}
}

func TestSetField_CodexSessionID_StampsDetectedAt(t *testing.T) {
	inst := NewInstanceWithTool("cx", "/tmp/p", "codex")
	if _, _, err := SetField(inst, FieldCodexSessionID, "11111111-2222-3333-4444-555555555555", nil); err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if inst.CodexSessionID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("CodexSessionID = %q", inst.CodexSessionID)
	}
	if inst.CodexDetectedAt.IsZero() {
		t.Fatal("CodexDetectedAt must be stamped")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/session/ -run 'TestSetField_(OpenCode|Codex)SessionID' -count=1`
Expected: FAIL — `FieldOpenCodeSessionID` / `FieldCodexSessionID` undefined.

- [ ] **Step 3: Add the field constants + register them**

In `internal/session/mutators.go`, add to the const block (after `FieldGeminiSessionID` at ~27):

```go
	FieldOpenCodeSessionID  = "opencode-session-id"
	FieldCodexSessionID     = "codex-session-id"
```

Add both to `ValidMutableFields` (after `FieldGeminiSessionID` at ~48):

```go
	FieldOpenCodeSessionID,
	FieldCodexSessionID,
```

- [ ] **Step 4: Add the `SetField` cases**

In `internal/session/mutators.go`, after the `case FieldGeminiSessionID:` block (~249):

```go
	case FieldOpenCodeSessionID:
		oldValue = inst.OpenCodeSessionID
		inst.OpenCodeSessionID = value
		inst.OpenCodeDetectedAt = time.Now()

	case FieldCodexSessionID:
		oldValue = inst.CodexSessionID
		inst.CodexSessionID = value
		inst.CodexDetectedAt = time.Now()
```

- [ ] **Step 5: Run to verify it passes**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/session/ -run 'TestSetField_(OpenCode|Codex)SessionID' -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/session/mutators.go internal/session/mutators_test.go
git commit -m "feat(session): add opencode-session-id + codex-session-id mutator fields"
```

---

## Task 8: CLI `session fork` parity for OpenCode

**Files:**
- Modify: `cmd/agent-deck/session_cmd.go` (`handleSessionFork`: tool gate ~685-693, dispatch ~927-932)
- Test: `cmd/agent-deck/session_cmd_fork_state_test.go` (append a wiring assertion)

The CLI currently rejects opencode (`isClaudeFork || isPiFork` gate at `session_cmd.go:687`)
and has no opencode dispatch branch. Add both, reusing Task 4A's
`CreateForkedOpenCodeInstanceWithOptionsAndWorkDir`. The worktree/with-state machinery
above the dispatch is tool-agnostic (operates on git), so it already works for opencode.

- [ ] **Step 1: Write the failing wiring test**

Append to `cmd/agent-deck/session_cmd_fork_state_test.go` (these source-introspection tests
match the file's existing style):

```go
func TestSessionFork_AdmitsOpenCode(t *testing.T) {
	src, err := os.ReadFile("session_cmd.go")
	if err != nil {
		t.Fatalf("read session_cmd.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, `isOpenCodeFork := inst.Tool == "opencode"`) {
		t.Fatal("fork gate must recognize opencode")
	}
	if !strings.Contains(s, "CreateForkedOpenCodeInstanceWithOptionsAndWorkDir") {
		t.Fatal("fork dispatch must route opencode through the worktree-aware create method")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./cmd/agent-deck/ -run 'TestSessionFork_AdmitsOpenCode' -count=1`
Expected: FAIL — neither string present.

- [ ] **Step 3: Admit opencode at the tool gate**

In `cmd/agent-deck/session_cmd.go`, replace the gate (lines ~685-693):

```go
	isClaudeFork := session.IsClaudeCompatible(inst.Tool)
	isPiFork := inst.Tool == "pi"
	isOpenCodeFork := inst.Tool == "opencode"
	if !isClaudeFork && !isPiFork && !isOpenCodeFork {
		out.Error(
			fmt.Sprintf("session '%s' is not a forkable session (tool: %s)", inst.Title, inst.Tool),
			ErrCodeInvalidOperation,
		)
		os.Exit(1)
	}
```

And generalize the `CanFork()` failure reason (lines ~701-711) so it is not Claude-specific:

```go
	if !inst.CanFork() {
		out.Error(
			fmt.Sprintf("session '%s' cannot be forked: no resumable session for tool %s", inst.Title, inst.Tool),
			ErrCodeInvalidOperation,
		)
		os.Exit(1)
	}
```

- [ ] **Step 4: Add the opencode dispatch branch**

In `cmd/agent-deck/session_cmd.go`, extend the create switch (lines ~927-932):

```go
	switch {
	case isPiFork:
		forkedInst, _, err = inst.CreateForkedPiInstanceWithOptions(forkTitle, forkGroup, opts)
	case isOpenCodeFork:
		workDir := inst.ProjectPath
		repoRoot := ""
		branch := ""
		if opts != nil && opts.WorkDir != "" {
			workDir = opts.WorkDir
			repoRoot = opts.WorktreeRepoRoot
			branch = opts.WorktreeBranch
		}
		forkedInst, _, err = inst.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(forkTitle, forkGroup, nil, workDir, repoRoot, branch)
	default:
		forkedInst, _, err = inst.CreateForkedInstanceWithOptions(forkTitle, forkGroup, opts)
	}
```

- [ ] **Step 5: Run + build**

Run: `export GOTOOLCHAIN=go1.25.11 && go build ./... && go test ./cmd/agent-deck/ -run 'TestSessionFork_AdmitsOpenCode' -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/agent-deck/session_cmd.go cmd/agent-deck/session_cmd_fork_state_test.go
git commit -m "feat(cli): session fork parity for opencode (worktree-aware)"
```

---

## Task 9: Codex-compatible forking (CanForkCodex + `codex fork <sid>` + dispatch)

**Files:**
- Modify: `internal/session/instance.go` (`CanFork` ~6056; add `CanForkCodex`, `buildCodexForkCommandForTarget`, `CreateForkedCodexInstanceWithOptions` near the Pi fork methods ~6390; add Codex fork-start guards in `Start` and `StartWithMessage`)
- Modify: `internal/ui/home.go` (`defaultForkInstanceDeps` switch ~9449)
- Modify: `cmd/agent-deck/session_cmd.go` (gate + dispatch from Task 8)
- Test: `internal/session/instance_codex_fork_test.go` (create)
- Test: `internal/session/fork_start_dispatch_test.go` (append a Codex fork-start structural assertion)

Codex forking mirrors the Pi pattern (deferred `ForkStartCommand` + `IsForkAwaitingStart`),
using the codex CLI's `codex fork <SESSION_ID>` primitive — analogous to the existing
`codex resume <sid>` builder (`instance.go:1374-1376`). **Version note:** `codex fork`
is present in the locally verified `codex-cli 0.137.0`; forkability is gated on a
flushed on-disk rollout (the same invariant `codex resume` uses), and if the installed
codex binary predates `fork`, the launched command fails and the session enters a
recoverable error state (no crash). Document `codex-cli 0.137.0` as the verified
support floor unless a lower version is independently verified during implementation.

- [ ] **Step 1: Write the failing tests**

Create `internal/session/instance_codex_fork_test.go`:

```go
package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
)

func seedCodexRollout(t *testing.T, codexHome, sid string) {
	t.Helper()
	dir := filepath.Join(codexHome, "sessions", "2026", "06", "06")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir rollout dir: %v", err)
	}
	p := filepath.Join(dir, "rollout-20260606T000000-"+sid+".jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}
}

func TestCanForkCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sid := "11111111-2222-3333-4444-555555555555"
	seedCodexRollout(t, home, sid)

	inst := NewInstanceWithTool("cx", "/tmp/p", "codex")
	inst.CodexSessionID = sid
	inst.CodexDetectedAt = time.Now()
	if !inst.CanForkCodex() {
		t.Fatal("codex session with an on-disk rollout must be forkable")
	}

	inst.CodexSessionID = "no-rollout-uuid"
	if inst.CanForkCodex() {
		t.Fatal("codex session without a rollout must NOT be forkable")
	}
}

func TestCreateForkedCodexInstance_UsesWorktreeAndForkCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	sid := "11111111-2222-3333-4444-555555555555"
	seedCodexRollout(t, home, sid)

	parent := NewInstanceWithTool("cx parent", "/tmp/original", "codex")
	parent.CodexSessionID = sid
	parent.CodexDetectedAt = time.Now()

	opts := &ClaudeOptions{
		WorkDir:          "/tmp/original-wt",
		WorktreePath:     "/tmp/original-wt",
		WorktreeRepoRoot: "/tmp/original",
		WorktreeBranch:   "fork/cx-parent",
	}
	forked, cmd, err := parent.CreateForkedCodexInstanceWithOptions("cx parent (fork)", "", opts)
	if err != nil {
		t.Fatalf("CreateForkedCodexInstanceWithOptions: %v", err)
	}
	if forked.ProjectPath != "/tmp/original-wt" {
		t.Fatalf("forked ProjectPath = %q, want worktree dir", forked.ProjectPath)
	}
	if forked.WorktreePath != "/tmp/original-wt" || forked.WorktreeBranch != "fork/cx-parent" {
		t.Fatalf("worktree metadata not copied: %+v", forked)
	}
	if !forked.IsForkAwaitingStart || forked.ForkStartCommand == "" {
		t.Fatal("codex fork must defer launch via ForkStartCommand/IsForkAwaitingStart (Pi pattern)")
	}
	if !strings.Contains(cmd, "fork "+sid) {
		t.Fatalf("fork command must run `codex fork <parent-sid>`; got: %s", cmd)
	}
}

func TestCreateForkedCodexInstance_UsesConfiguredCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	codexHome := filepath.Join(home, "codex work")
	cfg := &UserConfig{Codex: CodexSettings{Command: `CODEX_HOME="` + codexHome + `" codex`}}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	sid := "aaaaaaaa-2222-3333-4444-555555555555"
	seedCodexRollout(t, codexHome, sid)

	parent := NewInstanceWithTool("cx parent", "/tmp/original", "codex")
	parent.CodexSessionID = sid
	parent.CodexDetectedAt = time.Now()

	_, cmd, err := parent.CreateForkedCodexInstanceWithOptions("cx parent (fork)", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedCodexInstanceWithOptions: %v", err)
	}
	want := `CODEX_HOME="` + codexHome + `" codex fork ` + sid
	if !strings.Contains(cmd, want) {
		t.Fatalf("configured CODEX_HOME command must be preserved for fork; want %q in %q", want, cmd)
	}
}

func TestCreateForkedCodexInstance_QuotesConfiguredCodexConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	codexHome := filepath.Join(home, "codex config dir")
	cfg := &UserConfig{Codex: CodexSettings{ConfigDir: codexHome}}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	sid := "bbbbbbbb-2222-3333-4444-555555555555"
	seedCodexRollout(t, codexHome, sid)

	parent := NewInstanceWithTool("cx parent", "/tmp/original", "codex")
	parent.CodexSessionID = sid
	parent.CodexDetectedAt = time.Now()

	_, cmd, err := parent.CreateForkedCodexInstanceWithOptions("cx parent (fork)", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedCodexInstanceWithOptions: %v", err)
	}
	want := "CODEX_HOME=" + shellescape.Quote(codexHome) + " "
	if !strings.Contains(cmd, want) {
		t.Fatalf("configured [codex].config_dir must be shell-quoted for fork; want %q in %q", want, cmd)
	}
}

func TestCreateForkedCodexInstance_PreservesCompatibleToolIdentity(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	cfg := &UserConfig{
		Tools: map[string]ToolDef{
			"my-codex": {
				Command:        "codex-wrapper",
				CompatibleWith: "codex",
			},
		},
	}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()

	sid := "cccccccc-2222-3333-4444-555555555555"
	seedCodexRollout(t, codexHome, sid)

	parent := NewInstanceWithTool("cx parent", "/tmp/original", "my-codex")
	parent.Command = "codex-wrapper"
	parent.CodexSessionID = sid
	parent.CodexDetectedAt = time.Now()

	forked, cmd, err := parent.CreateForkedCodexInstanceWithOptions("cx parent (fork)", "", nil)
	if err != nil {
		t.Fatalf("CreateForkedCodexInstanceWithOptions: %v", err)
	}
	if forked.Tool != "my-codex" {
		t.Fatalf("forked Tool = %q, want custom Codex-compatible tool identity", forked.Tool)
	}
	if !strings.Contains(cmd, "AGENTDECK_TOOL=my-codex") {
		t.Fatalf("fork command must preserve AGENTDECK_TOOL identity; got %q", cmd)
	}
	if !strings.Contains(cmd, "codex-wrapper fork "+sid) {
		t.Fatalf("fork command must use the compatible tool command; got %q", cmd)
	}
}
```

Append to `internal/session/fork_start_dispatch_test.go`, adding `strings` to that file's imports:

```go
func TestCodexForkStartDispatchConsumesAwaitingStart(t *testing.T) {
	requireCodexForkStartGuard(t, "Start")
	requireCodexForkStartGuard(t, "StartWithMessage")
}

func requireCodexForkStartGuard(t *testing.T, funcName string) {
	t.Helper()
	body := extractFuncBodyInstance(funcName)
	if body == "" {
		t.Fatalf("%s body not found", funcName)
	}
	codexIdx := strings.Index(body, "case IsCodexCompatible(i.Tool):")
	if codexIdx < 0 {
		t.Fatalf("%s must have a Codex-compatible dispatch branch", funcName)
	}
	codexBody := body[codexIdx:]
	nextCase := strings.Index(codexBody[len("case IsCodexCompatible(i.Tool):"):], "\n\tcase ")
	if nextCase >= 0 {
		codexBody = codexBody[:len("case IsCodexCompatible(i.Tool):")+nextCase]
	}
	sentinelIdx := strings.Index(codexBody, "if i.IsForkAwaitingStart")
	buildIdx := strings.Index(codexBody, "i.buildCodexCommand")
	if sentinelIdx < 0 {
		t.Fatalf("%s Codex branch must consume IsForkAwaitingStart before normal resume/fresh dispatch", funcName)
	}
	if buildIdx < 0 {
		t.Fatalf("%s Codex branch must still call buildCodexCommand for normal starts", funcName)
	}
	if sentinelIdx > buildIdx {
		t.Fatalf("%s Codex branch must check IsForkAwaitingStart before buildCodexCommand", funcName)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `export GOTOOLCHAIN=go1.25.11 && go test ./internal/session/ -run 'TestCanForkCodex|TestCreateForkedCodexInstance|TestCodexForkStartDispatchConsumesAwaitingStart' -count=1`
Expected: FAIL — `CanForkCodex` / `CreateForkedCodexInstanceWithOptions` undefined, and the Codex `Start`/`StartWithMessage` branches do not yet consume `IsForkAwaitingStart`.

- [ ] **Step 3: Add `CanForkCodex` + the codex fork builder + create method**

In `internal/session/instance.go`, near the Pi fork methods (~6390), add:

```go
// CanForkCodex reports whether this Codex session can be forked. Forkability
// requires a flushed on-disk rollout for the captured session id — the same
// invariant buildCodexCommand uses to gate `codex resume` (#756). `codex fork`
// is a newer codex CLI subcommand; if the installed binary predates it the
// launched command fails into a recoverable error state.
func (i *Instance) CanForkCodex() bool {
	if !IsCodexCompatible(i.Tool) || i.CodexSessionID == "" {
		return false
	}
	return codexRolloutExistsInHome(i.CodexSessionID, i.getCodexHomeDir())
}

// buildCodexForkCommandForTarget builds the one-time `codex fork <parent-sid>`
// launch command for a forked codex instance. Mirrors buildCodexCommand's resume
// path (instance.go:1374) but uses `fork`, which clones the parent transcript into
// a new thread with a fresh id while leaving the parent intact.
func (i *Instance) buildCodexForkCommandForTarget(target *Instance, baseCommand string) (string, error) {
	if !i.CanForkCodex() {
		return "", fmt.Errorf("cannot fork: no resumable Codex session")
	}
	envPrefix := target.buildEnvSourceCommand()
	envPrefix += fmt.Sprintf("AGENTDECK_INSTANCE_ID=%s AGENTDECK_TITLE=%q AGENTDECK_TOOL=%s ",
		target.ID, target.Title, target.Tool)
	yoloFlag := target.resolveCodexYoloFlag()
	modelFlag := target.resolveCodexModelFlag()
	command := target.resolveCodexCommand(baseCommand)
	if isCodexHomeExplicit() {
		codexHome := strings.TrimSpace(getCodexHomeDir())
		if codexHome != "" {
			if err := os.MkdirAll(codexHome, 0o755); err != nil {
				sessionLog.Warn("codex_home_mkdir_failed",
					slog.String("path", codexHome),
					slog.String("error", err.Error()))
			}
			envPrefix += "CODEX_HOME=" + shellescape.Quote(codexHome) + " "
		}
	}
	return envPrefix + fmt.Sprintf("%s%s%s fork %s", command, yoloFlag, modelFlag, i.CodexSessionID), nil
}

// CreateForkedCodexInstanceWithOptions creates a forked Codex instance. Mirrors
// CreateForkedPiInstanceWithOptions: opts is the shared worktree carrier (only
// WorkDir/Worktree* consumed); launch is deferred via ForkStartCommand.
func (i *Instance) CreateForkedCodexInstanceWithOptions(
	newTitle, newGroupPath string,
	opts *ClaudeOptions,
) (*Instance, string, error) {
	projectPath := i.ProjectPath
	if opts != nil && opts.WorkDir != "" {
		projectPath = opts.WorkDir
	}

	forked := NewInstance(newTitle, projectPath)
	if newGroupPath != "" {
		forked.GroupPath = newGroupPath
	} else {
		forked.GroupPath = i.GroupPath
	}
	forked.Tool = i.Tool
	forked.Wrapper = i.Wrapper

	baseCommand := strings.TrimSpace(i.Command)
	if baseCommand == "" {
		baseCommand = "codex"
	}
	forked.Command = baseCommand

	cmd, err := i.buildCodexForkCommandForTarget(forked, baseCommand)
	if err != nil {
		return nil, "", err
	}
	forked.ForkStartCommand = cmd
	forked.IsForkAwaitingStart = true

	if opts != nil && opts.WorktreePath != "" {
		forked.WorktreePath = opts.WorktreePath
		forked.WorktreeRepoRoot = opts.WorktreeRepoRoot
		forked.WorktreeBranch = opts.WorktreeBranch
	}

	return forked, cmd, nil
}
```

- [ ] **Step 4: Add the Codex-compatible branch to `CanFork`**

In `internal/session/instance.go` `CanFork()` (~6056), add a Codex-compatible branch
beside the opencode/pi branches (before the Claude fallback):

```go
	if IsCodexCompatible(i.Tool) {
		return i.CanForkCodex()
	}
```

- [ ] **Step 5: Make Codex start paths consume the fork-start command**

In both `Instance.Start()` and `Instance.StartWithMessage()`, change the
`case IsCodexCompatible(i.Tool):` branch to consume `IsForkAwaitingStart` before
normal `buildCodexCommand` dispatch:

```go
		case IsCodexCompatible(i.Tool):
			if i.IsForkAwaitingStart {
				command = i.consumeForkStartCommand()
				sessionLog.Info("resume: none reason=fork_awaiting_start",
					slog.String("instance_id", i.ID),
					slog.String("path", i.ProjectPath),
					slog.String("reason", "fork_awaiting_start"))
				break
			}
			command = i.buildCodexCommand(i.Command)
			i.CodexStartedAt = time.Now().UnixMilli()
```

This mirrors the existing Claude and Pi branches. Without this guard,
`CreateForkedCodexInstanceWithOptions` can set `ForkStartCommand`, but the first
start would ignore it and run the normal `codex resume`/fresh command path.

- [ ] **Step 6: Wire the TUI + CLI dispatchers**

In `internal/ui/home.go` `defaultForkInstanceDeps` (`createInstance`), replace the
existing `switch source.Tool` block with a predicate switch so configured
Codex-compatible tools use the Codex fork path:

```go
				switch {
				case source.Tool == "opencode":
					workDir := source.ProjectPath
					repoRoot := ""
					branch := ""
					if opts != nil && opts.WorkDir != "" {
						workDir = opts.WorkDir
						repoRoot = opts.WorktreeRepoRoot
						branch = opts.WorktreeBranch
					}
					inst, _, err = source.CreateForkedOpenCodeInstanceWithOptionsAndWorkDir(title, groupPath, nil, workDir, repoRoot, branch)
				case source.Tool == "pi":
					inst, _, err = source.CreateForkedPiInstanceWithOptions(title, groupPath, opts)
				case session.IsCodexCompatible(source.Tool):
					inst, _, err = source.CreateForkedCodexInstanceWithOptions(title, groupPath, opts)
				default:
					inst, _, err = source.CreateForkedInstanceWithOptions(title, groupPath, opts)
				}
```

In `cmd/agent-deck/session_cmd.go`, extend the Task 8 gate and dispatch to include codex:
add `isCodexFork := session.IsCodexCompatible(inst.Tool)` to the gate condition
(`!isClaudeFork && !isPiFork && !isOpenCodeFork && !isCodexFork`), and add a dispatch case:

```go
		case isCodexFork:
			forkedInst, _, err = inst.CreateForkedCodexInstanceWithOptions(forkTitle, forkGroup, opts)
```

- [ ] **Step 7: Run tests + build**

Run: `export GOTOOLCHAIN=go1.25.11 && go build ./... && go test ./internal/session/ -run 'TestCanForkCodex|TestCreateForkedCodexInstance|TestCodexForkStartDispatchConsumesAwaitingStart' -count=1`
Expected: PASS.

- [ ] **Step 8: Docs sync (minimum codex version)**

Add a one-line note to `CHANGELOG.md` and the README fork section: "Codex forking
requires a codex CLI with `codex fork <session-id>` support; verified with
`codex-cli 0.137.0`." Keep it brief.

- [ ] **Step 9: Commit**

```bash
git add internal/session/instance.go internal/session/instance_codex_fork_test.go internal/ui/home.go cmd/agent-deck/session_cmd.go CHANGELOG.md README.md
git commit -m "feat: codex session forking (codex fork <sid>, worktree-aware, CLI+TUI)"
```

---

## Task 10: End-to-end fork evals — Pi, OpenCode, Codex

**Files:**
- Create: `tests/eval/session/fork_pi_test.go`, `tests/eval/session/fork_opencode_test.go`, `tests/eval/session/fork_codex_test.go`

Each mirrors `tests/eval/session/fork_with_state_test.go`: real `agent-deck` binary,
scratch HOME, real seeded git repo, then `session fork -w fork/<slug>`, asserting the
destination worktree exists on the right branch. Downstream `Start()` failing (no real
tool binary) is tolerated — worktree creation runs before Start, exactly as the Claude
eval documents. Reuse that file's helpers (`gitInit`, `gitMust`, `gitOut`, `worktreePathForBranch`,
`writeFile`, `runBin`, `runBinTry`) — they live in the same `session_test` package
(`fork_with_state_test.go` / `lifecycle_test.go`), so do **not** redefine them
(redefining `writeFile`/`gitOut` would be a duplicate-symbol compile error).

**Ordering dependency:** Task 10 MUST land after Tasks 7, 8, and 9 — the Pi case needs
nothing extra, but the OpenCode case requires the `opencode-session-id` setter (Task 7)
and the CLI gate (Task 8), and the Codex case requires the `codex-session-id` setter
(Task 7) and the codex CLI gate + fork builder (Task 9). Running Task 10 earlier fails:
the CLI rejects opencode/codex forks today.

- [ ] **Step 1: Pi fork eval**

Create `tests/eval/session/fork_pi_test.go`. Satisfy `CanForkPi` by seeding a session
JSONL under `~/.pi/agent-deck/<id>/` (no `session set` needed). This file also
defines the shared helpers used by the OpenCode and Codex evals:

```go
//go:build eval_smoke

package session_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestEval_SessionForkPi_RealBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	sb := harness.NewSandbox(t)
	writeForkConfig(t, sb) // [worktree] branch_prefix="" + sibling location

	repoDir := newForkEvalRepo(t, sb)

	// Register a Pi parent and capture its instance ID from --json.
	id := addJSONID(t, sb, "add", "-c", "pi", "-t", "parent", "-g", "evalgrp", "--json", repoDir)

	// Satisfy CanForkPi(): seed a session JSONL under ~/.pi/agent-deck/<id>/.
	piDir := filepath.Join(sb.Home, ".pi", "agent-deck", id)
	mustMkdir(t, piDir)
	writeFile(t, piDir, "session.jsonl", "{}\n")

	forkOut, forkErr := runBinTry(sb, "session", "fork", "parent", "-w", "fork/pi-eval", "-t", "fork-pi")

	assertForkWorktreeBranch(t, repoDir, "fork/pi-eval", forkOut, forkErr)
}

func newForkEvalRepo(t *testing.T, sb *harness.Sandbox) string {
	t.Helper()
	repoDir := filepath.Join(sb.Home, "proj")
	mustMkdir(t, repoDir)
	gitInit(t, repoDir)
	writeFile(t, repoDir, "README.md", "seed\n")
	gitMust(t, repoDir, "add", ".")
	gitMust(t, repoDir, "commit", "-m", "seed")
	return repoDir
}

func writeForkConfig(t *testing.T, sb *harness.Sandbox) {
	t.Helper()
	cfgDir := filepath.Join(sb.Home, ".agent-deck")
	mustMkdir(t, cfgDir)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(`[worktree]
branch_prefix = ""
default_location = "sibling"
`), 0o600); err != nil {
		t.Fatalf("write fork eval config: %v", err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func addJSONID(t *testing.T, sb *harness.Sandbox, args ...string) string {
	t.Helper()
	out, err := runBinTry(sb, args...)
	if err != nil {
		t.Fatalf("agent-deck %v: %v\n%s", args, err, out)
	}
	// `add --json` emits pretty-printed (json.MarshalIndent) MULTI-LINE JSON, so a
	// per-line "{...}" scan never matches. Slice the whole output from the first
	// '{' to the last '}' and unmarshal that. (Empirically verified: the older
	// line-by-line scanner t.Fatalf'd on every invocation.)
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end < start {
		t.Fatalf("agent-deck %v emitted no JSON object; output:\n%s", args, out)
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out[start:end+1]), &payload); err != nil {
		t.Fatalf("agent-deck %v: parse JSON %q: %v", args, out[start:end+1], err)
	}
	if payload.ID == "" {
		t.Fatalf("agent-deck %v JSON has empty id; output:\n%s", args, out)
	}
	return payload.ID
}

func assertForkWorktreeBranch(t *testing.T, repoDir, branch, forkOut string, forkErr error) {
	t.Helper()
	forkPath := worktreePathForBranch(t, repoDir, branch)
	if forkPath == "" {
		t.Fatalf("destination worktree for %s not found.\nerr: %v\noutput:\n%s", branch, forkErr, forkOut)
	}
	gotBranch := strings.TrimSpace(gitOut(t, forkPath, "rev-parse", "--abbrev-ref", "HEAD"))
	if gotBranch != branch {
		t.Errorf("destination branch = %q, want %s", gotBranch, branch)
	}
}

func codexHomeForSandbox(sb *harness.Sandbox) string {
	for _, kv := range sb.Env() {
		if strings.HasPrefix(kv, "CODEX_HOME=") {
			if v := strings.TrimSpace(strings.TrimPrefix(kv, "CODEX_HOME=")); v != "" {
				return v
			}
		}
	}
	return filepath.Join(sb.Home, ".codex")
}
```

- [ ] **Step 2: OpenCode fork eval**

Create `tests/eval/session/fork_opencode_test.go`. The parent is `add -c opencode`
and `CanForkOpenCode` is satisfied via the Task 7 setter:

```go
//go:build eval_smoke

package session_test

import (
	"os/exec"
	"testing"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestEval_SessionForkOpenCode_RealBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	sb := harness.NewSandbox(t)
	writeForkConfig(t, sb)
	repoDir := newForkEvalRepo(t, sb)

	_ = addJSONID(t, sb, "add", "-c", "opencode", "-t", "parent", "-g", "evalgrp", "--json", repoDir)
	runBin(t, sb, "session", "set", "parent", "opencode-session-id", "ses_evalparent")

	forkOut, forkErr := runBinTry(sb, "session", "fork", "parent", "-w", "fork/oc-eval", "-t", "fork-oc")
	assertForkWorktreeBranch(t, repoDir, "fork/oc-eval", forkOut, forkErr)
}
```

- [ ] **Step 3: Codex fork eval**

Create `tests/eval/session/fork_codex_test.go` — parent is `add -c codex`; satisfy
`CanForkCodex` by setting `codex-session-id` (Task 7) and seeding a codex rollout
under the same home that `getCodexHomeDir` resolves for the sandbox. The harness
currently does not set `CODEX_HOME`, so this resolves to `<sb.Home>/.codex`; the
helper still honors a future `CODEX_HOME=` entry in `sb.Env()`.

```go
//go:build eval_smoke

package session_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestEval_SessionForkCodex_RealBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	sb := harness.NewSandbox(t)
	writeForkConfig(t, sb)
	repoDir := newForkEvalRepo(t, sb)

	sid := "11111111-2222-3333-4444-555555555555"
	_ = addJSONID(t, sb, "add", "-c", "codex", "-t", "parent", "-g", "evalgrp", "--json", repoDir)
	runBin(t, sb, "session", "set", "parent", "codex-session-id", sid)

	// Seed a rollout so CanForkCodex() passes.
	rollDir := filepath.Join(codexHomeForSandbox(sb), "sessions", "2026", "06", "06")
	mustMkdir(t, rollDir)
	writeFile(t, rollDir, "rollout-20260606T000000-"+sid+".jsonl", "{}\n")

	forkOut, forkErr := runBinTry(sb, "session", "fork", "parent", "-w", "fork/cx-eval", "-t", "fork-cx")
	assertForkWorktreeBranch(t, repoDir, "fork/cx-eval", forkOut, forkErr)
}
```

- [ ] **Step 4: Run the eval suite**

Run: `export GOTOOLCHAIN=go1.25.11 && go test -tags eval_smoke ./tests/eval/session/... -run 'TestEval_SessionFork(Pi|OpenCode|Codex)' -count=1`
Expected: PASS (or `SKIP` where git is unavailable).

- [ ] **Step 5: Commit**

```bash
git add tests/eval/session/fork_pi_test.go tests/eval/session/fork_opencode_test.go tests/eval/session/fork_codex_test.go
git commit -m "test(eval): end-to-end fork coverage for pi, opencode, codex"
```

---

## Final Verification

- [ ] **Full mandated suites + build:**

```bash
export GOTOOLCHAIN=go1.25.11
go build ./...
go test -run TestPersistence_ ./internal/session/... -race -count=1
go test ./internal/session/... -run 'Fork|TestForkSettings|TestCanForkCodex|TestCreateForkedCodex|TestSetField_(OpenCode|Codex)SessionID' -race -count=1
go test ./internal/ui/... -run 'Fork|Watcher|SettingsPanel_Fork|TestQuickForkInputs|TestForkInstanceDeps' -race -count=1
go test ./cmd/agent-deck/... -run 'SessionFork' -race -count=1
go test -tags eval_smoke ./tests/eval/... ./internal/ui/...
```

Expected: all PASS (eval cases SKIP where git is unavailable). Persistence suite is green per the macOS fixture fix already committed.

- [ ] **Manual smoke (optional, real TUI):** launch agent-deck on a git project, press `f` on a Claude session, verify a `(fork)` session appears in a new worktree on a `fork/<slug>` branch; on a non-git dir verify the "forked without worktree" notice and a plain fork.

---

## Spec ↔ Plan coverage

| Spec item | Task |
|---|---|
| `[fork]` section, bare keys, `*bool` nil=ON, `GetDocker` like `GetLaunchAs` | 1 |
| Precedence: `[fork]` wins → comprehensive; globals ignored | 1, 2 |
| `inherit_from_parent` mapping; `with_ignored` implies `with_state` | 2 |
| SettingsPanel preserves hidden `[fork]` config | 2A |
| Docker `auto`/`on`/`off`, auto = `IsSandboxed()` | 2, 4, 5 |
| Worktree+state+gitignored ON by default | 1, 4 |
| Shared helper extraction + `sessionForkedMsg.notice` (+ replace obsolete introspection test) | 3 |
| Comprehensive `f`: sanitized branch, opts inherit, sibling placement; remove orphaned `forkSessionCmd` | 4 |
| OpenCode receives resolved worktree target (tested via `deps.createInstance`) | 4A |
| Graceful degradation + brief notice through success message | 3, 4 |
| Dialog seeded from `[fork]` with parent sandbox context; branch sanitizer aligned with quick fork | 5 |
| Eval case (user-observable mandate) | 6 |
| `opencode-session-id` / `codex-session-id` setter fields | 7 |
| CLI `session fork` parity for OpenCode | 8 |
| Codex forking (`codex fork <sid>`, worktree-aware, CLI+TUI, version-gated) | 9 |
| End-to-end fork evals — Pi, OpenCode, Codex | 10 |
