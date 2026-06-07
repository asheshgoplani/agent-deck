# Comprehensive Quick Fork — Design

**Date:** 2026-06-06
**Status:** Approved (design); implementation plan under review
**Area:** `internal/ui/` (fork paths), `internal/session/userconfig.go`

## Summary

Change the TUI quick fork (`f`) from a *conversation-only, instant* fork into a
*comprehensive by default* fork: new git worktree + branch, carry the parent's
uncommitted working-tree state (including gitignored files), match the parent's
Docker isolation, and inherit the parent's Claude launch options. Add a dedicated
`[fork]` config section so the comprehensive defaults are configurable, plus a
master "inherit everything from the parent" switch. The configurable dialog
(`Shift+F`) opens pre-populated from the same defaults so both paths agree.

## Motivation

Today `f` (`quickForkSession`, `internal/ui/home.go:9120`) hardcodes a minimal
fork: `opts=nil`, `sandbox=false`, empty `WorktreeStateOptions{}`. Crucially,
`opts=nil` is resolved against **global config**, not the parent session
(`instance.go:6164-6168`), so a fork silently drops the parent's launch flags
(e.g. `--chrome`). It also ignores the user's `worktree.default_enabled` /
`docker.default_enabled` settings entirely. The result is a fork that neither
resembles its parent nor honors configured defaults.

## Decisions (interview log)

1. **`f` becomes comprehensive by default.** Speed is no longer the point;
   isolation + fidelity is. `Shift+F` is the way to opt *out* of heavy steps.
   (Worktree creation already runs in an async `tea.Cmd`, so the UI stays
   responsive while it works.)
2. **Precedence:** the new `[fork]` block wins; any unset key falls back to the
   comprehensive built-in (ON); the global `worktree.default_enabled` /
   `docker.default_enabled` are **ignored for forks** (they continue to govern
   non-fork session creation).
3. **State scope:** worktree + `with_state` + `with_ignored` are **all ON** by
   default. The gitignored-copy cost and secret-duplication risk are accepted as
   the default and documented (see Trade-offs).
4. **Isolation:** worktree is the dependency-free baseline (default ON). Docker is
   a tri-state `"auto" | "on" | "off"`, default `"auto"` = **match parent**
   (parent dockerized → fork gets its own new container; parent has none → no
   Docker). Worktree and Docker are independent/composable, matching existing code.
5. **Tree placement:** **sibling** of source — the fork inherits
   `source.ParentSessionID` / `source.ParentProjectPath` (unchanged from today),
   preserving config_dir / Telegram / group inheritance from the source's
   conductor. A fork is a *peer* of what it forked, not a child.
6. **Graceful degradation:** each comprehensive step is best-effort; `f` never
   hard-fails. When a step can't run, fall back and surface a **brief
   non-blocking notice**.
7. **Config scope:** `[fork]` governs only structural toggles. For Claude-compatible
   sessions, Claude launch flags (skip-perms / chrome / teammate / model) **always
   inherit from the parent** via `source.GetClaudeOptions()`, falling back to global
   config when the parent has none. Non-Claude tools use their own tool defaults for
   launch options (see Decision 10) — only the structural worktree/state is shared.
8. **Dialog consistency:** `ForkDialog.Show` seeds its checkboxes from `[fork]`
   defaults — the dialog becomes "comprehensive, tweak down."
9. **Master switch:** `[fork].inherit_from_parent` (bool, default false). When
   true, the fork mirrors the parent and the individual structural keys are
   ignored (see Inherit-from-parent mapping).
10. **Forkable tool scope (full cross-tool parity):** structural fork behavior
   (worktree, state, gitignored files, Docker) applies to every local tool that
   `CanFork()` exposes. Claude launch options are Claude-specific and inherit only
   for Claude-compatible sessions; OpenCode, Pi, and Codex must still receive the
   resolved worktree directory + state through their own fork paths. The target is
   that **OpenCode, Pi, and Codex reach the same end-to-end fork coverage as Claude**:
   - **Pi** — already code-complete on TUI + CLI; needs only an end-to-end eval.
   - **OpenCode** — TUI worktree/metadata fix (the `*ClaudeOptions`↔`*OpenCodeOptions`
     type gap is bridged by a workdir-explicit create method), **plus** CLI
     `session fork` support (the CLI currently rejects opencode), plus an eval.
   - **Codex-compatible tools** — forking does **not** exist today and is built from
     scratch: `CanForkCodex`, a `codex fork <session-id>` launch builder (mirroring
     the existing `codex resume <sid>` path and the Pi deferred-`ForkStartCommand`
     pattern), `CreateForkedCodexInstanceWithOptions`, and CLI+TUI dispatch wiring,
     plus an eval. `codex fork` is a newer subcommand, verified locally on
     `codex-cli 0.137.0`, so forkability is gated on a flushed on-disk rollout (the
     same invariant `codex resume` uses) and the docs must state that Codex forking
     requires a codex CLI with `fork` support. An unsupporting binary fails into a
     recoverable error state, never a crash.
   - Cross-tool fork-faking for tests/users is enabled by `opencode-session-id` and
     `codex-session-id` `session set` mutator fields (mirroring `claude-session-id`).

### Web/API fork scope

The Web/API `POST /api/sessions/{id}/fork` endpoint is plain cross-tool
native fork parity in this iteration. It must route Claude-compatible,
OpenCode, Pi, and Codex-compatible sessions through the same tool-specific
fork builders as TUI/CLI, but it does not apply `[fork]` worktree/state/Docker
defaults and does not expose Shift+F title/group/branch controls.

Comprehensive Web fork defaults require an async workflow with branch conflict
handling, worktree/state materialization, rollback, and user-visible
degradation notices. That is intentionally deferred.

## Config schema

New `[fork]` TOML section on `UserConfig`, consistent with `[worktree]` /
`[docker]` / `[tmux]` (own section, bare keys, no key prefix):

```toml
[fork]
inherit_from_parent = false   # master switch; true => copy parent, ignore keys below
worktree            = true    # create new worktree + branch
with_state          = true    # carry tracked uncommitted changes (staged/unstaged/untracked)
with_ignored        = true    # also copy gitignored files (implies with_state)
docker              = "auto"  # "auto" (match parent) | "on" | "off"
branch_prefix       = "fork/" # auto branch name = <prefix><sanitized-title>
```

### Go shape

```go
// UserConfig
Fork ForkSettings `toml:"fork"`

type ForkSettings struct {
    InheritFromParent bool    `toml:"inherit_from_parent"`
    Worktree          *bool   `toml:"worktree"`       // nil => true (comprehensive default)
    WithState         *bool   `toml:"with_state"`     // nil => true
    WithIgnored       *bool   `toml:"with_ignored"`   // nil => true
    Docker            *string `toml:"docker"`         // nil/unknown => "auto"
    BranchPrefix      string  `toml:"branch_prefix"`  // "" => "fork/"
}
```

**Pointer fields are required for the structural toggles**: the comprehensive
default is ON, so "absent" must read as `true`. A plain `bool` would read absent
as Go's zero `false` and silently disable the comprehensive default. This mirrors
the established `*bool` nil-default precedent (`ShowOutput`/`ShowAnalytics` nil =
"default to true", `ClaudeSettings.DangerousMode` nil = true).

**Getters** default to comprehensive-ON and canonicalize like `GetLaunchAs`:

- `GetWorktree() bool` → `Worktree == nil || *Worktree`
- `GetWithState() bool`, `GetWithIgnored() bool` → same nil-true logic
- `GetDocker() string` → lowercased/trimmed; one of `"auto"|"on"|"off"`; unknown/nil → `"auto"`
- `GetBranchPrefix() string` → `BranchPrefix` or `"fork/"`

### Naming rationale (researched)

- Docker tri-state uses the **`[tmux].launch_as` string-enum-with-`"auto"`**
  convention, where `"auto"` means "decide from context." `GetDocker()` mirrors
  `GetLaunchAs()` (lowercase/trim/validate, unknown → default).
- `inherit_from_parent` uses the **`"inherit"`** term that the codebase already
  documents for "defer to parent/config" (`CodexOptions.YoloMode`,
  Hermes: *"nil = inherit from config"*). The `fork` namespace comes from the
  section, so no `fork_` key prefix (matches `[worktree].default_enabled`, not
  `worktree_default_enabled`).

## Behavior

### Quick fork (`f`) resolution

1. Load `config.Fork`.
2. If `inherit_from_parent` → resolve via the inherit mapping below. Else use the
   `Get*` defaults.
3. Claude opts: for Claude-compatible sessions, `opts = source.GetClaudeOptions()`
   (nil → downstream falls back to global config, as today). Transient worktree
   fields are non-persisted and must not leak; the new worktree fields are set
   fresh. For non-Claude forkable tools, the resolved worktree target is still
   structural state and must be applied to the fork target path even though
   Claude launch flags do not apply.
4. Docker: `"auto"` → `sandboxEnabled = source.IsSandboxed()`
   (`instance.go:459` = `Sandbox != nil && Sandbox.Enabled` — the authoritative
   signal, not bare `Sandbox != nil`); `"on"` → true; `"off"` → false.
5. If worktree enabled & git-capable: compute branch `<prefix><slug>` with the
   repo's existing branch sanitizer (`git.SanitizeBranchName` after
   lowercasing/trimming the title) and call `resolveWorktreeTarget(...)` (same as
   the dialog path).
6. Build `WorktreeStateOptions{WithState, WithIgnored}` and call the shared
   helper. `with_ignored=true` implies `with_state=true` at resolution time so
   quick fork and the dialog expose the same effective state.

### Inherit-from-parent mapping

`inherit_from_parent = true` resolves to: **Docker matches parent**, **Claude opts
inherited** (already always true for Claude-compatible sessions), and
**worktree + with_state + with_ignored ON**
(the parent is a real working tree, so the inherited intent is "carry my work into
an isolated copy"). A fork always creates a *new* worktree/branch — it cannot reuse
the parent's — so what is inherited is the *choice* to isolate with state, not the
parent's physical worktree.

### Graceful degradation (+ brief notice)

| Condition | Behavior | Notice |
|---|---|---|
| Source in non-git repo | skip worktree + state → plain fork | "forked without worktree: not a git repo" |
| Docker `auto`/`on` but Docker absent | fall back to worktree-only | "forked without Docker: not available" |
| Non-Claude tool (OpenCode/Pi) | worktree dir + state still applied; tool launch options use that tool's defaults (per Decision 10 — only Claude opts inherit) | — |
| Parent has no persisted opts | global config defaults | — |

**Notice channel (resolved):** reuse the existing transient message bar. `setError`
sets `h.err` + `h.errTime`; the View auto-clears it after 5s (`home.go:5157`).
Despite the name it is already used for non-error info/warnings (`home.go:4462`
"restored '%s' (%s)"; `msg.warning` at `4520-4521`), so it is the de-facto
non-blocking notice path. Thread the notice from the async fork command by adding a
`notice string` field to `sessionForkedMsg` (`home.go:725`); the handler's success
branch (`home.go:4277+`) calls `h.setError(fmt.Errorf("%s", msg.notice))` when set,
mirroring the existing restore-warning pattern. No new UI mechanism is introduced.
Multiple notices are joined into one short sentence; fatal validation errors still
use the existing dialog inline error path or the home message bar and do not become
success notices.

## Implementation shape

1. **Extract a shared helper** from `handleForkDialogKey`'s enter-branch
   (`home.go:8596-8621`): `buildForkCmd(source, title, group, branch,
   worktreeEnabled, withState, withIgnored, sandboxEnabled, opts, parentID,
   parentPath) forkBuildResult` — resolves the worktree target, populates the
   transient worktree fields, builds `WorktreeStateOptions`, and calls
   `forkSessionCmdWithOptions`. It returns fatal validation text for the caller to
   surface in the correct place (`ForkDialog.SetError` for Shift+F, `Home.setError`
   for quick fork) plus non-fatal success notices for `sessionForkedMsg.notice`.
   Both `f` and the dialog call it.
2. **`quickForkSession`** (`home.go:9120`): implement the resolution above using
   the shared helper. Pass `source.GetClaudeOptions()` instead of `nil`.
3. **`ForkDialog.Show`** (`forkdialog.go:201-229`): seed `worktreeEnabled`,
   `withStateEnabled`, `withStateAndGitignored`, `sandboxEnabled` from
   `config.Fork.Resolve(source.IsSandboxed())` instead of the global
   worktree/docker defaults. Preserve the existing public `Show` helper for tests
   if useful, but the production call from `forkSessionWithDialog` must pass the
   selected source's sandbox state so Docker `"auto"` matches quick fork.
4. **`ForkSettings`** in `userconfig.go` with fields + getters above.
5. **Settings TUI preservation:** `MergePanelConfigOntoDisk` already preserves new
   top-level sections by starting from disk, but `SettingsPanel.GetConfig` also has
   explicit hidden-section pass-through tests for `Worktree` and `Tmux`. Add the
   same small guard for `[fork]` so direct `GetConfig` consumers do not silently
   zero it.

## Testing (TDD; per tracked eval-harness mandates)

- `[fork]` parsing: comprehensive fallback when section/keys absent; explicit
  `false`/`"off"` honored; `GetDocker` canonicalization (auto/on/off/unknown).
- Quick fork builds worktree opts + `WithState/WithIgnored=true` + inherited
  parent opts; transient worktree fields excluded from inheritance.
- Docker `auto`: parent with `IsSandboxed()` true → fork sandboxed; parent without → not.
- `inherit_from_parent=true` resolves per the mapping; ignores individual keys.
- `with_ignored=true` implies `with_state=true` even if config sets
  `with_state=false`.
- Branch names use the existing git sanitizer, not space replacement only.
- Degradation: non-git source → plain fork + notice; Docker-absent → worktree-only
  + notice.
- `ForkDialog.Show` seeds checkboxes from `[fork]` and Docker `"auto"` from the
  selected source's `IsSandboxed()` state.
- SettingsPanel direct `GetConfig()` preserves `[fork]`.
- OpenCode/Pi/Codex fork paths receive the worktree target when structural worktree is on.
- `CanForkCodex` gates on an on-disk rollout; `CreateForkedCodexInstanceWithOptions` builds
  `codex fork <sid>` via the deferred-`ForkStartCommand` (Pi) pattern and copies worktree metadata.
- Codex-compatible fork targets preserve the source tool identity and launch command
  instead of collapsing custom compatible tools back to the built-in `codex` tool.
- Codex fork launch commands preserve inline `CODEX_HOME=... codex` commands and
  shell-quote configured `[codex].config_dir` / `CODEX_HOME` prefixes.
- Codex `Start()` and `StartWithMessage()` consume `IsForkAwaitingStart` before
  normal `buildCodexCommand` resume/fresh dispatch, mirroring the existing Claude
  and Pi fork-start sentinels.
- CLI `session fork` admits opencode + codex (not only claude/pi).
- End-to-end real-binary fork evals exist for Pi, OpenCode, and Codex (mirroring the
  Claude fork-with-state eval); each asserts the fork roots in a fresh worktree/branch.
- Session-persistence suite (`TestPersistence_*`) stays green (touches session
  lifecycle paths).
- Eval harness: this is a user-observable interactive behavior change to a tmux
  state mutation → an `eval_smoke` case is required per `tests/eval/README.md`
  and the existing fork-dialog eval pattern.

## Trade-offs (accepted, documented)

- **`with_ignored=true` duplicates gitignored content** — potentially gigabytes
  (`node_modules`, `.venv`, build dirs) and **secrets** (`.env`, local keys) — into
  the new worktree on every `f`. Accepted as the default. Mitigations: the notice
  path reports degraded fallbacks; `[fork]` config and the dialog allow turning it
  off. Copy-size accounting is out of scope for this change unless added as a
  separate follow-up.
- **`f` is no longer instant.** Acceptable per Decision 1; the async `tea.Cmd`
  keeps the UI responsive during materialization.

## Out of scope

- Async/background materialization of worktree+state (Decision 1 chose blocking-
  but-responsive over a new background-completion mechanism).
- Pinning individual Claude flags in `[fork]` (config is structural-only).
- Changing non-fork session-creation defaults or the global
  `[worktree]`/`[docker]` semantics.
