# PR #1952 verification results

## Rebase evidence

- Pre-rebase head: `ce4debeb6f3ea5b1cffdc9242eb598df4a3dede5`.
- Rebased head before the final fixes: `70bb777fd94106162f81a208764f42d4cfce84bc` on current `origin/main`.
- `git range-diff 92bb498f..ce4debeb origin/main..70bb777f` mapped all 16 PR commits one-for-one with `=`; no prior patch changed or disappeared.
- The rebased branch was pushed with `--force-with-lease` before findings work began.

## Findings addressed

- Made `SourceRemote` part of every pending-inbox identity decision: event fingerprint, turn fingerprint, last-wins producer replacement, and consumer collapse. This keeps local `boxb:nightly-build`, remote `nightly-build`, and caller-prefixed remote IDs distinct even when their visible child spelling overlaps.
- Removed prefix inference from `RemoteScopedChildID`; arbitrary caller-selected IDs are always scoped rather than mistaken for an already-scoped record.
- Converted injected CLI writers to error-tracking writers so `inbox` and `remote drain` cannot return success after partial/failed output.
- Made writer-status distinguish a missing heartbeat from permission/I/O/read failures; only `ENOENT` means “never stamped,” while other failures report unknown liveness.
- Fixed the suppressed-session absence test to fail on `ReadInboxEvents` errors instead of passing vacuously.
- Rechecked earlier findings on orphan export, suppression, completion-copy deduplication, corrupt ledger reads, recurring terminal turns, fetch/probe ordering, consumed-ledger bounds, and writer probe fail-closed behavior; their current-head fixes remain present after rebase.

## Revert proofs

Only the production hunks were reverse-applied while the new tests remained, and the focused tests were run in `golang:1.25`:

```text
RED_EXIT=1
TestIssue1952_OriginSeparatesEveryIdentityRule:
  local and remote records share EventFingerprint
TestIssue1952_OutputFailuresAreNotSuccess:
  remote drain output failure reported success
```

The production patch was then restored. With the fix present, these tests plus `TestIssue1952_WriterStatusReadFailureIsUnknown` pass.

## Container verification

- `go build ./...`: PASS in `golang:1.25`.
- `go vet ./...`: PASS in `golang:1.25`.
- Focused regression tests across `./internal/session` and `./cmd/agent-deck`: PASS.
- A raw `go test ./...` in the stock Go image reaches unrelated environment-dependent tests but lacks CI's tmux/zoxide packages and non-root permission behavior. The authoritative full race suite is the repository's GitHub Actions PR gate, which installs those dependencies.

## Invariant check

- Bounds: existing summary, inbox-line, retry, generation, and stale-record bounds are unchanged.
- Ordering: last-wins still preserves first-seen identity order; the identity is now `(SourceRemote, ChildSessionID)`.
- Idempotence: repeated drains of one remote retain the same structured origin and fingerprint; separate origins no longer destroy one another.
- Fail closed: fetch, writer probe, unreadable heartbeat, unreadable export, target resolution, and output failures all return non-success rather than an empty/successful drain.
- Sibling parity: both inbox producer replacement and consumer collapse use the same origin-aware key; both `EventFingerprint` and `TurnFingerprint` enumerate the same provenance field; both CLI entry paths track writer errors.

## CI state

- Verified head `14122746b119b6024209c4ef750c45ced5d56fac`: all 12 reported checks completed successfully.
- The required `Full test suite (PR gate)` completed in 6m30s, including the repository's full `-race` suite with CI's tmux/zoxide environment.
- Performance walltime and benchmark checks, CodeQL, govulncheck, golangci-lint, release snapshot drift, Homebrew verification, diff-scope, intake, and CodeRabbit all completed successfully.
- This results-only commit is the final branch mutation; its exact-head CI conclusions were checked after push.
