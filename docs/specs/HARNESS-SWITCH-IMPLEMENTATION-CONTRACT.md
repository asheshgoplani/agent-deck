# Account/harness switch implementation contract

The switch operation is one shared backend used by CLI and TUI. A caller supplies a source instance and an explicit target harness/account; the backend performs a read-only preflight before any lifecycle or registry mutation.

* Source identity is immutable: instance ID, tool, exact conversation ID, account, project/cwd, title, and group are journaled. Missing or colliding IDs are refused; no newest-file guessing is allowed.
* Claude→Claude and Codex→Codex use native resume. The exact transcript/rollout (and Claude companion directory) is copied to a staging directory, byte/hash verified, then installed in the target account directory. The source is never removed and a conflicting destination is refused. The account is committed only after destination verification.
* Claude→Codex uses a staged, bounded transcript handoff prompt. It starts a fresh Codex session with `codex` resume semantics intentionally absent; source history, files, tool state, and nonportable settings are disclosed as losses. The source remains available for rollback until destination startup succeeds.
* Pi has no named-account semantics. Its current account is shown as default/unknown and every switch involving Pi is an explicit refusal; Pi is not treated as Hermes.
* Every operation holds an instance journal lock and records prepared/staged/installed/committed/completed (or failed) state. A completed journal is an idempotent success; an incomplete journal is recovered only when its immutable source identity matches.
* Destination readiness and identity are checked before success is reported. Failed destination startup restores the source instance fields and attempts source restart; no OAuth credential is copied or escalated.
* CLI and TUI surface the same preview/result, including configured-vs-verified authentication wording and context loss disclosures. No routing, child-parent, cwd, title, group, or settings mutation is implicit.

This is a local implementation contract, not evidence that the feature has been runtime-verified or is ready to install.
