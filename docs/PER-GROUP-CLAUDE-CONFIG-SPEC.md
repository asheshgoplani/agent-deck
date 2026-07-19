# Per-group Claude config — v1.5.4

## Project overview

Agent-deck currently supports a single `CLAUDE_CONFIG_DIR` per agent-deck **profile** (`~/.claude` for `personal`, `~/.claude-work` for `work`). Sessions inside a profile all inherit the same config dir. This is not enough: a single profile often hosts groups that should use different Claude authentications — e.g. a `conductor` group that should use the work Claude account, a `side-projects` group that should use a separate account, all inside the `personal` profile.

## Problem statement

User has two Claude auth contexts (`~/.claude` personal, `~/.claude-work` work) and a dual-profile alias setup (`cdp` / `cdw`). Agent-deck exposes this at the profile level. **But agent-deck's conductor (in group `conductor`), the user's `agent-deck/*` work sessions, and their personal `trip`/`mails` sessions all run under a single `personal` profile** — they can't be split across Claude config dirs without creating more profiles, which fragments session visibility.

External contributor PR #578 (`feat/per-group-config` by @alec-pinson, 2026-04-12) solves this at the config schema + lookup level:

```toml
[groups."conductor".claude]
config_dir = "~/.claude-work"
env_file = "~/git/work/.envrc"
```

The PR's code-level integration points:
- `GetClaudeConfigDirForGroup(groupPath)` with priority env > group > profile > global
- `IsClaudeConfigDirExplicitForGroup(groupPath)` matching
- `GetGroupClaudeConfigDir` / `GetGroupClaudeEnvFile` helpers on UserConfig
- `instance.go` 3 call-sites rewired: `buildClaudeCommandWithMessage`, `buildBashExportPrefix`, `buildClaudeResumeCommand`

PR #578 is a clean base. This milestone (v1.5.4) **accepts** PR #578 and closes the gaps that block adoption for the user's actual use cases.

## Goals

1. Adopt PR #578's config schema and lookup priority, exactly as designed.
2. Prove per-group config dir works end-to-end for **custom-command sessions** (conductors, `add --command <script>`). This is the highest-risk path because existing code skips `CLAUDE_CONFIG_DIR` prefix for custom commands in `buildClaudeCommandWithMessage` (comment: "alias handles config dir").
3. Prove `env_file` is sourced before `claude` exec, not just exported into the tmux env.
4. Ship named regression tests so this never regresses on future refactors.
5. Ship a visual harness that prints the resolved `CLAUDE_CONFIG_DIR` per session and per group — human-watchable.

## Version

**v1.5.4** — small feature release on top of v1.5.3. Accepts external PR #578's implementation as base. No breaking changes. Added tests are additive.

## Open GitHub items relevant

- **PR #578** (`feat/per-group-config`) — @alec-pinson — OPEN, MERGEABLE, mergeStateStatus=UNSTABLE (no CI checks configured on branch). This milestone's branch `fix/per-group-claude-config-v154` is forked from PR #578's HEAD `fa9971e`, so a future merge strategy is either (a) merge PR #578 then this milestone's additions as a follow-up PR, or (b) land everything as one PR that supersedes #578, with attribution to @alec-pinson in the commit message. User decides at milestone end.

## Requirements

### REQ-1: PR #578 config schema and lookup priority (P0)

**Rule:** `[groups."<name>".claude] { config_dir, env_file }` is a valid TOML section. `GetClaudeConfigDirForGroup(groupPath)` resolves with priority: env var `CLAUDE_CONFIG_DIR` > group override > profile override > global `[claude] config_dir` > default `~/.claude`. Empty or missing group name falls through to profile.

**Acceptance:**
- Unit tests from PR #578 (`TestGetClaudeConfigDirForGroup_GroupWins`, `TestIsClaudeConfigDirExplicitForGroup`) remain GREEN — no modification to their assertions.
- `config_dir` accepts `~` expansion, absolute paths, and environment variable expansion (`$HOME`).
- Adding or removing a group's `config_dir` at runtime is picked up after `ClearUserConfigCache()` (agent-deck's existing cache invalidation path on config reload).

### REQ-2: Custom-command (conductor) sessions honor per-group config_dir (P0)

**Rule:** When an `Instance.Command` is non-empty (e.g. `/home/user/.agent-deck/conductor/agent-deck/start-conductor.sh`), agent-deck MUST still inject `CLAUDE_CONFIG_DIR=<resolved>` into the spawn environment for that session if the group or profile has an override. This closes the gap in PR #578's `buildClaudeCommandWithMessage` which skips the prefix for custom commands — the gap is acceptable for shell aliases that set the env themselves, but NOT acceptable for conductor-style wrapper scripts that have no such alias.

**Resolution approach:** `buildBashExportPrefix` already exports `CLAUDE_CONFIG_DIR` unconditionally (even for custom commands). Verify by test that this path is actually taken for custom-command sessions, OR move the export into the tmux pane env injection if not.

**Acceptance:**
- A session created with `agent-deck -p personal add ./some-wrapper.sh -t "test-conductor" -g "conductor"` where the config file has `[groups."conductor".claude] config_dir = "~/.claude-work"` launches with `CLAUDE_CONFIG_DIR=~/.claude-work` visible inside the tmux pane's environment (verified by `agent-deck session send <id> "echo CLAUDE_CONFIG_DIR=\$CLAUDE_CONFIG_DIR"`).
- After restart, the env var persists — the wrapper script sees it.
- Conductor restart (via `start-conductor.sh`) preserves the env var — the `exec claude ...` inside the wrapper uses `~/.claude-work` for its Claude auth.
- A session in a group with NO override falls through to the profile's config dir.

### REQ-3: env_file is sourced before claude exec (P0)

**Rule:** `[groups."<name>".claude] env_file = "/path/to/.envrc"` causes the tmux pane to `source "/path/to/.envrc"` before exec'ing claude (or the custom command). This enables per-group secrets, PATH adjustments, and tool versions (e.g. `direnv`-style workflows). Path expansion mirrors `config_dir` (`~`, env vars).

**Acceptance:**
- Write a throwaway `/tmp/envrc-test` that `export TEST_ENVFILE_VAR=hello`. Configure a group to use it. Launch a session. `echo $TEST_ENVFILE_VAR` inside the session returns `hello`.
- If `env_file` does not exist, log a warning and continue; do not block session start.
- `env_file` supports both shell-style `.envrc` (sourced) and flat KEY=VALUE `.env` format (also sourced — bash can handle both).

**Non-goals:**
- Not implementing a direnv integration layer. Just a source line.

### REQ-4: Named regression tests (P0)

A new test file `internal/session/pergroupconfig_test.go` MUST contain:

1. `TestPerGroupConfig_CustomCommandGetsGroupConfigDir` — instance with non-empty `Command`, group `foo` has config_dir override. The built env/exports include `CLAUDE_CONFIG_DIR=<foo's dir>`.
2. `TestPerGroupConfig_GroupOverrideBeatsProfile` — group and profile both set, group wins.
3. `TestPerGroupConfig_UnknownGroupFallsThroughToProfile` — instance in group `nonexistent`, falls through to profile override.
4. `TestPerGroupConfig_EnvFileSourcedInSpawn` — env_file set, its exported vars are visible in the spawn env (via `buildBashExportPrefix` or equivalent).
5. `TestPerGroupConfig_ConductorRestartPreservesConfigDir` — end-to-end: create custom-command session, stop, restart, assert `CLAUDE_CONFIG_DIR` in new spawn matches group's override. Connects REQ-2 to REQ-7 from v1.5.2 (custom-command resume path).
6. `TestPerGroupConfig_CacheInvalidation` — add/remove group override, `ClearUserConfigCache()`, resolver returns the new value.

Each test independently runnable (`go test -run TestPerGroupConfig_<name> ./internal/session/...`), self-cleaning, no network.

### REQ-5: Visual harness (P1)

`scripts/verify-per-group-claude-config.sh` — a human-watchable script that:

1. Creates two throwaway groups (`verify-group-a`, `verify-group-b`) with different `config_dir` values in a temp config.
2. Launches one session per group (one normal, one custom-command).
3. Sends `echo CLAUDE_CONFIG_DIR=$CLAUDE_CONFIG_DIR` to each. Captures output.
4. Prints a pass/fail table. Exit 0 iff both sessions show the expected per-group value.
5. Cleans up — stops sessions, restores config.

### REQ-6: Documentation (P0)

- `README.md` — add one subsection "Per-group Claude config" under Configuration, with the `[groups."conductor".claude]` example from PR #578.
- `CLAUDE.md` (repo root) — add a one-line entry under the session-persistence mandate block: "Per-group config dir applies to custom-command sessions too; `TestPerGroupConfig_*` suite enforces this."
- `CHANGELOG.md` — `[Unreleased] > Added` bullet: "Per-group Claude config overrides (`[groups."<name>".claude]`)."
- Attribution in at least one commit message: "Base implementation by @alec-pinson in PR #578."

### REQ-7: Observability (P2)

- On session spawn, one log line: `claude config resolution: session=<id> group=<g> resolved=<path> source=<env|group|profile|global|default>`.
- Helps future debugging (which level actually set the dir for a given session).

---

## Phase 4 — added 2026-04-15 (post-Phase-3, user-authorized)

Phase 4 closes three gaps surfaced after Phases 1–3 landed. None of these were in the original v1.5.4 scope; the user authorized the additive extension on 2026-04-15. Phase 4 keeps the same hard rules (TDD, no push/tag/PR, "Committed by Ashesh Goplani" signature, no Claude attribution) and the same scope discipline (additive only; new files are fine, refactors of PR #578 are not).

### REQ-8 / CFG-08: `[conductors.<name>]` schema + loader (P0)

**Background.** Issue [#602](https://github.com/asheshgoplani/agent-deck/issues/602) (reported by the milestone user, not @alec-pinson) observes that conductors are first-class entities in agent-deck (`internal/session/conductor.go`'s `ConductorMeta`, listed in TUI, registered with their own per-conductor `~/.agent-deck/conductors/<name>/` directory) but cannot carry their own Claude `config_dir`. Today the only way to give a conductor a different Claude auth is to add `[groups."conductor/<name>".claude]` — workable but indirect. CFG-08 promotes conductors to a top-level config section that reads more naturally for the user and gives the loader a per-conductor seam.

**Rule.** A new top-level TOML section is recognized:

```toml
[conductors.gsd-v154]
config_dir = "~/.claude-work"
env_file = "~/git/work/.envrc"
```

**Lookup precedence (extends CFG-01 chain).** When resolving `CLAUDE_CONFIG_DIR` for a session, the loader walks (most-specific → least-specific):

1. `CLAUDE_CONFIG_DIR` env var (unchanged top of chain)
2. `[conductors.<name>]` block, if the session's group path is `conductor/<name>` AND the conductor block sets `config_dir` *(NEW — CFG-08)*
3. `[groups."<group>".claude]` block (PR #578 — CFG-01)
4. `[profiles.<profile>.claude]` block
5. `[claude]` global block
6. Default `~/.claude`

The conductor block sits between env-var and group so that explicit `[groups."conductor/<name>".claude]` config can still win when the user wants to override only that one session, but a single `[conductors.<name>]` line covers every session in the conductor group without restating the group path.

**Propagation gap closed.** Today's code path for conductor-group sessions does NOT inject `CLAUDE_CONFIG_DIR` from a `[conductors.<name>]` source because no such block is parsed. CFG-08 fills this in two seams:

- `internal/session/userconfig.go` — add `ConductorClaudeConfig` struct (mirroring `GroupClaudeConfig`), top-level `Conductors map[string]ConductorClaudeConfig`, and `GetConductorClaudeConfigDir(name)` / `GetConductorClaudeEnvFile(name)` helpers.
- `internal/session/claude.go` — extend `GetClaudeConfigDirForGroup(groupPath)` to detect `groupPath == "conductor/<name>"` (or the canonical conductor-group prefix used by `conductor_cmd.go`) and consult `Conductors[<name>]` between the env-var check and the group check. `GetClaudeConfigDirSourceForGroup` returns source label `conductor` for this branch.

**Acceptance.**
- New unit tests (CFG-11) cover schema parse, precedence, and source-label.
- An end-to-end test creates a session with `GroupPath = "conductor/foo"`, sets only `[conductors.foo] config_dir = "/tmp/x"`, builds the spawn command, and asserts `CLAUDE_CONFIG_DIR=/tmp/x` is exported.
- Backward compat: a session in `conductor/foo` with NO `[conductors.foo]` block but WITH `[groups."conductor/foo".claude] config_dir = "/tmp/y"` still gets `/tmp/y` (group override beats nothing).
- An `env_file` set under `[conductors.<name>]` is also sourced before `claude` exec, mirroring CFG-03.

### REQ-9 / CFG-09: Documentation refresh (P0)

Two surfaces beyond the CFG-06 set need touching:

- **`README.md`** — extend the "Per-group Claude config" subsection (added in CFG-06) with a sibling `[conductors.<name>]` example and a one-line note that conductors win over their underlying group block when both are present. Cross-link to issue #602.
- **agent-deck skill `SKILL.md`** — the on-disk skill at `~/.claude/plugins/cache/agent-deck/agent-deck/<hash>/skills/agent-deck/SKILL.md` (canonical plugin-cache path, see user global CLAUDE.md "Agent-Deck Skill Auto-Load") and the pool copy at `~/.agent-deck/skills/pool/agent-deck/SKILL.md` (if present) need a one-paragraph addition documenting the `[conductors.<name>]` block, the precedence chain, and the pool-vs-canonical distinction. The skill text is what other Claude sessions read when they invoke `agent-deck` — so this is the discoverability channel for the new schema.

Both updates land in the same Phase 4 commits. No new attribution required (Phase 4 is user-authored, not @alec-pinson's PR #578); attribution to issue #602 reporter (the milestone user) is recorded in commit body.

### REQ-10 / CFG-10: `--no-verify` mandate scope clarification (P1)

**Problem.** Repo-root `CLAUDE.md` (v1.5.3 mandate) reads "No `--no-verify` — every commit goes through pre-commit hooks." During Phases 1–3 we hit cases where the pre-commit hook (`go test`, `go vet`) would no-op for purely metadata commits: `.planning/` updates, ROADMAP/STATE/REQUIREMENTS edits, doc-only changes. The hook runs regardless and adds 10–30s of latency for zero verification value. The mandate as written makes no exception, so Claude has been waiting through hook runs on metadata commits.

**Rule (clarification, not a relaxation of the source-code ban).** The `--no-verify` ban applies to commits that touch source code (anything matched by the test/lint hooks). Commits that touch ONLY metadata (`.planning/**`, `docs/**`, `*.md` outside source dirs, `CHANGELOG.md` while in a milestone-prep phase) MAY use `--no-verify` IFF the hook would no-op anyway. The Phase 4 mandate update spells this out in the repo-root `CLAUDE.md` so future Claude instances (and human contributors) don't re-litigate it.

**Acceptance.**
- Repo-root `CLAUDE.md` gets a new sub-section under "General rules" that lists exactly which paths qualify as "metadata-only" and reaffirms that source-modifying commits MUST use the hooks.
- A negative example is included: a commit that mixes `.planning/` AND `internal/session/*.go` is a source-modifying commit and MUST go through hooks.
- The Phase 4 commits themselves follow the new rule: TDD test commits + implementation commits use hooks; the ROADMAP/STATE/SPEC amendment commit (this one) qualifies as metadata-only.

### REQ-11 / CFG-11: Phase 4 regression tests (P0)

`internal/session/conductorconfig_test.go` (NEW file, kept separate from `pergroupconfig_test.go` so Phase 4 can be reverted cleanly if needed) MUST contain:

1. `TestConductorConfig_SchemaParses` — TOML containing `[conductors.foo] config_dir = "/tmp/x" env_file = "/tmp/y"` parses into `UserConfig.Conductors["foo"]`. Path expansion (`~`, `$HOME`) handled.
2. `TestConductorConfig_PrecedenceConductorBeatsGroup` — both `[conductors.foo]` and `[groups."conductor/foo".claude]` set; conductor block wins for `groupPath == "conductor/foo"`.
3. `TestConductorConfig_PrecedenceEnvBeatsConductor` — env var `CLAUDE_CONFIG_DIR` set; env wins.
4. `TestConductorConfig_FallsThroughToGroupOverride` — only `[groups."conductor/foo".claude]` set, no `[conductors.foo]`; group value returned (backward compat with PR #578).
5. `TestConductorConfig_FallsThroughToProfile` — only `[profiles.personal.claude]` set; profile value returned for a `conductor/foo` session.
6. `TestConductorConfig_PropagatesToConductorGroupSession` — end-to-end: build the spawn command for an Instance with `GroupPath = "conductor/foo"` and `[conductors.foo] config_dir = "/tmp/x"`; assert the rendered command contains `export CLAUDE_CONFIG_DIR=/tmp/x`. Closes the propagation gap from issue #602.
7. `TestConductorConfig_EnvFileSourced` — `[conductors.foo] env_file = "/tmp/conductor.envrc"` causes the spawn command to `source /tmp/conductor.envrc` for both the normal-claude and the custom-command paths. Mirrors CFG-03 / CFG-04 test 4 pattern.
8. `TestConductorConfig_SourceLabelIsConductor` — `GetClaudeConfigDirSourceForGroup("conductor/foo")` returns label `conductor` when the value comes from the conductor block. Locks the CFG-07 observability source-label for the new branch.

All eight independently runnable, self-cleaning, no network. Run gate: `go test ./internal/session/... -run TestConductorConfig_ -race -count=1` — all 8 GREEN.

## Out of scope

- Not touching Claude profile-level config (`[profiles.<x>.claude]`) semantics — keep as today.
- Not building a TUI editor for groups — config.toml is hand-edited.
- Not adding per-group `mcp_servers` overrides (future work; `.mcp.json` attach flow already covers this use case).
- Not implementing full direnv `.envrc` with hashing / auto-reload.

## Architecture notes

Based on PR #578's diff (already in this branch):

- `internal/session/userconfig.go` — adds `GetGroupClaudeConfigDir`, `GetGroupClaudeEnvFile`, `GroupClaudeConfig` struct.
- `internal/session/claude.go` — adds `GetClaudeConfigDirForGroup`, `IsClaudeConfigDirExplicitForGroup`, legacy `GetClaudeConfigDir` now delegates with empty group.
- `internal/session/instance.go` — three call-sites rewired to pass `i.GroupPath`.
- `internal/session/env.go` — 4 added lines for env injection.
- `internal/ui/home.go` — 2 lines touched (group passed in UI-initiated spawns).

Gaps this milestone closes:
- `env_file` source semantics: PR #578 adds the schema but the source-before-exec wiring needs verification. Check `env.go` and `buildBashExportPrefix`.
- Custom-command injection path: PR #578 intentionally skips `CLAUDE_CONFIG_DIR` prefix for custom commands in `buildClaudeCommandWithMessage`. `buildBashExportPrefix` is the fallback but needs test coverage.
- Conductor end-to-end: this milestone's visual harness proves it.

## Known pain points

- `fa9971e` (PR #578 HEAD) is several commits behind current `main` on this repo; the v1.5.4 branch may need a rebase before merge. Rebase is a merge-time concern, not this milestone's scope.
- External contributor PR — keep their commit history intact in the final merge, attribute properly.
- The user is on a `personal` profile but wants `conductor` group to use `~/.claude-work`. Unusual direction: groups overriding to a DIFFERENT profile's config dir. Test explicitly.

## Hard rules for all phases

- No `git push`, `git tag`, `gh release`, `gh pr create`, `gh pr merge`.
- No `rm` — use `trash` if cleanup needed.
- No Claude attribution in own commits. Sign as "Committed by Ashesh Goplani" when signing.
- TDD ordering: test before fix.
- Do NOT revert or refactor PR #578's existing code unless a test requires it. Additive only.
- Scope: `internal/session/claude.go`, `internal/session/userconfig.go`, `internal/session/instance.go`, `internal/session/env.go`, new test file `pergroupconfig_test.go`, `scripts/verify-per-group-claude-config.sh`, README.md, CLAUDE.md, CHANGELOG.md, docs/PER-GROUP-CLAUDE-CONFIG-SPEC.md. Anything else = escalate.
- **Phase 4 scope additions (added 2026-04-15):** new file `internal/session/conductorconfig_test.go`, additive edits to `internal/session/userconfig.go` and `internal/session/claude.go` for the `[conductors.<name>]` schema + loader, README.md "Per-group Claude config" subsection extension, agent-deck skill `SKILL.md` (canonical plugin-cache path AND pool path if present), repo-root `CLAUDE.md` `--no-verify` mandate clarification. Phase 4 commits sign "Committed by Ashesh Goplani"; no @alec-pinson attribution (Phase 4 is user-driven, not PR #578); issue #602 reference recorded in commit body.

## Success criteria for the milestone

1. PR #578 unit tests remain GREEN.
2. `go test ./internal/session/... -run TestPerGroupConfig_ -race -count=1` — all 6 GREEN.
3. `bash scripts/verify-per-group-claude-config.sh` exits 0 on conductor host with visual table.
4. Manual proof on conductor host: add `[groups."conductor".claude] config_dir = "~/.claude-work"` to config.toml, restart conductor, `ps -p <pane_pid>` env shows `CLAUDE_CONFIG_DIR=/home/user/.claude-work`, conductor now uses the work Claude account.
5. `git log main..HEAD --oneline` ends with README+CHANGELOG+CLAUDE.md commits and one attribution commit referencing @alec-pinson.
6. No push / tag / PR / merge performed.

### Phase 4 success criteria (additive, gated at milestone end)

7. `go test ./internal/session/... -run TestConductorConfig_ -race -count=1` — all 8 GREEN.
8. Manual proof on conductor host: add `[conductors.gsd-v154] config_dir = "~/.claude-work"` to config.toml with NO matching `[groups.*]` entry, restart the `gsd-v154` conductor, `agent-deck session send <id> "echo CLAUDE_CONFIG_DIR=\$CLAUDE_CONFIG_DIR"` reports `~/.claude-work`. Closes issue #602.
9. README.md "Per-group Claude config" subsection includes the `[conductors.<name>]` example and a one-line precedence note.
10. agent-deck skill `SKILL.md` (canonical plugin-cache path + pool copy if present) documents the `[conductors.<name>]` block.
11. Repo-root `CLAUDE.md` carries the `--no-verify` mandate clarification (metadata-only commits exemption with the negative example).
12. Phase 4 commits sign "Committed by Ashesh Goplani"; no Claude attribution; no @alec-pinson attribution on Phase 4 commits (issue #602 reference is acceptable in commit body).
