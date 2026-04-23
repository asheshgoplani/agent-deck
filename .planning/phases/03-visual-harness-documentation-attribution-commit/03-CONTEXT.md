# Phase 3: Visual harness + documentation + attribution commit — Context

**Gathered:** 2026-04-15
**Status:** Ready for planning
**Source:** PRD Express Path — `docs/PER-GROUP-CLAUDE-CONFIG-SPEC.md` (commit `4ade7f8`), scoped to CFG-05 (REQ-5) and CFG-06 (REQ-6). `/gsd-discuss-phase` skipped — spec is fully deterministic for this phase.

<domain>
## Phase Boundary

Ship the human-watchable verification harness that proves per-group `CLAUDE_CONFIG_DIR` works end-to-end for both normal and custom-command sessions (CFG-05), land the three doc surfaces (README subsection, repo-root CLAUDE.md one-liner, CHANGELOG `[Unreleased] > Added` bullet) and record attribution to @alec-pinson in at least one substantive commit (CFG-06). Additive on top of Phase 1 (CFG-01, CFG-02, CFG-04 tests 1/2/3/6) and Phase 2 (CFG-03, CFG-04 tests 4/5, CFG-07). No changes to any existing test assertion. No `internal/session/*.go` code modifications.

**In scope (files touched):**
- `scripts/verify-per-group-claude-config.sh` (new, `chmod +x`)
- `README.md` — new subsection "Per-group Claude config" under the existing Configuration area (anchor on line 85-87 / §Features or §Installation → Configuration; planner picks the cleanest insertion point consistent with the existing section order)
- `CLAUDE.md` (repo root) — one-line entry under the session-persistence mandate block. **Note: CLAUDE.md was `git rm --cached`'d in commit `5013940` and is currently untracked in both main and this worktree.** The planner MUST decide between (a) creating a fresh minimal CLAUDE.md with the one-line entry, (b) resurrecting content from git history (last tracked version before `5013940`), or (c) deferring if file absence makes the requirement non-applicable. See <claude_md_handling> below.
- `CHANGELOG.md` — single `[Unreleased] > Added` bullet

**Out of scope (this phase):**
- Any change to `internal/session/*.go`, `internal/ui/*.go`, or test files — Phase 1 and Phase 2 already closed CFG-01 through CFG-04 + CFG-07.
- direnv integration layer, profile-level `[profiles.<x>.claude]` semantics, per-group `mcp_servers` overrides.
- Rebase of `fa9971e` onto current `main` — merge-time concern, not this milestone.
- Any commit that pushes, tags, creates a PR, or merges (`git push`, `git tag`, `gh release`, `gh pr create`, `gh pr merge`).
- `rm` — use `trash` for any cleanup in the harness script.

</domain>

<decisions>
## Implementation Decisions

### CFG-05 — visual harness script (P1, locked)

- **Path:** `scripts/verify-per-group-claude-config.sh`. Bash, `#!/usr/bin/env bash`, `set -euo pipefail`. `chmod +x` after creation.
- **Config isolation:** Capture the user's original `~/.agent-deck/config.toml` to a temp backup before mutating. The script MUST NOT leave the user's config corrupted on any exit path (success, failure, interrupt). Prefer one of:
  - (a) Write a scratch TOML under `$(mktemp -d)` and launch agent-deck with a dedicated config path if `AGENT_DECK_CONFIG` (or equivalent env var) is supported.
  - (b) Back up the real config, inject temporary `[groups."verify-group-a".claude]` and `[groups."verify-group-b".claude]` sections, and restore on exit via `trap '<restore>' EXIT INT TERM`.
  - Planner picks based on whether agent-deck's current binary honors a config-path override env var. If (a) is not available, (b) with a `trap`-based restore is mandatory.
- **Test groups:** Exactly two throwaway groups:
  - `verify-group-a` with `config_dir = "~/.claude"` (or resolved absolute equivalent)
  - `verify-group-b` with `config_dir = "~/.claude-work"` (or resolved absolute equivalent)
  - Pick values that exist on the conductor host (the script is meant to be run manually, not in CI).
- **Session launch:**
  - One session per group. **One MUST be a normal (default `claude`) session, the other MUST be a custom-command session** (e.g. `bash -c 'exec claude'` wrapper or a throwaway wrapper script under `$(mktemp)` that `exec`s a shell that echoes the env var). The harness is the only thing in the milestone that actually exercises both code paths at runtime on the conductor host.
  - Use `agent-deck session start` / `agent-deck add` + explicit `-g verify-group-{a,b}`. Do not rely on group-autocreate magic; create the groups explicitly.
- **Assertion pipeline:**
  - `agent-deck session send <id> "echo CLAUDE_CONFIG_DIR=\$CLAUDE_CONFIG_DIR"`.
  - Capture via `agent-deck session output <id>` (or `session output --tail` if supported). Allow a short settle delay (reuse the `CAPTURE_DELAY=1.5` convention from `scripts/visual-verify.sh`) so tmux has time to flush the line.
  - Parse the echoed line with `grep -E '^CLAUDE_CONFIG_DIR='` or equivalent. The expected RHS is the group's resolved path.
- **Output:**
  - Pass/fail table. Aligned columns. **TTY-detection:** use color (green ✓ / red ✗) when stdout is a TTY (`[ -t 1 ]`); plain text ASCII on redirect/pipe. Mirror the ui-brand status-symbol convention (✓ / ✗).
  - Example layout:
    ```
    | Group           | Session Type    | Resolved CLAUDE_CONFIG_DIR | Expected         | Result |
    |-----------------|-----------------|----------------------------|------------------|--------|
    | verify-group-a  | normal          | /home/<u>/.claude          | /home/<u>/.claude| ✓      |
    | verify-group-b  | custom-command  | /home/<u>/.claude-work     | /home/<u>/.claude-work | ✓ |
    ```
  - Final line: `PASS: 2/2` or `FAIL: N/2`.
- **Exit code:** `0` iff both sessions show the expected per-group value. `1` otherwise. Any unexpected error (session launch fail, tmux missing, agent-deck binary missing) MUST exit non-zero with a one-line stderr message.
- **Cleanup (`trap`):** Stop both sessions (`agent-deck session stop <id>`), remove the two throwaway groups, restore the config backup, `trash` any temp files. MUST run on success, failure, and SIGINT/SIGTERM. **Use `trash`, not `rm`** (repo-root mandate, enforced per global CLAUDE.md).
- **Preflight checks:** Verify `agent-deck` binary on PATH, `tmux` available, the two target config dirs exist (warn only; the test can still run if one doesn't — the echo will just return the path string without resolution).
- **Harness-testable criterion (TDD substitute for this phase):** This phase has no Go unit tests. The deterministic gate is: running `bash scripts/verify-per-group-claude-config.sh` on the conductor host exits 0 and prints both rows as ✓. That exit-0 gate IS the acceptance test for CFG-05. Planner MUST include a task that runs the harness once during the phase, captures stdout into the phase artifact directory (NOT into the commit), and asserts `$? == 0`.

### CFG-06 — README subsection (P0, locked)

- **File:** `README.md`.
- **Location:** Under the existing Configuration coverage. The README has no `## Configuration` heading today; configuration-adjacent content lives near §Installation → "Claude Code Skill" (L364) and §Features. Planner picks between:
  - (a) Adding a new `## Configuration` top-level section and placing the subsection there.
  - (b) Inserting as a subsection under §Features (e.g., `### Per-group Claude config` sibling to `### MCP Manager`, `### Skills Manager`).
  - Rule: whichever insertion point preserves the existing section progression (Problem → Solution → Features → Installation → Quick Start → Documentation → FAQ → Development → Star History → License). Choose one; don't introduce both.
- **Content:** A new subsection titled `Per-group Claude config` containing:
  - One-paragraph explainer: agent-deck supports per-group `CLAUDE_CONFIG_DIR` and `env_file` overrides; useful when a single profile hosts groups that should use different Claude accounts (e.g. personal profile hosting a `conductor` group that uses `~/.claude-work`).
  - TOML example lifted verbatim from the spec:
    ```toml
    [groups."conductor".claude]
    config_dir = "~/.claude-work"
    env_file = "~/git/work/.envrc"
    ```
  - Pointer to `scripts/verify-per-group-claude-config.sh` as the human-watchable verification.
  - One-line resolution-priority summary: `env > group > profile > global > default` (mirrors CFG-07's `source=` labels for consistency).
- **No anchor churn:** Do not renumber or reshuffle existing subsections. Add, don't move.

### CFG-06 — repo-root CLAUDE.md one-liner (P0, locked) {#claude_md_handling}

- **File:** `CLAUDE.md` at the worktree root (`<repo-root>/.worktrees/per-group-claude-config/CLAUDE.md`).
- **Tracking status:** `CLAUDE.md` was removed from tracking in commit `5013940 chore: remove CLAUDE.md from tracking (keep local)`. It is NOT in the Git index on `main` or on this branch. The worktree currently has no CLAUDE.md file. It is also not in `.gitignore` — it is simply untracked.
- **Planner decision:** Choose ONE of the following three paths and commit it explicitly:
  - **Path A (resurrect):** `git show <last-tracked-commit>:CLAUDE.md > CLAUDE.md`, then add the one-line entry under the session-persistence mandate block, then commit with `git add -f CLAUDE.md` (since it may still be path-ignored — verify first).
  - **Path B (fresh minimal):** Create a new CLAUDE.md with a small skeleton that includes a session-persistence mandate block and the required one-liner. Commit with `git add -f` if needed.
  - **Path C (defer with written justification):** Declare the requirement non-applicable on the grounds that CLAUDE.md is explicitly untracked in this repo. Record the decision in the phase SUMMARY with the commit hash (`5013940`) as evidence. This path is permissible ONLY if Paths A and B both turn out to be blocked (e.g., the file is force-ignored by a higher-precedence ignore source).
- **Required content (regardless of path):** One line, verbatim from the spec, under a session-persistence mandate block:
  > Per-group config dir applies to custom-command sessions too; `TestPerGroupConfig_*` suite enforces this.
- **Verification:** `grep -n "TestPerGroupConfig_" CLAUDE.md` returns exactly one match on the new line (or the file exists with the line present).
- **Recommendation for the planner:** Path A is preferred because it preserves prior mandate-block context (commits `fb7caad` and `a262c6d` added substantive mandates). Path B is the fallback if git-history recovery fails. Path C is a last resort and requires explicit SUMMARY.md justification.

### CFG-06 — CHANGELOG bullet (P0, locked)

- **File:** `CHANGELOG.md`.
- **Location:** `## [Unreleased]` block (currently at line 8, empty). Add a `### Added` subsection if one does not exist under `[Unreleased]`.
- **Bullet content (verbatim):**
  ```markdown
  - Per-group Claude config overrides (`[groups."<name>".claude]`).
  ```
- **PR reference:** Append ` (Base implementation by @alec-pinson in [PR #578](https://github.com/asheshgoplani/agent-deck/pull/578))` to the bullet, OR keep the bullet pure and put the attribution only in the commit message — spec permits either. Recommendation: include the PR reference in the CHANGELOG bullet for end-user visibility; attribution commit message carries the same credit line in Git history.

### CFG-06 — attribution commit (P0, locked)

- **Requirement:** At least one commit on this branch (`main..HEAD`) MUST carry the exact string `Base implementation by @alec-pinson in PR #578.` in its commit body.
- **Placement options (planner chooses one):**
  - (a) Dedicated attribution commit at the end of the phase — clean, single-purpose. Subject: `docs: credit @alec-pinson for PR #578 per-group Claude config base`. Empty diff acceptable if prior commits didn't carry the attribution; otherwise this commit amends docs OR is empty (`--allow-empty`).
  - (b) Folded into the body of a substantive commit in this phase (CHANGELOG or CLAUDE.md commit) — lower commit count, still discoverable via `git log --grep "@alec-pinson"`.
- **Verification:** `git log main..HEAD --grep "@alec-pinson"` returns ≥ 1 commit.
- **Commit signature:** Every commit in this phase signs `Committed by Ashesh Goplani` in the trailer. **NO Claude attribution** (no `Generated with Claude Code`, no `Co-Authored-By: Claude`). Honor the repo-root v1.5.3 mandate: no `--no-verify` — all commits go through `lefthook` / pre-commit hooks.
- **Attribution scope:** Substantive commits in this phase (CHANGELOG addition, CLAUDE.md addition, harness script creation) SHOULD carry the attribution line in the body. A pure-whitespace doc nit MAY omit it. Aim for at least two commits carrying the credit so `git log --grep` is robust.

### Commit plan shape (locked framing; planner fills in exact granularity)

The phase produces a small linear commit stream. Suggested shape (planner may split or merge adjacent commits so long as all rules are honored):

1. `feat(scripts): add visual verification harness for per-group Claude config (CFG-05)` — new script + `chmod +x`. Body carries attribution.
2. `docs(readme): add per-group Claude config subsection (CFG-06)` — README-only. Body carries attribution.
3. `docs(claude): add one-line TestPerGroupConfig_ enforcement mandate (CFG-06)` — repo-root CLAUDE.md. Path A/B/C decision documented in commit body. Body carries attribution.
4. `docs(changelog): add [Unreleased] Added bullet for per-group Claude config (CFG-06)` — CHANGELOG-only. Body carries attribution.
5. (Optional) `docs: credit @alec-pinson for PR #578 base implementation` — empty/allow-empty commit if the planner prefers a dedicated attribution commit for audit clarity.

Every commit message trailer: `Committed by Ashesh Goplani`. No Claude attribution.

### TDD and commit discipline (carried forward)

- **No `--no-verify`.** All commits go through pre-commit hooks (lefthook from repo-root CLAUDE.md / v1.5.3 `ee7f29e` mandate).
- **No `git push`, no `git tag`, no `gh release`, no `gh pr create`, no `gh pr merge`** during this phase or during milestone completion.
- **No `rm`** in the harness — `trash` only (consistent with global CLAUDE.md rule).
- **Sign:** `Committed by Ashesh Goplani`. No Claude attribution under any circumstances.
- **Attribution:** `Base implementation by @alec-pinson in PR #578.` in substantive commit bodies; at minimum on the harness commit + one doc commit.
- **TDD substitute for this phase:** This phase does not introduce Go tests. The harness's own exit-0 gate is the regression test. Planner MUST structure the phase so that:
  1. The harness is written first and runs GREEN (exit 0) on the conductor host.
  2. Docs land after the harness is proven, because the README and CLAUDE.md both point at the harness by name.
- **Go 1.24.0 pin:** No Go code in this phase, but any `make` target invoked (none expected) must honor `GOTOOLCHAIN=go1.24.0`.

### Claude's Discretion

- Exact shell idioms in `scripts/verify-per-group-claude-config.sh` (`mktemp -d` vs. fixed `/tmp/...`, which tmux settle delay to use, whether to parametrize the two config dirs via `CLAUDE_CONFIG_DIR_A` / `CLAUDE_CONFIG_DIR_B` env vars for power users).
- Exact README insertion point (new `## Configuration` section vs. subsection under §Features) — rule is no anchor churn; planner picks the minimal-diff option.
- Which Path (A/B/C) to take for repo-root CLAUDE.md — recommendation is Path A, but the planner verifies git-history recovery is viable before committing to it.
- Whether the attribution commit is dedicated (option (a)) or folded (option (b)).
- Exact column widths / color codes in the harness pass/fail table.
- Whether to include the PR #578 link in the CHANGELOG bullet or keep it commit-only.
- Commit granularity (split README/CLAUDE.md/CHANGELOG into 3 commits vs. fold into 1 doc commit) — so long as attribution appears in at least one body and no commit message violates the "no Claude attribution" rule.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Source spec
- `docs/PER-GROUP-CLAUDE-CONFIG-SPEC.md` — full v1.5.4 spec. REQ-5 (CFG-05) and REQ-6 (CFG-06) are this phase's contract. Hard rules block (lines 141-148) governs no-push/no-tag/no-merge, `trash` not `rm`, no Claude attribution.

### Project state
- `.planning/ROADMAP.md` — Phase 3 block: goal, approach (6-step ordered list), scope, success criteria (6 items), dependencies (Phases 1+2 complete).
- `.planning/REQUIREMENTS.md` — CFG-05 (REQ-5 / L40-45) and CFG-06 (REQ-6 / L49-53) definitions and traceability table.
- `.planning/STATE.md` — milestone position, Phase 1 + Phase 2 commit history (used to avoid mixing up which requirements are already closed).
- `.planning/phases/01-custom-command-injection-core-regression-tests/01-01-SUMMARY.md` — Phase 1 outcome (CFG-01/02/04-tests-1/2/3/6 closed).
- `.planning/phases/02-env-file-source-semantics-observability-conductor-e2e/02-01-SUMMARY.md` — Phase 2 Plan 1 outcome (CFG-03 + CFG-04 test 4).
- `.planning/phases/02-env-file-source-semantics-observability-conductor-e2e/02-02-SUMMARY.md` — Phase 2 Plan 2 outcome (CFG-04 test 5 + CFG-07 with `GetClaudeConfigDirSourceForGroup` helper and `(i *Instance) logClaudeConfigResolution()`).

### Reference harness (style model)
- `scripts/visual-verify.sh` — existing bash harness in this repo. Read for conventions: shebang, `set -euo pipefail`, profile isolation (`_visual_verify`), `trap` cleanup, `CAPTURE_DELAY=1.5`, preflight checks, output directory layout. CFG-05 harness MUST align with this style.
- `scripts/visual-verify-claude.md` — companion prompt for the visual-verify workflow; demonstrates the "harness + doc pointer" pattern.

### Agent-deck CLI surface
- `cmd/agent-deck/session_cmd.go` — `session start|stop|send|output` command surfaces the harness calls. Read only if the planner needs to confirm exact flags (e.g., whether `--tail` is supported on `session output`).
- `cmd/agent-deck/group_cmd.go` — `group create|delete` commands for the harness's group setup/teardown.

### Repo-root rules
- `<repo-root>/CLAUDE.md` (NOT in this worktree; lives in the main-branch checkout) — reference copy if Path A (resurrect) is chosen. Do NOT rely on it being present inside this worktree's checkout.
- Global `~/.claude/CLAUDE.md` / `~/.claude-work/CLAUDE.md` — user-level mandates (no `rm`, no Claude attribution). Enforced by the planner/executor agents directly; already honored in Phase 1 + 2.

### Phase 1 + 2 commits (attribution precedent)
- `b39bbf3` (Phase 1 fix commit — CFG-02) — example of a substantive commit that SHOULD have carried @alec-pinson attribution; check whether it did, and if not, consider folding attribution into THIS phase's commits to make up.
- `476367c` (Phase 2 CFG-07 feat commit) — recent substantive commit on this branch; attribution audit reference.

</canonical_refs>

<specifics>
## Specific Ideas

- The harness's TTY-detection idiom: `if [ -t 1 ]; then GREEN=$'\033[32m'; RED=$'\033[31m'; RESET=$'\033[0m'; else GREEN=""; RED=""; RESET=""; fi`.
- The harness MUST be re-runnable on a dirty workspace — repeated runs must not leak groups, sessions, or config state. Trap-based cleanup handles this, but also include a best-effort pre-run cleanup (`agent-deck session stop` on any prior `verify-group-*` sessions, `agent-deck group delete` on prior `verify-group-*` groups, both wrapped in `|| true`).
- `agent-deck session send` + `session output` has an inherent race — the echoed line may not be on the latest line immediately. Either poll for up to ~3s (loop `agent-deck session output <id> | tail -n 5`) until the `CLAUDE_CONFIG_DIR=` line appears, or sleep for `CAPTURE_DELAY` once. Prefer the poll for robustness.
- CHANGELOG format compliance: the repo uses [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). `[Unreleased]` must have `### Added` as a sibling sub-heading. If none exists, the planner creates one.
- The existing `[Unreleased]` block is empty (line 8). First content goes there; verify with `sed -n '8,12p' CHANGELOG.md` after the edit.
- For the repo-root CLAUDE.md resurrection (Path A): `git log --all --oneline -- CLAUDE.md` returns commits including `fb7caad`, `a262c6d`, and removal commit `5013940`. The pre-removal tree of `5013940` is the correct source: `git show 5013940^:CLAUDE.md > CLAUDE.md`.
- Git attribute for the attribution commit: a `--allow-empty` commit is acceptable only if no surrounding doc commit already carries the attribution. Prefer folding into substantive commits.
- **Milestone success-criteria preview (runs at `/gsd-complete-milestone`, not here):** Criteria 1-6 from the roadmap's Milestone Verification block are mostly closed; this phase closes criteria 3 (harness exit 0), 5 (attribution in `git log --grep`), and contributes to 6 (no push/tag/PR/merge). The planner should double-check nothing in this phase's commit plan violates 6.

</specifics>

<deferred>
## Deferred Ideas

- Rebase of `fa9971e` onto current `main` — merge-time concern, not this milestone or this phase.
- Manual conductor-host proof (`ps -p <pane_pid>` env check) — milestone verification step at `/gsd-complete-milestone`, not this phase.
- External-contributor PR merge strategy (merge PR #578 then this branch as follow-up vs. single PR superseding #578) — user decides at milestone end, not this phase.
- A Claude-driven visual verification pass (analogous to `scripts/visual-verify-claude.md`) for the per-group config harness — nice-to-have but not required by CFG-05; deferred.
- direnv integration layer, profile-level `[profiles.<x>.claude]` semantics, per-group `mcp_servers` overrides — carried forward from v1.5.4 non-goals.

</deferred>

---

*Phase: 03-visual-harness-documentation-attribution-commit*
*Context gathered: 2026-04-15 via PRD Express Path (spec: docs/PER-GROUP-CLAUDE-CONFIG-SPEC.md @ 4ade7f8). `/gsd-discuss-phase` skipped — spec fully deterministic for CFG-05 + CFG-06.*
