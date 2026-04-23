# Phase 4: Verification harness, docs, and CI wiring - Context

**Gathered:** 2026-04-14
**Status:** Ready for planning
**Source:** User brief + SESSION-PERSISTENCE-SPEC.md REQ-4/REQ-5/REQ-6 + REQUIREMENTS.md DOC-01..05, SCRIPT-01..07

<domain>
## Phase Boundary

This is the FINAL phase of v1.5.2. It does NOT change session-persistence behavior — Phases 2 and 3 already fixed the bugs. Phase 4 only **proves** the fix end-to-end and **gates** regressions.

Three deliverables, in priority order:

1. **`scripts/verify-session-persistence.sh`** — a human-watchable end-to-end verification script that exercises real systemd login-session teardown on Linux (not mocked) and exits non-zero if any scenario fails.
2. **CLAUDE.md mandate verification + DOC-05** — the "Session persistence: mandatory test coverage" section at `<repo-root>/.worktrees/session-persistence/CLAUDE.md:5` already exists (drafted at commit a262c6d). Verify it covers DOC-01..04 verbatim, patch any gaps. Then add the one-line v1.5.2 mention to `CHANGELOG.md` (DOC-05).
3. **CI wiring** — add a GitHub Actions workflow (or extend an existing one) that runs `go test -run TestPersistence_ ./internal/session/... -race -count=1` AND `bash scripts/verify-session-persistence.sh` on every PR touching the mandated paths. Red of either blocks the PR (SCRIPT-07).

OUT OF SCOPE: changing `launch_in_user_scope` semantics, touching `internal/tmux/**` or `internal/session/instance.go` behavior, any new tests beyond what the script drives (the eight `TestPersistence_*` tests already exist). OBS sanity (mentioned in user brief) is a **read-only** log check inside the script, not new log emission work — OBS-01/02/03 are Phase 2/3 deliverables.

</domain>

<decisions>
## Implementation Decisions

### Script location and entry contract (locked — SCRIPT-01..06)
- Path: `scripts/verify-session-persistence.sh` (NOT `scripts/verify-session-persistence/`). Executable (`chmod +x`). `#!/usr/bin/env bash` with `set -euo pipefail`.
- First thing printed: a numbered checklist of the scenarios (SCRIPT-01). Format: `[1] <scenario name>` … `[N] <scenario name>`, so a reviewer can follow along.
- For every live tmux server the script observes, it prints `PID=<pid>` and the full `/proc/<pid>/cgroup` content (SCRIPT-03). On macOS this file does not exist — the script must print `cgroup: N/A (macOS)` and not crash.
- Each scenario ends with EXACTLY one line: `[PASS] <scenario name>` in green (`\033[32m`) or `[FAIL] <scenario name>: <reason>` in red (`\033[31m`). Skips print `[SKIP] <scenario name>: <reason>` in yellow (`\033[33m`) and are NOT counted as failures.
- Exit code: `0` iff every scenario is PASS or SKIP. Any FAIL → exit `1`. (SCRIPT-06)

### Scenarios the script MUST exercise (locked — SCRIPT-02..05)
The script drives REAL behavior — NO mocking, NO stubs, NO fake agent-deck binaries. On CI we use a stub `claude` binary that just sleeps (see "CI claude stub" below); the tmux + agent-deck binary path is real.

1. **Scenario 1: Live session + cgroup inspection** (SCRIPT-02, SCRIPT-03). Start an agent-deck session in a throwaway directory, wait for the tmux server to come up, read the pid via `tmux display-message -p -F '#{pid}'` (or `pgrep -f "tmux.*<socket>"`), print `/proc/<pid>/cgroup`, grep the cgroup output for `user@` to decide PASS/FAIL. On macOS skip the cgroup assertion (`[PASS]` if tmux is up).
2. **Scenario 2: Login-session teardown** (SCRIPT-04). On Linux+systemd:
   - Detect via `command -v systemd-run && systemctl --user is-system-running >/dev/null 2>&1` — if false, SKIP with `skipped: no systemd-run`.
   - Launch a throwaway sibling process inside a transient login-like scope: `systemd-run --user --scope --unit=adeck-verify-loginsim-$$ sleep 3600` in the background. Capture its scope name.
   - Start the agent-deck session AFTER the scope is active so the test simulates "same login session".
   - Record the tmux server PID.
   - Terminate the transient scope: `systemctl --user stop adeck-verify-loginsim-$$.scope`. On hosts with `loginctl` access to the current session, additionally run `loginctl terminate-session $(loginctl show-user $USER -p Sessions --value | awk '{print $1}')` ONLY if `AGENT_DECK_VERIFY_DESTRUCTIVE=1` is set (off by default — terminating your own login session disconnects SSH).
   - Sleep 2s, then check `kill -0 <tmux_pid>` — PASS iff the tmux pid is still alive.
   - NOTE (TEST-01/02 harness gap from Phase 3 verify): the existing Go test uses `systemd-run --user --scope` only. This script goes further with the `systemctl stop …scope` path to prove teardown actually propagates cgroup exit signals — which is the real production failure mode.
3. **Scenario 3: Stop → Restart resume** (SCRIPT-05). Start a session with a known project path, force-write a non-empty `ClaudeSessionID` into its instance state (via `agent-deck session state-set` or by creating a prepared state file — planner decides the least invasive path), stop it, then restart it. Capture the exact `claude` command line spawned (via `ps -ef` during launch OR by using the CI stub which echoes its argv to a tempfile). PASS iff the captured line contains `--resume` OR `--session-id`. FAIL with the captured line printed verbatim if neither appears.
4. **Scenario 4: Fresh session command shape** (optional, nice-to-have). Start a brand-new session with no `ClaudeSessionID`, capture the argv, PASS iff `--session-id <uuid>` is present AND `--resume` is NOT present. Only include if time permits — SCRIPT-05 covers the main production contract.

### CI claude stub (locked)
- Path in script: `scripts/verify-session-persistence.d/fake-claude.sh` (subdirectory colocated with the harness).
- Behavior: `exec sleep infinity` after writing its argv (`$@`) as one line to `${AGENT_DECK_VERIFY_ARGV_OUT:-/tmp/adeck-verify-argv.$$}`. Env `AGENT_DECK_VERIFY_USE_STUB=1` switches the script to `PATH=scripts/verify-session-persistence.d:$PATH`. Local-host runs without the stub — the script tries real `claude` first and only falls back to the stub if `claude` is not on PATH.

### Script cleanup invariants (locked)
- Use `trap 'cleanup' EXIT INT TERM`. Cleanup: stop every session the script created (by name prefix `verify-persist-$$-`), `systemctl --user stop adeck-verify-loginsim-$$.scope 2>/dev/null || true`, remove any temp dirs it made.
- All session names MUST use the prefix `verify-persist-$$-<scenario>` so a reviewer can visually distinguish them from real user sessions and a broken run leaves only sessions matching that grep.
- The script MUST fail closed: if the agent-deck binary isn't on PATH, exit `2` with a clear message BEFORE printing any `[PASS]`.

### CLAUDE.md mandate audit (locked — DOC-01..04)
- The section titled `## Session persistence: mandatory test coverage` at `CLAUDE.md:5` already exists and already contains: the eight test names (DOC-01 ✓), the PR mandate over the six mandated paths (DOC-02 ✓), the 2026-04-14 incident + 33 lost conversations (DOC-03 ✓), and the `Forbidden changes without an RFC` subsection that forbids flipping `launch_in_user_scope` back to `false` (DOC-04 ✓).
- Action: diff the section against the required shape in this CONTEXT, grep-verify all six mandated paths are named exactly, all eight test names are present, the phrase `2026-04-14` appears in the "Why this exists" block, and the phrase `may not be flipped back to false` (or equivalent) appears in the RFC block. If any piece is missing, patch MINIMALLY — do not rewrite the section.
- SCRIPT-07 requires the CLAUDE.md section to reference the script path by name. Verify `scripts/verify-session-persistence.sh` appears in the mandate block; patch if absent.

### DOC-05 — CHANGELOG mention (locked)
- Target file: `CHANGELOG.md` under `## [Unreleased]` (do NOT create a `[1.5.2]` heading yet — the user has explicit rule "no push/tag/PR"). One line, under the `### Fixed` subsection (create it if absent under Unreleased).
- Exact text (use this string, or a near-identical one — grep-checkable): `- Session persistence: tmux servers now survive SSH logout on Linux+systemd hosts via `launch_in_user_scope` default (v1.5.2 hotfix). ([docs/SESSION-PERSISTENCE-SPEC.md](docs/SESSION-PERSISTENCE-SPEC.md))`
- Verifiable: `grep -c '1.5.2' CHANGELOG.md` returns ≥ 1 AND `grep -c 'session-persistence\|Session persistence' CHANGELOG.md` returns ≥ 1.

### CI wiring (locked — SCRIPT-07)
- New file: `.github/workflows/session-persistence.yml`. Triggers: `pull_request` on the mandated paths via `paths:` filter — `internal/tmux/**`, `internal/session/instance.go`, `internal/session/userconfig.go`, `internal/session/storage*.go`, `cmd/session_cmd.go`, `cmd/start_cmd.go`, `cmd/restart_cmd.go`, `scripts/verify-session-persistence.sh`, `scripts/verify-session-persistence.d/**`, `CLAUDE.md`.
- Two jobs, both `runs-on: ubuntu-latest` (systemd-user is available on GH Actions Ubuntu runners via `loginctl enable-linger $USER`):
  1. `tests`: checkout → setup-go (go-version-file: go.mod) → `go test -run TestPersistence_ ./internal/session/... -race -count=1`. Fails on any red test.
  2. `verify-script`: checkout → setup-go → `go build -o /tmp/agent-deck ./cmd/agent-deck && export PATH=/tmp:$PATH` → `loginctl enable-linger $USER || true` → `AGENT_DECK_VERIFY_USE_STUB=1 bash scripts/verify-session-persistence.sh`. Fails on non-zero exit.
- Both jobs required via the workflow being in the paths filter — GitHub branch protection is user-managed and not part of this phase; the workflow existing + the CLAUDE.md mandate saying "test output or CI link required" is sufficient.
- Do NOT replace, rename, or modify the existing `.github/workflows/release.yml`. This is additive only.

### Commit messages (locked — per user rule)
- Sign-off: `Committed by Ashesh Goplani`. No `Co-Authored-By: Claude` lines. No `🤖 Generated with Claude Code` footer.
- One commit per plan-sized deliverable (script, docs, CI). Script may be split into `feat(04-01): add verification harness skeleton` + `feat(04-01): add teardown scenario` if the diff would exceed ~400 lines — planner decides granularity.
- `.planning/` files go through `git add -f .planning/<path>` because `.git/info/exclude` hides them.

### What plans must NOT do (locked)
- No `git push`, `git tag`, `gh pr create`, `gh release`. None.
- No `rm`. Use `trash` (globally configured; on Linux the project uses whatever `trash` binary is on PATH — if absent, the planner must surface this as a task blocker, not silently fall back to `rm`).
- No changes to Phase 1/2/3 code. Read-only on `internal/tmux/`, `internal/session/*.go` except for `session_persistence_test.go` helpers only if the harness needs them.
- No changes to the `launch_in_user_scope` default. That is locked by DOC-04.

### Claude's Discretion
- Exact bash style inside the script: function-per-scenario vs. inline is the planner's call, as long as each scenario is independently runnable via `SCENARIO=N bash scripts/verify-session-persistence.sh` for debugging.
- Whether to ship the stub as a Go program (`scripts/verify-session-persistence.d/fake-claude.go` compiled in CI) or a pure bash script. Bash stub is simpler; Go stub is more portable. Either is fine.
- Whether to use `tput setaf` or raw ANSI `\033[` codes for color. Raw ANSI is simpler and matches existing scripts in the repo.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Spec and requirements
- `docs/SESSION-PERSISTENCE-SPEC.md` — REQ-4 (docs), REQ-5 (verification script), REQ-6 (obs). Success criteria for the v1.5.2 milestone.
- `.planning/REQUIREMENTS.md` — DOC-01..05 and SCRIPT-01..07 exact requirement text (lines 36-52).
- `.planning/ROADMAP.md` — Phase 4 section: success criteria mirror this CONTEXT's <decisions>.

### Existing CLAUDE.md mandate (already drafted)
- `CLAUDE.md` (repo root) — `## Session persistence: mandatory test coverage` section at line 5. Audit this; do not rewrite. Already covers DOC-01..04.

### The eight tests that CI must run
- `internal/session/session_persistence_test.go` — all eight `TestPersistence_*` tests. Lines 21-28 contain the full list in a comment block.

### Fix points the script exercises
- `internal/session/userconfig.go:879-925` — `LaunchInUserScope` field and `GetLaunchInUserScope()` resolver. The Linux default is `true` (enforced by `TestPersistence_LinuxDefaultIsUserScope`).
- `internal/session/instance.go` — `Instance.ClaudeSessionID` field and the restart path that must emit `--resume` or `--session-id`. (Lines: planner should grep.)
- `cmd/session_cmd.go`, `cmd/start_cmd.go`, `cmd/restart_cmd.go` — where the `claude` command line is built. SCRIPT-05 asserts on this argv.

### CI conventions
- `.github/workflows/release.yml` — existing workflow showing `actions/setup-go@v5`, `go-version-file: go.mod`, `go test -race ./...`. Match this style.
- No current Go-test CI; Phase 4 adds it targeted at the mandated paths only (not a broad `go test ./...` — that's out of scope).

### Phase 3 pattern reference
- `.planning/phases/03-resume-on-start-and-error-recovery-req-2-fix/03-01-PLAN.md` through `03-05-PLAN.md` — use as the shape template for Phase 4 plans (frontmatter, wave numbering, `<tasks>` block, `<must_haves>`, `<acceptance_criteria>`).
- `.planning/phases/03-resume-on-start-and-error-recovery-req-2-fix/03-UAT.md` — Phase 3 UAT result that flagged TEST-01/02 harness gaps. Those gaps drive Scenario 2 in the verification script.

### Things the script and CI MUST NOT touch
- `internal/tmux/**` — read-only.
- `internal/session/instance.go`, `userconfig.go`, `storage*.go` — read-only.
- `.github/workflows/release.yml` — do not modify.

</canonical_refs>

<specifics>
## Specific Ideas

### Mandated paths (exact list — this set drives both the CLAUDE.md section and the CI `paths:` filter)
```
internal/tmux/**
internal/session/instance.go
internal/session/userconfig.go
internal/session/storage*.go
cmd/session_cmd.go
cmd/start_cmd.go
cmd/restart_cmd.go
scripts/verify-session-persistence.sh
```

### Exact grep-verifiables for each requirement (planner copies into acceptance_criteria)

- **SCRIPT-01**: `test -x scripts/verify-session-persistence.sh` AND `head -30 scripts/verify-session-persistence.sh | grep -E '^\[1\] '` returns a match.
- **SCRIPT-02**: `grep -c 'scripts/verify-session-persistence.d/fake-claude' scripts/verify-session-persistence.sh` ≥ 1 AND `grep -c 'tmux new-session\|agent-deck session start\|agent-deck add' scripts/verify-session-persistence.sh` ≥ 1.
- **SCRIPT-03**: `grep -c '/proc/.*/cgroup' scripts/verify-session-persistence.sh` ≥ 1 AND `grep -c 'PID=' scripts/verify-session-persistence.sh` ≥ 1.
- **SCRIPT-04**: `grep -c 'systemd-run --user --scope' scripts/verify-session-persistence.sh` ≥ 1 AND `grep -c 'systemctl --user stop' scripts/verify-session-persistence.sh` ≥ 1 AND `grep -c 'skipped: no systemd-run' scripts/verify-session-persistence.sh` ≥ 1.
- **SCRIPT-05**: `grep -Ec '\-\-resume|\-\-session-id' scripts/verify-session-persistence.sh` ≥ 2.
- **SCRIPT-06**: `grep -c '\[PASS\]\|\[FAIL\]' scripts/verify-session-persistence.sh` ≥ 4 AND `grep -c 'exit 1' scripts/verify-session-persistence.sh` ≥ 1.
- **SCRIPT-07**: `.github/workflows/session-persistence.yml` exists AND `grep -c 'verify-session-persistence.sh' .github/workflows/session-persistence.yml` ≥ 1 AND `grep -c 'verify-session-persistence.sh' CLAUDE.md` ≥ 1 (already true; verify).
- **DOC-01**: `grep -c 'TestPersistence_' CLAUDE.md` ≥ 8.
- **DOC-02**: `grep -c 'internal/tmux\|internal/session/instance\|internal/session/userconfig\|internal/session/storage\|cmd/session_cmd\|cmd/start_cmd\|cmd/restart_cmd' CLAUDE.md` ≥ 6.
- **DOC-03**: `grep -c '2026-04-14' CLAUDE.md` ≥ 1.
- **DOC-04**: `grep -c 'launch_in_user_scope' CLAUDE.md` ≥ 1 AND `grep -cE 'RFC' CLAUDE.md` ≥ 1.
- **DOC-05**: `grep -c '1.5.2' CHANGELOG.md` ≥ 1 AND `grep -ciE 'session.persistence' CHANGELOG.md` ≥ 1.

### Running order (suggested wave plan; planner finalizes)
- Wave 1 (parallelizable, independent files):
  - Plan 04-01: verification harness (`scripts/verify-session-persistence.sh` + `.d/fake-claude.sh`).
  - Plan 04-02: CLAUDE.md audit + DOC-05 CHANGELOG line.
- Wave 2 (depends on Wave 1 — CI needs the script to exist):
  - Plan 04-03: CI workflow `.github/workflows/session-persistence.yml`.
- Wave 3 (depends on everything):
  - Plan 04-04: run the script locally, capture output, attach to `04-VERIFY.md`, update `.planning/STATE.md`, final sign-off commit.

Three plans is the minimum. Planner may split 04-01 into skeleton + teardown scenario if diff gets big.

</specifics>

<deferred>
## Deferred Ideas

- **OBS log sanity as a NEW log emission**: the user brief mentions "OBS logs sanity" but OBS-01/02/03 are already Phase 2/3 deliverables. Phase 4 only READS the logs during the script (optional Scenario 5 that greps `~/.agent-deck/logs/*.log` for `tmux cgroup isolation` and `resume:`). If the grep returns zero rows on CI (because no real log file exists), the scenario SKIPs — does not fail. Implementing new log lines here is deferred; they are owned by Phases 2/3.
- **Broad `go test ./...` in CI**: out of scope. This phase only gates the `TestPersistence_*` suite. A repo-wide CI test matrix is a post-v1.5.2 concern.
- **Branch protection rules**: not editable from inside the repo. The user controls GitHub settings. The workflow existing is sufficient for SCRIPT-07.
- **Release tagging / `[1.5.2]` CHANGELOG heading**: deferred to a separate release cycle. Hard rule: no push/tag/PR.
- **Homebrew / goreleaser for v1.5.2**: not this phase.
- **Windows CI**: Windows has no systemd/tmux, so no sensible verification there. Linux-only CI is correct.

</deferred>

---

*Phase: 04-verification-harness-docs-and-ci-wiring*
*Context gathered: 2026-04-14 from user brief + spec REQ-4/REQ-5/REQ-6 + existing Phase 3 artifacts*
