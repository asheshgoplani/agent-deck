# PR #2055 round-5 fix results

## Decision and reconciliation

The round-4 worker tip was inspected rather than accepted blindly. Its local tip
`cc4009fc333b204c012b49674ca5537cd013c654` added command-family enumeration and
eval-smoke coverage on top of the reviewed `287e14db9e6f3649e54d5e9921395acfb6bdd575`,
but did not address the authoritative round-4 finding: hooks usage still omitted
the accepted `help` subcommand. Those useful tests were preserved. The branch was
rebased onto current `origin/main` `bf50689893053c6dd33a29b21e12eb36e251d94b`
before the round-5 edit.

## Fix

`agent-deck hooks help` now advertises
`<help|install|uninstall|status>` and lists `help` in its Commands section. The
focused unit and real-binary eval assertions require both strings while retaining
the existing successful dispatch and complete HOME-tree no-write checks.

## Predecessor RED and mutation proof

The assertion-only predecessor `c6b1db71ee4ec54eb1e924f2e000b548a989588d`
was run through `overnight/build-service.sh`. It was RED and reported
`TestHooksBareHelpPrintsUsageWithoutSideEffects`: dispatch exited successfully
and printed hooks usage, but that usage was still
`<install|uninstall|status>` and had no `help` command. Receipt:

- build-service result: `overnight/builds/c6b1db71ee4ec54eb1e924f2e000b548a989588d/test-936568f6`
- gate summary: `overnight/metrics/gates/20260823T185822-fix_2055_r5_predecessor-c6b1db71-container-targeted/summary.json`

This is the requested production mutation proof: removing only the usage fix
recreates that predecessor while leaving the successful, read-only `help`
dispatch intact, and the focused assertion becomes RED.

## Preserved guarantees

The complete command-family enumeration, detailed nested help, positional data
named `help`, and complete-tree no-write assertions from rounds 1-4 remain in
place. No dispatch, parsing, or mutation ordering changed in round 5.

## Fixed-tip gates and ancestry

- `overnight/build-service.sh` build/vet was GREEN at production tip
  `81f586cb5513e323e47c365741b428177e1c7ef6`; receipt
  `overnight/builds/81f586cb5513e323e47c365741b428177e1c7ef6/build`.
- The final committed tip is required to receive a fresh build-service build/vet
  receipt and exact-head GitHub CI after push; those receipts are recorded in
  the external `RESULT.json` so its own commit does not make the recorded SHA
  stale.
- `git merge-base --is-ancestor origin/main HEAD` passed after rebase, and
  `git rev-list --left-right --count origin/main...HEAD` reported `0 8` before
  the two round-5 test/eval commits and this evidence commit.

