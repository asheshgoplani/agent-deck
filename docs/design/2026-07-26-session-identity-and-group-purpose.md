# Session identity, display title, and group purpose — design

**Date:** 2026-07-26
**Issue:** [#1703](https://github.com/asheshgoplani/agent-deck/issues/1703) (reported by @kewtyboi)
**Status:** Design proposed — open for feedback, nothing implemented yet
**Labels:** `needs-design`, `design-proposed`

## Problem

Running many concurrent sessions (the reporter had 18+ live across several groups in a
single evening) exposes two distinct pains that both *look* like "naming":

1. **Titles stop being descriptive.** A session launched with a clear `-t` title can end up
   displayed as a short opaque handle, so the list no longer says what each session is for.
2. **Groups accumulate ad hoc.** Five sibling groups appeared in one profile in one evening
   with no recorded reason why any of them exists, and no way to see a group's purpose
   without opening every session inside it.

The instinct is to answer both with "define a naming convention". That is the wrong shape,
because a title is being asked to carry three unrelated jobs at once:

- a **machine handle** the launcher can rely on ("which dispatched unit of work is this?"),
- a **human label** for the list ("what is this, at a glance?"),
- an **organizational bucket** ("why do these belong together?").

A convention can only optimize for one of those. The rest of this document separates them,
inventories what already exists for each, and proposes the smallest additions that close
the real gaps.

## The three layers

| Layer | Question it answers | Who owns it | Mutability | Machine-readable? |
| --- | --- | --- | --- | --- |
| **Task identity** | Which dispatched unit of work is this? | the launcher (operator or conductor) | set once, never rewritten | yes — safe to key on |
| **Display title** | What is this session, at a glance? | the human, or the agent when title sync is on | freely mutable | **no** — never parse it |
| **Group purpose** | Why does this bucket exist? | the operator | mutable | yes (free text, for humans to read) |

A fourth relationship is already modelled separately and must not be folded into any of the
three: **parent/child linkage** (`Instance.ParentSessionID`) answers "who dispatched whom".

### Rule: parentage lives in linkage, not in names

Encoding dispatch relationships a second time in group names (a `work` group plus
`work-infra`, `work-hygiene`, `work-governance` siblings for workers dispatched by one
orchestrator) is the sprawl mechanism, not a symptom of it. It duplicates information the
registry already holds precisely, and the duplicate immediately drifts: reparenting a
session updates the linkage and leaves the group name lying.

Guidance, stated normatively so tooling can be built against it later:

- **R1 — Never encode parentage in a group name.** Use `--parent` / the auto-parenting that
  `launch` already applies, and read it back with `agent-deck session children`.
- **R2 — A group is warranted when its members share a *policy*, not a topic.** Groups
  already carry real, enforced policy: `max_concurrent` (serial vs bounded parallelism),
  `default_path`, and per-group Claude config / account resolution. If nothing
  policy-shaped differs between a proposed new group and its parent, the thing you have is
  a *topic*, and topics are carried by parent linkage plus the title — not by a new bucket.
- **R3 — Titles are for humans; identity is for machines.** Any script or agent that
  matches on a title is relying on something the user is explicitly allowed to change.
- **R4 — If you need a title to be stable, lock it.** Do not attempt to out-format the
  syncer with prefixes or suffixes; there is an explicit switch (below).

## What already exists

Much of the requested surface is already built. The gap for layer 2 is largely
*discoverability*, not capability.

### Display-title stability (layer 2) — complete, under-documented

| Control | Scope | Where |
| --- | --- | --- |
| `--title-lock` (alias `--no-title-sync`) on `agent-deck add` | per session, at creation | `cmd/agent-deck/main.go:1241` |
| `--title-lock` / `--no-title-sync` on `agent-deck launch` | per session, at creation | `cmd/agent-deck/launch_cmd.go:63` |
| `agent-deck session set-title-lock <id> <on\|off>` | per session, at runtime | `cmd/agent-deck/session_cmd.go:2452` |
| `sync_title = false` | whole installation | `UserConfig.GetSyncTitle()`, `internal/session/userconfig.go:1331` (default `true`) — [#1254](https://github.com/asheshgoplani/agent-deck/issues/1254) |
| Fork inherits an explicit title lock | forked sessions | `cmd/agent-deck/session_cmd.go:1225` |

Two related behaviours are also already correct and worth stating because they rule out
whole classes of suspected bug:

- **Folder-derived Claude names are ignored.** Claude Code 2.1.19x stamps
  `nameSource: "derived"` on a name it auto-derives from the cwd folder.
  `ClaudeSessionNameIn` treats a derived name as *no name at all*, including on the
  freshest entry, so it neither syncs nor lets a stale user name win
  (`internal/session/claude_title_reconcile.go`). Only a real `/rename` or `claude --name`
  can move a title.
- **A locked title cannot be silently overwritten.** The hook-triggered sync persists via a
  conditional `UPDATE ... WHERE title_locked = 0` and deliberately splits the *decision*
  (`ResolveTitleFromClaude`) from the tmux/badge side effects, so a rejected write cannot
  leave the terminal chrome showing a name the registry refused
  (`internal/statedb/update_title_if_unlocked_test.go`, `cmd/agent-deck/hook_name_sync.go`).

### Opaque titles: three different mechanisms, only one of them surprising

Short opaque handles in the list can come from any of these, and they need different
answers:

1. **`add --quick` / `-Q` (TUI `Q`)** generates a machine adjective-noun handle on purpose
   and sets `AutoName` (`cmd/agent-deck/main.go:1243`, `internal/session/namegen.go`). The
   TUI then shows the session's *live Claude task description* in place of the handle, and
   any explicit rename clears `AutoName` permanently. This is working as designed; the
   handle is meant to be invisible.
2. **Claude session-name sync** on a session whose title is not locked. Controlled by the
   table above.
3. **Collision suffixes.** Auto-derived names get numeric suffixes when basenames collide
   (`DeduplicateDirnames`, `internal/session/instance.go:513`) and there is a
   `<random8>-<unix>` fallback (`internal/session/instance.go:9202`).

Class 1 accounts for handles of the shape `adjective-noun`; class 3 accounts for
`basename-<n>`. Neither is a defect. **Open question O1** below asks the reporter for the
exact launch command behind the observed titles so we can confirm which class was hit —
the answer changes whether anything in layers 1–3 needs to move at all.

### Group purpose (layer 3) — genuinely missing

`session.Group` (`internal/session/groups.go:78`) carries `Name`, `Path`, `Expanded`,
`Order`, `DefaultPath`, `MaxConcurrent`. The `groups` table matches
(`internal/statedb/statedb.go:426`). There is **no field for why the group exists**, and
`group list` / `group show` therefore cannot answer it. That is the concrete gap behind
"no way to see group membership/purpose without cross-referencing every session".

### Task identity (layer 1) — missing, and currently borrowed from the title

There is no field whose contract is "stable, launcher-owned, never rewritten". Conductors
approximate one by putting a task key in the title (`SCRUM-351`) and then locking the
title — which works, but forces a choice between *a stable machine key* and *a title that
tracks what the agent is actually doing*. Those are different jobs (R3); one field cannot
serve both.

## Goals

- Give launchers a stable identity that no sync path can touch, **without** taking away
  title sync, which is genuinely useful for humans watching a list.
- Let a group state its own purpose, readable from `group list` / `group show` / `--json`.
- Make the existing title-stability controls easy to find.
- Make group sprawl *visible* at the moment it is created, advisorily.

## Non-goals

Per maintainer guidance on the issue, the first iteration stays advisory:

- **No enforced title format.** No regex gate, no required prefix/suffix, no rejection of a
  title the user chose. Hard naming enforcement is too opinionated across workflows.
- **No rejection of group creation.** Warn, never refuse; exit codes unchanged.
- **No automatic renaming or migration** of existing sessions or groups.
- **No new top-level noun.** Everything lands on `add` / `launch` / `session` / `group`.
- **No second relationship model.** Parent linkage stays the only encoding of dispatch.

## Design

Four changes. A, B, C are additive and independently shippable; D is documentation and
should land first because it may be sufficient on its own for layer 2.

### A. `task_id` — an explicit identity field (layer 1)

A new optional string on the session record, `task_id`:

- Set by `--task-id <string>` on `agent-deck add` and `agent-deck launch`. Empty by
  default, so behaviour for every existing session is unchanged.
- **Never** written by title sync, `AutoName`, fork, rename, or any hook path. The only
  writers are creation and an explicit `agent-deck session set-task-id <id> <value>`.
- Surfaced (omitempty) in `list --json`, `session show --json`, and
  `session children --json`, so a conductor can correlate a dispatched task to a live
  session without matching on the title.
- Rendered in the TUI only when set, as a dim prefix badge ahead of the title, so the
  human label stays the thing the eye lands on.
- Not unique-constrained. Two sessions may legitimately share a task id (a retry, a
  worktree pair). Deduplication is the caller's policy, not the registry's.

This is what makes R3 affordable: with identity available separately, leaving `sync_title`
on stops costing you your machine handle, and `--title-lock` becomes a *display* preference
rather than load-bearing infrastructure.

Rejected alternative: derive identity from a title convention (e.g. require a `[KEY]`
prefix and parse it back out). Rejected because it re-couples the two layers, breaks the
moment a human edits the title, and is exactly the "another title convention" the
maintainer asked us not to add.

### B. `group purpose` — one line of intent per bucket (layer 3)

- New `purpose TEXT NOT NULL DEFAULT ''` column on `groups`, added with the same idempotent
  `ALTER TABLE ... ADD COLUMN` + duplicate-column tolerance already used for
  `max_concurrent` (`internal/statedb/statedb.go:439`). Additive, so an older binary
  reading a newer DB is unaffected.
- `group create --purpose "<text>"`, `group update --purpose "<text>"`,
  `group update --clear-purpose` (mirroring the existing `--default-path` /
  `--clear-default-path` pair at `cmd/agent-deck/group_cmd.go:641`).
- Shown by `group show`, shown truncated by `group list`, and emitted as `purpose`
  (omitempty) in `group list --json`.
- Free text, length-capped (suggest 120 chars) and single-line. No schema, no vocabulary.

Suggested convention for what to write in it — documentation, not enforcement: state the
**policy** that justifies the group (R2), e.g. `serial: one release cut at a time` or
`account=work, default-path=~/src/api`. A purpose that only restates the group name is a
hint the group should not exist.

### C. Advisory sprawl warning on `group create`

Two cheap, local checks at creation time. Both print one line to **stderr** and change
nothing else — the group is always created, exit code is always unchanged:

1. **Near-duplicate sibling.** If the new group's name is within Damerau-Levenshtein
   distance ≤ 2 of an existing sibling under the same parent, or is a prefix/suffix of one:
   `note: sibling group 'work-infra' already exists under 'work' — reuse it if this is the same work (agent-deck group show work-infra)`
2. **One-off accumulation.** If the profile already has ≥ N groups holding ≤ 1 session
   (default N = 12):
   `note: 14 groups in this profile hold one session or fewer; consider parent linkage instead of new groups (see docs/design/2026-07-26-session-identity-and-group-purpose.md)`

Silenced by `--no-warn` or `[group_defaults] sprawl_warn = false`. Suppressed entirely when
`--json` or `--quiet` is in effect, so machine consumers never see it.

The distance threshold is deliberately tiny. A false warning costs one line of noise; a
false *silence* costs nothing at all, since this check has no safety role. Tuning should
err toward silence.

### D. Documentation (do this first)

- A **"Stable session titles"** subsection in the README Features area containing the
  control table above and this decision guide:

  | You want | Do this |
  | --- | --- |
  | This one session keeps the title I gave it | `--title-lock` at create, or `session set-title-lock <id> on` |
  | No session ever gets renamed by its agent | `sync_title = false` in config |
  | A stable key for scripts, but let the title track the agent | `--task-id` (change A), leave sync on |
  | A throwaway session; show me what it is doing | `add --quick` |

- A **"Groups vs parent linkage"** subsection carrying R1–R4 and the `--purpose`
  convention.
- Cross-link both from the existing conductor/fleet docs, since orchestrated fleets are
  where this bites.

## Components touched

- `internal/session/instance.go` — `TaskID` field + JSON tag; getters/setters if it is read
  off the render loop.
- `internal/session/groups.go` — `Group.Purpose`.
- `internal/statedb/statedb.go` — `instances.task_id` and `groups.purpose` columns,
  idempotent ALTERs, save/load round-trip.
- `cmd/agent-deck/main.go`, `launch_cmd.go` — `--task-id` flag.
- `cmd/agent-deck/session_cmd.go` — `set-task-id`, `session show` output, help text.
- `cmd/agent-deck/group_cmd.go` — `--purpose` / `--clear-purpose`, list/show output, JSON,
  sprawl warning.
- `internal/ui/` — dim task-id badge in the session row.
- `internal/web/` — `task_id` / `purpose` in the menu payload for parity
  (`tests/web/PARITY_MATRIX.md`).
- `README.md`.

## Error handling

- `--task-id` with leading/trailing whitespace is trimmed; empty after trimming is treated
  as unset, not an error.
- `set-task-id` on an unknown/ambiguous session uses the existing `ResolveSession` error
  path.
- `--purpose` longer than the cap is rejected with a clear message rather than silently
  truncated (silent truncation of user intent is worse than a retry).
- The sprawl warning must never fail a create. Any error inside the check (unreadable
  sibling list, etc.) is swallowed and the warning skipped.
- Older binary + newer DB: both new columns have defaults and are read with `omitempty`
  semantics, so a downgrade loses the values but never fails a load.

## Testing

- **`task_id` immutability:** a session with `task_id` set survives a Claude title sync, a
  rename, a fork, and `--quick` `AutoName` display with `task_id` byte-identical. This is
  the core contract and should be a table test over every writer of `Title`.
- **`task_id` absent by default:** existing create paths produce empty `task_id`, and
  `list --json` output for a pre-change session is byte-identical (omitempty).
- **Group purpose round-trip:** create with `--purpose`, reload, assert value; `--clear-purpose`
  empties it; migration test that opens a DB created *without* the column and asserts the
  ALTER is idempotent and no rows are lost (mirror
  `internal/statedb/save_groups_additive_test.go`).
- **Sprawl warning is advisory:** warning fires on a near-duplicate sibling; group is still
  created; exit code is 0; `--json` and `--quiet` suppress the line; and a set of
  deliberately dissimilar names produce **no** warning (false-positive guard).
- **Docs parity:** the README control table names flags that exist (a test that greps the
  documented flag strings out of the flag definitions would keep this honest).

## Rollout

1. **D (docs).** Ship alone. If the reporter's pain was discoverability of `--title-lock` /
   `sync_title`, this closes it with zero code.
2. **B + C (group purpose + advisory warning).** Small, self-contained, no cross-layer risk.
3. **A (`task_id`).** Largest surface (schema + CLI + TUI + web parity). Worth doing only if
   O1/O2 below confirm that conductors really are being forced to choose between a stable
   key and a live title.

## Open questions

- **O1.** @kewtyboi — for one of the sessions that showed an opaque title, what was the
  exact `add` / `launch` command (specifically: was `--quick`/`-Q` involved, and was
  `--title-lock` passed)? That distinguishes "working as designed" from a real
  overwrite bug worth its own issue.
- **O2.** Does `task_id` need to be visible in the TUI at all, or is `--json` enough? A
  badge costs horizontal space in the row that is already contended.
- **O3.** Should `launch` default `task_id` to something derived (e.g. the initial
  message's first token, or a short random key) when the flag is omitted? Defaulting makes
  the field reliably present for conductors; leaving it empty keeps output byte-stable.
  Current lean: leave empty, since a derived value would be a machine key nobody chose —
  the same failure mode as an opaque title.
- **O4.** Is `purpose` better as a group field or as an optional `[groups.<path>]` config
  entry? A DB column survives profile moves and is editable from the TUI; a config entry is
  reviewable in version control. Current lean: DB column, because `default_path` and
  `max_concurrent` set the precedent.
