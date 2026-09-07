# Terminated completion protocol activation

This candidate adds opt-in recovery for vanished local Claude/Codex panes using
current-launch completion evidence. It does not add a Pi producer. Default sandbox
images do not contain the matching helper executable, so sandbox launches continue
without this positive fallback.

## Quiescent upgrade is required

Before activation, stop every Deck controller that can acquire these locks:
TUIs, watchers, one-shot CLI jobs, automation, and SSH launch paths sharing the
same state/locks namespace. Upgrade all executable paths and prevent later use
of legacy binaries. Agent processes and tmux sessions need not be killed.

Activation is an operator declaration that this coordinated boundary has been
established. Neither a file nor a process/version snapshot proves it. Invoking a
legacy controller later invalidates the exclusion guarantee. No live migration
or automatic activation is performed by this change.

After completing that operation, the operator may create a regular private
completion-protocol.json in the resolved Deck locks directory with:
version: 1, quiescent_upgrade_declared: true, and locks_namespace equal to the
canonical absolute path of that directory. These are JSON fields. An absent,
invalid, unsupported, or mismatched declaration disables positive fallback.

Before activation, an empty marker permits the existing terminated classification;
an occupied or ambiguous marker defers classification. Positive completion fallback
is disabled even if the marker is empty, since a legacy controller could still
have a delayed unlink operation.

## Ownership

Upgraded controllers retain a permanent per-instance guard inode for the whole
protected operation. Never delete or recreate guard files. Status uses nonblocking
locking. Versioned marker ownership contains a PID and random nonce. Only proven
ESRCH death is reclaimable under the guard after activation. Age, EPERM, malformed
records, and unversioned legacy markers are not proof of death.

Dead legacy markers require separately coordinated operator handling after
quiescence. The original baseline control that expects transparent dead legacy
recovery remains a failed unsupported case in the preserved baseline receipts.
It is not presented as fixed.

## Completion boundary

The replacement command invokes the currently running local Deck executable before
the agent. This stamps a one-shot precise boundary under hook writer locks. It does
not reacquire the parent-held spawn marker. Missing/invalid boundary or helper
failure cannot authorize completion. Old transcript sentinels remain invalid even
when emitted after early restart invalidation but before actual replacement.

The final status decision requires unique generation authority, matching durable
sequence, correct conversation/turn evidence, current timestamps, and no contrary
status. A missing tmux server is not completion proof.
