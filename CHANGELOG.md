# Changelog

All notable changes to Agent Deck will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.9.13] - 2026-05-17

First release driven by findings from the **Weekly Regression cron**, which began producing actionable signal once the host-sensitive split landed in v1.9.12 ([#1019](https://github.com/asheshgoplani/agent-deck/pull/1019)). Two merged PRs, both closing the two halves of [#1022](https://github.com/asheshgoplani/agent-deck/issues/1022) (the cron's first real report): a visual-regression fix that restores the `<header>` landmark in the web shell, and a perf fix that code-splits Chart.js off the initial-paint payload to clear the Lighthouse budget overshoot. v1.9.13 is the **eighth release cut under the Option A pipeline** ([#981](https://github.com/asheshgoplani/agent-deck/pull/981) in v1.9.6); the local release worker stops at `git push origin <tag>` and `.github/workflows/release.yml` is the single source of truth for `goreleaser release --clean`.

### Fixed

- **Web shell restores the `<header>` semantic element** ([#1022](https://github.com/asheshgoplani/agent-deck/issues/1022) part 1, [PR #1024](https://github.com/asheshgoplani/agent-deck/pull/1024)). The Topbar component (introduced in the PR-B redesign port, b923e8bc / [#860](https://github.com/asheshgoplani/agent-deck/pull/860)) rendered its root as `<div class="topbar">` instead of `<header class="topbar">`. Several Playwright visual tests in the Weekly Regression suite gate on `page.waitForSelector('header', ...)` to detect when the Preact app has mounted; without a `<header>` landmark in the rendered DOM they timed out before the screenshot step, so the suite reported failures with no diff image to triage. Fix changes `Topbar()`'s root element back to `<header class="topbar">`; the CSS grid in `app.css` targets the `.topbar` class (not the tag), so layout and styling are unaffected — the change is purely semantic. Pinned by `internal/web/issue1022_header_test.go`, which asserts `Topbar.js` source contains a `<header>` opening tag and no longer contains the old `<div class="topbar">` root. Closes #1022 part 1 (PR #1024).

### Performance

- **`chart.umd.min.js` is now code-split and lazy-loads on the Costs route** ([#1022](https://github.com/asheshgoplani/agent-deck/issues/1022) part 2, [PR #1025](https://github.com/asheshgoplani/agent-deck/pull/1025)). The Weekly Regression Lighthouse run reported `script.size` 291 KB vs the 120 KB budget (2.4× over) and `total-byte-weight` 399 KB vs 180 KB (2.2× over). Confirmed cause: `index.html` eagerly loaded the 206 KB `chart.umd.min.js` even though only the Costs route consumes Chart.js. Fix removes the eager `<script src="/static/chart.umd.min.js" defer>` from `index.html` and dynamically injects the same asset from `CostDashboard.js` the first time the Costs tab renders; the loader caches a single Promise so concurrent mounts share one fetch and there's no second-paint race. Initial-paint payload: ~313 KB → ~107 KB (107 KB app JS + 206 KB chart → app JS only; chart deferred to Costs route). Pinned by `internal/web/issue1022_codesplit_test.go`, which asserts the served `index.html` and the entry JS (`main.js`, `App.js`) do not reference `chart.umd`, locking in the savings. Closes #1022 part 2 (PR #1025).

### Known issues

- `HOMEBREW_TAP_GITHUB_TOKEN` repo secret is not yet set, so the brew tap formula update step in `release.yml` will fail for v1.9.13 (known, user action — does not block the release tarballs or the GitHub release itself; same as v1.9.6 through v1.9.12).

## [1.9.12] - 2026-05-17

UX + perf + test infra sweep on top of v1.9.11 — four merged PRs covering two user-facing fixes, one perf-oriented addition, and one test-infra cleanup. v1.9.12 is the **seventh release cut under the Option A pipeline** ([#981](https://github.com/asheshgoplani/agent-deck/pull/981) in v1.9.6); the local release worker stops at `git push origin <tag>` and `.github/workflows/release.yml` is the single source of truth for `goreleaser release --clean`. Headline fixes: heartbeat NEED lines now auto-retire after 3 unanswered cycles instead of repeating verbatim for 12–21 hours and burying fresh urgent items ([#1017](https://github.com/asheshgoplani/agent-deck/pull/1017), closes [#971](https://github.com/asheshgoplani/agent-deck/issues/971)); and the New-session dialog's path-suggestions popup no longer swallows Up/Down arrows when focus has just Tab-landed on a pre-filled path, restoring the user's ability to navigate up/down out of the path section ([#1021](https://github.com/asheshgoplani/agent-deck/pull/1021), closes [#1020](https://github.com/asheshgoplani/agent-deck/issues/1020) — credit @JMBattista). Headline addition: the `web` subcommand gains a `--no-tui` flag that boots the HTTP server without bubbletea, saving 5 MB / 14% RSS at cold boot on Linux and addressing @arioliveira's "heavy on M4" complaint where bubbletea's macOS working set was traced at >60 MB ([#1018](https://github.com/asheshgoplani/agent-deck/pull/1018)). Test infra: four host-sensitive tests are now gated behind a `hostsensitive` build tag so the default pre-push and CI runs stay green; the Weekly Regression cron opts in with `-tags hostsensitive` and now runs cleanly ([#1019](https://github.com/asheshgoplani/agent-deck/pull/1019), closes [#969](https://github.com/asheshgoplani/agent-deck/issues/969)).

### Fixed

- **Heartbeat NEED lines auto-retire after 3 cycles** ([#971](https://github.com/asheshgoplani/agent-deck/issues/971), [PR #1017](https://github.com/asheshgoplani/agent-deck/pull/1017)). The conductor bridge's heartbeat loop emitted `NEED:` lines verbatim every cycle, so an unanswered NEED would repeat unchanged for 12–21 hours — once the user tuned out, fresh urgent items got buried under the same repeating line. Fix in `conductor/bridge.py` adds a pure helper `filter_need_lines(response, prev_counts, threshold=3)` with three-state behaviour: cycles 1..threshold-1 forward the NEED line as-is, cycle `threshold` emits a one-shot `STILL BLOCKED (3 cycles, no reply)` escalation in place of the verbatim NEED, and cycles `threshold+1..` drop the line silently (auto-retire). Wired into `heartbeat_loop` with per-conductor in-memory state so each conductor's NEED stream is tracked independently; NEED lines that stop appearing get cleared from state, so a recurring problem is re-counted fresh next time it shows up. Pinned by `conductor/tests/test_issue971_need_retire.py` (9 cases: per-cycle behaviour, multi-line independence, configurable threshold, "fresh NEED still passes" non-regression). Closes #971 (PR #1017).

- **Path-suggestions popup no longer swallows Up/Down when focus is Tab-landed on a pre-filled path** ([#1020](https://github.com/asheshgoplani/agent-deck/issues/1020), [PR #1021](https://github.com/asheshgoplani/agent-deck/pull/1021), credit @JMBattista). v1.9.x's #983 closed @paskal's #896 sub-bugs 3+4 by auto-activating the path-suggestions popup on the first Up/Down whenever it was visible. That worked for the active-editing flow but @JMBattista reported the side effect: once focus lands on a Tab-pre-filled path, arrows always got swallowed by the popup and the cursor could never move up/down out of the path section. Discriminator was already in the code — `pathSoftSelected` is true the moment focus lands on a path field with a pre-filled value (pathInput blurred, user navigating between fields), and the soft-select handler clears it on the first real keystroke (the boundary between "Tab-landed" and "actively-editing"). Fix in `internal/ui/newdialog.go` gates the auto-activate on `!d.pathSoftSelected`, restoring pre-#983 escape behaviour for #1020 while keeping the post-typing path that #896 sub-bugs 3+4 fix relies on. Explicit popup entry stays available via Space or Right per the existing soft-select handler. The #896 sub-bug 3+4 regression tests previously bypassed the soft-select handler via direct `pathInput.SetValue`, leaving them in a synthetic soft-selected-with-value state that doesn't happen via real keystrokes — each test now mirrors the post-typing state (`pathInput.Focus()` + `pathSoftSelected = false`) so they cover the actual user flow they were always meant to describe. Pinned by `internal/ui/issue1020_path_selector_ux_test.go`. Closes #1020 (PR #1021).

### Added

- **`agent-deck web --no-tui` for headless web mode** ([PR #1018](https://github.com/asheshgoplani/agent-deck/pull/1018), addresses @arioliveira "heavy on M4" complaint). The `web` subcommand previously booted a full bubbletea TUI in the same process as the HTTP server, costing 5–30+ MB of RSS overhead depending on workload (the `adeck-test-webui` worker traced ~60 MB steady-state to bubbletea + eager TUI initialization on macOS M4). `--no-tui` runs HTTP-only: bubbletea is never constructed (skips `tea.NewProgram`, `p.Run`, maintenance worker, kitty-keyboard disable, CSIu reader wrap), `main()` blocks on `server.Start()` in the foreground and returns on server shutdown, and the nested-session / outer-tmux / update-prompt guards are skipped (all TUI-specific and harmless to headless boot). Sessions remain manageable via the web UI; `MemoryMenuData` falls back to `SessionDataService` (storage-backed) when no in-memory snapshot is published. Default behaviour is unchanged — without `--no-tui`, the TUI boots as today. Benchmark (Linux, cold boot, empty profile): `--no-tui` peak RSS 30.5 MB vs. with-TUI 35.4 MB, saved 5 MB (14%); the Linux number is the floor — savings grow with bubbletea's working set under load (rendered widgets, populated session lists, macOS terminal overhead, the >60 MB observed on M4). Pinned by `cmd/agent-deck/issue_perf_no_tui_test.go` (two arms: `flag_extraction` pure unit on `extractNoTuiFlag`; `headless_server_starts` subprocess that runs the binary with `--no-tui` and a free listen port, asserts HTTP responds within 5s — only passable if bubbletea actually skipped, since bubbletea panics on stdin without a TTY and would kill the process before the server came up). (PR #1018.)

### Test infra

- **Host-sensitive tests split behind a `hostsensitive` build tag** ([#969](https://github.com/asheshgoplani/agent-deck/issues/969), [PR #1019](https://github.com/asheshgoplani/agent-deck/pull/1019)). Four tests with environment-dependent flakes are now gated behind the `hostsensitive` build tag, so the default pre-push and CI runs stay deterministic while the Weekly Regression cron opts in with `-tags hostsensitive` and runs them on hosts known to satisfy their preconditions. Tests moved: `TestWatcherEventDedup` (`internal/statedb/statedb_hostsensitive_test.go`, races two goroutines on a shared SQLite handle — `-race` + kernel scheduling tripped `SQLITE_BUSY` non-deterministically); `TestSession_SetAndGetEnvironment` (`internal/tmux/tmux_hostsensitive_test.go`, depends on a live external tmux server and is sensitive to per-session env table being clean of prior state — flaky on hosts with lingering tmux servers); `TmuxSurvivesLoginSessionRemoval` plus its two test-only helpers `startFakeLoginScope` / `startAgentDeckTmuxInUserScope` (`internal/session/session_persistence_hostsensitive_test.go`, requires `systemd-run --user`, a running user systemd manager tracking MainPID for the spawned scope, and no lingering tmux racing teardown — not the case on most CI runners, inside nested tmux, or without `loginctl enable-linger`); and `TestTmuxPTYBridgeResize` (in-place build-tag promotion in `internal/web/terminal_bridge_integration_test.go`, the inline `CI`/`GITHUB_ACTIONS` env skip didn't catch every headless environment without real PTY winsize propagation). Opt-in remains available via `go test -tags hostsensitive -race ./...` on machines that satisfy the preconditions. Closes #969 (PR #1019).

### Known issues

- `HOMEBREW_TAP_GITHUB_TOKEN` repo secret is not yet set, so the brew tap formula update step in `release.yml` will fail for v1.9.12 (known, user action — does not block the release tarballs or the GitHub release itself; same as v1.9.6 through v1.9.11).

## [1.9.11] - 2026-05-16

Small follow-up sweep on top of v1.9.10 — four merged PRs covering one user-facing fix, one infrastructure addition, and two documentation entries. v1.9.11 is the **sixth release cut under the Option A pipeline** ([#981](https://github.com/asheshgoplani/agent-deck/pull/981) in v1.9.6); the local release worker stops at `git push origin <tag>` and `.github/workflows/release.yml` is the single source of truth for `goreleaser release --clean`. Headline fix: the `NoTransitionNotify` mute flag is now honored on inbox / deferred-queue replay, closing the third reported variant of #962 — completing the unification of all three variants behind a single `eventDeliverable` gate shared between emission and replay ([#1014](https://github.com/asheshgoplani/agent-deck/pull/1014)). Headline addition: agent-deck now recognizes the bare-repo-at-root worktree layout alongside the existing nested `.bare/` convention, with strict self-discrimination so `findNestedBareRepo` no longer misidentifies internal git subdirs (`hooks/`, `objects/`, `refs/`) as nested bare repos ([#1016](https://github.com/asheshgoplani/agent-deck/pull/1016), takeover of @keelerm84's [#1011](https://github.com/asheshgoplani/agent-deck/pull/1011), closes [#891](https://github.com/asheshgoplani/agent-deck/issues/891) follow-up). Documentation: a 514-line `docs/internal/state-db-schema.md` now documents every table in the SQLite state.db that backs agent-deck profiles ([#1015](https://github.com/asheshgoplani/agent-deck/pull/1015), closes [#975](https://github.com/asheshgoplani/agent-deck/issues/975)); and the worker-prompt template now includes a "Step 0 — Prelude reads" convention so conductor-spawned workers stop hitting Claude Code's "must Read before Edit/Write" tool guard mid-cycle ([#1013](https://github.com/asheshgoplani/agent-deck/pull/1013), closes [#968](https://github.com/asheshgoplani/agent-deck/issues/968)).

### Fixed

- **Transition notifier respects `NoTransitionNotify` mute flag on inbox / deferred-queue replay** ([#962](https://github.com/asheshgoplani/agent-deck/issues/962) v3, [PR #1014](https://github.com/asheshgoplani/agent-deck/pull/1014)). Reported by @seanyoungberg as a third variant of #962: the per-session `NoTransitionNotify` flag was checked at *new* event emission (`transition_daemon.go:210` and `:375`) but pre-fix was never re-consulted during inbox / deferred-queue replay. Once an event sat in the queue, toggling `agent-deck session set-transition-notify <child> off` had no effect on already-queued re-deliveries — the user-observed symptom was 4–5 `[EVENT]` fires per single underlying transition into the conductor pane after the mute toggle. Architecturally this is the same shape as variants 1 (sessions removed between enqueue and drain, [PR #992](https://github.com/asheshgoplani/agent-deck/pull/992) in v1.9.8) and 2 (target-busy inbox entries never cleaned, [PR #1009](https://github.com/asheshgoplani/agent-deck/pull/1009) in v1.9.10) — the replay path never re-validated against current session state. Fix generalizes the existing `childPresence` resolver into a single `eventDeliverable` gate that returns `(deliverable, reason)` and centralizes the per-session predicate via `instanceAcceptsTransitionEvents` shared between emission and replay; future per-session bypass conditions (paused, conductor-stopped, etc.) plug in here only. Pinned by `TestTransitionNotifier_MuteFlagRespectedOnReplay_RegressionFor962V3` in `internal/session/issue962_v3_mute_replay_test.go`: enqueue 5 deferred events for a child → flip `NoTransitionNotify=true` → drain with target available → zero dispatches, queue drained. Pre-fix dispatches 4; post-fix dispatches 0. Variant 1 and variant 2 regression tests still green. Closes #962 v3 (PR #1014).

- **`findNestedBareRepo` no longer misidentifies internal git subdirs as nested bare repos** ([PR #1016](https://github.com/asheshgoplani/agent-deck/pull/1016)). It used `IsBareRepo` (which calls `git rev-parse --is-bare-repository`) on each child of a candidate dir; that subcommand walks up the directory tree via repo discovery and reports `true` for *every descendant* of a bare repo, including `hooks/`, `objects/`, `refs/`, etc. Pre-fix, calling `findNestedBareRepo` on a bare-at-root dir would return one of those subdirs as "the nested bare repo." The public APIs avoided this because they all check `IsGitRepo` first, but `MergeBack` did invoke the helper on potentially-bare paths and only "worked" because git rev-parse rescued by walking up. New `isBareRepoSelf` helper combines `--is-bare-repository == true` with `--git-dir == .` (or its resolved absolute equivalent) to confirm the candidate is itself the bare repo, not just a descendant. `findNestedBareRepo` and (as a post-takeover follow-up to the review note on #1011) `IsBareRepoAtRoot` both use it. Original fix by @keelerm84 in [#1011](https://github.com/asheshgoplani/agent-deck/pull/1011); review-note follow-up applied during the takeover.

- **`MergeBack` short-circuits to `mergeBackInBareRepo` when projectRoot is itself a bare repo** ([PR #1016](https://github.com/asheshgoplani/agent-deck/pull/1016)). Pre-fix it called `findNestedBareRepo(projectRoot)` even when the path was already a bare dir, then patched up empty results with an `IsBareRepo` check. It only produced correct behavior because `git rev-parse` inside the misidentified subdir would discover the parent bare repo. With the strict-self fix above, that accidental rescue went away — `MergeBack` now short-circuits to `mergeBackInBareRepo(projectRoot, …)` as soon as `IsBareRepo(projectRoot)` is true. Original fix by @keelerm84 in [#1011](https://github.com/asheshgoplani/agent-deck/pull/1011).

### Added

- **Agent-deck now recognizes the bare-repo-at-root worktree layout** ([#891](https://github.com/asheshgoplani/agent-deck/issues/891) follow-up, [PR #1016](https://github.com/asheshgoplani/agent-deck/pull/1016), takeover of @keelerm84's [#1011](https://github.com/asheshgoplani/agent-deck/pull/1011)) alongside the existing nested `.bare/` convention. The two are distinguished by basename: `.bare` ⇒ nested (project root is the parent dir), anything else ⇒ at-root (the bare dir itself is the project root and linked worktrees live as direct children alongside `HEAD`/`objects/`/`refs/`). Concretely: a plain `git clone --bare repo.git` checkout where the user adds worktrees inside the bare dir — e.g. `~/code/proj.git/{main,feature-x}/` with `~/code/proj.git/` being the bare repo — is now a first-class layout. Pre-fix, three flows misbehaved against this layout: `GetMainWorktreePath` returned the *parent* of the bare dir (a generic dir holding unrelated projects), `GetWorktreeBaseRoot` errored out via `git rev-parse --show-toplevel` (which has no meaning on a bare repo), and `GenerateWorktreePath` in `sibling` mode would have placed new worktrees at `<bare-dir>-<branch>` (outside the bare dir entirely). New `IsBareRepoAtRoot` predicate drives the branching in `GetMainWorktreePath`, `GetWorktreeBaseRoot`, and `GenerateWorktreePath` — the at-root layout auto-overrides `sibling`/`subdirectory` so new worktrees land at `<bareRoot>/<branch>`, matching what `agent-deck worktree list` already enumerates. Custom `path_template` config still wins. README's "Bare repositories and worktrees" section now documents both layouts side-by-side. Pinned by `internal/git/bare_at_root_test.go`. Original investigation and patch by @keelerm84 in #1011; re-applied after merge-train conflicts and extended with the strict-self discriminator (PR #1016).

- **Worker-prompt "Step 0 — Prelude reads" convention** ([#968](https://github.com/asheshgoplani/agent-deck/issues/968), [PR #1013](https://github.com/asheshgoplani/agent-deck/pull/1013)). Conductor-spawned workers were hitting Claude Code's "must Read before Edit/Write" tool guard mid-cycle because their `PROMPT.md` jumped straight into edits without first reading the files they were about to touch. Fix is template-level, not per-worker: `skills/agent-deck/SKILL.md` gains a new "Worker Prompt Conventions" subsection in Sub-Agent Launch with a "Step 0 — Prelude reads" template skeleton that future prompt-authors copy, and `skills/agent-deck/references/goal.md` injects Step 0 into the autonomous worker contract so goal-driven workers get the rule for free across every cycle re-fire. Closes #968 (PR #1013).

### Docs

- **`state.db` schema reference** ([#975](https://github.com/asheshgoplani/agent-deck/issues/975), [PR #1015](https://github.com/asheshgoplani/agent-deck/pull/1015)). New `docs/internal/state-db-schema.md` (514 lines) documents every table in the SQLite state.db that backs agent-deck profiles: `metadata`, `instances`, `groups`, `instance_heartbeats`, `recent_sessions`, `cost_events`, `watchers`, `watcher_events`. Per-column type, constraints, semantics, examples plus the `tool_data` JSON blob shape, migration history, and Go type mappings. Verified against a `.schema` dump from a freshly-initialised profile. Closes #975 (PR #1015).

### Known issues

- `HOMEBREW_TAP_GITHUB_TOKEN` repo secret is not yet set, so the brew tap formula update step in `release.yml` will fail for v1.9.11 (known, user action — does not block the release tarballs or the GitHub release itself; same as v1.9.6 through v1.9.10).

## [1.9.10] - 2026-05-16

Major sweep release on top of v1.9.9 — eight merged PRs covering five user-facing fixes, two infrastructure additions, and one repo-hygiene change. v1.9.10 is the **fifth release cut under the Option A pipeline** ([#981](https://github.com/asheshgoplani/agent-deck/pull/981) in v1.9.6); the local release worker stops at `git push origin <tag>` and `.github/workflows/release.yml` is the single source of truth for `goreleaser release --clean`. Headline fixes: chat history is now preserved across `conductor restart` so users no longer lose the active conversation when bouncing a conductor ([#1010](https://github.com/asheshgoplani/agent-deck/pull/1010), closes [#956](https://github.com/asheshgoplani/agent-deck/issues/956)); `agent-deck launch` detaches the tmux server from the SSH login cgroup so sessions survive SSH logout — the long-standing "sessions die when I disconnect" footgun ([#1008](https://github.com/asheshgoplani/agent-deck/pull/1008), closes [#958](https://github.com/asheshgoplani/agent-deck/issues/958)); the transition-notifier now cleans target-busy inbox entries on redelivery and enforces a TTL, closing a second-order variant of #962 that surfaced after v1.9.8's deferred-queue fix ([#1009](https://github.com/asheshgoplani/agent-deck/pull/1009)); the feedback dialog's "don't ask again" is now scoped per-release-series instead of forever, so a user who opted out of v1.9.x feedback still gets prompted on v1.10.x ([#1004](https://github.com/asheshgoplani/agent-deck/pull/1004), closes [#967](https://github.com/asheshgoplani/agent-deck/issues/967)); and worker worktrees always root at fresh `origin/<default>` instead of whatever the local main happens to point to, eliminating a class of "PR worker built on stale main" bugs ([#1005](https://github.com/asheshgoplani/agent-deck/pull/1005), closes [#973](https://github.com/asheshgoplani/agent-deck/issues/973)). Headline additions: `agent-deck launch` now enforces a concurrency cap to prevent swap thrash when a conductor fan-outs N child sessions in parallel on a memory-constrained host ([#1003](https://github.com/asheshgoplani/agent-deck/pull/1003), closes [#964](https://github.com/asheshgoplani/agent-deck/issues/964)); and the MCP child-reap mechanism shipped in v1.9.9 (#1000) is now wired into the production MCP spawn paths via process-tree discovery, so stopped sessions no longer leak orphan stdio MCP processes ([#1006](https://github.com/asheshgoplani/agent-deck/pull/1006), completes the #1000 mechanism). Repo hygiene: `.planning/` is now untracked, following the same per-developer-local-file pattern established for `CLAUDE.md` in v1.9.9's #1002 ([#1007](https://github.com/asheshgoplani/agent-deck/pull/1007), closes [#970](https://github.com/asheshgoplani/agent-deck/issues/970)).

### Fixed

- **Chat history preserved across conductor restart** ([#956](https://github.com/asheshgoplani/agent-deck/issues/956), [PR #1010](https://github.com/asheshgoplani/agent-deck/pull/1010)). When a conductor session was restarted via `agent-deck session restart` (or via the daemon's auto-restart path), the conductor's chat scrollback was thrown away because the restart path only preserved the session's metadata row and re-launched the agent process — the on-disk Claude Code history file for that profile/session was not bound back to the new agent's stdin context. Users observed a fresh "How can I help?" prompt instead of the prior conversation. Fix in `internal/session/instance.go`: the restart path now resolves and re-binds the prior history file before the new agent boots, so the agent re-enters with the same conversation context it had pre-restart. Pinned by `internal/session/issue956_chat_history_restart_test.go`. Closes #956 (PR #1010).

- **`agent-deck launch` detaches tmux server from login cgroup to survive SSH logout** ([#958](https://github.com/asheshgoplani/agent-deck/issues/958), [PR #1008](https://github.com/asheshgoplani/agent-deck/pull/1008)). When a user ran `agent-deck launch <path>` over SSH and then disconnected, systemd's `KillUserProcesses=yes` (default on most modern distros) reaped the entire SSH login cgroup — including the freshly-spawned tmux server — even though tmux conventionally daemonizes. Sessions appeared to launch successfully then vanish minutes later. Fix consolidates launch-settings wire-up into a single helper (`internal/session/issue958_launch_settings_wiring_test.go`) that explicitly detaches the tmux server from the login cgroup at spawn time, so `loginctl terminate-session` no longer cascades into the agent-deck session tree. Closes #958 (PR #1008).

- **Transition-notifier cleans target-busy inbox entries on redelivery + enforces TTL** ([#962](https://github.com/asheshgoplani/agent-deck/issues/962) variant, [PR #1009](https://github.com/asheshgoplani/agent-deck/pull/1009)). v1.9.8's #992 closed the most common #962 spam class (replayed events for removed sessions), but a second-order variant remained: when a conductor was *busy* at delivery time, the notifier correctly deferred — but on redelivery the prior "target-busy" inbox entry was not cleaned, accumulating one stale entry per retry until the inbox file grew to thousands of duplicate lines. Fix in `internal/session/inbox.go`: redelivery now removes the prior `target-busy` entry for the same (child, event) tuple, and the inbox enforces a TTL on `target-busy`-class entries so a permanently-busy conductor doesn't accumulate them indefinitely. Pinned by `internal/session/issue962_target_busy_ttl_test.go`. Closes #962 variant (PR #1009).

- **Feedback "don't ask again" is now per-release-series instead of forever** ([#967](https://github.com/asheshgoplani/agent-deck/issues/967), [PR #1004](https://github.com/asheshgoplani/agent-deck/pull/1004)). The feedback dialog's "don't ask me again" checkbox wrote an unbounded opt-out to `feedback-state.json` — a user who clicked it once in v1.9.0 would never be prompted again, even after multiple minor-version upgrades. Fix in `internal/feedback/state.go`: the opt-out is now scoped to the current MAJOR.MINOR series (e.g. opting out in v1.9.4 suppresses prompts for the rest of v1.9.x but resumes prompting at v1.10.0). The state file format gains a `major_minor` field; legacy entries (no field) are treated as opted-out only for the version they were recorded against, so existing users aren't suddenly re-prompted on the v1.9.10 upgrade itself. Pinned by `internal/feedback/feedback_test.go` additions. Closes #967 (PR #1004).

- **Worker worktrees always root at fresh `origin/<default>`** ([#973](https://github.com/asheshgoplani/agent-deck/issues/973), [PR #1005](https://github.com/asheshgoplani/agent-deck/pull/1005)). The PR worker's worktree-creation path branched from whatever the local `main` pointed at — which on a stale workstation could be hours or days behind `origin/main`. Workers thus produced PRs against a stale base, and even after rebase the test harness ran against the worker's stale base for the first run. Fix in `internal/git/git.go`: new-branch worktree creation now performs an explicit `git fetch origin <default>` and roots the new branch at `origin/<default>` rather than at the local symbolic ref. Pinned by `internal/git/issue973_worker_spawn_fresh_main_test.go`. Closes #973 (PR #1005).

### Added

- **`agent-deck launch` concurrency cap to prevent swap thrash** ([#964](https://github.com/asheshgoplani/agent-deck/issues/964), [PR #1003](https://github.com/asheshgoplani/agent-deck/pull/1003)). On memory-constrained hosts (≤16 GB), a conductor that fan-outs N child sessions via parallel `agent-deck launch` calls could OOM the host — each Claude Code child process is ~1.5–2 GB resident, and N=8 trivially exceeds available RAM, pushing the host into swap thrash that takes the whole machine unresponsive. Fix in `cmd/agent-deck/launch_throttle.go`: introduce a process-wide semaphore that caps in-flight launches; the cap is configurable via `AGENT_DECK_LAUNCH_MAX_CONCURRENCY` (default derived from available memory) and waits politely rather than failing the launch. Pinned by `cmd/agent-deck/issue964_launch_cap_test.go`. Closes #964 (PR #1003).

- **MCP child-reap wiring via process-tree discovery — completes the v1.9.9 #1000 mechanism** ([#1000](https://github.com/asheshgoplani/agent-deck/pull/1000) follow-up, [PR #1006](https://github.com/asheshgoplani/agent-deck/pull/1006)). v1.9.9 shipped the `mcp_child_reap.go` primitive that walks the MCP catalog and signals registered child PIDs, but the production session-stop path had no callers — the reap was unit-tested but never fired in real use. v1.9.10 closes the loop: `internal/session/mcp_child_reap.go` is now invoked from the session-stop path via process-tree discovery (so even MCP children not in the registry — e.g. spawned by a runaway plugin — are reaped if their parent was the stopping session). Pinned by `internal/session/issue965_wiring_test.go`. Completes the #1000 mechanism (PR #1006).

### Changed

- **`.planning/` is now untracked** ([#970](https://github.com/asheshgoplani/agent-deck/issues/970), [PR #1007](https://github.com/asheshgoplani/agent-deck/pull/1007)). `.planning/` holds per-developer local planning notes that should never be committed — the same pattern v1.9.9's #1002 applied to `CLAUDE.md`. v1.9.10 extends the pattern: `.planning/` is removed from version control and added to `.gitignore`. Existing local copies are preserved; the directory is simply no longer tracked. Closes #970, completes the #1002 pattern (PR #1007).

### Known issues

- `HOMEBREW_TAP_GITHUB_TOKEN` repo secret is not yet set, so the brew tap formula update step in `release.yml` will fail for v1.9.10 (known, user action — does not block the release tarballs or the GitHub release itself; same as v1.9.6 through v1.9.9).

## [1.9.9] - 2026-05-15

Patch release on top of v1.9.8 — five user-facing fixes spanning the launch path, MCP plugin lifecycle, session reaping, send-after-restart timing, and the web waiting-status renderer. v1.9.9 is the **fourth release cut under the Option A pipeline** ([#981](https://github.com/asheshgoplani/agent-deck/pull/981) in v1.9.6); the local release worker stops at `git push origin <tag>` and `.github/workflows/release.yml` is the single source of truth for `goreleaser release --clean`. Headline fixes: `agent-deck launch` no longer propagates `TELEGRAM_STATE_DIR` into child sessions, eliminating duplicate Telegram pollers that fired on every conductor-spawned child ([#998](https://github.com/asheshgoplani/agent-deck/pull/998), closes [#955](https://github.com/asheshgoplani/agent-deck/issues/955)); MCP pool now refreshes `.mcp.json` plugin pins on session upgrade so stale pin entries don't survive a plugin version bump ([#999](https://github.com/asheshgoplani/agent-deck/pull/999), closes [#960](https://github.com/asheshgoplani/agent-deck/issues/960)); the web event renderer maps `waiting` status to a waiting badge instead of misclassifying it as an error ([#997](https://github.com/asheshgoplani/agent-deck/pull/997), closes [#963](https://github.com/asheshgoplani/agent-deck/issues/963)); session stop now reaps lingering MCP child processes so a stopped session no longer leaves orphan stdio servers attached to the daemon — mechanism shipped, full session-cmd wiring follow-up TBD ([#1000](https://github.com/asheshgoplani/agent-deck/pull/1000), closes [#965](https://github.com/asheshgoplani/agent-deck/issues/965)); and `session send` after a restart waits for slash-command registration before dispatching the first slash payload, fixing the race that silently dropped the message ([#1001](https://github.com/asheshgoplani/agent-deck/pull/1001), closes [#966](https://github.com/asheshgoplani/agent-deck/issues/966)). (Note: REQ-7 [#989] and Node 24 actions [#991] remain pending and are deferred to v1.9.10.)

### Fixed

- **`agent-deck launch` strips `TELEGRAM_STATE_DIR` from child env to prevent duplicate Telegram pollers** ([#955](https://github.com/asheshgoplani/agent-deck/issues/955), [PR #998](https://github.com/asheshgoplani/agent-deck/pull/998)). When a conductor with `TELEGRAM_STATE_DIR` exported in its process env spawned a child via `agent-deck launch <path>`, the env var leaked into the child session's wrapper. The plugin-telegram MCP server treats a non-empty `TELEGRAM_STATE_DIR` as a signal to attach the bot poller — so every conductor-spawned child silently started a second `bun telegram` process polling the same bot token, producing duplicate inbound messages and double-reactions in the conductor. Fix in `internal/session/env.go`: launch-time env construction now explicitly removes `TELEGRAM_STATE_DIR` (and any other channel-scoped state vars that must not propagate) before the child wrapper command is materialized. The conductor's own poller is unaffected because it runs under the conductor's `env_file`-injected env, not the inherited shell env. Pinned by `cmd/agent-deck/issue955_telegram_env_strip_test.go`. Closes #955 (PR #998).

- **MCP pool refreshes `.mcp.json` plugin pins on session upgrade** ([#960](https://github.com/asheshgoplani/agent-deck/issues/960), [PR #999](https://github.com/asheshgoplani/agent-deck/pull/999)). The `.mcp.json` file written by the MCP pool included plugin pin entries (`enabledPlugins` + per-plugin version pins) that were materialized once at session creation and never refreshed. When a plugin's catalog version advanced (e.g. `telegram@claude-plugins-official` published a new version), an existing session kept its old pin and continued running the stale plugin until the user manually edited `.mcp.json` or removed and re-created the session. Fix in `internal/session/pin_refresh.go`: the MCP catalog write path now diffs the existing `.mcp.json` pins against the current catalog state and rewrites the pin block when a drift is detected, preserving any user-edited fields outside the pin block. Pinned by `internal/session/issue960_pin_refresh_test.go`. Closes #960 (PR #999).

- **Web event renderer treats `waiting` status as waiting, not error** ([#963](https://github.com/asheshgoplani/agent-deck/issues/963), [PR #997](https://github.com/asheshgoplani/agent-deck/pull/997)). The SSE event payload in `internal/web/handlers_events.go` mapped session statuses to badge classes via a switch that lacked a `waiting` case — so a session in the `waiting` status (the most common attention-needed signal, fired on `AskUserQuestion` and `EnterPlanMode`) fell through to the `default` branch which rendered as the `error` badge class. Users reported sessions appearing red on the web dashboard despite no actual error. Fix: add explicit `waiting` → `status-waiting` mapping. Pinned by `internal/web/issue963_waiting_status_test.go`. Closes #963 (PR #997).

- **Session stop reaps lingering MCP child processes (mechanism shipped)** ([#965](https://github.com/asheshgoplani/agent-deck/issues/965), [PR #1000](https://github.com/asheshgoplani/agent-deck/pull/1000)). When a session was stopped, its attached MCP stdio servers (one process per attached MCP) were left running because the stop path only signaled the top-level Claude Code process and relied on the OS to GC the orphans — but stdio MCPs daemonized into their own session and survived parent death, accumulating on the host until the user manually killed them. Fix: new `internal/session/mcp_child_reap.go` introduces a reaping primitive that walks the MCP catalog for the stopping session and sends SIGTERM (then SIGKILL after grace) to each registered child PID. Mechanism is shipped and unit-tested; the session-cmd wiring that calls it from the stop path is a follow-up tracked separately so the reap can be exercised by the daemon before being wired into the user-facing command. Pinned by `internal/session/issue965_mcp_reap_test.go`. Closes #965 (PR #1000, wiring follow-up TBD).

- **`session send` after restart waits for slash-command registration before dispatching** ([#966](https://github.com/asheshgoplani/agent-deck/issues/966), [PR #1001](https://github.com/asheshgoplani/agent-deck/pull/1001)). Immediately after a session restart, Claude Code re-registers its slash commands asynchronously — there's a ~500ms–2s window where the agent is "ready" (responds to the keystroke probe) but slash-command dispatch is not yet wired. A `session send` issued in that window with a slash-prefixed payload (`/foo …`) was delivered as literal text instead of being recognized as a command, producing a silent miss. Fix in `cmd/agent-deck/session_cmd.go`: `waitForAgentReady` now additionally polls for slash-command registration before returning when the payload begins with `/`, with a bounded extra wait. Non-slash payloads keep the prior ready-only semantics. Pinned by `cmd/agent-deck/issue966_slash_after_restart_test.go`. Closes #966 (PR #1001).

### Known issues

- `HOMEBREW_TAP_GITHUB_TOKEN` repo secret is not yet set, so the brew tap formula update step in `release.yml` will fail for v1.9.9 (known, user action — does not block the release tarballs or the GitHub release itself; same as v1.9.6, v1.9.7, and v1.9.8).

## [1.9.8] - 2026-05-15

Patch release on top of v1.9.7 — a quality-of-life sweep that closes five user-facing CLI/notify papercuts plus one CLI ergonomics gap. v1.9.8 is the **third release cut under the Option A pipeline** ([#981](https://github.com/asheshgoplani/agent-deck/pull/981) in v1.9.6); the local release worker stops at `git push origin <tag>` and `.github/workflows/release.yml` is the single source of truth for `goreleaser release --clean`. Headline fixes: the transition-notifier's deferred retry queue no longer replays transition events for sessions removed via `agent-deck rm` — a class of bug responsible for all-day stale-event spam on conductor-innotrade ([#992](https://github.com/asheshgoplani/agent-deck/pull/992), closes [#962](https://github.com/asheshgoplani/agent-deck/issues/962)); `agent-deck rm` is now parallel-safe at the CLI-subprocess layer (the structural fix landed in #909 in v1.9.1, but until #993 there was no regression coverage at the user-facing surface that pinned `xargs -P N agent-deck rm`-style invocation) and `session remove <bogus>` correctly exits non-zero ([#993](https://github.com/asheshgoplani/agent-deck/pull/993), closes [#961](https://github.com/asheshgoplani/agent-deck/issues/961)); `agent-deck launch <path>` invoked from inside a conductor now derives the group from the cwd instead of inheriting the parent's `conductor` group ([#994](https://github.com/asheshgoplani/agent-deck/pull/994), closes [#972](https://github.com/asheshgoplani/agent-deck/issues/972)); `session send --timeout` now actually extends the agent-ready wait instead of bailing at a hardcoded 80s ([#995](https://github.com/asheshgoplani/agent-deck/pull/995), closes [#957](https://github.com/asheshgoplani/agent-deck/issues/957)); and 17+ alias forms across `session`, `group`, and `launch` are now accepted instead of silently rejected ([#996](https://github.com/asheshgoplani/agent-deck/pull/996), closes [#974](https://github.com/asheshgoplani/agent-deck/issues/974)). (Note: REQ-7 [#989] and Node 24 actions [#991] remain pending CI/web-UI merge respectively and are deferred to v1.9.9.)

### Fixed

- **`session send --timeout` actually extends the agent-ready wait — was hardcoded to 80s** ([#957](https://github.com/asheshgoplani/agent-deck/issues/957), [PR #995](https://github.com/asheshgoplani/agent-deck/pull/995)). The 80s `waitForAgentReady` gate in `cmd/agent-deck/session_cmd.go` was hardcoded (`maxAttempts := 400` at a 200ms poll) and ignored the `--timeout` flag that the caller already passed through. As a result, `session send --timeout 5m` against a busy recipient silently failed at ~80s instead of waiting the requested 5 minutes — exactly the symptom in #957. The `--timeout` flag still bounded the *completion* phase (when paired with `--wait`), but never the *agent-ready* phase that preceded it, so a slow-to-become-ready agent was unreachable above 80s no matter what value the user passed. Fix: introduce an `agentReadyChecker` interface (`GetStatus` + `CapturePaneFresh`) mirroring the existing `statusChecker` pattern used by `waitForCompletion`; `*tmux.Session` satisfies it naturally, and tests can now exercise the timeout loop without a real tmux session. `waitForAgentReady` now accepts the caller's timeout; poll interval stays 200ms; `maxAttempts` is derived from the requested duration. Zero/negative preserves the historical 80s default for safety. The `--timeout` help text is updated to reflect that it now bounds both the agent-ready phase and (with `--wait`) the completion phase. Pinned by `TestSessionSend_RespectsTimeoutFlag_RegressionFor957` in `cmd/agent-deck/issue957_send_timeout_test.go`, which mocks an agent that never reaches "waiting" and asserts the function honors a 1s caller timeout (returns in ~1s, not the legacy 80s) and a 500ms timeout (returns in <2s); the test fails to compile on origin/main (new signature) and passes after the fix. Closes #957 (PR #995).

- **`agent-deck rm` is now parallel-safe at the CLI-subprocess layer and `session remove <bogus>` exits non-zero instead of silently lying** ([#961](https://github.com/asheshgoplani/agent-deck/issues/961), [PR #993](https://github.com/asheshgoplani/agent-deck/pull/993)). The structural fix for the parallel-`rm` load-modify-write race landed in v1.9.1 (#909) — but the regression coverage stopped at the storage layer (`internal/session/rm_lifecycle_test.go`), so a future refactor could silently re-open the race at the CLI surface without any failing test. The production pattern `xargs -P N agent-deck rm` exercises a fundamentally different shape (distinct OS processes, distinct `*sql.DB` pools racing for the same `state.db`) than the in-process storage test. #961 also reported a smaller-but-paired contract bug: `session remove <bogus>` silently printed a success line and exited 0 instead of returning a `NOT_FOUND` error. Fix in `cmd/agent-deck/issue961_rm_safety_test.go`: **(1)** `TestAgentDeckRm_ParallelSafe_RegressionFor961` seeds 14 sessions, spawns 14 concurrent `agent-deck rm <title> --json` *subprocesses* against a shared `HOME`/`state.db`, and asserts every CLI exits 0 *and* an independent `list --json` shows zero survivors — RED-proven against a reverted pre-#909 `handleRemove` (≈13/14 survivors despite every CLI reporting success), GREEN against current main. **(2)** `TestSessionRemove_NoOpExitsNonZero_RegressionFor961` pins the not-found contract: `session remove <bogus>` and `session remove <bogus> --force` must exit non-zero (code 2, `NOT_FOUND`); the no-op-success-exit-0 path is now blocked. The bugs reported in #961 themselves were already remediated on main; this PR closes the CLI-layer regression-coverage gap. Closes #961 (PR #993).

- **Transition-notifier deferred queue no longer replays events for sessions removed via `agent-deck rm`** ([#962](https://github.com/asheshgoplani/agent-deck/issues/962), [PR #992](https://github.com/asheshgoplani/agent-deck/pull/992)). The transition-notifier's deferred retry queue (`runtime/transition-deferred-queue.json`) kept replaying transition events for child sessions that had been removed via `agent-deck rm`. The rm path (issue [#910](https://github.com/asheshgoplani/agent-deck/issues/910), v1.9.x) sweeps the inbox JSONL and the dedup ledger but never touched the queue file, so on every daemon poll `DrainRetryQueueWithResolver` redispatched stale child events to the conductor for hours — observed all day on `conductor-innotrade` as a recurring spam class. Fix: add a consumer-side registry-presence filter inside `DrainRetryQueueWithResolver` (`internal/session/transition_notifier.go`). Queued entries whose `child_session_id` no longer exists in the profile registry are dropped (logged as `child_removed_from_registry` in `notifier-missed.log`) before the target-availability check fires the send. Defense-in-depth: even if the rm-sweep path misses a queue entry (race, parallel rm, stale file from upgrade), the dispatch loop refuses to fire events for vanished children. The check is wired via a nullable resolver field (`n.childPresence`) so existing struct-literal test helpers stay backwards compatible — only notifiers built via `NewTransitionNotifier` get the live filter. Fail-open on storage errors so a transient DB outage doesn't introduce a silent-loss path strictly worse than the bug being fixed. Pinned by `TestTransitionNotifier_SkipsRemovedSessions_RegressionFor962` in `internal/session/issue962_no_replay_test.go` (enqueue deferred event for a child → `DeleteInstance` the child → drain; pre-fix the sender is invoked once, post-fix zero sends and queue cleared). Closes #962 (PR #992).

- **`agent-deck launch <path>` from inside a conductor derives the group from the cwd, not the parent session** ([#972](https://github.com/asheshgoplani/agent-deck/issues/972), [PR #994](https://github.com/asheshgoplani/agent-deck/pull/994)). Bug: `agent-deck launch <path>` invoked from inside a conductor inherited the parent's `conductor` group instead of landing in the project group derived from the cwd. Every conductor-spawned child required a follow-up `agent-deck group move` to land in the right group — directly contradicting the `feedback_agent_deck_conductor_uses_agent_deck_group.md` memory rule that each conductor's children must land in its project group, never in `conductor`. Fix in `cmd/agent-deck/launch_cmd.go`: extend `resolveGroupSelection` with a `cwdDerived` parameter and a fixed priority order — explicit `-g/--group` > cwd-derived project group > parent-session group. The cwd-derived value is computed from the resolved project path via the new exported `session.GroupPathForProject` wrapper around the existing `extractGroupPath` heuristic, so `launch` and `NewInstance` share a single source of truth. `handleAdd` keeps its existing semantics (passes `""` for the cwd-derived slot) because its path resolution happens after group resolution. Pinned by `TestLaunch_DerivesGroupFromCwdNotParent_RegressionFor972` in `cmd/agent-deck/issue972_launch_group_test.go` (4 sub-cases: cwd-derived group wins over parent; explicit `-g` still wins over both; parent is fallback when no cwd-derived group; empty-empty returns empty for the caller's default). Closes #972 (PR #994).

### Changed

- **17+ CLI verb/flag alias forms across `session`, `group`, and `launch` now resolve to the canonical handlers** ([#974](https://github.com/asheshgoplani/agent-deck/issues/974), [PR #996](https://github.com/asheshgoplani/agent-deck/pull/996)). Three CLI ergonomic rejections reported in #974 that broke muscle memory and made shell scripts brittle: **(1)** `session update <id> --no-parent` — verb `update` was not in the dispatch switch in `cmd/agent-deck/session_cmd.go`, so the entire subcommand returned `unknown verb`. Fix: add `update` dispatch plus `resolveSessionUpdateAlias` that maps `--no-parent` to `unset-parent <id>` and `--parent <pid>` (or `--parent=<pid>`) to `set-parent <id> <pid>`; bare `update` falls through to the existing `set` handler. **(2)** `group remove <name>` — only `delete` and `rm` were aliased in `group_cmd.go`. Fix: extract `groupVerbCanonical` and add `remove` alongside the existing aliases. **(3)** `launch <path> ... -parent <pid>` — the value was silently swallowed because `reorderArgsForFlagParsing` in `cli_utils.go` matched flag tokens by literal string (`-p`, `--parent`) and never matched `-parent`. Go's `flag` package itself treats `-foo` and `--foo` as the same flag, so the reorder pass was the only thing rejecting it. Fix: look flags up by name (dashes stripped) so every long form works with either single or double dash. Side effect: the previously missing `--tmux-socket` entry is now covered too. Pinned by `TestCLI_VerbAliases_Accepted_RegressionFor974` in `cmd/agent-deck/issue974_verb_aliases_test.go` (7 sub-cases covering all three forms plus sanity rows for the existing canonical spellings). Closes #974 (PR #996).

### Known issues

- `HOMEBREW_TAP_GITHUB_TOKEN` repo secret is not yet set, so the brew tap formula update step in `release.yml` will fail for v1.9.8 (known, user action — does not block the release tarballs or the GitHub release itself; same as v1.9.6 and v1.9.7).

## [1.9.7] - 2026-05-15

Patch release on top of v1.9.6 — two community-impacting UX features plus two infrastructure repairs that close long-standing CI flakes. v1.9.7 is the **second release cut under the Option A pipeline** ([#981](https://github.com/asheshgoplani/agent-deck/pull/981) in v1.9.6); the local release worker stops at `git push origin <tag>` and `.github/workflows/release.yml` is the single source of truth for `goreleaser release --clean`. Headline fixes: the `Weekly Regression Check` cron — broken since v1.9.0 and tracked as a "Known issue" in every release between — is finally repaired by switching to the TUI-less `agent-deck-test-server` binary so the cron no longer needs an interactive PTY ([#986](https://github.com/asheshgoplani/agent-deck/pull/986), closes [#943](https://github.com/asheshgoplani/agent-deck/issues/943)); and the `TotalLastWeek` Monday-UTC test flake — v1.9.6's #977 fixed the production bug, but the *older* `TestStore_TotalLastWeek_OnlyLastWeekEvent` test was still wall-clock-bound and would still fail on a Monday-UTC tick in CI — is now deterministic via the `SetClock` injection seam shipped in #977 ([#990](https://github.com/asheshgoplani/agent-deck/pull/990)). Headline features: session list within each group is now sorted by "most actionable" (waiting > attached > running > idle > stopped, ties broken by recency) so the top of each group is always where attention should go ([#987](https://github.com/asheshgoplani/agent-deck/pull/987), closes [#857](https://github.com/asheshgoplani/agent-deck/issues/857)); and the New Group dialog now pre-fills the path field with the parent of the most-recently-created session in the current view, eliminating the most common Tab-and-paste step ([#988](https://github.com/asheshgoplani/agent-deck/pull/988), closes [#918](https://github.com/asheshgoplani/agent-deck/issues/918)). (Note: REQ-7 [#989] and Node 24 actions [#991] remain UNSTABLE pending CI green and are deferred to v1.9.8.)

### Fixed

- **`Weekly Regression Check` cron job no longer fails on every run — switched to TUI-less `agent-deck-test-server` binary** ([#943](https://github.com/asheshgoplani/agent-deck/issues/943), [PR #986](https://github.com/asheshgoplani/agent-deck/pull/986)). The Weekly Regression Check workflow has been failing since v1.9.0 (tracked as a "Known issue" in every v1.9.x release) because `.github/workflows/weekly-regression.yml` invoked the full TUI-bound `agent-deck` binary inside a non-PTY GitHub Actions runner. Bubble Tea's `tea.NewProgram` requires a TTY; without one the program initializer returned `ErrInputTTYNotFound` and the workflow exited 1 before any of the regression scenarios ran. The TUI-less harness binary `cmd/agent-deck-test-server` already existed (added in v1.8.x for browser-harness E2E coverage) — it exposes the same session/group/state APIs over a localhost HTTP control plane without ever instantiating a Bubble Tea program — but the cron workflow was never updated to use it. Fix: `.github/workflows/weekly-regression.yml` now `go install`s `./cmd/agent-deck-test-server`, exposes `AGENT_DECK_TEST_SERVER_PORT`, and points the regression scenarios at the HTTP control plane; the TUI binary is no longer invoked in cron paths. Verified by manually triggering the workflow against this branch and observing all 12 regression scenarios pass green for the first time since v1.9.0. Closes #943 — the entry can be removed from the v1.9.7 "Known issues" list (it was carried in v1.9.5 and v1.9.6 explicitly) (PR #986, closes #943).

- **`TestStore_TotalLastWeek_OnlyLastWeekEvent` is now deterministic via `SetClock` — Monday-UTC flake closed permanently** ([PR #990](https://github.com/asheshgoplani/agent-deck/pull/990)). v1.9.6's #977 fixed the *production* `TotalLastWeek` Monday-UTC bug by hoisting boundary computation into Go via the new `Store.SetClock(func() time.Time)` injection seam — but the *older* `TestStore_TotalLastWeek_OnlyLastWeekEvent` (which long predates #932 and tested a different invariant: that only events within the last-week window are counted) still constructed event timestamps relative to the wall clock and computed its expected window the same way. On a Monday-UTC CI tick the old SQL bug was gone but the test still flipped from green to red because both sides of the assertion shifted independently. Fix: thread the `SetClock` seam through `internal/costs/store_test.go`'s `TestStore_TotalLastWeek_OnlyLastWeekEvent` so it pins the clock to a deterministic mid-week instant; event-row construction and expected-window computation now both consume the pinned clock. The new `TestStore_TotalLastWeek_HandlesMondayBoundary` from #977 already pinned the Monday boundary explicitly; this PR extends that discipline to the older test. The "Known issues" entry from v1.9.5 (`internal/costs::TestStore_TotalLastWeek_OnlyLastWeekEvent` Monday-UTC flake) — already removed in v1.9.6 because v1.9.6 was cut on a non-Monday — is now closed structurally, not just calendrically (PR #990).

### Added

- **Sessions within each group are now sorted by "most actionable" instead of insertion order** ([#857](https://github.com/asheshgoplani/agent-deck/issues/857), [PR #987](https://github.com/asheshgoplani/agent-deck/pull/987)). Within a group, session order had been creation-order — so the session most needing attention (waiting on user input, e.g. an `AskUserQuestion` prompt or an `EnterPlanMode` checkpoint) could end up at the bottom of a long list, requiring vertical scrolling to find. #857 requested a stable status-driven ordering. Fix: new `sortByActionable` in `internal/session/groups.go` sorts each group's sessions by status priority — `waiting` (highest, attention-needed) > `attached` > `running` > `idle` > `stopped` — with ties broken by most-recent activity (`updated_at` DESC). The home view (`internal/ui/home.go`) calls the sort after every group rebuild, so newly-`waiting` sessions float to the top of their group automatically. Pinned by `internal/ui/issue857_sort_actionable_test.go` (5 sub-cases covering each status pair plus the recency tiebreaker). The grouping dimension itself (which group a session belongs to) is unchanged — only intra-group order is affected. Closes #857 (PR #987).

- **New Group dialog pre-fills the path field with the most-recently-used parent directory** ([#918](https://github.com/asheshgoplani/agent-deck/issues/918), [PR #988](https://github.com/asheshgoplani/agent-deck/pull/988)). Creating a new group always opened the path field empty, forcing the user to either type the path or Tab over to a terminal, `pwd`, copy, paste back. For users whose groups all live under the same parent (e.g. `~/Developer/`, `~/innotrade/`), this was a recurring papercut. Fix: `internal/ui/group_dialog.go` now pre-fills the path field with the parent directory of the most-recently-created session visible in the current view — falling back to `$HOME` if no sessions exist yet. The user can still Tab into the field and edit / clear, so the default never gets in the way. Pinned by `internal/ui/issue918_default_path_test.go` (3 sub-cases: empty-state fallback to `$HOME`, single-session parent extraction, multi-session most-recent-wins). Closes #918 (PR #988).

### Known issues

- `HOMEBREW_TAP_GITHUB_TOKEN` repo secret is not yet set, so the brew tap formula update step in `release.yml` will fail for v1.9.7 (known, user action — does not block the release tarballs or the GitHub release itself; same as v1.9.6).

## [1.9.6] - 2026-05-15

Patch release on top of v1.9.5 — 7 community-credited bug fixes plus a structural change to the release pipeline (Option A: CI-only publish). v1.9.6 is the **first release cut under the new pipeline** (#981, closes #980): the local release worker now stops at `git push origin <tag>` and `.github/workflows/release.yml` is the single source of truth for `goreleaser release --clean`. This eliminates the double-publish race that fired on every release from v1.9.0–v1.9.5 (workflow runs returned `422 Validation Failed: already_exists` × 5 because the local worker and the tag-push workflow both raced to upload the same five assets). Headline fixes: TotalLastWeek Monday-UTC SQL flake (#977, closes #932 — production bug, not a test bug); session-share `export.sh` silent exit 5 on real Claude Code sessions (#978, closes #895); worktree merge-back support in the bare-repo parent layout (#979, closes #891); brew-upgrade silent-success lie in `agent-deck update` (#982, closes #954); two residual sub-bugs from @paskal's #896 popup-keyboard report (#983); spawn-path unification with one-INFO-per-override audit seam plus sidecar cleanup on session-id clear (#984, closes #922 + #923); and the duplicate `bun telegram` poller race when `enabledPlugins.telegram=true` collides with conductor `--channels plugin:telegram@...` (#985, closes #941). Credits: @Victor Salvador Gasparini (#895), @Clindbergh (#891), @alexandergharibian (#954), @paskal (#896 residual), @bautrey (#922 + #923).

### Fixed

- **`TotalLastWeek` returns the correct week window even when the host clock ticks Monday 00:00 UTC** ([#932](https://github.com/asheshgoplani/agent-deck/issues/932), [PR #977](https://github.com/asheshgoplani/agent-deck/pull/977)). SQLite's `date('now','weekday 1')` is a no-op when "now" is already Monday — it returns today rather than advancing to next Monday — so on a Monday-UTC tick `internal/costs/store.go`'s `TotalLastWeek` query computed the window as `[two-Mondays-ago, last-Monday)` instead of the intended `[last-Monday, this-Monday)`. This is a real production bug (the costs panel showed the week-before-last's totals every Monday morning UTC, off by one week), not just a test flake — but the surface symptom users saw first was `TestStore_TotalLastWeek_OnlyLastWeekEvent` failing whenever the CI runner happened to wake across the Monday 00:00 UTC tick, which is what the v1.9.5 "Known issues" entry tracked. Fix: compute both boundaries in Go using an injectable `Store.SetClock(func() time.Time)` and pass them as bound parameters to the SQL query, so the SQL no longer touches `date('now',...)` for boundary math. New `TestStore_TotalLastWeek_HandlesMondayBoundary` pins the clock to a Monday UTC instant and asserts the window is `[last Mon, this Mon)`; the test fails deterministically against the pre-#977 SQL and passes with the fix (PR #977, closes #932).

- **`session-share` `export.sh` no longer silently exits 5 on real Claude Code sessions** ([#895](https://github.com/asheshgoplani/agent-deck/issues/895), [PR #978](https://github.com/asheshgoplani/agent-deck/pull/978)). Reporter @Victor Salvador Gasparini hit silent exit 5 on every real Claude Code session export. Two stacked bugs collapsed onto the same exit code. **(1)** `skills/session-share/scripts/utils.sh`'s `encode_path` helper only converted `/` to `-`, but Claude Code's project-path encoding (under `~/.claude/projects/<encoded>/`) also converts `.` to `-`. Any username or directory containing a `.` (common on macOS where `first.last` accounts are the norm) caused `find_session_file` to return empty and the script bailed silently at line 81. **(2)** `export.sh:130`'s `jq` filter assumed `.message.content` was always an array. Real Claude Code JSONL contains records where `.message.content` is a plain string — slash-command output, local-command caveats, etc. — and `jq` errored with exit 5 on the type mismatch; `set -o pipefail` propagated the nonzero status, the assignment's failure tripped `set -e`, and the `EXIT` trap's `rm -f "$TEMP_FILE"` then ate the diagnostic, leaving only the bare exit 5 visible to the user. Fix: `encode_path` becomes `sed 's|[/.]|-|g'` (single pass, both characters); the `jq` filters in both `export.sh` and `sanitize_jsonl` in `utils.sh` type-check `.message.content` and only iterate when it's an array, falling through to a string-passthrough branch otherwise. New regression test `skills/session-share/tests/test_export_895_regression.sh` reproduces the original exit 5 with a string-content user record and also pins the path-encoding behaviour; both cases fail at HEAD pre-fix and pass post-fix. Credit @Victor Salvador Gasparini for the repro and the JSONL sample (PR #978, closes #895).

- **`finishWorktree` merge-back now works in the bare-repo parent layout instead of exiting 128** ([#891](https://github.com/asheshgoplani/agent-deck/issues/891), [PR #979](https://github.com/asheshgoplani/agent-deck/pull/979)). In the bare-repo layout introduced by #715 (`<project>/.bare/` + linked worktrees), `GetWorktreeBaseRoot` returns the project root — which is the parent of `.bare/`, not itself a git working tree. The pre-#979 `finishWorktree` in `internal/ui/home.go` shelled out to `git -C <projectRoot> checkout <target>` and `git merge`, both of which exit 128 with `fatal: not a git repository` (or `must be run in a work tree` on the bare directory directly). The user saw "merge-back failed" with no actionable diagnostic, and the worktree was left orphaned — exactly what @Clindbergh reported in #891 against the #715 bare layout. Fix: new `git.MergeBack(projectRoot, source, target)` helper in `internal/git/mergeback.go` detects the layout and branches: regular layout takes the existing `checkout`+`merge` path; bare layout's fast-forward case uses `git update-ref` directly on the bare dir; the non-FF case spins up a throwaway worktree of `target`, performs the merge there, then prunes the temporary worktree. `finishWorktree` in `internal/ui/home.go` now delegates to `git.MergeBack` and the unused `os/exec` import is dropped. Pinned by `TestWorktree_MergeBack_BareRepo_RegressionFor891` in `internal/git/mergeback_bare_repo_test.go`, which uses the existing hermetic `createBareRepoLayout` fixture and asserts `main` advances to the feature SHA after merge-back. Credit @Clindbergh for the #891 + #715 reproduction (PR #979, closes #891).

- **`agent-deck update` now detects brew refusing to upgrade and fails loudly instead of lying** ([#954](https://github.com/asheshgoplani/agent-deck/issues/954), [PR #982](https://github.com/asheshgoplani/agent-deck/pull/982)). Reporter @alexandergharibian filed #954 with this exact sequence: `agent-deck update` printed `Already up-to-date. Warning: agent-deck 1.8.3 already installed` immediately followed by `✓ Updated to v1.9.4` — the second line was a lie. Root cause: `brew upgrade` exits 0 even when it refuses to upgrade (e.g. when the tap formula is stale, when the cask metadata cache is out of date, or when the installed version literal is already at the requested target despite a higher latest existing). The pre-#982 `handleUpdate` in `cmd/agent-deck/update_cmd.go` only checked brew's exit status, saw 0, and printed the success line unconditionally. Fix: capture brew's combined stdout+stderr through a small `brewRunner` interface, scan the output for the `already installed` refusal marker (and the cask-side equivalent), and surface a clear actionable error pointing at tap-staleness as the root cause. The `brewRunner` indirection also makes the path mockable — new `TestUpdate_DetectsNoVersionBump_FailsLoudly_RegressionFor954` replays @alexandergharibian's exact output verbatim and asserts the CLI errors instead of printing the success line; a companion `TestUpdate_AcceptsRealUpgrade_NoFalseFailure` guards the happy path against false positives from over-aggressive marker matching. Credit @alexandergharibian for the report with full repro output (PR #982, closes #954).

- **New-session dialog popup: arrow keys auto-activate the suggestion list and Enter selects the highlighted item** ([#896](https://github.com/asheshgoplani/agent-deck/issues/896), [PR #983](https://github.com/asheshgoplani/agent-deck/pull/983)). @paskal's original #896 enumerated four sub-bugs in the path-suggestions popup. PR #908 (in v1.9.2) closed sub-bugs 1 (Tab on an invalid path) and 4 (Ctrl+W on a path). #983 closes the remaining two: **sub-bug 3** — when the popup was visible after typing a prefix, Enter submitted the form with the literally-typed value instead of the highlighted suggestion, because `home.go`'s Enter handler only intercepted when `IsSuggestionsActive()` was true and plain arrow keys on a freshly-rendered popup left `suggestionsActive=false`. **Sub-bug 2** — Up/Down on a visible popup advanced dialog focus between fields instead of navigating the popup, forcing the user to fall back to `Ctrl+N` + `Space` to "wake" the popup before they could arrow through it. Shared root cause: the popup had two distinct states — *visible* (just rendered) and *active* (consuming nav keys) — gated by separate flags, and only `Space` / `Ctrl+N` ever flipped the active flag. From the user's perspective the popup looked interactive when it wasn't. Fix in `internal/ui/newdialog.go`: when the popup is visible on the path field and the user presses the first Down or Up arrow, auto-activate it. The existing `suggestionsActive` arrow handler then advances the cursor, and `home.go`'s existing Enter handler picks the highlighted suggestion correctly. Pinned by `TestNewDialog_PopupEnter_SelectsHighlightedSuggestion_RegressionFor896` and `TestNewDialog_PopupArrows_NavigateReliably_RegressionFor896` in `internal/ui/issue896_residual_test.go`. Credit @paskal for the original four-bug enumeration that drove both PRs (PR #983, closes #896 residual).

- **Spawn paths emit one INFO event per worker-scratch `CLAUDE_CONFIG_DIR` override, and clearing a session's Claude session id deletes the hook sidecar** ([#922](https://github.com/asheshgoplani/agent-deck/issues/922), [#923](https://github.com/asheshgoplani/agent-deck/issues/923), [PR #984](https://github.com/asheshgoplani/agent-deck/pull/984)). **#922 (spawn unification audit seam):** three separate spawn-env builders in `internal/session/instance.go` — `buildClaudeCommandWithMessage`, `buildBashExportPrefix`, `buildClaudeResumeCommand` — each silently swapped the resolved `CLAUDE_CONFIG_DIR` for `WorkerScratchConfigDir` when the latter was non-empty. Users whose per-group `[groups.X.claude].config_dir` resolution was overridden by the worker-scratch mechanism had nothing to grep for: the swap was invisible in logs, and the audit reporter could not tell from log output whether a misroute had occurred (a real concern after v1.9.4's #950 hoisted `prepareWorkerScratchConfigDirForSpawn` to the top of `Restart()`). The prep-call unification half of @bautrey's original investigation was already addressed by #950 — the wedge that remained on main was the silent swap. Fix: every swap now routes through a single seam (`applyWorkerScratchOverride` in `internal/session/worker_scratch.go`) that emits one INFO event per swap, carrying `instance_id`, the resolved (overridden) `config_dir`, and the worker-scratch dir; the three call sites share one log shape, so misroutes are debuggable instead of silent. Pinned by `TestBuildClaudeCommand_WorkerScratchOverrideEmitsInfoLog`, `TestBuildClaudeResume_WorkerScratchOverrideEmitsInfoLog`, and `TestBuildClaudeCommand_NoOverrideNoLog` in `internal/session/issue922_spawn_unify_test.go`. **#923 (sidecar lifecycle):** clearing a session's Claude session id (via `session set claude-session-id ""` or by editing the field empty in the Edit Session dialog) left the hook sidecar file on disk; subsequent operations could find the stale sidecar and treat the session as still hook-bound. Fix in `internal/session/mutators.go`: clearing the field now also unlinks the sidecar path; setting it to a non-empty value preserves the sidecar. Pinned by `TestSetField_ClearClaudeSessionID_DeletesHookSidecar` and `TestSetField_NonEmptyClaudeSessionID_KeepsSidecar` in `internal/session/issue923_sid_clear_test.go`. Credit @bautrey for both reports and the original spawn-unification investigation (PR #984, closes #922 + #923).

- **Conductor sessions with `--channels plugin:telegram@...` no longer spawn duplicate `bun telegram` pollers when `enabledPlugins.telegram=true` is set globally** ([#941](https://github.com/asheshgoplani/agent-deck/issues/941), [PR #985](https://github.com/asheshgoplani/agent-deck/pull/985)). When a conductor session was launched with `--channels plugin:telegram@claude-plugins-official` **and** the ambient profile's `settings.json` already had `enabledPlugins."telegram@claude-plugins-official"=true`, the Claude CLI loaded the plugin twice — once from the global `enabledPlugins` flag and once from the `--channels` flag. Each load spawned its own `bun telegram start` poller against the conductor's `TELEGRAM_STATE_DIR`, and the two pollers raced for the same bot token. Telegram's Bot API returns `409 Conflict` on competing `getUpdates` calls and starts dropping incoming messages — exactly the symptom reported in #941 (intermittent message loss on the agent-deck and innotrade conductors). The `TelegramValidator` already surfaced this as `GLOBAL_ANTIPATTERN+DOUBLE_LOAD` warnings, and v1.7.22 plus the v3 topology memory rule (`telegram_channel_conductor_only.md`) documented the contract — *conductors use `--channels` explicitly; `enabledPlugins.telegram` must be false globally* — but the contract was enforced only by docs, so operators who left the global flag on silently got two pollers. #985 lifts the rule into the spawn path: new predicate `needsScratchForGlobalChannelConflict` in `internal/session/worker_scratch.go` fires when a Claude session has a `plugin:telegram@...` channel **and** the resolved source profile's `settings.json` has the global flag set. `NeedsWorkerScratchConfigDir` now ORs the new predicate, so channel-owning conductors with the antipattern automatically get a per-session worker-scratch config dir with `enabledPlugins.telegram=false` — the global load is suppressed inside that scratch dir while the `--channels` load still runs, so exactly one `bun telegram` poller runs per conductor. Pinned by `TestTelegram_GlobalScope_OneBunPollerOnly_RegressionFor941` in `internal/session/issue941_global_antipattern_test.go` (PR #985, closes #941).

### Changed

- **Release pipeline restructured: CI is now the single source of truth for publishing (Option A)** ([#980](https://github.com/asheshgoplani/agent-deck/issues/980), [PR #981](https://github.com/asheshgoplani/agent-deck/pull/981)). Every release from v1.9.0 through v1.9.5 generated five `422 Validation Failed: already_exists` errors in the CI workflow logs (e.g. workflow run 25884533972 for v1.9.5) because the local release worker ran `goreleaser release --clean` **and** the tag push triggered `.github/workflows/release.yml` to do the same thing — both raced to upload the same five assets (4 platform tarballs + `checksums.txt`) to the same GitHub release. Whichever lost the race got back 422 on every asset, exited 1, and the release-watcher fired on every release. The fix splits responsibility cleanly: the local release worker now stops at `git push origin <tag>` and never invokes `goreleaser release` (see the "Release worker template" section of `FLEET-PIPELINE-PLAN.md`), and `.github/workflows/release.yml` gets a `concurrency.group: release-${{ github.ref }}` block as belt-and-suspenders against accidental re-runs for the same tag. To catch `.goreleaser.yml` drift at PR review time instead of at tag-push time (when recovery requires deleting a partial GitHub release), this PR also adds `.github/workflows/release-snapshot.yml` — a path-filtered workflow that runs `goreleaser release --snapshot --skip=publish` on every PR touching `.goreleaser.yml`, `cmd/agent-deck/**`, or the release workflows themselves. Verified locally: snapshot on clean main exits 0 with 4 platform tarballs + `checksums.txt`; snapshot with a deliberately broken `main.go` path exits 1 with the goreleaser build error surfaced inline. **v1.9.6 is the first release cut under this pipeline.** (PR #981, closes #980).

### Known issues

- `Weekly Regression Check` cron job still failing (issue [#943](https://github.com/asheshgoplani/agent-deck/issues/943), unchanged from prior releases) — does not block the release pipeline; a separate worker is fixing.
- `HOMEBREW_TAP_GITHUB_TOKEN` repo secret is not yet set, so the brew tap formula update step in `release.yml` will fail for v1.9.6 (known, user action — does not block the release tarballs or the GitHub release itself).

## [1.9.5] - 2026-05-14

Patch release on top of v1.9.4 — two community PRs plus an inline fix closing a silent drift bug. Headline is the real fix for keycap-class emoji rendering (#952, closes #937 v2): the v1.9.3 fix in #948 corrected `<base>+U+FE0F` pairs via uniseg but missed the keycap class `<base>+U+FE0F+U+20E3` (e.g. `#️⃣ 0️⃣–9️⃣ *️⃣`), which uniseg classifies as 1 cell while every modern terminal paints at 2 — re-introducing per-frame row-offset drift @jennings reported against v1.9.3 (`68dba73d`). Second fix prevents an empty Claude command from no-op'ing a session restart and leaving the pane dead (#855). Third fix bumps the in-source `Version` constant in `cmd/agent-deck/main.go` from `1.8.3` to `1.9.5`, closing a 5-release silent drift that the `validate-tag` CI check kept false-alarming on (investigated by worker #947 against v1.9.4). Thanks to @maxfi and @jennings for the dual repros on #952.

### Fixed

- **Keycap emoji sequences (`#️⃣ 0️⃣–9️⃣ *️⃣`) no longer cause per-frame row-offset drift in the TUI — reopen of #937 against v1.9.3** ([#937](https://github.com/asheshgoplani/agent-deck/issues/937) v2, [PR #952](https://github.com/asheshgoplani/agent-deck/pull/952)). PR #948 (v1.9.3) closed #937 by routing `internal/ui/home.go`'s width and truncation gates through `github.com/charmbracelet/x/ansi` (uniseg-backed) on the theory that uniseg correctly classifies `<codepoint>+U+FE0F` as 2 cells; that holds for @maxfi's four reported emoji (`🏷️ 🛠️ ⚙️ 🗂️`) but **does not** hold for keycap sequences such as `#️⃣` (U+0023 + U+FE0F + U+20E3) — uniseg reports 1 cell while every terminal we tested (Ghostty, Terminal.app, iTerm2, Warp, Termius) renders 2. @jennings reported continued drift against v1.9.3 (commit `68dba73d`) with exactly that emoji class plus `🔁`, appearing in pane content, not just session titles. Fix: new `cellWidth` / `cellTruncate` helpers in `internal/ui/cellwidth.go` walk extended grapheme clusters via `rivo/uniseg` and promote any cluster containing U+20E3 (COMBINING ENCLOSING KEYCAP) to width 2; the existing 9 callsites #948 swapped plus 6 more pane-content / final-viewport-clamp callsites it missed (`clampViewToViewport`, `renderSessionItem` pane-title append, `renderPreviewPane` per-line + post-build width enforcement, notes-editor line truncation) now route through these helpers so the cell-count gates and the terminal agree on keycap glyphs. Pinned by `internal/ui/issue937_keycap_test.go` (5 sub-cases across width parity, truncation budget, and `truncatePath` integration). Residual: `ensureExactWidth` / `lipgloss.JoinHorizontal` measure with `lipgloss.Width`, which shares the upstream uniseg disagreement — `clampViewToViewport` now acts as the final cell-correct backstop, so a worst-case keycap-at-right-edge clips visually rather than wrapping; full structural fix requires an upstream change in `github.com/charmbracelet/x/ansi` and/or `github.com/rivo/uniseg` and is being filed separately. Credit @maxfi and @jennings for the dual repros (PR #952, closes #937).

- **Empty Claude command no longer no-ops a session restart, leaving the pane dead** ([PR #855](https://github.com/asheshgoplani/agent-deck/pull/855)). When a session's configured Claude command was empty (string-empty after trim, e.g. from a partially-edited Edit Session dialog or a legacy `state.db` row written before the launcher gate landed), `buildClaudeCommand()` in `internal/session/instance.go` returned an empty argv slice; the restart path then forwarded that empty argv to `tmux respawn-pane`, which interprets no-arg respawn as "use the shell" — so the user's pane was rebound to a bare interactive shell with no Claude process, no MCP servers, no channel wiring. From the TUI it looked like a successful restart (status flipped to `attached`) but the pane was effectively dead: no agent, no `/q` handler, no hook fast-path, just a shell prompt. Fix: `buildClaudeCommand()` now treats an empty base command as "still emit the Claude binary" and falls through to default-arg construction, so the restart respawns Claude with the same default invocation a fresh session would get rather than degrading to a shell. Pinned by `internal/session/empty_command_claude_restart_test.go` (`TestBuildClaudeCommand_EmptyBaseCommand_StillEmitsClaudeBinary`) plus `shell_restart_test.go` and `sessions_disappear_on_restart_test.go` coverage in `internal/session` / `internal/ui` (PR #855).

- **In-source `Version` constant in `cmd/agent-deck/main.go` no longer drifts from the released tag** (no external issue — worker #947 investigation). `cmd/agent-deck/main.go:39` declares `var Version = "1.8.3"` with the comment `// overridden at build time via -ldflags "-X main.Version=..."`; goreleaser sets the ldflag on every release build, so released binaries always reported the correct version and the drift was invisible in production. The literal had been stuck at `1.8.3` since the v1.9.0 cut — five consecutive releases (v1.9.0, v1.9.1, v1.9.2, v1.9.3, v1.9.4) shipped with a stale literal that only surfaced when something read it without the ldflag, e.g. `go build ./cmd/agent-deck` from a developer checkout, `go run` invocations, or the `validate-tag` CI check on a non-goreleaser build path — the latter false-alarmed on every release tag from v1.9.0 onward and worker #947 traced it to the literal in main.go rather than the tag mechanics. Fix: bump the literal to `1.9.5` in lockstep with the tag; the ldflag override remains so goreleaser builds are unaffected. Validated by `go build -o /tmp/agent-deck-no-ldflag ./cmd/agent-deck && /tmp/agent-deck-no-ldflag --version` reporting `Agent Deck v1.9.5` (would have reported `v1.8.3` pre-bump). This closes the `validate-tag` false-alarm class permanently — future releases that forget the literal bump will be caught by the same check.

### Known issues

- `internal/costs::TestStore_TotalLastWeek_OnlyLastWeekEvent` still fails when the local clock is on a Monday in UTC (issue [#932](https://github.com/asheshgoplani/agent-deck/issues/932), unchanged from v1.9.0–v1.9.4). v1.9.5 was cut on a Thursday — full suite green.
- `Weekly Regression Check` cron job still failing (issue [#943](https://github.com/asheshgoplani/agent-deck/issues/943), unchanged from prior releases) — does not block the release pipeline.

## [1.9.4] - 2026-05-14

Emergency P0 hotfix on top of v1.9.3 — single PR (#950) restoring macOS OAuth onboarding for users on the default Claude profile. v1.9.2's #779 (per-session Claude Code plugin enablement) inadvertently broke a long-standing invariant: worker-scratch `CLAUDE_CONFIG_DIR` injection was firing for every session, not just those with an explicit `config_dir`. On macOS this caused the Claude CLI to look up OAuth credentials in a non-keychain path and fail onboarding entirely. @paskal bisected the regression to #779 and shipped the fix within 2 hours of report — thank you.

### Fixed

- **macOS OAuth onboarding no longer breaks for sessions without an explicit `config_dir`** ([#949](https://github.com/asheshgoplani/agent-deck/issues/949), [PR #950](https://github.com/asheshgoplani/agent-deck/pull/950)). v1.9.2's #779 expanded `internal/session/instance.go`'s env-construction path so that the worker-scratch `CLAUDE_CONFIG_DIR` override was set unconditionally whenever the session had a worker-scratch directory — which is every managed session. Pre-#779, the override fired only when the user had explicitly configured a per-session `config_dir` (e.g. for multi-profile setups). On Linux this was mostly harmless; on macOS it diverted the Claude CLI away from the default keychain-backed OAuth credential store, so first-run onboarding silently failed with a generic "auth required" loop and existing OAuth tokens stopped being found. Fix: re-gate the worker-scratch `CLAUDE_CONFIG_DIR` injection on a non-empty `config_dir` field, restoring the v1.7.68/v1.9.1 invariant. Pinned by `internal/session/issue949_scratch_injection_gate_test.go` (PR #950, closes #949). Credit to @paskal for bisect-and-fix within 2 hours of the original report.

### Known issues

- `internal/costs::TestStore_TotalLastWeek_OnlyLastWeekEvent` still fails when the local clock is on a Monday in UTC (issue [#932](https://github.com/asheshgoplani/agent-deck/issues/932), unchanged from v1.9.0–v1.9.3). v1.9.4 was cut on a Thursday — full suite green.

## [1.9.3] - 2026-05-13

Hotfix release on top of v1.9.2 — single PR (#948) addressing two TUI rendering regressions reported on the day v1.9.2 shipped. Both issues were visible on first attach: viewport content from the previously-attached session bled into newly-attached panes until a manual resize, and emoji glyphs followed by a Variation Selector-16 (U+FE0F) were drawn at single-cell width, causing overlapping text in the session list and status bar. Thanks to @Kevsosmooth, @maxfi, and @jennings for repro details and pane captures.

### Fixed

- **Viewport content no longer bleeds across session-switch / resize until manual repaint** ([#936](https://github.com/asheshgoplani/agent-deck/issues/936), [PR #948](https://github.com/asheshgoplani/agent-deck/pull/948)). After v1.9.2's `home.go` refactor, attaching session B while session A's pane was still in the viewport left A's last-rendered content visible in B's frame until the user manually resized the terminal — the viewport's internal content cache wasn't being invalidated on the attach transition or on terminal resize, so Bubble Tea's diff renderer computed an empty diff against the stale cache and drew nothing for B. Fix: explicit `viewport.SetContent("")` + cache-invalidation flag on every attach-state transition (`attaching` → `attached` and `attached` → `detached`) and on every `tea.WindowSizeMsg`, before the next paint cycle. Coverage in `internal/ui/issue936_attach_resize_test.go` (PR #948, closes #936).

- **Emoji + Variation Selector-16 (U+FE0F) sequences now render at correct two-cell width instead of overlapping** ([#937](https://github.com/asheshgoplani/agent-deck/issues/937), [PR #948](https://github.com/asheshgoplani/agent-deck/pull/948)). Sessions whose names contained emoji presentation sequences (e.g. `⚙️` = U+2699 + U+FE0F, `❤️` = U+2764 + U+FE0F) were measured at one cell because the cell-width function summed `runewidth.RuneWidth` per rune — VS16 reports width 0, and the base symbol is a width-1 "text-default" codepoint, so the combined glyph rendered as 1 cell while the terminal actually drew it at 2 cells, shifting every column to the right of it by one and producing overlap in the session list, status bar, and dialog labels. Fix: cell-width walker now detects the `<base, U+FE0F>` pair and returns 2 for the sequence, matching Unicode UAX #11 emoji presentation semantics and matching what every modern terminal (iTerm2, kitty, WezTerm, GNOME Terminal, Windows Terminal) actually paints. Coverage in `internal/ui/issue937_emoji_vs16_test.go` (PR #948, closes #937).

### Known issues

- `internal/costs::TestStore_TotalLastWeek_OnlyLastWeekEvent` still fails when the local clock is on a Monday in UTC (issue [#932](https://github.com/asheshgoplani/agent-deck/issues/932), unchanged from v1.9.0/v1.9.1/v1.9.2). v1.9.3 was cut on a Wednesday — full suite green.

## [1.9.2] - 2026-05-13

Patch release on top of v1.9.1 — 17 community PRs merged over the two days since v1.9.1. Headline fix is the **two-TUI-against-one-profile** crash (#944, closes #927): with `allow_multiple=true` (the default), running agent-deck simultaneously on PC + phone-over-SSH caused every managed session to oscillate to StatusError within ~20s because each TUI's reconnect sweep killed the other's control pipes. Other bundles: conductor bridge.py robustness for non-UTF-8 output and `--wait`-vs-`session output --json` reply parsing (#926, closes #920/#921); cross-profile data migration CLI (`session move` / `conductor move` / `group move`, #929, closes #928); first-class per-session Claude Code plugin enablement mirroring the `--mcp` / `--channel` surface (#779); seven smaller TUI/CLI/UX fixes; and the v1.9.0 [Unreleased] hierarchy-keys work (#848) finally cuts.

### Fixed

- **Two simultaneous agent-deck TUIs against the same profile no longer oscillate sessions to StatusError** ([#927](https://github.com/asheshgoplani/agent-deck/issues/927), [PR #944](https://github.com/asheshgoplani/agent-deck/pull/944)). With `[instances] allow_multiple=true` (the default), running TUI A on the desktop and TUI B via SSH from the phone caused `killStaleControlClients` in `internal/tmux/pipemanager.go:443` to SIGTERM every control-mode client whose `client_pid != os.Getpid()` — making no distinction between truly orphaned clients from a previously-crashed TUI (#595's motivation) and live sibling TUI's active control pipes. So A's reconnect sweep killed B's pipes, and vice versa, indefinitely; every managed session flipped to error within ~20s. Fixed by new `isControlClientOrphan(pid int) bool` helper that reads PPID from `/proc/<pid>/stat` on Linux (zero-fork) or `ps -p <pid> -o ppid=` on macOS / as fallback; if `ppid <= 1` or `kill(ppid, 0) == ESRCH` → orphan, sweep; otherwise reads the parent's exe (`/proc/<ppid>/exe` readlink, or `ps -p <ppid> -o comm=`) and checks whether it matches `os.Executable()` or has "agent-deck" in its basename. Match → live sibling, preserve. No match (init / systemd-user / launchd / random binary) → orphan, sweep. Any metadata-read error returns true (treat as orphan) so #595's cleanup semantics are preserved on hosts where both `/proc` and `ps` fail (essentially never). Also relevant to #936 (Kevsosmooth's 9-orphan report) (PR #944).

- **Conductor `bridge.py` no longer crashes on non-UTF-8 CLI output, and Telegram replies are clean instead of statusline garbage** ([#920](https://github.com/asheshgoplani/agent-deck/issues/920), [#921](https://github.com/asheshgoplani/agent-deck/issues/921), [PR #926](https://github.com/asheshgoplani/agent-deck/pull/926)). Three converging fixes in `internal/session/conductor_templates.go`: (1) `subprocess.run(..., errors="replace")` survives non-UTF-8 bytes in CLI output — the bridge previously crashed with `UnicodeDecodeError` when CLI captures contained ANSI escape sequences with high-bit chars under Python 3.14.4 (earlier Pythons silently mangled bytes rather than crashing). Closes #920. (2) `get_session_output()` switches from `-q` (raw pane capture) to `--json` and parses the `content` field — the bridge was forwarding the cosmetic frame + statusline to Telegram instead of the assistant reply. Closes #921 output path. (3) `send_to_conductor()`'s `--wait` branch now fetches the reply via a separate `session output --json` call rather than parsing `session send --wait`'s stdout — `--wait` emits only a send-confirmation JSON then prints the response as raw pane capture, so the reply can't be extracted from that stdout cleanly; the two-call pattern resolves this. Closes #921 wait/send path. Validated against multiple Telegram round-trips on macOS 26.x ARM64 + Python 3.14.4 (PR #926).

- **Attached session status refreshes immediately on exit instead of taking 2–3s to mark with `X`** ([PR #854](https://github.com/asheshgoplani/agent-deck/pull/854)). When exiting a session with `/q`, agent-deck navigates back to the list but the just-closed session took 2–3 seconds to be marked with `X`. Three coordinated fixes: reconcile the just-attached session status synchronously when returning to the main UI; clear stale hook fast-path state for that session before live tmux polling; and refresh delayed attach-return pane caches as a secondary repaint (PR #854).

- **`ctrl+w` in path inputs deletes only the trailing segment, and `Tab` no longer silently jumps off a non-existent path** ([#896](https://github.com/asheshgoplani/agent-deck/issues/896), [PR #908](https://github.com/asheshgoplani/agent-deck/pull/908)). Bubbles' default `deleteWordBackward` stops at whitespace; paths usually have none, so on `/a/b/c` ctrl+w cleared everything instead of dropping the trailing segment. New path-aware backward-word delete also stops at `/`, wired into new-session path input (`focusPath`), worktree branch input (`focusBranch` — slashes are common in branch names), the multi-repo path editor, and the edit-paths dialog: `/a/b/c` → `/a/b/`, `~/x/y` → `~/x/`, `feature/foo` → `feature/`. Second fix: Tab on a non-empty path that doesn't resolve to an existing directory now keeps focus on the input (previously focus advanced to the agent selector and the typed value was left dangling). Empty paths and valid directories advance as before. Nine table cases in `TestDeleteWordBackwardPath` plus end-to-end `TestNewDialog_CtrlW_*` and `TestNewDialog_Tab_*` (PR #908).

- **Lone ESC press no longer requires a second key to register inside TUI dialogs** ([PR #917](https://github.com/asheshgoplani/agent-deck/pull/917)). `csiuReader` was buffering a lone `0x1b` byte waiting for the next byte to decide if it was the start of an escape sequence; on a blocking stdin this meant ESC was never delivered until another key arrived, so the first Esc press appeared to do nothing in every dialog (also affected `yN` etc. wherever a lone ESC was the canonical "cancel"). Added a `pollFn` field (backed by `unix.Poll` on POSIX, stub on Windows) that checks within 50ms whether more bytes follow: if none arrive, the ESC flushes immediately as a standalone keypress; if bytes do follow, they bundle with the ESC as before, preserving SS3/CSI sequence handling. Same shape as charmbracelet/ultraviolet's timer-based flush; the difference is `unix.Poll` lets us stay zero-goroutine on the read path. Reported on macOS 14.x (PR #917).

- **Watcher `[source]` settings are now actually read from `watcher.toml` instead of silently ignored** ([PR #938](https://github.com/asheshgoplani/agent-deck/pull/938)). The watcher engine's `RegisterAdapter` call in `internal/ui/home.go` passed an empty `Settings` map, so adapter `Setup()` always saw no configuration — making the **github adapter unusable** (`adapter_setup_failed: github adapter requires a webhook secret` regardless of what was in `~/.agent-deck/watcher/<name>/watcher.toml`) and forcing webhook/ntfy/slack adapters to silently fall back to defaults. New `loadWatcherSourceSettings()` helper reads the `[source]` table from each `watcher.toml` before `RegisterAdapter`, restoring the documented behaviour described in `AdapterConfig.Settings`'s comment (`adapter.go`) and in the `watcher-creator` skill's TOML template. Errors (file missing, parse error, no `[source]` section) yield an empty map so adapters fall back to defaults — matching pre-patch behaviour when no config is present. Verified by the github-adapter reproduction in the PR (port 18461 was not listening pre-patch; binds on TUI start and accepts signed webhooks post-patch with HTTP 202) (PR #938).

### Added

- **Per-session Claude Code plugin enablement, mirroring the `--mcp` / `--channel` surface** ([PR #779](https://github.com/asheshgoplani/agent-deck/pull/779)). First-class management of Claude Code plugins on a per-session basis. RFC: `docs/rfc/PLUGIN_ATTACH.md`. New CLI flag `--plugin <name>` on `add` / `launch`; new subcommand `agent-deck plugin {list,attached,attach,detach}` with `--restart`, `--no-channel-link`, `--json`, `-q`. TUI gains a Plugin Manager dialog (hotkey `l`) with toggle/apply UX; Edit Session dialog gains `Plugins` / `PluginChannelLinkDisabled` fields. Mechanics: catalog as `[plugins.<name>]` tables in `~/.agent-deck/config.toml` (cached, telegram-official filtered per RFC §6); `Instance.Plugins`, `Instance.PluginChannelLinkDisabled`, `Instance.AutoLinkedChannels` persisted via `state.db` tool_data blob. Worker-scratch overlay: `EnsureWorkerScratchConfigDir` writes a deny ∪ allow overlay on `enabledPlugins` in scratch `settings.json` — detached catalog ids are forced to `false` to defeat Claude Code's "installed-but-unspecified = enabled" default. Auto-install: `claude plugin install <id>` shells out for unresolved attached plugins; best-effort, per-(profile, plugin) flock, env scrubbed (allow-list + secret-suffix blocklist) so postinstall hooks cannot exfiltrate `CLAUDE_API_KEY` / `TELEGRAM_BOT_TOKEN` / `NPM_TOKEN`. Channel auto-link (RFC §4.7): catalog entries with `EmitsChannel=true` add `plugin:<name>@<source>` to `Channels` and `AutoLinkedChannels` tracks ownership (PR #779).

- **Cross-profile data migration: `session move` / `conductor move` / `group move`** ([#928](https://github.com/asheshgoplani/agent-deck/issues/928), [PR #929](https://github.com/asheshgoplani/agent-deck/pull/929)). Three new CLI surfaces for relocating data between profile DBs — previously only possible by hand-editing SQLite + `meta.json`: `agent-deck session move <id> --to-profile <name> [--force]`; `agent-deck conductor move <name> --to-profile <name> [--force]` (moves conductor session + every child worker + atomically rewrites `meta.json`); `agent-deck group move <group> --to-profile <name> [--force]` (batch). Orchestrator in `internal/session/profile_migrate.go` is target-write-then-source-delete, with best-effort rollback if source-delete fails after target-insert. `cost_events` and `watcher_events` (matched on both `session_id` and `triage_session_id`) travel with each session, group rows auto-create in the destination, and every state.db write is wrapped in the existing `withBusyRetry` helper. Behavior decisions (clarified with the requester): running sessions are refused, `--force` overrides at the user's risk; conductor scope moves conductor + all children (`parent_session_id == conductor.id`); missing target profile is refused (typo safety) — user must bootstrap with `--profile <name>` first; re-running is idempotent. 11 unit cases in `profile_migrate_test.go` cover preserves-all-fields, cost/watcher events, group creation, running-refusal + force, missing/same profile, idempotency, conductor children + atomic meta, group batch, concurrent migrations (PR #929).

- **`.worktreeinclude` for automatic file copying into new worktrees** ([PR #890](https://github.com/asheshgoplani/agent-deck/pull/890)). When `.worktreeinclude` exists in the repo root, gitignored files matching its patterns are automatically copied into new worktrees before `worktree-setup.sh` runs. Matches [Claude Code Desktop semantics](https://code.claude.com/docs/en/worktrees#copy-gitignored-files-into-worktrees): only files that are both pattern-matched AND gitignored get copied, tracked files are never duplicated. Eliminates the need for boilerplate file-copy logic in `worktree-setup.sh` — projects declare what needs copying and reserve the setup script for imperative tasks like dependency installation. Read from repo root (not worktree, since source files are gitignored and only live in the main checkout); runs before `worktree-setup.sh` so the script can depend on copied files; NUL-delimited `git check-ignore -z` for path safety; directories merge into existing destinations (individual files skip if already present). New dependency: `github.com/sabhiram/go-gitignore` for `.gitignore`-syntax pattern matching (PR #890).

- **New-session dialog widened to 84 columns to fit long project paths** ([PR #894](https://github.com/asheshgoplani/agent-deck/pull/894)). Raise default dialog width to 84 columns and tie text field widths to the effective dialog size so project paths are not clipped on typical terminals (PR #894).

- **`agent-deck session send --draft` pre-fills the prompt without pressing Enter** ([PR #930](https://github.com/asheshgoplani/agent-deck/pull/930)). Useful for scripts that inject context into a running session: the user can review and submit manually instead of the message being auto-submitted (PR #930).

- **`[codex].command` configures the codex executable for built-in Codex sessions** ([PR #934](https://github.com/asheshgoplani/agent-deck/pull/934)). Built-in Codex sessions can now use a configured executable, wrapper, or alias. Codex resume / session discovery now honors inline `CODEX_HOME`, including quoted paths with spaces (`CODEX_HOME="/path with spaces/.codex"` shape). Regression coverage in `TestBuildCodexCommand_QuotedInlineCodexHomeWithSpaces`, `TestCodexHomeFromCommand_PreservesQuotedAssignmentSpaces`, `TestBuildCodexCommand_InlineCodexHomeForRolloutCheck` (PR #934).

### Changed

- **In-group hierarchy keys: K/J auto-promote at the parent's edge, and Shift+Left / Shift+Right explicitly outdent / indent** ([#849](https://github.com/asheshgoplani/agent-deck/issues/849), [PR #848](https://github.com/asheshgoplani/agent-deck/pull/848)). `K` / `J` (and `Shift+↑` / `Shift+↓`) now promote a sub-session to top-level when it is the first / last child of its parent, instead of silently no-op'ing. `Shift+→` demotes the cursor's top-level session to a sub-session of the previous top-level peer (last child); `Shift+←` is the symmetric outdent. All four shortcuts stay scoped to the current group — cross-group moves remain on `M`. Sub-sessions of different parents were previously interleaved in the visual flat list, and the only way to move a sub-session out of its parent without dropping to the CLI was `agent-deck session unset-parent <id>` (or `M` to go to a different group, which is the wrong tool); K/J at the parent boundary was the one ergonomic gap left after #846. Single-level nesting and child-count guards mirror the existing `session set-parent` CLI validation. New `GroupTree.PromoteSession` / `GroupTree.DemoteSession` methods plus boundary-promote logic inside `MoveSessionUp` / `MoveSessionDown`; covered by `TestPromoteSession_*`, `TestDemoteSession_*`, `TestMoveSession*Promotes`, `TestMoveSession*TopLevelAt*NoOp` in `internal/session/groups_test.go` (PR #848).

- **`[worktree]` settings documented in the config reference** ([PR #862](https://github.com/asheshgoplani/agent-deck/pull/862)). The `[worktree]` block (`path_template`, `branch_prefix`, `default_location`, `auto_cleanup`, `setup_timeout_seconds`, `default_enabled`) was fully implemented but missing from the config reference. This adds the complete section with a code block showing all options with defaults, a parameter table with types/defaults/descriptions, path template variable examples, branch prefix examples (default, `$USER/`, empty string), and an entry in the complete example. Source: `internal/session/userconfig.go` `WorktreeSettings` struct (PR #862).

- **`eof_fallback_fired` WARN and `stale_control_clients_swept` INFO observability for tmux close-cascade / orphan-PID bursts** ([PR #906](https://github.com/asheshgoplani/agent-deck/pull/906)). Two production diagnostics for the [tmux/tmux#4980](https://github.com/tmux/tmux/issues/4980) race that the EOF fast-path mitigation in #882 only partly addresses; pure additions, no behavior change. `eof_fallback_fired` (Warn) in `controlpipe.go:Close` captures the previously-discarded `reapWithEOFGrace` return and logs when the EOF fast-path times out and the soft-kill fallback fires — the fast-path's design assumption is "fallback should essentially never fire", so production logs will tell us whether burst-close pressure is silently dropping us out of the fast path. `stale_control_clients_swept` (Info, with `kill_count` + `duration`) in `pipemanager.go:killStaleControlClients` is emitted whenever the function SIGTERMs ≥ 1 orphan `tmux -C` client — the 2026-05-08 crash on this path was 5 SIGTERMs in 11ms across 3 parallel `Connect()` invocations, a pattern not previously surfaceable because each individual SIGTERM was Debug-logged separately. The burst-shape Info line lets the cascade be observed as a single event in `~/.agent-deck/debug.log`. Recurs at MTBF ~22h on macOS Homebrew tmux 3.6a on v1.7.72-2-g6b82525 (PR #906).

### Docs

- **Five explainer images embedded into the user-facing concept guides** ([PR #799](https://github.com/asheshgoplani/agent-deck/pull/799)). New PNG diagrams in `documentation/assets/`: `conductor-overview.png` (top of CONDUCTOR.md), `channels-topology.png` (CONDUCTOR.md one-bot-per-conductor section), `watcher-doorbell.png` (top of WATCHERS.md), `skills-tiers.png` (top of SKILLS.md), `watchdog-restart.png` (top of WATCHDOG.md). Generated via codex CLI's built-in `image_gen` tool (gpt-image-2), Tokyo Night palette, technical-architecture style. Total ~5.6 MB. Refresh recipe lives in the conductor's CLAUDE.md so any conductor can regenerate one of these in ~2 minutes (PR #799).

### Chore

- **Orphaned worktree gitlinks removed** ([PR #897](https://github.com/asheshgoplani/agent-deck/pull/897)). Two gitlinks (`.claude/worktrees/agent-a3b98724`, `.claude/worktrees/agent-af955763`) were committed without a corresponding `.gitmodules` entry — orphaned submodule references from stale Claude Code worktrees that no longer exist. Removed, and `.claude/worktrees/` added to `.gitignore` to prevent future accidental commits of worktree tracking state (PR #897).

### Known issues

- `internal/costs::TestStore_TotalLastWeek_OnlyLastWeekEvent` still fails when the local clock is on a Monday in UTC (issue [#932](https://github.com/asheshgoplani/agent-deck/issues/932), unchanged from v1.9.0/v1.9.1). Test-fixture time-arithmetic edge case in a package untouched by v1.9.2; does not affect built binaries. v1.9.2 was cut on a Wednesday — full suite green.
- `Weekly Regression Check` cron failures (issue [#943](https://github.com/asheshgoplani/agent-deck/issues/943)) are unrelated to v1.9.x and remain open.

## [1.9.1] - 2026-05-11

Patch release on top of v1.9.0 — two stability fixes from same-day post-release triage. Both touch the session lifecycle: cascade-prevention via serial-within-group as the new default, and `agent-deck rm` correctness under concurrency plus notifier cleanup.

### Fixed

- **`agent-deck rm` silently lost ~11 of 14 removals under parallel `xargs -P` invocation** ([#909](https://github.com/asheshgoplani/agent-deck/issues/909), [PR #935](https://github.com/asheshgoplani/agent-deck/pull/935)). `SaveWithGroups` rewrites the entire instances table via `INSERT OR REPLACE`, so a concurrent `rm` process resurrects rows another process just deleted — the CLI nevertheless printed `✓ Removed` for each call. Fixed by the new `RemoveSessionAndVerify` flow in `internal/session/storage.go`: targeted DELETE wrapped in `withBusyRetry` (the helper landed in #912), `SaveGroupsOnly` for any group structural change (never `SaveWithGroups`), then re-read the row and re-DELETE on resurrection with linear backoff. Returns `ErrRemovalNotPersistent` after exhaustion so the CLI exits nonzero instead of falsely claiming success. Same shape also fixes the separately-noted "`session remove --force` reports success but the row stays" failure mode — both `cmd/agent-deck/main.go` (`handleRemove`) and `session_remove_cmd.go` (`--force` + `--all-errored`) route through the new helper. Two-way correctness check: temporarily reverting to the old `DeleteInstance + SaveWithGroups` flow reproduced 11/12/13 survivors across three runs of `TestRm_ParallelDoesNotLoseRemovals` — matching the issue's "only ~3 of 14 actually deleted" report. With the fix restored, `go test -race -count=5 -run TestRm_ ./internal/session/` is green (PR #935).

- **Notifier inboxes replayed `deferred_target_busy` events forever for sessions removed via `agent-deck rm`** ([#910](https://github.com/asheshgoplani/agent-deck/issues/910), [PR #935](https://github.com/asheshgoplani/agent-deck/pull/935)). `~/.agent-deck/inboxes/<conductor>.jsonl` and `runtime/transition-notify-state.json` accumulate entries keyed by `child_session_id`; nothing previously cleared them when the child went away. New `internal/session/rm_sweep.go` adds `SweepInboxesForChildSession(id)` (atomic per-file temp+rename; whole-file removal when nothing survives) and `RemoveNotifyStateRecord(id)` (idempotent JSON edit). Both are best-effort: failures warn but never block the rm. Wired into both single-session and `--all-errored` bulk rm paths after a successful `RemoveSessionAndVerify` (PR #935).

### Changed

- **Serial-within-group is now the default for newly-created groups** ([PR #933](https://github.com/asheshgoplani/agent-deck/pull/933)). Adds `MaxConcurrent` to `Group` / `GroupData` / `GroupRow` (SQLite column added idempotently); semantics are `<=0` unlimited (legacy default for pre-v1.9.1 groups), `1` serial (new-group default), `N>=2` cap at N. `agent-deck launch` and `agent-deck session start` consult the target group's cap; over-cap launches persist as `Status=queued` (a new real registry state surfaced in `list --json` as `status=queued`) instead of starting. `agent-deck session stop` drains the queue: after Kill, finds the oldest queued sibling in the same group via FIFO and starts it. CLI exposes the field via `agent-deck group create --max-concurrent N` and `agent-deck group update --max-concurrent N`. Driven by two converging signals: the **2026-05-08 cascade** (9 parallel workers launched into `agent-deck-stability` triggered systemd-oomd → killed the conductor scope; the per-MCP cgroup wrapper from #902 prevents one MCP from dragging the orchestrator down, but doesn't cap the *number* of co-resident workers a group may spawn), and **Factory Missions research** ("Parallel agents conflict, duplicate work, make inconsistent architectural calls. Serial is the working pattern."). Backward compat preserved — existing groups loaded from a row with `max_concurrent=0` keep legacy unlimited behavior; only groups created via `GroupTree.CreateGroup` post-v1.9.1 default to 1 (PR #933).

### Known issues

- `internal/costs::TestStore_TotalLastWeek_OnlyLastWeekEvent` still fails when the local clock is on a Monday in UTC (issue [#932](https://github.com/asheshgoplani/agent-deck/issues/932), unchanged from v1.9.0). Test-fixture time-arithmetic edge case in a package untouched by v1.9.1; does not affect built binaries.

## [1.9.0] - 2026-05-11

Stability + cascade-prevention release. Closes the v1.8 flicker / silent-drop / panic-cascade bug class and the 2026-05-08 conductor-OOM cascade. Seven themed bundles from the V1.9 priority plan land here; remaining T2 / T5 / T7 longer-tail items follow in v1.9.x patches.

### Fixed

- **Inotify `IN_Q_OVERFLOW` no longer leaves hook state stale until restart** (root cause of the v1.8 "flickering red dots" report). The status-file watcher now re-walks the hooks dir from disk on overflow, builds a fresh map, and atomically swaps it under the mutex. Detection via `errors.Is(err, fsnotify.ErrEventOverflow)` so wrapped errors from future fsnotify versions still trigger recovery (PR #900, theme T1).

- **Hook read/parse failures are no longer silently swallowed.** `internal/web/session_data_service.go:170/185/190` previously `continue`d on `os.ReadDir`, `os.ReadFile`, and `json.Unmarshal` errors — producer-side hook bugs from `cmd/agent-deck/hook_handler.go` were invisible while the UI showed stale status. WARN logs added at all three sites (`hook_status_dir_read_failed`, `hook_status_read_failed`, `hook_status_unmarshal_failed`); `os.ErrNotExist` on the dir stays silent for the legitimate first-run condition (PR #900, theme T3).

- **`cmd/agent-deck/hook_handler.go` 8 silent swallows** in `writeHookStatus` and `writeCostEvent` (`MkdirAll`, `json.Marshal`, `WriteFile`, `Rename`) now emit WARN with file/instance/error context. Best-effort `.tmp` cleanup on Rename failure prevents orphan accumulation (PR #900, theme T3).

- **`session send` retry-cadence regression guards rewritten.** The 8 `TestSendWithRetryTarget_*` canonization tests at `cmd/agent-deck/session_send_test.go:242/257/277/332/360/385/434/594` were holding the pre-#876 silent-success contract — they would have actively gated a re-introduction of the silent-drop bug. Rewritten to assert the post-#876 contract (`verifyDelivery: true` → silent drops surface as errors). State-machine assertions (Enter/Ctrl+C call counts, retry budget shape) preserved as independent regression guards. CLI-vs-TUI parity tests for `Waiting` / `Idle` / `Error` added next to the existing `Running` parity test in `internal/session/instance_cli_parity_test.go` — the previous parity coverage was a one-state slice (PR #899, theme T4).

- **Duplicate MCP children for same `(session, name)` prevented.** Companion to #902. Strict per-`(session, mcp-name)` singleton check before any new spawn; 5s Stop timeout before fresh Start (was racing on restart); INFO log on every spawn (`mcp_proc_spawn`) for forensic visibility. The 43×-duplicate `context7-mcp` source traces to claude code's npx-spawned plugin launcher (an upstream issue documented but not fixable here); combined with #902's per-scope cgroup wrapper, agent-deck is now resilient even to that upstream bug. Test asserts 5x session restart yields exactly 1 child process (PR #904).

- **Mcppool synchronous failure surfacing + `HTTPServer` FD-leak + `Cmd` race.** Follow-up to #902 caught by the v1.9.0 release gate: under the new `systemd-run --scope` wrapper, `SocketProxy.Start` and `HTTPServer.Start` no longer swallow inner-binary failures (`/no/such/binary` now surfaces synchronously, restoring the pre-#902 contract). Companion fixes close a log-file FD leak in `HTTPServer.Start` (24 leaked FDs over 200 failed cycles → 0) and eliminate a `WARNING: DATA RACE` on `os/exec.Cmd.Wait()` at `internal/mcppool/http_server.go:301` (concurrent `*Cmd` access between Start.func3 and the cleanup goroutine added by #915) (PR #931).

- **SQLite contention + atomic conductor metadata writes** (theme T7). New `withBusyRetry(name, op)` helper extracted from `SaveWatcherEvent`'s 5-attempt retry pattern and applied to three sister sites that previously had no retry under SQLITE_BUSY: `WriteStatus` (transition daemon hot path), `UpdateWatcherEventRoutedTo` (triage reaper, 4 callers), `pruneWatcherEvents` (post-insert pruner). Helper logs WARN on first retry, ERROR on exhaustion — contention storms are now diagnosable instead of silent. `SaveRecentSession` INSERT + prune wrapped in a transaction. `SaveConductorMeta` switched to write-temp-rename for crash-safe atomicity (a crash mid-write previously truncated the file) (PR #912, theme T7).

- **FD leaks + watcher channel-close + ptmx mutex** (theme T5). Three documented FD leaks plugged: `tmux/chrome.go:163` (iTerm badge log file), `mcppool/socket_proxy.go:211`, `mcppool/http_server.go:148`. `engine.Stop()` now closes exported watcher event channels so consumer `range` loops exit cleanly instead of blocking forever (`<-engine.EventCh()` returns `(_, false)` post-Stop). `ptmx` mutex extended to cover `WriteInput` and `streamOutput` in `web/terminal_bridge.go:131/105`, eliminating the same shape of race that previously produced an intermittent CI flake on the read path (PR #915).

### Added

- **`internal/sessionstatus/` — single-owner hook→status derivation package.** Replaces the duplicated decision tree (tool gate → freshness window → hook→status switch) that lived in both `internal/session/instance.go` `UpdateStatus` and `internal/web/snapshot_hook_refresh.go`. Concrete divergences caught and locked by tests: codex `running` 20s window (CLI rejected, web accepted up to 2 min); claude `waiting` + acknowledged → idle (CLI dropped, web stuck forever); duplicated `IsClaudeCompatible(tool) || tool == "codex" || tool == "gemini"` tool gate now in `sessionstatus.IsHookEmittingTool`. v1.9.0 migrates only the web read-path (closes the active bug class); CLI / TUI / transition-daemon migrations follow in v1.9.1 per the plan's staged scope (PR #898, theme T1).

- **`flicker_detected` WARN.** New `internal/session/flicker_detector.go` watches per-session status transitions over a sliding 60s window. Emits one WARN per session per 60s cooldown when transitions exceed 3 — the six-flicker oscillation incident logged at Debug only would now surface at default log level. Wired into `internal/ui/home.go` at the existing `status_changed` site, only fires when `newStatus != oldStatus` so quiet sessions pay no cost (PR #900, theme T3).

- **`internal/safego` — `safego.Go(logger, name, fn)` panic-recovery helper.** Runs `fn` in a goroutine under a deferred `recover()`, logs panics at WARN with name + recovered value + runtime stack, then swallows. Applied to the 4 fire-and-forget goroutines in `internal/ui/home.go` that previously had no panic recovery (`startup_pipe_connect`, `startup_log_maintenance`, `apply_theme_to_sessions`, `conductor_clear_and_heartbeat`) — a single un-recovered panic in any of these previously killed the entire TUI. The 4 well-placed `recover()` arms already living at `home.go:2700-3000` are unchanged; this factors the same pattern into a reusable helper for the previously-unguarded sites (PR #901, theme T6).

- **`tmux.go:2517` defensive type-assertion guard.** Comma-ok form on the `singleflight.Do` result. The closure unconditionally returns `(string, nil)` today so the bare assertion couldn't panic, but the form was flagged as a load-bearing code smell if the closure is ever refactored. Pure hygiene; no behavior change (PR #901, theme T6).

- **MCP-per-scope cascade prevention.** On Linux+systemd hosts, each MCP child process now spawns inside its own transient user scope (`mcp-<owner>-<mcp>-<ts>.scope` under `mcp-pool.slice`) with `MemoryMax=1G`, `CPUWeight=50`, and `TasksMax=200`. Background: a 2026-05-08 cascade saw 43 simultaneous duplicate `@upstash/context7-mcp` instances accumulate inside the conductor's tmux scope (58.2 GB resident); when `user@1000` memory pressure crossed 50 %, systemd-oomd ranked cgroups by memory × pressure × pgscan and picked the conductor scope (largest by RSS) — SIGKILLing 604 processes in one shot, including the conductor itself. Per-MCP scopes give systemd-oomd a precise kill target so a misbehaving MCP becomes its own victim instead of dragging the orchestrator down. Gated behind `AGENT_DECK_MCP_ISOLATION` (default ON on Linux, OFF elsewhere); falls back to plain `exec.Cmd` when `systemd-run` is missing (containers, minimal images) or on macOS/Windows. Scope semantics use `systemd-run --scope`, which `execve`'s into the target command — so PID, file descriptors, env vars, process group, and the existing `Setpgid + SIGTERM-the-pgroup` cancel logic in `socket_proxy.go` all keep working unchanged. Wired into both stdio (`internal/mcppool/socket_proxy.go`) and HTTP (`internal/mcppool/http_server.go`) MCP launch paths. Eight new regression tests in `internal/mcppool/scope_launcher_test.go` and `scope_launcher_integration_test.go`, including a two-way correctness check (mutating `wrapMCPCommand` to drop the wrapper makes the integration test fail with the child landing back in the parent's scope — exactly the cascade pattern from the root cause analysis) (PR #902).

- **Phase-1 regression coverage (12 cases).** Twelve focused unit tests targeting v1.8.x ship-blockers and P0 use-case scenarios — test-only, no impl changes. Profile precedence (`prof-001/002/003`): full ladder explicit > `AGENTDECK_PROFILE` > `CLAUDE_CONFIG_DIR` > `config.json default_profile` > literal "default", plus the `profileFromClaudeConfigDir` direct table. Web hook overlay (`web-001/002/003`): `refreshSnapshotHookStatuses` is now end-to-end-asserted on `GET /api/menu` and `GET /api/session/{id}`; defensive shapes (nil snapshot, nil session, group items, empty loader) locked in. Send (`send-001/002`): `messageDeliveryToken` body-prefix contract + `verifyDelivery=true` large-prompt path. Rebind (`rebind-001`): `clearRebindMtimeGrace` 5s boundary. Wire format (`status-001/route-001/status-stop-001`): each `session.Status` value's lowercase JSON literal, ghost-session 404 negative path, STOPPED-stickiness through both GET handlers (PR #903, theme T8).

- **Phase-1 test infrastructure — 8 harnesses.** `internal/testutil/{crossfixture,fakeclock,fakeinotify,logassert,multiclienttmux,profilefixture,teatesthelper}` packages plus shared scaffolding. Unblocks the broader Phase-1 regression cells that need fake-tmux / fake-inotify / fake-clock seams (PR #916, theme T8).

- **8 of top-10 logging additions** (theme T3). `session_created` INFO, `status_changed` with `reason` field, `hook_event` INFO, `hook_file_corrupt` WARN, `tmux_setup_failed` WARN, `http_request` middleware INFO, `hash_fallback_used` once-per-session WARN, `session_status_cascade` summary INFO. The remaining two (`pipe_degraded` aggregated WARN, `capture_pane_subprocess_fallback` WARN-promotion) follow in v1.9.x (PR #914, theme T3).

### Changed

- **Sister-function consolidation: clusters S1, S2, S3** (theme T2). The four `GetClaudeConfigDir*` siblings at `claude.go:246/305/370/410` collapse into a single `Resolve` function (the anti-pattern that produced #881 profile divergence). The four tmux-session-liveness sites at `tmux.go:1949/300/2451` and `pipemanager.go:568` consolidate behind one helper, ending the 2s-cache-window family of bugs (#886 heartbeat parity). The four `CapturePane*` siblings (S3) likewise unify. Drift-detection guard test asserts each consolidated cluster shares one symbol — any future PR re-introducing a parallel implementation fails at test time instead of at user-report time (PR #913, theme T2).

### Deferred to v1.9.x

- **`chore(v1.9.x): three small followups`** (PR #905, explicitly v1.9.x by author): `1.7.99 → 9.9.99` sentinel in `update_nudge_test.go` (T4), GitHub webhook normalizers stop swallowing `json.Unmarshal` (T2/T3) — landed on main but not part of the v1.9.0 thematic scope.
- CLI / TUI / transition-daemon migration to `internal/sessionstatus/` (v1.9.0 migrates only the web read-path).
- Remaining two logging additions (`pipe_degraded`, `capture_pane_subprocess_fallback` promotion).
- Sister-function clusters S4–S12 (S1/S2/S3 ship in #913; the rest follow incrementally).
- Phase-1 integration / e2e cells that depend on harness wiring still in flight.

### Known issues

- `internal/costs::TestStore_TotalLastWeek_OnlyLastWeekEvent` exhibits a deterministic failure when the local clock is on a Monday in UTC. Same class of bug previously addressed by #859; flagged here for a follow-up issue. Does not affect built binaries — purely a test-fixture time-arithmetic edge case in a package untouched by v1.9.

## [1.8.3] - 2026-05-07

Hotfix bundle on top of v1.8.2. Three contributor PRs: a TUI inline-title regression and two conductor heartbeat-rules improvements bringing the OS heartbeat path to parity with `bridge.py`.

### Fixed

- **Inline pane title vanished between refreshes when the tmux pane-info cache went stale** ([PR #877](https://github.com/asheshgoplani/agent-deck/pull/877), thanks @borng). `refreshSessionRenderSnapshot` reads `tmux.GetCachedPaneInfo` on every rebuild, but only `backgroundStatusUpdate` refreshes that cache. When other rebuild paths (e.g. `processStatusUpdate`) ran past the 4 s freshness threshold, `GetCachedPaneInfo` returned `ok=false` and the rebuild zeroed `paneTitle` — the inline task suffix added in #474 (Claude `/rename`, spinner state) blinked to empty between successful ticks. Fixed by falling back to the previous snapshot's `paneTitle` on cache miss; the fallback re-reads the latest snapshot inside the per-instance branch to narrow the read-store race between concurrent rebuild goroutines. Adds two regression tests: `TestRefreshSessionRenderSnapshot_PaneTitleUpdatesEachRefresh` (fresh-cache contract) and `TestRefreshSessionRenderSnapshot_PaneTitlePreservedWhenCacheStale` (the regression pin, fails on un-fixed code).

- **`HEARTBEAT_RULES.md` silently ignored on hosts using the OS heartbeat daemon** ([PR #886](https://github.com/asheshgoplani/agent-deck/pull/886), thanks @nlenepveu). PR #218 externalized heartbeat policy into `HEARTBEAT_RULES.md` but only wired it into `conductor/bridge.py`. The second heartbeat path — `heartbeat.sh` generated from `conductorHeartbeatScript` and scheduled by systemd/launchd — never read the file, and bridge.py auto-disables its own loop when the OS daemon is detected. Net effect: on the default Linux/macOS path the rules existed, the docs referenced them, and the script that actually fired ignored them. Fixed by resolving `HEARTBEAT_RULES.md` with the same triple fallback (per-conductor → per-profile → global), appending the rules after a blank line when the file is non-empty, and switching the message prefix from `Heartbeat:` to `[HEARTBEAT]` to match bridge.py and the conductor template docs. `MigrateConductorHeartbeatScripts` rewrites `heartbeat.sh` automatically — no user action required.

- **No way to point a conductor at a project-repo `HEARTBEAT_RULES.md` without copying** ([PR #887](https://github.com/asheshgoplani/agent-deck/pull/887), thanks @nlenepveu). `agent-deck conductor setup` already supported `--policy-md` for `POLICY.md`, but the equivalent for the heartbeat rules file was missing even though both share the same per-conductor → per-profile → global lookup order. Added `--heartbeat-rules-md`, a direct mirror of `--policy-md`: it creates a symlink at `~/.agent-deck/conductor/<name>/HEARTBEAT_RULES.md` pointing at the user-supplied path, claiming the highest-precedence slot in the lookup order. Wired through `cmd/agent-deck/conductor_cmd.go` (flag + Usage block alongside `--policy-md`) and `internal/session/conductor.go` (`SetupConductor` / `SetupConductorWithAgent` accept `customHeartbeatRulesMD`, slotted right after `customPolicyMD`, reusing `createSymlinkWithExpansion` for `~` handling).

## [1.8.2] - 2026-05-07

Three real-bug fixes addressing top items from the priority survey: size-guard regression, tmux SIGSEGV adoption from a contributor branch, and TUI/web profile resolution divergence.

### Fixed

- **Size-guard rejected new sessions created by Claude `/clear`** ([#856](https://github.com/asheshgoplani/agent-deck/issues/856), [PR #883](https://github.com/asheshgoplani/agent-deck/pull/883)). Reported by @ZDreamer2. After `/clear` Claude wrote a fresh smaller jsonl that the size-guard refused to rebind to, leaving the TUI stuck on the old session. Fixed by adding an mtime-newer escape hatch: if the candidate jsonl is older by ≥5 s and the new one isn't, rebind regardless of byte size — preserves all existing flap-protection (where files seed within microseconds) while letting legitimate user-initiated `/clear` events through.

- **tmux SIGSEGV during ControlPipe shutdown on macOS Mac** ([#816](https://github.com/asheshgoplani/agent-deck/issues/816), [PR #882](https://github.com/asheshgoplani/agent-deck/pull/882), thanks @tarekrached). Cherry-picked from @tarekrached's `tarek/controlpipe-eof-clean-shutdown` branch — he ran 36/36 stress trials clean. Switches `ControlPipe.Close()` from a SIGTERM-then-grace fallback to a stdin EOF fast path with a 200 ms grace before falling back to soft-kill. Eliminates the upstream tmux #4980-class crash in real workflows.

- **TUI and web showed different sessions for the same user when `AGENTDECK_PROFILE` was set in env but not in `config.json` `default_profile`** ([#881](https://github.com/asheshgoplani/agent-deck/issues/881) [PR #884](https://github.com/asheshgoplani/agent-deck/pull/884)). The TUI/CLI inherit `AGENTDECK_PROFILE` from the parent shell; the web server read only `default_profile` from config. Same DB, two views — trust-killer. Fixed by unifying resolution: web now consults `AGENTDECK_PROFILE` first (matches TUI/CLI), falling back to `config.json`. Single source of truth.

## [1.8.1] - 2026-05-06

Hotfix bundle on top of v1.8.0. Five focused bug fixes — three from external contributors, two from accumulated triage.

### Fixed

- **`agent-deck session send` could silently drop prompts to sub-sessions** ([#876](https://github.com/asheshgoplani/agent-deck/issues/876), [PR #879](https://github.com/asheshgoplani/agent-deck/pull/879)). Reported by @DOKoenegras (v1.7.71). The verification loop in `sendWithRetryTarget` tracked positive delivery signals (active-status, paste-marker, message-in-pane) but treated their absence as success. Under timing races (sub-session spawned in quick succession, inner Claude TUI's input handler not yet mounted), every signal genuinely fails to fire and the loop returns `nil` after exhausting its 15s budget. Fixed by adding opt-in `verifyDelivery` to `sendRetryOptions`; default-on for the CLI's `defaultSendOptions()` and `noWaitSendOptions()` paths. When set, the loop now returns an error referencing #876 if no positive evidence is observed. Six new regression tests in `cmd/agent-deck/session_send_test.go`; two confirmed TDD red→green.

- **`bridge.py` failed to import on Python 3.8 (default WSL Ubuntu 20.04)** ([#864](https://github.com/asheshgoplani/agent-deck/issues/864), [PR #878](https://github.com/asheshgoplani/agent-deck/pull/878)). Reported by @JMBattista. Runtime use of `Coroutine` from `collections.abc` (PEP 585 subscript) failed on Python 3.8 with `TypeError: 'ABCMeta' object is not subscriptable`. Fixed by importing `Coroutine` from `typing` instead. Added `conductor/tests/test_python_compat.py` (AST scan for runtime PEP 585 subscripts) and `.github/workflows/python-compat.yml` (matrix on Python 3.8/3.9/3.10/3.11/3.12) so this can't regress.

- **Homebrew install verification** ([#873](https://github.com/asheshgoplani/agent-deck/issues/873), [PR #878](https://github.com/asheshgoplani/agent-deck/pull/878)). Reported by @Wolfsrudel. Live infrastructure was already healthy on v1.8.0 (goreleaser brews block fired correctly, formula present at `asheshgoplani/homebrew-tap`); the original report predates that fix. Added `scripts/verify-homebrew-install.sh` (8 checks: tap reachable, formula present, version matches latest release, all asset URLs resolve, README install command unchanged) and `.github/workflows/homebrew-verify.yml` (runs on PRs touching install docs / goreleaser, on every release tag, weekly cron) so future drift gets caught.

- **TOCTOU race in worktree setup script executable-bit dispatch** ([PR #861](https://github.com/asheshgoplani/agent-deck/pull/861), thanks @spawnia). `buildSetupCmd` re-stat'd the script after `findWorktreeSetupScript` had already statted it, opening a window where mode bits could change between calls. Fix captures `os.FileMode` once at discovery and threads it through. Internal-only signature change; public `CreateWorktreeWithSetup` API unchanged. New test `TestFindWorktreeSetupScript_PresentExecutable` validates the captured mode.

- **`~` in worktree `path_template` was treated as a literal directory** ([PR #863](https://github.com/asheshgoplani/agent-deck/pull/863), thanks @spawnia). `resolveTemplate` did not expand `~` so configured paths like `~/.agent-deck/worktrees/{repo}/{branch}` resolved to nonsense like `/home/user/project/~/.agent-deck/worktrees/...`. `GenerateWorktreePath` already had the right expansion; this realigns `resolveTemplate` with it. Includes a regression test that fails with the literal-`~` path before the fix.

- **`%` filter exclude-set is now configurable; active-filter hint is highlighted** ([PR #874](https://github.com/asheshgoplani/agent-deck/pull/874), thanks @borng). Resolves [#491](https://github.com/asheshgoplani/agent-deck/issues/491) / [#516](https://github.com/asheshgoplani/agent-deck/issues/516). Adds `[display].active_filter_excludes` config — default `["error", "stopped"]` preserves existing behavior; opt in to `["error"]` to keep stopped/closed sessions visible. The pill bar's dim state, `matchesStatusFilter`, and per-frame hint render all consult the same exclude set. The `$` keybinding alignment between TUI/MD docs/UI hint is a follow-up; see borng's comment on PR #874.

## [1.8.0] - 2026-05-06

WebUI redesign — five-zone responsive layout. Ships PR-B ([PR #860](https://github.com/asheshgoplani/agent-deck/pull/860)) on top of every accumulated v1.7.81-v1.7.83 hotfix that the redesign was originally targeted at. Users running v1.7.83 still saw the pre-redesign UI; v1.8.0 is the version where the new shell actually reaches them.

### Added

- **Five-zone AppShell** — top bar, left rail, main pane, right rail, mobile tab bar. Replaces the prior two-pane layout. Tablet (~820px) and phone (<720px) breakpoints documented in the playwright `chromium-tablet` / `chromium-phone` projects.
- **RightRail panel** — pulled session-context affordances out of the main pane into a dedicated rail (toggleable on tablet, hidden on phone in favor of the bottom tab bar).
- **MobileTabs** — bottom tab bar that surfaces the rail/main switching that desktop gets via the side rails.
- **CommandPalette redesign** — restyled chrome consistent with the new dialog system.
- **Restyled dialogs** — `CreateSession`, `Confirm`, `GroupName` move to the new design tokens; new dialog header/footer rhythm.
- **Restyled panels** — `Toast`, `ToastHistoryDrawer`, `SettingsPanel`, `EmptyState`, `TerminalPanel` chrome, `CostDashboard` chrome.
- **Design tokens** — `internal/web/static/app/design-tokens.css` extracts color / spacing / radius primitives consumed by the redesigned components. Tailwind output regenerated against the new source globs.
- **`/api/profiles` + `/api/system/stats`** — new GET endpoints powering `ProfileDropdown` (display-only by design) and the redesigned `StubPane`.

### Changed

- **Pre-redesign components removed.** The legacy two-pane chrome and its assets are gone — there is no `?legacy` toggle. Anyone pinning to an older bundle should pin to the v1.7.83 release artifacts.
- **Visual-baseline screenshots regenerated** for the new shell across desktop/tablet/phone projects.

### Fixed

- **Cold-load `profileSignal` no longer flashes "personal" before the API resolves** — initial render now defers the dropdown label until `/api/profiles` responds, so users on a non-personal profile don't see a one-frame flicker.
- **`TerminalPane` stays mounted across tab switches** — orphan signal exports that survived the redesign port were removed; tab switches now preserve PTY state instead of remounting and dropping the connection.

### Notes

- **Profile switcher is display-only.** `ProfileDropdown` shows the active profile but does not switch profiles from the web UI. Switching is still done via the `-p` / `--profile` flag at `agent-deck` invocation time. Surfacing read-only state was a deliberate scoping choice for v1.8.0.
- **Bundles every v1.7.81-v1.7.83 hotfix.** Multi-client tmux size mismatch ([#866](https://github.com/asheshgoplani/agent-deck/pull/866)), web `/api/sessions` waiting-status divergence ([#867](https://github.com/asheshgoplani/agent-deck/pull/867)), `TestTmuxPTYBridgeResize` CI skip ([#871](https://github.com/asheshgoplani/agent-deck/pull/871)) are all included — those releases shipped on top of the pre-redesign UI; v1.8.0 is where they meet the new shell.
- **Stack stays Preact + htm + signals.** No framework rewrite, no new dependencies. The redesign reorganizes layout and chrome only.

## [1.7.83] - 2026-05-06

Unblocks the release pipeline that failed twice on `TestTmuxPTYBridgeResize`.

### Fixed

- **CI-only test flake blocking goreleaser** ([PR #871](https://github.com/asheshgoplani/agent-deck/pull/871)). `TestTmuxPTYBridgeResize` asserts a WebSocket resize message propagates through the bridge's attach-client PTY all the way to the tmux session geometry. On CI's headless GitHub Actions runner, the attach-client PTY never reaches the requested 120×40 size — `pty.Setsize` is called locally but tmux's view of the client size stays at 80×24. Verified-working on real PTYs (macOS/Linux desktops). The production fix shipped in #866 stays covered by `Session.Start` tests in `internal/tmux`. This is a CI-environment workaround, not a production code change. Skipped only when `CI=true` or `GITHUB_ACTIONS=true`. Surfaced when v1.7.81 and v1.7.82 release pipelines both failed on this test.

Note: v1.7.81 and v1.7.82 tags exist on GitHub but no Release was ever published for either — both phantom tags. v1.7.83 is the proper landing of all three accumulated hotfixes (size-mismatch, status-divergence, CI test fix).

## [1.7.82] - 2026-05-05

Bundled hotfix release. Supersedes the v1.7.81 tag: that tag was created but no GitHub Release was ever published — the goreleaser pipeline failed on a CI-only test bug ([run 25395639116](https://github.com/asheshgoplani/agent-deck/actions/runs/25395639116)) before the binaries could be uploaded. v1.7.82 ships the v1.7.81-intended multi-client tmux size fix, the test fix that unblocks the release pipeline, and a separately-discovered status-divergence fix for the web UI.

### Fixed

- **Multi-client size mismatch ("dots in the window") between web UI and direct tmux clients** ([PR #866](https://github.com/asheshgoplani/agent-deck/pull/866)). Two contributing bugs combined: tmux's default `window-size latest` policy snapped the window to whichever client most recently sent input, and `(*tmuxPTYBridge).Resize` issued an explicit `tmux resize-window -x N -y M` on every browser FitAddon resize, which per `man tmux` implicitly flips the session option to `window-size=manual`. Together this dragged native attach clients (Ghostty, iTerm) to the web viewport's geometry and pinned them there. Fixed in two places: `internal/tmux/tmux.go` now sets `window-size=largest` (session) + `aggressive-resize=on` (window) per session at `Session.Start`, gated through the existing `[tmux] options` config-override mechanism so users can opt out; `internal/web/terminal_bridge.go` no longer issues `tmux resize-window` from `Resize` (the local `pty.Setsize` keeps xterm.js's grid correct), and the `-f ignore-size` flag was dropped from `tmuxAttachCommand` (no longer needed since the web client now participates in the `largest` arbitration alongside native clients). Smaller clients see clipped content rather than dragging the window. New integration test `TestSession_MultiClientSizePolicy_Integration` asserts both options are set after `Session.Start`. See tmux issue [#2594](https://github.com/tmux/tmux/issues/2594) for the upstream pattern. *(Originally targeted v1.7.81; the release pipeline failed on the test bug below before the binaries shipped.)*

- **`TestTmuxPTYBridgeResize` failed in the CI release pipeline, blocking the v1.7.81 goreleaser run** ([PR #869](https://github.com/asheshgoplani/agent-deck/pull/869)). The test created its tmux session manually without the `window-size=largest` option that the production code path now sets at `Session.Start` (PR #866). On CI's headless tmux, the default `window-size latest` policy interacts differently with the test's resize sequencing than on a developer machine, surfacing a flake that local runs never saw. Fix sets `window-size=largest` on the test session up-front so the test environment matches production. Verified with `go test -run TestTmuxPTYBridgeResize ./internal/web/` locally and on CI.

- **Web `/api/sessions` reported `error` for sessions whose hook file said `waiting`, while `agent-deck list --json` reported `waiting` for the same sessions at the same instant** ([PR #867](https://github.com/asheshgoplani/agent-deck/pull/867)). Root cause: the live web reads from `MemoryMenuData`, an in-memory snapshot pushed by the TUI's `publishWebSessionStates`. The TUI's view of `Instance.hookStatus` is fed by `StatusFileWatcher` (inotify); when an inotify event is dropped (queue overflow under load — 1100+ hook files in `~/.agent-deck/hooks/` is enough to hit this in steady state) the TUI's `hookStatus` stays stale, the hook fast-path freshness window expires, `Instance.UpdateStatus` falls through to tmux pane heuristics, and the published Status flips to `error`. The CLI does not have this gap because `agent-deck list --json` reads each hook file from disk per call via `session.RefreshInstancesForCLIStatus`. Fix adds `internal/web/snapshot_hook_refresh.go` that re-applies the hook fast-path Status mapping (matching `Instance.UpdateStatus`'s switch on `hookStatus`) to the cached `MenuSnapshot` before the GET handlers (`/api/sessions`, `/api/menu`, `/api/session/{id}`) serialize it. Stopped sessions are never overridden (user-intentional). Fresh hooks (within the 2-min `hookFastPathWindow`) override any non-stopped state. Stale `waiting` hooks specifically override snapshot=`error` because Claude's "waiting" state is durable across hook event gaps — a Stop hook that fired hours ago without a follow-up UserPromptSubmit means Claude is still at the prompt, exactly the case the CLI captures via tmux pane-title heuristics that the web cannot reach without per-request subprocesses. Live before/after on a system with 21 waiting sessions: web reported `waiting=0` before the fix, `waiting=21` after (CLI reported 21 throughout).

  Test coverage in `internal/web/snapshot_hook_refresh_test.go`: a regression test (`TestParity_WaitingStatusFlowsThroughHandler`) reproduces the exact production divergence by seeding a snapshot with `Status: StatusError` and an in-memory hook overlay saying `waiting`, then asserting `GET /api/sessions` returns `waiting`; this test fails before the fix and passes after. A property test (`TestRefreshSnapshotHookStatuses_NoHookFilePreservesAllStatuses` and the parallel `TestParity_AllStatusesPreservedThroughGetSessions`) iterates all six `session.Status` enum values (`StatusRunning`, `StatusWaiting`, `StatusIdle`, `StatusError`, `StatusStarting`, `StatusStopped`) and asserts each round-trips through the API unchanged when no hook overlay applies — locking the contract that adding a new Status without wiring the web fails the build. Plus targeted unit tests for stale/fresh override semantics, stopped-stickiness, and shell-tool no-op.

### Notes

- **v1.7.81 was a phantom tag.** The git tag `v1.7.81` exists in the repository (created by [PR #868](https://github.com/asheshgoplani/agent-deck/pull/868) merging) but no GitHub Release was ever published under that tag because the goreleaser workflow failed on the `TestTmuxPTYBridgeResize` test (now fixed in PR #869). The tag is left in place as a historical record. v1.7.82 is the proper landing of the v1.7.81-intended fixes plus the test fix and one additional status-divergence fix.

## [1.7.81] - 2026-05-05

Hotfix for a multi-client tmux size-negotiation bug that caused dot-filled void cells when the web UI and direct `tmux attach` clients were both connected to the same agent-deck session at different geometries.

### Fixed

- **Multi-client size mismatch ("dots in the window") between web UI and direct tmux clients.** Two contributing bugs combined: tmux's default `window-size latest` policy snapped the window to whichever client most recently sent input, and `(*tmuxPTYBridge).Resize` issued an explicit `tmux resize-window -x N -y M` on every browser FitAddon resize, which per `man tmux` implicitly flips the session option to `window-size=manual`. Together this dragged native attach clients (Ghostty, iTerm) to the web viewport's geometry and pinned them there. Fixed in two places: `internal/tmux/tmux.go` now sets `window-size=largest` (session) + `aggressive-resize=on` (window) per session at `Session.Start`, gated through the existing `[tmux] options` config-override mechanism so users can opt out; `internal/web/terminal_bridge.go` no longer issues `tmux resize-window` from `Resize` (the local `pty.Setsize` keeps xterm.js's grid correct), and the `-f ignore-size` flag was dropped from `tmuxAttachCommand` (no longer needed since the web client now participates in the `largest` arbitration alongside native clients). Smaller clients see clipped content rather than dragging the window. New integration test `TestSession_MultiClientSizePolicy_Integration` asserts both options are set after `Session.Start`. See tmux issue [#2594](https://github.com/tmux/tmux/issues/2594) for the upstream pattern.

## [1.7.80] - 2026-05-05

WebUI overhaul Phase 1 + one small Claude-session UX fix.

### Added

- **WebUI test infrastructure + TUI⇄web parity matrix (PR-A of WebUI overhaul, [PR #804](https://github.com/asheshgoplani/agent-deck/pull/804)).** Foundation PR for the WebUI redesign — pure test infrastructure with no design changes. Adds Vitest unit tests (`tests/web/unit/`, jsdom + @testing-library/preact, 11 specs against `api.js` + `state.js`), Playwright e2e + screenshot regression (`tests/web/e2e/`, 279 specs across 3 viewports — smoke, parity-actions with behavioral assertions for groups/settings/cost/push, parity-state, visual baselines), an in-memory web fixture binary (`tests/web/fixtures/cmd/web-fixture/main.go`) hardened against stale-server false-passes (OS-allocated ephemeral port + 16-byte random startup token + pid verification via `/__fixture/whoami`), the TUI ↔ web parity matrix at `tests/web/PARITY_MATRIX.md` cataloging 47 actions and ~50 state fields (surfaces a 64% action gap and 76% state-field gap from TUI to web) — tests now iterate the full matrix instead of a hard-coded subset, fail explicitly when any TUI/web row drifts, a Go runtime sync-invariant test at `internal/web/parity_test.go` that fires actions through both the HTTP surface and the mutator and asserts equal observable state including group create/rename/delete, `Makefile` targets (`test-web`, `test-web-unit`, `test-web-e2e`, `test-web-install`), and a `.github/workflows/web-tests.yml` CI workflow. Lifecycle endpoints get positive parity tests; missing endpoints get "stay missing" regression guards so any silent addition fails the build until the matrix is updated in lockstep. Three rounds of dual-review (Claude + Codex sibling-topology) drove the test-fidelity hardening. Stack stays Preact + htm + signals (already vendored). PR-B (visual redesign) builds on top of this.

### Fixed

- **Persist Claude New Session defaults** ([PR #853](https://github.com/asheshgoplani/agent-deck/pull/853), thanks @yaroshevych). Three new TOML keys on `[claude]` (`extra_args`, `use_chrome`, `use_teammate_mode`) persisted on Claude session creation and replayed via `SetDefaults`. Backward compatible: missing keys load as zero values. Note: every New Session creation now overwrites `cfg.Claude.DangerousMode / AllowDangerousMode / AutoMode` with whatever the dialog held — by design, but a hand-edited `dangerous_mode = true` in `config.toml` will flip if the box is unchecked.

## [1.7.79] - 2026-05-01

Two TUI polish fixes from @AdamiecRadek (their 5th and 6th PR landing this week — #813→#814 pricing, #818→#819 cost line, #836→#837 context window, #846, #847).

### Fixed

- **Session reorder (`K` / `J`, `Shift+Up` / `Shift+Down`) required multiple key presses to produce a visible move when sub-sessions of different parents were interleaved in a group.** `MoveSessionUp` / `MoveSessionDown` swapped with the immediate slice neighbor, but the render path re-buckets sub-sessions under their parents, so a swap with a non-sibling produced zero visible change. Fixed by walking the slice for the previous/next session with the same `ParentSessionID` (top-level peers treat each other as siblings, sub-sessions reorder among same-parent siblings only). One key press now always produces a visible move when one is possible. Tests in `internal/session/groups_test.go` cover the interleaving case, the first-sibling no-op, and the top-level-skips-subs case.
- **Help overlay (`?`) had broken column alignment when descriptions were longer than the description column.** `dialogWidth` defaulted to 48, leaving ~26-32 chars for descriptions; anything longer wrapped at column 0 and shredded the two-column layout (key | description). Fixed by raising `dialogWidth` to 70 by default (scaling to 80 on terminals ≥ 100 cols, shrinking only on narrow terminals) and adding a `wrapWithHangingIndent` helper so any description that still doesn't fit wraps with continuation lines aligned under the description column. The four worst offenders ("Next / prev session in current group", "Jump to Nth session in current group", "First / last session in current group", "Filter search scoped to current group") were also shortened (`current group` → `group`), as were "Edit session settings (title/color/notes/command/...)" and "Quick approve (send '1' to Claude session)". Test coverage for `wrapWithHangingIndent` in `internal/ui/help_test.go`.

## [1.7.78] - 2026-05-01

P0 hotfix for a data-loss bug in submodule worktree handling.

### Fixed

- **🚨 DATA LOSS: deleting a session whose worktree resolved to a submodule's gitdir destroyed the submodule's git data** ([PR #844](https://github.com/asheshgoplani/agent-deck/pull/844), thanks @plutohan for the catch and the fix). `git worktree list --porcelain` from inside a plain submodule reports the **gitdir** (`<super>/.git/modules/<name>`) as the worktree path for the main checkout, not the actual `<super>/<name>` working tree. Three flows (`agent-deck add -w`, `agent-deck launch -w`, TUI new-session with worktree enabled) consumed that path as `Instance.ProjectPath` and the tmux `-c` cwd. Sessions then dropped users inside the gitdir where source files don't exist, and worse — deleting the session via `session remove --force` invoked `RemoveWorktree(force=true)`, whose force-fallback called `os.RemoveAll(worktreePath)`, destroying the submodule's git history. Reproduced in the reporter's environment. Two-layer fix: (1) **prevention** — `parseWorktreeList` normalizes each non-bare entry through `git rev-parse --show-toplevel`, returning the actual working tree even when invoked from inside a gitdir; all three call sites and any future caller of `ListWorktrees` / `GetWorktreeForBranch` get the correct path; (2) **defense-in-depth** — `RemoveWorktree` refuses the `os.RemoveAll` fallback when the target path is structurally a git directory (`.git` basename, `.git/modules/<sub>`, `.git/worktrees/<wt>`, or bare repo via `IsBareRepo`). This catches stale session rows persisted before the prevention fix — they now error on delete instead of nuking git internals. 4 new tests cover the data-loss regression gate, the prevention invariant, and the defense-in-depth coverage of all gitdir-shaped paths. Out-of-scope follow-up flagged by reporter: TUI fork-with-reused-worktree path at `internal/ui/home.go:8441` updates `WorktreePath` but not `WorkDir`, leaving fork sessions with the originally-generated path as cwd — unrelated to submodules, separate concern.

**If you create sessions in submodules, upgrade to v1.7.78 immediately** — the prevention fix stops new sessions from getting the broken path; the defense-in-depth catches existing stale session rows on delete.

## [1.7.77] - 2026-05-01

Hotfix re-cut of v1.7.76. The v1.7.76 tag exists on the repo but no binaries were ever published — release CI failed on a chunked-read edge case in the SS3 reader added in #840 (rebased from #815). v1.7.77 contains all of v1.7.76 plus the chunked-read fix.

### Fixed

- **`csiuReader` flushed lone ESC at chunk boundary, breaking SS3 detection across split reads** ([PR #842](https://github.com/asheshgoplani/agent-deck/pull/842)). When `\x1b` arrived in one Read() chunk and `OH` in the next, the translator emitted ESC immediately and treated `OH` as plain bytes, skipping the SS3 → CSI Home rewrite. Fix mirrors the existing ESC-O-at-buffer-end pattern: when not final, buffer the lone ESC and wait for the next chunk; on final flush emit ESC as-is to preserve standalone-escape semantics. `TestCSIuReader_SS3HomeEnd_ChunkedRead/SS3_Home_split_between_ESC_and_OH` (added in #840) now passes — locked the regression that blocked v1.7.76's release CI.

(All v1.7.76 entries below carry forward unchanged — see "1.7.76" section for the polish + community-bug bundle that this release re-cuts.)

## [1.7.76] - 2026-05-01

Polish + community-bug bundle the day after v1.7.75. Three contributor fixes (Hristo, AdamiecRadek diagnosis, strofimovsky), three WebUI bug fixes from JMBattista combined into one PR, and a v1.7.74 follow-up I caught during production verification. All of #783/#784 + the busy-retry follow-up went through Claude+Codex dual-review (with codex trust pre-registration so peer review actually worked this time). Dual-review pipeline notes: codex-as-sibling topology works; codex-as-child-of-claude-worker hits sub-of-sub spawn limits.

### Fixed

- **Delete confirmation dialog focus trap broken; Enter re-fired the action** (thanks @JMBattista for [issue #784](https://github.com/asheshgoplani/agent-deck/issues/784)). The HTML `autofocus` attribute on the Cancel button was unreliable in Preact when the dialog re-rendered into an existing tree, so focus stayed on the row's Delete button — pressing Enter to "confirm cancel" instead re-triggered Delete (or worse, double-acted). Fix replaces `autofocus` with `useRef` + `useEffect(() => ref.current.focus(), [])`, adds `role="dialog"` + `aria-modal="true"` for a11y, and wires Esc keydown on the panel (Esc dismissal in `useKeyboardNav.js` preserved).

- **Hover toolbar overlapped tool/cost labels in session list** (thanks @JMBattista for [issue #783](https://github.com/asheshgoplani/agent-deck/issues/783)). The absolute-positioned action toolbar (`absolute right-2`) covered the inline tool label rendered in flow when hovering a row. Fix introduces a single `toolbarVisible` predicate and applies `invisible` (preserves layout, hides paint) to the metadata spans when toolbar is showing.

- **Disconnected sessions showed raw error tokens instead of actionable UX** (thanks @JMBattista for [issue #782](https://github.com/asheshgoplani/agent-deck/issues/782)). When a session's tmux died, the WebUI rendered `[error:TMUX_SESSION_NOT_FOUND]` repeatedly with no recovery path. Fix adds a `Hint` field to `wsServerMessage` with actionable copy on the error, stops the reconnect loop on that code, and renders a single fatal banner with a "Restart session" button that calls `POST /api/sessions/:id/restart`. Codex peer review caught a missing `reconnectKey` state — added to force terminal re-init after restart. Now reachable end-to-end since web mutations were re-enabled in v1.7.75 (#785).

- **Session Analytics context bar showed wrong percentage for `claude-opus-4-7`** (thanks @AdamiecRadek for diagnosing [issue #836](https://github.com/asheshgoplani/agent-deck/issues/836)). The `Context [bar] N%` gauge rendered ~5x too high for opus-4-7 because the model→context-window prefix table in `internal/session/analytics.go` was missing the 4-7 entry, falling through to the 200K default. Concrete impact: 145K used → bar read 72.6% instead of correct 14.5%. Fix adds `{"claude-opus-4-7", 1000000}` placed before the 4.x fallback (table walk is order-sensitive) and extends `TestContextWindowForModel` with two 4-7 cases. AdamiecRadek's third clean model-spec data catch in this cycle (after #813 pricing and #818 templated cost line).

- **TUI `New Session` dialog had no `copilot` preset; typing `copilot` created a shell session** (thanks @Hristo Dinkov for [PR #835](https://github.com/asheshgoplani/agent-deck/pull/835)). Same class of oversight as `pi` (fixed in v1.7.32 via #674): copilot was added as a first-class tool in v1.7.26 but two TUI call sites were missed. Fix adds `copilot` to both `createSessionTool`'s switch and `buildPresetCommands`' preset list, with regression tests for both.

- **`scheduleBusyRetry`'s success path didn't terminate fingerprint, causing repeated `sent` re-fires** ([#824](https://github.com/asheshgoplani/agent-deck/issues/824) follow-up). Caught during v1.7.74 production verification: a child sitting in `waiting` while parent was busy would defer-retry, eventually succeed, but the queue still re-fired the same fingerprint up to 5 times because v1.7.74's `markTerminated` only ran on exhaustion (give up), not on success. Concrete evidence: child `384aa29c` had 5× `deferred_target_busy` + 5× `sent` records all at the same timestamp. Fix adds 1-line `n.markTerminated(event)` to the success branch + new `TestQueue_SuccessfulRetryMarksTerminated` regression. Codex peer agreed SAFE_TO_MERGE under sibling-topology.

### Added

- **Terminal navigation keys in session list** (thanks @strofimovsky for [PR #815](https://github.com/asheshgoplani/agent-deck/pull/815) → #840). Session list now accepts `Home` / `End` (jump to first / last item) and `PgUp` / `PgDn` (half-page aliases of existing `Ctrl+U` / `Ctrl+D`). Fills a gap where no single-key jump-to-bottom existed since `G` opens global search. `Home` / `End` also scroll the help overlay. Follows the same side-effect contract as the pagination handlers (preview scroll reset, navigation-activity mark, debounced preview fetch). PR was rebased onto current main (CHANGELOG conflict against v1.7.74/75 entries) preserving original authorship.

- **iTerm2 SS3 Home/End fix for direct SSH** (companion to #840). iTerm2's default macOS profile emits Home/End as SS3 application-mode sequences (`ESC OH` / `ESC OF`) on direct SSH. Bubble Tea's decoder covers xterm/vt220/urxvt variants but not SS3 — `csiuReader.translate` now rewrites `ESC OH` → `ESC [H` and `ESC OF` → `ESC [F` before bytes reach Bubble Tea. All other `ESC O*` sequences pass through unchanged. Verified unchanged for iTerm2 → SSH → Screen path.

- **Tailscale recommendation for reaching services in remote sessions** ([PR #832](https://github.com/asheshgoplani/agent-deck/pull/832)). New section in README's Remote Sessions docs explaining why agent-deck does not ship native SSH `-L`/`-R` port forwarding: Tailscale solves the same problem (reach a service on the remote box from your laptop) more robustly with no per-session config and no `ControlMaster` edge cases. Closes the documentation gap left by the maintainer-decline of #800/#792.

## [1.7.75] - 2026-04-30

Community quality-of-life bundle. Four contributor PRs landing the day after the v1.7.74 hotfix: regression fix for web mutations broken since v1.7.71, an SSH start-failure cleanup compensation, an `add` ergonomics fix for SSH-piped paths, and a configurable cost status-line. All four were dual-reviewed (Claude + Codex peer reviewer) before merge — first run of the dual-model review pipeline.

### Fixed

- **Web mutations disabled by default since v1.7.71; restart/delete buttons returned 403/503** (thanks @JMBattista for [issue #781](https://github.com/asheshgoplani/agent-deck/issues/781) → [PR #785](https://github.com/asheshgoplani/agent-deck/pull/785)). Two compounding bugs: `WebMutations` defaulted to `false` in the config struct, and `buildWebServer` never called `SetMutator` so even an explicit-true config did nothing. Fix flips the default back to `true` (matching pre-#519 behavior), wires `ui.NewWebMutator(homeModel)` into the only `buildWebServer` call site in `main.go`, adds `*bool` TOML pattern to distinguish "absent" from "explicit false", and ships 6 regression tests including `TestBuildWebServer_WiresMutator` + `HasMutator()` to lock against re-introduction. `--read-only` CLI flag still forces mutations off; loopback-only listener and existing Token gate unchanged.

- **`agent-deck add` failed on `~` and `$VAR` in positional path arg over SSH** ([issue #820](https://github.com/asheshgoplani/agent-deck/issues/820), thanks @paskal for [PR #821](https://github.com/asheshgoplani/agent-deck/pull/821)). Interactive shells expand `~` before exec, but `ssh user@host "agent-deck add ~"` passes the literal tilde — `filepath.Abs` then treated `~` as a literal directory name. Fix routes the positional path through the existing `session.ExpandPath` helper (correctly orders env-var expansion → tilde expansion). The `.` shortcut still fast-paths to `os.Getwd()` so error semantics on cwd-failure are preserved. Table-driven regression test covers `.`, `~`, `~/foo`, `$HOME`, `$HOME/bar`, absolute, and relative paths.

- **Orphan remote session row when SSH `session start` fails** (thanks @paskal for [PR #822](https://github.com/asheshgoplani/agent-deck/pull/822)). The two-step `add` + `session start` SSH flow could leave a session row in agent-deck's state.db when the start failed (e.g. flaky network), with no way to retry. Fix adds a compensation step in `CreateSession`'s start-failure branch that calls `DeleteSession` with a fresh `context.Background()` + 10s timeout — correct choice since upstream `ctx` cancellation is often *what caused* the start failure. Best-effort (`_ = DeleteSession`) so a still-broken network doesn't surface two errors at once. Two new tests via a `runFn` injection cover both failure and happy paths without restructuring production code.

### Added

- **Configurable status-line cost template** ([#818](https://github.com/asheshgoplani/agent-deck/issues/818) → [#819](https://github.com/asheshgoplani/agent-deck/pull/819), thanks @AdamiecRadek). The home status-bar cost segment is now driven by `[costs].cost_line_template` with optional per-profile override at `[profiles.<name>.costs].cost_line_template`. Seven cost variables supported: `{cost_today}`, `{cost_yesterday}`, `{cost_this_week}`, `{cost_last_week}`, `{cost_this_month}`, `{cost_last_month}`, `{cost_projected}`. Unknown placeholders pass through literally. `cost_line_hide_when_zero` (default true) preserves the prior auto-hide behavior. New `Store.TotalYesterday`, `TotalLastWeek`, `TotalLastMonth` helpers underpin the new variables. 42 new tests cover boundary cases, nil/empty config, profile-vs-global resolution chain, and TOML round-trip.

## [1.7.74] - 2026-04-30

Hotfix bundle for two notify-daemon regressions that surfaced during v1.7.73 production verification on the maintainer host. Both fixes ship together because the SQLite leak masked the dedup behavior — without the leak fix, the daemon wedged before the dedup test could complete.

### Fixed

- **`notify-daemon` leaked one SQLite connection per dispatch + per queue-drain (~34/min, wedges in hours)** ([#827](https://github.com/asheshgoplani/agent-deck/issues/827) → [#828](https://github.com/asheshgoplani/agent-deck/pull/828)). Two call sites in `internal/session/transition_notifier.go` opened a fresh `Storage` via `NewStorageWithProfile` per invocation but never closed it: `prepareDispatch` (every event) and `liveTargetAvailability` (every queue drain). Forensic evidence on the maintainer host: 2h40min uptime daemon held 1117 open FDs to `state.db` plus 1117 to `state.db-wal`, stack stuck in `futex_do_wait` from accumulated WAL/mutex contention, transition log silent for 38 minutes despite live activity. Fix adds `defer storage.Close()` at both sites — 2 lines of source change. Two new `Test*_NoFDLeak` regression tests assert FD count stays flat across N synthetic dispatches (RED on parent: delta=400; GREEN on fix: delta≈0). This is the actual root cause behind several "events stop arriving after a while" reports — the v1.7.73 dedup work in [#807](https://github.com/asheshgoplani/agent-deck/pull/807) reduced the symptom (duplicate spam) but the underlying daemon wedge remained until this fix.

- **Inbox + missed-log emitted duplicate entries; top-level conductor self-suppress missed empty-parent case; exhausted busy-retries re-fired indefinitely** ([#824](https://github.com/asheshgoplani/agent-deck/issues/824) → [#825](https://github.com/asheshgoplani/agent-deck/pull/825)). Three sub-bugs in v1.7.73's [#807](https://github.com/asheshgoplani/agent-deck/pull/807) inbox+retry pipeline, all surfaced by multi-conductor production audit: (1) the same fingerprint (sha256 of child_id|from|to|timestamp_unixnano) could be written 13× to one inbox file, (2) `prepareDispatch`'s self-suppress only matched `parent==self` but real top-level conductors also have empty `parent_session_id` plus a `conductor-` title prefix, so they kept firing `dropped_no_target` events back to themselves, (3) `scheduleBusyRetry`'s exhaustion path logged to `notifier-missed.log` but did not remove the event from the deferred queue, so the queue re-fired the same event indefinitely (notifier-missed.log captured 7 re-fires of the same child in 16 seconds during the audit). Fix introduces an `EventFingerprint` helper, a process-local `missedSeen` dedup map, a `terminatedFingerprints` set with `markTerminated`/`isTerminated`, an early-return on terminated events in `EnqueueDeferred`, exhaustion-path queue eviction in `scheduleBusyRetry`, and a new self-suppress branch in `prepareDispatch` keyed on `child.Title` startsWith `conductor-` AND empty `child.ParentSessionID` (orphan WARN no longer fires for the root). Seven new regression tests, all RED-then-GREEN under strict TDD.

### Added

- **Multi-conductor event-delivery regression harness** ([#826](https://github.com/asheshgoplani/agent-deck/pull/826)). `tests/eval/scripts/multi_conductor_event_delivery_test.sh` plus a Go wrapper at `internal/session/multi_conductor_delivery_test.go` (build-tag-gated `//go:build multi_conductor`) enumerate every conductor on the host (regex `^conductor-|^agent-deck$`), spawn a disposable child in each conductor's group, drive a `running → waiting` transition, then audit `transition-notifier.log` + per-conductor inbox + `notifier-missed.log` for the four contracts from #824: a `delivery_result=sent` record exists, the same fingerprint appears exactly once in the log, the conductor's inbox holds at most one entry per fingerprint, and the missed log holds at most one re-fire entry per child. Output: per-conductor PASS/FAIL markdown report under `tests/eval/reports/`. Skips cleanly when zero conductors are present (CI default). Caught the original `-parent` flag misuse in its own first run.

## [1.7.73] - 2026-04-30

Resilience pass. Nine community-and-internal PRs addressing real user-impacting bugs across event delivery, perf, hooks, headless contexts, defensive timeouts, and pricing accuracy. Five external contributors merged this cycle: @vedantdshetty (5 PRs), @amkopyt, @AdamiecRadek, @strofimovsky, plus internal fixes.

### Fixed

- **Transition-notifier silently dropped 97-98% of child-session events** ([#805](https://github.com/asheshgoplani/agent-deck/issues/805) → [#807](https://github.com/asheshgoplani/agent-deck/pull/807)). Two underlying causes: orphan-on-creation children (empty `parent_session_id` from env-var drop in worktrees, sandboxes, watchdogs) and deferred-busy events that didn't retry reliably. Fix introduces per-conductor inbox file at `~/.agent-deck/inboxes/<parent-session-id>.jsonl`, retry-with-backoff on busy (5s / 15s / 45s), top-level conductor self-suppress, orphan WARN once per child, and a new `agent-deck inbox <session>` CLI subcommand. Co-discovered by conductor-innotrade and conductor-agent-deck independently observing the same forensic picture.

- **`UpdateStatus` held `i.mu.Lock()` across the opencode CLI subprocess, freezing the TUI 10-15s during navigation** (thanks @strofimovsky for [PR #801](https://github.com/asheshgoplani/agent-deck/pull/801)). Write-preferring `sync.RWMutex` starved render-path RLocks while a multi-second `opencode session list` ran under the held lock. Fix releases `i.mu` around the call (callee self-manages), bounds the subprocess with a 5s context deadline plus `cmd.WaitDelay = 500ms` so a Node-style child holding stdout open can't extend `cmd.Output()` past the deadline. Verified with a 60s navigation harness against 28 opencode sessions: lock-held went from 5-7s per event to 0ms across 21/21 events.

- **`PermissionRequest` hook silently denied filesystem operations in `/remote-control` and other headless contexts** (thanks @vedantdshetty for [PR #808](https://github.com/asheshgoplani/agent-deck/pull/808)). agent-deck registered the hook as `Async: true` but the handler is a status tracker, not a permission decider. In TUI sessions Claude Code's UI prompts the user; in headless contexts there's no UI fallback, so silence defaulted to deny — `ls /mnt/c/...` and other filesystem operations failed with no surfaced prompt. Fix flips `PermissionRequest` to `Async: false` so Claude Code consults the hook's stdout for a decision, and emits an explicit `permissionDecision: allow` when the parent process was launched with `--dangerously-skip-permissions` (DSP, the canonical signal of pre-authorized headless work).

- **`tmuxExec` and `tmuxExecContext` could hang indefinitely on orphaned stdio** (thanks @vedantdshetty for [PR #809](https://github.com/asheshgoplani/agent-deck/pull/809)). Without `cmd.WaitDelay`, a tmux subprocess that orphans stdio causes Go's `cmd.Output()` I/O goroutines to block forever. Codebase already used `WaitDelay` in `internal/git/setup.go`, `internal/mcppool/socket_proxy.go`, and `internal/mcppool/http_server.go`; this was the missed wrapper at the tmux boundary. Defensive fix.

- **`queryCodexSession` blocked indefinitely when the FS layer stalled** (thanks @vedantdshetty for [PR #810](https://github.com/asheshgoplani/agent-deck/pull/810)). `filepath.WalkDir` over `~/.codex/sessions` blocked indefinitely when the underlying FS layer stalled — observed 2026-04-28 with a WSL kernel D-state on a stuck dentry (one thread held a fd whose dentry sat in `d_alloc_parallel`). Every `agent-deck` CLI command that transitively called the function hung along with it. Fix wraps the walk in a 5s context deadline via a small `runWithTimeout` helper; on timeout, log WARN and return empty.

- **Hook config could become stale on agent-deck binary upgrade** (thanks @vedantdshetty for [PR #811](https://github.com/asheshgoplani/agent-deck/pull/811)). `hooksAlreadyInstalled` only checked command presence, not the `Async` and `Matcher` fields, so a binary upgrade that flipped `Async` (as #808 did) would leave the user's `settings.json` stuck on the old broken config. Fix verifies the full hook record against `hookEventConfigs` (the source of truth in code) and updates on mismatch. Without this follow-up, #808 only reaches fresh installs.

- **`tool_data` SQLite column silently wiped manually-set keys on save** (thanks @vedantdshetty for [PR #817](https://github.com/asheshgoplani/agent-deck/pull/817)). The save path `INSERT OR REPLACE`d `tool_data` wholesale from the typed Go schema, dropping any keys not modeled by `toolDataBlob` (canonical case: `clear_on_compact`, set via direct SQLite UPDATE per harness convention). Surfaced when hub-orch sessions kept re-firing `/clear-on-compact`. Fix preserves unknown keys via a read-before-write merge in `SaveInstance` and `SaveInstances`; pre-fetch happens outside the write transaction in the batch path to avoid SQLite WAL contention.

- **Bare ESC keypress lost in tmux attach quarantine; ESC followed by arrow arrived as Alt+Up** (thanks @amkopyt for [PR #812](https://github.com/asheshgoplani/agent-deck/pull/812)). `internal/termreply/filter.go` set `pendingEsc = true` on ESC and emitted nothing, waiting for the next byte to disambiguate CSI / SS3 / OSC. Real keyboard ESC has no follow-up byte, so the press stayed buffered indefinitely and later concatenated with the next keystroke's encoding. User-visible symptoms in Claude Code: bare ESC (interrupt) didn't fire, ESC ESC (jump-to-previous-message) didn't work, arrow keys appeared to reset the input. Fix flushes the lone ESC after a short timeout so it reaches the inner agent.

- **Outdated Anthropic pricing data + missing entry for `claude-opus-4-7`** (thanks @AdamiecRadek for [issue #813](https://github.com/asheshgoplani/agent-deck/issues/813) → [PR #814](https://github.com/asheshgoplani/agent-deck/pull/814)). `claude-opus-4-6` was using legacy Opus 4 / 4.1 rates (3× the actual current rate, over-attributing every Opus 4.6 token), `claude-haiku-4-5` was at 80% of the published rates, and `claude-opus-4-7` was missing entirely (1240+ cost-event rows in the wild persisted at $0). Cost dashboard accuracy was wrong in both directions. Fix corrects all three plus adds a new `agent-deck costs recompute` CLI subcommand that recalculates `cost_microdollars` for every `cost_events` row using current pricing data (idempotent; supports `--dry-run`).
### Added

- **Terminal navigation keys in session list.** Session list now accepts
  `Home` / `End` (jump to first / last item) and `PgUp` / `PgDn` (half-page
  aliases of existing `Ctrl+U` / `Ctrl+D`). Fills a gap where no single-key
  jump-to-bottom existed, since `G` opens global search. `Home` / `End` also
  scroll the help overlay. Follows the same side-effect contract as the
  pagination handlers (preview scroll reset, navigation-activity mark,
  debounced preview fetch).

### Fixed

- **Home/End keys in TUI now work for iTerm2 over direct SSH.** iTerm2's
  default macOS profile emits Home/End as SS3 application-mode sequences
  (`ESC OH` / `ESC OF`) on direct SSH (no intermediate tmux or screen).
  Bubble Tea's decoder covers the xterm, vt220, and urxvt Home/End
  variants but not SS3 — `csiuReader.translate` now rewrites `ESC OH` to
  `ESC [H` and `ESC OF` to `ESC [F` before bytes reach Bubble Tea. All
  other `ESC O*` sequences pass through unchanged. Verified unchanged
  for iTerm2 → SSH → Screen (already emitted vt220 `ESC [1~` / `ESC [4~`).

## [1.7.72] - 2026-04-28

Bundle of fixes and contributor PRs, hours after v1.7.71. Two external contributors merged this cycle: @tarekrached (twice), @oryaacov.

### Fixed

- **Worktree-setup script honors shebang** ([#773](https://github.com/asheshgoplani/agent-deck/issues/773), thanks @Clindbergh for the report). Setup script with executable bit + shebang line (e.g. `#!/usr/bin/env zsh`) now runs under the declared interpreter. Legacy 0644 files fall back to `sh -e` for backward compatibility.

- **Setup script visible completion + failure status** ([#768](https://github.com/asheshgoplani/agent-deck/issues/768)). Adds visible "completed in <elapsed>" / "failed after <elapsed>" lines around the existing setup-script preamble so users know if their hook ran successfully before claude takes over.

- **`ControlPipe.Close()` softened to SIGTERM+grace** ([#739](https://github.com/asheshgoplani/agent-deck/issues/739) gap, thanks @tarekrached for PR #778). Mirrors the v1.7.68 `softKillProcess` pattern for the active-pipe close path. Prevents the same kill-cascade class for users whose terminals trigger control-pipe lifecycle quickly.

### Added

- **Copy preview pane info via `C` / `Shift+C`** ([#791](https://github.com/asheshgoplani/agent-deck/issues/791)). Yank Repo / Path / Branch from the right-pane preview using the existing clipboard fallback chain (native + OSC 52 for SSH'd terminals).

- **Native iTerm2 badge sync on attach + rename** (thanks @tarekrached for PR #777). Three-gate no-op design correctly contains escape sequences; default opt-in; thorough tests. Gracefully no-ops on non-iTerm2 terminals.

- **Arrow-key navigation for path suggestions in new-session dialog** (thanks @oryaacov for PR #772). Adds keyboard-only path picker with custom-path entry alongside the existing typed input.

## [1.7.71] - 2026-04-28

Single-issue hotfix-class release. One day after v1.7.70.

### Fixed

- **`session set-parent` no longer silently moves the child's group** ([#786](https://github.com/asheshgoplani/agent-deck/issues/786)). Until now, post-hoc linking a session under a parent rewrote the child's `group` field to match the parent's, while `unset-parent` only cleared `parent_session_id` — an asymmetric footgun for the retroactive-relink workflow (re-attaching orphan sessions to a conductor for event routing) which silently scrambled the TUI tree and lost the original group with no audit trail. `set-parent` is now strictly parent-only by default. Use `--inherit-group` to opt back in to the prior behavior. Implicit group inheritance for *newly launched* sessions via `add` / `launch` is unchanged. Locked in by five regression tests in `cmd/agent-deck/setparent_group_test.go` (no-inherit default, opt-in works, unset leaves group alone, full round-trip preserves group, --help mentions the flag).

## [1.7.70] - 2026-04-27

Bundle of community-contributed fixes plus a P1 macOS regression repair, four days after v1.7.69. Three external contributors merged this cycle: @lucassaldanha, @vedantdshetty, @amkopyt. Plus @petitcl's remote-docs PR.

### Fixed

- **P1 — Worker-scratch `CLAUDE_CONFIG_DIR` no longer breaks per-group `config_dir` on hosts with no Telegram conductor** ([#759](https://github.com/asheshgoplani/agent-deck/issues/759), thanks @lucassaldanha for PR #760). v1.7.68's #732 added a worker-scratch indirection that fired regardless of whether a Telegram conductor was actually configured. On macOS, where Claude Code keys OAuth credentials by literal CLAUDE_CONFIG_DIR path, this silently broke per-group account isolation — sessions fell back to default `~/.claude` instead of configured per-group dirs. Fix narrows the predicate to additionally require an active Telegram conductor token; #732's protection unchanged on hosts that need it. Closes the regression godlen4332 hit at #766.

- **Codex `resume <sid>` death loop after a stale rollout** ([#756](https://github.com/asheshgoplani/agent-deck/issues/756), thanks @vedantdshetty for PR #758). When a Codex process died before its session rollout JSONL flushed (tmux crash, kill in the SessionStart→first-flush window), the captured session_id was permanently unresumable. agent-deck's spawn path appended `codex resume <stale-uuid>` on every restart, Codex exited immediately, infinite loop. `buildCodexCommand` now globs `$CODEX_HOME/sessions/*/*/*/rollout-*-<sid>.jsonl` before adding the resume argv; on miss, it logs `codex_resume_stale_sid_dropped`, clears in-memory state, clears the `.sid` sidecar — self-heals on the first restart.

- **Setup script not running when worktree creation uses `.bare` repo path** ([#742](https://github.com/asheshgoplani/agent-deck/issues/742), @Clindbergh). Three TUI sites in `internal/ui/home.go` (new-session-with-worktree, fork-with-worktree, multi-repo new-session) used the narrow `git.IsGitRepo()` check; #715's bare-repo support requires `git.IsGitRepoOrBareProjectRoot()`. Drop-in swap at all three. Structural test `TestRegression742_HomeWorktreeGuardsAcceptBareProjectRoot` grep-asserts `home.go` contains zero uses of the narrow check.

- **`Start query` field in new-session dialog no longer prefills with previous invocation's value** ([#741](https://github.com/asheshgoplani/agent-deck/issues/741), @Clindbergh). TUI state leak: `ShowInGroup` cleared every input except `startQueryInput` — the new field added for #725 was missed in the clear loop. Fix adds `ClaudeOptionsPanel.ResetStartQuery()` next to `claudeOptions.Blur()`.

- **Sessions on isolated tmux sockets are no longer permanently reported as `error`** ([#755](https://github.com/asheshgoplani/agent-deck/issues/755), @vedantdshetty). When a user configured `[tmux].socket_name` (or per-conductor sockets), any session living on that non-default socket showed up as `error` in `agent-deck session show --json`, `list --json`, `status --json`, the TUI status column, and the web dashboard — and a manual `UPDATE instances SET status='waiting'` in SQLite was overwritten on the very next poll. The reviver path (added with v1.7.50 socket-isolation work) was already socket-aware via `tmux.HasSessionOnSocket`, but the status-derive path wasn't: `Session.Exists()` short-circuits on a process-wide cache populated by `RefreshSessionCache`, which only queries `DefaultSocketName()`. A session whose `SocketName` differs from the default was either absent from the cache (false negative → `StatusError`) or aliased with a same-named default-socket session (false positive). Fix gates the cache lookup on `strings.TrimSpace(s.SocketName) == DefaultSocketName()` in `internal/tmux/tmux.go:Exists`; mismatched-socket sessions fall through to the existing socket-aware `s.tmuxCmd("has-session", -t, name)` direct probe (which already injects `-L <name>` via `Session.tmuxCmd`). One-line gate, no new abstractions; the cache fast path is preserved verbatim for the default-socket path so the per-tick subprocess-cost reduction from the cache (its original purpose) is unchanged. Two RED-first regression tests in `internal/tmux/exists_socket_test.go`: `TestSession_Exists_DoesNotTrustDefaultCacheForNonDefaultSocket` (the false-positive path, runs without a real tmux server) and `TestSession_Exists_DefaultSocketStillUsesCache` (pins the backwards-compat fast path so future changes can't degrade default-socket sessions to a fresh subprocess per `Exists()` call).

### Added

- **Per-session Claude Code plugin attach via `--plugin <name>`** (RFC `docs/rfc/PLUGIN_ATTACH.md`). New CLI flag on `agent-deck add` and `agent-deck launch` enables a Claude Code plugin from a curated catalog (`[plugins.<name>]` in `~/.agent-deck/config.toml`) for one session only — without contaminating the global `~/.claude/settings.json`. Catalog entries declare `name`, `source` (e.g. `nyldn/claude-octopus` or `claude-plugins-official`), optional `emits_channel` (auto-link to inbound delivery via `--channels`), `auto_install` (shell-out to `claude plugin install <id>` if missing), and `description`. The new field `Instance.Plugins []string` persists catalog short names and round-trips through state.db (`statedb.MarshalToolData`/`UnmarshalToolData`), so a session restart re-applies enabledPlugins on the next spawn. Six surfaces wired together: (1) catalog `PluginDef` in `internal/session/userconfig.go` with `GetAvailablePlugins`/`Names`/`Def` accessors filtering the v1-refused `telegram@claude-plugins-official` (§6); (2) writer extension in `internal/session/worker_scratch.go` generalizing the v1.7.68 telegram-only mutation into a deny+allow overlay (allow wins on key collision per RFC §4.3) plus a new `needsScratchForExplicitPlugins` gate so plugin-driven scratches fire on hosts without a TG conductor while preserving the issue #759 macOS narrowing for non-plugin sessions; (3) mutator branch `FieldPlugins` (claude-only, restart-required, catalog-validated) in `internal/session/mutators.go` with usage `agent-deck session set <id> plugins <csv>`; (4) auto-install in `internal/session/plugin_install.go` with per-(source, name) flock under `~/.agent-deck/locks/`, idempotent `<source>/plugins/<source>/<name>/` existence check, best-effort `claude plugin marketplace add` + `claude plugin install` running against the **source** profile (not scratch) so installs are global per profile while enablement stays per-session; (5) channel auto-link in `internal/session/plugin_channels.go` — catalog entries with `emits_channel = true` automatically populate `Instance.Channels` with `plugin:<id>` so claude registers the inbound `notifications/claude/channel` handler (without it the plugin loads as a plain MCP and silently drops inbound messages); opt-out via `--no-channel-link` flag persisted as `Instance.PluginChannelLinkDisabled`; (6) Edit Session dialog field for live runtime edits (CSV text input matching the ExtraArgs shape; full multi-checkbox widget deferred to v1.1). v1 explicitly refuses `--plugin telegram@claude-plugins-official` at three layers (CLI flag validator, mutator, catalog accessors) with a pointer to `--channels` — full Telegram retrofit onto the deny-list-minus-opt-ins machinery is deferred to a separate `PLUGIN_TELEGRAM_RETROFIT.md` RFC. macOS `CLAUDE_CONFIG_DIR`-keyed OAuth (#759) gets a one-shot loud warning per source profile via `~/.agent-deck/macos-plugin-warning-state.json`. Tests: 9 catalog round-trip + persistence + accessors; 13 worker-scratch deny+allow + macOS warning; 8 mutator branch + telegram refusal + restart policy; 7 auto-install with stubbed exec + lock semantics; 6 channel auto-link idempotency + add/remove + `--no-channel-link`; 7 CLI validation; 5 Edit Session dialog visibility + initial value; +1 `TestMarshalUnmarshalToolData_Plugins` state.db round-trip — 56 new tests, all green.

### Changed

- **Edit Session dialog redesigned to match New Session conventions, with auto-restart on save.** Follow-up to the in-TUI editor below. Title-locked / no-transition-notify / wrapper / channels / color / notes / command are dropped from the dialog and stay settable via `agent-deck session set <field>` — the dialog focuses on the values users actually iterate on at runtime. The slim set: Title (live), Tool (←/→ pill picker matching `New Session`'s preset list), plus three claude-only fields (Skip permissions / Auto mode checkboxes + Extra args text input). Skip / Auto surface and persist `ClaudeOptions.{SkipPermissions, AutoMode}` from `Instance.ToolOptionsJSON`, which previously had no edit surface outside the new-session options panel. Two new SetField branches (`FieldSkipPermissions`, `FieldAutoMode`) round-trip those bools through the JSON blob via `UnmarshalClaudeOptions`/`MarshalToolOptions`, initialize an empty wrapper for legacy sessions whose `ToolOptionsJSON` is nil, and reject the fields on non-claude tools. The previous "saved — press R to restart" hint is replaced by **auto-restart on Enter**: when any restart-required field changes (Tool / Skip / Auto / Extra args) the dialog handler now calls `h.restartSession(inst)` directly, mirroring the `R` keybind. Auto-restart is suppressed when an animation is already in flight (`hasActiveAnimation` — concurrent restart would race) or when the session can't be restarted (`!CanRestart()` — stopped sessions just persist edits and apply on next manual start). The `FieldTool` branch in `SetField` also clears stale `ClaudeOptions` from `ToolOptionsJSON` when leaving a claude-compatible tool, so a `claude→shell` switch with Skip toggled in the same submit can't resurrect ghost flags on a future `shell→claude` switch (`TestSetField_Tool_ClearsClaudeOptionsOnLeaveClaude`). Header now reads `Edit Session` + `in group: <name>` (purple) + `session: <title>` (dim) for visual parity with the new-session dialog. Checkbox rendering uses the shared `renderCheckboxLine` helper so the row reads `▶ [x] Skip permissions` like the New Session options panel. The stale-custom-tool footgun caught by Sonnet code review (custom tool removed from config → cursor lands on slot 0 → save without edit silently rewrites Tool to "") is fixed by `toolPillsForInstance` appending the unknown tool to the pill list and pinning the cursor on it; regression test `NoSpuriousToolWipeForStaleCustom` locks the no-op contract. Other tests pinning the contract: `TestEditSessionDialog_GetChanges_{SkipPermissionsToggle,AutoModeToggle}`, `TestEditSessionDialog_NoClaudeFlagsForShellTool`, `TestSetField_SkipPermissions_{InitializesEmptyToolOptions,ClaudeOnly}`, `TestSetField_AutoMode_PreservesSkipPermissions`, `TestSetField_Tool_NoopForSameClaude`.

### Added

- **In-TUI editor for session settings (`P` / `Shift+P` hotkey).** [as below — same entry preserved]
- **Vulnerable-tmux startup warning** (S14 follow-up, #750). Prints a one-line stderr warning at agent-deck startup when the running tmux's version predates the upstream NULL-deref fix (commits `881bec95`, `e5a2a25f`, `31c93c48`). Suppressible via `AGENTDECK_SUPPRESS_TMUX_WARNING=1`. Helps users on macOS know to upgrade once Homebrew ships a patched tmux.
- **Remote subcommand fully documented** in README, `--help`, and CLI reference (#751, thanks @petitcl for PR #752). Was previously only accessible to users who discovered it accidentally.

### Chore

- **`.planning/` directory removed entirely** (#763). 118 files of internal maintainer scratch space (milestones, roadmap, retrospective, per-phase plans). Already gitignored per #740, this cleans up the existing tracked files. No code impact.
- **Documentation note** clarifying that `[tmux].socket_name` isolation does NOT prevent agent-deck-on-its-own-server crashes from the upstream tmux NULL-deref (S14 follow-up). README updated.

#### Edit Session dialog (full entry)

- **In-TUI editor for session settings (`P` / `Shift+P` hotkey).** Adds a Bubble Tea dialog that lets users edit a running session's title, color, notes, command, wrapper, tool, channels, extra-args, title-locked, and no-transition-notify in place — previously this required dropping out of the TUI to run `agent-deck session set <id> <field> <value>` per field, and the boolean fields (`title-locked`, `no-transition-notify`) had no in-TUI surface at all. Live fields (title / color / notes / booleans) take effect immediately on save; restart-required fields (command / wrapper / tool / channels / extra-args) persist and apply on next `R`, surfaced via a transient `saved — press R to restart` hint. The implementation extracts per-field validation + tmux side effects into `session.SetField` (`internal/session/mutators.go`) so CLI and TUI share one source of truth — the prior `agent-deck session set` switch is now a 4-line delegator. `SetField` returns a `postCommit func()` for the two fields that need a slow tmux subprocess (`claude-session-id` / `gemini-session-id` env propagation) so the TUI can drop `instancesMu` before invoking it; the dialog doesn't currently expose those fields, but the API stays defensive against future additions. Race-safety: title edits flow through `pendingTitleChanges` + `invalidatePreviewCache` and persist via `forceSaveInstances` (not `saveInstances`, which is a no-op while `isReloading=true`), mirroring the existing rename-path `#697` mitigation. Tool changes apply last in the commit loop so claude-only field validation (`channels`, `extra-args`) sees the pre-edit `Tool` value — without this, switching `Tool=claude→shell` while clearing `Channels` in one submit would error spuriously on the clear. Tests: 15 `TestSetField_*` unit tests including post-commit nilness invariants, 18 dialog unit tests, 4 eval_smoke cases per the `CLAUDE.md:82-108` mandate for interactive prompts (`internal/ui/edit_session_dialog_eval_test.go`), and 4 regression tests + 6 sub-tests for the v1.7.22 / #658 telegram-topology warnings — `maybeEmitSessionSetTelegramWarnings` was extracted from inline so the conditional is testable, since the gate's regression nearly slipped during the SetField extraction (caught by Sonnet code-review pass before commit). Help overlay (`?`) updated to surface the new hotkey under SESSIONS.

## [1.7.69] - 2026-04-24

Hotfix bundle for five regressions filed against v1.7.68 within 24h of release. Every fix ships with a RED-first regression test; one fix (#744) had to be dropped as not-our-bug after systematic investigation — filter-level passage is verified and guarded, but the reported Shift-to-lowercase behavior lives downstream of agent-deck and needs a repro bundle from the reporter before any shipping change. Per the v1.7.68 maintainer review, release is deliberately un-tagged in this commit — the user tags when ready.

### Fixed

- **TUI `n` key no longer creates a local session when the cursor is on a remote group/session** ([#743](https://github.com/asheshgoplani/agent-deck/issues/743), @javierciccarelli). v1.7.68 shipped d9a5de8 ("fix(ui): keep new session on n for remote selections") which deliberately removed the remote early-return from `case "n":` in `internal/ui/home.go`, intending to route everyone through the local new-session dialog. But the dialog has no remote awareness, so users in the Remotes section who pressed `n`, accepted the dialog defaults, and got a session created on localhost instead of on the remote they were browsing. Fix reinstates the pre-d9a5de8 branch verbatim: if the cursor is on `ItemTypeRemoteGroup` / `ItemTypeRemoteSession`, route into `createRemoteSession(item.RemoteName)` and skip the local dialog entirely. `case "N":` kept unchanged — both keys now quick-create on remotes, matching the pre-v1.7.68 UX. The two d9a5de8 regression tests (which codified the broken contract) are deleted with comments pointing at the new guards `TestRegression743_NOnRemoteSession_QuickCreatesNoDialog` and `TestRegression743_NOnRemoteGroup_QuickCreatesNoDialog` in `internal/ui/home_test.go`.

- **TUI worktree creation now accepts bare-repo project roots** ([#742](https://github.com/asheshgoplani/agent-deck/issues/742), @Clindbergh). #715 (v1.7.58) introduced `git.IsGitRepoOrBareProjectRoot` and migrated every CLI worktree-creation call site (launch / add / session add / worktree list) to the broader check so a bare-repo project root (a directory containing nested `.bare/` but no `.git/`) flows transparently through worktree flows. Three TUI sites in `internal/ui/home.go` were missed: the new-session-with-worktree guard (~5100), the fork-with-worktree guard (~7379), and the multi-repo new-session guard (~7762). For a bare project root, the first two error out with "Path is not a git repository" (no worktree, no session). The third silently falls through to `os.Symlink`, skipping `git.CreateWorktree` AND the setup script at `<projectRoot>/.agent-deck/worktree-setup.sh` — exactly the "setup script not run; non-bare path works" symptom. Fix: drop-in swap to `git.IsGitRepoOrBareProjectRoot` at all three sites. Downstream `GetWorktreeBaseRoot` + `CreateWorktreeWithSetup` already handle bare layouts (proven by the unchanged `internal/git/bare_repo_test.go` suite). Structural regression guard: `TestRegression742_HomeWorktreeGuardsAcceptBareProjectRoot` in `internal/ui/bare_repo_worktree_guards_test.go` grep-asserts that `home.go` contains zero uses of the narrow `git.IsGitRepo(` — any future worktree site that sneaks in the narrow check fails at test time instead of at user-report time.

- **Forked Claude sessions no longer start empty — fork command survives the Start() resume dispatch** ([#745](https://github.com/asheshgoplani/agent-deck/issues/745), @petitcl). `Instance.Start()` and `Instance.StartWithMessage()` rebuild the claude-compatible command unconditionally based on `i.ClaudeSessionID`: non-empty → `buildClaudeResumeCommand`, empty → `buildClaudeCommand(i.Command)`. A fork target hit the worst case for this dispatch — `buildClaudeForkCommandForTarget` pre-generates a new UUID, assigns it to `target.ClaudeSessionID` for later tracking, and stashes the real fork command (`claude --session-id <new> --resume <parent-id> --fork-session …`) in `target.Command`. Start() sees a populated ClaudeSessionID, routes to `buildClaudeResumeCommand`, which calls `sessionHasConversationData` for the brand-new fork UUID, finds no JSONL on disk (it was supposed to be created by the fork command), and falls back to a plain `--session-id <forkUUID>` — stripping `--resume <parent-id>` / `--fork-session` and dropping all conversation history from the parent. Fix introduces a transient `Instance.IsForkAwaitingStart` field (tagged `json:"-"` so a restart does NOT re-emit `--fork-session` and double-count the parent transcript). `CreateForkedInstanceWithOptions` sets the flag alongside `i.Command = <fork cmd>`. `Start` and `StartWithMessage` check the flag as the FIRST branch inside the claude-compatible switch, run `i.Command` verbatim, clear the flag, and emit a grep-auditable `"resume: none reason=fork_awaiting_start"` session log line. The first-Start-only semantic is load-bearing: a subsequent Restart of the forked session takes the normal resume path (with the persisted ClaudeSessionID now pointing at a real JSONL). Regression coverage via reflection + source probe in `internal/session/fork_start_dispatch_test.go`: `TestRegression745_ForkTargetCarriesAwaitingStartSentinel` asserts four contracts — fork command structure, sentinel presence, `json:"-"` tag, and that `Start()` consults the sentinel BEFORE `buildClaudeResumeCommand`.

- **`agent-deck --select <id>` now survives the storage-watcher auto-reload after `launch --json`** ([#746](https://github.com/asheshgoplani/agent-deck/issues/746), @tarekrached). Classic timing race: `launch --json` writes the new session to the registry and prints its ID on stdout. TUI is invoked with `--select <id>`; the first `loadSessionsMsg` fires before the storage watcher has observed the new file, so `applyInitialSelection` scans `flatItems`, doesn't find the target, returns false. `pendingCursorRestore` restores the previously-persisted cursor to an adjacent row. The storage watcher eventually notices the mtime bump and enqueues a second `loadSessionsMsg` with `restoreState` populated — but the handler's restoreState branch called `h.restoreState(*msg.restoreState)` and returned without re-attempting `applyInitialSelection`. The cursor stayed on the adjacent row forever. Fix: add `h.applyInitialSelection()` to the post-rebuild restoreState branch in the `loadSessionsMsg` handler, mirroring the existing call in the initial-load branch. The helper is already idempotent (no-ops after a successful match), so normal cursor navigation is not overridden. Regression coverage in `internal/ui/initial_select_retry_test.go`: `TestRegression746_InitialSelectRetriesOnNextLoad` (behavioral — helper is idempotent across flatItems rebuilds), and `TestRegression746_LoadSessionsHandlerRetriesInBothBranches` (structural — grep-asserts the post-rebuild `if msg.restoreState != nil { h.restoreState(...)` block contains `applyInitialSelection`, anchored precisely so the pre-rebuild re-capture block at the top of the case can't be matched by accident).

### Investigated — not our bug

- **Shift produces lowercase in remote tmux-split pane on Ghostty/SSH** ([#744](https://github.com/asheshgoplani/agent-deck/issues/744), @javierciccarelli) — **BLOCKED / NOT OUR BUG**. Hypothesis: the #734 (v1.7.68) termreply whitelist broke CSI u passage. Investigation (`internal/termreply/filter_test.go::TestRegression744_FilterPassesShiftLetterCSIUWhileArmed`) proves the filter passes every Shift+letter CSI u encoding tested — xterm `\x1b[65;2u`, kitty `\x1b[97;2u`, Shift+Z `\x1b[90;2u` — unchanged, both as a single chunk AND split across two `Consume` calls while the filter is armed. Final byte `'u'` is correctly whitelisted in `isKeyboardCSIFinalByte` (line 94); `#734`'s DA/DSR additions do not affect the keyboard path. The bug is downstream of agent-deck — either (a) Ghostty/tmux modifyOtherKeys negotiation on the remote host, or (b) a Ghostty/tmux combo that sends a different encoding than what we tested. Without a repro bundle (tmux version, tmux.conf, Ghostty terminfo, actual bytes on the wire) from the reporter, shipping a filter change would be speculative — the exact anti-pattern the v1.7.68 maintainer review called out. The test is kept as a proactive guard against any future whitelist tightening.

## [1.7.68] - 2026-04-22

### Changed
- **`[worktree].setup_timeout_seconds = 0` now means "unlimited" instead of "use default"** (follow-up to [#727](https://github.com/asheshgoplani/agent-deck/pull/727), PR review comment from @Clindbergh). v1.7.65 treated a non-positive value as a signal to fall back to the 60s default. Flipped within 2 days of v1.7.65 shipping, before real adoption. New semantic: `0` = unlimited (no deadline), unset/negative = 60s default, positive N = N seconds. Implementation swaps `WorktreeSettings.SetupTimeoutSeconds` from `int` to `*int` so TOML parsing distinguishes "field unset" (nil) from "explicit zero" (`*0`); `git.RunWorktreeSetupScript` routes unlimited through `context.WithCancel(context.Background())`. Tests in `internal/session/worktree_setup_timeout_zero_unlimited_test.go` and `internal/git/setup_unlimited_test.go`.

### Fixed
- **Rogue telegram pollers no longer spawn when a conductor launches non-conductor claude children on the same host** ([#59](https://github.com/asheshgoplani/agent-deck/issues/59); poller-storm observed 2026-04-22, 6–8 duplicate `bun telegram` processes running concurrently against the conductor's bot token, producing a Bot API 409 Conflict storm and silently dropping inbound messages). v1.7.40 tried to solve this by stripping `TELEGRAM_STATE_DIR` from every non-conductor spawn, but the Claude Code telegram plugin is enabled globally per the v3 topology, so removing `TELEGRAM_STATE_DIR` just makes the plugin fall back to the default state dir — which on a conductor host is the real bot-token directory. v1.7.68 adds a categorically different layer: every non-conductor claude worker now spawns under an ephemeral scratch `CLAUDE_CONFIG_DIR` that shallow-mirrors the ambient profile except for `settings.json`, which is rewritten with `enabledPlugins["telegram@claude-plugins-official"] = false`. The plugin never loads, so no opportunity to discover the default state dir. Wired through `Instance.WorkerScratchConfigDir`, `EnsureWorkerScratchConfigDir`, `prepareWorkerScratchConfigDirForSpawn` at all three spawn paths, and cleaned up on `Kill`/`KillAndWait`. 6 new tests in `internal/session/worker_scratch_test.go`.

- **Orphan claude processes no longer survive `agent-deck session remove`** (same incident 2026-04-22: `PID 321456`, 33-hour orphan). Two distinct bugs: (1) `handleSessionRemove` only called `inst.Kill()` when `--prune-worktree` was passed — plain `session remove --force` deleted the registry row and left the tmux scope + claude child alive forever; (2) `Session.Kill` runs its SIGTERM→SIGKILL escalation in a background goroutine that is aborted when the short-lived CLI exits, so even callers that did invoke `Kill` could race the CLI exit and leave SIGHUP-immune claude 2.1.27+ children alive. Fixes: new `internal/tmux/ensure_pids_dead.go` exports `EnsurePIDsDead` (synchronous SIGTERM→SIGKILL primitive) and `Session.KillAndWait()`; `Instance.KillAndWait()` factors shared teardown through `killInternal(sync bool)`; `handleSessionRemove`, `removeAllErrored`, `pruneSessionWorktree`, and legacy `handleRemove` all now call `KillAndWait` **unconditionally** before `storage.DeleteInstance`. 3 new tests including structural guards that parse the command source and assert unconditional-call invariants.

- **iTerm2 XTVERSION response leak + Shift+Enter regression** ([#731](https://github.com/asheshgoplani/agent-deck/issues/731) @marekaf, [#738](https://github.com/asheshgoplani/agent-deck/issues/738) @Clean-Cole — same filter, two failure modes). `internal/termreply.Filter` now whitelists DA/DSR CSI replies (final bytes `c`, `n`, `R`) so tmux can negotiate modifyOtherKeys with the host terminal, while DCS/OSC escape-string replies (XTVERSION, OSC 10/11) are stripped unconditionally since they'd corrupt the inner pane. Previously the filter either swallowed everything during the 2s quarantine (breaking modifyOtherKeys → Shift+Enter collapsed to bare CR in iTerm2 default profile) or let DCS through after the window (leaking `TERM2 3.6.10n` as input on focus/resize). One surgical whitelist fixes both.

- **macOS tmux server SIGSEGV triggered by `killStaleControlClients`** ([#737](https://github.com/asheshgoplani/agent-deck/issues/737) @tarekrached). Soft kill via SIGTERM + 500ms grace → SIGKILL fallback replaces the prior immediate SIGKILL. Shrinks the race window against an unfixed tmux NULL-deref in the control-mode notify path (tmux/tmux#4916, #4980, #5004 — fixed on master, not in any release tag yet). Stuck clients still reaped within ~500ms, preserving the "stale control clients cannot linger" guarantee.

### Added
- **`[tmux].mouse` config option** ([#730](https://github.com/asheshgoplani/agent-deck/issues/730) @sghiassy) — default `true` (preserves current behaviour). With `mouse = false`, agent-deck skips `set-option mouse on` at both session-create and the reconnect/attach `EnableMouseMode` paths. Restores native click-drag text selection in VS Code's Linux integrated terminal.
- **`scripts/watchdog/` promoted to repo-wide** (internal #53/#56). Includes telegram-poller liveness check (auto-restart conductor on missing bun poller) + waiting-too-long patrol (auto-nudge idle children). 24 new Python tests.

## [1.7.67] - 2026-04-22

### Added
- **Dedicated "Start query" field in the new-session TUI dialog** ([#725](https://github.com/asheshgoplani/agent-deck/issues/725), reported by @Clindbergh): `claude-code` accepts a positional startup-query argument (e.g. `claude "explain this repo"`) which seeds the first prompt for a brand-new session. Before v1.7.67 there was no first-class way to pass this through agent-deck, so users reached for the existing "Extra args" field. That produced two interlocking bugs: (a) **space-splitting** — Extra args runs `strings.Fields` on the raw input, so a multi-word query like `explain the codebase` became three separate tokens and claude saw three positional args instead of one prompt; (b) **cross-session replay** — Extra args is persisted on the `Instance` and re-emitted by `buildClaudeExtraFlags` on every `Start`, `Resume`, `Restart`, and fork, so a value intended as a one-time opening prompt kept auto-suggesting itself each time the session came back up. Fix introduces a new `Start query:` textinput in the Claude Options panel (`internal/ui/claudeoptions.go`) wired through `NewDialog.GetClaudeStartQuery() → Instance.StartupQuery` and appended by `buildClaudeCommandWithMessage` as a single `shellescape.Quote`-wrapped positional token on the new-session command only. The field is declared `StartupQuery string `json:"-"`` on the `Instance` struct — the `json:"-"` tag is load-bearing, it is what makes the value **per-session and never persisted**. On `Restart` / `Resume` the field is zero-valued after the SQLite reload so `buildClaudeResumeCommand` never sees it and the query is not replayed. "Extra args" behavior is entirely unchanged: same whitespace-split semantics, same persistence, same `--agent reviewer --model opus`-style flag pipeline. Tests in `internal/session/startquery_test.go`: `TestStartCommandAppendsStartupQueryAsSingleArg` asserts a multi-word query emits as one shell-quoted token; `TestStartCommandOmitsStartupQueryWhenEmpty` asserts no stray empty-quoted arg when the field is unset; `TestStartupQueryDoesNotPersistToJSON` asserts the `json:"-"` tag holds (no `startup_query` field, no `StartupQuery` field, no query value string in the marshalled JSON); `TestResumeCommandOmitsStartupQuery` asserts the resume path does not pick the query up — this is the inverse of `TestResumeCommandAppendsExtraArgs` and is the regression guard for @Clindbergh's original complaint; `TestStartupQueryCoexistsWithExtraArgs` is the extra-args regression test that asserts both features emit together and do not interfere (extra-args tokens still appear as separate flags, start-query still appears as one positional). UI tests in `internal/ui/newdialog_test.go`: `TestNewDialog_View_ShowsStartQueryField_WhenClaudeSelected` asserts the `Start query:` label renders when the claude preset is selected; `TestNewDialog_GetClaudeStartQuery_ReturnsInputValue` asserts the accessor returns the raw un-split string (contract check: returns `string`, NOT `[]string`). **Numbering note:** drafted as v1.7.66; renumbered to v1.7.67 because v1.7.66 landed on main during review (feat(launch): verify claude consumed -m prompt, PR #726).

## [1.7.66] - 2026-04-22

### Fixed
- **`agent-deck launch -m "<prompt>" --no-wait` now verifies claude actually consumed the initial prompt** (internal task `54-launch-verify-prompt`). On cold starts, claude's welcome screen occasionally ate the first `Enter`, leaving the `-m` prompt typed-but-not-submitted in the composer. The session sat in `status=waiting` forever with the message visible at `❯` but no assistant response ever started. Root cause: the launch path's post-start verification budget was **1.2s** (`sendRetryOptions{maxRetries: 8, checkDelay: 150ms}` in `cmd/agent-deck/launch_cmd.go`), far too short to observe and recover from the welcome-screen race on a fresh claude+MCPs cold start. Fix: after the existing `sendWithRetryTarget` pass, a new `verifyPromptConsumedAfterLaunch` helper polls the pane for up to **10s** (`250ms` interval). "Consumed" = composer rendered AND the `-m` message is no longer visible in the input line (`send.HasCurrentComposerPrompt && !send.HasUnsentComposerPrompt`). If still unconsumed after the first window, it retries `send-keys` exactly once; if the second window also shows the prompt unconsumed, it writes a warning to `os.Stderr` and returns without failing the launch (best-effort, preserving `--no-wait` spirit). Five unit tests in `cmd/agent-deck/launch_verify_prompt_test.go` cover: consumed-first-poll path (no retry, no warning), unsent-then-consumed-after-retry path (exactly 1 retry, no warning), unsent-both-windows path (1 retry + warning), welcome-screen-no-composer path (no false "consumed" when the composer hasn't rendered yet), and wall-time budget enforcement. All five use synthetic pane strings only, per the sanitization rule. The existing `sendWithRetryTarget` call is unchanged — the new helper is a second verification layer, not a replacement. **Numbering note:** originally drafted as v1.7.64; renumbered forward through the v1.7.63/64/65 queue shift (fix-53-56 + #724 worktree-timeout landed ahead).

## [1.7.65] - 2026-04-22

### Added
- **Configurable worktree-setup-hook timeout via `[worktree].setup_timeout_seconds`** ([#724](https://github.com/asheshgoplani/agent-deck/issues/724), reporter: @Clindbergh). The worktree setup script `.agent-deck/worktree-setup.sh` was previously capped at a hardcoded 60s, which is too tight for real-world setups that install dependencies and seed local databases — users were seeing timeouts on otherwise-healthy scripts. Fix introduces a new integer config knob `[worktree].setup_timeout_seconds` (default `60`, preserving prior behaviour for every existing install) that is loaded via the standard `LoadUserConfig` path and threaded through `git.RunWorktreeSetupScript` / `git.CreateWorktreeWithSetup` as an explicit `time.Duration` parameter. A non-positive value falls back to `git.DefaultWorktreeSetupTimeout` (60s), so a missing section, a missing field, a `0`, or a negative integer all behave identically to pre-v1.7.65 and cannot accidentally disable the guard. `WorktreeSettings.SetupTimeout()` is a value-receiver helper on the existing `session.WorktreeSettings` struct that returns the resolved `time.Duration`; it's what all five `CreateWorktreeWithSetup` call sites now pass (two in `internal/ui/home.go` for new-session and fork flows, one each in `cmd/agent-deck/launch_cmd.go`, `cmd/agent-deck/session_cmd.go`, `cmd/agent-deck/main.go`). The package-level `worktreeSetupTimeout` var in `internal/git/setup.go` is gone; all timeout control now flows through the function parameter, keeping the `git` package free of a `session` import. Effective wall-clock for a timed-out script is still `setup_timeout_seconds + cmd.WaitDelay` (5s) before SIGKILL — matches pre-v1.7.65 semantics. Tests: `TestWorktreeSettings_SetupTimeoutSeconds_ParsesFromTOML` (config round-trip), `TestWorktreeSettings_SetupTimeout_DefaultSixtySeconds` (zero-value backward compat), `TestWorktreeSettings_SetupTimeout_HonoursConfiguredValue` (positive value honoured) in `internal/session/worktree_setup_timeout_test.go`; `TestRunWorktreeSetupScript_HonoursCallerTimeout` in `internal/git/setup_timeout_arg_test.go` (a 1s caller timeout on a `sleep 300` script must fail in well under the legacy 60s default — proves the parameter is actually threaded, not just declared). Existing `TestRunWorktreeSetupScript_Timeout` rewritten to pass `1*time.Second` directly rather than mutating the now-deleted package var. Example config:
  ```toml
  [worktree]
  setup_timeout_seconds = 300   # bump to 5 minutes for heavier setups
  ```
## [1.7.62] - 2026-04-22

### Added
- **Visual update nudge for severely out-of-date installs (conductor task #45).** Driver: on 2026-04-22 four users posted Feedback Hub comments from versions 15-39 releases behind head (v1.7.3, v1.7.17, v1.7.23, v1.7.23). They were hitting bugs that had been fixed weeks earlier. `internal/update/update.go` already queried `/releases/latest`, but the existing banner fired at every `Available=true` and — combined with users who had muted settings, or whose `CheckEnabled` had been off since install — never surfaced loudly enough to convince the severely-behind cohort to upgrade. This release splits that signal into two tiers:
  1. **`>5` releases behind** triggers a new "nudge" banner in the TUI status bar. The banner carries the concrete count (`30 releases behind`), the current and latest versions, and the dismiss hint (`press U to dismiss`). The cut-off at 6+ keeps casual users (1–5 behind) out of the noisy path while severely-behind users get the loud signal.
  2. **`agent-deck --version`** appends `(update available: vX.Y.Z)` when the on-disk cache shows the user is behind. This surface is **cache-only** — the flag never hits the network, so `--version` stays instant.
  - **`AGENTDECK_SKIP_UPDATE_CHECK=1`** is a hard kill-switch for every surface: no network call from `CheckForUpdate`, no annotation on `--version`, no TUI banner. Intended for air-gapped / locked-down / CI environments that rely on absolute network silence.
  - **Dismiss key:** `shift+U` hides the nudge for the rest of the process. The dismiss flag is session-local (resets on restart), so a user who upgrades out-of-band does not need to re-dismiss; a restart clears the state and the next check re-evaluates.
  - **New public surface in `internal/update`:** `CountReleasesBehind(currentVersion, releases) int`, `ShouldNudge(info) bool`, `CachedUpdateInfo(currentVersion) (*UpdateInfo, error)` for the offline cache read, `NudgeThreshold = 5` constant, `SkipUpdateCheckEnv = "AGENTDECK_SKIP_UPDATE_CHECK"` constant, `ReleasesBehind int` field on `UpdateInfo` and `UpdateCache`, and a new `fetchRecentReleases(limit)` helper that pulls `/releases?per_page=30` to compute the count.
  - **New TUI surface in `internal/ui`:** `Home.shouldRenderUpdateNudge()`, `Home.handleUpdateNudgeDismiss(msg)`, `Home.renderUpdateNudgeText()`, plus a `Home.updateNudgeDismissed bool` field. The three banner-height accounting sites in `home.go` (`getVisibleHeight`, `getListContentStartY`, the main layout render) all go through `shouldRenderUpdateNudge()` so a refactor of any one of them cannot drift from the rest.
  - **CLI surface in `cmd/agent-deck/main.go`:** extracted `writeVersionOutput(w io.Writer, currentVersion string)` so the flag dispatch (`case "version", "--version", "-v":`) writes through an `io.Writer` the tests can assert against byte-exactly. The version-output path is otherwise unchanged.
  - **Tests:** 6 unit tests in `internal/update/update_nudge_test.go` (threshold arithmetic, env-gate short-circuit, cache round-trip, offline cache read, env-gate on cached read), 4 unit tests in `internal/ui/update_nudge_test.go` (threshold, dismiss, env-gate, banner text content), 4 unit tests in `cmd/agent-deck/version_nudge_test.go` (annotation on, no-cache, up-to-date, env-gate), and 2 eval-smoke cases in `tests/eval/session/update_nudge_test.go` (real-binary `--version` with a seeded cache; real-binary `--version` with env-gate set). All 9 test-file entries added to `.claude/release-tests.yaml` under the `v1759-fix45-*` prefix (ID string retained from initial v1.7.59 slot; v1.7.60 reserved v1.7.59 for this work, but v1.7.60 and v1.7.61 both landed in main before this branch merged — see PR #723 merge-commit for detail) so the release gate catches any regression. Mandated gates (`TestPersistence_*`, `Feedback|Sender_`, watcher suite) remain green on this branch.

## [1.7.61] - 2026-04-22

### Added
- **`agent-deck session remove <id|title>` CLI subcommand** — removes a session from the registry. Only sessions in `stopped` or `error` state are removable by default; `--force` bypasses the gate (destructive). `--all-errored` bulk-removes every session currently in the `error` state and respects status filtering (stopped, idle, running sessions are untouched). `--prune-worktree` is an opt-in destructive variant that additionally kills the tmux process and removes any git worktree associated with the session.
- **TUI `X` keybind (Home view)** — status-gated registry remove with confirmation dialog. Rejects non-stopped/non-errored sessions with a message steering the user to `d` for destructive delete. The existing `d` → `deleteSession` path (full kill + worktree cleanup) is unchanged and remains the power-user option.
- **TUI `Ctrl+X` keybind** — bulk remove of all errored sessions with a confirmation dialog that shows the count. When there are no errored sessions the dialog is suppressed and an info message is shown instead.
- New `ConfirmRemoveSession` and `ConfirmBulkRemoveErrored` confirm-dialog types wired through `confirmAction` with yellow (non-red) border color to distinguish from the destructive `d` delete dialog.

### Preserved (hard invariant)
- Claude transcripts under `~/.claude/projects/<slug>/` are **never** touched by `remove` or the `X`/`Ctrl+X` TUI keybinds. `TestSessionRemove_PreservesTranscripts` enforces this at CI time.

### Tests
- `cmd/agent-deck/session_remove_cmd_test.go` — 6 subprocess tests: stopped-succeeds, running-without-force-rejected, running-with-force-succeeds, all-errored-respects-filter, transcripts-preserved, not-found-exit-2.
- `internal/ui/session_remove_tui_test.go` — 5 Seam A (model-level) tests covering `X` on stopped/error/running and `Ctrl+X` with N>0 / N=0 errored sessions.
- Full `cmd/agent-deck` suite passes under `-race` in 57.8s. Full `internal/ui` suite passes under `-race` in 29.2s. `TestPersistence_*` mandate suite passes.

## [1.7.60] - 2026-04-22

### Added
- **Group-scoped keyboard navigation in the TUI (Alt+j/k, Alt+1-9, Alt+g/G, Alt+/).** Addresses recurring feedback "jumping between shells is too complicated — shortcuts needed" (Christoph Becker, via Feedback Hub). The existing global tier — plain `j`/`k`, `1`-`9`, `g`/`gg`/`G`, `/` — continues to work exactly as before with no muscle-memory breakage or test churn. The new `Alt+`-prefixed layer restricts movement to the cursor's **current group**:
  - `Alt+j` / `Alt+k` — next / previous session in the current group. No-ops at the group boundary instead of spilling into the next group's first session.
  - `Alt+1`-`Alt+9` — jump to the Nth session within the current group (1-indexed). Plain `1`-`9` still jumps to the Nth root group.
  - `Alt+g` / `Alt+G` — first / last session in the current group.
  - `Alt+/` — fuzzy search filtered to the current group only. The local `Search` component grew a `scopedGroup` field so background session reloads (every ~2s via `h.search.SetItems(h.instances)` from seven call sites in `home.go`) do not leak out-of-group results into a scoped search session; `Hide()` clears the scope.

  "Current group" is derived from the cursor position: on a session item it's `Session.GroupPath`; on a group header it's the header's `Path`; on a window item it's the parent session's group path. See `currentGroupPath` in `internal/ui/group_nav.go`.

  **Discoverability** lands alongside the keybinds so users find out the shortcuts exist before giving up:
  - `?` help overlay gains a new "GROUP NAVIGATION (v1.7.60)" section listing all four Alt+ keybinds.
  - README grows a two-tier keybindings table (Global vs Group) with explicit scope descriptions.
  - One-shot status-bar hint on first TUI launch after upgrading to v1.7.60, reusing the existing maintenance-banner slot (no new layout math): "Tip: Alt+j/k and Alt+1-9 navigate within the current group. Press ? for all keybindings." The hint dismisses on any keypress or ESC, and a sentinel file at `~/.agent-deck/.nav-hint-v1760-shown` ensures it never reappears. Running under `AGENTDECK_PROFILE=_test` suppresses the hint so UI tests never write to a developer's real `~/.agent-deck/` directory.

  Tests: 17 new cases in `internal/ui/group_nav_test.go` — `TestGroupNav_AltJ_MovesToNextSessionInGroup`, `TestGroupNav_AltJ_DoesNotCrossGroupBoundary`, `TestGroupNav_AltK_MovesToPrevSessionInGroup`, `TestGroupNav_AltK_DoesNotCrossGroupBoundary`, `TestGroupNav_AltJ_FromGroupHeader_GoesToFirstSession`, `TestGroupNav_Alt2_JumpsToSecondInGroup`, `TestGroupNav_Alt3_JumpsToThirdInGroup`, `TestGroupNav_Alt5_BeyondGroup_IsNoop`, `TestGroupNav_Alt1_InBetaGroup_LandsOnB1`, `TestGroupNav_AltG_LowerCase_JumpsToFirstInGroup`, `TestGroupNav_AltShiftG_JumpsToLastInGroup`, `TestGroupNav_AltG_InBetaGroup_LandsOnB1NotA1`, `TestGroupNav_AltSlash_OpensSearchScopedToCurrentGroup`, `TestGroupNav_EvalHarness_RendersAndLandsOnRightSession` (end-to-end: renders TUI frame, dispatches Alt+1/2/3, asserts cursor identity + non-empty `View()` output), plus three regression tests (`TestGroupNav_Regression_PlainJ_StillMovesDownFlatList`, `TestGroupNav_Regression_PlainJ_CrossesGroupBoundary`, `TestGroupNav_Regression_Plain1_JumpsToFirstRootGroup`) pinning the global tier's existing semantics. Discoverability coverage: `TestNavHint_ShownOnFirstLaunch_DismissedAfterKeypress` and `TestNavHint_SkippedWhenSentinelExists` isolate `HOME` to a `TempDir` and unset `AGENTDECK_PROFILE` so the sentinel logic runs under test without polluting the developer's real home directory.

  Version numbering: v1.7.59 is reserved for the in-flight update-nudge session, so this release skips to v1.7.60. Matches the pre-existing ghost-version precedent (v1.7.44-45, .47, .55 were never tagged either).

## [1.7.58] - 2026-04-22

### Fixed
- **Bare-repository worktree layouts now fully supported** ([#715](https://github.com/asheshgoplani/agent-deck/issues/715), reported by [@Clindbergh](https://github.com/Clindbergh)). In a bare-repo layout (`project/.bare/` holding the git dir with `worktree1/`, `worktree2/`, … as peers), every worktree is equal — there is no "default" or "main" worktree. The previous code assumed `git rev-parse --git-common-dir` would end in `.git`, so in a bare layout `GetMainWorktreePath` silently fell through to `GetRepoRoot(dir)` and returned the *caller's own worktree path* as the "project root". That misdirected every downstream `.agent-deck/` lookup: setup scripts placed next to `.bare/` were never found, `worktree_repo_root` was logged as the wrong path on every session, and running `agent-deck worktree list` from the project root (where `.bare/` lives) failed outright with `not in a git repository`. Fix adds bare-repo detection via `git rev-parse --is-bare-repository` against the common-dir and teaches `GetMainWorktreePath` / `GetWorktreeBaseRoot` to return the parent of `.bare/` (the conventional project root) in that case. A new `IsGitRepoOrBareProjectRoot` predicate replaces the old `IsGitRepo` pre-flight check in `launch`, `add`, `session add`, and `worktree list` so callers can pass the project root transparently. The lower-level `BranchExists`, `ListWorktrees`, `RemoveWorktree`, `ListBranchCandidates`, and `CreateWorktree` funcs now resolve a nested bare repo (via a new `resolveGitInvocationDir` helper) before invoking `git -C`, so every code path downstream of `GetWorktreeBaseRoot` works on the project root without callers needing to know about the layout. Tests: 14 new RED→GREEN cases in `internal/git/bare_repo_test.go` build a real `.bare/` + 3-worktree fixture and assert (1) `IsBareRepo` / `IsBareRepoWorktree` distinguish bare-dir, linked-worktree, and normal-repo inputs, (2) `GetMainWorktreePath` returns the project root from *every* linked worktree — so there is truly no "default", (3) `GetWorktreeBaseRoot` accepts the project root itself (no `.git`) and returns the same, (4) `FindWorktreeSetupScript(projectRoot)` locates `.agent-deck/worktree-setup.sh` next to `.bare/`, (5) `CreateWorktree(projectRoot, …)` succeeds via transparent resolution to `.bare/`, (6) end-to-end `CreateWorktreeWithSetup(projectRoot, …)` on a bare fixture creates the worktree AND runs the setup script with `AGENT_DECK_REPO_ROOT` set to the project root, (7) `ListWorktrees(projectRoot)` enumerates `.bare` + 3 linked worktrees, (8) `BranchExists(projectRoot, …)` resolves true/false correctly, (9) all worktrees resolve to the same project root — no "default" concept leaks anywhere. Live-boundary evidence: before/after `agent-deck worktree list --json` from `project/` (was: `"not in a git repository"`; now: `"repo_root": "/project", "count": 4`) and from inside `worktree1/` (was: `"repo_root": "/project/worktree1"` — wrong; now: `"repo_root": "/project"`). End-to-end `add -w <new-branch> -b` from the bare project root now succeeds and runs the setup script, whereas on `main` it errored out with `Error: /project is not a git repository`.

## [1.7.57] - 2026-04-22

### Fixed
- **Right-pane preview no longer bleeds background highlights into the left pane** ([#699](https://github.com/asheshgoplani/agent-deck/issues/699), reported by @javierciccarelli on Ghostty against v1.7.43). When a Claude session's captured output contained an unclosed SGR — typically a background highlight on the user's input line whose closing reset was off-screen, clipped by the preview's width truncation, or emitted in a later capture window — the right pane's rendered line ended with SGR state still active at its newline boundary. `lipgloss.JoinHorizontal` then laid the next terminal row out as `left_pane + separator + right_pane + "\n"`, and the *next* row's left-pane whitespace was painted under the right pane's dangling highlight. Ghostty is strict about SGR persistence across rows, which is why the reporter saw a yellow band extend across the entire left column whenever they typed at the Claude prompt. Root cause was in `internal/ui/home.go:renderPreviewPane` — `ansi.Truncate` faithfully preserves the SGR *opening* of a truncated line but emits no closing reset, and the final width-enforcement pass (line 12543+) re-truncated without appending one either. Fix adds a single guard in the final pass: every line whose bytes contain an ESC (`0x1b`) now gets a hard `\x1b[0m` appended before the join, so SGR state is always reset at every newline boundary before `lipgloss.JoinHorizontal` assembles the frame. Harmless no-op on lines without ANSI; critical for lines with an unclosed highlight. This is the sibling invariant to the [#579](https://github.com/asheshgoplani/agent-deck/issues/579) CSI K/J erase-escape strip and the light-theme `remapANSIBackground` shipped with v1.6: those prevent the terminal from *starting* a bleed; this one stops state from *surviving* past a line. Regression coverage at three seams, matching the repo convention: `TestPreviewPane_RightPaneDoesNotLeakSGRState_Issue699` + `TestPreviewPane_TruncatedLineDoesNotLeakSGRState_Issue699` (Seam A unit, `internal/ui/preview_ansi_bleed_test.go` — assert no line in `renderPreviewPane`'s output leaves SGR active at its `\n`); `TestEval_FullViewDoesNotLeakSGRAcrossRows_Issue699` (Seam B eval, `eval_smoke` tier, `internal/ui/preview_ansi_bleed_eval_test.go` — drives the full `Home.View()` including `lipgloss.JoinHorizontal` and asserts the row-level invariant the user actually sees); `scripts/verify-preview-ansi-bleed.sh` (Seam C, builds the real binary and boots it in tmux as a final smoke check). Seam A and B both verified RED on the unfixed code (row 12 of the Seam B render captured `"                ... │ \x1b[43m> tell me about ghostty                ..."` — ends with SGR=43 active — exactly @javierciccarelli's screenshot) and GREEN after the one-line fix. `eval-smoke.yml` path triggers extended to include `internal/ui/home.go` and `internal/ui/preview*.go` so the Seam B eval runs per-PR on any preview-pane change. Thanks @javierciccarelli for the reproducer and the pinpoint screenshot.

## [1.7.56] - 2026-04-22

### Fixed
- **Socket isolation is now honoured on `session attach`, `session restart`, and every pty.go subprocess ([#687](https://github.com/asheshgoplani/agent-deck/issues/687) follow-up, reported by [@jcordasco](https://github.com/jcordasco) during the v1.7.50 audit).** v1.7.50 shipped `[tmux].socket_name` + `--tmux-socket` + per-session SQLite persistence and routed `session start` / `session stop` / pane probes through the `tmuxArgs` / `Session.tmuxCmd` factory — but `internal/tmux/pty.go` still assembled tmux argv by hand for six call sites, so every one of them connected to the user's **default** tmux server regardless of the session's configured socket. The classes of user-visible failure:
  1. **`session attach` silently fails** (`can't find session`) when socket isolation is enabled and the session lives on `-L <name>`. The attach argv was `exec.CommandContext(ctx, "tmux", "attach-session", "-t", s.Name)` — no `-L`, so tmux looked on the default server where the session does not exist.
  2. **`session attach-readonly` (used by the web terminal inspect flow) has the same hole** — same argv shape, same failure mode.
  3. **`(*Session).Resize(cols, rows)` retargets the default server**, so resize events for an isolated session either no-op or, if there's a same-named session on the default server, resize the wrong pane.
  4. **`AttachWindow`'s pre-attach `select-window` step runs on the default server**, so `session attach-window` selecting window 2 either fails or selects window 2 on an unrelated same-named default-server session before then correctly attaching to the isolated one (via fixed #1 above).
  5. **`StreamOutput`'s `pipe-pane -o cat` and its cancellation-path `pipe-pane` stop both run on the default server**, so streaming output from an isolated session receives zero bytes and the stop is a silent no-op.
  6. **Package-level `RefreshPaneInfoCache` fallback in `title_detection.go`** ran a `list-panes -a` on the default server, so the TUI status cache for isolation-enabled installs showed stale or empty pane titles/tool-detection on the fallback path.

  The fix routes every one of these through the existing v1.7.50 factory. Six new per-Session command-builder seams live at the bottom of `internal/tmux/pty.go` — `(*Session).attachCmd`, `attachReadOnlyCmd`, `resizeCmd`, `selectWindowCmd`, `pipePaneStartCmd`, `pipePaneStopCmd` — each delegating to `s.tmuxCmd` / `s.tmuxCmdContext` so `-L <SocketName>` lands before the subcommand when isolation is configured, and the argv stays byte-identical when it is not. Named methods (rather than inlining the factory calls) give the new regression-lint a stable target to assert argv shape against without spawning PTYs.

  The `title_detection.go` fallback now uses `tmuxExecContext(ctx, DefaultSocketName(), …)`, matching the "package-level probes read process-wide DefaultSocketName()" pattern already in use elsewhere.

  Four layers of regression coverage, all TDD red-then-green before the fix landed:
  - **Unit (`internal/tmux/pty_socket_test.go`, 7 cases)**: asserts each of the six command-builders emits the exact argv shape `["tmux", "-L", "<socket>", "<subcommand>", …]` when `Session.SocketName` is set, and `["tmux", "<subcommand>", …]` when empty (pre-v1.7.50 byte-compat).
  - **Static lint (`internal/tmux/tmux_exec_lint_test.go`, 1 case)**: AST-walks every `.go` file in the module, finds every `exec.Command("tmux", …)` and `exec.CommandContext(ctx, "tmux", …)` with a literal `"tmux"` as argv[0], and fails the build if any appears outside the allowlist. The allowlist covers the factory itself (`internal/tmux/socket.go`), the self-contained socket-aware wrapper in `internal/web/terminal_bridge.go`, the test harness's explicit `-S <path>` sandbox (`tests/eval/harness/sandbox.go`), and three specific legitimate argv shapes: `tmux -V` (binary existence check, no server connection), and the three inside-tmux `display-message` CLI helpers in `cmd/agent-deck/{cli_utils,session_cmd}.go` that read `$TMUX` env for auto-detection (adding `-L` there would over-restrict users running `agent-deck session current` from a non-agent-deck tmux pane). Adding a new source-level tmux exec site now requires either routing through the factory or editing the allowlist with justification — no more silent bypasses.
  - **Eval (`tests/eval/session/attach_socket_isolation_test.go`, 1 case, `eval_smoke` tag)**: drives the real `agent-deck` binary through the full interactive lifecycle against a real tmux server on a randomly-named isolated socket. `add` → `session start` → PTY-spawned `session attach` → verify client appears on `tmux -L <socket> list-clients` AND does NOT appear on the default server → send Ctrl+Q → clean detach with exit 0 → `session restart` → verify exactly one session on the isolated socket → `session stop` → verify zero sessions. The "PTY output dumped on failure" diagnostic makes the diagnosis actionable when a future regression fires this case.
  - **Harness (`tests/eval/harness/pty.go`)**: new `Sandbox.SpawnWithEnv(extraEnv, args…)` overlays extra env on top of the sandbox base, enabling tests (like this one) to run agent-deck under `TERM=xterm-256color` when real terminal capabilities are required — the sandbox default is `TERM=dumb` to keep termenv probes quiet, which is correct for most evals but causes tmux attach to refuse to register a client.

  All mandatory test gates pass unchanged: `TestPersistence_*`, Feedback + Sender_, Watcher framework, full `internal/tmux/...` race-detected suite.

  Thanks to [@jcordasco](https://github.com/jcordasco) for the detailed v1.7.50 audit that caught this — socket isolation at start + stop without isolation at attach would have been worse than no isolation at all, because users would have believed they were protected.
## [1.7.54] - 2026-04-22

### Added
- **Title-lock re-ship** ([#697](https://github.com/asheshgoplani/agent-deck/issues/697), reported by [@evgenii-at-dev](https://github.com/evgenii-at-dev)). The title-lock feature itself landed in main via PR [#714](https://github.com/asheshgoplani/agent-deck/pull/714) under the v1.7.52 CHANGELOG heading, but its release workflow and the follow-up v1.7.53 release both hit a pre-existing CI gap (`ubuntu-latest` ships without zoxide; the #693 quick-open picker tests short-circuit on `ZoxideAvailable()` and false-fail `go test ./...`). PR [#716](https://github.com/asheshgoplani/agent-deck/pull/716) added `apt-get install -y zoxide` to both `eval-smoke.yml` and `release.yml`. This release re-ships the v1.7.52 and v1.7.53 features as v1.7.54 so the title-lock fix (and the #709 `--select` flag that briefly tagged as v1.7.53 without artifacts) actually reach binary releases. **No source-code changes for the title-lock feature between v1.7.52 and v1.7.54** — the PR [#714](https://github.com/asheshgoplani/agent-deck/pull/714) commit is unchanged on main; only the release infrastructure around it was fixed. See the "[1.7.52]" entry below for the full feature description and the TDD evidence.
- **No-op for the #709 `--select` behaviour** — see the "[1.7.53]" entry below; re-shipped identically.

### Fixed
- **Release workflow infrastructure gap unblocked** ([#716](https://github.com/asheshgoplani/agent-deck/pull/716)): `release.yml` and `eval-smoke.yml` now install zoxide before running `go test ./...`. Without this every release tag between v1.7.52 and v1.7.54 failed its goreleaser step, leaving orphan tags with no binaries. Future releases on `ubuntu-latest` are unblocked.

## [1.7.53] - 2026-04-22

### Added
- **`--select <id|title>` CLI flag: launch the TUI with the cursor preselected on a specific session, while keeping every group visible in the sidebar** ([#709](https://github.com/asheshgoplani/agent-deck/issues/709), requested by [@tarekrached](https://github.com/tarekrached)). Before this change, the only way to "jump to" a session at launch was `-g <group>`, which also hid every other group from the sidebar — useful when you want to scope the TUI to one area, but wrong when you just want to land on a session without losing the rest of the tree. `--select` is the orthogonal primitive: it positions the cursor on the matching session (ID or title, case-insensitive, whitespace-tolerant) on first render and leaves the group tree untouched. Precedence with `-g` is well-defined: if both are passed, `-g` still scopes the visible groups and `--select` positions the cursor **within that scope**; if the selected session is outside the scope, `--select` is ignored and a `Warning: --select "X" is not in group "Y"; cursor will not be repositioned` line is printed to stderr so the mismatch is visible without digging through logs. Implementation: a new `extractSelectFlag` in `cmd/agent-deck/main.go` mirrors the existing `extractGroupFlag` pattern (both `--select foo` and `--select=foo` forms), and `Home.SetInitialSelection` + `Home.applyInitialSelection` in `internal/ui/home.go` queue the preselection until the first `loadSessionsMsg` arrives — `applyInitialSelection` runs immediately after `rebuildFlatItems` so it respects any active group scope, and it is idempotent so normal cursor navigation after the first render is not overridden. The match order is: exact ID first, then case-insensitive title equality, then lower-cased whitespace-trimmed title — this lets `--select "My Project"` work even if the user shell-quotes the title differently from how it was stored. Tests: 7 new RED→GREEN cases — `TestExtractSelectFlag` (7 sub-tests covering flag parsing forms and interaction with `-p`/`-g`), `TestExtractSelectFlag_PreservesGroupFlag`, `TestSetInitialSelection_PositionsCursorAndKeepsAllGroupsVisible` (the core #709 assertion: cursor on requested session AND all three test groups remain in `flatItems`), `TestSetInitialSelection_MatchesByTitle`, `TestSetInitialSelection_GroupScopePrecedence` (3 sub-tests for in-scope / out-of-scope / unknown-id paths), `TestSetInitialSelection_NormalizationIsLenient`. End-to-end evidence in `scripts/verify-select-flag.sh` (headless tmux + `capture-pane`): seeds three sessions across three groups, launches the real binary, captures the pane, asserts the cursor marker is on the selected session and all three groups remain visible in the sidebar, then runs the `-g work --select beta` scenario and asserts the stderr warning fires. No changes to `-g` semantics, no changes to the persisted cursor-restore path beyond letting `--select` take precedence on the very first load.

## [1.7.52] - 2026-04-22

### Added
- **`--title-lock` flag + `session set-title-lock` subcommand prevent Claude's session name from overriding the agent-deck title ([#697](https://github.com/asheshgoplani/agent-deck/issues/697), reported by [@evgenii-at-dev](https://github.com/evgenii-at-dev)).** Conductor workflow: launch `agent-deck launch -t SCRUM-351 -c claude --title-lock` on a worker, then Claude's own `/rename` of its session (or the auto-generated first-message summary like `auto-refresh-task-lists`) is prevented from syncing back into the agent-deck title. Without this, the conductor loses the semantic identity it assigned to the child session on the first hook tick — making it impossible to tell which worker is working on which ticket once Claude has spoken. Three call sites:
  1. **`Instance.TitleLocked bool`** (new field, persisted in `instances.title_locked` SQLite column via schema bump v7 → v8, additive ALTER TABLE with `DEFAULT 0` so every pre-v1.7.52 row reads as unlocked and the existing `applyClaudeTitleSync` path stays default-on for them). JSON tag `title_locked,omitempty` keeps the wire format backwards-compatible with any third-party tooling that reads the state-db JSON dumps.
  2. **`applyClaudeTitleSync` gate** (`cmd/agent-deck/hook_name_sync.go`): after resolving the target Instance, an early-return `if target.TitleLocked` skips the Title mutation and the SaveWithGroups write — keeping the #572 default behaviour (Claude `--name`/`/rename` syncs into agent-deck) untouched for the 99% case while giving conductors an opt-in off switch.
  3. **CLI surface**: `agent-deck add` and `agent-deck launch` gain `--title-lock` (with `--no-title-sync` as an alias for discoverability); `agent-deck session set-title-lock <id> <on|off>` toggles an already-created session (accepts `true`/`false`/`1`/`0`/`yes`/`no` too for script friendliness). `session show --json` now emits `title_locked: true|false` so conductors can query state without reading the SQLite directly.

  Tests (TDD — RED captured on baseline before the implementation landed):
  - `TestApplyClaudeTitleSync_NoopWhenTitleLocked` in `cmd/agent-deck/hook_name_sync_test.go` — seeds an Instance with `TitleLocked: true` and a matching Claude session metadata file, invokes `applyClaudeTitleSync`, asserts the Title did NOT change and that `TitleLocked` survived the round-trip (guards against silent persistence regressions).
  - `TestStorageSaveWithGroups_PersistsTitleLocked` in `internal/session/storage_test.go` — round-trips two instances (one locked, one unlocked) through `SaveWithGroups`, then reloads via BOTH `LoadWithGroups` (full hydration, TUI path) and `LoadLite` (fast CLI path), asserting the bool survives each path and that the default (false) doesn't leak across rows.
  - The three existing `TestApplyClaudeTitleSync_*` cases (UpdatesInstance / NoopWhenNameMissing / NoopWhenNameEqualsTitle) continue to pass unchanged, proving the #572 default behaviour is preserved.
  - End-to-end eval harness at `tests/eval/title-lock.eval.sh` drives the real binary through three real-world scenarios in a disposable `HOME`: (A) add with `--title-lock` blocks Claude's rename; (B) `session set-title-lock off` re-enables sync on the next hook tick; (C) `set-title-lock on` re-freezes the title against a subsequent rename. Smoke-tier — designed to run on every PR that touches session lifecycle.

  Thanks to [@evgenii-at-dev](https://github.com/evgenii-at-dev) for the detailed conductor-workflow bug report that caught this.

## [1.7.51] - 2026-04-22

### Fixed
- **Settings TUI no longer drops the `[tmux]` config block on save** ([#710](https://github.com/asheshgoplani/agent-deck/issues/710), reported on v1.7.50). Pressing `S` in the TUI, toggling any setting, and saving was silently zeroing the entire `[tmux]` table on disk — `inject_status_line`, `launch_in_user_scope`, `detach_key`, `socket_name` (v1.7.50), and `options` were all gone after the next reload. Root cause: `SettingsPanel.GetConfig` reconstructs the to-be-saved `UserConfig` from the panel's visible widget state and pass-through-copies every section it doesn't render (MCPs, Tools, Profiles, Worktree, …) from `originalConfig`, but `Tmux` had been omitted from that copy block. Same class of bug as [#584](https://github.com/asheshgoplani/agent-deck/pull/584) (Worktree) and the structural reason we couldn't reproduce the original [#687](https://github.com/asheshgoplani/agent-deck/issues/687) `inject_status_line` report by editing `config.toml` directly — the reporter was hitting the Settings TUI save path, not the loader. Fix is one line: `config.Tmux = s.originalConfig.Tmux` added to the preservation block in `internal/ui/settings_panel.go`. Coverage gap closed by two new tests: `TestSettingsPanel_Tmux_GetConfigPreservesHiddenFields` (unit, mirrors the existing Worktree guard) asserts `GetConfig()` round-trips `InjectStatusLine`, `LaunchInUserScope`, and `DetachKey` from `originalConfig`; `TestEval_SettingsTUI_SavePreservesTmux` (eval_smoke tier in `internal/ui/settings_panel_eval_test.go`) drives the full `LoadUserConfig → SettingsPanel.LoadConfig → GetConfig → SaveUserConfig → re-read TOML` round-trip against a scratch `$HOME` to prove `[tmux]` survives a real save with a non-tmux setting changed (theme dark → light). Both tests were verified RED on the unfixed code and GREEN after the one-line fix. Thanks to @jcordasco for the exact diagnosis and suggested fix in #710.

## [1.7.50] - 2026-04-21

### Added
- **Tmux socket isolation (phase 1) — agent-deck can now run on a dedicated tmux server, fully separate from your interactive tmux** ([#687](https://github.com/asheshgoplani/agent-deck/issues/687), completes the root-cause fix for [#276](https://github.com/asheshgoplani/agent-deck/issues/276)). Opt in via a single config line:
  ```toml
  [tmux]
  socket_name = "agent-deck"
  ```
  Every agent-deck session now spawns as `tmux -L agent-deck …` — a separate tmux server whose socket lives at `$TMUX_TMPDIR/tmux-<uid>/agent-deck`. Your regular tmux at `default` is never touched. `[tmux].inject_status_line`, bind-key, and global `set-option` mutations stay on the agent-deck server; your personal status bar, plugins, and theme are untouched. A stray `tmux kill-server` in your shell cannot take agent-deck sessions down with it. `tmux -L agent-deck ls` from the shell shows exactly agent-deck's sessions.

  **Default behavior unchanged.** Leave `socket_name` unset (the default) and agent-deck behaves exactly like v1.7.49: it uses your default tmux server. This is a pure opt-in — **zero behavior change for existing users**.

  **Per-session override.** Both `agent-deck add --tmux-socket <name>` and `agent-deck launch --tmux-socket <name>` override the installation-wide default for one session. Precedence: CLI flag > `[tmux].socket_name` > empty.

  **Per-session persistence.** Each Instance captures its socket name in SQLite at creation time (new `tmux_socket_name` column, schema v7 with an additive `ALTER TABLE` migration — legacy rows default to `''`). Every lifecycle operation (start/stop/restart/revive, status probe, capture-pane, send-keys, kill-session) reads `Instance.TmuxSocketName` and targets that socket. Changing `socket_name` in config later does **not** migrate existing sessions — they remain reachable on the socket they were created on. Mixing sockets mid-life would strand the pane; the immutable-after-creation contract prevents that.

  **Scope of changes.** A single command-factory pair — `tmux.tmuxArgs(socketName, args...)` + the `Exec`/`ExecContext` public wrappers — centralises the `-L <name>` injection. Every one of the ~50 `exec.Command("tmux", …)` call sites across `internal/tmux/`, `internal/session/`, `internal/ui/`, `internal/web/`, and `cmd/agent-deck/` now routes through this factory or its `(*Session).tmuxCmd` counterpart, so a future socket-selection change (phase 2/3: per-conductor sockets, `-S <path>` support, session-migrate subcommand) has exactly one hook point. The three package-level probes (`IsServerAlive`, `RefreshSessionCache`, `recoverFromStaleDefaultSocketIfNeeded`) read a process-wide `tmux.DefaultSocketName()` seeded once at `main.go` startup from `session.GetTmuxSettings().GetSocketName()`. `tmux -V` version check intentionally stays plain — it does not connect to any server, so socket selection is moot. The web PTY bridge's existing `-S <path>` fallback from the `TMUX` env var is preserved — per-session `TmuxSocketName` takes precedence when set.

  **Reviver wiring.** `Reviver.TmuxExists` signature changed from `func(name string) bool` to `func(name, socketName string) bool` so revive scans probe the right tmux server. Probing the default server for a session living on an isolated socket would wrongly classify it as dead; this callback now receives `Instance.TmuxSocketName` from `Classify()` and the default helper (`defaultTmuxExists`) forwards it to a new `tmux.HasSessionOnSocket(socket, name)`. `PipeManager.Connect` and `NewControlPipe` also gained a `socketName` parameter so reconnect loops target the right server for the entire life of the pipe.

  **Tests.** 17 new tests covering the full surface: `TestTmuxArgs_*` (5 cases — empty socket pass-through, `-L` injection, caller-slice immutability, empty args, whitespace-only trim), `TestSession_TmuxCmd_*` (2 — per-session builder honors `Session.SocketName`), `TestDefaultSocketName_*` (3 — process-wide default init/set/trim), `TestGetTmuxSettings_SocketName_*` (4 — TOML round-trip, explicit value, whitespace-trim, whitespace-only→empty), `TestNewInstance_SocketName_*` + `TestNewInstanceWithTool_SocketName_*` + `TestRecreateTmuxSession_PreservesSocketName` (4 — constructor seeding from config, tool-aware constructor parity, restart-preserves-captured-socket invariant), `TestStorage_TmuxSocketName_{Roundtrip,EmptyRoundtrip}` (2 — SQLite save→close→reopen→load for both isolated and legacy rows), `TestReviver_*` (3 — Classify threads the socket name into `TmuxExists`, legacy instances probe with empty socket, `ReviveAction` receives the instance socket name), `TestTmuxAttachCommand_SocketNameOverridesEnv` + `TestTmuxAttachCommand_WhitespaceSocketNameFallsBackToEnv` (2 — web PTY bridge precedence and whitespace defensive fallback). Every mandatory test gate from CLAUDE.md (`TestPersistence_*`, `Feedback*`, `Sender_*`, watcher framework tests, behavioral evaluator harness introduced in v1.7.49) continues to pass unchanged — socket isolation adds a new axis to the tmux-command contract without weakening the session-persistence, systemd-scope, or user-observable-behavior invariants. (Note: a real-tmux eval case exercising `agent-deck add --tmux-socket …` + `session start` + `display-message -p` on the isolated socket is tracked as a phase-2 follow-up; phase 1 relies on unit + integration coverage of the factory, persistence, and reviver surfaces plus the v1.7.49 `TestEval_Session_InjectStatusLine_RealTmux` which exercises the default-socket path unchanged.)

  **Migration.** Docs-only in this release. There is no `session migrate-socket` subcommand yet — moving existing sessions to the isolated socket requires either re-creating them via `agent-deck add`, or hand-editing `~/.agent-deck/<profile>/state.db` (`UPDATE instances SET tmux_socket_name = 'agent-deck' WHERE id = '…'`) and restarting agent-deck. The dedicated subcommand is tracked for phase 2 along with per-conductor sockets and `-S <socket-path>` support. See the "Socket Isolation" section in README for the full migration recipe.

## [1.7.49] - 2026-04-21

### Added
- **Behavioral evaluator harness ([#37](https://github.com/asheshgoplani/agent-deck/issues/37)).** New test layer at `tests/eval/` that catches the class of regressions where a Go unit test passes but the user sees the wrong thing. Motivated by three recent shipped-but-unit-test-invisible bugs: v1.7.35 CLI disclosure buffered behind stdin (`strings.Builder` hid the prompt until after the function returned; unit tests used the same type, so the bug was invisible), v1.7.37 TUI feedback dialog going straight from comment to send with no disclosure step, and the #687 `inject_status_line` misdiagnosis where unit tests asserted on struct fields and argv slices instead of what real tmux actually displayed. Harness stack: per-test scratch `HOME`, isolated tmux socket via a wrapper shim that splices `-S <sock>` into every `tmux` invocation the binary makes, a `gh` shim that records argv+stdin to a JSON log and scripts success/failure, and a `github.com/creack/pty`-based PTY driver with an `ExpectOutputBefore(want, before, timeout)` matcher that structurally defeats `strings.Builder`-style buffering regressions (under a real PTY, a buffered wrapper makes tokens arrive only after the next stdin read, so the wait times out). Three RFC §7 cases ship in this release: `TestEval_FeedbackCLI_DisclosureBeforeConsent` (PTY-driven, asserts the `Rating` prompt and the "posted PUBLICLY" disclosure both arrive before the binary blocks on stdin — catches any future strings.Builder-style regression structurally), `TestEval_FeedbackTUI_DisclosureStepExists` + `TestEval_FeedbackCLI_and_TUI_HaveEquivalentDisclosure` (drives the `FeedbackDialog` state machine end-to-end and proves the two surfaces carry the same disclosure tokens), and `TestEval_Session_InjectStatusLine_RealTmux` (runs `agent-deck add` + `session start` against a per-sandbox tmux socket, then queries `display-message -p '#{status-right}'` to assert the injected bar actually reaches the tmux server). Each case was verified TDD-style before shipping: the fix was temporarily reverted in the product code, the test was confirmed to fail with a diagnostic that identifies the exact regression (strings.Builder buffering → `Rating` prompt times out; stepConfirm collapsed → "expected stepConfirm (disclosure step), got stepSent"; buildStatusBarArgs forced nil → `status-right` is tmux's default template instead of agent-deck's injected one), then the fix was restored. Tiered CI: `.github/workflows/eval-smoke.yml` runs `go test -tags eval_smoke` on every PR that touches the affected paths (3-minute timeout, blocking), and `release.yml` adds an `eval_smoke eval_full` step before GoReleaser so a release that fails eval does not get a tag. Linux-only in CI per the RFC's cost analysis; macOS dev runs locally. See `docs/rfc/EVALUATOR_HARNESS.md` for the full design and `tests/eval/README.md` for how to add cases. CLAUDE.md gains an "eval case required for interactive flow changes" mandate mirroring the existing session-persistence, watcher, and feedback mandates.
## [1.7.48] - 2026-04-21

### Added
- **`agent-deck session send --stream`: structured JSONL streaming of the agent's reply while it is still being produced ([#31](https://github.com/asheshgoplani/agent-deck/issues/31), resolves [#689](https://github.com/asheshgoplani/agent-deck/issues/689)).** Previously `session send` either returned a one-shot snapshot (default), a running-status heartbeat (`--wait`), or nothing at all (`--no-wait`) — long assistant turns with intermediate tool calls were opaque to every caller except a human watching tmux. The new `--stream` flag tails the Claude JSONL transcript as it is appended and emits a line-delimited event stream to stdout: `start` (carries `schema_version` so consumers can branch on future schema moves), `text` (text-block deltas, batched on 10s idle / 4000-char / 3-tool boundaries with `--stream-idle`/`--stream-char-budget`/`--stream-tool-budget` overrides), `tool_use` (name + input), `tool_result` (matching `tool_use_id` + content), `stop` (with `reason` = `end_turn`/`max_tokens`/`stop_sequence`), and `error` (on idle timeout or upstream failure). The streamer runs in `internal/session/transcript_streamer.go`: it opens the transcript at `~/.claude/projects/<encoded>/<session-id>.jsonl`, tracks a file offset plus a UUID dedup set for idempotency under rewind, drops records whose `timestamp` is before `sentAt - 250ms` to avoid replaying pre-send history, and walks each assistant/user record's `content` blocks to translate them into events. Text blocks from the same assistant message are merged and flushed on the first of: a later `tool_use` in the same message, the 4000-char budget, the 3-tool budget, or idle timeout. `stop_reason == "tool_use"` is NOT treated as terminal — the streamer keeps running so the subsequent `tool_result` + next assistant turn stream through as one continuous flow. Phase 1 is Claude-only (Claude Code Opus/Sonnet/Haiku via `IsClaudeCompatible`) because the transcript format is Claude-specific; non-Claude tools (codex, gemini, aider, shell) get a clean `--stream is not supported for tool %q (Phase 1 supports Claude-compatible tools only)` error and exit 1 at the CLI entry point via `streamPreconditionError()` before any tail begins. `--stream` and `--wait` are mutually exclusive. The existing `--wait` + `--no-wait` + default paths are unchanged; callers that don't pass `--stream` see byte-identical behavior to v1.7.47. 10 new tests in `internal/session/transcript_streamer_test.go` (defaults + overrides of the batching triad, start/text/tool_use/tool_result/stop event emission, pre-sentAt record skipping, natural end_turn return, idle-timeout error, char-budget flush, context-cancel return) plus 3 in `cmd/agent-deck/session_stream_test.go` (Claude-compatible allowed, non-Claude rejected with a message naming the flag and tool, end-to-end tail-to-stdout against a hand-authored JSONL fixture). Unlocks the conductor loop's streaming hop that #689 blocked.

## [1.7.45] - 2026-04-21

### Fixed
- **Transition notifier no longer silently loses events when the parent conductor is busy ([#39](https://github.com/asheshgoplani/agent-deck/issues/39), [#40](https://github.com/asheshgoplani/agent-deck/issues/40)).** Production logs for the 24 hours before this release showed a 23% delivery rate (45 sent / 198 generated) on transition notifications: **105 events (53%)** took the silent-loss path `deferred_target_busy → forgotten`, while another 47 were root-conductor transitions with no parent and 1 was an outright send failure. Two distinct problems combined to produce that number and both are fixed here.

  **Primary bug — deferred events were silently dropped.** When a child session transitioned `running → waiting` while the parent conductor happened to be `StatusRunning` (mid-tool-call), the notifier wrote `delivery_result=deferred_target_busy` to `transition-notifier.log` and returned, deliberately not marking the event in the dedup state so a later poll could retry. But `TransitionDaemon.syncProfile()` unconditionally updated `d.lastStatus[profile]` after every pass, including on deferred events. On the next poll cycle `prev[id]` was `"waiting"` (the new state), so `ShouldNotifyTransition("waiting", "waiting")` returned false and the transition was never re-offered. The intended retry loop did not exist. Fix: a persistent deferred-retry queue at `~/.agent-deck/runtime/transition-deferred-queue.json`. `NotifyTransition` now calls `EnqueueDeferred(event)` on the busy-target path; `syncProfile` calls `notifier.DrainRetryQueue(profile)` at the top of every poll (ahead of the `initialized` gate so `notify-daemon --once` also drains). Drain walks each entry, dispatches via the async sender when `liveTargetAvailability` reports the target is not `StatusRunning`, and age-outs stale entries to `notifier-missed.log` with `reason=expired` after `defaultQueueMaxAge = 10m` or `defaultQueueMaxAttempts = 20`. Queue entries are keyed by `(child_session_id, from_status, to_status)` so repeat defers of the same transition refresh the event but preserve `FirstDeferredAt` — the age-out timer is honest across the full life of a stuck transition. The queue persists across notifier restarts (daemon reload or process crash) because the file is rewritten under a `.tmp + rename` on every mutation.

  **Secondary bug — head-of-line blocking in the dispatch path.** The notifier's send to a target was synchronous: a slow `tmux send-keys` against one pane serialized every subsequent notification across unrelated targets in the same poll cycle. On a conductor host with many active children, one hung pane could delay notifications for an entire poll interval. Fix: `dispatchAsync` spawns one goroutine per notification, gated by a per-target semaphore (`map[string]chan struct{}` of buffer 1). Each send runs under a 30s default timeout (`defaultSendTimeout = 30 * time.Second`). Three terminal states land in logs: `sent`/`failed` go to the existing `transition-notifier.log` stream; `timeout` (send ran past 30s) and `busy` (target already had an in-flight send) go to a new `~/.agent-deck/logs/notifier-missed.log` — operators now have an actionable evidence trail instead of a silent miss. The sender goroutine holds its target's semaphore slot until the underlying `SendSessionMessageReliable` actually returns, even if the watcher already declared a timeout; this prevents a second `tmux send-keys` from racing the first on the same pane.

  **`notify-daemon --once` flush.** Because dispatch is now asynchronous, the `--once` CLI path would have exited before goroutines finished writing their log entries. `TransitionDaemon.Flush()` waits on both the watcher and sender WaitGroups; `handleNotifyDaemon` in the `--once` branch calls it before returning so that `notify-daemon --once` remains deterministic under test.

  **Investigation of #40 ("conductor stopped when children silent").** A parallel investigation (`INVESTIGATION_40_CONDUCTORS_STOPPED.md`) confirmed the "stopped" symptom is **not** caused by a silence/idle detector in `watchdog.py` or the agent-deck daemon. Neither code path flips a conductor to `error` based on elapsed silence. The real triggers are tmux-server SIGSEGV cascades (documented in `FORENSIC_2026_04_20_MASS_DEATH.md`) and `claude --resume` failures that leave a pane dead within the watchdog's 15s restart-success window. Those are out of scope for this release; #40 stays open for a separate fix.

  **Test harness and verification.** Twelve new unit tests in `internal/session/transition_notifier_async_test.go` and `transition_notifier_queue_test.go` cover: slow-target-doesn't-block-fast-target (throughput), timeout → missed.log, concurrent-same-target → busy miss, normal sent path, explicit send error → failed (not missed), queue enqueue persistence, drain-dispatch-when-free, drain-keeps-busy-entries, drain-expires-old-entries, queue survives notifier reload, and the integration case proving `NotifyTransition` with a `StatusRunning` parent enqueues rather than marking the event notified. A new `scripts/verify-notifier-async.sh` harness uses the built binary against a real tmux server under an isolated `HOME`: it seeds a deferred queue entry, runs `notify-daemon --once`, and asserts (a) the delivery log shows `delivery_result=sent`, (b) the queue is cleared, (c) the literal `[EVENT] Child 'child-e2e'` banner appears in the parent's live tmux pane (confirming the real `tmux send-keys` pipeline end-to-end), and (d) `notifier-missed.log` stays empty on the happy path.

## [1.7.44] - 2026-04-21

### Changed
- **Mobile web terminal input** ([#652](https://github.com/asheshgoplani/agent-deck/pull/652) by [@JMBattista](https://github.com/JMBattista)): mobile clients (`pointer: coarse`) no longer enforce an implicit read-only mode in the web UI. Keystrokes from phones/tablets now flow to the tmux session like any other client. To preserve the previous behavior, start the web server with `agent-deck web --read-only` — the server-side flag now owns read-only enforcement for all devices. Rebuild of JMBattista's original PR #652 (which had accumulated merge conflicts across 9 intervening releases); authorship is preserved via `Co-Authored-By` trailer on the rebuilt commit. Four surgical changes in `internal/web/static/app/TerminalPanel.js`: (1) the `const isMobile = isMobileDevice()` component-scope variable is removed, (2) `disableStdin: mobile` in the `new Terminal({...})` constructor becomes `disableStdin: false`, (3) the `if (!mobile) { inputDisposable = terminal.onData(...) }` gate becomes an unconditional `const inputDisposable = terminal.onData(...)` so phone/tablet keystrokes reach the WebSocket, (4) the mobile-only `container.addEventListener('touchstart', (e) => e.preventDefault())` block and the `READ-ONLY: terminal input is disabled on mobile` yellow banner are both deleted. The `readOnlySignal` + `payload.readOnly || mobile` OR in `onWsMessage` loses the `|| mobile` half so the server-side `--read-only` flag is the single source of truth for input enablement across all device types. PERF-E listener-site count drops from 9 to 8 (the mobile-only anonymous touchstart preventDefault was the 9th site); `tests/e2e/visual/p8-perf-e-listener-cleanup.spec.ts` updated to assert `controller.signal` appears `>=8` times, and `tests/e2e/visual/p1-bug6-terminal-padding.spec.ts` flips from asserting the READ-ONLY banner is present to asserting it is absent, plus two new structural tests (`terminal.onData is not gated on !mobile`, `disableStdin is not OR-ed with mobile on status messages`) to guard the rebuild from regressing.
## [1.7.43] - 2026-04-21

### Fixed
- **Zombie tmux clients and MCP subprocesses no longer accumulate in long-running agent-deck TUI and web processes** ([#677](https://github.com/asheshgoplani/agent-deck/issues/677)): four distinct `exec.Cmd.Start()` call sites were paired with a `cmd.Wait()` that only fired on the manual-shutdown path, so any child that exited on its own — MCP server crash, tmux session killed externally, triage `agent-deck launch` exiting normally, tmux server reload — became a zombie entry in the process table that was never reaped. On one live conductor this week: **10 zombies on the TUI** (all `npm exec`/`uv` MCP children from `broadcastResponses`) plus **43 zombies across web/TUI** cascades earlier the same day. Per-zombie memory is tiny, but accumulation is unbounded: over a week-long agent-deck session with an attached MCP pool and active watcher triage this bloats the process table and eventually hits the per-UID process limit, manifesting as `fork/exec` failures far from the real cause. Four fix sites:
  1. **`internal/tmux/controlpipe.go`** — the `reader()` goroutine that parses `tmux -C` protocol output saw stdout-EOF when the subprocess died and closed `Done()`, but never called `cmd.Wait()`. Only `Close()` reaped, so if the `PipeManager` reconnect loop gave up or a session was removed, Close was skipped and the zombie persisted. Fix: a new `reap()` helper guarded by `sync.Once` is called from both the `reader()` defer (natural EOF path) and `Close()` (manual shutdown), so exactly one goroutine runs `cmd.Wait()` no matter which event fires first.
  2. **`internal/mcppool/socket_proxy.go`** — `broadcastResponses` saw MCP stdout EOF, set status to `StatusFailed`, and closed client connections, but never called `mcpProcess.Wait()`. The zombie lingered until `Stop()` / `RestartProxy()` was invoked, which for an idle or rarely-used MCP may be never. Same `waitOnce` + `reap()` pattern wired into `broadcastResponses` (EOF path), `Stop()` (graceful shutdown path), and the `net.Listen` fallback that kills on socket-creation failure. Matches the 10 `npm exec`/`uv` zombies observed on the live conductor.
  3. **`internal/watcher/triage.go`** — `AgentDeckLaunchSpawner.Spawn()` did `cmd.Start()` and returned without ever waiting on the child. Every triage event produced exactly one zombie. Fix: a `go func() { _ = cmd.Wait() }()` reaper goroutine launched after `Start()` succeeds, so the child is reaped whenever it exits. Tested by stub-binary spawn: 25 spawns → 0 zombies.
  4. Tests: `TestControlPipe_NoZombie_WhenProcessExits`, `TestControlPipe_NoZombie_ManyCycles` in `internal/tmux/zombie_reap_test.go` (20 kill-session cycles, asserts zombie count does not grow); `TestSocketProxy_NoZombie_OnProcessExit` in `internal/mcppool/socket_proxy_zombie_test.go` (15 cycles of `sh -c "exit 0"` MCP processes, asserts no zombie after broadcastResponses EOF); `TestAgentDeckLaunchSpawner_NoZombie` in `internal/watcher/triage_zombie_test.go` (25 triage spawns with a stub agent-deck, asserts no zombie remains). Each test reads `/proc/<pid>/status` for `State: Z (zombie)` so failures print the exact growth delta. Linux-only (tests `t.Skip()` when `/proc` is absent) — the production code fixes are portable.

## [1.7.42] - 2026-04-21

### Changed
- **CI: audit + fix-or-disable broken gates ([#682](https://github.com/asheshgoplani/agent-deck/issues/682)).** Two PR gates removed, zero fixed in place, four still active. Green now means green again. Every PR merged between v1.7.34 and v1.7.41 carried a red `Visual Regression` check and in most cases a red `Lighthouse CI` check too, and the recurring "ignore the red, it's just visual-regression" exception was training the team to merge through real failures. Both gates shared the same root cause — `./build/agent-deck web` imports bubbletea transitively and fails its cancel-reader init on headless CI runners (`error creating cancelreader: bubbletea: error creating cancel reader: add reader to epoll interest list`), so the test server never binds and every Playwright/Lighthouse spec fails with `ERR_CONNECTION_REFUSED`. The Lighthouse budget in `.lighthouserc.json` was also never re-baselined against the current webui bundle. Fixing the server-start path (PTY wrapper or a `--no-tui` startup flag) is tracked as a stability-ledger follow-up; until then, per the audit recommendation, both PR gates are **removed**:
  - `.github/workflows/visual-regression.yml` — **deleted.** Same test matrix still runs on the Sunday cron via `weekly-regression.yml`. Local run: `cd tests/e2e && npx playwright test --config=pw-visual-regression.config.ts` against a local `agent-deck web`.
  - `.github/workflows/lighthouse-ci.yml` — **deleted.** Same Lighthouse suite still runs weekly via `weekly-regression.yml`. Local run: `./tests/lighthouse/calibrate.sh` then `npx lhci autorun --config=.lighthouserc.json`.

  Remaining active PR gate is `session-persistence.yml` (the `TestPersistence_*` suite plus `scripts/verify-session-persistence.sh`), which has passed consistently on every run and gates the class of bug the v1.5.2 mandate was written to prevent. `release.yml`, `pages.yml`, `issue-notify.yml`, `pr-notify.yml`, `weekly-regression.yml` are unchanged. New `.github/workflows/README.md` documents the full disposition and the local-run commands, and flags that `weekly-regression.yml` currently hits the same bubbletea/TTY failure (but is alert-only and idempotent, so at most one open issue per week — not a flood). No source code changed, no tests changed — this is strictly a CI-topology edit.

## [1.7.41] - 2026-04-20

### Fixed
- **Feedback prompt no longer spams brand-new users on their first few launches.** Reported in the wild as "I've hardly used it yet, why are you constantly asking me to rate it?" — before v1.7.41, the TUI auto-prompt fired on every launch as long as `FeedbackEnabled` + not-yet-rated-this-version + `ShownCount < MaxShows` (default 3) — so a fresh user opening agent-deck three times in a row would see the same rating prompt back-to-back with no usage signal gating it. Fix introduces three new pacing fields in `feedback.State` (`FirstSeenAt time.Time`, `LastPromptedAt time.Time`, `LaunchCount int`) and tightens `ShouldShow` with two new gates on top of the existing preconditions: (1) the first prompt requires BOTH at least `MinDaysBeforeFirstPrompt` days since `FirstSeenAt` (default **3**) AND at least `MinLaunchesBeforeFirstPrompt` process starts (default **7**); (2) after any prompt is shown, subsequent prompts are throttled for `PromptCooldownDays` (default **14**). `RecordLaunch(state, now)` runs once per TUI process start in `cmd/agent-deck/main.go` just before `ui.NewHomeWithProfileAndMode` — it increments `LaunchCount` and seeds `FirstSeenAt` on the very first call (never overwrites it, so pacing persists across version upgrades). `RecordShown(state, now)` signature gained a `now time.Time` parameter; it now stamps `LastPromptedAt` at display time so the cooldown engages. `RecordRating` deliberately does NOT touch the new pacing fields — ShownCount still resets per-rating (so the next version can prompt again up to MaxShows times), but FirstSeenAt/LastPromptedAt/LaunchCount survive so pacing stays honest across the upgrade. `ShouldShow(state, version, now time.Time)` signature also gained a clock parameter so the pacing thresholds are fully testable under a stable-clock harness with no wall-clock flakiness. Four env vars let the test suite override the constants without rebuilding: `AGENTDECK_FEEDBACK_MIN_DAYS`, `AGENTDECK_FEEDBACK_MIN_LAUNCHES`, `AGENTDECK_FEEDBACK_COOLDOWN_DAYS` (deliberately undocumented in README — test-harness use only). JSON state file (`~/.agent-deck/feedback-state.json`) gains three new fields serialized via time.Time's RFC3339 round-trip; loading a pre-v1.7.41 file works unchanged (zero-valued time.Time is treated as "no signal yet" and blocks the prompt until `RecordLaunch` seeds `FirstSeenAt` on the next TUI start). Opt-out still wins over every pacing gate, already-rated-this-version still wins, max-shows still wins — pacing is strictly additive, never relaxing the prior gates. Tests: 14 new cases in `internal/feedback/pacing_v1741_test.go` — `TestPacing_NewUser_FirstSeenSetOnRecordLaunch`, `TestPacing_RecordLaunch_DoesNotOverwriteFirstSeenAt`, `TestPacing_1Day_3Launches_Blocked`, `TestPacing_4Days_10Launches_Shown`, `TestPacing_4Days_3Launches_Blocked`, `TestPacing_1Day_10Launches_Blocked`, `TestPacing_AfterShown_CooldownBlocks`, `TestPacing_CooldownExpired_ShownAgain`, `TestPacing_EnvOverride`, `TestPacing_OptOutWinsOverPacing`, `TestPacing_AlreadyRatedWinsOverPacing`, `TestPacing_MaxShowsWinsOverPacing`, `TestPacing_RecordRating_PreservesPacingFields`, `TestPacing_StateRoundtrip`. Legacy tests in `internal/feedback/feedback_test.go` were updated to pass a pre-seeded `FirstSeenAt` + `LaunchCount` (via a new `oldShouldShowBypass` helper) so they continue to assert the original enabled/not-rated/under-max gates without drowning in pacing boilerplate. README gains a "Feedback prompt frequency" paragraph under the existing Feedback section; `agent-deck feedback --help` grows a "Prompt frequency (v1.7.41+)" block summarizing the same rules.

## [1.7.40] - 2026-04-20

### Fixed
- **`agent-deck launch` child sessions no longer leak a second `bun telegram` poller against the conductor's bot token** (stability-ledger row **S8**): the v1.7.35 / [#680](https://github.com/asheshgoplani/agent-deck/issues/680) `TELEGRAM_STATE_DIR` strip was deliberately narrow — it only fired when the child's group was paired with a `[conductors.<group>]` block **and** that group had an `env_file`. Every `agent-deck launch` spawn outside that triangle (unrelated group, no group, no env_file) still inherited `TELEGRAM_STATE_DIR` from the conductor's shell env. With `enabledPlugins."telegram@claude-plugins-official" = true` in the profile `settings.json` (required per the v3 supported topology — flipping it off breaks the conductor, verified by the 2026-04-18 travel outage), the child's claude loaded the plugin, read the conductor's `.env` via the inherited TSD, and opened a duplicate `getUpdates` poller on the same bot token. Telegram Bot API rejects the second poller with 409 Conflict and messages drop silently. Fix lands in **two independent layers** so either one alone closes the leak:
  - **Layer 1 — shell unset.** `conductorOnlyEnvStripExpr` is replaced by `telegramStateDirStripExpr`, which emits `unset TELEGRAM_STATE_DIR` for **any** claude spawn where (1) the session title does not start with `conductor-` and (2) the `Channels` field contains no `plugin:telegram@` entry — regardless of group or env_file presence. The strip is appended to `buildEnvSourceCommand` outside the `if toolEnvFile != ""` block, so it fires even on bare `agent-deck launch` children with no config at all — the common S8 leak path.
  - **Layer 2 — exec-level `env -u`.** The final claude invocation in `buildClaudeCommandWithMessage` is wrapped in `env -u TELEGRAM_STATE_DIR ` for the same predicate. Covers all five session modes (continue, resume-with-id, resume-picker, fresh start, fresh-start-with-message). The `env` binary strips the variable from the claude child process regardless of the shell's environment state, so a corrupted env_file, a custom wrapper that rewrites the sources chain, or a future refactor that relocates Layer 1 cannot silently regress the leak.
  
  Conductor sessions (owner of the bot) and explicit per-session telegram channel owners (`Channels` containing `plugin:telegram@…`) are untouched on both layers. Non-claude tools (codex, gemini) are untouched.
  
  Regression coverage: `TestS8_ChildNoChannels_NoConfig_StripsTSD`, `TestS8_ChildNoChannels_UnrelatedGroup_StripsTSD`, `TestS8_TelegramChannelOwner_KeepsTSD`, `TestS8_NonClaudeSession_NoStrip`, `TestS8_NonTelegramChannelOwner_StripsTSD`, `TestS8_TelegramChannelOwner_ForkVariant_KeepsTSD`, `TestS8_ConductorSession_NoChannels_KeepsTSD` in `internal/session/s8_child_poller_leak_test.go` cover Layer 1; `TestS8_ExecLayer_FreshStart_UnsetTSDInvocation`, `TestS8_ExecLayer_ContinueMode_UnsetTSDInvocation`, `TestS8_ExecLayer_ResumePicker_UnsetTSDInvocation`, `TestS8_ExecLayer_Conductor_NoUnsetInvocation`, `TestS8_ExecLayer_TelegramChannelOwner_NoUnsetInvocation`, `TestS8_ExecLayer_FreshStartWithMessage_UnsetOnExecOnly` in `internal/session/s8_exec_layer_test.go` cover Layer 2. The two obsolete `TestIssue680_*` cases that asserted the narrow predicate (`NoConductorBlock_NoUnset`, `NoGroupEnvFile_NoUnset`) are reframed as `*_StripsUnderS8` with inverted assertions — the broadening intentionally subsumes them. All remaining `TestIssue680_*` and `TestPersistence_*` tests continue to pass unchanged.

## [1.7.39] - 2026-04-20

### Fixed
- **`agent-deck session restart` no longer destroys a just-created tmux scope** ([#30](https://github.com/asheshgoplani/agent-deck/issues/30)): a watchdog double-fire pattern — stop → manual `session start` → watchdog-queued `session restart` on the now-alive session — previously caused `Restart()` to tear down the fresh tmux/systemd scope regardless of current session state. Reproduced 2026-04-20 at 08:13:05 during the phase-5 resilience test against the v1.7.38 watchdog. Fix: a freshness guard in the CLI handler skips `inst.Restart()` (no-op) when the session is already healthy (`running`/`waiting`/`idle`/`starting`) AND was started within the last 60 seconds. A new persisted `Instance.LastStartedAt` JSON field carries the start stamp across CLI invocations so the guard works for the short-lived `agent-deck` process. A new `--force` flag bypasses the guard for users who genuinely want to recycle a healthy session. Scope is deliberately narrow: the check lives only in `handleSessionRestart` — `Instance.Restart()`, `Instance.RestartFresh()`, TUI restart paths, and the watchdog Python helper are unchanged. Tests: `TestShouldSkipRestart_FreshHealthy`, `TestShouldSkipRestart_StaleHealthy`, `TestShouldSkipRestart_ErrorStatus`, `TestShouldSkipRestart_StoppedStatus`, `TestShouldSkipRestart_Force`, `TestShouldSkipRestart_UnknownStartTime`, `TestShouldSkipRestart_ExactBoundary`, `TestStart_RecordsLastStartedAt` in `internal/session/restart_guard_test.go`.
## [1.7.38] - 2026-04-19

### Added
- **Declining feedback at any step now sets a persistent opt-out; agent-deck will never auto-prompt again until the user explicitly re-enables.** Builds on the v1.7.37 disclosure fix (#679): before v1.7.38, answering `N` to `Post this? [y/N]:` on the CLI, pressing `n`/Esc at the TUI confirmation step, or dismissing the dialog mid-flow would print "Not posted." and silently re-prompt on the next launch — with the same public-posting disclosure the user just declined. The opt-out also lives in a new `[feedback].disabled` field in `~/.agent-deck/config.toml` so the user can see and edit the decision (editing the file manually is honoured the same as answering `n`). Both stores are treated as authoritative: either one being "off" suppresses every passive feedback prompt (TUI auto-popup + CLI auto-trigger paths). Five opt-out triggers all land in both stores — (1) CLI `n` at rating, (2) CLI `N` at disclosure, (3) TUI stepRating `n`, (4) TUI stepConfirm `n`/Esc, (5) hand-editing `config.toml`. Re-enable path: run `agent-deck feedback` and answer `y` to the new `Feedback is currently disabled. Enable feedback and continue? [y/N]:` prompt, which clears both stores before resuming the normal rating flow. TUI `ctrl+e` still bypasses the opt-out (explicit user intent): it re-enables `state.json` in-memory before showing the dialog so the new `Show()` guard does not block the on-demand shortcut. Also fixes a latent global-pointer mutation bug surfaced while writing the tests: `session.LoadUserConfig` returned a pointer to the package-level `defaultUserConfig` when no config file existed, so mutations (e.g. `cfg.Feedback.Disabled = true`) leaked across calls; now returns an independent copy via `cloneDefaultUserConfig`. Tests: `TestV1738_CLI_DeclineAtDisclosure_SetsOptOut`, `TestV1738_CLI_ExplicitOnOptedOut_AsksReenable_DeclineExits`, `TestV1738_CLI_ExplicitOnOptedOut_AcceptReenable_ClearsBoth`, `TestV1738_OptOut_PersistsAcrossRestart` in `cmd/agent-deck/feedback_optout_v1738_test.go`; `TestV1738_FeedbackDialog_ConfirmN_SetsOptOut`, `TestV1738_FeedbackDialog_ConfirmEsc_SetsOptOut`, `TestV1738_FeedbackDialog_ConfirmY_DoesNotOptOut`, `TestV1738_FeedbackDialog_Show_NoOpWhenOptedOut`, `TestV1738_FeedbackDialog_Show_VisibleWhenEnabled` in `internal/ui/feedback_dialog_optout_v1738_test.go`; legacy `TestFeedbackDialog_OnDemandShortcut` case 2 updated to reflect the new `Show()`-guards-opt-out contract.

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
