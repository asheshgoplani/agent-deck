# Task 07 — CHANGELOG, config documentation, full repo gate

tier: mid
depends on: tasks 01–06 (all of them)
parallel with: nothing
worktree: `/Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` (branch `feature/selfheal-auto-resume`)

Use absolute paths under that worktree for every Read/Edit/Write, and
`git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` for
every git command. Never run `git stash`, `git checkout`, `git switch`, or
`git reset`; never edit the root checkout at `/Users/doozyx/DoozyX/agent-deck`.

**Precondition to check first:**
```sh
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume
go build ./... && echo BUILD_OK
grep -c 'runSelfHealPass' internal/session/selfheal_pass.go
```
Expected: `BUILD_OK` and a count of `1` or more. If the build fails or the count
is `0`, task 06 has not landed — stop and report BLOCKED.

---

## Design extracts (verbatim from the approved design)

> ## 4. Configuration
>
> ```toml
> [selfheal]
> enabled = true
> mode    = "resume"
> global_per_hour = 30
> ```
>
> No new dial. `global_per_hour` already exists, but its default of 5 is wrong for
> this workload: a transport outage is correlated and wedges every session at once,
> so 5 would heal 5 of 30. The cap was sized for restarts; a resume is a single
> delivered message and is far cheaper. 30 is the recommended operator setting, not
> a new default — the shipped default stays 5.

> ## 5. Out of scope
>
> - **Wiring `SubstateStalled` in.** Its 10-minute dwell requires text already
>   sitting in the composer, which D6 refuses to submit. Wiring it in would produce
>   a detector that can only ever escalate. The drafted-composer wedge stays manual.
> - **`model-unavailable` and `auth-401` recovery.** Stages 2–3 remain guarded.
> - **Restart actions.** This design only ever sends a message.
> - **Changing the shipped defaults.** `enabled` stays false; `mode` stays
>   `observe`.

> ## 6. Verification
>
> **Manual.** Enable locally; on the next transport blip confirm the audit NDJSON
> records a real `action: resume` with `delivery: submitted`, and that the pane
> resumed.

> ## 7. Deployment note (operator machine)
>
> Self-heal runs inside the transition daemon, so **the running daemon must be the
> new binary** or the feature is silently absent. Two known local hazards:
>
> - the `com.agentdeck.menubar` LaunchAgent has previously kept a stale pre-fix
>   build alive across a rebuild;
> - the transition-notifier launchd agent needs `bootout` + `bootstrap` after a
>   `make install` — `kickstart` does not pick up the new binary.
>
> Confirm which binary the live daemon is running before trusting the feature.

---

## Acceptance criteria

1. `CHANGELOG.md` has an `### Added` entry under `## [Unreleased]`.
2. The design doc's status line records that implementation landed.
3. `make fmt` leaves the tree unchanged.
4. `make lint` reports no issues.
5. `go build ./...`, `go vet ./...` clean.
6. The four touched packages' scoped tests pass.
7. `.github/skills/agent-deck-contributor/scripts/self-check.sh` reports no FAIL.

## Edits

### 1. `CHANGELOG.md`

The file currently reads:

```
## [Unreleased]

## [1.11.0] - 2026-08-01
```

Insert between them:

```markdown
## [Unreleased]

### Added

- **Self-heal auto-resume for transport errors and usage limits.** Two failure modes end a Claude turn and leave the session sitting there indefinitely, and both recover from a single continuation prompt. A DNS outage on 2026-08-07 wedged 3 of 32 live sessions for 16, 18 and 39 minutes each; the panes were never frozen — they repainted and accepted keystrokes — and one prompt resumed all three on the first attempt. Neither condition was recognised by anything in agent-deck: a transport banner matched none of the existing error-banner markers, so those sessions classified as `idle-at-empty-prompt`, and the `usage-limit` substate had no consumer at all.
  - **New `api-error` substate.** A transport banner (`API Error: Unable to connect to API (ENOTFOUND)` and the `ECONNREFUSED` / `ConnectionRefused` shapes) is now classified distinctly from `auth-401`, through the same over-match guards — a conductor quoting a child's banner behind the tool-result connector still does not match. It is checked *after* the busy indicator, unlike `auth-401`: a credential failure is terminal, a transport error is not, so a live spinner means the session already recovered and must not be prompted. Rendered in the TUI as `🌐` and in verbose CLI status as `api unreachable (transport)`.
  - **A schedule gate, not just a dwell.** `api-error` becomes actionable after 60 s of the banner with no output movement, anchored on when the state was entered rather than on the last send — so a session whose last prompt a human typed by hand is equally eligible. A usage limit is not a dwell problem at all: the window reopens at a wall-clock time hours away. The rejection's own reset string (`resets 6:10pm (Europe/Skopje)`) is resolved to the next occurrence of that wall time in that zone, and the session is left alone until then. Correctness never depends on that parse — an absent, unparseable or unloadable-zone string falls back to a 20-minute retry, and a retry that is itself rejected backs off again rather than trusting the same prose twice.
  - **One action, one narrow authority.** The new `resume` action delivers exactly one continuation prompt through the same verified send path `session nudge` uses, inheriting its composer-draft guard, submit verification and Escape+Enter escalation. A new `[selfheal] mode = "resume"` authorises exactly that one (mode, action) pair; every other pair — including `single_action` and `full` — still refuses with the guarded-mode error. The audit record carries the real `delivery` value, so a resume typed into a composer that never accepted Enter is recorded as the failure it is and counts toward the circuit breaker.
  - **An operator draft is a hard stop.** If the composer holds text a human typed, the verdict downgrades to escalate and self-heal does not act. Submitting someone else's text is not a decision a status probe gets to make, and the `--force` send path is known to consume such a draft rather than restore it.
  - The two-read confirm, per-session cap (2 per 6 h), global hourly cap, circuit breaker, flicker quarantine, per-session/group opt-out and NDJSON audit are inherited unchanged. No new timer, goroutine or supervisor unit — the transition daemon's existing 1–3 s poll drives it. **Disabled by default**: `[selfheal] enabled` still ships `false` and `mode` still ships `"observe"`. Operators enabling this for a large fleet should also raise `global_per_hour` (default 5, sized for restarts) — a transport outage is correlated and wedges every session at once, and a resume is a single delivered message.
```

Keep the rest of the file untouched.

### 2. `docs/plans/2026-08-07-selfheal-auto-resume-design.md`

Change line 3 from:

```
Status: approved 2026-08-07
```

to:

```
Status: approved 2026-08-07 · implemented on `feature/selfheal-auto-resume`
Implementation plan: `docs/plans/2026-08-07-selfheal-resume-plan.md`
```

Leave every other line of the design untouched — it is the approved record.

## Verification

Run each command bare. Never pipe a check into `tail`/`head` and read that exit
status; if you must capture output, write it to a file and read an `EXIT=$?` line.

```sh
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume
make fmt
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume status --porcelain
```
Expected: `make fmt` produces no reformatting, and `git status --porcelain` shows
only the two files this task edits (`CHANGELOG.md` and the design doc). If
`make fmt` rewrote a `.go` file, an earlier task shipped unformatted code —
record it, include the reformat in this task's commit, and note which file.

```sh
make lint
```
Expected: golangci-lint runs and exits 0 with no findings printed. A finding in a
file this feature touched must be fixed; a pre-existing finding elsewhere should
be recorded, not fixed here.

```sh
go build ./... && go vet ./...
```
Expected: no output, exit 0.

```sh
go test ./internal/selfheal/ -count=1
```
Expected: `ok  	github.com/asheshgoplani/agent-deck/internal/selfheal`.

```sh
go test ./internal/tmux/ -run 'APIError|ClassifySubstate|AuthFailure|ErrorBanner' -count=1
```
Expected: `ok  	github.com/asheshgoplani/agent-deck/internal/tmux`.

```sh
go test ./internal/session/ -run 'SelfHeal|UsageLimit|TranscriptRecord|ComposerHasDraft' -count=1
```
Expected: `ok  	github.com/asheshgoplani/agent-deck/internal/session`.

```sh
go test ./cmd/agent-deck/ -run 'Send|Nudge' -count=1
```
Expected: `ok  	github.com/asheshgoplani/agent-deck/cmd/agent-deck`. This is the
non-regression check on the send path the executor now shares.

**Invariant check — the shipped defaults must not have moved.** Write a
throwaway assertion rather than eyeballing it:

```sh
cat > /tmp/selfheal_default_check_test.go <<'EOF'
package session

import "testing"

func TestShippedSelfHealDefaultsUnchanged(t *testing.T) {
	s := SelfHealSettings{}
	if s.Enabled {
		t.Fatal("[selfheal] enabled must ship false")
	}
	if got := s.SelfHealMode(); got != "observe" {
		t.Fatalf("[selfheal] mode must ship observe, got %q", got)
	}
}
EOF
cp /tmp/selfheal_default_check_test.go internal/session/zz_selfheal_default_check_test.go
go test ./internal/session/ -run TestShippedSelfHealDefaultsUnchanged -count=1 -v
/bin/rm -f internal/session/zz_selfheal_default_check_test.go /tmp/selfheal_default_check_test.go
```
Expected: `--- PASS: TestShippedSelfHealDefaultsUnchanged`. Remove the temporary
file afterwards (the `/bin/rm -f` above does it — `rm` is aliased to interactive
mode on this machine, so the absolute path is required).

Confirm it is gone before committing:
```sh
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume status --porcelain
```
Expected: no `zz_selfheal_default_check_test.go` in the output.

**The contributor gate.** It compares against `origin/main`, so fetch first:

```sh
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume fetch origin main
bash .github/skills/agent-deck-contributor/scripts/self-check.sh
```
Expected: a `PASS`/`WARN`/`skip` table with **zero `FAIL` lines**. The script
exits 0 when there is no FAIL. `WARN` lines must be recorded below with a
one-line resolution each, not silently ignored. Note that the script runs
`go vet ./...` and `go build ./...` in a sandboxed HOME; a failure there that
does not reproduce outside the sandbox is a HOME-dependency in a test, which is
worth recording.

## Commit

```sh
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume add \
  CHANGELOG.md docs/plans/2026-08-07-selfheal-auto-resume-design.md
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume commit -m "docs: changelog for self-heal auto-resume

Records the transport-error and usage-limit resume path, the api-error substate,
the NotBefore schedule gate and the narrow resume mode — and states plainly that
the shipped defaults are unchanged (enabled false, mode observe) so an operator
reading the entry does not assume the fleet started acting on its own."
```

## Deployment note to carry into the PR body (do not act on it here)

Self-heal runs inside the transition daemon, so the running daemon must be the
new binary or the feature is silently absent. Two known local hazards on the
operator's machine:

- the `com.agentdeck.menubar` LaunchAgent has previously kept a stale pre-fix
  build alive across a rebuild;
- the transition-notifier launchd agent needs `bootout` + `bootstrap` after a
  `make install` — `kickstart` does not pick up the new binary.

This task does **not** install, restart or bootstrap anything. Put the note in
the PR body and leave the operator machine alone.

## Interfaces

### consumes
- `internal/session/userconfig.go`: `SelfHealSettings`, `SelfHealSettings.Enabled`, `SelfHealSettings.SelfHealMode()` (**task 06** added the `"resume"` case)
- `internal/session/selfheal_pass.go`: `runSelfHealPass` (**task 06**) — existence only, as a build precondition
- `Makefile`: `fmt` (runs `go fmt ./...`), `lint` (runs `golangci-lint run`)
- `.github/skills/agent-deck-contributor/scripts/self-check.sh` — the PR intake gate; honours `BASE_REF` (default `origin/main`)

### produces
- `CHANGELOG.md`: an `### Added` block under `## [Unreleased]`
- `docs/plans/2026-08-07-selfheal-auto-resume-design.md`: status line pointing at the plan
- No Go symbols. Nothing depends on this task.

## Record (append-only)
