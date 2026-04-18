# Investigation: Issue #666 — Silent group-disappearance + unexpected `error` state

**Date:** 2026-04-18  
**Scope:** READ-ONLY investigation, then RED→GREEN fix on v1.7.25 chain  
**Reporter asks:** (1) can `GroupPath` be persisted empty? (2) stop the fallback from re-deriving silently, (3) auto-sanitize `enabledPlugins.telegram`, (4) confirm `session restart` cleanly kills prior claude.

## 1. Reproduction confirmation

### 1a. Group disappearance (mechanism 2)

Attempted minimal reproducer scenarios against the HEAD tree:

| Scenario | Can it leave `inst.GroupPath = ""` in memory? | Notes |
|---|---|---|
| `agent-deck add -g X` → CLI exit → TUI start | **No** | `NewInstanceWithGroup` always writes `GroupPath = X`. Save writes `"X"`. Load reads `"X"`. |
| `agent-deck session set <id> title ...` while TUI is running | **No** | CLI loads from DB, mutates only the named field, `SaveWithGroups` writes the same `GroupPath` back. |
| `agent-deck group move <id> root` | **No** | `cmd/agent-deck/group_cmd.go:716` normalizes `""` / `"root"` → `DefaultGroupPath` before `MoveSessionToGroup`. |
| TUI `M` key → move dialog | **No** | `internal/ui/home.go:6868` guards on `targetGroupPath != ""`. |
| Fork (`instance.go:4641 / :4722 / :4864`) | **Only second-order** | Each fork falls back to `i.GroupPath` when `newGroupPath == ""`. If parent already has empty `GroupPath`, the child inherits empty. Parent cannot have empty on HEAD → harmless in isolation. |
| Rename group | **No** | `sanitizeGroupName` returns `"unnamed"` for empty input; the new path is always non-empty. |
| Delete group | **No** | Survivors are re-parented to `DefaultGroupPath`. |
| Concurrent writes (TUI + CLI last-writer-wins) | **No** | The losing writer's `GroupPath` is overwritten with the winner's `GroupPath`, which is still a valid non-empty string. |

**Conclusion:** On HEAD, there is no observable code path that writes `inst.GroupPath = ""` to SQLite. **The symptom the user reports ("session disappears and reappears under a path-derived name") can only be explained by a SQLite row that already has `group_path = ''` from a historical save path, or by direct DB edits, or by a future regression.**

The defensive fallback at `internal/session/storage.go:768-772` is the *trigger* for the symptom — when it fires it *silently* rewrites the group to `extractGroupPath(projectPath)`, which for `/tmp` returns `tmp`, for `/home/user/foo` returns `foo`, etc. That's the "session moved to a path-derived group" observation.

**Why the symptom still matters:** the pit is dug. The next bug that writes `""` anywhere (a new fork path, a bad migration, a manual DB touch, or a half-completed `session set group`) will be silently masked by the fallback, landing in a wrong-but-plausible group. The user will not see an error — just the wrong group name.

### 1b. Session flipping to `error` (mechanism 1)

Confirmed by code read:

- `internal/session/telegram_validator.go:52-84` emits `GLOBAL_ANTIPATTERN` when `enabledPlugins."telegram@claude-plugins-official" = true`.
- `cmd/agent-deck/conductor_cmd.go:249-253` *only writes a warning to stderr*; it **does not mutate** `settings.json`.
- Consequence: after `agent-deck conductor setup` prints the warning once, any freshly-created child claude session in that profile still auto-loads the telegram plugin. Two pollers race on the same bot token → Telegram 409 → claude exits → agent-deck flips session to `StatusError`.

This is a confirmed, non-theoretical bug. It affects every generic child session launched under a profile whose `settings.json` still has the legacy `enabledPlugins.telegram = true`.

### 1c. Stacked claude on restart (mechanism 3)

- `internal/session/instance.go:4076` — `Restart()`.
- Primary path (Claude + known session id + live tmux): `RespawnPane("-k")` at `:4103`. `internal/tmux/tmux.go:2184-2267` captures old PIDs, runs `tmux respawn-pane -k`, then a goroutine `ensureProcessesDead(oldPIDs, newPanePID)` escalates to SIGKILL. **Within one tmux session, stacking is impossible.**
- Fallback path (`:4303-4421`): explicitly `tmux.Kill()` at `:4308`, then `i.recreateTmuxSession()`, then (`:4394-4398`) `KillSessionsWithEnvValue("CLAUDE_SESSION_ID", i.ClaudeSessionID, i.tmuxSession.Name)` — kills **other** agentdeck tmux sessions sharing this claude session id (issue #596 fix).
- **Asymmetry:** the primary respawn-pane path does *not* call `KillSessionsWithEnvValue`. If two agentdeck tmux sessions somehow share the same CLAUDE_SESSION_ID (e.g. user ran `session set claude-session-id` to point two sessions at the same conversation, or a fork path did it), restarting one will not kill the other. Both claude processes keep running; both load the telegram plugin; 409 loop.

This is the plausible mechanism behind the user's "two claude processes" observation on a conductor. It does **not** apply to the simple single-session restart.

## 2. Root cause — group disappearance

### Primary: the fallback at `internal/session/storage.go:768-772`

```go
groupPath := instData.GroupPath
if groupPath == "" {
    groupPath = extractGroupPath(instData.ProjectPath)
}
```

This silently masks any present or future regression that writes `""`. It returns path-derived names like `tmp`, `home`, the parent folder of `ProjectPath`. The user loses their explicitly-chosen grouping with no log, no warning, no persistence trail.

**Every single test scenario I ran exits with in-memory `GroupPath` non-empty and SQLite `group_path != ''`.** The only way this fallback fires in production is via (a) historical DB rows from pre-GroupPath code, (b) a direct DB edit, (c) a future write-empty regression.

### Secondary vectors to close for defense in depth

- **`instance.go:4657 / :4740 / :4878`** — fork inheritance. If `newGroupPath == ""` AND `i.GroupPath == ""`, the child is saved with `""`. Cannot be reached today but one malformed call from a caller is all it takes.
- **Concurrent writes** — the TUI-vs-CLI last-writer-wins race does not cause emptiness, but it does cause silently-overwritten valid groups. This is not the 666 symptom but is a sharp edge worth noting.
- **`SaveGroups` is destructive** (`internal/statedb/statedb.go:556` — `DELETE FROM groups` then bulk insert). If a caller passes a subset of the true groups list, user-created empty groups get wiped. Again, not the 666 symptom — but it's the same "silent rewrite" hazard.

### Fix scope decision

Replace the silent fallback with a loud-and-safe path:
1. Log at `warn` level when we see an empty `GroupPath` row.
2. Route survivors to `DefaultGroupPath` (`"my-sessions"`), which the user can identify as "something went wrong, I can fix this".
3. Do **not** re-derive from `ProjectPath`. That code path creates non-determinism the user can never debug.

`extractGroupPath` itself stays (it's used at `NewInstance` creation time, which is the correct place to derive a group from a path). It's removed only from the load-time fallback.

## 3. Root cause — `session flips to error by itself`

### Primary (confirmed): `enabledPlugins.telegram = true` in profile settings.json

Code trace:
1. `~/.claude*/settings.json` has `"enabledPlugins": { "telegram@claude-plugins-official": true }` — a legacy setting left by early `claude plugin enable` guidance.
2. Any `claude` subprocess launched under that `CLAUDE_CONFIG_DIR` auto-loads the plugin.
3. On a host that *also* runs a dedicated telegram-channel conductor, two processes race the same bot token.
4. Telegram returns `409 Conflict` on one poller. The `bun telegram` plugin enters retry loop, hot-loops, crashes its parent claude process.
5. agent-deck flips the session to `StatusError` (instance.go detects claude exit).

**v1.7.22 gap:** validator detects but does not mutate. The fix has to be applied manually in every profile.

### Secondary: KillSessionsWithEnvValue asymmetry

As discussed in 1c — the respawn-pane path of `Restart()` doesn't run the duplicate-kill sweep the fallback path does. Under unusual fork/edit scenarios two agentdeck tmux sessions can end up with the same CLAUDE_SESSION_ID, compounding the telegram 409.

### Fix scope decision

- `conductor setup` should **auto-remediate** `enabledPlugins.telegram` → `false` for the resolved profile's `settings.json`, not just warn. (See §5 for the both-sides argument.)
- Extend `KillSessionsWithEnvValue` sweep to the respawn-pane path of `Restart()` for Claude / Gemini / OpenCode / Codex tools.

## 4. Proposed fix scope (LOC-graded)

### Fix A — storage.go fallback (group-disappearance root cause)

- **File:** `internal/session/storage.go`
- **Change:** Lines 768-772 — replace `extractGroupPath(instData.ProjectPath)` fallback with `DefaultGroupPath` + a `storageLog.Warn` on the empty-row event. Leave `extractGroupPath` untouched for creation-time use.
- **Test:** `internal/session/storage_test.go` — `TestStorage_LoadRowWithEmptyGroupPath_FallsBackToDefaultNotPathDerived` + companion `TestStorage_LoadRowWithExplicitGroupPath_IsPreserved`.
- **LOC:** ~5 in storage.go, ~60 in tests.
- **Risk:** Very low. Changes the failure mode of a defensive branch; does not affect happy path.

### Fix B — conductor setup auto-remediation

- **File:** `cmd/agent-deck/conductor_cmd_telegram.go` — add `disableTelegramGlobally(configDir string) (changed bool, err error)` that reads + mutates `settings.json`.
- **File:** `cmd/agent-deck/conductor_cmd.go:249` — replace the detect-and-warn block with detect → remediate → print a stdout "fixed" line if remediation changed anything.
- **Test:** `cmd/agent-deck/conductor_cmd_telegram_test.go` — `TestConductorSetup_AutoDisablesGlobalTelegram` writing a fixture `settings.json` with `true`, invoking the remediator, and asserting the file now has `false`. Also preserve unknown keys in the JSON (we must not clobber user's other plugin settings).
- **LOC:** ~40 in conductor_cmd_telegram.go, ~100 in tests.
- **Risk:** Mutates a user-editable config file. Must preserve unknown keys. Must be idempotent. Must skip gracefully when file missing.

### Fix C — restart: same-id sweep on respawn path

- **File:** `internal/session/instance.go` — in each of the four respawn-pane branches (Claude `:4090`, Gemini `:4128`, OpenCode `:4153`, Codex `:4223`) call `tmux.KillSessionsWithEnvValue(toolEnvVar, sessionID, i.tmuxSession.Name)` *after* successful `RespawnPane`. Currently only the fallback at `:4396` does this.
- **Test:** `internal/session/instance_test.go` — `TestRestart_Respawn_SweepsDuplicateClaudeSessionIDAcrossTmuxSessions` using a stub tmux + KillSessionsWithEnvValue spy.
- **LOC:** ~8 in instance.go, ~80 in test.
- **Risk:** Low. The sweep already exists in the fallback path and is proven; we're just extending to the primary path.

### Fix D — guard against empty `GroupPath` writes at the save boundary

- **File:** `internal/session/storage.go` — in `SaveWithGroups` (around `:317`), if `inst.GroupPath == ""`, bump it to `DefaultGroupPath` and `storageLog.Warn` before persisting. This is belt-and-braces on top of Fix A.
- **Test:** `internal/session/storage_test.go` — `TestSaveWithGroups_NormalizesEmptyGroupPath`.
- **LOC:** ~6 in storage.go, ~40 in test.
- **Risk:** Minimal — only fires on the pathological case Fix A already handles at load time.

**Total net LOC:** ~60 source, ~280 test. Contained, TDD-testable, no cross-cutting surface.

## 5. Should `conductor setup` auto-disable `enabledPlugins.telegram`?

### Yes (recommended)

- The flag *will* cause session-crash under any conductor topology. There is no supported use case for it being `true` globally. (Per-session enablement via `--channels` is the only sanctioned path — documented in `skills/agent-deck/SKILL.md:479-499`.)
- The v1.7.22 warning is shown once at setup time. Users miss it in scrollback, especially in a long conductor setup flow that prints many lines.
- agent-deck already writes to the profile via session env files and config.toml; editing `settings.json` is not a new surface.
- The failure mode silent-crashes generic child sessions; auto-remediation prevents *future* crashes of sessions the user hasn't created yet.
- Idempotent: if already `false` or absent, the remediator no-ops.

### No (counterargument)

- `settings.json` is a user-owned file, not an agent-deck file; mutating it without explicit opt-in is a surprise.
- If the user is running a non-conductor workflow where they *want* the global plugin for some reason (there is no such documented use case, but they may have their own), we'd break it.
- `conductor setup` is run exactly once per conductor; the cost of manual remediation is one `jq` command, shown in the warning.

### Recommendation

**Auto-remediate by default, print a loud stdout line about what we changed, add a `--no-settings-fix` escape hatch.** The silent-crash risk outweighs the "surprise edit" risk; the default must protect users who aren't reading stderr carefully. The escape hatch respects the 1% of users with a custom reason.

Loud output example:
```
✓ Disabled enabledPlugins."telegram@claude-plugins-official" in /home/user/.claude-work/settings.json
  (this flag causes every child claude session to start a telegram poller; use --channels per-session instead)
```

## Appendix — files and line anchors

- `internal/session/storage.go:317` save path (writes `inst.GroupPath` verbatim)
- `internal/session/storage.go:768-772` — **the silent fallback**
- `internal/session/groups.go:597-631` — `MoveSessionToGroup`
- `internal/session/groups.go:731-794` — `RenameGroup`
- `internal/session/instance.go:411-468` — `NewInstance*` constructors
- `internal/session/instance.go:470-489` — `extractGroupPath`
- `internal/session/instance.go:4641-4891` — fork paths
- `internal/session/instance.go:4076-4421` — `Restart()`
- `internal/session/instance.go:4394-4398` — `KillSessionsWithEnvValue` (fallback path only)
- `internal/tmux/tmux.go:2184-2267` — `RespawnPane` + `ensureProcessesDead`
- `cmd/agent-deck/conductor_cmd_telegram.go:23-47` — reader + warning emitter
- `cmd/agent-deck/conductor_cmd.go:242-253` — setup-time detection
- `internal/session/telegram_validator.go:52-84` — validator

---

**Investigation complete.** Proceeding to TDD phase 2 (RED tests) per scope-expansion instruction.
