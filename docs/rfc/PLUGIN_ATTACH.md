# RFC: Plugin Attach (`--plugin <name>`)

- **Status:** v1 implemented
- **Scope:** design + implementation for v1; Telegram retrofit, macOS OAuth fix, free-form plugins, and group inheritance are out of scope (see §11)

## 1. Motivation

agent-deck has three orthogonal mechanisms for extending a Claude session today, but none of them covers Claude Code **plugins** as a first-class concept:

1. **`--mcp <name>`** (catalog-driven) — writes per-project `.mcp.json`, attaches MCP servers. Implemented end-to-end: catalog `[mcps.<name>]` in `~/.agent-deck/config.toml`, `MCPDef` schema (`internal/session/userconfig.go:1014-1045`), writer `WriteMCPJsonFromConfig` (`internal/session/mcp_catalog.go:143-251`), tracking field `Instance.LoadedMCPNames` (`internal/session/instance.go:186-188`), dedicated dialog (`internal/ui/mcp_dialog.go`) and CLI subcommand (`cmd/agent-deck/mcp_cmd.go`).
2. **`--channel <id>`** (free-form) — populates `Instance.Channels []string` (`internal/session/instance.go:190-196`), emitted as `claude --channels <csv>` to enable inbound delivery. Mutator path exists (`internal/session/mutators.go:135-149`), Telegram-topology validator guards anti-patterns (`internal/session/telegram_validator.go`).
3. **Global `enabledPlugins` in `~/.claude/settings.json`** — manual user step, affects every Claude session under that profile, no per-session isolation. The v1.7.68 `WorkerScratchConfigDir` mechanism (`internal/session/worker_scratch.go`) already mutates a per-session settings.json — but only to *force-disable* the Telegram plugin on non-conductor workers (issue #59).

The gap: enabling a plugin like `octopus@nyldn/claude-octopus` for **only one session** today requires either (a) editing global `enabledPlugins` and accepting cross-session contamination, or (b) reaching into `WorkerScratchConfigDir`'s hardcoded telegram-disable path and modifying source. Both options are wrong.

This RFC generalizes the v1.7.68 scratch-dir mechanism into a per-session **plugin allowlist**, exposed via a new `--plugin <name>` CLI flag, persisted on `Instance.Plugins []string`, and applied at spawn time through the existing `EnsureWorkerScratchConfigDir` writer.

### 1.1 Concrete user pain points

- **Per-session plugin isolation** — running `claude-octopus` on one session for code review without affecting other claude sessions on the same host.
- **Channel-emitter discovery** — users currently have to know that `--channels plugin:telegram@…` requires the plugin to also be `enabledPlugins=true`. The two flags must be coordinated manually; mismatch fails silently (capability handler not registered).
- **Reproducibility of session topology** — `agent-deck session show` cannot today list "what plugins are enabled for this session" because there is no per-session field to show.

## 2. Goals (v1)

- Catalog of curated plugins under `[plugins.<name>]` in `~/.agent-deck/config.toml`.
- New CLI flag `--plugin <name>` on `agent-deck add` and `agent-deck launch` (multi-flag, catalog-only).
- New mutator `agent-deck session set <id> plugins <csv>` for runtime edits.
- New persisted field `Instance.Plugins []string` (JSON-tagged, sticky across restarts).
- Writer extension on `internal/session/worker_scratch.go` to apply `enabledPlugins[<id>] = true` per session, layered with the existing Telegram deny-list.
- Auto-install by shell-out to `claude plugin install <name>@<source>` when a catalog entry has `auto_install = true` and the plugin is not yet installed under the source profile.
- New multi-checkbox field in the Edit Session dialog (`internal/ui/edit_session_dialog.go`).
- Auto-link with Channels via `emits_channel` catalog hint (e.g., `telegram`/`discord`/`slack` plugins auto-populate `Instance.Channels`).
- Behavioral eval coverage under `tests/eval/session/` per CLAUDE.md:81-108 mandate.
- Persistence test guards (`TestPersistence_*` remain green; new `TestPersistence_PluginsSurviveRestart` added).

## 3. Non-goals (v1, deferred)

- **Free-form plugins** (`--plugin name@source` without a catalog entry). Catalog-only in v1; free-form is a follow-up.
- **Telegram retrofit** onto a generic deny-list-minus-opt-ins. v1 explicitly *refuses* `--plugin telegram@claude-plugins-official` with a pointer to `--channels`; full migration is a separate RFC (`docs/rfc/PLUGIN_TELEGRAM_RETROFIT.md`).
- **macOS OAuth keying-by-`CLAUDE_CONFIG_DIR`** (issue #759). v1 ships a loud warning at first scratch creation; a structural fix is a separate RFC.
- **Group / profile inheritance** of plugins. `GroupSettings` / `ProfileSettings` in `internal/session/userconfig.go:222-246` carry no MCP defaults; plugins follow that precedent.
- **New Session dialog field**. MCPs are not exposed there either (`internal/ui/newdialog.go`); plugins follow that precedent. Edits go through Edit Session dialog post-creation, mirroring the MCP dialog UX.
- **Pool mode** for plugins (no shared subprocess sharing between sessions).
- **Plugin enablement in non-claude tools** (gemini, codex, copilot, shell). v1 is claude-only — same gate as `Channels` and `ExtraArgs`.

## 4. Architecture

### 4.1 Catalog: `PluginDef`

New struct in `internal/session/userconfig.go` next to `MCPDef` (line 1014+):

```go
type PluginDef struct {
    Name         string `toml:"name"`           // short name e.g. "telegram", "octopus"
    Source       string `toml:"source"`         // "claude-plugins-official" or "owner/repo"
    EmitsChannel bool   `toml:"emits_channel"`  // auto-link --plugin → Channels
    AutoInstall  bool   `toml:"auto_install"`   // shell-out claude plugin install if missing
    Description  string `toml:"description"`
}
```

Storage: `UserConfig.Plugins map[string]PluginDef \`toml:"plugins"\`` (next to `UserConfig.MCPs` at line 58).

Defaults / nil-init mirrors MCPs (line 1474, 1561). Accessors: `GetPluginDef(name)`, `GetAvailablePlugins()`, `GetAvailablePluginNames()` (sorted).

Validation: `Source` required, `Name` required. Reject `Name == "telegram" && Source == "claude-plugins-official"` in v1 — see §6.

### 4.2 Instance persistence

```go
// internal/session/instance.go (next to Channels at line 196)
Plugins []string `json:"plugins,omitempty"`
```

Persisted as catalog **short names** (e.g., `["octopus", "discord"]`), not fully-qualified `name@source` IDs. Resolution to `id` happens at spawn time via the catalog lookup. Rationale: rename of marketplace source updates only `config.toml`, never state.db.

State.db column added in `internal/statedb/migrate.go` mirroring the `loaded_mcp_names` column (lines 54, 84, 134, 219, 238, 278, 316).

### 4.3 Writer extension: `worker_scratch.go`

The current writer hardcodes one mutation (`internal/session/worker_scratch.go:140-145`):

```go
plugins[telegramPluginID] = false
```

Generalized to a `denyList ∪ allowList` model:

```go
denyList  := computeDenyList(i, hostHasTelegramConductor)
            // ["telegram@claude-plugins-official"] when host has TG conductor
            // AND i is not the channel-owner (Channels has no plugin:telegram@... prefix)
allowList := resolveInstancePlugins(i, cfg)
            // map catalog names → "<name>@<source>" IDs

for _, id := range denyList  { plugins[id] = false }
for _, id := range allowList { plugins[id] = true }
// allowList runs LAST so explicit user opt-in beats deny default
```

The deny-then-allow ordering is **load-bearing**: in v2 (after the Telegram retrofit RFC) it lets a user opt-in to telegram via `--plugin` even on a TG-conductor host. v1 explicitly refuses this case at the CLI layer (see §6) so the ordering is currently never exercised for telegram, but stays correct by construction.

### 4.4 Gate extension: `NeedsWorkerScratchConfigDir`

Current gate (`worker_scratch.go:79-84`) is:

```go
return telegramStateDirStripExpr(i) != "" && hostHasTelegramConductor()
```

Becomes:

```go
return needsScratchForTelegram(i) || needsScratchForExplicitPlugins(i)

func needsScratchForExplicitPlugins(i *Instance) bool {
    return i.Tool == "claude" && len(i.Plugins) > 0
}
```

`hostHasTelegramConductor()` (`worker_scratch.go:60-66`) is preserved verbatim — issue #759 (macOS OAuth de-auth on hosts that don't need scratch) does not regress for users who don't use `--plugin`.

### 4.5 CLI surface

**`agent-deck add` and `agent-deck launch`** (mirrors `--mcp` exactly):

- `cmd/agent-deck/main.go:1050-1063` — `fs.Func("plugin", ...)`, accumulates into `pluginFlags []string`.
- `cmd/agent-deck/main.go:865` (`valueFlags` map) — `"--plugin": true` so positional `[path]` reorder works.
- `cmd/agent-deck/main.go:1421-1427` — claude-only validation, catalog lookup, `Instance.Plugins = pluginFlags`.
- `cmd/agent-deck/launch_cmd.go:48-53, 329-335` — same.

Validation up-front (matching `--mcp`'s strict path at `launch_cmd.go:387-391`): unknown name → `ErrCodePluginNotAvailable`, exit 2 with sorted list of available names.

**`agent-deck session set <id> plugins <csv>`**:

- `internal/session/mutators.go` — new `FieldPlugins = "plugins"` (line 21+), added to `RestartPolicyFor` returning `FieldRestartRequired` (line 58-66), case branch in `SetField` (line 135 shape from `FieldChannels`):
  - claude-only gate
  - CSV split, trim, drop empties (matches `FieldChannels` exactly)
  - validate every name against catalog, error on unknown
  - replace `inst.Plugins` (no append, no merge)
- `cmd/agent-deck/session_cmd.go:1014` — usage string entry.

**`session show --json`** — emit `plugins: [...]` when non-empty (next to existing `channels` handling at `session_cmd.go:905-909`). `session list --json` continues to omit (matches `--mcp`'s deliberate omission).

### 4.6 Auto-install

New file `internal/session/plugin_install.go`:

```go
func ensurePluginInstalled(sourceProfileDir string, def PluginDef) error
```

Algorithm:

1. Check `<sourceProfileDir>/plugins/<source>/<name>/` — if exists, return nil (idempotent).
2. Acquire flock at `~/.agent-deck/locks/plugin-<source>-<name>.lock` (kernel-level fcntl, 30s timeout). Two concurrent sessions installing the same plugin race-free.
3. Run `claude plugin marketplace add <source>` if `<sourceProfileDir>/plugin-marketplaces.json` doesn't list it. Best-effort (already-present is not an error).
4. Run `claude plugin install <name>@<source>`. Best-effort: failure logs warning to session log, returns nil so session spawn proceeds without the plugin.
5. Release flock.

Call site: `Instance.Start()` AFTER `prepareWorkerScratchConfigDirForSpawn` returns, BEFORE the actual `tmux new-session`. Order matters: scratch dir is now built (so `enabledPlugins` is set in scratch settings.json), but plugin code may not be physically present yet. `claude plugin install` must run against `sourceProfileDir`, NOT the scratch path — so installs are global per source profile (cheap, idempotent), enablement is per-session (scratch). The scratch dir's symlinked `plugins/` directory points at the source profile's `plugins/` (current `mirrorProfileEntries` behavior at `worker_scratch.go:170-190`), so the per-session claude finds the just-installed plugin code at runtime.

Failure mode: if `claude plugin install` errors (network, version mismatch, marketplace API change), session still starts; user sees the warning in `~/.agent-deck/state/<id>/session.log`. The scratch settings.json still has `enabledPlugins[<id>] = true`, but Claude won't load a plugin whose code is absent — it logs and proceeds. Net effect: zero blocking; observability via session log.

### 4.7 Channel auto-link

When `--plugin <name>` resolves to a `PluginDef` with `EmitsChannel == true`:

- Append `plugin:<name>@<source>` to `Instance.Channels` if not already present.
- On removal via `session set plugins`: also drop the matching channel entry (only if it was auto-linked — distinguished by exact `plugin:<name>@<source>` match for a `PluginDef.EmitsChannel == true` plugin in the current catalog).

Override: new `--no-channel-link` boolean flag for users who explicitly want tools-only mode. Stored as `Instance.PluginChannelLinkDisabled bool` (`json:"plugin_channel_link_disabled,omitempty"`).

Rationale for auto-link as default — see §5.3.

### 4.8 Edit Session dialog (text-input field)

v1 ships a text-input field for Plugins (CSV) in `internal/ui/edit_session_dialog.go`, mirroring the `ExtraArgs` shape. Registration in `Show(inst)`: inserted under the existing claude-only block (after `Extra args`), guarded by `len(GetAvailablePluginNames()) > 0` — empty catalog hides the row entirely.

The full multi-checkbox `editFieldPillsMulti` widget originally specified here is deferred to v1.1 — the text-input is functionally complete but visually denser. Auto-restart inherits free from the existing pipeline: `RestartPolicyFor(FieldPlugins) == FieldRestartRequired` → `HasRestartRequiredChanges` → `home.go:7269-7283`.

Tests:
- `internal/ui/edit_session_dialog_plugins_test.go` — five cases mirroring `ExtraArgs` shape.
- `internal/ui/edit_session_dialog_eval_test.go` — `TestEval_PluginToggleSurfacesAsRestartHint` under `//go:build eval_smoke`.

### 4.9 Standalone Plugin Manager dialog (hotkey `L`)

Added in addition to §4.8 to mirror the existing MCP Manager UX (hotkey `m`) and Skills Manager (`s`). Implemented as a separate Bubble Tea dialog at `internal/ui/plugin_dialog.go` with single-column checkbox list, `Space`/`x` toggle, navigation `↑/↓`/`j/k`/`g/G`/`Tab`, Apply on `Enter`, cancel on `Esc`. Reads catalog via `GetAvailablePluginNames` and per-session state via `inst.Plugins`; persists through the same `session.SetField(FieldPlugins, ...)` path as every other entry point — so validation, telegram refusal, and channel auto-link are shared.

Hotkey registration in `internal/ui/hotkeys.go` as `hotkeyPluginManager` (default `L`), mirroring the parallel registrations of `hotkeyMCPManager` (`m`) and `hotkeySkillsManager` (`s`). Help-overlay row added in `help.go`. Modal-blocking-list in `home.go:hasModalVisible` updated.

Tests:
- `internal/ui/plugin_dialog_test.go` — eight unit tests (Show populates, HasChanged tracks toggles, navigation wraps, Esc hides, empty catalog still renders with help, telegram-official filtered, SelectedPluginNames preserves state).

This subsection was not in the original RFC §4 scope but landed in the same iteration as the consensus path for the "first-class plugin management" UX. It is documented here for completeness rather than as a deviation — the in-process invariant (single source of truth via `SetField`) is preserved.

## 5. Key design decisions

### 5.1 Per-session scratch dir (chosen over per-profile shared)

Rationale: matches the v1.7.68 isolation model; aligns with the user mental model of "one agent-deck session = one independent Claude environment"; lets plugins be added to one session without restarting others sharing the profile. The cost is duplication of the scratch dir per session (handful of KB) and the macOS OAuth pitfall (§7).

### 5.2 Catalog-only (chosen over free-form)

Rationale:
- Forces explicit documentation of every plugin a user enables (the `[plugins.<name>]` block IS the documentation).
- Makes `emits_channel` and `auto_install` policy declarable per plugin without per-flag complexity.
- Validation at CLI level catches typos with a sorted list of options.
- The free-form path can be added in v2 without breaking compatibility — the CLI flag's value space is a superset of catalog names.

Cost: users must edit `config.toml` once per new plugin. Compared to the "manually edit `~/.claude/settings.json`" status quo, this is a strict improvement.

### 5.3 Auto-link with Channels via `emits_channel` (chosen over strict-orthogonal)

Rationale:
- The current Telegram-validator (`telegram_validator.go:9-17`) exists exactly because users get the global-plugin / `--channels` coordination wrong. Auto-link makes the two impossible to desynchronize for catalog-listed channel-emitters.
- Combined with §5.4 (auto-install), agent-deck already owns the plugin lifecycle for a session — owning the channel wiring is a natural extension.
- Without auto-link, `--plugin discord@…` succeeds but inbound delivery silently doesn't work (capability handler unregistered) — this is a sharp footgun.

Cost: "magic" behavior. Mitigations:
- Catalog `emits_channel` is opt-in per plugin entry; only flagged plugins auto-link.
- `--no-channel-link` override exists.
- `agent-deck session show` lists the auto-linked channel explicitly so the topology is visible.

### 5.4 Auto-install via `claude plugin install` shell-out

Rationale:
- User explicitly chose this in the planning Q&A as the most ergonomic path.
- agent-deck already shells out to other Claude binaries (`claude --resume`, `bun telegram` etc.); shell-out to `claude plugin install` is consistent.
- Idempotent (existence check + flock).
- Best-effort: install failure does not block session start.

Cost: adds an external dependency on the `claude plugin` subcommand stability. Mitigated by:
- Catching all error modes and logging-not-blocking.
- Per-(source, name) flock prevents the install storm if a user starts 5 sessions in parallel.
- `auto_install = false` in catalog opts a plugin out (manual install required).

## 6. Telegram coexistence policy (v1)

v1 explicitly **refuses** `--plugin telegram@claude-plugins-official` at the CLI layer:

```
Error: telegram cannot be enabled via --plugin in v1.
       Use --channel plugin:telegram@claude-plugins-official instead.
       The legacy worker-scratch deny-list (worker_scratch.go:140-145, issue #59)
       hardcodes telegram=false to prevent 409 Conflict pollers; reconciling
       this with --plugin opt-in requires a refactor of the 409-Conflict
       guard. See docs/rfc/PLUGIN_TELEGRAM_RETROFIT.md (planned, post-v1).
```

The check fires:
- In CLI flag handling (`cmd/agent-deck/main.go` and `launch_cmd.go`) at the validation pass.
- In the mutator (`internal/session/mutators.go::SetField`).
- In catalog validation when loading `config.toml` — the check rejects a `[plugins.telegram] source = "claude-plugins-official"` entry at config-load time so the catalog is internally consistent.

Forks / alternative telegram plugins (`plugin:telegram@<other-fork>`) are NOT refused — only the exact `telegram@claude-plugins-official` ID is gated. Rationale: the worker-scratch deny-list is keyed on that exact ID (`worker_scratch.go:46`), so other telegram-shaped plugins don't trip the 409 code path.

Test: `internal/session/plugin_telegram_refusal_test.go` — `TestPluginRefuse_TelegramOfficial_AtCLI`, `TestPluginRefuse_TelegramOfficial_AtMutator`, `TestPluginRefuse_TelegramOfficial_AtCatalogLoad`, `TestPluginAllow_TelegramFork_LandsInPlugins`.

## 7. macOS OAuth handling (v1 policy)

Issue #759 documents that on macOS, Claude Code keys OAuth credentials by the literal `CLAUDE_CONFIG_DIR` path string. A per-session scratch dir under `~/.agent-deck/worker-scratch/<instance-id>/` has no prior login association and triggers a "login required" prompt on first claude spawn.

v1 strategy: **loud warning + best-effort, no blocking**.

At first scratch creation (detected by checking `~/.agent-deck/state.json` for a `macos_plugin_warning_shown_for_profile_<name>` flag):

- Print to stderr (and to session log) a multiline warning:
  ```
  ┌─ NOTICE: per-session plugin scratch on macOS ──────────────────┐
  │ This session enables plugins via a per-session CLAUDE_CONFIG_DIR. │
  │ On macOS, Claude Code keys OAuth credentials to the literal     │
  │ config-dir path, so this session may show "login required."     │
  │                                                                  │
  │ If that happens:                                                 │
  │   1. Open a regular shell                                        │
  │   2. Run: CLAUDE_CONFIG_DIR=<path> claude                        │
  │   3. Authenticate                                                │
  │   4. Restart this agent-deck session                             │
  │                                                                  │
  │ See: docs/rfc/PLUGIN_ATTACH.md §7                                │
  └──────────────────────────────────────────────────────────────────┘
  ```
- Mark the flag in `state.json` so the warning shows once per profile (not on every session start).

Linux/Docker hosts skip this warning (no path-keyed OAuth issue).

Hosts WITH `hostHasTelegramConductor() == true` already use scratch dirs today and have presumably handled the OAuth issue; the warning still fires there if a `--plugin` is the trigger (because the messaging is plugin-specific).

Test: `internal/session/macos_oauth_warning_test.go` — `TestMacOSWarning_ShowsOnceAtFirstScratchEvent`, `TestMacOSWarning_SkippedOnLinux`, `TestMacOSWarning_StateFlagPersists`.

A structural fix (proactively symlinking `.credentials.json`, or filing a Claude Code issue for path-independent keying) is deferred to a follow-up RFC.

## 8. Test mandate map

Per CLAUDE.md, the following mandates fire on this work:

### 8.1 Session persistence (CLAUDE.md:13-31)

`internal/session/instance.go` and `internal/session/userconfig.go` are in the touch-list. Required pass:

```bash
GOTOOLCHAIN=go1.24.0 go test -run TestPersistence_ ./internal/session/... -race -count=1
bash scripts/verify-session-persistence.sh   # Linux+systemd, final merge gate
```

Forbidden by mandate: code path that "starts a Claude session and ignores `Instance.ClaudeSessionID`" (CLAUDE.md:30) — RFC introduces no such path. New `TestPersistence_PluginsSurviveRestart` validates round-trip through Restart.

### 8.2 Per-group config (CLAUDE.md:42-44)

Not directly affected (plugins are not group-inherited in v1). `TestPerGroupConfig_*` must remain green; the worker-scratch gate change must preserve `TestPerGroupConfig_NoScratchWhenNoTelegramConductor` (`internal/session/worker_scratch_telegram_gate_test.go`).

### 8.3 Worker-scratch suite

Existing 8-test suite (`worker_scratch_test.go` + `worker_scratch_telegram_gate_test.go`) augmented:

- `TestEnsureWorkerScratch_AllowListWinsOverDenyList`
- `TestNeedsScratch_NoTelegramButHasPlugins_ReturnsTrue`
- `TestPluginsAndTelegramDenyCoexist`
- `TestEnsureWorkerScratch_PreservesUnrelatedSettingsKeys` (regression guard for §4.3)

### 8.4 Behavioral evaluator harness (CLAUDE.md:81-108)

Mandatory eval case under `tests/eval/session/` (real-tmux harness):

```go
//go:build eval_smoke
func TestEval_LaunchPluginFlag_WritesEnabledPlugins(t *testing.T) {
    // exec: agent-deck launch . -c claude --plugin <catalog-name>
    // assert: <scratch>/settings.json contains enabledPlugins[<id>] = true
    // assert: tmux pane shows claude actually spawned
    // assert: stderr contains the macOS warning if runtime.GOOS == "darwin"
}
```

Plus the in-package `internal/ui/edit_session_dialog_eval_test.go` case (§4.8).

Required pass:

```bash
GOTOOLCHAIN=go1.24.0 go test -tags eval_smoke ./tests/eval/... ./internal/ui/...
```

### 8.5 `--no-verify` mandate (CLAUDE.md:110-112)

All commits run hooks. RFC change itself is `docs/rfc/**` (metadata-only), so the metadata-only carve-out applies; subsequent code commits do NOT carve out.

### 8.6 TDD always (CLAUDE.md:119)

Each RED test lands in a separate commit before the GREEN fix.

## 9. Implementation phases

| # | Phase                              | Deliverables                                                         | Mandates fired         |
|---|------------------------------------|----------------------------------------------------------------------|------------------------|
| 0 | RFC sign-off                       | This document approved                                               | none                   |
| 1 | Catalog + persistence              | `PluginDef`, `Instance.Plugins`, state.db migration, RED+GREEN tests | persistence            |
| 2 | Writer extension                   | `worker_scratch.go` deny+allow, gate extension, scratch tests        | persistence, scratch   |
| 3 | CLI flag + mutator + Telegram refusal | `--plugin` on add/launch, `session set plugins`, telegram refusal at three layers | eval, persistence |
| 4 | Auto-install + macOS warning       | `plugin_install.go`, flock, state.json warning flag                  | persistence            |
| 5 | Edit Session dialog field          | `editFieldPillsMulti`, dialog tests, eval test                       | eval                   |
| 6 | Channel auto-link                  | `emits_channel` resolution, `--no-channel-link` flag, validator extension | eval, scratch     |
| 7 | Docs sync                          | SKILL.md, cli-reference.md, llms-full.txt, CHANGELOG.md (Unreleased) | drift (informal)       |

Each phase is a separate PR. Phases 1–2 are independent and can land before CLI/UI; this gives us the foundation without exposing user-facing surface until phase 3.

## 10. Open questions — resolutions

Answers below are author defaults; reviewer may override during sign-off.

1. **Lock file location for auto-install.** **Resolved: global** path `~/.agent-deck/locks/plugin-<source>-<name>.lock`. No multi-profile-concurrent-install scenario exists in current usage; per-profile lock buys nothing and adds path-construction complexity. Revisit only if profiles grow per-profile catalogs.

2. **`emits_channel` for forks.** **Resolved: leave validator ID-specific.** The current `GLOBAL_ANTIPATTERN` check is keyed on the literal `claude-plugins-official` source because that is the ID whose 409-Conflict pathology is documented (`telegram_validator.go:63-67`). Forks (`plugin:telegram@<owner>/<repo>`) are presumed to have their own state-dirs and don't auto-collide globally. The `DOUBLE_LOAD` warning already matches the prefix `plugin:telegram@*` (line 53-58) — that stays correct for forks. Net: no validator change needed; document the asymmetry.

3. **Old claude binary behavior.** **Resolved: per-call error, no proactive version check.** Auto-install is already best-effort; failure logs and the session proceeds. A proactive `claude --version` parse adds maintenance burden (string format drift) for limited gain. The per-call error message from `claude plugin install` is the most accurate signal — surface it verbatim in the session log.

4. **Eval test runtime cost.** **Resolved: one parameterized eval per phase area, table-driven.** Phase 3 ships `TestEval_PluginCLI` covering `add`, `launch`, and `session set`; phase 5 ships `TestEval_PluginEditDialog` covering toggle/restart/persist; phase 6 ships `TestEval_PluginChannelLink`. Each is one Go subtest tree, ~10-15s wall time. Total `eval_smoke` budget bump: ~45s — acceptable per RFC EVALUATOR_HARNESS.md tolerance.

5. **`session show` JSON shape.** **Resolved: always emit for claude sessions** (mirroring `channels` at `session_cmd.go:905-909`). Empty `plugins: []` is fine and lets downstream tooling check presence without nil-handling. Non-claude sessions still omit (consistent with `channels`).

## 11. Future work (deferred RFCs)

- **`docs/rfc/PLUGIN_TELEGRAM_RETROFIT.md`** — full migration of the Telegram deny-list onto the generic plugin allowlist machinery. Removes the v1 refusal policy from §6, lets users opt in to telegram via `--plugin` while preserving 409-Conflict guards. Touches `worker_scratch.go` deny-list semantics (mandate-impacted), `telegram_validator.go` (extends `TelegramValidatorInput` with `EnabledPlugins`).
- **`docs/rfc/PLUGIN_FREEFORM.md`** — relax catalog-only restriction. Adds `--plugin name@source` parsing without a catalog entry. Loses `emits_channel` and `auto_install` for free-form plugins.
- **`docs/rfc/PLUGIN_GROUP_INHERITANCE.md`** — `[groups."x".plugins]` table with merge-with-instance semantics. Mirrors potential MCP group inheritance (which doesn't exist either, so this might be a co-RFC).
- **`docs/rfc/MACOS_OAUTH_PATH_INDEPENDENCE.md`** — structural fix for #759. Either upstream patch to Claude Code, or proactive credentials symlinking with empirical validation that the path-keying is `realpath`-tolerant.
- **`docs/rfc/PLUGIN_POOL.md`** — shared subprocess for plugin servers across sessions (analogous to MCP pool mode). Lower priority; only worthwhile if heavyweight plugins emerge.

## 12. Acceptance criteria

This RFC is "accepted" when:
- All Open Questions in §10 have answers in the RFC body or this section.
- A maintainer (@asheshgoplani or designate) signs off.
- The phase 1 PR opens with RED tests for persistence and catalog round-trip.
- An issue is filed referencing this RFC for tracking.

## 13. References

- v1.7.68 worker-scratch implementation: `internal/session/worker_scratch.go`, issue #59.
- v1.7.22 Telegram-topology validator: `internal/session/telegram_validator.go`, issue #658.
- v1.7.40 (superseded) `TELEGRAM_STATE_DIR`-stripping: `internal/session/env.go:363-379`.
- macOS OAuth path-keying: issue #759, gate `hostHasTelegramConductor` in `worker_scratch.go:60-66`.
- MCP attach pathway (template for this work): `internal/session/mcp_catalog.go:143-251`, `cmd/agent-deck/mcp_cmd.go`, `internal/ui/mcp_dialog.go`.
- Channels mutator (template for `session set plugins`): `internal/session/mutators.go:135-149`.
- Edit Session dialog: `internal/ui/edit_session_dialog.go` (recent work, commits `fc924906` / `8e15da9b`).
- Behavioral evaluator harness RFC: `docs/rfc/EVALUATOR_HARNESS.md`.
- Claude Code plugin protocol (external): `anthropics/claude-plugins-official/external_plugins/telegram/server.ts`, capability `experimental: { 'claude/channel': {} }`, notification `notifications/claude/channel`.
