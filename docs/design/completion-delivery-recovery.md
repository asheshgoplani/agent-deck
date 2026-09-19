# Completion delivery recovery boundaries

The durable inbox preserves every distinct completion after `CommitToInbox`
returns successfully. Replays of the same turn are collapsed by the hashed turn
fingerprint; distinct turns for one child are retained through the in-flight WAL,
drain, and consumed-turn ledger.

This is not retroactive repair. A transition suppressed before durable append by
an older notifier build is permanently lost because no inbox, WAL, event log, or
dead-letter record exists. A distinct turn overwritten after commit by an older
last-wins build may have forensic notifier logs, but it is not automatically
recoverable and operators must not treat those logs as a replay queue.

Upstream Codex thread/turn generation text is hashed before it enters notifier
state or inbox records. The hook status file may retain the source integration's
own evidence, but the delivery boundary persists only its SHA-256-derived signal.
