# PR #1961 final verification

## Rebase evidence

- Pre-rebase head: `f0f507eb641b6af51fdfa2ac82952164107eed11`.
- Fetched `origin/main` from `47bb2103` to `7771aca6`, then rebased before making any finding fixes.
- Resolved the only conflict in `internal/session/instance.go` by preserving both upstream DeepSeek fast-exit/prompt guards and the PR's shared ownership-generation watcher.
- `git range-diff 47bb2103..f0f507eb origin/main..384eeaf7` preserved all four PR commits. The config-lock file removed from the second patch was already present on the new main.
- Rebased head `384eeaf7` was force-pushed with lease before review work.

## Findings addressed

- Darwin `ps` cancellation/timeouts now remain unreadable and fail closed; only a genuinely exited `ps` with empty output means the PID is gone.
- Descendant attribution revalidates the exact leader observation used as its walk root (PID, start identity, and UID).
- `Claim` validates the completed receipt, rejecting missing instance IDs or start identities immediately.
- Restart receipts record the prepared command actually launched. DeepSeek task validation occurs before spawn stamps, generation changes, or pane termination.
- Restart generation replacement carries forward every still-owned prior identity, preserving escaped descendants rather than silently dropping ownership.
- Receipt `Clear` preserves corrupt evidence; only explicit `ForceClear`/abandon discards it.
- Store construction requires an explicit cross-process lock choice.
- Config-file/receipt locking now has a bounded 30-second wait for both the in-process mutex and host flock.
- Ownership CLI treats omitted IDs as usage errors, storage failures as invalid operations, reports post-abandon state, and avoids verb-free `Fprintf` calls.
- The receipt-race child handshake preserves partial reads and has an effective deadline.
- Fake-prober fields are mutex-protected; Linux attribution asserts returned members; nil-receipt reap is covered; escaped-tree PPID checks the actual pane; remote ownership is explicitly documented as host-local via a skipped test.

## Revert proofs

All commands ran in `golang:1.25` containers. Each production guard was restored immediately after the failing run.

- Removed the second-observation leader identity comparison: `TestAttribute_RefusesLeaderReusedBetweenVerificationAndWalk` failed because attribution returned nil error and would accept the stranger's tree.
- Removed final receipt validation from `Claim`: `TestClaim_RefusesWhatItCannotProve` failed because an empty instance ID produced no error.
- Restored corrupt-receipt deletion in ordinary `Clear`: `TestStore_ClearPreservesCorruptReceiptForExplicitAbandon` failed because clear returned nil and deleted the evidence.

## Local verification

- PASS: `go build ./... && go vet ./...` in `golang:1.25` after marking `/src` as a Git safe directory for VCS stamping.
- PASS: full `internal/procowner` tests.
- PASS: targeted ownership, issue-1873, config-lock, DeepSeek, and CLI ownership tests.
- The stock container's full affected-package attempt additionally exposed only known environment failures: no `tmux`, root bypassing a read-only-directory assertion, and nested test builds rejecting the bind-mounted Git ownership. These are unrelated and are exercised by repository CI's prepared environment.

## Invariant check

- Bounds: receipt `MaxMembers` remains enforced while carrying identities across restart.
- Ordering: task validation and ownership admission precede all state mutation; identity is rechecked immediately before descendant traversal and again before every signal.
- Idempotence/CAS: duplicate members are key-deduplicated; generation and leader checks remain under one cross-process lock; attribution cancellation/barrier behavior is unchanged.
- Fail closed: unreadable probes, lock timeouts, corrupt receipts, and identity changes never authorize a signal or a replacement spawn.
- Sibling parity: `Start`, `StartWithMessage`, restart fallback, and every respawn-pane branch now feed the actual launched command into ownership; DeepSeek validation covers all three spawn families.

## CI state

- Exact verified code head: `0f3cddbce629ae72cb6cafeda7d61b7af87a7ab7`.
- GREEN: full PR gate (7m18s), race persistence suite, persistence script, macOS and Ubuntu harnesses, golangci-lint, CodeQL analysis, govulncheck, eval smoke, stale-tmux smoke, diff-scope, snapshot, intake, and CodeRabbit.
- The first post-fix CI cycle found one unused upstream watcher wrapper left by the rebase conflict. The signed follow-up `0f3cddbc` reused that wrapper with the PR's shared generation/wake arguments, preserving watcher lifecycle tracking; the complete exact-head rerun above passed.
