# PR #2043 verification results

## Rebase evidence (second rebase, onto v1.15.0 main)

- Pre-rebase head: `e34144d6b7a8880cedba151567f259419ad2fc8f` (the head cold verification passed on).
- Rebased onto fetched `origin/main` at `01c011b5` (v1.15.0 changelog/version bump plus the #1952 remote-drain merge).
- `git range-diff 7771aca6..e34144d6 01c011b5..HEAD` mapped all three functional commits one-for-one with `=`; only the docs commit differed, and only in RESULTS.md.
- Single conflict: RESULTS.md (main carries PR #1952's record; this file is a per-PR record, so the PR's version was kept unchanged — `git diff` against the pre-rebase copy is empty).
- `git diff 7771aca6..e34144d6 -- ':!RESULTS.md'` and `git diff 01c011b5..HEAD -- ':!RESULTS.md'` produce the identical stable patch-id `596b03dd`, proving the full functional content — including the wait/stream turn-identity work and every earlier review fix — survived the rebase byte-for-byte.
- CHANGELOG.md and cmd/agent-deck/main.go took main's v1.15.0 state untouched; the PR never modified either relative to its merge-base.

## Rebase evidence (first rebase)

- Pre-rebase head: `be59b0b3ddd0b5571afc55d318346a3b65bb4a32`.
- Rebased onto fetched `origin/main` at `7771aca6c5a40d775fa96ecf15cd9df16ec23af6`.
- Initial post-rebase head: `b291fad1f134cf402b86d31674fe82f54fa28af1`.
- `git range-diff 47bb2103...be59b0b3 7771aca6...b291fad1` reported both PR commits patch-equivalent (`=`), confirming neither earlier fix was dropped.
- The rebased branch was force-pushed with lease before review remediation began.

## Findings addressed

- Read the complete PR conversation and all five inline review comments through `gh pr view 2043 --comments` and the paginated pull-review-comments API.
- Made hook-busy delivery classification independent of retry thresholds and full-resend budgets. This covers `--no-wait`, whose Ctrl+C/full-resend budget intentionally remains disabled.
- Kept `--wait` working for non-Claude tools through its prior best-effort adapter instead of accepting the proposed breaking Claude-only restriction. Claude `--wait` and `--stream` use durable turn identity.
- Resolve and validate the Claude transcript path before transport submission, capture a non-guessed cursor, and remove post-send cursor-zero recovery.
- Record own-pane session-ID provenance and detection time when adopting a fresh tmux session ID.
- Preserve partial trailing JSONL records between polls; the identity cursor advances only for newline-terminated records.
- Added an explicit `RemoteSession` skip: `session send` accepts local `Instance` targets; remote delivery runs this local command over SSH on the owning host, while `RemoteSessionInfo` is only a TUI cache row.

## Revert proofs

All commands ran in `golang:1.25`; no host `go test` was used.

- Reverted only the remediation production hunks while retaining their tests.
- `TestAwaitTurnIdentity_RetainsPartialTrailingRecord` failed with `turn identity not established within 1s`.
- `TestNoWaitClassifiesHookBusyAsQueued` failed because the no-wait send exhausted all 30 checks with `no evidence of delivery` instead of returning `queued`.
- Combined red-proof exit: `1`.
- Restored the exact production patch and reran both tests: both packages passed.
- The earlier turn-correlation suite also passes: interleaved sends return only their own nonce, streaming excludes the preceding turn, missing UUIDs fail closed, busy suppresses interrupts, idle preserves recovery, unknown hooks preserve pane fallback, and nil probes preserve prior behavior.

## Container verification

- Targeted command: `go test ./internal/session ./cmd/agent-deck -run "Test(TurnIdentity|AwaitTurnIdentity|StreamTranscriptForTurn|Interrupt|NoWaitClassifiesHookBusy)" -count=1` — PASS.
- Required `go build ./...` — PASS.
- Required `go vet ./...` — PASS.
- A broader local `go test ./...` was also attempted. Its relevant packages/tests passed, but the minimal Go image lacks `tmux`, nested test builds cannot VCS-stamp the bind-mounted repository, and three unrelated filesystem/time-sensitive tests failed. The authoritative full-suite result is the repository CI environment recorded below.

## Invariant check

- Bounds: transcript polling retains the existing 8 MiB record limit for response scanning and remains timeout-bounded; identity scanning consumes only complete newline-delimited records.
- Ordering/identity: cursor capture occurs before send; consumers start after the exact user record and refuse to cross a later user turn.
- Idempotence: hook-busy and `--no-wait` paths issue exactly one `SendKeysAndEnter` and zero interrupts/resends.
- Fail closed: missing path, missing UUID, changed stream transcript, and absent `end_turn` all error rather than returning guessed output.
- Sibling parity: verified default, `--no-wait`, `--wait`, and `--stream`; preserved non-Claude `--wait`; documented the remote execution boundary with an explicit skip/enumeration test.

## CI state

The final commit is pushed before CI observation. PR #2043's required checks must complete green on that exact remote `headRefOid`; the final handoff reports the observed SHA and check conclusions without creating a post-CI commit.
