# Workers report to the session that launched them (issue #3165)

## Problem

Parent links are one level deep. A coordinator that is itself a sub-session
(a root launched it) cannot be the parent of the workers it launches:

- `[launch] nest_under_parent` off (default): the worker starts top-level with
  no link. Its done line, `NEED:` or question has no inbox to go to.
- `nest_under_parent` on: the worker is linked under the root, and the
  coordinator is kept only as a `launched-by` hint. The root gets the record;
  the coordinator, which asked for the work, gets nothing.

Either way the coordinator learns only by polling. The reporter measured 29
sub-sessions launching 73 workers over four weeks, none of which agent-deck
could wake.

## Decision

Keep the tree one level deep. Add one routing field, `reports_to`, and route
every comms record to it when it is set.

### 1. Recording the launcher

`selectLaunchParent` (one selector for `launch`, `add` and the capacity
pre-check) returns `launchedBy` whenever the auto-resolved caller is a
sub-session that cannot be the parent, on both paths:

| caller | `nest_under_parent` | tree parent | `reports_to` |
| --- | --- | --- | --- |
| human shell (no identity) | any | none | none |
| top-level session | any | caller | none (the parent already is the caller) |
| sub-session | off | none (top-level) | caller |
| sub-session | on | caller's parent | caller |
| sub-session whose parent is gone | on | none, with a note | caller |
| sub-session, `--no-parent` | any | none | none |
| any, `--parent X` | any | X (or the single-level error) | none |

`launch` and `add` store it as `Instance.ReportsTo` (tool_data extras zone,
no schema migration; a legacy binary keeps the key on save) and keep writing
the `launched-by` hint, now on both paths. `launch --json` / `add --json`,
`session show --json`, `session current --json` and `list --json` show
`reports_to`.

### 2. Routing

`Instance.ReportTargetID()` returns `ReportsTo` when set (and not self), else
`ParentSessionID`. Every comms decision that read `ParentSessionID` reads it
instead:

- inbox target and self-conductor/orphan guards (`resolveInboxParent`,
  `resolveParentNotificationTarget`, `isSelfSuppressedConductor`);
- the reply-copy dedup (`replySenderFor`): a sender equal to the report target
  already holds the record; the tree parent no longer does, so a root that
  sent a tagged message to a nested worker still gets its reply copy;
- stats bucket (`statsParentFor`), per-parent config overrides
  (`parentInstanceFor`), info digest wakes, idle-exit, restart and fresh-start
  lines, the comms ledger's observer (`parentOf`), dead-letter retry, and the
  inbox export's local-parent check.

The wake gate (`isWakeTarget`, `parentIsNudgeableIdle`) is unchanged: idle
only, debounced, never over composer text, never into a non-Claude pane, and
`wake_parents` stays opt-in. Only the target it is asked about changes.

With `nest_under_parent` on, the root gets no copy: it launched the
coordinator, not the worker, and cannot act on the result (the reporter's
lean, and the brief's "no duplicate" rule).

### 3. Kickoff tag

`tagLaunchKickoff` tags the kickoff when the caller is the session the child
reports to (`launchReportTarget`: the launcher, else the tree parent). The
worker's first turn is then a send from the coordinator, so a sentinel,
question or error wakes an idle Claude coordinator under the existing "it
asked, so wake it" rule, without `wake_parents`. `--no-tag`, `[send]
tag_sends`, `--no-transition-notify` and the non-Claude exclusions apply as
for #3131.

### 4. `session children`

`childrenOf(id)` lists sessions whose report target is `id`: tree children
plus `reports_to` workers. A `reports_to` worker is marked
`"reports_to": true` in `--json`. A nested worker that reports to the
coordinator is not listed under the root. The children context hook and
`--follow` use the same helper.

### 5. Changing the link

`session set-parent` and `session unset-parent` clear `reports_to`: a link the
user picks replaces the one launch recorded. A cleared value is written as an
explicit empty string so the extras merge does not carry it forward. A
cross-harness switch copies it to the replacement. The conditional archive
fence (`--expected-parent`) accepts the tree parent or the report target.

## `--no-parent` from a sub-session

The reporter counted 41 `-no-parent` launches from sub-sessions, noting it
"doesn't change the outcome". It records no `reports_to`. The identity block
documents `-no-parent` as "for a peer", and a peer reports to nobody,
the same as `--no-parent` from a top-level session. Routing it to the
launcher would leave no way to start an unlinked session from a coordinator.
Those launches were made when the flag made no difference; a coordinator
that wants the result now just drops the flag. This is listed as an open
question for the owner.

## `--no-transition-notify`

Unchanged: `reports_to` is recorded (so `session children` lists the worker),
but no records are sent, exactly as for a muted tree child.

## Not in scope

- Multi-level parent links (tree view, `set-parent`, cascade remove).
- The Codex-parent wake (the issue's second ask): a typed wake into a non-Claude
  pane remains off. That is a separate wake-policy decision.
- `prompt_suggestions = children-off` still keys on the tree link, as its
  docs say: a top-level `reports_to` worker keeps suggestions on.
- The web UI and TUI tree still show tree links only.

## Tests

`cmd/agent-deck/issue3165_reports_to_test.go`,
`internal/session/issue3165_reports_to_test.go`, and the updated
`TestLaunchParent_DefaultOffKeepsTheOldRule`,
`TestLaunchParent_DanglingParentStartsTopLevelWithNote`,
`TestLaunchParent_AddDefaultOffSubsessionCallerIsTopLevel`.
