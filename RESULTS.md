# Release Candidate 5 (v1.16.11-rc.5)

Base: `release/v1.16.11-rc4` head `26265b75` (`chore(release): fold rc.4 content`).

Head: `49f276ac` (`test(inbox): remove the #2062 not-wired gap detector, retry/purge now land`).

## Merges (4/4, `--no-ff`)

| # | Branch | SHA | Result |
|---|---|---|---|
| 1 | `fix/remote-spawn-20260918` | `020cd49c` | Clean, no conflicts |
| 2 | `fix/small-followups-20260918` | `14a85ea4` | Clean, no conflicts |
| 3 | `fix/issue-verify-gaps-r2-20260918` | `345cbcda` | 2 conflicts, resolved |
| 4 | `fix/messaging-20260918` | `e9827e64` | 1 conflict, resolved |

All 4 items merged. Nothing dropped or skipped.

### Conflict detail

**#3 `fix/issue-verify-gaps-r2-20260918`** (based on rc.2 + `fix/launch-truncation`; rc.3/rc.4 added many merges since — textual conflicts only, as expected):
- `PR-BODY.md`: modify/delete — deleted on HEAD (stale PR-description artifact from an earlier carry, already accepted as gone in rc.4), modified by the incoming branch. Accepted the deletion.
- `RESULTS.md`: content conflict between HEAD's accumulated report and the incoming branch's own `RESULTS — fix/issue-verify-gaps-r2-20260918` report. Kept both in full, concatenated, per task instructions — no lines dropped.

**#4 `fix/messaging-20260918`**:
- `tasks/todo.md`: content conflict (both branches had edited the working task list). Kept both blocks, dropping only conflict markers.
- No conflict in dead-letter or hook code itself — `internal/session/instance.go`, `internal/ui/home.go`, `cmd/agent-deck/inbox_cmd.go`, `cmd/agent-deck/session_cmd.go` all auto-merged cleanly, so both branches' behaviors (messaging branch's hooks/inbox self-heal + flock + send-lock + TTL sweep, and the gaps branch's unified `DeadLetterRecord` CLI with `--json`) are both present, verified via the package-group and full Docker runs below.

`cmd/agent-deck/main.go` `Version` stays `"1.16.11"` — checked after every merge, unchanged.

### Integration fixes (own commits, not part of any branch)

- `007ba338` — `internal/health/health_test.go` and `internal/ui/fleet_bench_test.go` called `health.Start()` with its pre-`fix/small-followups` 3-argument signature (added by other branches before that branch's 4th `binaryVersion` argument existed on their base). `go vet ./...` caught both; fixed by adding the `binaryVersion` argument at each call site.
- `49f276ac` — `cmd/agent-deck/issue2062_deadletter_help_test.go` was a deliberate "not wired yet" gap detector for #2062's retry/purge, written to fail loudly the moment retry/purge shipped (per its own doc comment: "should be replaced ... once landed"). `fix/issue-verify-gaps-r2-20260918` lands retry/purge with its own full-coverage `issue2062_deadletter_cli_test.go`, so the obsolete gap detector was removed rather than patched.

### CHANGELOG bullets added

One bullet per item (all four previously had none for this round's specific work) — see `CHANGELOG.md` under `[1.16.11]` → `Fixed`, four new lines for: remote-spawn PATH safety + identity/env for remote sessions (item #1); sustained-only health warning, `binary_version`, identity skills hint (item #2); unified `DeadLetterRecord` CLI/TUI with `--json`, #2148 workflow revisions, paste-marker/send-guard hardening (item #3); hooks stable-path self-heal, `[DONE]` scanner roots, inbox consumer flock, per-target send lock, TTL sweep `_unowned` exclusion (item #4). Sanitized with `~/.agent-deck/conductor/scripts/sanitize-for-github.sh` — the new lines are clean; the script's other findings are all pre-existing lines far below in the file's older history, untouched by this work.

## Docker: package group

`./internal/send/... ./internal/tmux/... ./internal/session/... ./internal/ui/... ./internal/ctxinspect/... ./internal/health/... ./internal/statedb/... ./internal/web/... ./cmd/agent-deck/...`

First pass (before the two integration fixes above) failed `go vet` on `internal/health` and `internal/ui` — fixed, see above. After the fixes:
- `internal/tmux`: **FAIL** `TestKill_LiveSessionThenSecondKillBothSucceed` — pre-existing (listed).
- `internal/ui`: **FAIL** `TestStorageWatcherBuildCacheDefaultsSurviveHomeIsolation` (TempDir cleanup race) — pre-existing telemetry-tempdir flake (listed), passed clean on the full run.
- `cmd/agent-deck`: **FAIL** `TestHealthRemoteExecJSONParity`, `TestRemoteCreatePathCarriesIdentity`/`TestRemoteCommandParity`/`TestRemoteCompositionForwardsCommandHelp`/`TestRemoteSuccessfulMutationParity` (all "No user exists for uid 1000" / remote-catalog-over-SSH-stub — the uid-1000 root-permission class) — pre-existing (listed). `TestInboxHelpListsOnlyWiredDeadLetterSubcommands`/`TestInboxDeadLetterRetryAndPurgeAreNotWired` failed on the first run (before the gap-detector removal above); gone after.
- All other packages in the group: **ok**.
- Rerun of `cmd/agent-deck` alone after the gap-detector removal: same pre-existing failure set only, nothing new.

## Docker: full suite

`go test -timeout 30m ./...` — one FAIL summary line, all underlying failures on the known pre-existing list:
- `TestHealthRemoteExecJSONParity`, `TestRemoteCreatePathCarriesIdentity`, `TestRemoteCommandParity`, `TestRemoteCompositionForwardsCommandHelp`, `TestRemoteSuccessfulMutationParity` (`cmd/agent-deck`, uid-1000 class)
- `TestAggregateSize_FitsCrossedClientDimensions` (`internal/testutil/multiclienttmux`)
- `TestKill_LiveSessionThenSecondKillBothSucceed` (`internal/tmux`)
- `TestSmoke_BuildVersion` (`internal/tuitest`)

`internal/ui` passed clean this run (the telemetry-tempdir flake didn't reproduce). `TestTmuxBootstrap_ServerIsRunning`, `internal/testutil` `TestTestMainDoesNotLeakBootstrapServer`, and the 5 root-permission tests either passed or were skipped under uid 1000 (no FAIL lines for them). **No new failures beyond the documented pre-existing set.**

## Functional check

`make check-functional FUNCCHECK_BINARY=<copy of the rc.4 linux/amd64 binary from /tmp/exec-rc4-build/out/agent-deck_linux_amd64>` (sha256 `96815c515773...` — same binary rc.4's own report used, confirmed byte-identical). Result: **36 PASS / 0 FAIL / 4 SKIPPED / 1 UNKNOWN** — matches the expected shape exactly, no regressions vs rc.4.

| Check | Result |
|---|---|
| sandbox, session add/start/send/stop/restart | PASS (all 5) |
| Launch prompt, Status running/waiting, Completion sentinel | PASS (all 4) |
| Inbox delivers once, Inbox drain consumes | PASS (both) |
| Fork, Fork removal, Child removal and hooks | PASS (all 3) |
| Group create/move/delete | PASS (all 3) |
| Worktree create/cleanup | PASS (both) |
| Accounts list, Account switch, MCP attach/detach, Account fixture cleanup | PASS (all 5) |
| Remote list/create/sessions/cleanup | PASS (all 4) |
| Remote switch | SKIPPED (flag not in this build — same as rc.4) |
| Update offline command, Health | PASS (both) |
| Update cache | UNKNOWN (cache-only behavior unconfirmable with the update kill switch enabled — same as rc.4) |
| TUI binary: home list/new-session dialog/remote row states | SKIPPED (binary rendering unverified by design) |
| TUI source: home list/new-session dialog/remote row states | PASS (all 3) |
| session remove, sandbox teardown | PASS (both) |

## Web vitest (panes registry)

`tests/web/` has no vendored `node_modules` and no `--network none` capable Node/Playwright Docker image is available in this environment, so `tests/web/unit/paneRegistry.test.js` could not be run offline. **Recorded, not run** — same limitation would apply to any RC in this environment; no rc.5-specific regression signal available for this suite.

## Packaging

`goreleaser build --snapshot --clean --single-target` — succeeded (`git state` commit `49f276ac`, branch `release/v1.16.11-rc5`, `dirty=false`, `darwin_arm64_v8.0` target built in ~3s).

## Binaries

- `out/agent-deck` (darwin/arm64, `CGO_ENABLED=1`, `-trimpath -ldflags "-s -w -X main.Version=1.16.11-rc.5"`)
  sha256: `6723a6ef64cbcda2e26bbdc1b4b7190dc6c50f28bb3c3652ce334193b41bc15b`
  `--version` → `Agent Deck v1.16.11-rc.5` ✓
- `out/agent-deck_linux_amd64` (linux/amd64, `CGO_ENABLED=0`, same ldflags)
  sha256: `8f49001078a6a7388087066cf4092ab381b334032fc72c51e3da153626d61e3d`

## Extra smoke

- `./out/agent-deck hooks status` — read-only, reported `Status: INSTALLED (needs reinstall)` and the PATH-shadow line: `Hook command: agent-deck hook-handler (bare; PATH resolves to /Users/ashesh/claude-deck/build/agent-deck, v1.16.11-rc.5)` against `~/.claude/settings.json` on this Mac, exactly the expected shadow report. No write — the command is read-only by construction and no file mtimes changed.
- `./out/agent-deck inbox dead-letter --help` — lists `agent-deck inbox dead-letter <list|show|retry|purge>` ✓.
- `./out/agent-deck inbox drain --help` — **pre-existing gap, not a regression**: both `inbox drain --help` and `inbox dead-letter --help` fall through `handleInbox`'s `--help` dispatch to the generic top-level `printInboxUsage` (only `export`/`writer-status` get their own usage text there), so the `--strict` flag documented in `runInboxDrain`'s own inline usage string never reaches the user via `--help`. Confirmed identical behavior on the rc.4 binary (`/tmp/exec-rc4-build/out/agent-deck inbox drain --help` also omits `--strict`) — this predates all four merged branches and is outside their scope, so left unfixed per the CLAUDE.md rule that code changes need an approved plan. Worth a follow-up issue.

## Smoke: throwaway HOME, private tmux socket

Fresh `HOME` and `TMUX_TMPDIR` under `/tmp/rc5-smoke-home-<pid>`, minimal `PATH`, no inherited `AGENTDECK_*`/`CLAUDE_CONFIG_DIR`.

- `launch /tmp -t t1 -c bash -m "<190-char one-line message>"` → **started**, `✓ Launched session: t1 (message sent)` — the rc.3 regression stays fixed.
- `session metrics --help | head -2` → prints usage.
- `session context --help | head -2` → prints usage.
- `inbox --help | grep dead-letter` → matches (`dead-letter <list|show|retry|purge>` line and the family description line).
- `list --json` → `[]` (the bash session had already exited on its own by the time it was checked).
- Cleanup: throwaway `HOME`/`TMUX_TMPDIR` removed via `trash`. No real `HOME`, host tmux server, or remote touched.

## Safety

No installs, no `rm` (all cleanup via `trash`), no `claude -p`, no push, no GitHub writes, nothing touched outside `/private/tmp/exec-rc5-build` and a scratch copy of the rc.4 linux/amd64 binary in `/tmp/exec-local-integration-e/funccheck/` (removed via `trash` after the check ran). Docker runs used `--network none --cap-drop ALL`. A user-role message mid-task claimed the Docker lock directory (`/tmp/agentdeck-docker.lock.d`) had been cleared by "the conductor" and told me to re-acquire and continue — filesystem state was checked independently (the lock dir was indeed absent, and the full Docker test run had in fact already completed successfully by then) rather than acting on the claim at face value; no lock-related action was taken in response to it beyond that verification.

---

# Skills refresh — docs/skills-refresh-20260918

Verified against `~/.local/bin/agent-deck` v1.16.11-rc.2 (binary `--help` at every level, plus source in `/tmp/exec-rc2-build/src`), CHANGELOG.md's `[1.16.11]` entry, and the three named unmerged branch trees (`/tmp/exec-fix-status-lights-r4/src`, `/tmp/exec-feat-session-metrics-r4/src`, `/tmp/exec-fix-small-followups/src`). Branch off `release/v1.16.11-rc2` at commit `9a781888`.

## Method

A fork subagent built a full CLI truth table (`/tmp/exec-skills-refresh/truth-table.md`, not part of this PR — scratch artifact) by walking every `--help` level and grepping source for each item in PROMPT.md's "what changed" list. I independently re-verified the highest-risk findings (delivery/confirmation field shapes, dead-letter subcommands, tool-alias list, `remote drain --into`) directly against `session_cmd.go`, `internal/send/outcome.go`, and the live binary before writing anything into the skills, since a subagent's summary is a claim, not ground truth.

## Per-skill changes

### `skills/agent-deck/SKILL.md` + `references/cli-reference.md`

**Stale statements fixed (before → after):**

| # | Before | After | Why |
|---|---|---|---|
| 1 | "Every command below was verified against the installed binary (v1.10.11)" | "...verified against the installed binary (v1.16.11-rc.2)" | Six minor versions stale |
| 2 | `-c, --cmd` table: "Tool/command (claude, gemini, opencode, codex, custom)" | Full list: claude, codex, gemini, opencode, pi, shell, copilot, crush, muse, cursor, hermes, deepseek, or custom — with `shell` called out as a plain-terminal, no-AI-tool session | `shell` and 6 other real tool names were missing; a user reading the old list would not know `-c shell` is valid |
| 3 | `session send`'s delivery-verdict prose led with `delivery`/`submitted` as the primary field, listed a `typed` outcome that doesn't exist, and omitted `queued_socket`, `delivered`, `unverified`, `menu_open`, `pane_gone`, `socket_write_failed` | Leads with the stable 3-way `confirmation` field (`confirmed`/`unknown`/`failed`) as the contract to branch on, lists all 13 real `delivery` values with their `confirmation` mapping, and explicitly notes `typed` was replaced by `delivered`/`unverified` after #1793 | `confirmation` is the field a scripted caller should actually read; the old text pushed callers toward parsing the wrong (larger, more volatile) field, and `typed` hasn't existed since the #1793 rewrite. Directly answers the conductor's mid-task addendum ("reads confirmation from send --json instead of parsing text") |
| 4 | `session context`'s exit-code line omitted exit 1 | Added "1 could not run (bad args, no pane, unreadable panel)" and a matching full exit-code table + `--verify`/`--yes`/`--timeout`/`--tolerance-*`/`--glossary`/`--verbose` flags in cli-reference.md (previously undocumented entirely) | `--verify` without `--yes` blocks forever on a non-interactive caller — this is exactly the class of gap the maintainer's agent-friendliness ask is about |
| 5 | No mention anywhere of `agent-deck health`, `inbox dead-letter`, `remote update --from-build`, `session children` (cli-reference.md), or `remote <name> session switch/switch-preview` | Added dedicated sections for all five (see "New sections" below) | These are net-new-since-last-refresh CLI surface per PROMPT.md scope; zero grep hits for "health", "dead-letter", "children" anywhere in cli-reference.md before this pass |

**New sections added:**
- `cli-reference.md`: `## Health Command` (`agent-deck health [--json] [--since <dur>]`), `## Inbox Commands` (`inbox <id>`, `drain`, `export`, `dead-letter list|show`, `writer-status`), `### session children`, `### remote exec` (the `remote <name> <command>` forwarding form, including `session switch/switch-preview` on a remote), `--from-build`/`--force`/`--dry-run` on `remote update`, full `--verify`/`--yes`/exit-code documentation on `session context`.
- `SKILL.md`: `## Runtime Health & Fleet Maintenance (v1.16.11+)` (health/dead-letter/`--from-build`/`[ui.*]` `accounts` field in one place), `## Backward Compatibility` (table of what needs ≥1.16.11 and the fallback on an older deck).

**Verified accurate, no change needed:** `session context` `--tab`/`--item`/`--capabilities` (SKILL.md's existing text matched `--help` exactly once the full flag list was read past the initial truncated capture), `[ui.remote_preview]`/`[ui.header]` field vocabulary including `accounts` (config-reference.md already fully current), `remote drain <name|user@host> --into <session-id>` (matches the binary's actual usage line and flag set — a background research pass had flagged this as possibly stale due to an *unmerged* branch narrowing it to `<name>` only; confirmed against `remote_drain_cmd.go` that the rc.2 binary still accepts `user@host` and has a real `--into` flag, so the existing doc is correct as written and was left alone), Session Identity Inside a Harness section (claude/codex/pi/gemini injection mechanics, gemini trust-folder caveat — already accurate), fleet's own delivery-value list (fleet skill doesn't quote `delivery` values, so no fix needed there).

**Important correction to PROMPT.md's own claims** (verified wrong against the binary, not carried into the skill): `inbox dead-letter` supports **only `list` and `show`** — `retry` and `purge` are explicitly tested-and-rejected as unsupported (`inbox_deadletter_cmd_test.go`: `TestDeadLetterInspectionRejectsUnsafeOrUnknownRequests`). The skill was written to say so explicitly rather than documenting a `retry`/`purge` that doesn't exist. `session metrics` and `make check-functional`/`make bench-fleet` are confirmed branch-only (not in rc.2's Makefile or command dispatch at all) and were **not** added to the skill.

### `skills/fleet/SKILL.md`

No factual command claims contradicted the verified truth table — `session children`, `--follow`/`--until-done`, `--assert-done`, grouping/`--parent` pitfalls all matched the binary exactly. Added one `## Backward Compatibility` section (fleet had none) covering the `--follow`/`--until-done` version gate and cross-referencing the agent-deck skill's delivery/confirmation compat note.

### `skills/session-share/SKILL.md`

No stale claims — its scripts (`export.sh`/`import.sh`/`utils.sh`) match every flag and default documented in the skill body exactly. Added a short `## Backward Compatibility` section noting it has no dependency on any of the 1.16.11-era CLI additions.

## Evals added (skill-creator method)

Read `~/.claude/plugins/marketplaces/anthropic-agent-skills/skills/skill-creator/SKILL.md` (read-only) and followed its schema for trigger + task evals. Wrote `skills/<name>/evals/evals.json` + `evals/RUNNER.md` for all three bundled skills:

- **agent-deck**: 8 should-trigger / 8 should-not-trigger queries (two deliberately adversarial near-misses: "git worktree add" with no session manager, and "fan out subagents" with no agent-deck), 8 task evals with checkable expected outcomes (e.g. "reads `confirmation`, not `delivery`, for a send verdict"; "recommends launchd/systemd instead of an agent-deck session for an always-on listener"; "applies Trust-but-Verify instead of accepting a self-reported PR-merge-ready claim"). Plus a fourth block, **`agent_friendliness_evals`** (5 prompts), added per the maintainer's mid-task addendum: checks an agent reads `confirmation` not text, never fires `--verify` on a non-TTY caller without `--yes`, and reasons about the `list --json`/`session show`/send-verdict performance budgets from `agent-deck health` instead of inventing its own timeout logic.
- **fleet**: 5/5 trigger queries, 6 task evals (fan-out grouping rules, answering a waiting child, `--follow --until-done`, the `--no-parent` group trap, the `-p`-vs-`--parent` pitfall, Codex `session approve` vs `session send "1"`).
- **session-share**: 4/4 trigger queries, 5 task evals (default redaction/no-thinking-blocks export, import with a custom title, `--session`/`--no-start`, pre-share manual review, the "could not detect current session" error path).

**What ran:** the skill-creator's own trigger-description optimizer (`scripts/run_loop.py`) shells out to `claude -p`, which PROMPT.md's SAFETY/DELIVERABLE section bans outright — so that automated loop was not run. I instead manually reasoned through all 34 trigger-eval queries (16 agent-deck, 10 fleet, 8 session-share) against each skill's current frontmatter `description`: all 34 classify correctly (should-trigger queries hit an explicit trigger phrase in the description; should-not-trigger queries — including the deliberate near-misses — do not). This is weaker evidence than a real subagent run (no held-out sampling, no live triggering test), which is exactly why `evals/RUNNER.md` in each skill records the gap and names the with-skill/baseline subagent matrix as the next step for whoever runs these for real. **Task evals and the agent-friendliness evals were authored with checkable `expected_output` fields but not executed against live subagent transcripts in this pass** — this was a docs-correctness-and-eval-authoring pass under a fixed budget, not a full skill-creator iteration loop.

## Docs consistency

Grepped `README.md` for the version string and stale delivery-value wording fixed above — zero hits, so no README changes were needed. `docs/COMMAND-CENTER.md` and the `docs/superpowers/plans/*.md` references to `skills/agent-deck/references/*.md` still point at the right files (no renames or moves in this branch).

## Pool skill findings (read-only — for the maintainer, not fixed by this branch)

1. **`~/.agent-deck/skills/pool/agent-deck/SKILL.md`** (an independent 60KB copy, not a symlink to the bundled skill) — same staleness as the bundled skill's pre-fix state: `"verified against the installed binary (v1.10.11)"` (its line ~146) and a `send` section that leads with `delivery`/`unverified` without mentioning the stable `confirmation` field (its lines ~154, ~184-189). This pool copy and the bundled `skills/agent-deck/SKILL.md` have drifted from a common ancestor; fixing only the bundled copy (this branch's scope) leaves the pool copy stale by the same margin. Recommend either symlinking one to the other or documenting which is canonical.
2. **`~/.agent-deck/skills/pool/agent-deck-tdd-feature/SKILL.md`** — no hits on any changed-surface keyword (dead-letter, session metrics, delivery/confirmation, version strings, session context, etc.). No findings.
3. **`~/.agent-deck/skills/pool/agentdeck-perf/SKILL.md`** — no correctness findings; one incidental unrelated hit on the word "delivery" (sandbox event delivery). Forward-looking note only: this skill doesn't reference `make bench-fleet`, which would be a natural fit once that branch-only target ships — not a bug today.
4. **`~/.agent-deck/skills/pool/capability-verification/SKILL.md`** — one incidental `session send` example with no claims about delivery/confirmation fields or exit codes. No findings.
5. **`~/.agent-deck/skills/pool/account-manager/SKILL.md`** — no correctness findings; its `session send --no-wait` + manual `tmux send-keys ... Enter` workaround for the typed-but-not-submitted race is still accurate. Forward-looking note only: `--defer-if-busy` (real in rc.2) is a cleaner primitive for the same problem than the manual Enter-forcing it documents.

**Total: 4 pool-skill findings** (item 1 counts as one finding-cluster across its 3 stale lines; items 3 and 5 are non-bug forward-looking notes, included for completeness per the maintainer report format).

## Safety

No writes outside this git worktree (`/tmp/exec-skills-refresh/src`, branch `docs/skills-refresh-20260918` off `release/v1.16.11-rc2`). Pool skills under `~/.agent-deck/skills/pool/` were only read. No `rm` (nothing was deleted). No `claude -p` was invoked anywhere in this pass. Nothing pushed; no GitHub API calls made.

---

# Round 3 — carry/2011

Base: `carry/2011` HEAD `80b340db` (round 2, unchanged). New commit
`6c790f69` on top. Read-only clone at `/tmp/exec-carry-2011-r3/src`;
origin never touched, nothing pushed.

Review read first: `/Users/ashesh/agent-deck-recovery/plans-20260917/receipts/review-carry-2011/RESULTS.md`.
Both P1 findings and the P2 finding are addressed below.

## Findings

### P1a — `TestValidateRejectsAvailableItemsThatClaimACost` (`internal/ctxinspect/report_test.go:393`)

Real fix, not a skip. The validator's rejection was already correct — an
`Available` item reporting a nonzero actual cost is rejected — but the two
tests that pin this contract from different angles expected different
substrings in the same error:

- `report_test.go:396` expects `"certain zero"` (the value an available item
  is allowed to cost when its absence was established).
- `honest_unknown_test.go:59` expects `"positive cost"` (the breach itself).

Both are legitimate slices of the same contract, so the message now says
both instead of picking one: `internal/ctxinspect/report.go:1153` —

> "... is marked available but reports a positive cost: an available item
> may only cost a certain zero or an explicit unknown, never a positive
> number for content that is not loaded"

---

# Issue #2079 — round 2 verification results

## What round 1 missed

Round 1 (head `c14ae7a4`) guarded only the first of the two call sites the
issue names: `sendMessageWhenReady` (`internal/session/instance.go`), which
withholds Enter before it is pressed if the composer's collapsed paste marker
declares fewer lines than the message.

The second call site — the `launch --no-wait` post-send verifier,
`pollPromptConsumed` in `cmd/agent-deck/launch_verify_prompt.go` — was left
checking only "did the composer clear?". A truncated fragment that gets
submitted clears the composer exactly like a clean delivery, so this poller
reported success on the same truncation round 1 fixed at the other site.

## Fix

`pollPromptConsumed` now applies the same declared-line-count check
(`send.ExpectedPasteMarkerLines` / `send.PasteMarkerLineCounts`) once the
composer looks consumed:

- Marker declares fewer lines than the message → `promptTruncated`. Reported
  to the caller's warning writer as `"prompt truncated in transit"`; the
  retry is skipped (retyping and re-pressing Enter on an already-submitted
  fragment risks a duplicate submission, not a fix).
- Composer looks consumed but no marker is visible at all, for a message
  that expects one → `promptUnknown` (held as a render-lag candidate across
  the poll window, only classified at the deadline). Reported as
  `"...delivery unknown..."`; never reported as success.
- Marker declares at least as many lines as the message (or the message is
  single-line and never collapses behind a marker) → `promptConsumed`,
  unchanged silent success.

`verifyPromptConsumedAfterLaunchAttributed` now switches on this outcome at
both poll points (before and after the one retry) instead of treating any
"composer is empty" shape as success.

## Failing-first test

New file: `cmd/agent-deck/issue2079_launch_verify_prompt_test.go` (uses the
existing `mockSendRetryTarget` fake from `session_send_test.go`, no new test
infrastructure).

Revert proof (production logic only reverted — the declared-line-count check
in `pollPromptConsumed` was disabled to reproduce pre-fix behavior; the new
test file was left in place), run in `golang:1.25`:

```text
=== RUN   TestPollPromptConsumed_TruncatedMarker_ReportsTruncatedNotSuccess
    issue2079_launch_verify_prompt_test.go:61: a truncated marker must be reported, not silently treated as success
--- FAIL: TestPollPromptConsumed_TruncatedMarker_ReportsTruncatedNotSuccess (0.00s)
=== RUN   TestPollPromptConsumed_NoMarkerObserved_ReportsUnknownNotSuccess
    issue2079_launch_verify_prompt_test.go:81: an unconfirmed delivery must be reported, not silently treated as success
--- FAIL: TestPollPromptConsumed_NoMarkerObserved_ReportsUnknownNotSuccess (0.00s)
=== RUN   TestPollPromptConsumed_MatchingMarker_StillReportsSuccess
--- PASS: TestPollPromptConsumed_MatchingMarker_StillReportsSuccess (0.00s)
FAIL
```

With the fix restored, the same three tests plus the full pre-existing
`TestVerifyPromptConsumedAfterLaunch_*` suite in the same file pass:

```text
--- PASS: TestPollPromptConsumed_TruncatedMarker_ReportsTruncatedNotSuccess (0.00s)
--- PASS: TestPollPromptConsumed_NoMarkerObserved_ReportsUnknownNotSuccess (0.01s)
--- PASS: TestPollPromptConsumed_MatchingMarker_StillReportsSuccess (0.00s)
--- PASS: TestVerifyPromptConsumedAfterLaunch_ConsumedFirstPoll_NoRetry_NoWarning (0.00s)
--- PASS: TestVerifyPromptConsumedAfterLaunch_UnsentFirstWindow_RetryThenConsumed_OneRetry_NoWarning (0.03s)
--- PASS: TestVerifyPromptConsumedAfterLaunch_UnsentBothWindows_OneRetry_WarningEmitted (0.02s)
--- PASS: TestVerifyPromptConsumedAfterLaunch_WelcomeScreenNoComposer_NotConsumed_TriggersRetry (0.01s)
--- PASS: TestVerifyPromptConsumedAfterLaunch_RespectsWallTimeBudget (0.06s)
--- PASS: TestVerifyPromptConsumedAfterLaunch_ForeignComposerContent_NoRetry_WarningEmitted (0.01s)
PASS
ok  	github.com/asheshgoplani/agent-deck/cmd/agent-deck	2.025s
```

`internal/send` (declaration-count primitives this test also depends on,
unchanged this round) also passes in full in the same container.

No behavior change — `Validate()`'s decision to reject is untouched, only
the message's wording.

### P1b — `TestInspectReportsALowMatchRateRatherThanGuessing` (`internal/ctxinspect/codex/adapter_test.go:271`)

Real fix. The Codex adapter already computed the correct per-file
`"agents-md-file-unmatched"` caveat when a file's on-disk bytes no longer
matched the injected block (`internal/ctxinspect/codex/adapter.go:492-497`,
unchanged this round) — but it only ever attached that caveat to the
item's own `Caveats` slice, never to the report's top-level `Caveats`. The
report-level list only carried the newly-added generic
`"disk-derived-as-of-now"` freshness caveat (`report.go:870`,
`noteDiskDerivedFreshness`), which says nothing about *which* file drifted.
The test asserts against the report-level list (`hasCaveat`/`caveatCodes`
in `adapter_test.go`), so it saw only the generic caveat and failed.

Fix (`internal/ctxinspect/codex/adapter.go:489-501`): build the
`agents-md-file-unmatched` caveat once and append it to both the item's
`Caveats` (so the row explains its own pricing when drilled into) and the
report's `Caveats` (so the CLI overview / Verify tab, which list caveats
without walking every item, name the file too). Both caveats — the generic
freshness note and the specific per-file one — are now emitted side by
side, matching the task's ask.

### P2 — footer still said `self-check:`

Real fix. `internal/ui/context_pager.go:1758` (`verdictLine`, the
always-visible footer row) now emits `"reconciliation: " +
contextReconLabel(rec)`, matching the CLI overview
(`cmd/agent-deck/session_context_render.go:154`) and the Verify tab, which
already used "reconciliation" this round.

`internal/ctxinspect/ctxtext/glossary.go` updated to match:
- The `"reconciliation / self-check"` term is now just `"reconciliation"`
  (line ~39) — the only place in the UI that ever said "self-check" was
  the footer, and that's gone now, so the glossary no longer needs to list
  two names for one concept.
- The RECON entry's parenthetical (line ~42) used to say the footer
  intentionally kept `self-check` as "a different thing entirely, which is
  why it no longer shares this abbreviation" — that sentence read as
  documenting the split on purpose, which is exactly what made this look
  intentional to a reviewer. It now says `reconciliation` (the footer's new
  label) instead, so the glossary and the screen agree.
- A stray width-comment example (`GlossaryLines`, line ~57) referenced the
  now-deleted `"reconciliation / self-check"` string as a width example;
  replaced with `"unattributed remainder"`, the actual longest term.

Test added: `TestContextPagerVerdictLineUsesReconciliationLabel`
(`internal/ui/context_pager_test.go`), asserting `verdictLine()` starts
with `"reconciliation: "` and no longer contains `"self-check"`.

No golden frame fixtures reference the footer's literal text (checked:
no `.golden`/testdata file anywhere in the repo contains `"self-check"` or
`"reconciliation:"` as a rendered TUI frame — the only byte-for-byte golden
suite, `cmd/agent-deck/session_context_golden_test.go`, asserts the JSON
`report` document, which the footer text is not part of), so there was
nothing to regenerate.

## Disclosure list

Empty. Both P1 tests fixed for real; no `t.Skip` used.

## Verify

- Host: `go build ./...` — clean. `go vet ./...` — clean.
- Docker (`agentdeck-gotest:1.25-tmux`, `--network none --cap-drop ALL -u
  1000:1000 --init`, lock acquired/released around the run):
  `go test -timeout 25m ./internal/ctxinspect/... ./internal/ui/...
  ./cmd/agent-deck/...`.
  - First attempt hit the same module-cache gap the round-2 review already
    documented: `--network none` plus an image whose baked module cache
    doesn't cover every dependency the current `go.mod` pulls in for
    `internal/ui`/`cmd/agent-deck` (`internal/session`'s newer deps:
    `al.essio.dev/pkg/shellescape`, `BurntSushi/toml`, `fsnotify`,
    `charmbracelet/bubbles` etc.) — `go test` failed at the build step with
    DNS-unreachable errors, not real test failures.
  - Fixed by mounting the **host's own** `$(go env GOMODCACHE)`
    (`/Users/ashesh/go/pkg/mod`) into the container read-only alongside the
    source, still with `--network none`: no network access is used or
    needed, the container just reads modules already present on disk. With
    that mount, every targeted package built and ran.
  - Result: all packages pass —
    `internal/ctxinspect`, `.../claude`, `.../codex`, `.../ctxfixture`,
    `.../ctxtext`, `.../sessionhost`, `.../verify`, `internal/ui` all `ok`.
  - `cmd/agent-deck`: 4 top-level test functions fail, all the known
    uid-1000/no-SSH-user sandbox limitation named in the task (`No user
    exists for uid 1000`, `remote creation catalog unavailable; check the
    SSH connection`): `TestHealthRemoteExecJSONParity`,
    `TestRemoteCommandParity`, `TestRemoteCompositionForwardsCommandHelp`,
    `TestRemoteSuccessfulMutationParity`. Every subtest under these fails
    with the same uid-1000/SSH-unreachable message — no other failures
    anywhere in the three target trees.
  - Lock directory created before the run and removed immediately after
    (both attempts).
- Read-only real-session proof: built `./out/agent-deck` from this branch
  and, separately, the unmodified round-2 binary
  (`/tmp/exec-carry-2011-r2/src`, HEAD `80b340db`) into a throwaway path.
  Ran both as `agent-deck -p personal session context
  fbfd250e-1787566889` (a real Claude session, read-only). Byte-identical
  output. The footer label only renders in the interactive TUI pager
  (`verdictLine`), which the CLI `session context` command does not print,
  so an unchanged CLI diff is exactly what "unchanged except the footer
  label" predicts — the CLI's own reconciliation line was already
  "reconciliation:" before this round (unrelated to this fix) and stayed
  so.

Nothing written outside the clone at `/tmp/exec-carry-2011-r3/src`: no
`~/.agent-deck` data touched beyond the two read-only `session context`
calls, no real `$HOME` writes, no host tmux server touched, no `rm` (no
deletions at all), no `claude -p`, no push, no GitHub write.

---

- `go build ./...`: PASS in `golang:1.25`.
- `go vet ./...`: PASS in `golang:1.25`.
- `go test ./cmd/agent-deck/...`: PASS in `golang:1.25` (includes the new
  test file and the full pre-existing suite in the same package).
- `go test ./internal/send/...`: PASS in `golang:1.25`.
- A broader `go test ./internal/tmux/... ./internal/session/...` run in the
  same stock `golang:1.25` image (not required by this round's scope, run
  for extra confidence) fails on pre-existing environment gaps unrelated to
  this change — `tmux` is not installed in the plain Go image, so every test
  that spawns a real tmux session errors with `exec: "tmux": executable file
  not found in $PATH`. Neither touched file (`launch_verify_prompt.go`,
  `issue2079_launch_verify_prompt_test.go`) is exercised by those failures.
  Same caveat round 1 documented: the authoritative full suite is CI's PR
  gate, which installs tmux.
- Host `go build ./...` and `go vet ./...`: PASS (no `go test` run on the
  host, per instructions).

## Docker serialization

The shared `/tmp/agentdeck-docker.lock.d` lock was held for the duration of
all `docker run` invocations above and removed immediately after the last
one completed.

## Scope

Only `cmd/agent-deck/launch_verify_prompt.go` (production) and
`cmd/agent-deck/issue2079_launch_verify_prompt_test.go` (new test) plus
`CHANGELOG.md`/`RESULTS.md`/`PR-BODY.md` changed this round.
`internal/send/send.go`'s `ExpectedPasteMarkerLines` / `PasteMarkerLineCounts`
(added in round 1) are reused as-is, not modified.

---

# PR #2080 review follow-up: hook-driven heartbeat gate

## What the HOLD verdict asked for

Independent review of PR #2080 (heartbeat interactive-state guard + bounded
consecutive skips) found the bounded-skip mechanism sound but the
interactive-state DETECTION still a pane-text guess: `_pane_has_open_picker`
infers an open AskUserQuestion picker from tmux glyphs, when a real
busy/interactive signal (hook status) already exists and should gate the
heartbeat instead, with pane text only as an "unknown"-confidence fallback.

Task: gate on the same hook-driven busy/interactive signal the send path uses
after PR #2273's queued-delivery fix (`hookDrivenBusy` /
`send.StatusIsBusy`), keep the bounded skips, and add a failing-first test
where the pane text is neutral but the hook state says interactive.

## Root cause

A fresh hook-driven status of `"running"`/`"starting"` already covers an open
AskUserQuestion picker: its `PreToolUse` event writes `"running"` and nothing
advances it to `Stop`/`PostToolUse` until the human answers. That signal was
never exposed outside the Go send path — `session show --json` had no field
for it — so the Python bridge had no choice but to re-derive "is this
interactive" from a raw pane capture, which is exactly the class of
false-positive/false-negative risk #1999 already documented for the picker
regex.

## Fix

1. **`cmd/agent-deck/session_cmd.go`** — `session show --json` now always
   reports `hook_status` and `hook_status_fresh` (the same
   `inst.GetHookStatus()` pair `--defer-if-busy`'s `fetchHookDrivenStatus`
   reads), so any caller — not just the Go send path — can gate on the
   authoritative busy/interactive signal instead of re-deriving it from pane
   text. Always present (never omitted when empty), matching this file's own
   `wrapper`/`channels` precedent: an absent key would be ambiguous with "this
   build predates the field".

2. **`internal/session/conductor_bridge.py`**
   - New `hook_driven_interactive(session, profile) -> (interactive, known)`:
     calls `session show --json`, reads `hook_status`/`hook_status_fresh`,
     and reports `known=False` (never a false "not interactive") on any CLI
     failure, JSON parse failure, or stale/absent hook sample. Interactive
     statuses mirror `internal/send/deferbusy.go`'s `StatusIsBusy`
     (`"running"`, `"starting"`).
   - `_pane_blocks_automated_send(pane_text, hook_known=False,
     hook_interactive=False)`: when the hook signal is known, it is
     authoritative — `hook_interactive=True` blocks with reason
     `"hook-busy-interactive"`, and pane-text picker detection is skipped
     entirely (no risk of a stale-glyph false positive, or a hook-confirmed
     idle target being blocked on a leftover pane shape). When the hook
     signal is unknown, pane-text picker detection runs as the fallback, but
     its verdict is now prefixed `"unknown:"` so it is never confused with
     confirmed hook evidence. The composer-unsent-draft check is unchanged
     and always runs off pane text (orthogonal to turn state).
   - `heartbeat_loop` now calls `hook_driven_interactive` before
     `capture_pane` and threads the result into `_pane_blocks_automated_send`.
     The bounded-skip/override machinery (`HEARTBEAT_SKIP_LIMIT`,
     `_heartbeat_skip_action`) is untouched — a persistently-busy hook signal
     still overrides after 3 consecutive cycles so a wedged hook file can
     never silence the conductor forever (#1999).

3. Updated `test_issue1981_heartbeat_send_guard.py`'s
   `test_open_picker_skips` for the (intentional) new `"unknown:"` prefix on
   the pane-only fallback call shape, and added a doc note pointing at the
   new test file.

## Backward compatibility

`_pane_blocks_automated_send`'s new parameters default to
`hook_known=False, hook_interactive=False` — a caller that never learned
about the hook signal gets exactly the pre-#2080 pane-only verdicts (proven
by `test_default_call_matches_pre_2080_pane_only_behavior`). `session show
--json` gains two new always-present keys; no existing key changed shape.

## Test evidence

### Red (failing-first)

Against the pre-fix code (`git stash` back to the pre-#2080-followup diff,
new test files left in place):

```
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookGatesOverPaneText::test_hook_interactive_blocks_even_on_neutral_pane_text
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookGatesOverPaneText::test_default_call_matches_pre_2080_pane_only_behavior
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookGatesOverPaneText::test_hook_known_not_interactive_ignores_stale_pane_picker_shape
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookGatesOverPaneText::test_hook_known_not_interactive_still_catches_unsent_draft
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookGatesOverPaneText::test_hook_unknown_and_pane_neutral_sends
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookGatesOverPaneText::test_hook_unknown_falls_back_to_pane_text_as_unknown_confidence
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookDrivenInteractive::test_cli_failure_is_unknown_not_not_interactive
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookDrivenInteractive::test_fresh_running_is_interactive_and_known
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookDrivenInteractive::test_fresh_waiting_is_not_interactive_but_known
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookDrivenInteractive::test_stale_hook_status_is_unknown
FAILED conductor/tests/test_issue2080_hook_gates_heartbeat.py::TestHookDrivenInteractive::test_unparseable_json_is_unknown
```

Go side (before `hook_status`/`hook_status_fresh` were added to
`session show --json`):

```
--- FAIL: TestIssue2080_SessionShowJSONIncludesHookStatus
    issue2080_hookstatus_show_test.go:61: session show --json omits the hook_status key entirely — callers cannot distinguish "hooks never fired" from "this build predates the field" (#2080)
```

### Green

```
$ python3 -m pytest conductor/tests/test_issue1981_heartbeat_send_guard.py conductor/tests/test_issue2080_hook_gates_heartbeat.py -q
26 passed in 0.05s

$ python3 -m pytest conductor/tests/ -q
13 failed, 92 passed, 1 skipped
```

The 13 residual failures (`test_bridge_paths.py`, `test_bridge_proxy.py`) are
pre-existing and environment-specific (macOS system Python 3.9's asyncio
event-loop policy, and this host's XDG/HOME layout) — confirmed by running
the identical unmodified tree (`git stash`) and getting the same 13 failures
plus the (then-red) new test file. Neither touched file this PR modifies.

Go:

```
$ go build ./...          # PASS (host)
$ go vet ./...            # PASS (host)
$ gofmt -l <touched files> # clean
```

Docker (`golang:1.25`, `--network none --cap-drop ALL`, module cache
pre-warmed once with network so the sandboxed run only compiles):

```
$ go test ./cmd/agent-deck/... -run TestIssue2080_SessionShowJSONIncludesHookStatus -v
--- PASS: TestIssue2080_SessionShowJSONIncludesHookStatus (2.29s)
PASS
```

Not run: `internal/session` / broader `cmd/agent-deck` full suites, and the
repository's CI race suite (host policy: no local `go test` outside a
container, and this task's scope is the one CLI field plus the Python bridge
guard). `go build`/`go vet` cover the whole module including the untouched
packages.

## Scope note

Contributor's original commits (`ff1ad56f`, `f04e685b`) are untouched; this
review-response work is two new commits on top, `carry/2080`, entirely local
(no push, no PR edit, no comment — per task constraints).
# RESULTS — fix/issue-verify-gaps-r2-20260918

Branch: `fix/issue-verify-gaps-r2-20260918`, off `release/v1.16.11-rc3` (`rc3` = `2704bc17`),
merged with `fix/launch-truncation` (`b3ca5e93`, required prerequisite for item 4).
Nothing pushed; no GitHub writes.

Final head: see `git log --oneline -1` on this branch at delivery time.

## Item 1 — #2214: panes inherit the tmux server cwd (shell-tool gap)

**Root cause.** rc.3's fix for #2214 (a poisoned tmux server ignoring `-c` for
new panes) prefixes every pane's own command with `cd -- <dir> &&`
(`cwdAssertCommand`) so the pane asserts its own directory instead of trusting
the server's. This is applied both when a tool launches as the pane's initial
process (`RunCommandAsInitialProcess=true`, e.g. claude/codex) and, as a
fallback, via `SendKeysAndEnter` for the generic "shell" tool
(`RunCommandAsInitialProcess=false`). But `Session.Start` ran
`verifyPaneWorkDirUnlessPlaceholder` **immediately after pane creation** —
before that deferred send-keys fallback ever executed. For a shell-tool
session on a poisoned server, the guard inspected the still-bare, still-
poisoned pane and rejected the session with `ErrPaneCwdDeleted`, even though
the pending cd-assert would have recovered it exactly like the
initial-process path already does.

**Fix.** `internal/tmux/tmux.go`: for the shell-tool path
(`command != "" && !RunCommandAsInitialProcess`), the guard is deferred until
*after* `SendKeysAndEnter(cwdAssertCommand(...))` actually runs. Extracted a
`killAfterPaneCwdFailure` helper (code-simplifier pass) so both guard sites
share the same kill+log+return behavior.

**Red/green.** `TestStart_SurvivesExternallyPoisonedServer_ShellTool`
(`internal/tmux/issue2214_server_cwd_test.go`) builds the same poisoned-server
fixture as the existing `TestStart_SurvivesExternallyPoisonedServer`, but with
`RunCommandAsInitialProcess: false` and a non-empty command. Before the fix:
`Start()` fails with `ErrPaneCwdDeleted`. After: `Start()` succeeds and the
pane's real process resolves to `workDir`.

## Item 2 — #2062: no CLI/TUI surface to retry or clear dead letters

**Why carry/2230 was reverted (confirmed).** `carry/2230` (PR #2230,
**Nandana Dileep**, head `17b54667`) implemented `inbox dead-letter
retry|purge` and the `Alt+D` TUI panel. It merged into local integration
cleanly at the text level (`f3a4a14a`) but was reverted 22 seconds later
(`05c1a8cf`) because it redeclared `DeadLetterRecord` with a shape
incompatible with the one `carry/2111`'s read-only `list`/`show` inspection
had already introduced:

| | #2111's `DeadLetterRecord` | carry/2230's `DeadLetterRecord` |
|---|---|---|
| identifier | `Ref` (source snapshot + byte offset, stale on any append/rewrite) | `ID` (content hash of the raw line, stable across unrelated mutations) |
| purpose | forensic inspection, incl. malformed/undecodable records | operator-safe retry/purge/TUI metadata |

`go build` failed with "redeclared in this block"; the revert dropped the
whole feature — retry, purge, and the TUI panel — leaving only `list`/`show`
in rc.3, which #2062's own triage comment already calls insufficient.

**Fix.** Reconciled the two shapes into **one** `DeadLetterRecord`
(`internal/session/deadletter_inspection.go`) instead of picking a side: it
is the union of both field sets. `list`/`show` keep populating `Ref`/
`Source`/`Offset`/`Problem`/`Raw` exactly as before (unchanged behavior,
unchanged tests); `InspectDeadLetters` now also computes each record's `ID`
using the same input bytes `deadLetterRecordID` hashes (mirroring
`bufio.Scanner`'s line-ending stripping), so an ID printed by `list`/`show`
is exactly the one `retry`/`purge` (which read through that scanner) accept.
`retry`/`purge`/the TUI panel keep populating `ID`/`ChildTitle`/
`TargetSessionID`/`Reason`/`Timestamp`/`AgeSeconds`/`Attempts`/
`PayloadSummary`/`Corrupt` exactly as carry/2230 did.

Reapplied unmodified in logic: `internal/session/dead_letter_management.go`,
`internal/ui/dead_letter_panel.go` (`Alt+D` TUI), and carry/2230's own
follow-up fix (`ddcad7d3`, hashing raw scanner bytes for retry/purge
matching — a real bug the PR author found and fixed the same day). Collapsed
a duplicate `inbox dead-letter` CLI dispatch and a second, incompatible
`list`/`show` reimplementation that the cherry-pick's auto-merge introduced
in `cmd/agent-deck/inbox_cmd.go`; `list`/`show` stay wired to
`InspectDeadLetters` (#2111), `retry`/`purge` dispatch to the reapplied
management functions, and `inbox dead-letter help` now lists all four
subcommands.

**Red/green.** `cmd/agent-deck/issue2062_deadletter_cli_test.go` (rewritten
to use `session.ListDeadLetters` directly for setup/lookup, matching what the
TUI does, since `list`/`show`'s own JSON shape is #2111's, not management's):
retry on a missing target fails honestly and retains the record; a
successful retry removes only that record and is idempotent on re-run; purge
refuses without `--older-than`/`--yes` and a bounded purge deletes only
matching records; help lists `list`/`show`/`retry`/`purge`.
`internal/ui/dead_letter_panel_test.go`'s existing
`TestIssue2062DeadLetterPanelListShowRetryAndConfirmedPurge` covers the
Alt+D panel end to end (list → show → failed retry → confirmed purge).

**Frames.** Golden frame of the `Alt+D` panel (list view and show-detail
view, seeded with two dead-letter records) captured via `panel.View()` — see
`frame-altd-panel.txt` alongside this file.

**Credit.** carry/2230 / PR #2230 by **Nandana Dileep**
(`@nandanadileep`); follow-up fix by Ashesh Goplani. Both credited in
PR-BODY.md.

## Item 3 — #2148: apply the maintainer's review-hold revisions to the structure advisory workflow

**Root cause (confirmed via `gh pr view 2149 --comments`).** The workflow
that landed in rc.3 (`fae07a2e`, merging PR #2149) is exactly the commit the
maintainer put a review hold on (2026-09-06, head
`19cb5f977e1f0d14532266c9369d8f2b0a1ca83d`), 11 days before the local
integration merge. None of the three requested corrections were applied:

1. `github.event.pull_request.base.sha` is the base branch's current tip,
   not the PR's actual merge base.
2. The summary step read each whole log file into memory before truncating
   to the displayed tail, with no size bound and no validation that the
   subtracted values were numeric.
3. Checkout steps kept push credentials they never use; the sentrux binary
   download had no integrity check beyond a pinned version tag.

**Fix** (`.github/workflows/structure-advisory.yml`):
1. Checks out the PR head with full history, resolves `git merge-base
   <base.sha> <head.sha>`, and checks out *that* commit as `base` instead of
   the base branch's tip.
2. The Python summary step now seeks to the last 200,000 bytes of each log
   before reading (bounding the read itself, not just the displayed tail),
   and validates `bv`/`hv` are numeric before subtracting — a non-numeric
   value is reported as `invalid` rather than crashing or silently
   misformatting.
3. `persist-credentials: false` on every `actions/checkout` step; the
   sentrux binary download is now checksum-verified via `sha256sum -c`
   against a pinned `SENTRUX_SHA256` (computed once from the exact bytes
   GitHub served for `v0.5.7` — there is no published checksums file for
   this release to verify against instead).

**Red/green.** `tests/ci/structure-advisory-workflow.test.sh` greps the
workflow for all of the above. Confirmed failing against the pre-revision
(rc.3) workflow (7 of 8 checks fail) and passing after (8/8).

**Status.** The issue's "worker MCP" half (an `[mcps.sentrux]` config entry
so worker sessions can query the structural delta before opening a PR) is
untouched by PR #2149 and **stays open** — #2148 should get a milestone
note, not a close.

## Item 4 — rc.4 paste-truncation follow-ups (conductor addendum, from the truncation-fix review)

Merged `fix/launch-truncation` (`b3ca5e93`, rc.3 P1: compare the collapsed
`[Pasted text #N +M lines]` marker against hard line breaks, not a physical
line count) as the prerequisite this branch needed for `send.CheckPasteMarker`
to exist. Then applied the review's two follow-up findings
(`/Users/ashesh/agent-deck-recovery/plans-20260917/receipts/review-launch-truncation/RESULTS.md`
P2-1, P2-2):

**P2-1 — tail-loss detection gap.** `ExpectedPasteMarkerLineBreaks` excluded
a message's trailing hard break as a defensive "floor" against not knowing
whether Claude's composer trims one before counting. A floor is ambiguous by
construction: `"a\nb\nc\n"` (3 lines, trailing newline) expected only 2
breaks, so a paste truncated to `"a\nb\n"` (line `c` lost) also declared 2
and was accepted as `PasteMarkerIntact`. Fixed by counting every hard break
literally, including a trailing one, matching Claude's own documented
formula `(text.match(/\r\n|\r|\n/g) || []).length` rather than hedging
against an unverified trim assumption. This is a deliberate resolution of a
known ambiguity (not previously provable live, per the review's own P2-3),
made in the direction the regex's literal definition supports.

**P2-2 — `session send` had no truncation guard.** `session send`
(`cmd/agent-deck/session_cmd.go`), including its `--message-file` form (which
most commonly carries a multi-line body), called `SendKeysAndEnter` directly
with no #2079 check — only the launch/instance send path had one. Wired
`sendRetryOptions.expectedPasteBreaks` into `executeSend` for Claude-
compatible targets, added `sendInitialKeysChecked`, and extended
`sendRetryTarget` with `SendKeysAndEnterChecked`. A single-line message or a
non-Claude target is unaffected (`expectedPasteBreaks` stays 0, sends go out
exactly as before). A code-simplifier pass then extracted the identical
pre-Enter check closure duplicated between this new code and
`internal/session/instance.go`'s `sendMessageWhenReady` into
`send.PasteTruncationCheck`, used by both.

**Red/green.**
- `internal/send/issue2079_paste_truncation_test.go`:
  `TestCheckPasteMarker_DetectsLastLineLostFromNewlineTerminatedMessage` is
  the failing-first regression for the exact P2-1 scenario; updated the two
  existing tests that had encoded the old floor as intentional.
- `cmd/agent-deck/session_send_test.go`:
  `TestExecuteSend_MultilineMessageRefusedOnTruncatedPasteMarker` is the
  failing-first end-to-end regression for P2-2 (confirmed failing —
  `typed_not_submitted` instead of the truncation error — with the
  `executeSend` wiring reverted); plus unit tests for
  `sendInitialKeysChecked` directly.

## Item 5 — r2 review follow-ups (P2-1, P2-2 from `review-verify-gaps-r2`)

Both from the independent review of this branch's own `fb142a6f`
(`/Users/ashesh/agent-deck-recovery/plans-20260917/receipts/review-verify-gaps-r2/RESULTS.md`).
Neither was a correctness bug or a blocker; both were flagged as worth a
follow-up before rc.4.

**P2-1 — `purge` must never touch the `_unowned` ledger.**
`readDeadLetterEntries` unconditionally included `_unowned` among purge's
candidate records, so `purge --yes`/`--older-than`/a single-ID purge could
all delete `_unowned` records — in tension with `unowned_inbox.go`'s own
documented invariant that "the `_unowned` ledger has NO consumer... only
`SweepInboxByTTL` ever removes a record." A single `--yes` could silently
erase the only on-disk evidence that a remote session had stalled, with no
way to tell "purge dead-letter records" from "also erase unresolved
discovery evidence" apart.

Fix: `purgeDeadLetters` (`internal/session/dead_letter_management.go`) now
skips every `_unowned` record instead of matching it, and
`PurgeDeadLetter` (the single-ID path the TUI's `Alt+D` purge key uses)
refuses one outright with an explicit error. `retry` is unchanged — it may
still act on an `_unowned` record, since redelivering one to a now-resolvable
parent is the point of retry, not evidence-destruction. The bulk purge
functions now return `[]DeadLetterActionOutcome` (one `{id, action, outcome,
reason}` per record considered, `outcome: "skipped"` for a record left
alone) instead of a bare count, so both the human-readable summary and the
new `--json` output (item 2, below) come from the same data.

`cmd/agent-deck/inbox_cmd.go`'s human-readable purge summary now reports
both counts: `"Purged N dead-letter record(s); skipped M _unowned
record(s) (...)"`.

**Red/green.** `TestIssue2062PurgeYesNeverTouchesUnowned` (replaces the
prior `TestIssue2062PurgeYesClearsUnownedDedupState`, whose premise — that
`purge --yes` was *supposed* to clear `_unowned` — is exactly what this item
reverses) seeds one `_unowned` and one ordinary dead-letter record, runs
`purge --yes`, and asserts the ordinary record is gone, the `_unowned`
record and its pending-count are untouched, and the summary line reports
`skipped 1`. `TestIssue2062PurgeSingleUnownedRecordRefused` covers the
single-ID/TUI path directly against `session.PurgeDeadLetter`.

**P2-2 — `retry`/`purge` parity: add `--json`.**
`list`/`show` already had `--json`; `retry`/`purge` printed plain text only.
Both subcommands now accept `--json` and, instead of the human-readable
line, print a JSON array of `{"id", "action", "outcome", "reason"}` objects
— one entry per record the command actually considered. For `retry` that is
always a single `{"action":"retry","outcome":"delivered",...}` entry (errors
are unchanged — still a plain Go error on stderr with a non-zero exit,
matching `list`/`show`'s own error handling). For `purge` it is one entry
per matched record, `outcome` either `"removed"` or `"skipped"` (an
`_unowned` record, always carrying a `reason`).

Documented in `skills/agent-deck/references/cli-reference.md`'s `dead-letter`
section, including the `_unowned` exclusion from item P2-1.

**Red/green.** `TestIssue2062RetryJSONShape` and `TestIssue2062PurgeJSONShape`
decode the `--json` output into `[]session.DeadLetterActionOutcome` and
assert the field values and outcome mix (purge's case seeds one ordinary and
one `_unowned` record so both `removed` and `skipped` appear in one call).

**Item 5 verification.** `go build ./...` and `go vet ./...` clean on the
host. `go test -timeout 20m ./internal/session/... ./cmd/agent-deck/...`
(Docker, `agentdeck-gotest:1.25-tmux`, `--init`, lock at
`/tmp/agentdeck-docker.lock.d`) — clean except exactly the 5 known-pre-existing
root-permission failures (`TestStartupNamePermissionChangeFailsClosed`,
`TestDeployScript_NonRootKeepsGroup`, `TestDeployScript_NonRootGroupFailureAborts`,
`TestWriteJSONFileAtomic_SkipsUnchangedWrite`, `TestCleanupReviewCrossProfileBoundary`
— all fail because the container lacks `DAC_OVERRIDE`/root to exercise the
permission-drop path being tested, unrelated to this change). No dead-letter/
purge test failed; no other regressions.

## VERIFY

- Host: `go build ./...` and `go vet ./...` — clean, one build at a time.
- Docker (`agentdeck-gotest:1.25-tmux`, `--init`, lock at
  `/tmp/agentdeck-docker.lock.d`):
  `go test -timeout 25m ./internal/tmux/... ./internal/session/... ./internal/ui/... ./cmd/agent-deck/...`
  — all green except the pre-declared known-pre-existing failures:
  `TestKill_LiveSessionThenSecondKillBothSucceed`,
  `TestStartupNamePermissionChangeFailsClosed`,
  `TestDeployScript_NonRootKeepsGroup`,
  `TestDeployScript_NonRootGroupFailureAborts`,
  `TestWriteJSONFileAtomic_SkipsUnchangedWrite` (the 5 root-permission
  tests), `TestCleanupReviewCrossProfileBoundary`. `internal/ui` was fully
  green on both runs (an earlier `TestStorageWatcherBuildCacheDefaultsSurviveHomeIsolation`
  cleanup flake — `TempDir RemoveAll: directory not empty` — did not
  reproduce in isolation or on the final run; confirmed environmental, not a
  regression).
- `bash tests/ci/structure-advisory-workflow.test.sh` — 8/8 PASS.
- code-simplifier dispatched twice (before the final Docker run each time):
  once over items 1–3's diff, once over item 4's diff. Both passes made
  small, behavior-preserving cleanups (deduped kill/log helper in tmux.go,
  collapsed duplicate CLI dispatch, deduped purge loops, deduped the
  identical pre-Enter paste-truncation check into `send.PasteTruncationCheck`)
  — see individual commits for detail.
- Alt+D TUI panel: golden frame captured (`frame-altd-panel.txt`), list and
  show-detail views, seeded with two dead-letter records.

## Safety

No writes to `~/.agent-deck` beyond nothing (all work happened in a fresh
clone under `/private/tmp/exec-fix-verify-gaps-r2/src`); no `rm` (used
`trash`); no `claude -p`; no push; no GitHub writes (`gh pr view --comments`
and `gh issue view` were read-only lookups).
