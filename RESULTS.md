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
