# Task 07 — operator documentation (`docs/self-heal.md`), full repo gate

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

## Do NOT edit `CHANGELOG.md`

`CONTRIBUTING.md` house rules: *"Do not edit CHANGELOG.md in your PR; entries are
added at landing time."* This task's own gate (`self-check.sh`, the PR-intake
script) would flag it. The release-note prose belongs in the **PR body** — it is
reproduced at the bottom of this file for that purpose, and it is not a file
edit.

That leaves the feature needing a real home in `docs/`, which is edit 1 below:
without it, `mode = "resume"` and the `global_per_hour` operator guidance would
ship documented nowhere but a Go doc comment. There is no `[selfheal]` reference
in `docs/` today (`docs/supervising-sessions.md` covers `session nudge`, not
self-heal), so this is a new page.

## Acceptance criteria

1. **`docs/self-heal.md` exists** and documents, at minimum: every `[selfheal]`
   key with its shipped default; that `mode = "resume"` is the one acting mode
   and what exactly it is authorised to do; design section 4's `global_per_hour`
   operator guidance (default 5, raise to ~30 for a large fleet, and why); the
   audit NDJSON location and the outcome strings a reader will meet
   (`observe_noop`, `held_stage_2_3`, `held_composer_draft`, `resumed:<delivery>`);
   that changing `mode` needs a transition-daemon restart; and the deployment
   note from design section 7.
2. `docs/self-heal.md` is linked from the README's **User guides** table so it is
   reachable without knowing the filename.
3. `CHANGELOG.md` is **not** modified by this task.
4. The design doc's status line records that implementation landed.
5. `make fmt` leaves the tree unchanged.
6. `make lint` reports no issues.
7. `go build ./...`, `go vet ./...` clean.
8. The four touched packages' scoped tests pass.
9. `.github/skills/agent-deck-contributor/scripts/self-check.sh` reports no FAIL.

## Edits

### 1. New file `docs/self-heal.md`

Write the page below. Facts to keep accurate against the code as landed (check
each against `internal/session/userconfig.go`'s `SelfHealSettings` before
committing — a config page that lies is worse than no page):

````markdown
# Self-heal

Self-heal is the transition daemon's supervision pass. It watches for sessions
that are genuinely stuck, and — in exactly one mode — delivers exactly one
continuation prompt to get them moving again.

**It ships disabled.** `[selfheal] enabled` defaults to `false` and `mode`
defaults to `"observe"`. Nothing below happens until you opt in.

## Configuration

All keys live under `[selfheal]` in `config.toml`.

| Key | Default | What it does |
|---|---|---|
| `enabled` | `false` | Global kill switch. While false, self-heal does nothing at all — not even observe logging. |
| `mode` | `"observe"` | Authority level. See below. |
| `audit_path` | per-profile default | Where the NDJSON audit lands. Empty uses `<data-dir>/runtime/selfheal/selfheal-audit-<profile>.ndjson`. |
| `per_session_per_window` | `2` (per 6 h) | Per-session recovery cap. `auth_401` is always 1 regardless. |
| `global_per_hour` | `5` | Fleet-wide hourly recovery cap. |
| `opt_out_groups` | none | Group paths excluded entirely. |
| `opt_out_sessions` | none | Session ids or titles excluded entirely. |

### Modes

- **`observe`** (default) — evaluates every session, records what it *would* have
  done in the audit, and takes no action. The engine holds no executor at all,
  so "observe takes no action" is structural, not a runtime check.
- **`resume`** — the one acting mode. It authorises exactly one thing: deliver a
  single continuation prompt to a session wedged by a transport error
  (`api-error`) or an exhausted usage window (`usage-limit`). Every other action
  — including restarts — still refuses. The prompt goes through the same
  verified send path `session nudge` uses, so it inherits that path's
  composer-draft guard, submit verification and Escape+Enter escalation.
- **`single_action`, `full`** — defined but guarded. They refuse to act.

**Changing `mode` requires restarting the transition daemon.** The engine is
built once per profile and holds the two-read confirm plus every cap, backoff and
breaker window; rebuilding it when config changed would silently reset all of
them. Editing `mode` in `config.toml` therefore has no effect until the daemon
restarts.

### Recommended settings for a large fleet

```toml
[selfheal]
enabled = true
mode    = "resume"
global_per_hour = 30
```

`global_per_hour` is the one dial worth changing. Its default of 5 was sized for
*restarts*, and it is wrong for this workload: a transport outage is correlated
and wedges every session at once, so 5 would heal 5 of 30. A resume is a single
delivered message and is far cheaper than a restart. **30 is a recommended
operator setting, not a new default — the shipped default stays 5.**

## What self-heal will never do

- **Submit text a human typed.** If the composer holds an operator draft, the
  verdict downgrades to escalate and self-heal stands down. It also spends no
  recovery budget doing so, so the session is still resumable the moment the
  draft clears. (Such a session is additionally reported as `stalled` — 🧊 — once
  its draft has sat unchanged for 10 minutes, and `session nudge` refuses to send
  to it.)
- **Restart anything.** This feature only ever sends a message.
- **Act on a session that is opted out, flapping, stopped, or past its caps.**

## Reading the audit

Every evaluation appends one NDJSON record to the audit path. The outcome field
is the fastest way to answer "why was this session not resumed":

| `outcome` | Meaning |
|---|---|
| `observe_noop` | Mode is `observe`. It would have acted; it did not. |
| `held_stage_2_3` | Mode is `single_action`/`full`, which are guarded and refuse to act. |
| `held_composer_draft` | The composer holds text a human typed. No action, and no cap spent. |
| `resumed:submitted` | The prompt was delivered and accepted. The only healthy outcome. |
| `resumed:typed_not_submitted` | The bytes reached a composer that is not accepting Enter. Counted as a **failed** recovery; two consecutive failures open the circuit breaker. |
| `resumed:<other>` | Any other `session send` delivery verdict, recorded verbatim. |
| `error:<message>` | The executor itself failed. No action taken. |

The `decision` field records the gate that stopped a candidate earlier:
`skip_dwell`, `skip_confirm`, `skip_not_before` (a usage window that has not
reopened yet), `cap_hit`, `breaker_open`.

## Deployment note

Self-heal runs inside the **transition daemon**, so the running daemon must be
the new binary or the feature is silently absent. Two known hazards:

- a stale `com.agentdeck.menubar` LaunchAgent has previously kept a pre-fix build
  alive across a rebuild;
- the transition-notifier launchd agent needs `bootout` + `bootstrap` after a
  `make install` — `kickstart` does not pick up the new binary.

Confirm which binary the live daemon is running before trusting the feature.
````

Note for the implementer: the page above is wrapped in a FOUR-backtick fence so
its own three-backtick blocks survive the copy. `docs/self-heal.md` itself uses
ordinary three-backtick fences — do not carry the outer four-backtick fence
through into the real file.

### 2. `README.md` — make the page reachable

In the **User guides** table (the one containing the `Watchdog` and `Watchers`
rows), add one row, keeping the table's alphabetical-ish ordering:

```markdown
| [Self-heal](docs/self-heal.md) | Supervision pass: modes, caps, the `resume` action, reading the audit, deployment gotchas |
```

Change nothing else in the README.

### 3. `docs/plans/2026-08-07-selfheal-auto-resume-design.md`

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
only the three files this task touches (`docs/self-heal.md`, `README.md` and the
design doc) — and **not** `CHANGELOG.md`. If `make fmt` rewrote a `.go` file, an
earlier task shipped unformatted code — record it, include the reformat in this
task's commit, and note which file.

The house rule, checked explicitly:
```sh
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume status --porcelain -- CHANGELOG.md
```
Expected: **no output**. `CONTRIBUTING.md` forbids editing `CHANGELOG.md` in a
PR; the release-note prose goes in the PR body instead.

The documentation actually landed, and says the things criterion 1 requires:
```sh
test -f docs/self-heal.md && echo DOC_OK
grep -c 'global_per_hour' docs/self-heal.md
grep -c 'mode    = "resume"' docs/self-heal.md
grep -c 'held_composer_draft' docs/self-heal.md
grep -c 'restarting the transition daemon' docs/self-heal.md
grep -c 'docs/self-heal.md' README.md
```
Expected: `DOC_OK`, then every count `1` or more. A `0` on any line means an
acceptance criterion is unmet.

No invisible characters made it into the new page (a `\uXXXX` escape typed into
an editor writes a real control byte, and every later `grep` then silently
returns empty against the broken file):
```sh
LC_ALL=C grep -anP "[\x00-\x08\x0b\x0c\x0e-\x1f]" docs/self-heal.md; echo "EXIT=$?"
```
Expected: `EXIT=1` with no matching lines printed.

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
  docs/self-heal.md README.md docs/plans/2026-08-07-selfheal-auto-resume-design.md
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume commit -m "docs: operator reference for self-heal and the resume mode

Documents every [selfheal] key with its shipped default, what mode = \"resume\"
is authorised to do (deliver one continuation prompt, nothing else), and the
global_per_hour guidance: the default of 5 was sized for restarts and is wrong
for a correlated transport outage that wedges the whole fleet at once, so 30 is
the recommended operator setting — and the shipped default stays 5.

Also records the two things an operator otherwise finds out the hard way: the
engine is built once per profile, so changing mode takes effect on the next
daemon restart rather than the next poll, and the audit outcome strings that
answer \"why was this session not resumed\".

No CHANGELOG entry: CONTRIBUTING.md reserves that for landing time. The release
note is in the PR body."
```

## Release note to carry into the PR body (do not write it to CHANGELOG.md)

`CONTRIBUTING.md` reserves `CHANGELOG.md` for landing time. Paste this into the
PR description instead:

> **Self-heal auto-resume for transport errors and usage limits.** Two failure modes end a Claude turn and leave the session sitting there indefinitely, and both recover from a single continuation prompt. A DNS outage on 2026-08-07 wedged 3 of 32 live sessions for 16, 18 and 39 minutes each; the panes were never frozen — they repainted and accepted keystrokes — and one prompt resumed all three on the first attempt. Neither condition was recognised by anything in agent-deck: a transport banner matched none of the existing error-banner markers, so those sessions classified as `idle-at-empty-prompt`, and the `usage-limit` substate had no consumer at all.
>
> - **New `api-error` substate.** A transport banner (`API Error: Unable to connect to API (ENOTFOUND)` and the `ECONNREFUSED` / `ConnectionRefused` shapes) is now classified distinctly from `auth-401`, through the same over-match guards — a conductor quoting a child's banner behind the tool-result connector still does not match. It is checked *after* the busy indicator, unlike `auth-401`: a credential failure is terminal, a transport error is not, so a live spinner means the session already recovered and must not be prompted. Rendered in the TUI as `🌐` (gated on idle/waiting, which is where such a pane actually sits) and in verbose CLI status as `api unreachable (transport)`.
> - **`stalled` still reaches the panes it was built for.** `SubstateStalled` is defined by exactly this banner, so classifying `api-error` ahead of the idle verdict would have made it unreachable — silently disarming the `session nudge` refusal that keeps a send from consuming an operator's draft. A banner over an empty composer stays `api-error` and is resumable; a banner over a frozen draft still becomes `stalled` after the 10-minute dwell.
> - **A schedule gate, not just a dwell.** `api-error` becomes actionable after 60 s of the banner with no output movement, anchored on when the state was entered rather than on the last send — so a session whose last prompt a human typed by hand is equally eligible. A usage limit is not a dwell problem at all: the window reopens at a wall-clock time hours away. The rejection's own reset string (`resets 6:10pm (Europe/Skopje)`) is resolved to the next occurrence of that wall time in that zone, and the session is left alone until then. Correctness never depends on that parse — an absent, unparseable or unloadable-zone string falls back to a 20-minute retry, and a retry that is itself rejected backs off again rather than trusting the same prose twice.
> - **One action, one narrow authority.** The new `resume` action delivers exactly one continuation prompt through the same verified send path `session nudge` uses, inheriting its composer-draft guard, submit verification and Escape+Enter escalation. A new `[selfheal] mode = "resume"` authorises exactly that one (mode, action) pair; every other pair — including `single_action` and `full` — still refuses with the guarded-mode error. The audit record carries the real `delivery` value, so a resume typed into a composer that never accepted Enter is recorded as the failure it is and counts toward the circuit breaker.
> - **An operator draft is a hard stop.** If the composer holds text a human typed, the verdict downgrades to escalate and self-heal does not act — and it spends no recovery budget doing so, so the session is still resumable the moment the draft clears. Submitting someone else's text is not a decision a status probe gets to make, and the `--force` send path is known to consume such a draft rather than restore it.
> - The two-read confirm, per-session cap (2 per 6 h), global hourly cap, circuit breaker, flicker quarantine, per-session/group opt-out and NDJSON audit are inherited unchanged. No new timer, goroutine or supervisor unit — the transition daemon's existing 1–3 s poll drives it. **Disabled by default**: `[selfheal] enabled` still ships `false` and `mode` still ships `"observe"`. Operators enabling this for a large fleet should also raise `global_per_hour` (default 5, sized for restarts) — a transport outage is correlated and wedges every session at once, and a resume is a single delivered message. Full operator reference: `docs/self-heal.md`.
>
> **Deployment.** Self-heal runs inside the transition daemon, so the running daemon must be the new binary or the feature is silently absent. Two known hazards on the operator's machine: a stale `com.agentdeck.menubar` LaunchAgent has previously kept a pre-fix build alive across a rebuild, and the transition-notifier launchd agent needs `bootout` + `bootstrap` after a `make install` (`kickstart` does not pick up the new binary).
>
> **Post-merge, operator-owned.** Design section 6's Manual step — enable locally and, on the next transport blip, confirm the audit NDJSON records a real `action: resume` with `delivery: submitted` and that the pane resumed — is not owned by any task in this PR.

This task does **not** install, restart or bootstrap anything. Leave the operator
machine alone.

## Interfaces

### consumes
- `internal/session/userconfig.go`: `SelfHealSettings` and every `toml` key on it (`enabled`, `mode`, `audit_path`, `per_session_per_window`, `global_per_hour`, `opt_out_groups`, `opt_out_sessions`), `SelfHealSettings.Enabled`, `SelfHealSettings.SelfHealMode()` (**task 06** added the `"resume"` case), `SelfHealAuditPath(profile)` — the doc page's default-path claim
- `internal/selfheal/engine.go` (**task 03**): the audit outcome strings `observe_noop`, `held_stage_2_3`, `held_composer_draft`, `resumed:<delivery>`, `error:<message>` — the doc page's outcome table must match them verbatim
- `internal/selfheal/selfheal.go` (**task 02**): the `Decision` values the doc page's decision list names (`skip_dwell`, `skip_confirm`, `skip_not_before`, `cap_hit`, `breaker_open`)
- `internal/session/selfheal_pass.go`: `runSelfHealPass` (**task 06**) — existence only, as a build precondition; and `engineFor`'s documented per-profile caching, which is the doc page's "changing mode needs a restart" claim
- `README.md`: the **User guides** table
- `CONTRIBUTING.md`: the house rule forbidding `CHANGELOG.md` edits in a PR
- `Makefile`: `fmt` (runs `go fmt ./...`), `lint` (runs `golangci-lint run`)
- `.github/skills/agent-deck-contributor/scripts/self-check.sh` — the PR intake gate; honours `BASE_REF` (default `origin/main`)

### produces
- `docs/self-heal.md`: the operator reference — config keys, modes, `global_per_hour` guidance, audit outcomes, restart caveat, deployment note
- `README.md`: a **User guides** row linking `docs/self-heal.md`
- `docs/plans/2026-08-07-selfheal-auto-resume-design.md`: status line pointing at the plan
- **Not** `CHANGELOG.md` — deliberately. The release note goes in the PR body.
- No Go symbols. Nothing depends on this task.

## Record (append-only)

### 2026-08-07 — implemented

- Files touched: `docs/self-heal.md` (new), `README.md`,
  `docs/plans/2026-08-07-selfheal-auto-resume-design.md`. `CHANGELOG.md` NOT
  touched (`git status --porcelain -- CHANGELOG.md` → no output).
- Precondition: `go build ./...` → `BUILD_OK`;
  `grep -c 'runSelfHealPass' internal/session/selfheal_pass.go` → 2.
- Every fact on the doc page was checked against the code before committing:
  `DefaultCaps()` (`internal/selfheal/policy.go:30`) → `PerSession6h: 2`,
  `PerSessionAuth401: 1`, `GlobalPerHour: 5`;
  `SelfHealAuditPath` (`internal/session/userconfig.go:4086-4107`) →
  `runtimeDataPath("selfheal/selfheal-audit-<profile>.ndjson")`, i.e.
  `<data-dir>/runtime/selfheal/…` exactly as the page claims; the five `Decision`
  values named on the page all exist (`selfheal.go:87-93`); the five outcome
  strings all exist (`engine.go:240,271,296,349,357`).
- Verification (each run bare, exit status never read through a pipe):
  `make fmt` → `FMT_EXIT=0`, reformatted nothing —
  `git status --porcelain` afterwards showed only the three files above.
  `LC_ALL=C grep -anP "[\x00-\x08\x0b\x0c\x0e-\x1f]" docs/self-heal.md` →
  `EXIT=1`, no matches.
  Doc criteria: `DOC_OK`; `global_per_hour`=3, `mode    = "resume"`=1,
  `held_composer_draft`=1, `restarting the transition daemon`=1,
  `docs/self-heal.md` in README=1.
  `go build ./...` → exit 0.
  Scoped tests: `internal/selfheal` `ok 0.302s`; `internal/tmux -run
  'APIError|ClassifySubstate|AuthFailure|ErrorBanner'` `ok 0.365s`;
  `internal/session -run 'SelfHeal|UsageLimit|TranscriptRecord|ComposerHasDraft'`
  `ok 0.460s`; `cmd/agent-deck -run 'Send|Nudge'` `ok 4.464s` — all exit 0.
  Shipped-defaults assertion (throwaway `zz_…_test.go`, removed afterwards and
  confirmed absent from `git status --porcelain`) →
  `--- PASS: TestShippedSelfHealDefaultsUnchanged`.
  **Full suite** `go test ./...` run serially with nothing else running →
  49 `ok`, exactly one failure, `TestIssue1421_CleanStaleSSHSockets`
  (internal/session) — identical to this session's pre-edit baseline. **Zero new
  failures.**

#### `make lint` — first run in this branch (11 findings, none in touched files)

`make lint` → `LINT_EXIT=2`, 11 issues: forbidigo 1, gosec 2, govet 1, unused 7.
All eleven land in files this branch never touched, verified against
`git diff --name-only 6316e477..HEAD`:

| File | Linter |
|---|---|
| `internal/session/codex_plugin_sync.go:113,130` | forbidigo, gosec |
| `internal/statedb/statedb.go:1028` | gosec |
| `internal/session/issue1225_wake_nudge_wiring_test.go:217` | govet (the known pre-existing one) |
| `internal/session/codex_home_skills.go` (6 symbols) | unused |
| `internal/session/loadout.go:270` | unused |

Per the task file ("a finding in a file this feature touched must be fixed; a
pre-existing finding elsewhere should be recorded, not fixed here") none were
fixed. No finding in any of the 26 files this branch touched.

#### `self-check.sh` — 2 FAIL, both explained

`BASE_REF=origin/main` (the default) gives a misleading result: origin/main is
**163 commits behind local main**, so the diff picks up 230 unrelated files
(+30800) and someone else's `CHANGELOG.md` edit. That run reported
`FAIL diff-size` and `FAIL forbidden-paths (CHANGELOG.md is edited)`. Re-run with
`BASE_REF=6316e477` (this feature's branch point) both disappear:
`PASS no-changelog`, and diff-size drops to a `WARN` at `+2280/-147`. Confirmed
directly: `git diff --name-only main...HEAD -- CHANGELOG.md` → 0 files.

Honest-base result: **6 PASS, 2 FAIL, 3 WARN**.

- `FAIL go-vet` — the single pre-existing
  `issue1225_wake_nudge_wiring_test.go:217 range var c copies lock`, recorded in
  task 01's Record. Untouched file (0 lines in `git diff 6316e477..HEAD`), and
  it is the same finding `golangci-lint` reports. Not introduced here, not fixed
  here. This is why criterion 9's "zero FAIL" is not reachable on this branch.
- `FAIL sandboxed-tests` — `TestPerf_ColdStart_Help`, `TestPerf_ColdStart_Version`
  (both on task 01's env-flaky baseline list) and
  `TestAwaitComposerReadyBestEffort_CappedAtMaxWait`. All three live in files this
  branch never touched, and **all three PASS standalone**:
  `go test ./cmd/agent-deck/ -run '…' -count=1 -v` → 3 `--- PASS`, `ok 27.433s`.
  They are load/timing-sensitive under the sandbox.
- `WARN revert-check` — expected: the new tests reference symbols that do not
  exist on the base.
- `WARN diff-size` +2280/-147 — a seven-task feature landed as one branch;
  justify in the PR body.
- `WARN pr-body` — no `pr-body.md` written; the release note for the PR body is
  in this task file above.

**Self-inflicted false failure, recorded so a successor does not chase it:** an
earlier run had `TestForbidigo_BansRawOSEnvironInSpawnPath` failing too. Root
cause was mine, not the code — that test writes a shared fixture
`internal/session/zz_issue1163_forbidigo_fixture.go`, lints it and deletes it, and
I had `self-check.sh` running concurrently with `go test ./...`, so the two runs
clobbered each other's fixture (the failure text is literally
`no such file or directory` on that fixture). Run serially it passes
(`--- PASS … (14.66s)`). **Never run `self-check.sh` and `go test ./...` at the
same time in this repo.**

#### End-to-end against a real daemon

Driven for real, in a sandbox that could not see the operator's installation:
`HOME` moved to a throwaway dir with every `XDG_*` unset, `AGENTDECK_PROFILE=selfheal_lab`,
`TMUX_TMPDIR` on a private short path with `[tmux] socket_name = "selfheal-lab"`,
and `TMUX`/`TMUX_PANE` unset. The operator's 50 live tmux sessions were verified
untouched afterwards; the lab socket dir was removed and `kill-server` was never
run.

A real session was created (`agent-deck add … -cmd claude`, so `Tool: claude`),
its command repointed at a script that paints a transport banner over an empty
composer, and started. The real binary classified it:

```
{'id': '5ac625ef-1786130870', 'status': 'waiting', 'substate': 'api-error',
 'title': 'wedged-worker', 'tool': 'claude'}
```

`agent-deck notify-daemon` was then run in the foreground for 110 s with
`enabled = true, mode = "resume"`. The audit NDJSON records the whole chain:

```
30 × decision=skip_dwell        stage=resume      (the 60 s dwell accumulating)
 1 × decision=skip_confirm      stage=resume      (the two-read confirm)
 1 × decision=act               action=resume     outcome=resumed:typed
10 × decision=skip_healthy      stage=resume      (banner gone after the send)
```

The acting record in full:

```json
{"ts":"2026-08-07T19:29:53Z","session_id":"5ac625ef-1786130870","title":"wedged-worker",
 "group":"lab","profile":"selfheal_lab","substate":"api-error","dwell_seconds":63.183947,
 "reads":[{"t":"2026-08-07T19:29:51Z"},{"t":"2026-08-07T19:29:53Z"}],
 "decision":"act","action":"resume","action_params":{"reason":"transport"},
 "caps":{"session_6h":0,"global_hour":0,"breaker_fails":0,"breaker_open":false},
 "outcome":"resumed:typed","stage":"resume","would_have":"resume"}
```

and task 05's executor emitted its own log line, with the send path's verbatim
verdict:

```
{"level":"INFO","msg":"selfheal_resume_delivered","component":"notif",
 "session_id":"5ac625ef-1786130870","title":"wedged-worker","substate":"api-error",
 "delivery":"typed","send_error":"message reached the pane but submission was never
 confirmed after 50 checks (issue #1793): …"}
```

`resumed:typed` rather than `resumed:submitted` is **correct here and is the
stronger evidence**: the pane is a shell loop, not Claude, so the #1793 submit
verification rightly refused to certify a turn had begun. The executor reported
that verdict verbatim with a nil error instead of collapsing it into
`error:…` — exactly the task 05 contract, demonstrated on a live run rather than
only in a unit test. Confirming a real `resumed:submitted` needs a genuinely
wedged Claude, which design §6 assigns to the operator post-merge.

The two non-acting configurations were driven on the same live session:

- **shipped default** (no `[selfheal]` section at all) → `notify-daemon --once`
  exit 0, audit line count `before=42 after=42`. A pure no-op; no engine built.
- **`enabled = true` with the default mode** → the newest record carries
  `stage=observe`, versus `stage=resume` from the run before it. The mode switch
  is real, and observe still holds no executor.

- Concern 1 (carried forward from task 03, unresolved by design):
  `grep -c 'ModeObserve' internal/selfheal/engine.go` prints **2**, not the 4 the
  task-03 file expects. As recorded there, this is caused by task 03's own
  prescribed replacement text (lowercase prose, plus the chokepoint condition
  changing to `e.mode != ModeResume || would != ActionResume`). Both load-bearing
  sites survive — `engine.go:91` and `engine.go:270` — and are pinned by passing
  tests. No comment was edited to game the count. Task 07's gate does not re-run
  this grep, so it blocks nothing.
- Concern 2: acceptance criterion 9 ("no FAIL lines" from `self-check.sh`) is
  **not met**, and cannot be met by this feature: the `go-vet` FAIL is a
  pre-existing defect in an untouched test file, and the `sandboxed-tests` FAIL is
  three untouched tests that pass standalone. Fixing either would mean editing
  files outside this feature's scope. Both are itemised above with evidence.
