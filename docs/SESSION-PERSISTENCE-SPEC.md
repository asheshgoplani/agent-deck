# Session Persistence Hotfix — v1.5.2

## Project overview

Agent-deck is a terminal session manager for AI coding agents (Claude, Codex, Gemini). It creates and manages tmux sessions that host long-running agent conversations. The project is at v1.5.1 (local `main`, not yet pushed). This spec defines a hotfix milestone **v1.5.2 "session-persistence"** that closes two recurring failures.

The target audience is any developer running agent-deck on a Linux host with multiple SSH sessions, where a single SSH logout currently destroys every managed session.

## Problem statement (the 2026-04-14 incident)

On 2026-04-14 at 09:08:01 local time, `systemd-logind` removed three SSH login sessions on the conductor host. At the exact same second, every `tmux-spawn-*.scope` belonging to agent-deck-managed sessions stopped. 33 sessions landed in `error` status, 39 in `stopped`, all live Claude conversations were lost. Uptime was 25 days — no reboot, no OOM, no user action on agent-deck itself.

This is the third recurrence on the same host. It must not happen again.

Root cause, confirmed in code:

1. **REQ-1 root cause — cgroup inheritance.** `agent-deck` spawns tmux as a child of the invoking shell. The tmux server lands in that shell's login-session scope. When the SSH session is removed, logind tears down the scope tree and tmux dies with it. Linger (`loginctl enable-linger`) is already active on this host, but linger only protects `user@UID.service` and its direct children — **not** tmux servers parented under a login-session scope. The codebase already has the escape mechanism: `LaunchInUserScope` in `internal/tmux/tmux.go:724` wraps spawning in `systemd-run --user --scope --quiet --collect --unit agentdeck-tmux-<name>`. But it defaults to `false` (`internal/session/userconfig_test.go:1102`) and the incident host's `~/.agent-deck/config.toml` does not override it. The feature exists and is dormant.

2. **REQ-2 root cause — restart does not resume.** When a dead session is restarted, agent-deck does have resume logic (`internal/session/instance.go:3763 Restart() → buildClaudeResumeCommand() at :4114`). `Instance.ClaudeSessionID` is persisted through stop (`session_cmd.go:286`) and survives in storage. But the resume flow only fires when `Restart()` is called on a session that was `Stop()`-ed cleanly. On the 2026-04-14 incident, tmux panes were SIGKILLed by logind — the session went `error`, not `stopped`. The error-recovery code path does not go through `Restart()`; it goes through `session start` which (a) re-invokes the start logic and (b) may or may not honor `ClaudeSessionID`. Needs audit and regression coverage.

Additionally, neither failure mode has a regression test. The user has declared this category of bug must be permanently test-gated — every PR touching tmux spawn or session start/restart must run a dedicated persistence test suite that fails the build if a dead-session recovery path regresses.

## Goals

1. Make agent-deck **survive SSH logout by default** on Linux+systemd hosts, without user configuration.
2. Make **session start and restart resume the prior Claude conversation** from any terminal state (`stopped`, `error`, cold boot), using the persisted `ClaudeSessionID` — and for custom-command sessions where that ID is empty, discover the latest JSONL transcript on disk and resume from it (REQ-7).
3. Make these behaviors **permanently test-enforced**: regression tests that replicate the 2026-04-14 incident must pass on every PR. The repo's `CLAUDE.md` must state this as a hard rule.

## Version

This is agent-deck **v1.5.2** — a hotfix milestone off v1.5.1. No breaking changes, no new user-facing features. Scope is strictly the two failures above and their test enforcement.

## Open GitHub issues relevant to this work

- None filed for this specific recurrence as of spec-writing. The conductor's `task-log.md` is the authoritative incident record.

## Requirements

### REQ-1: Default-on cgroup isolation (P0)

**Rule:** On Linux hosts where `systemd-run --user --version` succeeds, `launch_in_user_scope` defaults to `true`. On non-systemd hosts (macOS, BSD) it silently defaults to `false`. Explicit `launch_in_user_scope = false` in `config.toml` is always honored.

**Acceptance:**
- A fresh install on a Linux+systemd host spawns tmux under `user@UID.service`, verified by `systemctl status user@UID.service` showing an `agentdeck-tmux-*.scope` child.
- An SSH session that spawned the agent-deck command can `logout`, and the tmux server continues running. `tmux list-sessions` from a new SSH login returns the same server.
- Startup logs one line: `tmux cgroup isolation: enabled (systemd-run detected)` or `tmux cgroup isolation: disabled (systemd-run not available)` or `tmux cgroup isolation: disabled (config override)`.
- No behavior change on macOS/BSD hosts; the detection must not emit errors if `systemd-run` is missing.
- If `systemd-run` exists but the invocation fails (e.g. no user manager), fall back to direct tmux spawn and log a warning — never block session creation.

**Non-goals:**
- Not touching `KillUserProcesses` in `/etc/systemd/logind.conf`.
- Not adding a setup wizard prompt. The default change is silent.

### REQ-2: Resume-on-start and resume-on-error-recovery (P0)

**Rule:** Any code path that starts a Claude session for an `Instance` with a non-empty `ClaudeSessionID` must launch `claude --resume <id>` if `sessionHasConversationData()` returns true, else `claude --session-id <id>`. This applies to `session start`, `session restart`, automatic error-recovery, and conductor-driven restart after tmux teardown.

**Acceptance:**
- Stop a running Claude session via `agent-deck session stop`. Start it with `agent-deck session start`. The tmux pane shows `/resume <id>` being used and Claude loads the prior conversation. Chat history is visible.
- Kill a session's tmux server with `SIGKILL` to simulate the 2026-04-14 incident. Run `agent-deck session start` on the orphaned instance. Same result: resume with history.
- Delete the tmux server AND the hook sidecar at `~/.agent-deck/hooks/<instance>.sid`. Run `agent-deck session start`. Resume still works because `ClaudeSessionID` lives in instance storage, not only in the sidecar.
- `ClaudeSessionID` is preserved through any transition that moves the instance to `stopped` or `error`. It is only cleared on explicit delete or user-initiated `fork`.
- Existing `docs/session-id-lifecycle.md` invariants remain honored (no disk-scan authoritative binding).

**Non-goals:**
- No UI changes to show "resumable" status. If the phase has bandwidth, add a `↻` glyph, but it's P2.
- Not changing the `fork` semantics.

### REQ-3: Regression test suite enforcing REQ-1 and REQ-2 (P0)

**Rule:** A dedicated test file `internal/session/session_persistence_test.go` exists and is tagged so it runs in CI on every PR. The suite MUST contain at minimum the following named tests, each asserting the exact failure mode it is named after:

1. `TestPersistence_TmuxSurvivesLoginSessionRemoval` — spawn a session with `LaunchInUserScope=true`, record the tmux server PID, run `systemd-run --user --scope --unit=fake-login bash -c "exec sleep 1"` in a way that simulates a login-session teardown (or use `loginctl terminate-session` against a throwaway session), then verify the agent-deck tmux server PID is still alive. Skips on non-systemd hosts with a clear reason, does not pass vacuously.
2. `TestPersistence_TmuxDiesWithoutUserScope` — inverse assertion: with `LaunchInUserScope=false` and a simulated session teardown, the tmux server does die. This test pins the failure mode so we don't accidentally "fix" it by changing the scope and leave users who opted out still vulnerable. Skips on non-systemd hosts.
3. `TestPersistence_LinuxDefaultIsUserScope` — on a Linux host with `systemd-run` available, `TmuxSettings{}.GetLaunchInUserScope()` returns `true` without any config file.
4. `TestPersistence_MacOSDefaultIsDirect` — on a host without `systemd-run`, same call returns `false` and no error is logged.
5. `TestPersistence_RestartResumesConversation` — end-to-end: start a session, write a synthetic JSONL transcript to `~/.claude/projects/<hash>/`, stop the session, restart, verify the spawned command line contains `--resume <claudeSessionID>` and the JSONL path exists.
6. `TestPersistence_StartAfterSIGKILLResumesConversation` — same as #5 but the session is marked `error` after a simulated SIGKILL of its tmux server, and recovery is via `session start` not `session restart`.
7. `TestPersistence_ClaudeSessionIDSurvivesHookSidecarDeletion` — delete `~/.agent-deck/hooks/<instance>.sid`, start the session, assert `ClaudeSessionID` is still read from instance JSON storage and applied.
8. `TestPersistence_FreshSessionUsesSessionIDNotResume` — first start (no prior conversation) uses `--session-id <uuid>` only, not `--resume`. Guards against accidentally passing `--resume` with a non-existent ID.
9. `TestPersistence_CustomCommandResumesFromLatestJSONL` (REQ-7) — instance with `command` field set (custom wrapper script) and empty `ClaudeSessionID`, but a JSONL transcript present in `~/.claude/projects/<cwd-encoded>/`. Start the session, assert the spawned command contains `--resume <uuid>` where the UUID matches the JSONL basename, and assert `Instance.ClaudeSessionID` is populated to that UUID after spawn. With two JSONLs of different mtimes, assert the newer one wins.

Each test MUST be independently runnable (`go test -run TestPersistence_<name> ./internal/session/...`), MUST not depend on external network, MUST clean up any tmux servers and transcripts it creates.

### REQ-4: Documentation as enforcement (P0)

**Rule:** The repo's `CLAUDE.md` (at `<repo-root>/CLAUDE.md`) contains a section titled "Session persistence: mandatory test coverage" that:

- Lists the eight tests above by name.
- Declares that any PR modifying files in `internal/tmux/`, `internal/session/instance.go`, `internal/session/userconfig.go`, or the `session start`/`session restart` command handlers MUST have passing runs of the full `TestPersistence_*` suite, and the PR description MUST include the test output or a CI run link.
- Names the 2026-04-14 incident as the reason, so future maintainers understand why the rule exists.
- States that the `launch_in_user_scope` default may not be flipped back to `false` on Linux without an RFC.

The repo's top-level README or CHANGELOG also mentions the v1.5.2 hotfix in one line.

### REQ-5: Visual end-to-end verification harness (P0)

**Rule:** Unit tests are not enough. The repo must ship a runnable verification script at `scripts/verify-session-persistence.sh` that a human can watch in a terminal and see pass/fail with their own eyes. The script:

1. Prints a numbered checklist of the scenarios it will test.
2. Launches a real agent-deck session with a real Claude (or a stub claude binary on CI) in a real tmux server.
3. Prints the tmux server PID and its cgroup path (from `/proc/<pid>/cgroup`), so the human can confirm it is under `user@UID.service` and not a login-session scope.
4. Forces a simulated SSH-session teardown (on Linux+systemd only: pick a throwaway `systemd-run --user --scope` unit, terminate it, and verify the agent-deck tmux PID is still alive). On macOS, skip with a clear "skipped: no systemd-run" message.
5. Stops the session, restarts it, prints the exact claude command line spawned (captured via `tmux display-message -p -t ... '#{pane_current_command}'` or a test-only log line) and highlights whether it contains `--resume` or `--session-id`.
6. Ends with a green `[PASS]` banner or a red `[FAIL]` banner per scenario, and exits non-zero on any failure.
7. Usable by the user as `bash scripts/verify-session-persistence.sh` after any install of v1.5.2, to prove to themselves that the fix is live.

This script is invoked in CI AND is referenced from the CLAUDE.md section (REQ-4). If it fails, the PR is blocked.

### REQ-6: Observability (P1)

**Rule:** On startup, emit one structured log line describing the cgroup isolation decision (`enabled` / `disabled` / `unavailable`). On every `session start` / `restart`, emit a structured log line stating whether the resume path was taken (`resume: id=<x> reason=conversation_data_present`) or skipped (`resume: none reason=fresh_session`).

**Acceptance:** `grep 'tmux cgroup isolation' ~/.agent-deck/logs/*.log` and `grep 'resume:' ~/.agent-deck/logs/*.log` each return rows. No goal is to surface this in the TUI; log-level is enough.

### REQ-7: Custom-command Claude sessions resume from latest JSONL (P0)

**Rule:** When an `Instance` with `tool: claude` is started and its stored `ClaudeSessionID` is empty (common for conductor-style sessions launched via a custom wrapper script or `add --command <path>`), agent-deck MUST discover the latest JSONL under the canonical Claude Code project directory for that instance's `cwd` and:

1. If a JSONL exists: spawn `claude --resume <uuid-from-filename>` (where the UUID is the JSONL basename without extension), and persist that UUID back into `Instance.ClaudeSessionID` so future restarts do not need to re-scan.
2. If no JSONL exists: spawn fresh (`claude` with no resume flag), and capture the new `ClaudeSessionID` from hook sidecar as today.
3. The project dir is resolved per Claude Code's convention: `~/.claude/projects/<cwd-with-slashes-replaced-by-dashes>/`. Example: cwd `/home/u/.agent-deck/conductor/agent-deck` → dir `-home-u--agent-deck-conductor-agent-deck`.

**Why this matters (2026-04-15 incident):** the user's `conductor-agent-deck` session has ten JSONL transcripts on disk but `claude_session_id=""` in agent-deck storage. Every restart launches a fresh `claude` process, losing all chat history — despite other sessions (which have `claude_session_id` populated at creation via the `session add` happy path) resuming correctly. Custom-command sessions must reach the same resume guarantee.

**Acceptance:**
- Create a session via `agent-deck session add --command ./my-wrapper.sh` where `my-wrapper.sh` execs `claude` with flags. Confirm `claude_session_id` is empty in storage immediately after add.
- Send one message to establish a JSONL transcript in the Claude Code project dir.
- Stop the session. Restart it. The conversation history loads — `claude --resume <uuid>` was invoked.
- After the first successful resume, `agent-deck session show --json <id>` shows `claude_session_id` populated from the discovered JSONL.
- If multiple JSONLs exist in the project dir, the **most recently modified** one is chosen.
- If the project dir is missing or empty, the spawn is fresh and no error is raised.
- Regression test: a session whose `command` field is non-empty (custom wrapper) exercises the same resume path as a normal-command session. No code path branches on "custom command ⇒ skip resume".

**Non-goals:**
- Not changing the conductor's `start-conductor.sh` wrapper (the ops-layer fix landed 2026-04-15; REQ-7 is the structural code-layer fix so no future conductor or custom-command session ever needs a wrapper hack).
- Not scanning for resume across `tool: codex` or `tool: gemini` — those have their own transcript formats (separate future work).

## Out of scope

- Not migrating the existing 33 error / 39 stopped sessions on the user's host as part of this milestone. Recovery of those is a separate manual task.
- Not changing MCP attach/detach flow.
- Not changing the session-sharing export/import mechanism.
- Not introducing a config auto-upgrade path that rewrites user `config.toml` (too invasive). Defaults are runtime-only.

## Architecture notes

Agent-deck is a Go 1.22+ CLI + Bubble Tea TUI. Relevant packages for this spec:

- `internal/tmux/` — low-level tmux server/session spawn (`tmux.go` line 814-837 holds the systemd-run wrap).
- `internal/session/` — `Instance` struct, lifecycle (`instance.go`), user config (`userconfig.go`), storage (JSON on disk under `~/.agent-deck/<profile>/`).
- `cmd/` — CLI subcommands including `session_cmd.go`.
- Hooks live at `~/.agent-deck/hooks/<instance>.json` (sidecar for Claude Code hook integration).

The session-id binding contract is documented at `docs/session-id-lifecycle.md` and must not be violated.

## Known pain points

- On macOS CI, `systemd-run` is absent. Tests must skip cleanly, not error.
- Prior GSD attempts on this repo ran phases 1–10 and stalled at phase 11 release-plan (see `.planning.legacy-v15/`). This hotfix milestone is intentionally scoped small and standalone — do not attempt to pick up the older roadmap.
- `main` is currently >10 commits ahead of `origin/main` from the v1.5.1 bugfix batch. This milestone will add more; **do not push, tag, or open PRs.** User merges manually.

## Hard rules for all phases

- No `git push`, `git tag`, `gh release`, `gh pr create`, `gh pr merge`.
- No `rm` — use `trash` (`/usr/bin/trash`) if cleanup is needed.
- No Claude attribution in commits. Sign as "Committed by Ashesh Goplani" only if requested.
- TDD ordering per plan: every fix's test lands BEFORE the fix.
- No scope creep — if a plan wants to refactor code outside the paths named above, stop and escalate to the conductor.
- No mocking of tmux or systemd in the persistence tests — use the real binaries; skip on hosts that don't have them.

## Success criteria for the milestone

1. On the user's conductor host, after installing v1.5.2, `launch_in_user_scope` is effectively `true` without any config edit. Proof: `systemctl --user status` shows `agentdeck-tmux-*.scope` units.
2. An SSH logout cycle on the conductor host does not kill any agent-deck tmux server. Proof: manual test — conductor records PIDs before logout and after re-login.
3. `go test ./internal/session/... -run TestPersistence_ -race -count=1` passes locally on Linux.
4. `bash scripts/verify-session-persistence.sh` runs end-to-end on the user's conductor host and exits 0 with every scenario showing `[PASS]`. The user watches this run and confirms visually.
5. `git log main..HEAD --oneline` on the `fix/session-persistence` branch ends with a commit that adds the CLAUDE.md section.
6. No commit on this branch pushes, tags, or opens a PR.
