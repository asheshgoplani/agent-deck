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

Only a Claude-compatible launcher is recorded as `reports_to`
(`reportsToLauncher`): those drain an inbox at prompt time and can be woken.
A shell sub-session is a person at a prompt (the "no change for a human
shell" rule), and another harness would hold records nobody reads, so those
launches keep the old routing. The `launched-by` hint is still written.

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
lean, and the brief's "no duplicate" rule). If the launcher is no longer
registered or is archived, a nested worker's records fall back to its tree
parent when that parent is registered and not archived (`reportTargetIDIn`),
as before this change. Otherwise the launcher id is kept: a top-level worker
whose launcher was removed goes to the unowned ledger as `parent_removed`,
like any child of a removed parent, and one whose launcher is archived waits
in the archived launcher's inbox, like any child of an archived parent.
Where the daemon has the registry in scope, the comms ledger and the stats
bucket use the same fallback (`statsParentForIn`, `parentOf`): the turn and
status records' recipient and a send's observers name the tree parent, not
the removed or archived launcher.

A launcher with workers that report to it cannot be switched to another
harness: the switch archives the source and the replacement gets a new id,
which would strand the workers' records. Both switch guards
(`SnapshotSwitchSource` and the commit-time check in
`CommitCrossHarnessSupersession`) count a `reports_to` worker as a dependent,
like a tree child.

### 3. Kickoff tag

`tagLaunchKickoff` tags the kickoff when the caller is the session the child
reports to (`launchReportTarget`: the launcher, else the tree parent). The
worker's first turn is then a send from the coordinator, so a sentinel,
question or error wakes an idle Claude coordinator under the existing "it
asked, so wake it" rule, without `wake_parents`. `--no-tag`, `[send]
tag_sends`, `--no-transition-notify` and the non-Claude exclusions apply as
for #3131.

### 4. `session children`

`childrenOf(id)` lists tree children plus sessions whose report target is
`id`. Each row carries `reports_to`, the launcher id, when the child has one,
so a nested worker shows under the root (tree) with `reports_to` naming the
coordinator, and under the coordinator. The root keeps its polling surfaces
(`--follow --until-done`, the fleet block) for nested workers. The children
context hook and `--follow` use the same helper.

### 4b. Prompt suggestions

Owner ruling: a session with `reports_to` counts as a child for
`prompt_suggestions = children-off` (`PromptSuggestionsDisabled` keys on
`ReportTargetID`), so a top-level worker starts with suggestions off like a
parented one.

### 5. Changing the link

`session set-parent`, `session unset-parent` and the TUI's promote, demote
and move-up paths clear `reports_to`: a link the
user picks replaces the one launch recorded. A default (top-level) worker has
`reports_to` but no tree parent; `unset-parent` and promote detach it from its
launcher, and `unset-parent` refuses only a session with neither link. Its
output says which link it cleared. A cleared value is written as an
explicit empty string so the extras merge does not carry it forward. A
cross-harness switch copies it to the replacement. The conditional archive
fence (`--expected-parent`) accepts the tree parent or the report target.

## `--no-parent` from a sub-session

Owner ruling: `--no-parent` stays as built. It is an explicit request for a
peer, and a peer reports to nobody, the same as `--no-parent` from a
top-level session; it records no `reports_to`. The reporter's 41
`-no-parent` launches were made when the flag made no difference; a
coordinator that wants its workers' results drops the flag.

## `--no-transition-notify`

Unchanged: `reports_to` is recorded (so `session children` lists the worker),
but no records are sent, exactly as for a muted tree child.

## Not in scope

- Multi-level parent links (tree view, `set-parent`, cascade remove).
- The Codex-parent wake (the issue's second ask): a typed wake into a non-Claude
  pane remains off. That is a separate wake-policy decision.
- Non-Claude launchers (Codex, other harnesses, shell sub-sessions) record no
  `reports_to`; supporting them is a follow-up, together with the Codex wake.
- The web UI and TUI tree still show tree links only.

## Tests

`cmd/agent-deck/issue3165_reports_to_test.go`,
`internal/session/issue3165_reports_to_test.go`, and the updated
`TestLaunchParent_DefaultOffKeepsTheOldRule`,
`TestLaunchParent_DanglingParentStartsTopLevelWithNote`,
`TestLaunchParent_AddDefaultOffSubsessionCallerIsTopLevel`.
