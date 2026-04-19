# Changelog

All notable changes to Agent Deck will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.7.37] - 2026-04-19

### Fixed
- **TUI feedback dialog now requires explicit y/N confirmation and shows the exact destination URL, which GitHub account will carry the post, and the full body before sending** — closes the [#679](https://github.com/asheshgoplani/agent-deck/issues/679) privacy gap on the TUI code path, which v1.7.35 and v1.7.36 had fixed only on the CLI side. Under v1.7.36 the in-app feedback popup (ctrl+e or the auto-popup after upgrade) still jumped straight from the comment box to `sender.Send()` on Enter, posting the comment publicly to GitHub Discussion #600 under the user's `gh`-authenticated account with no disclosure of where it was going, no preview of the body, and no opportunity to decline. It also inherited `Sender.Send`'s three-tier fallback (gh → clipboard+browser → clipboard), so a failed `gh` auth would silently copy the comment to the system clipboard and open a browser window — the exact silent-effect class of bug the CLI fix had removed. This release adds a new `stepConfirm` between `stepComment` and `stepSent` that mirrors the CLI's disclosure block verbatim: `"This feedback will be posted PUBLICLY on GitHub."`, the exact URL (`https://github.com/asheshgoplani/agent-deck/discussions/600`), the `gh` CLI attribution, the authenticated `@<login>` resolved via `gh api user -q .login` (falling back to a generic `"your GitHub account"` line when gh is unauthenticated), and a four-space-indented preview of the exact body produced by `feedback.FormatComment` — the same variable the subsequent gh mutation posts, so preview-vs-post drift is impossible. Confirmation requires `y`/`Y`; any other key (`n`, `N`, `Esc`, Enter, stray input) routes to `stepDismissed` with no post. The dialog's internal `sendCmd` now calls `sender.GhCmd` directly with the `addDiscussionComment` GraphQL mutation and surfaces `feedbackSentMsg{err:...}` unchanged on failure — the three-tier clipboard/browser fallback can NEVER fire from the TUI consent path, matching the CLI guarantee. `stepSent` now renders one of three states off a new `sentResult/sentErr` pair populated by `FeedbackDialog.OnSent(msg)` (called from `home.go` on `feedbackSentMsg`): a neutral "Posting to Discussion #600 via gh..." line while in-flight, `"Posted to Discussion #600. Thanks!"` on success, or `"Error: could not post via gh. Not sent."` with a `gh auth status` hint on failure — removing the ambiguous "Sent!" message that appeared regardless of outcome. Dialog width bumped from 56 to 80 columns so the disclosure URL fits on a single line after the `"  Where:  "` prefix, border, and padding. `stepComment` Esc also now routes to `stepDismissed` with no post (previously it jumped to `stepSent` and fired `sender.Send("")`, silently posting an empty-comment feedback entry under the user's gh handle — same bug class). Tests: `TestFeedbackDialog_EnterAtComment_TransitionsToConfirm`, `TestFeedbackDialog_Confirm_N_DismissesWithoutSend`, `TestFeedbackDialog_Confirm_Esc_DismissesWithoutSend`, `TestFeedbackDialog_Confirm_Y_TransitionsToSent`, `TestFeedbackDialog_SendCmd_NoSilentFallback_OnGhError` (the critical regression guard — asserts browser/clipboard stay at zero when gh fails), `TestFeedbackDialog_ConfirmView_ContainsDisclosure`, `TestFeedbackDialog_ConfirmView_FallsBackWhenGhLoginEmpty`, `TestFeedbackDialog_OnSent_ErrorRendersInSentView`, `TestFeedbackDialog_OnSent_SuccessRendersPostedMessage` in `internal/ui/feedback_dialog_test.go`. Users opting in from the TUI now see the same disclosure they would see from `agent-deck feedback` — no code path reaches GitHub under a user's handle without an explicit `y`.

## [1.7.36] - 2026-04-19

### Fixed
- **`agent-deck feedback` prompts now print interactively to stdout instead of being buffered until the whole flow returns** (#679 follow-up, reported by @rgarlik after testing v1.7.35): the v1.7.35 fix for #679 added an explicit disclosure block and `Post this? [y/N]` confirm — but the disclosure was rendered into a `strings.Builder` that was only flushed to `os.Stdout` *after* `handleFeedbackWithSender` returned. Users typed `Rating`, `Comment`, and the confirm answer at a blank cursor, and the disclosure they were supposed to read before consenting was never visible while they were being asked to consent. The same buffering predated #679 (the `Sent! Thanks` path had it too) — #679 just made it impossible to ignore. Fix: `handleFeedbackWithSender` signature gains `in io.Reader` before the writer; `handleFeedback` now wires `os.Stdin`/`os.Stdout` directly, so every `fmt.Fprint(w, ...)` reaches the terminal immediately. Test gap closed by `TestFeedback_PromptPrintsBeforeStdinBlocks` in `cmd/agent-deck/feedback_cmd_test.go`: pairs `io.Pipe` for both stdin and stdout, spawns the handler in a goroutine, reads from the out pipe and asserts "Rating" arrives before sending anything to the in pipe, and times out at 2s if the function buffered. The legacy #679 tests continue to use `strings.Builder` for convenience — that type silently buffers, which is exactly the class of test gap that hid this regression; a follow-up issue tracks adding similar pipe-based smoke tests to every interactive subcommand.

## [1.7.35] - 2026-04-19

This is a **consolidated batch release**. It ships three new fixes (#678, #680, #679) together with the two previously-unreleased `chore(release)` rebuilds that landed on `main` but were never tagged: the PR #655 custom-tool `compatible_with` work (previously slated for v1.7.33) and the PR #580 transition-notify toggle (previously slated for v1.7.34). There are no standalone v1.7.33 or v1.7.34 releases — everything is collapsed into v1.7.35 to avoid tag gaps and user confusion.

### Fixed
- **Shell / placeholder sessions no longer accumulate duplicate tmux sessions on concurrent restart** ([#678](https://github.com/asheshgoplani/agent-deck/issues/678), reported by @bautrey): the duplicate-guard added in the #596 fix keyed on `CLAUDE_SESSION_ID` (and later `GEMINI_/OPENCODE_/CODEX_SESSION_ID`) and was a silent no-op for any session that had no tool-level session id — shell sessions, placeholder sessions, and sessions where the tool id had not been captured yet. @bautrey observed 10 duplicate tmux sessions accumulate over a 2-week run on a Linux+systemd host with 30 shell-tool projects, with orphan-vs-real creation gaps of 1–7 seconds that are inconsistent with human double-press and point to concurrent `Restart()` callers (TUI keymap, HTTP mutator, undo, dialog apply, auto-restart). Fix: `sweepDuplicateToolSessions` now runs a second, unconditional sweep keyed on `AGENTDECK_INSTANCE_ID` (set on every agent-deck tmux session via `SetEnvironment` at start), so the guard is tool-agnostic. The fallback recreate branch in `Restart()` is also re-routed through the shared sweep so it benefits from both guards. Tests: `TestIssue678_SweepDuplicateToolSessions_ShellUsesInstanceID`, `TestIssue678_SweepDuplicateToolSessions_ClaudeAlsoInstanceID`, `TestIssue678_SweepDuplicateToolSessions_ClaudePlaceholderUsesInstanceID`, `TestIssue678_SweepDuplicateToolSessions_ShellSkipsWhenNoTmux` in `internal/session/issue678_shell_dedup_test.go`; #666 tests relaxed to `findSweepCall()` lookup so both sweeps are tolerated side-by-side.
- **`TELEGRAM_STATE_DIR` no longer leaks from a conductor group env_file into child sessions** ([#680](https://github.com/asheshgoplani/agent-deck/issues/680)): the documented conductor pattern mirrors `[conductors.<name>.claude].env_file` and `[groups.<name>.claude].env_file` at the same envrc so that `CLAUDE_CONFIG_DIR` is consistent across conductor and children. That also smuggled `TELEGRAM_STATE_DIR` into every child joining the group, and the telegram plugin auto-started a second `bun telegram` poller per child — all racing the same bot token via `getUpdates` (single-consumer API). Observed: 10 concurrent pollers on one bot token, ~10% delivery rate to the intended conductor, no error surfaced. Fix: in `buildEnvSourceCommand`, after sourcing the group env_file, `conductorOnlyEnvStripExpr` emits `unset TELEGRAM_STATE_DIR` when the session is (a) not a conductor itself AND (b) in a group paired with a `[conductors.<group>]` block. Conductors keep the variable; unrelated groups are unchanged; no schema change. Tests: `TestIssue680_ChildSession_StripsTelegramStateDir`, `TestIssue680_ConductorSession_KeepsTelegramStateDir`, `TestIssue680_ChildSession_NoConductorBlock_NoUnset`, `TestIssue680_ChildSession_NoGroupEnvFile_NoUnset` in `internal/session/issue680_env_leak_test.go`. Doc updated in `conductor/conductor-claude.md`.
- **`agent-deck feedback` now requires explicit consent before posting publicly** ([#679](https://github.com/asheshgoplani/agent-deck/issues/679), reported by @rgarlik): the feedback CLI posted comments to the public [Feedback Hub discussion](https://github.com/asheshgoplani/agent-deck/discussions/600) using the user's local `gh` CLI authentication — under their own GitHub account, visible to anyone browsing the discussion — with no disclosure before submission. @rgarlik described this as "tacky and a bit creepy" and noted they would not have left feedback had they known. Fix: the CLI now (1) saves the rating to local state BEFORE the disclosure, so declining does not re-prompt on the next run; (2) prints an explicit disclosure block — public URL, "posted via the `gh` CLI", `@<login>` as fetched by `gh api user -q .login` (with `your GitHub account` fallback), and the exact body that will be posted (the `FormatComment` output, not a prettier lookalike that could drift); (3) prompts `Post this? [y/N]:` with default-N — only `y`/`yes` case-insensitive after trim confirms; (4) on confirm, bypasses `sender.Send()` and calls `gh api graphql` directly, so the clipboard-and-browser fallback can NEVER fire from the CLI path; (5) on gh failure, prints `Error: could not post via gh. Feedback was NOT sent.` plus a `gh auth status` recovery hint and exits non-zero with no side effects. Tests: `TestIssue679_ConfirmN_DoesNotPost`, `TestIssue679_ConfirmY_GhSuccess_Posts`, `TestIssue679_ConfirmY_GhFailure_NoFallback`, `TestIssue679_EmptyConfirm_DefaultNo`, `TestIssue679_Confirm_UppercaseY`, `TestIssue679_Confirm_WhitespaceY`, `TestIssue679_Disclosure_PreviewMatchesFormatComment`, `TestIssue679_Disclosure_ShowsLogin`, `TestIssue679_Disclosure_LoginFallback`, `TestIssue679_OptOut_Unchanged` in `cmd/agent-deck/feedback_cmd_test.go`. README Feedback section rewritten; `agent-deck feedback --help` documents the flow. Scope locked to the CLI: the TUI feedback dialog (`internal/ui/feedback_dialog.go`) is unchanged. A private/anonymous feedback channel is being designed for a future release — track in #679.

### Added (carried from previously-untagged chore-release work)
- **Transition notifications can be suppressed globally or per-session** (community PR [#580](https://github.com/asheshgoplani/agent-deck/pull/580) by @johnuopini, rebased onto current main + dispatch-level regression test added by maintainers, previously slated for v1.7.34): the transition daemon (`agent-deck notify-daemon`) unconditionally sent a tmux message to the parent session whenever a child transitioned `running → waiting|error|idle`, which is the right default for conductor patterns but wrong for users who want a child to run silently (batch workloads, one-shot scripts, sessions where the parent is interactive and shouldn't be interrupted). Three layered controls: (1) a global kill switch `[notifications].transition_events = false` in `~/.agent-deck/config.toml` (default `true` via `NotificationsConfig.GetTransitionEventsEnabled()`, nil-safe); (2) a per-instance `NoTransitionNotify` field set at creation via `--no-transition-notify` on both `agent-deck add` and `agent-deck launch`; (3) a runtime toggle `agent-deck session set-transition-notify <id> <on|off>`. Three guard sites, defense in depth: `TransitionDaemon.syncProfile` and `TransitionDaemon.emitHookTransitionCandidates` check both flags before building an event; `TransitionNotifier.dispatch` re-checks `child.NoTransitionNotify` before calling `SendSessionMessageReliable` so deferred/retried events that survive a daemon restart also honour the flag. SQLite schema **v6** adds `instances.no_transition_notify INTEGER NOT NULL DEFAULT 0` with an idempotent `ALTER TABLE` path. JSON round-trip uses `omitempty`. Suppression affects **dispatch only** — parent linking is untouched, so `session show` still reports the parent. Tests: `TestUserConfig_TransitionEventsDefault`, `TestUserConfig_TransitionEventsExplicitFalse`, `TestSyncProfileSkipsWhenInstanceNoTransitionNotify`, `TestDispatchDropsEventWhenChildNoTransitionNotify`, `TestInstanceNoTransitionNotifyJSONRoundTrip` in `internal/session/transition_notifier_test.go`. Co-credit: @johnuopini (PR #580) for the three-layer design, the schema v6 migration, and the CLI plumbing; maintainers rebased across the v1.7.25–v1.7.33 main advance and added the dispatch-level regression test.
- **Custom tools can declare `compatible_with = "claude"` or `"codex"` to opt into built-in compatibility behavior** (community PR [#655](https://github.com/asheshgoplani/agent-deck/pull/655) by @johnrichardrinehart, rebased onto current main by maintainers, previously slated for v1.7.33): a custom tool's compatibility with built-ins (Claude resume semantics, Codex session-ID detection and resume, restart flow) was inferred by parsing the tool's `command` field for a literal `claude`/`codex` basename. Users wrapping those CLIs in a shell script (`codex-wrapper`, `claude-env`) lost every downstream capability gate. The new `compatible_with` field in `[tools.<name>]` is an explicit opt-in that promotes the wrapped tool into the corresponding built-in's behavior set while **preserving the custom tool identity** (so `Instance.Tool` stays `my-codex`, not `codex`, and `UpdateStatus`'s tmux content-sniff detection does not clobber the configured name once a built-in CLI is detected inside the wrapper). Refactor unifies `isClaudeCommand` / `isCodexCommand` behind a shared `isCommand(command, wantBase)` helper; `buildCodexCommand` now resumes through the custom wrapper command (`codex-wrapper resume <id>`) instead of the hard-coded literal `codex`. `CreateExampleConfig` gains a documented `# Example: Custom Codex wrapper` block. Tests: `TestIsCodexCompatible_CustomToolCommands`, `TestIsClaudeCompatible_CustomToolCommands` in `internal/session/userconfig_test.go`; `TestBuildCodexCommand_CustomWrapperPreservesToolIdentity` and `TestCanRestart_CustomCodexWrapperWithKnownID` in `internal/session/instance_test.go`; `TestCreateExampleConfigDocumentsCompatibleWith` in `internal/session/userconfig_test.go`. Co-credit: @johnrichardrinehart (PR #655) for design and implementation; maintainers rebased across the v1.7.25–v1.7.32 switch-statement expansions.

## [1.7.34] - 2026-04-19

### Added
- **Transition notifications can be suppressed globally or per-session** (community PR [#580](https://github.com/asheshgoplani/agent-deck/pull/580) by @johnuopini, rebased onto current main + dispatch-level regression test added by maintainers): the transition daemon (`agent-deck notify-daemon`) unconditionally sent a tmux message to the parent session whenever a child transitioned `running → waiting|error|idle`, which is the right default for conductor patterns but wrong for users who want a child to run silently (batch workloads, one-shot scripts, sessions where the parent is interactive and shouldn't be interrupted). This release adds three layered controls: (1) a global kill switch `[notifications].transition_events = false` in `~/.agent-deck/config.toml` (default `true` via `NotificationsConfig.GetTransitionEventsEnabled()`, nil-safe); (2) a per-instance `NoTransitionNotify` field set at creation via `--no-transition-notify` on both `agent-deck add` and `agent-deck launch`; (3) a runtime toggle `agent-deck session set-transition-notify <id> <on|off>`. Three guard sites, defense in depth: `TransitionDaemon.syncProfile` and `TransitionDaemon.emitHookTransitionCandidates` (the two daemon entry points) check both flags before building an event; `TransitionNotifier.dispatch` re-checks `child.NoTransitionNotify` before calling `SendSessionMessageReliable`, so deferred/retried events that survive a daemon restart also honour the flag. SQLite schema **v6** adds `instances.no_transition_notify INTEGER NOT NULL DEFAULT 0` with a `CREATE IF NOT EXISTS`-safe `ALTER TABLE` path (idempotent via duplicate-column check). JSON round-trip uses `omitempty` so existing session records don't grow. Parent linking itself is untouched — suppression affects **dispatch only**, so `session show` still reports the parent and the link survives suppression toggles. Tests: `TestUserConfig_TransitionEventsDefault`, `TestUserConfig_TransitionEventsExplicitFalse` (nil-safe getter contract), `TestSyncProfileSkipsWhenInstanceNoTransitionNotify` (resolver reachability check), `TestDispatchDropsEventWhenChildNoTransitionNotify` (the dispatch-level regression test added during PR review — exercises the full `NotifyTransition → dispatch → guard` path end-to-end with a real profile-scoped Storage, asserts `transitionDeliveryDropped` when the flag is true so a future refactor that relocates the guard into the daemon layer only cannot silently regress), `TestInstanceNoTransitionNotifyJSONRoundTrip` (omitempty contract) in `internal/session/transition_notifier_test.go`. Co-credit: @johnuopini (PR #580) for the three-layer design, the schema v6 migration, and the CLI plumbing; maintainers rebased across the v1.7.25–v1.7.33 main advance and added the dispatch-level regression test.

## [1.7.33] - 2026-04-19

### Added
- **Custom tools can declare `compatible_with = "claude"` or `"codex"` to opt into built-in compatibility behavior** (community PR [#655](https://github.com/asheshgoplani/agent-deck/pull/655) by @johnrichardrinehart, rebased onto current main by maintainers): previously, a custom tool's compatibility with built-ins (Claude resume semantics, Codex session-ID detection and resume, restart flow) was inferred by parsing the tool's `command` field for a literal `claude`/`codex` basename. Users wrapping those CLIs in a shell script (`codex-wrapper`, `claude-env`) lost every downstream capability gate — `IsClaudeCompatible` / `IsCodexCompatible` returned false, `buildCodexCommand` refused to prepend `CODEX_HOME`, and `Restart()` wouldn't reuse the captured `CodexSessionID`. The new `compatible_with` field in `[tools.<name>]` is an explicit opt-in that promotes the wrapped tool into the corresponding built-in's behavior set while **preserving the custom tool identity** (so `Instance.Tool` stays `my-codex`, not `codex`, and `UpdateStatus`'s tmux content-sniff detection does not clobber the configured name once a built-in CLI is detected inside the wrapper). Refactor unifies `isClaudeCommand` / `isCodexCommand` behind a shared `isCommand(command, wantBase)` helper, and `buildCodexCommand` now resumes through the custom wrapper command (`codex-wrapper resume <id>`) instead of the hard-coded literal `codex`. `CreateExampleConfig` gains a documented `# Example: Custom Codex wrapper` block (field docs + example TOML with `compatible_with = "codex"`). Tests: `TestIsCodexCompatible_CustomToolCommands` (4 cases: built-in, `compatible_with=codex`, env-prefixed exact `codex`, plain wrapper without opt-in) and `TestIsClaudeCompatible_CustomToolCommands` (adds `compatible_with=claude` case) in `internal/session/userconfig_test.go`; `TestBuildCodexCommand_CustomWrapperPreservesToolIdentity` (verifies `AGENTDECK_TOOL=my-codex` tmux env and resume-through-wrapper) and `TestCanRestart_CustomCodexWrapperWithKnownID` in `internal/session/instance_test.go`; `TestCreateExampleConfigDocumentsCompatibleWith` in `internal/session/userconfig_test.go`. Co-credit: @johnrichardrinehart (PR #655) for design and implementation; maintainers rebased across the v1.7.25–v1.7.32 switch-statement expansions (added `copilot` to `isBuiltinToolName`, preserved the rebased commit authorship).

## [1.7.32] - 2026-04-19

### Added
- **Project skills now work for Gemini, Codex, and Pi sessions — not just Claude** (community PR [#675](https://github.com/asheshgoplani/agent-deck/pull/675) by @masta-g3, cherry-picked onto current main after the parent branch landed in v1.7.31): `agent-deck skill attach` and the TUI Skills Manager (`s`) previously hard-gated on `IsClaudeCompatible`, materializing every project skill into `<project>/.claude/skills/`. This release generalizes attachment to a runtime-specific destination: Claude-compatible sessions keep writing to `.claude/skills/`, while Gemini, Codex, and Pi sessions now materialize into `<project>/.agents/skills/`. The `.agents/skills/` path is the cross-tool convention Anthropic published Dec 2025 and that Codex CLI, Gemini CLI, and GitHub Copilot CLI all auto-discover, so skills attached via agent-deck are picked up by those runtimes with no further configuration. The global source registry (`~/.agent-deck/skills/sources.toml`) and the per-project manifest format (`<project>/.agent-deck/skills.toml`) are unchanged: the manifest is still authoritative, and the materialized dirs are derived from it. Three explicit migration cases are handled in `attachSkillCandidate` and `ApplyProjectSkills`: (1) **fresh attach** materializes into the active runtime's root; (2) **re-materialize stale managed target**, where the manifest still owns the skill but the on-disk target is missing, re-materializing in place; (3) **migrate between managed roots**, where the manifest entry points under the other managed root (e.g. the session was restarted with a different `-c` flag), materializing into the active root first, then removing the old target and updating `TargetPath`. When the original `SourcePath` is unavailable but the old managed target is still readable, the new root is rebuilt from the existing managed target (copy-only; no symlink indirection since the source is gone). If neither source nor old target is readable, migration fails loudly without mutating the manifest first. `skill attached` inspects both known managed roots so stale/unmanaged entries remain visible across runtime switches. The TUI Skills Manager gains a `needsReconcile` flag: if any attached skill's `TargetPath` doesn't match the active runtime's expected dir, pressing Enter runs Apply even when the user made no manual changes, triggering the migration automatically when a Claude session is restarted as Gemini/Codex/Pi. Auto-restart after attach/detach fires for Claude, Gemini, and Codex; Pi is opted out of auto-restart since Pi does not yet hot-reload skills (users must manually reload). Defense-in-depth around detach: `removeAttachmentTarget` requires the target to be under a known managed skill dir (`.claude/skills` or `.agents/skills`) AND the resolved absolute path must stay inside the base, blocking `..`-traversal even if the manifest were hand-edited. Tests: `TestProjectSkillsDirMapping` (5 cases: claude/gemini/codex/pi/shell), `TestSkillRuntime_AttachUsesAgentSkillsDirForGemini`, `TestSkillRuntime_ApplyMigratesBetweenManagedRoots`, `TestSkillRuntime_AttachMigratesFromExistingTargetWhenSourceUnavailable`, `TestSkillRuntime_ApplyMigratesFromExistingTargetWhenSourceUnavailable`, `TestSkillRuntime_DetachRemovesAgentSkillsTarget` in `internal/session/skills_runtime_test.go`; `TestSkillDialog_Show_SupportedNonClaudeSession`, `TestSkillDialog_ApplyUsesAgentSkillsDirForGemini`, `TestSkillDialog_ShowMarksReconcileNeededForRuntimeSwitch` in `internal/ui/skill_dialog_test.go`; `TestApplyProjectSkills_RejectsLegacyFileSkill` in `internal/session/skills_catalog_test.go`. Co-credit: @masta-g3 (PR #675) for the design and implementation; cherry-picked onto current main by maintainers.

## [1.7.31] - 2026-04-19

### Fixed
- **Pi (Inflection AI's `pi` CLI) is now detected as a first-class tool in CLI and TUI session creation paths** (community PR [#674](https://github.com/asheshgoplani/agent-deck/pull/674) by @masta-g3, rebased onto current main as the original branch was stale): `agent-deck add -c pi .` and TUI session creation both produced `Tool="shell"` with `Command="pi"`, even though the rest of the framework (tmux content detection in `internal/tmux/tmux.go`, userconfig builtin registration, pattern detection, GetToolIcon) was already wired for Pi. Two missed call sites: `cmd/agent-deck/main.go::detectTool` (the free-form `-c` parser) and `internal/ui/home.go` (the TUI session creation switch). `detectTool` now recognises `pi` via a new `hasCommandToken` helper that does whitespace-token matching rather than `strings.Contains`, so short ambiguous names like "pi" do not get hijacked by substrings of unrelated words ("epic", "tapioca", "spider", "happiness"). The TUI's inline tool-mapping switch is extracted into a reusable `createSessionTool(command) (tool, command)` and given a `pi` case. Tests: `TestDetectTool_Pi` (5 cases including the false-match guards) in `cmd/agent-deck/copilot_detect_test.go`; `TestCreateSessionTool_Pi` in `internal/ui/home_test.go`. Co-credit: @masta-g3 (PR #674) for the original pattern; rebased + extended with the substring-false-match cases by maintainers.

## [1.7.30] - 2026-04-19

### Fixed
- **Per-session color tint now actually renders in the TUI** (issue [#391](https://github.com/asheshgoplani/agent-deck/issues/391)): PR #650 (v1.7.27) added the `Instance.Color` field, TOML validation, CLI plumbing (`agent-deck session set <id> color '#FF0000'`), SQLite persistence, and `list --json` exposure — but the TUI dashboard never consumed the field, so users setting a color saw it round-trip through storage yet every row kept the default palette. `renderSessionItem` now overrides the title foreground with `lipgloss.Color(Instance.Color)` when the field is non-empty, preserving the bold/underline weight cues that distinguish Running/Waiting/Error states for colorblind users. Empty `Color` is the default and leaves rendering byte-identical to v1.7.29 (fully opt-in). Accepts both accepted formats from `isValidSessionColor`: `#RRGGBB` truecolor hex and `0..255` ANSI 256-palette index. Tests: `TestIssue391_SessionRow_{HexColorRenderedAsForeground,ANSIIndexColorRendered,EmptyColorLeavesRowUntinted}` in `internal/ui/issue391_tui_test.go` (Seam A, per `internal/ui/TUI_TESTS.md`).

## [1.7.29] - 2026-04-19

### Added
- **`agent-deck group change <source> [<dest>]` — reparent an entire group subtree** (issue [#447](https://github.com/asheshgoplani/agent-deck/issues/447)): groups can now be moved as a unit, taking all their subgroups and sessions along. `group change personal/project1 work` places `project1` (and everything beneath it) under `work`, rewriting every descendant path in one atomic persist. Passing an empty destination (`group change work/project1 ""` or simply omitting it) promotes the group back to root level. The new `GroupTree.MoveGroupTo(source, destParent)` engine refuses circular moves (dest == source or a descendant of source), collisions at the target path, and moving the protected default group. Tests: `TestMoveGroupTo_{ToRoot,ToOtherParent,WithSubgroups,DestMissing,Circular,NoOpSameParent,Collision,SourceMissing,DefaultGroupForbidden}` in `internal/session/groups_reorganize_test.go`; `TestGroupChange_{RootToSubgroup,MoveToRoot,RejectsCircular}` end-to-end CLI in `cmd/agent-deck/group_change_test.go`. TUI group-move dialog is intentionally deferred to a follow-up — the CLI is the minimum shippable surface for the feature.
- **`agent-deck session search <query>` — full-content search across Claude sessions** (issue [#483](https://github.com/asheshgoplani/agent-deck/issues/483)): the global-search index that powers the TUI's (currently-disabled) `G` overlay is now exposed as a first-class CLI so users can grep their conversation history from scripts and one-liners. Returns matching `SessionID`, `cwd`, and a 60-char snippet around the first match; `--json` emits a machine-readable shape with `{query, results, count}`. Flags: `--limit N` (default 20), `--days N` (default 30 — searches files modified in the last N days; `0` = all), `--tier {instant|balanced|auto}` (default auto — switches based on corpus size). Honours `CLAUDE_CONFIG_DIR` so per-profile `cdp` / `cdw` setups search the right tree. Test isolation fix (strip `CLAUDE_CONFIG_DIR=` from subprocess env in `runAgentDeck`) prevents CLI test suites from leaking into the developer's real `~/.claude`. Tests: `TestSessionSearch_{FindsMessageContent,EmptyQuery,NoMatches}` in `cmd/agent-deck/session_search_test.go`.

## [1.7.28] - 2026-04-19

### Added
- **Auto-sync session title from Claude Code's `--name` / `/rename`** (issue [#572](https://github.com/asheshgoplani/agent-deck/issues/572)): when a user starts `claude --name my-feature-branch` inside an agent-deck session, or runs `/rename …` mid-session, the agent-deck title now syncs automatically on the next hook event (SessionStart, UserPromptSubmit, Stop — whichever fires first, typically within seconds). Implementation piggybacks on the existing `hook-handler` event-driven flow: after writing status, `applyClaudeTitleSync(instanceID, sessionID)` scans `~/.claude/sessions/*.json` for the matching `sessionId`, reads the `name` field, and updates the stored title if non-empty and different. Sessions started without `--name` keep their auto-generated adjective-noun title (no change from current behavior). No extra process spawn, no polling — every existing hook event already pays the filesystem cost for status writes. Tests: `TestFindClaudeSessionName_{MatchBySessionID,NoMatch,EmptyNameField,MissingSessionsDir}`, `TestApplyClaudeTitleSync_{UpdatesInstance,NoopWhenNameMissing,NoopWhenNameEqualsTitle}` in `cmd/agent-deck/hook_name_sync_test.go` (7 cases).
- **`agent-deck session move <id> <new-path> [--group …] [--no-restart] [--copy]`** (issue [#414](https://github.com/asheshgoplani/agent-deck/issues/414)): new CLI verb that wraps what used to be a 4-step manual ritual (`session set path` + `group move` + `cp ~/.claude/projects/<old-slug>/` + `session restart`) into one atomic command. Migrates the Claude Code conversation history at `~/.claude/projects/<slug>/` to the new slugified path so `claude --resume` in the new location picks up prior turns. `--copy` preserves the old dir instead of renaming (useful when other sessions share history). `--group` moves to a target group in the same operation. `--no-restart` skips the default post-move restart. Shares `SlugifyClaudeProjectPath` with the costs sync path so both call sites encode `/` and `.` identically (was previously duplicated in `internal/costs/sync.go`). Tests: `TestSessionMove_{UpdatesPath,MigratesClaudeProjectDir,CopyFlagPreservesOldDir,GroupFlag,MissingArguments}` in `cmd/agent-deck/session_move_test.go` (5 cases).

### Fixed
- **`TestWatcherEventDedup` -race flake** (pre-existing): `SaveWatcherEvent` now retries up to 5 times on SQLITE_BUSY with linear backoff (10ms, 20ms, …). The op is `INSERT OR IGNORE`-idempotent so retries are safe. Was failing reliably on release CI under concurrent inserts from two goroutines sharing the same dedup key; retrying resolves the race without weakening the dedup invariant (still exactly 1 row after N racers).

## [1.7.27] - 2026-04-19

### Fixed
- **`sessionHasConversationData` false-negatives caused `--session-id` instead of `--resume` despite rich jsonl on disk** (issue [#662](https://github.com/asheshgoplani/agent-deck/issues/662)): when a conductor's Claude session was restarted while the SessionEnd hook was still flushing the jsonl (a ~100–150ms window), `buildClaudeResumeCommand` would observe the file as not-yet-written, fall through to `--session-id`, and hand the user a blank conversation even though the historic jsonl was on disk. Two layers of fix: (1) a bounded retry-once at the call site (`resumeCheckRetryDelay = 200ms`) that re-checks after the flush window closes, firing only when the first check is negative AND `ClaudeSessionID` is non-empty so the happy path is untouched; (2) a new `session_data_decision` structured log line carrying `config_dir`, `resolved_project_path`, `encoded_path`, `primary_path_tested`, `primary_path_stat_err`, `fallback_lookup_tried`, `fallback_path_found`, and `final_result` so production false-negatives can be diagnosed from logs alone without attaching a debugger. Tests: `TestIssue662_HiddenDirInPath_EncodesToDoubleDash`, `TestIssue662_FindsFileViaFallback_WhenPrimaryPathMisses`, `TestIssue662_DiagnosticLog_CapturesAllDecisionFields`, `TestIssue662_BuildClaudeResumeCommand_RetriesOnceOnSessionEndRace` in `internal/session/issue662_session_data_diag_test.go`.

### Deferred
- **Tmux control-client supervision** (issue [#659](https://github.com/asheshgoplani/agent-deck/issues/659)): deferred to its own design cycle. #659's own body notes that "Pipe-death is already recovered" by the v1.7.8 reviver and frames the control-client wrapping as a structural improvement rather than a bug fix, with four open design questions (per-instance vs shared service, TUI coordination, per-user vs global, CLI-without-TUI behaviour). Tracked under issue [#668](https://github.com/asheshgoplani/agent-deck/issues/668) as an RFC to pick the shape before any code lands.

## [1.7.26] - 2026-04-18

### Added
- **GitHub Copilot CLI support** (issue [#556](https://github.com/asheshgoplani/agent-deck/issues/556)): Agent Deck now recognises the standalone `copilot` binary from `@github/copilot` (GA 2026-02-25) as a first-class tool identity alongside `claude`, `gemini`, `codex`, and `opencode`. `agent-deck add -c copilot .` lands on `Tool="copilot"` instead of the generic `shell` fallback, so sessions get the right status detection, the right icon (🐙), and the right per-tool config path. A new `[copilot]` TOML block (`env_file` for now) gives users a home for future knobs without schema churn. The `CopilotOptions` envelope mirrors the existing Claude/OpenCode shape (`SessionMode` + `ResumeSessionID`) and emits `--resume` (picker) or `--resume <id>` (direct). `IsClaudeCompatible("copilot")` is deliberately **false** — Copilot is not a Claude wrapper, so Claude-only surfaces (`--channels`, `--extra-arg`, skill injection, MCP hook paths) stay off. This ships the foundation; deeper hook-based session-id capture (analogous to `internal/session/gemini.go` analytics) will land as a follow-up once Copilot CLI's on-disk session format stabilises. Tests: `TestCopilotOptions_{ToolName,ToArgs,MarshalUnmarshalRoundtrip}`, `TestNewCopilotOptions_{Defaults,WithConfig}`, `TestUnmarshalCopilotOptions_WrongTool`, `TestIsClaudeCompatible_CopilotNotCompatible`, `TestGetToolIcon_Copilot`, `TestGetCustomToolNames_CopilotIsBuiltin`, `TestNewInstanceWithTool_Copilot` in `internal/session/copilot_test.go`; `TestDetectToolFromCommand_Copilot`, `TestDefaultRawPatterns_Copilot` in `internal/tmux/copilot_test.go`; `TestDetectTool_Copilot` in `cmd/agent-deck/copilot_detect_test.go`.

## [1.7.25] - 2026-04-18

### Added
- **Per-session color tint (plumbing)** (issue [#391](https://github.com/asheshgoplani/agent-deck/issues/391)): sessions now carry an optional `color` field accepting `"#RRGGBB"` truecolor hex or an ANSI-256 palette index (`"0"`..`"255"`). Set via `agent-deck session set <id> color "#ff00aa"`, clear with `agent-deck session set <id> color ""`. The field persists through the SQLite `tool_data` blob and is exposed via `agent-deck list --json`. Validation runs at the CLI boundary so typos (`"red"`, malformed hex, out-of-range ints) are rejected with a diagnostic rather than silently stored. This PR ships the plumbing only — TUI row rendering that consumes the field will land as a follow-up so the change is risk-free for users who don't opt in (default: empty string = no tint, rendering unchanged). Tests: `TestIsValidSessionColor` (17 cases) + `TestSessionSetColor_PersistsValidAndRejectsInvalid` (end-to-end CLI round-trip).
- **Watcher feature documentation** (issue [#628](https://github.com/asheshgoplani/agent-deck/issues/628)): `agent-deck watcher --help` now documents each adapter type (webhook, github, ntfy, slack) with a concrete usage example, required flags, and a pointer to the conversational `watcher-creator` skill. README gains a dedicated **Watchers** section describing event routing, per-type flags, routing rules in `~/.agent-deck/watcher/<name>/clients.json`, and safety guarantees (HMAC-SHA256 verification on GitHub, SQLite event dedup). No behavior change — docs only. Regression test: `TestWatcherHelp_MentionsAdapterExamples`.
- **`[tmux].detach_key` config alias for the PTY-attach detach key** (issue [#434](https://github.com/asheshgoplani/agent-deck/issues/434)): the detach key was already configurable via `[hotkeys].detach = "ctrl+d"`, but reporters were looking under `[tmux]` since they think of detach as a tmux-attach concern. This release adds `[tmux].detach_key` as an explicit alias with clear precedence — `[hotkeys].detach` always wins when both are set, so the alias never changes behavior for users who already configured the hotkey. Default (no config) remains `Ctrl+Q`. Also documents `[hotkeys].detach` in the embedded config template so the feature is discoverable at setup time. Tests: `TestDetachKey_ConfigurableViaToml` (6 sub-cases) in `internal/session/userconfig_test.go`.

### Fixed
- **Sessions silently disappearing from their assigned group after TUI creation** (issue [#666](https://github.com/asheshgoplani/agent-deck/issues/666)): the `createSessionFromGlobalSearch` path at `internal/ui/home.go:4762` called `h.getCurrentGroupPath()` directly and passed its return value into `session.NewInstanceWithGroupAndTool`. When the cursor sat on a flatItem that is neither a group nor a session (`ItemTypeWindow`, `ItemTypeRemoteGroup`, `ItemTypeRemoteSession`, or a creating-placeholder) the return was `""`, and the constructor unconditionally overrode the `extractGroupPath` default with it — producing `inst.GroupPath=""`. The storage layer persisted `''` and the next reload silently re-derived via `extractGroupPath(ProjectPath)`, surfacing the session under a path-derived group ("tmp", "home", etc.). Exact user-reported symptom: *"session created in group X ends up in a different group, sometimes with a path-derived name."* Fix: new helper `Home.resolveNewSessionGroup()` wraps `getCurrentGroupPath` with a rescue chain (scoped group → `DefaultGroupPath`) so the empty string never reaches the constructor. Belt-and-braces guards in `storage.go` normalize any remaining empties at save + load as defense-in-depth — the load-time fallback now routes empties to `DefaultGroupPath` and emits a `warn` log (was: silent re-derive). Verified end-to-end with a three-config revert-dance: baseline v1.7.24 reproduces the exact symptom (`"GroupPath after reload = tmp, want agent-deck"`), partial-fix reproduces the belt-and-braces-only case (`"GroupPath collapsed to my-sessions"`), both-fixes-on passes. Tests: `TestIssue666_ResolveNewSessionGroup_*`, `TestIssue666_GlobalSearchImport_EndToEnd_PreservesGroupAcrossReload` in `internal/ui/issue666_tui_test.go`; `TestIssue666_LoadRowWithEmptyGroupPath_FallsBackToDefaultNotPathDerived`, `TestIssue666_LoadRowWithExplicitGroupPath_IsPreserved`, `TestIssue666_SaveWithGroups_NormalizesEmptyGroupPath` in `internal/session/issue666_test.go`.
- **`conductor setup` now auto-remediates `enabledPlugins.telegram` = true** (issue [#666](https://github.com/asheshgoplani/agent-deck/issues/666), mechanism 1): v1.7.22 only warned on stderr, users missed the warning in long setup logs, and generic child claude sessions kept flipping to `error` state when the auto-loaded telegram plugin raced the conductor's poller (409 Conflict → claude exits). Setup now flips the flag to `false` in `<profile>/settings.json`, preserves all other keys, and prints a loud `✓ Auto-disabled …` stdout line. Idempotent; missing file / missing key / already-false are all no-ops. Tests: `TestDisableTelegramGlobally_*` in `cmd/agent-deck/conductor_cmd_telegram_autofix_test.go`.
- **Respawn-pane restart path now sweeps duplicate cross-tmux tool sessions** (issue [#666](https://github.com/asheshgoplani/agent-deck/issues/666), mechanism 3): the fallback restart branch at `instance.go:4411` already killed other agentdeck tmux sessions sharing the same `CLAUDE_SESSION_ID` (issue #596 guard). The primary respawn-pane branches did not. Under rare fork-then-edit collisions two agentdeck sessions could run `claude --resume` on the same conversation, stacking two telegram pollers. The new `Instance.sweepDuplicateToolSessions()` helper runs on every successful respawn for Claude, Gemini, OpenCode, and Codex. Tests: `TestIssue666_SweepDuplicateToolSessions_{Claude,Gemini,OpenCode,Codex,SkipsWhenNoSessionID,SkipsWhenNoTmux}` in `internal/session/issue666_restart_sweep_test.go`.
## [1.7.13] - 2026-04-17

### Fixed
- **Cross-session `x` send-output transferred unpredictable content** (issue [#598](https://github.com/asheshgoplani/agent-deck/issues/598)): when the user pressed `x` to transfer output from session A to session B, the transferred text was often from a *prior* conversation rather than the most-recent assistant response. Root cause: `getSessionContent` read the last assistant message via `Instance.ClaudeSessionID`, but that stored ID goes stale every time Claude is resumed — it continues pointing at the prior JSONL while the live `CLAUDE_SESSION_ID` in tmux env holds the current UUID. The CLI `session output` path already used `GetLastResponseBestEffort` with stale-ID recovery; the TUI path didn't. Fix adds `Instance.RefreshLiveSessionIDs()` (Claude + Gemini) and routes `getSessionContent` through a testable `getSessionContentWithLive(inst, liveID)` helper that prefers the live tmux env ID over any stored value before the JSONL lookup. Tmux scrollback fallback is unchanged. Tests: `TestGetSessionContentWithLive_PrefersFreshIDOverStoredStaleID`, `TestGetSessionContentWithLive_KeepsStoredIDWhenLiveEmpty`, `TestGetSessionContentWithLive_NoOpForNonClaudeTool` in `internal/ui/send_output_content_test.go`; `TestInstance_RefreshLiveSessionIDs_NoOpWhenTmuxSessionNil`, `TestInstance_RefreshLiveSessionIDs_NoOpForNonAgenticTool` in `internal/session/instance_test.go`.

## [1.7.10] - 2026-04-17

### Fixed
- **`session send --no-wait` reliability on freshly-launched Claude sessions** (issue [#616](https://github.com/asheshgoplani/agent-deck/issues/616)): the pre-v1.7.10 code skipped all readiness detection in `--no-wait` mode, then ran a 1.2-second verification loop. On cold Claude launches (where TUI mount takes 5-40s with MCPs), the loop counted startup-animation "active" status as submission success and returned before the composer rendered — leaving the pasted message typed-but-not-submitted. The 30-50% failure rate users reported is now 0% in 10 consecutive live-boundary runs. Fix has three layers: a 5s preflight barrier waiting for the Claude composer `❯` to render, a 500ms post-composer settle for React mount, and an extended 6s verification budget (from 1.2s). `maxFullResends=-1` is preserved — the #479 regression (double-send) still passes. Non-Claude tools skip the preflight (their prompt shapes differ). Tests: `TestSendNoWait_ReEntersWhenComposerRendersLate`, `TestAwaitComposerReadyBestEffort_*`, `TestSendWithRetryTarget_NoWait_BudgetSpansRealisticClaudeStartup` in `cmd/agent-deck/session_send_test.go`.

## [1.7.6] - 2026-04-17

### Fixed
- **Priority inversion on `CLAUDE_CONFIG_DIR`**: explicit `[conductors.<name>.claude]` and `[groups."<name>".claude]` TOML overrides now beat the shell-wide `CLAUDE_CONFIG_DIR` env var. Previously, developer shells that exported `CLAUDE_CONFIG_DIR` via profile aliases (`cdp`/`cdw`) silently shadowed every per-conductor/per-group override — making config.toml overrides unreliable for the exact users most likely to use them. Profile/global fallbacks remain weaker than env (they're shell-wide too). Scope: `GetClaudeConfigDirForInstance`, `GetClaudeConfigDirSourceForInstance`, `IsClaudeConfigDirExplicitForInstance` in `internal/session/claude.go`. Group-less variants unchanged.
- **Web terminal `TestTmuxPTYBridgeResize` -race flake**: added `ptmxMu sync.RWMutex` protecting the PTY file handle against concurrent Close/Resize. Previously intermittent on GH Actions release runs (v1.7.4, v1.7.5).

## [1.5.4] - 2026-04-16

### Added
- Per-group Claude config overrides (`[groups."<name>".claude]`). (Base implementation by @alec-pinson in [PR #578](https://github.com/asheshgoplani/agent-deck/pull/578))
- In-product feedback feature: CLI `agent-deck feedback`, TUI `Ctrl+E`, three-tier submit (GraphQL, clipboard, browser).

### Fixed
- Session persistence: tmux servers now survive SSH logout on Linux+systemd hosts via `launch_in_user_scope` default (v1.5.2 hotfix). ([docs/SESSION-PERSISTENCE-SPEC.md](docs/SESSION-PERSISTENCE-SPEC.md))
- Custom-command Claude sessions (conductors) now resume from latest JSONL on restart.

## [1.6.0] - 2026-04-16

v1.6.0 is the Watcher Framework milestone. Event-driven automation via five adapter types (webhook, ntfy, GitHub, Slack, Gmail), a self-improving routing engine, health alerts bridge, and conductor-style on-disk layout.

### Added
- **Watcher engine** — event-driven automation framework with five adapters (webhook, ntfy, GitHub, Slack, Gmail), SQLite-backed dedup, HMAC-SHA256 verification, and self-improving routing via triage sessions. See `internal/watcher/`.
- **Watcher health alerts bridge** — opt-in `[watcher.alerts]` config block wires engine health state to Telegram/Slack/Discord with per-(watcher x trigger) 15-minute debounce. See `internal/watcher/health_bridge.go`. Closes REQ-WF-3.
- **Watcher folder hierarchy** — on-disk state reorganized to `~/.agent-deck/watcher/` (singular) mirroring the conductor folder pattern. Shared files (CLAUDE.md, POLICY.md, LEARNINGS.md, clients.json) at root, per-watcher subdirs (meta.json, state.json, task-log.md). Closes REQ-WF-6.
- **Per-watcher health fields** — `agent-deck watcher list --json` now exposes `last_event_ts`, `error_count`, `health_status` per watcher.
- **Watcher CLI** — 8 subcommands: create, start, stop, status, list, logs, import, install-skill.

### Changed
- **BREAKING: Watcher data directory renamed** — `~/.agent-deck/watchers/` is now `~/.agent-deck/watcher/` (singular). A compatibility symlink `watchers -> watcher/` is created automatically on first boot so existing scripts continue to work. The symlink will be removed in v1.7.0. Update any hardcoded paths.

## [1.5.1] - 2026-04-13

Patch release fixing 7 bugs reported by users and merging 3 community PRs.

### Fixed
- Clear host terminal scrollback on session detach. ([#419](https://github.com/asheshgoplani/agent-deck/issues/419))
- Web terminal resize now uses pty.Setsize + tmux resize-window for correct dimensions. ([#568](https://github.com/asheshgoplani/agent-deck/pull/568))
- Narrow controlSeqTimeout to ESC-only and ignore SIGINT during attach, fixing Ctrl+C forwarding. ([#571](https://github.com/asheshgoplani/agent-deck/pull/571))
- Allow underscore character in TUI dialog text inputs. ([#573](https://github.com/asheshgoplani/agent-deck/pull/573))
- Allow Esc to dismiss setup wizard on welcome step. ([#564](https://github.com/asheshgoplani/agent-deck/issues/564), [#566](https://github.com/asheshgoplani/agent-deck/pull/566))
- Initialize branchAutoSet when worktree default_enabled is true. ([#561](https://github.com/asheshgoplani/agent-deck/issues/561), [#562](https://github.com/asheshgoplani/agent-deck/pull/562))
- Harden sandbox runtime probes and respawn bash wrapping. ([#575](https://github.com/asheshgoplani/agent-deck/pull/575))
- Preserve existing OpenCode session binding on restart. ([#576](https://github.com/asheshgoplani/agent-deck/pull/576))

### Added
- Arrow-key navigation for confirm dialogs. ([#557](https://github.com/asheshgoplani/agent-deck/pull/557))

## [1.5.0] - 2026-04-10

v1.5.0 is the Premium Web App milestone. The web interface gets P0/P1 bug fixes, performance optimization (first-load wire size from 668 KB to under 150 KB gzipped), UX polish, and automated visual regression testing.

### Fixed
- [Phase 5, v1.4.1] Six critical regressions: Shift+letter key drops (CSI u), tmux scrollback clearing, mousewheel [0/0], conductor heartbeat on Linux, tmux PATH detection, bash -c quoting. (REG-01..06)
- [Phase 6] Mobile hamburger menu clickable at all viewports <=768px with systematic 7-level z-index scale. (WEB-P0-1)
- [Phase 6] Profile switcher: single profile shows read-only label; multi profile shows non-interactive list with help text for CLI switching. (WEB-P0-2)
- [Phase 6] Session title truncation: action buttons use absolute positioning with hover-reveal, no longer reserving 90px of space. (WEB-P0-3)
- [Phase 6] Write-protected mode: mutationsEnabled=false hides all write buttons; toast auto-dismisses at 5s with stack cap of 3 and history drawer for dismissed toasts. (WEB-P0-4, POL-7)
- [Phase 7] Terminal panel fills container on attach, no empty gray space below terminal. (WEB-P1-1)
- [Phase 7] Sidebar width fluid via clamp(260px, 22vw, 380px) on screens >=1280px. (WEB-P1-2)
- [Phase 7] Sidebar row density increased to 40px per row (from ~52px); 20+ sessions visible at 1080p. (WEB-P1-3)
- [Phase 7] Empty-state dashboard uses centered card layout with max-width 1024px. (WEB-P1-4)
- [Phase 7] Mobile topbar overflow menu for controls on viewports <600px. (WEB-P1-5)

### Performance
- [Phase 8] gzip compression on static file handler via klauspost/compress/gzhttp; ~518 KB saved per cold load. (PERF-A)
- [Phase 8] Chart.js script tag deferred to unblock HTML parser. (PERF-B)
- [Phase 8] xterm canvas addon removed (dead code); fallback chain is now WebGL then DOM only. (PERF-C)
- [Phase 8] WebGL addon lazy-loaded on desktop only; mobile skips import entirely, saving 126 KB. (PERF-D)
- [Phase 8] Event listener leak fixed via AbortController; listener count at rest drops from 290 to ~50. (PERF-E)
- [Phase 8] Search input debounced at 250ms; typing lag drops from 33ms to <8ms. (PERF-F)
- [Phase 8] SessionRow memoized; group collapse no longer rerenders 152 unrelated components. (PERF-G)
- [Phase 8] ES modules bundled via esbuild with code splitting and cache-busted filenames. (PERF-H)
- [Phase 8] Cost batch endpoint converted from GET to POST, preventing 414 URI Too Long. (PERF-I)
- [Phase 8] Immutable cache headers on hashed assets (1-year max-age). (PERF-J)
- [Phase 8] SessionList virtualized for 50+ sessions via hand-rolled useVirtualList hook. (PERF-K)

### Added
- [Phase 9] Skeleton loading state with CSS-only animate-pulse during initial sidebar render. (POL-1)
- [Phase 9] Action button 120ms opacity fade transitions with prefers-reduced-motion support. (POL-2)
- [Phase 9] Profile dropdown filters out _* test profiles, scrollable at 300px max-height. (POL-3)
- [Phase 9] Group divider gap reduced from 48px to 12-16px for tighter sidebar density. (POL-4)
- [Phase 9] Cost dashboard uses locale-aware currency formatting via Intl.NumberFormat. (POL-5)
- [Phase 9] Light theme re-audited across all surfaces for contrast and consistency. (POL-6)
- [Phase 10] Playwright visual regression tests with committed baselines; CI blocks merge on >0.1% pixel diff. (TEST-A)
- [Phase 10] Lighthouse CI on every PR with byte-weight hard gates and soft performance thresholds. (TEST-B)
- [Phase 10] Functional E2E tests for session lifecycle and group CRUD. (TEST-C)
- [Phase 10] Mobile E2E at 3 viewports: iPhone SE, iPhone 14, iPad. (TEST-D)
- [Phase 10] Weekly regression alerting workflow: runs visual + Lighthouse, posts issue on failure. (TEST-E)

## [1.4.2] - 2026-04-09

### Fixed
- Restore TUI keyboard input on all terminals (iTerm2, Ghostty, WezTerm, Kitty, tmux). Arrow keys, j/k, and mouse scroll were broken in v1.4.1 because `CSIuReader` wrapping `os.Stdin` made Bubble Tea skip raw-mode setup (`tcsetattr`), leaving the TTY in cooked mode and echoing escape sequences as text. Fixes #539, #544. ([#541](https://github.com/asheshgoplani/agent-deck/pull/541))
- Fix CSI final-byte whitelist in `csiuReader.translate` to include SGR mouse terminators (`M`/`m`), so mouse events are no longer corrupted when the reader is used. ([#541](https://github.com/asheshgoplani/agent-deck/pull/541))
- Remove `EnableKittyKeyboard(os.Stdout)` / `DisableKittyKeyboard(os.Stdout)` pairs from all four attach paths (`attachCmd`, `remoteCreateAndAttachCmd`, `attachWindowCmd`, `remoteAttachCmd`) in `internal/ui/home.go`. Writing `ESC[>1u` to the outer terminal before `tmux attach` put Ghostty (and other kitty-protocol terminals) into CSI u mode; tmux could not translate these sequences for the inner application, causing arrow keys to appear as raw escape codes. Restores v0.28.3 attach behavior. Fixes #546. ([#547](https://github.com/asheshgoplani/agent-deck/pull/547))

### Added
- Integration tests for TUI keyboard input (`internal/integration/tui_input_test.go`) to prevent future regressions in raw-mode setup and CSI handling.

## [0.25.1] - 2026-03-11

### Added
- Expose custom tools in the Settings panel default-tool picker so configured tools can be selected without editing `config.toml` by hand.

## [0.25.0] - 2026-03-11

### Added
- Add `preview.show_notes` support so the notes section can be hidden from the preview pane while keeping the main session view intact.
- Add Gemini hook management commands and hook-based Gemini session/status sync, including install, uninstall, and status flows.
- Add remote-session lifecycle actions in the TUI so remote sessions can be restarted, closed, or deleted directly from the session list.
- Add richer Slack bridge context so forwarded messages include stable sender/channel enrichment.

### Fixed
- Preserve hook-derived session identity across empty hook payloads by persisting a read-time session-id anchor fallback.
- Improve Telegram bot mention stripping and username handling so bridge messages route more reliably in group chats.
- Avoid repeated regexp compilation in hot paths by hoisting `regexp.MustCompile` calls to package-level variables.

## [0.24.1] - 2026-03-07

### Fixed
- Restore instant preview rendering from cached content during session navigation and immediately after returning from an attached session, removing placeholder delays introduced in `0.24.0`.

## [0.24.0] - 2026-03-07

### Added
- Add `internal/send` package consolidating all send verification functions (prompt detection, composer parsing, unsent-prompt checks) into a single location.
- Add Codex readiness detection: `waitForAgentReady` and `sendMessageWhenReady` now gate on `codex>` prompt before delivering messages to Codex sessions.
- Add session death detection in `--wait` mode: `waitForCompletion` detects 5 consecutive status errors and returns exit code 1 instead of hanging indefinitely.
- Add heartbeat migration function (`MigrateConductorHeartbeatScripts`) that auto-refreshes installed scripts to the latest template.
- Add exit 137 (SIGKILL) investigation report documenting root cause as Claude Code limitation with reproduction steps and mitigation strategies.
- Add exit 137 mitigation guidance to shared conductor CLAUDE.md and GSD conductor SKILL.md.
- Promote 27 validated conductor learnings to shared docs: 10 universal orchestration patterns to conductor CLAUDE.md, 6 GSD-specific learnings to gsd-conductor SKILL.md, 11 operational patterns to agent-deck-workflow SKILL.md.

### Fixed
- Harden Enter retry loop: retry every iteration for first 5 attempts (previously every 3rd), increasing ambiguous budget from 2 to 4.
- Scope heartbeat scripts to conductor's own group instead of broadcasting to all sessions in the profile.
- Honor `heartbeat_interval = 0` as disabled: skip heartbeat daemon installation during conductor setup.
- Add enabled-status guard to heartbeat scripts so they exit silently when conductor is disabled.
- Fix `-c` and `-g` flag co-parsing so both flags work together in `agent-deck add`.
- Improve `--no-parent` help text to reference `set-parent` for later parent linking.

### Changed
- Clean up all six conductor LEARNINGS.md files: mark promoted entries, remove retired entries, consolidate duplicates.

## [0.23.0] - 2026-03-07

### Added
- Add status detection integration tests: real tmux status transition cycles, pattern detection, and tool config verification.
- Add conductor pipeline integration tests: send-to-child delivery, cross-session event write-watch, heartbeat round-trips, and chunked send delivery.
- Add edge case integration tests: skills discover-attach verification.
- Complete milestone v1.1 Integration Testing (38 integration tests across 6 phases).

### Fixed
- Handle nested binary paths in release tarballs so self-update works with both flat and directory-wrapped archives.

## [0.22.0] - 2026-03-06

### Added
- Add integration test framework: TmuxHarness (auto-cleanup real tmux sessions), polling helpers (WaitForCondition, WaitForPaneContent, WaitForStatus), and SQLite fixture helpers (NewTestDB, InstanceBuilder).
- Add session lifecycle integration tests (start, stop, fork, restart) using real tmux sessions with automatic cleanup.
- Add session lifecycle unit tests covering start, stop, fork, and attach operations with tmux verification.
- Add status lifecycle tests for sleep/wake detection and SQLite persistence round-trips.
- Add skills runtime tests verifying on-demand skill loading, pool skill discovery, and project skill application.

### Changed
- Reformat agent-deck and session-share SKILL.md files to official Anthropic skill-creator format with proper frontmatter.
- Add $SKILL_DIR path resolution to session-share skill for plugin cache compatibility.
- Register session-share skill in marketplace.json for independent discoverability.
- Update GSD conductor skill content in pool directory with current lifecycle documentation.

## [0.21.1] - 2026-03-06

### Fixed

- Propagate forked `AGENTDECK_INSTANCE_ID` values correctly so Claude hook subprocesses update the child session instead of the parent.
- Fully honor `[tmux].inject_status_line = false` by skipping tmux notification/status-line mutations when status injection is disabled.
- Add Gemini `--yolo` CLI overrides for `agent-deck add`, `agent-deck session start`, and TUI session creation.
- Clamp final TUI frames to the terminal viewport so navigation cannot spill duplicate footer/help rows into scrollback.

## [0.21.0] - 2026-03-06

### Added

- Add built-in Pi tool support, configurable hotkeys, session notes in the preview pane, and optional follow-CWD-on-attach behavior in the TUI.
- Add OpenClaw gateway integration with sync, status, list, send, and bridge commands for managing OpenClaw agents as agent-deck sessions.
- Add per-window tmux tracking in the session list with direct window navigation and AI tool badges.
- Add remote session creation from the TUI (`n`/`N` on remote groups and remote sessions).
- Add remote binary management with automatic install during `agent-deck remote add` and the new `agent-deck remote update` command.
- Add configurable `[worktree].branch_prefix` for new worktree sessions.
- Add Vimium-style jump mode for session-list navigation.

### Changed

- Significantly reduce TUI lag during navigation, attach/return flows, preview rendering, and background status refreshes.

### Fixed

- Enable Claude-specific session management features for custom tools that wrap the `claude` binary.
- Prevent non-interactive installs from hanging when `tmux` is missing by skipping interactive prompts and failing fast when `sudo` would block.

## [0.20.2] - 2026-03-03

### Fixed

- Recover automatically when tmux startup fails due to a stale/unreachable default socket by quarantining the stale socket and retrying session creation once. This prevents `failed to create tmux session ... server exited unexpectedly` startup failures.

## [0.20.1] - 2026-03-03

### Added

- Add Discord bot support to the conductor bridge with setup flow and config support (`[conductor.discord]`), including slash commands (`/ad-status`, `/ad-sessions`, `/ad-restart`, `/ad-help`) and heartbeat alert delivery to Discord.

### Changed

- Reduce tmux `%output`-driven status update frequency for chatty sessions to lower parsing overhead and smooth CPU usage under heavy output.

### Fixed

- Restrict Discord slash commands to the configured Discord channel so conductor control stays channel-scoped.

## [0.20.0] - 2026-03-01

### Added

- Add remote SSH session support with two workflows:
  - `agent-deck add --ssh <user@host> [--remote-path <path>]` to launch/manage sessions on remote hosts.
  - `agent-deck remote add/list/sessions/attach/rename` to manage and interact with remote agent-deck instances.
- Add remote sessions to the TUI under `remotes/<name>`, with keyboard attach (`Enter`) and rename (`r`) support.
- Add JSON session fields `ssh_host` and `ssh_remote_path` in `agent-deck list --json` output.

### Fixed

- Recover repository state after the broken PR #260 merge and re-apply the feature cleanly on `main`.
- Harden SSH command handling by shell-quoting remote command parts and SSH host/path values.
- Prevent remote name parsing collisions by rejecting `:` in remote names.
- Preserve full multi-word titles in `agent-deck remote rename`.
- Stabilize remote session rendering order and snapshot-copy remote data during TUI rebuilds for safer async updates.

## [0.19.19] - 2026-02-26

### Fixed

- Make Homebrew update installs resilient to stale local tap metadata by running `brew update` before `brew upgrade` in `agent-deck update`.
- Update Homebrew check/install guidance to show the full install command (`brew update && brew upgrade asheshgoplani/tap/agent-deck`) so users can copy-paste a working path directly.

## [0.19.18] - 2026-02-26

### Fixed

- Make `agent-deck update` Homebrew-aware end-to-end: `--check` now shows the correct `brew upgrade` command and interactive install can execute the Homebrew upgrade path directly instead of failing after confirmation.
- Harden conductor/daemon binary resolution to prefer the active executable path and robust PATH ordering, avoiding stale `/usr/local/bin` picks that could drop parent transition notifications.
- Prevent TUI freezes during create/fork worktree flows by moving worktree creation into async command execution instead of blocking the Enter key handler.
- Enforce Claude conversation ID deduplication on storage saves (CLI + TUI paths) so duplicate `claude_session_id` ownership does not persist, with deterministic older-session retention.

### Changed

- Add conductor permission-loop troubleshooting guidance (`allow_dangerous_mode` / `dangerous_mode`) in README and troubleshooting docs.

## [0.19.17] - 2026-02-26

### Added

- Add Docker sandbox mode for sessions (TUI + CLI), including per-session containers, hardened container defaults, and sandbox docs/config references.

### Fixed

- Preserve non-sandbox tmux startup behavior while keeping sandbox dead-pane restart support.
- Strengthen `session send --no-wait` / launch no-wait initial-message delivery with retry+verification to reduce dropped prompt submits.
- Route transition notifications through explicit parent linkage only (no conductor fallback), and align conductor/README guidance with parent-linked routing.

## [0.19.16] - 2026-02-26

### Fixed

- Restore OpenCode/Codex status detection for active output by matching both `status_details` and `status` fields in tmux JSON pane formats.
- Eliminate a worktree creation TOCTOU race in `add` by creating/checking candidate worktree paths in one flow and retrying with suffixed names when collisions happen.
- Avoid false Claude tool detection for shell wrappers by validating shell executables exactly and only classifying wrappers as Claude when `claude` appears as a command token.
- Resolve duplicate group-name move failures in the TUI by moving sessions using canonical group paths while preserving user-facing group labels.

## [0.19.15] - 2026-02-25

### Added

- Add soft-select path editing and filterable recent-path suggestions in the New Session dialog, including matching-count hints and focused keyboard help text.
- Add compact notifications mode (`[notifications].minimal = true`) with status icon/count summary in tmux status-left, including `starting` sessions in the active count.
- Add conductor heartbeat rules externalization via `HEARTBEAT_RULES.md` (global default plus per-profile override support in the bridge runtime).
- Add proactive conductor context management with `clear_on_compact` controls (`conductor setup --no-clear-on-compact` and per-conductor metadata) and synchronous `PreCompact` hook registration.

### Fixed

- Preserve ANSI color/styling in session preview rendering while keeping status/readiness parsing reliable by normalizing ANSI where plain-text matching is required.
- Restore original tmux `status-left` correctly when clearing notifications, including intentionally empty original values.
- Guard analytics cache map access across UI and background worker paths to avoid concurrent map read/write races during background status updates.
- Prevent self-update prompts/flows on Homebrew-managed installs.

## [0.19.14] - 2026-02-24

### Added

- Add automatic heartbeat script migration for existing conductors so managed `heartbeat.sh` files are refreshed to the current generated template during conductor migration checks.
- Add `--cmd` parsing support for tool commands with inline args in `add`/`launch` (for example `-c "codex --dangerously-bypass-approvals-and-sandbox"`), with automatic wrapper generation when needed.

### Fixed

- Switch generated conductor heartbeat sends to non-blocking `session send --no-wait -q`, eliminating recurring `agent not ready after 80 seconds` timeout churn for busy conductors.
- Improve `add`/`launch` CLI help and JSON output to expose resolved command/wrapper details and avoid confusing launch behavior when mixing tool names with extra args.
- Fix parent/group friction for conductor-launched sessions by allowing explicit `-g/--group` to override inherited parent group while keeping parent linkage for notifications.

### Changed

- Expand README and CLI reference guidance for conductor-launched sessions (`--no-parent` vs auto-parent), transition notifier behavior, and safe command patterns.

## [0.19.13] - 2026-02-24

### Added

- Add built-in event-driven transition notifications (`notify-daemon`) that nudge a parent session first, then fall back to a conductor session when a child transitions from `running` to `waiting`/`error`/`idle`.
- Add `--no-parent` and default auto-parent linking for `add`/`launch` when launched from a managed session (`AGENT_DECK_SESSION_ID`), with conflict protection for `--parent` + `--no-parent`.
- Add `parent_session_id` and `parent_project_path` to `agent-deck session show --json`.
- Add conductor setup/status/teardown integration for the transition notifier daemon so always-on notifications can be installed and managed with conductor commands.

### Fixed

- Reduce SQLite lock contention under concurrent daemon and CLI usage by avoiding unnecessary schema-version writes and retrying transient busy errors during storage migration/open.
- Improve status-driven notification reliability for fast tool completions by combining watcher updates with direct hook-file fallback reads and hook-based terminal transition candidates.

## [0.19.11] - 2026-02-23

### Added

- Add shared and per-conductor `LEARNINGS.md` support with setup/migration wiring so conductors can capture reusable orchestration lessons over time.

### Fixed

- Harden `launch -m` and `session send` message delivery for Claude by using fresh pane captures, robust composer prompt parsing (including wrapped prompts), and stronger Enter retry verification to avoid pasted-but-unsent prompts.
- Improve readiness detection for non-Claude tools (including Codex) by treating stable `idle`/`waiting` states as ready, preventing false startup timeouts when launching with an initial message.
- Fix launch/session-start messaging semantics so non-`--no-wait` flows correctly report message sent state (`message_pending=false`).

## [0.19.10] - 2026-02-23

### Fixed

- Make `agent-deck session send --wait` and `agent-deck session output` resilient when Claude session IDs are missing/stale by using best-effort response recovery (tmux env refresh, disk sync fallback, and terminal parse fallback).
- Improve Claude send verification to catch pasted-but-unsent prompts even after an initial `waiting` state, reducing false positives where a prompt was pasted but never submitted.
- Update conductor bridge messaging to use single-call `session send --wait -q --timeout ...` flow for Telegram/Slack and heartbeat handling, reducing extra polling steps and improving reliability.
- Reject non-directory legacy file skills when attaching project skills, and harden skill materialization to recover from broken symlinks and symlinked target-path edge cases.

### Changed

- Update conductor templates/docs and launcher helper scripts to prefer one-shot launch/send flows and single-call wait semantics for smoother orchestration.

## [0.19.9] - 2026-02-20

### Fixed

- Fix terminal style leakage after tmux attach by waiting for PTY output to drain and resetting OSC-8/SGR styles before the TUI redraws.
- Harden `agent-deck session send` delivery by retrying `Enter` only when Claude shows a pasted-but-unsent marker (`[Pasted text ...]`) and avoiding unnecessary retries once status is already `waiting`/`idle`.

### Changed

- Clarify tmux wait-bar shortcut docs: press `Ctrl+b`, release, then press `1`–`6` to jump to waiting sessions.

## [0.19.8] - 2026-02-20

### Fixed

- Fix `agent-deck session show --json` MCP output marshalling by emitting concrete local/global/project values instead of a method reference in `mcps.local` (#213).
- Fix conductor daemon Python resolution by preferring `python3` from the active shell `PATH` before fallback absolute paths (#215).

## [0.19.7] - 2026-02-20

### Fixed

- Fix heartbeat script profile text stamping so generated `heartbeat.sh` uses the real profile name in message text for non-default profiles (#207, contributed by @CoderNoveau).
- Fix conductor bridge message delivery when the conductor session is idle by using non-blocking `session send --no-wait`, and apply this in the embedded runtime bridge template with regression coverage (#210, contributed by @sjoeboo).

## [0.19.6] - 2026-02-19

### Added

- Add `manage_mcp_json` config option to disable all `.mcp.json` writes, plus a LOCAL-scope MCP Manager warning when disabled (#197, contributed by @sjoeboo).
- Split conductor guidance into shared mechanism (`CLAUDE.md`) and policy (`POLICY.md`) with per-conductor policy override support (#201).

### Fixed

- Fix conductor setup migration so legacy generated per-conductor `CLAUDE.md` files are updated safely for the policy split while preserving custom and symlinked files (#201).
- Fix launchd and systemd conductor daemon units to include the installed `agent-deck` binary directory in `PATH` so bridge/heartbeat jobs can find the CLI (#196, contributed by @sjoeboo).
- Support environment variable expansion (`$VAR`, `${VAR}`) in path-based config values and unify path expansion behavior across config consumers (#194, contributed by @tiwillia).

## [0.19.5] - 2026-02-18

### Changed

- Remap TUI shortcuts to reduce conflicts: `m` opens MCP Manager, `s` opens Skills Manager (Claude), and `M` moves sessions between groups.

### Fixed

- Reduce Codex session watcher CPU usage by rate-limiting expensive on-disk session scans and avoiding redundant tmux environment writes.
- Fix macOS installer crash on default Bash 3.2 by replacing associative arrays in `install.sh` with Bash 3.2 compatible helper functions (#192, contributed by @slkiser).

## [0.19.4] - 2026-02-18

### Added

- Add pool-focused type-to-jump navigation and scrolling in the Skills Manager (`P`) dialog for long lists.
- Add stricter Skills Manager available list behavior so project attach/detach is driven by the managed pool source.

### Changed

- Update README and skill references with Skills Manager usage, skill CLI command coverage, and skills registry path documentation.

## [0.19.0] - 2026-02-17

### Added

- Add `agent-deck web` mode to run the TUI and web UI server together, with browser terminal streaming and session menu APIs (#174, contributed by @PatrickStraeter)
- Add web push notification and PWA support for web mode (`--push`, `--push-vapid-subject`, `--push-test-every`) (#174)
- Add macOS MacPorts support to `install.sh` with `--pkg-manager` selection alongside Homebrew (#187, contributed by @bronweg)

### Fixed

- Fix `allow_dangerous_mode` propagation for Claude sessions created from the UI flow (#185, contributed by @daniel-shimon)
- Fix TUI scroll artifacts caused by width-measurement inconsistency and control-character leakage in preview rendering (#182, contributed by @jsvana)
- Fix Claude busy-pattern false positives from welcome-banner separators by anchoring spinner regexes to line start (#179, contributed by @mtparet)
- Harden web mode by restricting WebSocket upgrades to same-host origins and preserving auth token in push deep links (#174)

## [0.18.1] - 2026-02-17

### Added

- Add `--wait` flag to `session send` for blocking until command completion (#180)

## [0.18.0] - 2026-02-17

### Added

- Add Codex notify hook integration for instant session status updates
- Add notification show_all mode to display all notifications at once
- Add automatic bridge.py updates when running `agent-deck update` (#178)

### Fixed

- Fix: handle error returns in test cleanup functions
- Fix: bridge.py not updating with agent-deck binary updates (#178)

## [0.17.0] - 2026-02-16

### Added

- Add top-level rename command with validation (#176, contributed by @nlenepveu)
- Add Slack user ID authorization for conductors (#170, contributed by @mtparet)
- Custom CLAUDE.md paths via symlinks for conductors (#173, contributed by @mtparet)

### Fixed

- Fix: remove thread context fetching from Slack handler (#175, contributed by @mtparet)
- Fix: prevent worktree nesting when creating from within worktrees (#177)

## [0.16.0] - 2026-02-14

### Added

- Add `--teammate-mode` tmux option to Claude session launcher for shared terminal pairing (#168, contributed by @jonnocraig)
- Add Slack integration and cross-platform daemon support (#169, contributed by @mtparet)
- Add Claude Code lifecycle hooks for real-time status detection (instant green/yellow/gray transitions without tmux polling)
- Add first-launch prompt asking users to install hooks (preserves existing Claude settings.json)
- Add `agent-deck hooks install/uninstall/status` CLI subcommands for manual hook management
- Add `hooks_enabled` config option under `[claude]` to opt out of hook-based detection
- Add StatusFileWatcher (fsnotify) for instant hook status file processing
- Add `AGENTDECK_INSTANCE_ID` env var export for Claude hook subprocess identification
- Add acknowledgment awareness to hook fast path (attach turns session gray, `u` key turns it orange)
- Add `llms.txt` for LLM discoverability, fix schema version, add FAQ entries (#167)

### Fixed

- Fix middot `·` spinner character not detected as busy indicator when followed by ellipsis (BusyPatterns regex now includes `·`)

### Changed

- Sessions with active hooks skip tmux content polling entirely (2-minute timeout as crash safety net only)
- Existing sessions without hooks continue using polling (seamless hybrid mode)

## [0.15.0] - 2026-02-13

### Added

- Add `inject_status_line` config option under `[tmux]` to disable tmux statusline injection, allowing users to keep their own tmux status bar (#157)
- Add system theme option: sync TUI theme with OS dark/light mode (#162)
- Improve quick session creation: inherit path, tool, and options from hovered session (#165)

### Fixed

- Fix Claude session ID not updating after `/clear`, `/fork`, or `/compact` by syncing from disk (#166)
- Restore delay between paste and Enter in `SendKeysAndEnter` to prevent swallowed input in tmux (#168)

## [0.14.0] - 2026-02-12

### Added

- Add title-based status detection fast-path: reads tmux pane titles (Braille spinner / done markers) to determine Claude session state without expensive content scanning
- Add `RefreshPaneInfoCache()` for zero-subprocess pane title fetching via PipeManager
- Add worktree finish dialog (`W` key): merge branch, remove worktree, delete branch, and clean up session in one step
- Add worktree branch badge `[branch]` in session list for worktree sessions
- Add worktree info section in preview pane (branch, repo, path, dirty status)
- Add worktree dirty status cache with lazy 10s TTL checks
- Add repository worktree summary in group preview when sessions share a repo
- Add `esc to interrupt` fallback to Claude busy patterns for older Claude Code versions
- Add worktree section to help overlay

### Fixed

- Fix busy indicator false negatives for `·` and `✻` spinner chars with ellipsis (BusyRegexp now correctly catches all spinner frames with active context)
- Remove unused `matchesDetectPatterns` function (lint warning)
- Fix `starting` and `inactive` status mapping in instance status update

## [0.13.0] - 2026-02-11

### Added

- Add quick session creation with `Shift+N` hotkey: instant session with auto-generated name and smart defaults (#161)
- Add Docker-style name generator (adjective-noun) with ~10,000 unique combinations
- Add `--quick` / `-Q` flag to `agent-deck add` CLI for auto-named sessions
- Smart defaults: inherits tool, options, and path from most recent session in the group

## [0.12.3] - 2026-02-11

### Fixed

- Fix busy detection window reduced from 25 to 10 lines for faster status transitions
- Fix conductor group permanently pinned to top of group list
- Optimize status detection pipeline for faster green/yellow transitions
- Add spinner movement detection tests for stuck spinner validation

## [0.12.2] - 2026-02-10

### Fixed

- Fix `session send` intermittently dropping Enter key (and sometimes text) due to tmux race condition between two separate `send-keys` process invocations (tmux#1185, tmux#1517, tmux#1778)
- Fix all 6 send-keys + Enter code paths to use atomic tmux command chaining (`;`) in a single subprocess
- Add retry with verification to CLI `session send` for resilience under heavy load or SSH latency

## [0.12.1] - 2026-02-10

### Fixed

- Fix Shift+R restart race condition with animation guard on restart and fork hotkeys (#147)
- Fix settings menu viewport cropping in small terminals with scroll windowing (#149)
- Fix .mcp.json clobber by preserving existing entries when managing MCP sessions (#146)
- Fix --resume-session arg parsing by registering it in the arg reorder map (#145)

### Added

- Add tmux option overrides via `[tmux]` config section in config.toml (#150)
- Add opencode fork infrastructure with OpenCodeOptions for model/agent/fork support (#148)

## [0.12.0] - 2026-02-10

### Added

- Multiple conductors per profile: create N named conductors in a single profile
  - `agent-deck conductor setup <name>` with `--heartbeat`, `--no-heartbeat`, `--description` flags
  - `agent-deck conductor teardown <name>` or `--all` to remove conductors
  - `agent-deck conductor list` with `--json` and `--profile` filters
  - `agent-deck conductor status [name]` shows all or specific conductor health
- Two-tier CLAUDE.md for conductors: shared knowledge base + per-conductor identity
  - Shared `CLAUDE.md` at conductor root with CLI reference, protocols, and rules
  - Per-conductor `CLAUDE.md` with name and profile substitution
- Conductor metadata via `meta.json` files for name, profile, heartbeat settings, and description
- Auto-migration of legacy single-conductor directories to new multi-conductor format
- Bridge (Telegram) updated for dynamic conductor discovery via `meta.json` scanning
- `normalizeArgs` utility for consistent flag parsing across all CLI commands
- Status field added to `agent-deck list --json` output

## [0.11.4] - 2026-02-09

### Added

- Add `allow_dangerous_mode` option to `[claude]` config section
  - Passes `--allow-dangerously-skip-permissions` to Claude (opt-in bypass mode)
  - `dangerous_mode = true` takes precedence when both are set
  - Based on contribution by @daniel-shimon (#152), with architectural fixes (#153)
- New permission flag persists per-session across fork and restart operations

## [0.11.3] - 2026-02-09

### Fixed

- Fix deleted sessions reappearing after reload or app restart
  - `SaveInstances()` now deletes stale rows from SQLite within the same transaction
  - Added explicit `DeleteInstance()` call in the delete handler as a safeguard
  - Root cause: `INSERT OR REPLACE` never removed deleted session rows from the database
- Update profile detection to check for `state.db` (SQLite) in addition to legacy `sessions.json`
- Update uninstall script to count sessions from SQLite instead of JSON

### Added

- Persist UI state (cursor position, preview mode, status filter) across restarts via SQLite metadata
- Save group expanded/collapsed state immediately on toggle
- Discord badge and link in README

### Changed

- Simplify multi-instance coordination: remove periodic primary re-election from background worker
- Create new profiles with SQLite directly instead of empty `sessions.json`
- Update troubleshooting docs for SQLite-based recovery

## [0.11.2] - 2026-02-06

### Fixed

- Enable notification bar on all instances, not just the primary
  - Previously secondary instances had notifications disabled entirely
  - All instances share the same SQLite state, so they produce identical bar content

## [0.11.1] - 2026-02-06

### Changed

- Replace file-based lock with SQLite heartbeat-based primary election for multi-instance coordination
  - Dynamic failover: if the primary instance crashes, a secondary takes over the notification bar within ~12 seconds
  - Eliminates stale `.lock` files that required manual cleanup after crashes
  - `ElectPrimary()` uses atomic SQLite transactions to prevent split-brain

### Removed

- Remove `acquireLock`, `releaseLock`, `getLockFilePath`, `isProcessRunning` (replaced by SQLite election)

## [0.11.0] - 2026-02-06

### Changed

- Replace `sessions.json` with SQLite (`state.db`) as the single source of truth
  - WAL mode for concurrent multi-instance reads/writes without corruption
  - Auto-migrates existing `sessions.json` on first run (renamed to `.migrated` as backup)
  - Removes fragile full-file JSON rewrites, backup rotation, and fsnotify dependency
  - Tool-specific data stored as JSON blob in `tool_data` column for schema flexibility
- Replace fsnotify-based storage watcher with SQLite metadata polling
  - Simpler, works reliably on all filesystems (9p, NFS, WSL)
  - 2-second poll interval using `metadata.last_modified` timestamp
- Replace tmux rate limiter and watcher with control mode pipes (PipeManager)
  - Event-driven status detection via `tmux -C` control mode
  - Zero-subprocess architecture: no more `tmux capture-pane` for idle sessions

### Added

- Add `internal/statedb` package: SQLite wrapper with CRUD, heartbeat, status sync, and change detection
- Add cross-instance acknowledgment sync via SQLite (ack in instance A visible in instance B)
- Add instance heartbeat table for tracking alive TUI processes
- Add `StatusSettings` in user config (reserved for future status detection settings)

## [0.10.20] - 2026-02-06

### Added

- Add `worktree finish` command to merge branch, remove worktree, and delete session in one step (#140)
  - Flags: `--into`, `--no-merge`, `--keep-branch`, `--force`, `--json`
  - Abort-safe: merge conflicts trigger `git merge --abort`, leaving everything intact
- Auto-cleanup worktree directories when deleting worktree sessions (CLI `remove` and TUI `d` key)

### Fixed

- Fix orphaned MCP server processes (Playwright CPU leak) by killing entire process group
  - Set `Setpgid=true` so grandchild processes (npx/uvx spawned) share a process group
  - Shutdown now sends SIGTERM/SIGKILL to `-pid` (group) instead of just the parent
- Fix test cleanup killing user sessions with "test" in their title
- Fix session rename lost during reload race condition

## [0.10.19] - 2026-02-05

### Fixed

- Fix session rename not persisting (#141)
  - `lastLoadMtime` was not updated after saves, causing mtime check to incorrectly abort subsequent saves
  - Renames, reorders, and other non-force saves now persist correctly

## [0.10.18] - 2026-02-05

### Added

- Add Codex CLI `--yolo` flag support (#142)
  - Global config: `[codex] yolo_mode = true` in config.toml
  - Per-session override in New Session dialog (checkbox)
  - Flag preserved across session restarts
  - Settings panel toggle for global default
- Add unified `OptionsPanel` interface for tool-specific options (#143)
  - New tools can add options by implementing interface + 1 case in `updateToolOptions()`
  - Shared `renderCheckboxLine()` helper ensures visual consistency across panels

### Fixed

- Fix `ClaudeOptionsPanel.Blur()` not resetting focus state
  - `IsFocused()` now correctly returns false after blur

## [0.10.17] - 2026-02-05

### Fixed

- Fix sessions disappearing after creation in TUI
  - Critical saves (create, fork, delete, restore) now bypass mtime check that was incorrectly aborting saves
  - Sessions created during reload are now properly persisted to JSON before triggering reload
- Fix import function to recover orphaned agent-deck sessions
  - Press `i` to import sessions that exist in tmux but are missing from sessions.json
  - Recovered sessions are placed in a "Recovered" group for easy identification

## [0.10.16] - 2026-02-05

### Fixed

- Fix garbled input at update confirmation prompt
  - Add `drainStdin()` to flush terminal input buffer before prompting
  - Use `TCFLSH` ioctl to discard pending escape sequences and accidental keypresses
  - Switch from `fmt.Scanln` to `bufio.NewReader` for more robust input handling

## [0.10.15] - 2026-02-05

### Fixed

- Fix TUI overwriting CLI changes to sessions.json (#139)
  - Add mtime check before save: compares file mtime against when we last loaded, aborts save and triggers reload if external changes detected
  - Fix TOCTOU race condition: `isReloading` flag now protected by mutex in all 6 read locations
  - Add filesystem detection for WSL2/NFS: warns users when on 9p/NFS/CIFS/SSHFS mounts where fsnotify is unreliable

## [0.10.14] - 2026-02-04

### Fixed

- Fix critical OOM crash: Global Search was loading 4.4 GB of JSONL content into memory and opening 884 fsnotify directory watchers (7,900+ file descriptors), causing agent-deck to balloon to 6+ GB RSS until macOS killed it
  - Temporarily disable Global Search at startup until memory-safe implementation is complete
  - Optimize directory traversal to skip `tool-results/` and `subagents/` subdirectories (never contain JSONL files)
  - Limit fsnotify watchers to project-level directories only (was recursively watching ALL subdirectories)
- Add max client cap (100) per MCP socket proxy to prevent unbounded goroutine growth from reconnect loops
  - Broken MCPs (e.g., `reddit-yilin` with 72 connects/30s) could spawn unlimited goroutines and scanner buffers

### Changed

- Global Search (`G` key) is temporarily disabled pending a memory-safe reimplementation
  - Will be re-enabled once balanced tier is enforced for large datasets and memory limits are properly applied

## [0.10.13] - 2026-02-04

### Added

- Migrate all logging to structured JSONL via `log/slog` with automatic rotation
  - JSONL output to `~/.agent-deck/debug.log` with component-based filtering (`jq 'select(.component=="pool")'`)
  - Automatic log rotation via lumberjack (configurable size, backups, retention in `[logs]` config)
  - Event aggregation for high-frequency MCP socket events (1 summary per 30s instead of 40 lines/sec)
  - In-memory ring buffer with crash dump support (`kill -USR1 <pid>`)
  - Optional pprof profiling on `localhost:6060`
  - 9 log components: status, mcp, notif, perf, ui, session, storage, pool, http
  - New `[logs]` config options: `debug_level`, `debug_format`, `debug_max_mb`, `debug_backups`, `debug_retention_days`, `debug_compress`, `ring_buffer_mb`, `pprof_enabled`, `aggregate_interval_secs`

### Fixed

- Fix MCP pool infinite restart loop causing 45 GB memory leak over 15 hours
  - Add `StatusPermanentlyFailed` status: broken MCPs are disabled after 10 consecutive failures
  - Fix leaked proxy context/goroutines when `Start()` fails during restart
  - Reset failure counters after proxy is healthy for 5+ minutes (allows transient failure recovery)
  - Skip permanently failed proxies in health monitor for both socket and HTTP pools
- Fix inconsistent debug flag check in tmux.go (`== "1"` changed to `!= ""` to match rest of codebase)

## [0.10.12] - 2026-02-04

### Fixed

- Fix tmux pane showing stale conversation history after session restart (#138)
  - Clear scrollback buffer before respawn to remove old content
  - Invalidate preview cache on restart for immediate refresh
  - Kill old tmux session in fallback restart path to prevent orphans

## [0.10.11] - 2026-02-04

### Added

- Add `mcp_default_scope` config option to control where MCPs are written (#137)
  - Set to `"global"` or `"user"` to stop agent-deck from overwriting `.mcp.json` on restart
  - Affects MCP Manager default tab, CLI attach/detach defaults, and session restart regeneration
  - Defaults to `"local"` (no breaking change)

## [0.10.10] - 2026-02-04

### Added

- Add configurable worktree path templates via `path_template` config option (#135, contributed by @peteski22)
  - Template variables: `{repo-name}`, `{repo-root}`, `{branch}`, `{session-id}`
  - Overrides `default_location` when set; falls back to existing behavior when unset
  - Integrated at all 4 worktree creation points (CLI add, CLI fork, TUI new session, TUI fork)
  - Backported from [njbrake/agent-of-empires](https://github.com/njbrake/agent-of-empires)

## [0.10.9] - 2026-02-03

### Removed

- Remove dead GoReleaser ldflags targeting non-existent `main.version/commit/date` vars
- Remove redundant `make release` target (superseded by `release-local`)
- Remove unused deprecated wrappers `NewStorage()` and `GetStoragePath()`
- Remove unused test helpers file (`internal/ui/test_helpers.go`)
- Remove stale `home.go.bak` backup file

## [0.10.8] - 2026-02-03

### Fixed

- Fix shell dying after tool exit by removing `exec` prefix from all tool commands (#133, contributed by @kurochenko)
  - When Claude, Gemini, OpenCode, Codex, or generic tools exit, users now return to their shell prompt instead of a dead tmux pane
  - Enables workflows where tools run inside wrappers (e.g., nvim) that should survive tool exit

## [0.10.7] - 2026-02-03

### Added

- Add `make release-local` target for local GoReleaser releases (no GitHub Actions dependency)

## [0.10.6] - 2026-02-03

### Fixed

- **TUI freezes with 40+ sessions**: Parallel status polling replaces sequential loop that couldn't complete within 2s tick
  - 10-worker pool via errgroup for concurrent tmux status checks
  - Instance-level RWMutex prevents data races between background worker and TUI rendering
  - Tiered polling skips idle sessions with no activity (10s recheck gate)
  - 3-second timeout on CapturePane/GetWindowActivity prevents hung tmux calls from blocking workers
  - Timeout preserves previous status instead of flashing RED
  - Race detector (`-race`) enabled in tests and CI

## [0.10.5] - 2026-02-03

### Fixed

- **Fix intermittent `zsh: killed` due to memory exhaustion (#128)**: Four memory leaks causing macOS OOM killer (Jetsam) to SIGKILL agent-deck after prolonged use with many sessions:
  - Cap global search content buffer memory at 100MB (configurable via `memory_limit_mb`), evict oldest 25% of entries when exceeded
  - Release all content memory and clear file trackers on index Close()
  - Stop debounce timers on watcher shutdown to prevent goroutine leaks
  - Prune stale analytics/activity caches every 20 seconds (were never cleaned up)
  - Clean up analytics caches on session delete
  - Clear orphaned MCP socket proxy request map entries on client disconnect and MCP failure
  - Prune LogWatcher rate limiters for removed sessions every 20 seconds

## [0.10.4] - 2026-02-03

### Added

- **Prevent nested agent-deck sessions (#127)**: Running `agent-deck` inside a managed tmux session now shows a clear error instead of causing infinite `...` output. Read-only commands (`version`, `help`, `status`, `list`, `session current/show/output`, `mcp list/attached`) still work for debugging

## [0.10.3] - 2026-02-03

### Fixed

- **Global search unusable with large datasets (#125)**: Multiple performance fixes make global search work with multi-GB session data:
  - Remove rate limiter from initial load (was causing 42+ minute "Loading..." on large datasets)
  - Read only first 32KB of files for metadata in balanced tier (was reading entire files, some 800MB+)
  - Early exit from parsing once metadata found (SessionID/CWD/Summary)
  - Parallelize disk search with 8-worker pool (was sequential)
  - Debounced async search on UI thread (250ms debounce + background goroutine)
  - Default `recent_days` to 30 when not set (was 0 = all time)
- **G key didn't open Global Search**: Help bar showed `G Global` but the key actually jumped to the bottom of the list. `G` now opens Global Search (falls back to local search if global search is disabled)

## [0.10.2] - 2026-02-03

### Fixed

- **Global search freezes when typing with many sessions (#125)**: Search ran synchronously on the UI thread, blocking all input while scanning files from disk. Now uses debounced async search (250ms debounce + background goroutine) so the UI stays responsive regardless of data size
- **G key didn't open Global Search**: Help bar showed `G Global` but the key actually jumped to the bottom of the list. `G` now opens Global Search (falls back to local search if global search is disabled)

## [0.10.1] - 2026-02-02

### Fixed

- **GREEN status not detecting Claude 2.1.25+ spinners**: Prompt detector only checked braille spinner chars (`⠋⠙⠹...`) as busy guards, missing the asterisk spinners (`✳✽✶✢`) used since Claude 2.1.25. This caused sessions to show YELLOW instead of GREEN while Claude was actively working
- **Prompt detector missing whimsical word timing patterns**: Only "thinking" and "connecting" were recognized as active processing. Now detects all 90+ whimsical words (e.g., "Hullaballooing", "Clauding") via the universal `…` + `tokens` pattern
- **Spinner check range too narrow**: Only checked last 3 lines for spinner chars, but Claude's UI can push the spinner line 6+ lines from the bottom (tip lines, borders, status bar). Expanded to last 10 lines
- **Acknowledge override on attach**: Attaching to a waiting (yellow) session would briefly acknowledge it, but the background poller immediately reset it back to waiting because the prompt was still visible. Prompt detection now respects the acknowledged state

## [0.10.0] - 2026-02-02

### Changed

- **Group dialog defaults to root mode on grouped sessions**: Pressing `g` while the cursor is on a session inside a group now opens the "Create New Group" dialog in root mode instead of subgroup mode. Tab toggle still switches to subgroup. Group headers still default to subgroup mode. This makes it easier for users with all sessions in groups to create new root-level groups

### Added

- **MCP socket pool resilience docs**: README updated to mention automatic ~3s crash recovery via reconnecting proxy
- **Pattern override documentation**: `config.toml init` now includes documentation for `busy_patterns_extra`, `prompt_patterns_extra`, and `spinner_chars_extra` fields for extending built-in tool detection patterns

## [0.9.2] - 2026-01-31

### Fixed

- **492% CPU usage**: Main TUI process was consuming 5 CPU cores due to reading 100-841MB JSONL files every 2 seconds per Claude session. Now uses tail-read (last 32KB only) with file-size caching to skip unchanged files entirely
- **Duplicate notification sync**: Both foreground TUI tick and background worker were running identical notification sync every 2 seconds, spawning duplicate tmux subprocesses. Removed foreground sync since background worker handles everything
- **Excessive tmux subprocess spawns**: `GetEnvironment()` spawned `tmux show-environment` every 2 seconds per Claude session for session ID lookup. Added 30-second cache since session IDs rarely change
- **Unnecessary idle session polling**: Claude/Gemini/Codex session tracking updates now skip idle sessions where nothing changes

### Added

- Configurable pattern detection system: `ResolvedPatterns` with compiled regexes replaces hardcoded busy/prompt detection, enabling pattern overrides via `config.toml`

## [0.9.1] - 2026-01-31

### Fixed

- **MCP socket proxy 64KB crash**: `bufio.Scanner` default 64KB limit caused socket proxy to crash when MCPs like context7 or firecrawl returned large responses. Increased buffer to 10MB, preventing orphaned MCP processes and permanent "failed" status
- **Faster MCP failure recovery**: Health monitor interval reduced from 10s to 3s for quicker detection and restart of failed proxies
- **Active client disconnect on proxy failure**: When socket proxy dies, all connected clients are now actively closed so reconnecting proxies detect failure immediately instead of hanging

### Added

- **Reconnecting MCP proxy** (`agent-deck mcp-proxy`): New subcommand replaces `nc -U` as the stdio bridge to MCP sockets. Automatically reconnects with exponential backoff when sockets drop, making MCP pool restarts invisible to Claude sessions (~3s recovery)

## [0.9.0] - 2026-01-31

### Added

- **Fork worktree isolation**: Fork dialog (`F` key) now includes an opt-in worktree toggle for git repos. When enabled, the forked session gets its own git worktree directory, isolating Claude Code project state (plan, memory, attachments) between parent and fork (#123)
- Auto-suggested branch name (`fork/<session-name>`) in fork dialog when worktree is enabled
- CLI `session fork` command gains `-w/--worktree <branch>` and `-b/--new-branch` flags for worktree-based forks
- Branch validation in fork dialog using existing git helpers

## [0.8.99] - 2026-01-31

### Fixed

- **Session reorder persistence**: Reordering sessions with Shift+K/J now persists across reloads. Added `Order` field to session instances, normalized on every move, and sorted by Order on load. Legacy sessions (no Order field) preserve their original order via stable sort (#119)

## [0.8.98] - 2026-01-30

### Fixed

- **Claude Code 2.1.25+ busy detection**: Claude Code 2.1.25 removed `"ctrl+c to interrupt"` from the status line, causing all sessions to appear YELLOW/GRAY instead of GREEN while working. Detection now uses the unicode ellipsis (`…`) pattern: active state shows `"✳ Gusting… (35s · ↑ 673 tokens)"`, done state shows `"✻ Worked for 54s"` (no ellipsis)
- Status line token format detection updated to match new `↑`/`↓` arrow format (`(35s · ↑ 673 tokens)`)
- Content normalization updated for asterisk spinner characters (`·✳✽✶✻✢`) to prevent false hash changes

### Changed

- Analytics preview panel now defaults to OFF (opt-in via `show_analytics = true` in config.toml)

### Added

- 6 new whimsical thinking words: `billowing`, `gusting`, `metamorphosing`, `sublimating`, `recombobulating`, `sautéing`
- Word-list-independent spinner detection regex for future-proofing against new Claude Code words

## [0.8.97] - 2026-01-29

### Fixed

- **CLI session ID capture**: `session start`, `session restart`, `session fork`, and `try` now persist Claude session IDs to JSON immediately, enabling fork and resume from CLI-only workflows without the TUI
- Fork pre-check recovery: `session fork` attempts to recover missing session IDs from tmux before failing, fixing sessions started before this fix
- Stale comment in `loadSessionData` corrected to reflect lazy loading behavior

### Added

- `PostStartSync()` method on Instance for synchronous session ID capture after Start/Restart (CLI-only; TUI uses its existing background worker)

## [0.8.96] - 2026-01-28

### Added

- **HTTP Transport Support for MCP Servers**: Native support for HTTP/SSE MCP servers with auto-start capability
- Add `[mcps.X.server]` config block for auto-starting HTTP MCP servers (command, args, env, startup_timeout, health_check)
- Add `mcp server` CLI commands: `start`, `stop`, `status` for managing HTTP MCP servers
- Add transport type indicators in `mcp list`: `[S]`=stdio, `[H]`=http, `[E]`=sse
- Add TUI MCP dialog transport indicators with status: `●`=running, `○`=external, `✗`=stopped
- Add HTTP server pool with health monitoring and automatic restart of failed servers
- External server detection: if URL is already reachable, use it without spawning a new process

### Changed

- MCP dialog now shows transport type and server status for each MCP
- `mcp list` output now includes transport type column

## [0.8.95] - 2026-01-28

### Changed

- **Performance: TUI startup ~3x faster** (6s → 2s for 44 sessions)
- Batch tmux operations: ConfigureStatusBar (5→1 call), EnableMouseMode (6→2 calls) using command chaining
- Lazy loading: defer non-essential tmux configuration until first attach or background tick
- Skip UpdateStatus and session ID sync at load time (use cached status from JSON)

### Added

- Add `ReconnectSessionLazy()` for deferred session configuration
- Add `EnsureConfigured()` method for on-demand tmux setup
- Add `SyncSessionIDsToTmux()` method for on-demand session ID sync
- Background worker gradually configures unconfigured sessions (one per 2s tick)

## [0.8.94] - 2026-01-28

### Added

- Add undo delete (Ctrl+Z) for sessions: press Ctrl+Z after deleting a session to restore it including AI conversation resume. Supports multiple undos in reverse order (stack of up to 10)
- Show ^Z Undo hint in help bar (compact and full modes) when undo stack is non-empty
- Add Ctrl+Z entry to help overlay (? screen)

### Changed

- Update delete confirmation dialog: "This cannot be undone" → "Press Ctrl+Z after deletion to undo"

## [0.8.93] - 2026-01-28

### Fixed

- Fix `g` key unable to create root-level groups when any group exists (#111). Add Tab toggle in the create-group dialog to switch between Root and Subgroup modes
- Fix `n` key handler using display name constant instead of path constant for default group

### Added

- Group DefaultPath tracking: groups now track the most recently accessed session's project path via `updateGroupDefaultPath`

## [0.8.92] - 2026-01-28

### Fixed

- Fix CI test failure in `TestBindUnbindKey` by making default key restore best-effort in `UnbindKey`

## [0.8.91] - 2026-01-28

### Fixed

- Fix TUI cursor not following notification bar session switch after detach (Ctrl+b N during attach now moves cursor to the switched-to session on Ctrl+Q)

## [0.8.90] - 2026-01-28

### Fixed

- Fix quit dialog ("Keep running" / "Shut down") hidden behind splash screen, causing infinite hang on quit with MCP pool
- Fix `isQuitting` flag not reset when canceling quit dialog with Esc
- Add 5s safety timeouts to status worker and log worker waits during shutdown

## [0.8.89] - 2026-01-28

### Fixed

- Fix shutdown hang when quitting with "shut down" MCP pool option (process `Wait()` blocked forever on child-held pipes)
- Set `cmd.Cancel` (SIGTERM) and `cmd.WaitDelay` (3s) on MCP processes for graceful shutdown with escalation
- Add 5s safety timeout to individual proxy `Stop()` and 10s overall timeout to pool `Shutdown()`

## [0.8.88] - 2026-01-28

### Fixed

- Fix stale expanded group state during reload causing cursor jumps when CLI adds a session while TUI is running
- Fix new groups added via CLI appearing collapsed instead of expanded
- Eliminate redundant tree rebuild and viewport sync during reload (performance)

## [0.8.87] - 2026-01-28

### Added

- Add `env` field to custom tool definitions for inline environment variables (closes #101)
- Custom tools from config.toml now appear in the TUI command picker with icons
- CLI `agent-deck add -c <custom-tool>` resolves tool to actual command automatically

### Fixed

- Fix `[worktree] default_location = "subdirectory"` config not being applied (fixes #110)
- Add `--location` CLI flag to override worktree placement per session (`sibling` or `subdirectory`)
- Worktree location now respects config in both CLI and TUI new session dialog

## [0.8.86] - 2026-01-28

### Fixed

- Fix changelog display dropping unrecognized lines (plain text paragraphs now preserved)
- Fix trailing-slash path completion returning directory name instead of listing contents
- Reset path autocomplete state when reopening new session dialog
- Fix double-close on LogWatcher and StorageWatcher (move watcher.Close inside sync.Once)
- Fix log worker shutdown race (replace unused channel with sync.WaitGroup)
- Fix CapturePane TOCTOU race with singleflight deduplication

### Added

- Comprehensive test suite for update package (CompareVersions, ParseChangelog, GetChangesBetweenVersions, FormatChangelogForDisplay)

## [0.8.85] - 2026-01-27

### Fixed

- Clear MCP cache before regeneration to prevent stale reads
- Cursor jump during navigation and view duplication bugs

## [0.8.83] - 2026-01-26

### Fixed

- Resume with empty session ID opens picker instead of random UUID
- Subgroup creation under selected group

### Added

- Fast text copy (`c`) and inter-session transfer (`x`)

## [0.8.79] - 2026-01-26

### Added

- Gemini model selection dialog (`Ctrl+G`)
- Configurable maintenance system with TUI feedback
- Improved status detection accuracy and Gemini prompt caching
- `.env` file sourcing support for sessions (`[shell] env_files`)
- Default dangerous mode for power users

### Fixed

- Sync session IDs to tmux env for cross-project search
- Write headers to Claude config for HTTP MCPs
- OpenCode session detection persistence and "Detecting session..." bug
- Preserve parent path when renaming subgroups

## [0.8.69] - 2026-01-20

### Added

- MCP Manager user scope: attach MCPs to `~/.claude.json` (affects all sessions)
- Three-scope MCP system: LOCAL, GLOBAL, USER
- Session sharing skill (export/import sessions between developers)
- Scrolling support for help overlay on small screens

### Fixed

- Prevent orphaned test sessions
- MCP pool quit confirmation

## [0.8.67] - 2026-01-20

### Added

- Notification bar enabled by default
- Thread-safe key bindings for background sync
- Background worker self-ticking for status updates during `tea.Exec`
- `ctrl+c to interrupt` as primary busy indicator detection
- Debug logging for status transitions

### Changed

- Reduced grace period from 5s to 1.5s for faster startup detection
- Removed 6-second animation minimum; uses status-based detection
- Hook-based polling replaces frequent tick-based detection

## [0.8.65] - 2026-01-19

### Improved

- Notification bar performance and active session detection
- Increased busy indicator check depth from 10 to 20 lines

## [0.6.1] - 2025-12-24

### Changed

- **Replaced Aider with OpenCode** - Full integration of OpenCode (open-source AI coding agent)
  - OpenCode replaces Aider as the default alternative to Claude Code
  - New icon: 🌐 representing OpenCode's open and universal approach
  - Detection patterns for OpenCode's TUI (input box, mode indicators, logo)
  - Updated all documentation, examples, and tests

## [0.1.0] - 2025-12-03

### Added

- **Terminal UI** - Full-featured TUI built with Bubble Tea
  - Session list with hierarchical group organization
  - Live preview pane showing terminal output
  - Fuzzy search with `/` key
  - Keyboard-driven navigation (vim-style `hjkl`)

- **Session Management**
  - Create, rename, delete sessions
  - Attach/detach with `Ctrl+Q`
  - Import existing tmux sessions
  - Reorder sessions within groups

- **Group Organization**
  - Hierarchical folder structure
  - Create nested groups
  - Move sessions between groups
  - Collapsible groups with persistence

- **Intelligent Status Detection**
  - 3-state model: Running (green), Waiting (yellow), Idle (gray)
  - Tool-specific busy indicator detection
  - Prompt detection for Claude Code, Gemini CLI, OpenCode, Codex
  - Content hashing with 2-second activity cooldown
  - Status persistence across restarts

- **CLI Commands**
  - `agent-deck` - Launch TUI
  - `agent-deck add <path>` - Add session from CLI
  - `agent-deck list` - List sessions (table or JSON)
  - `agent-deck remove <id|title>` - Remove session

- **Tool Support**
  - Claude Code - Full status detection
  - Gemini CLI - Activity and prompt detection
  - OpenCode - TUI element detection
  - Codex - Prompt detection
  - Generic shell support

- **tmux Integration**
  - Automatic session creation with unique names
  - Mouse mode enabled by default
  - 50,000 line scrollback buffer
  - PTY attachment with `Ctrl+Q` detach

### Technical

- Built with Go 1.24+
- Bubble Tea TUI framework
- Lip Gloss styling
- Tokyo Night color theme
- Atomic JSON persistence
- Cross-platform: macOS, Linux

[0.1.0]: https://github.com/asheshgoplani/agent-deck/releases/tag/v0.1.0
