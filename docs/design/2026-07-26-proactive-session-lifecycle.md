# Proactive session lifecycle — reap-candidate design

**Date:** 2026-07-26
**Issue:** [#1704](https://github.com/asheshgoplani/agent-deck/issues/1704) (reported by @kewtyboi)
**Status:** Design proposed — open for feedback, nothing implemented yet
**Labels:** `accepted`, `needs-design`, `design-proposed`

## Problem

Sessions are managed reactively. They accumulate for hours or days, and periodically a human
or an orchestrating agent runs a verify-and-reap sweep to work out which are genuinely
finished. That sweep is real work: the reporter had to check git state, PR status, and tmux
liveness for **18 sessions individually** before any could be stopped, because nothing in
agent-deck answers *"this session's task is complete and its output has been durably landed
somewhere."*

This is structural, not a one-off. Session creation is cheap and dispatch-heavy workflows
spawn many bounded workers, so the list grows until something proactively manages it.

The dangerous version of "fix this" is an automatic reaper. A session that *looks* finished
but holds an uncommitted worktree is the one case where being wrong is unrecoverable. So the
first slice is deliberately **read-only**: assist the verification, do not perform the
removal.

## What already exists

### `agent-deck session cleanup` (alias `prune`) — the dead-and-old class

`cmd/agent-deck/session_cleanup_cmd.go` already implements a careful purge, and its safety
properties are the model this design follows rather than replaces:

- **Dry-run by default.** Nothing is deleted without `--yes` or an explicit interactive
  confirmation (`handleSessionCleanup`, line 200).
- **Liveness is probed, not inferred from stored status.** `newTmuxLivenessProbe` (line 135)
  runs one `list-sessions` per distinct socket. Stored `Status` is deliberately *not*
  trusted, because `StatusError` has a known false-positive class.
- **Indeterminate means alive.** A socket whose probe fails or times out marks every session
  on it alive, so it can never become a candidate (line 158). This is the single most
  important precedent in the file.
- **Protective timestamps.** `cleanupLastTouched` (line 81) takes the *later* of `CreatedAt`
  and `LastAccessedAt`, specifically so a recently-attached session that has since died is
  not purged for having been created long ago.
- **Startup grace.** `starting` / `queued` sessions are exempt for 5 minutes, but not
  indefinitely — an open-ended exemption made crash-during-start ghosts permanently
  unpurgeable (line 19).
- **Pins and archives are respected** unless `--force` / `--include-archived`, and the count
  retained-because-pinned is reported so the user knows something was deliberately kept.
- **Registry-only** unless `--prune-worktree`.

### Durable, non-destructive completion signal

`internal/session/completion_ledger.go` persists a `CompletionLedgerEntry` per child:
`{child_id, profile, title, status: ok|fail, summary, finished_at}`, written atomically,
last-wins, and read with `ReadLedgerEntry` (line 80) **without consuming anything**. This is
the piece that makes a candidate view possible at all — it is an already-shipped, already-durable
"the worker asserted it was done" record, independent of the destructive inbox.

Its upstream: the `===AGENTDECK_DONE=== status=<ok|fail> summary=<...>` sentinel
(`internal/session/done_sentinel.go`, [#1186](https://github.com/asheshgoplani/agent-deck/issues/1186)),
made reliable by `launch --assert-done` being **default-on for `-c claude`**
(`cmd/agent-deck/launch_cmd.go:211`).

### Other usable inputs

| Input | Where |
| --- | --- |
| Live status + Honest-Status-v2 substate | `Instance.Status`, `Instance.Substate()` |
| Parent/child linkage | `Instance.ParentSessionID`; `agent-deck session children [--follow]` |
| Worktree paths and branch | `WorktreePath`, `WorktreeRepoRoot`, `WorktreeBranch`, `WorktreeType` |
| Uncommitted-changes check | `git.HasUncommittedChanges` (`internal/git/git.go:957`); jj via `internal/jujutsu` |
| Reversible park | `session archive` / `unarchive`, `Instance.ArchivedAt` |
| Keep-this marker | `Instance.Pin` |

## The gap

`session cleanup` covers sessions that are **dead** and untouched for ≥ 30 days. The class
that actually piles up during orchestration is the opposite: **alive but finished** — a live
tmux pane sitting `waiting`, hours old, whose work has already landed in a merged PR. Those
sessions are invisible to `cleanup` by construction (the liveness probe reports them alive,
correctly), and there is no view that says *"here are the ones that look done, and here is
the evidence"*.

Secondary gap: the evidence a human gathers during a sweep (ledger entry, git cleanliness,
PR state, parent state) is never collated anywhere, so every sweep starts from scratch.

## Goals

- A **read-only** view that ranks sessions as reap candidates and **shows the evidence
  behind every classification**, so an operator or agent reviews a judgement instead of
  re-deriving it.
- Work offline, with no `gh`, no network, and a wedged tmux server. Degrade to `unknown`,
  never to a wrong confident answer.
- Never mutate anything — not the DB, not the ledger, not the inbox.
- Define the candidate schema and the false-positive test matrix **now**, before any
  automated action is designed on top of it.

## Non-goals

Explicit maintainer guidance on the issue, restated as hard constraints:

- **No silent deletion. Ever.** Not on a timer, not at a score threshold, not in a daemon.
- **No auto-stop** of a live session.
- **No background TTL reaper** in this slice. TTL stays advisory (Stage 2 sketch below).
- **`status = waiting` alone is never evidence of completion.** It is the normal end-of-turn
  state for every Claude session.
- **No hard dependency on `gh` or the network.**
- **Not a replacement for `session cleanup`.** Different class, shared predicates.

## Design — Stage 1: a read-only candidate view

`agent-deck status --stale [--stale-days N] [--with-pr] [--json]`

Chosen over a new top-level noun because `status` is already the "what is the state of my
deck" command and already has `--json` / `-v` conventions. `--stale` is additive: without it,
`status` output stays byte-identical.

Behaviour: loads sessions, computes a candidate record per session, prints a table (or
JSON), exits **0** regardless of what it finds. It is a view.

### Candidate schema

```json
{
  "id": "1a2b3c4d-1780000000",
  "title": "fix-1701-flag-parsing",
  "group": "work/fixes",
  "status": "waiting",
  "substate": "idle-at-empty-prompt",
  "age": "5d",
  "idle_for": "19h",
  "verdict": "likely-reapable",
  "score": 0.82,
  "signals": [
    {
      "name": "completion_sentinel",
      "value": "ok",
      "confidence": "high",
      "weight": 0.35,
      "evidence": "ledger 2026-07-25T09:11:04Z: \"opened PR #1701, CI green\""
    },
    {
      "name": "worktree_clean",
      "value": "clean",
      "confidence": "high",
      "weight": 0.20,
      "evidence": "git status --porcelain empty in <repo>/.worktrees/fix-1701"
    },
    {
      "name": "branch_pushed",
      "value": "pushed",
      "confidence": "high",
      "weight": 0.15,
      "evidence": "rev-list fix/1701-slug@{u}..HEAD = 0 commits ahead"
    },
    {
      "name": "idle_duration",
      "value": "19h",
      "confidence": "high",
      "weight": 0.12,
      "evidence": "last activity 2026-07-25T14:02:11Z, threshold --stale-days 1"
    },
    {
      "name": "linked_pr",
      "value": "unknown",
      "confidence": "none",
      "weight": 0.0,
      "evidence": "skipped: --with-pr not set"
    },
    {
      "name": "parent_state",
      "value": "parent-finished",
      "confidence": "medium",
      "weight": 0.10,
      "evidence": "parent 9f8e7d6c ledger status=ok at 2026-07-25T10:40:00Z"
    }
  ],
  "blockers": [],
  "recommendation": "review, then `agent-deck session archive 1a2b3c4d`"
}
```

Schema rules — these are the contract, not the presentation:

1. **Every signal carries `value`, `confidence`, and a human-readable `evidence` string.** A
   row a reviewer cannot audit from the output alone is a bug. `evidence` is the whole point
   of the feature: it is the sweep work, cached.
2. **`unknown` is a first-class value and contributes weight `0.0`.** Absence of information
   is never scored as "done". A missing upstream is `unknown`, not "not pushed"; an absent
   `gh` is `unknown`, not "no PR".
3. **`confidence` ∈ {`high`, `medium`, `low`, `none`}.** `high` means directly observed
   (ledger entry read, `git status` run). `medium` means inferred one hop away (parent's
   ledger, PR matched by branch name). `none` accompanies `unknown`.
4. **`blockers` are hard vetoes.** Any non-empty `blockers` forces `verdict` away from
   `likely-reapable` regardless of `score`. Blockers are not weighted; they are absolute.
5. **`score` is advisory and always displayed.** It exists to *order* the review queue. It
   must never be the sole input to an action, and no threshold in this design triggers a
   mutation.
6. **Additive only.** New signals may be appended; existing signal `name`s and the top-level
   keys are stable. Consumers must tolerate unknown signal names.

### Blockers (absolute vetoes)

| Blocker | Why |
| --- | --- |
| `running` / `starting` / `queued` | actively working; verdict becomes `active` |
| `within_startup_grace` | mirrors `cleanupStartupGrace`; boot is not idleness |
| `pinned` | the user's explicit keep marker |
| `archived` | archiving is already a deliberate keep (excluded unless `--include-archived`) |
| `is_conductor` | orchestrators are long-lived by design and must never be suggested |
| `worktree_dirty` | **the unrecoverable case.** Uncommitted work in a worktree |
| `commits_unpushed` | work exists only on this machine |
| `live_children` | has sub-sessions not themselves reapable |
| `liveness_indeterminate` | tmux probe failed/timed out — same assume-alive rule as `cleanup` |
| `sentinel_stale` | ledger `finished_at` predates last activity: the session kept working after asserting done, so the assertion no longer describes current state |
| `sentinel_fail` | `status=fail` means unfinished work, and is *more* interesting than no signal at all |

`sentinel_stale` deserves emphasis: last-wins ledger semantics mean an old `ok` can sit on a
session that has since been given new work. Comparing `finished_at` against last activity is
the cheap guard, and without it the highest-weight signal in the system is also the one most
likely to be wrong.

### Verdict ladder

```
any blocker AND status in {running, starting, queued}   -> "active"
any other blocker                                       -> "needs-review"  (blockers listed)
no blocker AND score >= 0.70 AND >= 2 high-confidence
  signals with value != unknown                         -> "likely-reapable"
no blocker AND some evidence                            -> "needs-review"
nothing known beyond age                                -> "unknown"
```

The **two-independent-high-confidence-signals floor** is the false-positive control. A long
idle time alone, or a clean tree alone, can never produce `likely-reapable` — because
neither is evidence that work *landed*, only that nothing is happening right now.

### Signal sources — no new subsystems

| Signal | Source | Failure mode |
| --- | --- | --- |
| `completion_sentinel` | `session.ReadLedgerEntry` (`completion_ledger.go:80`) | no entry → `unknown` |
| `idle_duration` | `cleanupLastTouched` + `GetLastActivityTime` | zero timestamps → `unknown`, never "old" |
| `liveness` | `newTmuxLivenessProbe` (`session_cleanup_cmd.go:135`), reused verbatim | probe fails → blocker |
| `worktree_clean` | `git.HasUncommittedChanges`; `internal/jujutsu` for `WorktreeType == "jujutsu"` | error → `unknown` **and** a `worktree_indeterminate` blocker (cannot prove clean ⇒ do not suggest) |
| `branch_pushed` | new helper `git.AheadOfUpstream(dir)` → `git rev-list --count @{u}..HEAD` | no upstream → `unknown`; error → `unknown` |
| `linked_pr` | **opt-in `--with-pr`.** `gh pr list --head <branch> --state all --json number,state` with a short timeout | `gh` missing / offline / timeout → `unknown`, view still renders, exit 0 |
| `parent_state` | `ParentSessionID` + parent's ledger entry | no parent → signal omitted |
| `live_children` | existing children lookup + each child's own verdict | — |

`--with-pr` is off by default on purpose: it is the only signal that shells out to the
network, it is the slowest, and a `status --stale` that hangs behind a network stall is
worse than one that reports `unknown`.

### Human output sketch

```
$ agent-deck status --stale --stale-days 1
LIKELY REAPABLE (2)
  1a2b3c4d  fix-1701-flag-parsing        waiting  idle 19h  score 0.82
            done: ok "opened PR #1701, CI green" (2026-07-25T09:11Z)
            worktree clean; branch fix/1701-slug 0 ahead of upstream
            -> review, then: agent-deck session archive 1a2b3c4d
  ...

NEEDS REVIEW (3)
  5e6f7a8b  release-cut-v1.10.11         waiting  idle 2d   score 0.55
            BLOCKED: worktree_dirty (3 modified files in <repo>/.worktrees/release)
            done: ok "tagged v1.10.11" (2026-07-24T18:02Z)
            -> uncommitted work present; do not remove

ACTIVE (11)   UNKNOWN (4)
Nothing was changed. This is a read-only view.
```

The closing line is not decoration. The failure mode this feature must avoid is a user
believing a cleanup happened.

### Stage 2 (deferred — sketch only, do not implement with Stage 1)

Once the candidate schema has been exercised against real decks:

- `[lifecycle]` config: `stale_days`, `review_prompt = true` (advisory thresholds only).
- `session cleanup --include-live`, reusing the *same* candidate function, prompting per
  session and printing the full evidence block before each prompt. `--yes` still required;
  no new bypass.
- **The default offered action is `archive`, not `remove`.** Archive is reversible
  (`unarchive` exists), removal is not. A lifecycle feature should push users toward the
  reversible door.

## Components touched

- `internal/session/` — new `lifecycle` file (or `candidate.go`) holding the pure
  `EvaluateCandidate(inst, deps) Candidate` function; deps injected (clock, liveness probe,
  git checks, optional PR lookup) so it is testable with no tmux, no git, and no network.
- `cmd/agent-deck/main.go` — `--stale`, `--stale-days`, `--with-pr`, `--include-archived` on
  `handleStatus`; table + JSON rendering.
- `cmd/agent-deck/session_cleanup_cmd.go` — export the liveness probe and
  `cleanupLastTouched` for reuse; no behaviour change.
- `internal/git/git.go` — `AheadOfUpstream`.
- Docs: README lifecycle subsection; cross-link from the fleet/conductor docs.

## Error handling

- Any signal that errors becomes `unknown` with the error text in `evidence`. Nothing is
  fatal; the view always renders.
- Unprovable cleanliness is a blocker, not a neutral unknown. `worktree_clean: unknown`
  implies `worktree_indeterminate` — the asymmetry is intentional, because the cost of
  wrongly suggesting a dirty worktree is unrecoverable and the cost of wrongly withholding a
  suggestion is one manual check.
- `--stale-days` negative → clear error, exit 1 (matches `cleanup --days` validation).
- Empty deck → empty candidate list, exit 0.
- `--json` output is a single object `{"candidates": [...], "counts": {...}}` so future
  top-level fields stay additive.

## Testing

The false-positive matrix the maintainer asked for before any automation. Each row is a unit
test against `EvaluateCandidate` with injected deps — no tmux, no git, no network.

| # | Scenario | Required outcome |
| --- | --- | --- |
| 1 | sentinel `ok` + dirty worktree | `needs-review`, blocker `worktree_dirty` |
| 2 | sentinel `ok` + commits ahead of upstream | `needs-review`, blocker `commits_unpushed` |
| 3 | sentinel `ok` + pinned | `needs-review`, blocker `pinned` reported |
| 4 | sentinel `ok` + `is_conductor` | never `likely-reapable` |
| 5 | sentinel `fail` | never `likely-reapable`; blocker `sentinel_fail` |
| 6 | long idle + no sentinel + clean tree + no PR data | `unknown` (fails the two-signal floor) |
| 7 | archived session | excluded unless `--include-archived` |
| 8 | `starting`, within grace | `active`, blocker `within_startup_grace` |
| 9 | tmux probe indeterminate | blocker `liveness_indeterminate` (assume alive) |
| 10 | parent still running, child looks reapable | child `needs-review` |
| 11 | ledger `finished_at` **older** than last activity | blocker `sentinel_stale` |
| 12 | `gh` absent / times out with `--with-pr` | `linked_pr: unknown`, exit 0, view renders |
| 13 | `git` errors on the worktree | `worktree_clean: unknown` **and** blocker |
| 14 | zero-valued `CreatedAt` / `LastAccessedAt` | `idle_duration: unknown`, not "very old" |
| 15 | empty deck | empty list, exit 0 |

Plus two integration-level invariants that are the real safety net:

- **Read-only proof.** Run `status --stale` against a populated sandbox profile and assert:
  `state.db` mtime and size unchanged, every completion-ledger file byte-identical, and the
  session's inbox still fully drainable afterwards (nothing consumed). This test is the
  feature's core promise and should fail loudly if any future signal starts writing.
- **Evidence completeness.** For every signal in every candidate, `evidence` is non-empty.
  A signal that cannot explain itself must not ship.

All test runs must be sandboxed per the repo `CLAUDE.md`
(`HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME=`), and any tmux server a
test spawns must be killed by teardown on the same socket it was created on.

## Rollout

1. `EvaluateCandidate` + the full test matrix, no CLI surface. Pure function, zero risk.
2. `status --stale` human output.
3. `--json` (freeze the schema here — it becomes a consumer contract).
4. `--with-pr`.
5. Stage 2, only after real-deck feedback, and only in the archive-first, confirm-always
   shape above.

## Open questions

- **O1.** Should `status --stale` grow `--exit-nonzero-if-candidates` so a conductor
  heartbeat can cheaply detect "the deck needs a review pass" without parsing JSON? Useful,
  but it makes a *view* carry a signal in its exit code. Current lean: no — parse the JSON.
- **O2.** Is `score` worth exposing at all, given every action requires human confirmation?
  It orders the queue, but a visible number invites automation against it. Alternative: drop
  `score` and order by (verdict, idle_for). Current lean: keep it in `--json`, omit it from
  human output.
- **O3.** Should `linked_pr` matching be by branch name only, or should the sentinel summary
  be scanned for a PR URL? Branch matching is more reliable; summary scanning catches
  non-worktree sessions. Both are `medium` confidence at best.
- **O4.** @kewtyboi — in your 18-session sweep, which check did the most work: PR state, git
  cleanliness, or tmux liveness? That should get the highest weight and the best evidence
  string.
