# Round 3 verification results — PR #2057

Rebased `pr-2057-round3` on current `origin/main`; it was already current.
All Go commands below ran in `golang:1.25` containers. No host Go command was
used.

## Finding 1 — distinct-turn outbox lost its producer bound

- Reproduction: added
  `TestIssue2057_DistinctTurnQueueIsBoundedAndOverflowObservable`. Before the
  production fix the test was red because the bound and observable overflow
  contract did not exist (`undefined: maxPendingTurnsPerChild` and
  `undefined: ErrInboxTurnOverflow`).
- Root cause: `internal/session/inbox_outbox.go:157-178` previously removed
  only an identical turn and appended every distinct turn, so one child could
  grow a non-draining parent's inbox without limit.
- Fix: `internal/session/inbox_outbox.go:47-57,157-217` caps pending distinct
  turns at 64 per child. The 65th distinct turn returns the inspectable sentinel
  `ErrInboxTurnOverflow` and leaves the durable inbox unchanged. The cap is
  checked while both the process mutex and cross-process file lock are held.
- Red-without-fix proof: changed only the enforcement condition to false and
  ran
  `go test ./internal/session -run '^TestIssue2057_DistinctTurnQueueIsBoundedAndOverflowObservable$'`.
  It failed with `overflow error = <nil>, want ErrInboxTurnOverflow` (exit 1).
  The production condition was then restored.
- Preserved invariants: the test fills all 64 slots with distinct turns,
  verifies none is evicted, verifies the next distinct turn is explicitly
  rejected, and verifies retrying an existing fingerprint at capacity succeeds
  idempotently without changing the queue size. Existing two-distinct-turn and
  durable-drain tests also remain green.

## Finding 2 — fallback ambiguity test had no positive control

- Reproduction: the original negative-only assertions could pass when Codex
  signaling always returned empty, exactly as the cold review observed.
- Root cause: `internal/session/issue2057_identity_matrix_test.go:110-139`
  asserted only empty outputs for invalid state, so total absence of the
  implementation was indistinguishable from correct fail-closed behavior.
- Fix: added a valid completion-scoped sequence (`started == completed == 8`)
  and require a nonempty turn signal before exercising generic-noise,
  mismatched-sequence, and partial-JSON negative cases.
- Red-without-fix proof: replaced the complete `codexTurnSignal` behavior with
  an empty return and ran
  `go test ./internal/session -run '^TestIssue2057_FallbackAmbiguityFailsClosed$'`.
  It failed with `valid completion-scoped fallback produced no signal` (exit
  1). The production implementation was then restored.
- Preserved invariant: valid completion-scoped fallback is recognized while
  ambiguous/noisy/corrupt hook state still fails closed.

## Final verification

- Targeted issue suite: PASS.
- `go build ./... && go vet ./...`: PASS.
- Full `go test ./internal/session`: reached the suite and failed only in the
  pre-existing root-container-sensitive
  `TestWriteJSONFileAtomic_SkipsUnchangedWrite`: root can write through the
  test's read-only-directory permission setup, so the expected write failure
  was nil. This is the same unrelated baseline failure documented by the cold
  review. All PR-specific targeted tests pass.
