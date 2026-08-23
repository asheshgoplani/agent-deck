# Issue #2045 results

## Reproduction

Reproduced on current `origin/main` at `47bb2103` in a `golang:1.25`
container. The regression test `TestIssue2045LaunchHelpOffersNamedAccountSlot`
failed because `agent-deck launch --help` had no `--account` option. The test
`TestIssue2045AccountsListsConfiguredSlots` failed because the unknown
`accounts` command fell through to TUI startup and exited with `Error: tmux not
found` instead of listing the two configured slots.

## Root cause

- `cmd/agent-deck/launch_cmd.go:124` had no account flag, and the instance
  construction path near `cmd/agent-deck/launch_cmd.go:441` therefore never
  copied a selected slot into `Instance.Account`. The existing start-time
  resolver already consumes that field, so the missing launch wiring was the
  gap.
- The top-level command switch in `cmd/agent-deck/main.go:334` had no
  `accounts` route. Unknown commands continue into TUI startup, which explains
  the unrelated tmux error seen in the reproduction.

## Fix

- Added `launch --account <slot>` and persist the trimmed value on the new
  instance before it is saved and started.
- Added a read-only `accounts [--json]` command. It lists profiles with a
  configured Claude `config_dir`, expands paths through the existing config
  resolver, and sorts by slot name for deterministic human and JSON output.
- Updated top-level help, README documentation, and the bundled agent-deck
  skill/reference.

## Proof

- PASS: `go test ./cmd/agent-deck -run "TestIssue2045" -count=1 -v`
- PASS: `go build ./... && go vet ./...`
- Both commands ran only in `golang:1.25` containers.
- The full `go test ./cmd/agent-deck -count=1` suite was also attempted in the
  stock Go container. It is not green there because tmux is absent; existing
  tmux-dependent tests fail with `Error: tmux not found`, and the two existing
  40 ms cold-start budgets also exceeded the shared-container timings. The
  issue-specific tests pass in that same environment.

## Pull request

https://github.com/asheshgoplani/agent-deck/pull/2053

# PR #2053 round-2 review results

## Finding: launch consumed the next flag as `--account`'s value

### Reproduction

On the pre-fix parent `4ccce635`, I applied only the new regression test and
ran it in `golang:1.25`. The `launch` table case failed with:

```text
launch accepted a flag-shaped account value; output:
    ✓ Launched session: agent-deck
```

This reproduces the review's exact `launch . --account --no-wait` scenario:
the command reported success instead of rejecting the missing account name.
The test also enumerates both session-creation commands with an `--account`
surface (`add` and `launch`) so the existing sibling guard cannot silently
diverge again.

### Root cause

`cmd/agent-deck/launch_cmd.go:167` proceeded to argument reordering and Go flag
parsing without calling the shared `checkFlagValueNotFlag` guard. Go's flag
parser therefore bound the registered `--no-wait` token as the string value of
`--account`; account resolution then treated that unknown slot as a fallback.
The sibling `add` command already invoked the guard on original argv at
`cmd/agent-deck/main.go:1445`.

### Fix

`cmd/agent-deck/launch_cmd.go:167` now invokes `checkFlagValueNotFlag` before
argument reordering, parsing, account fallback, state writes, or tmux launch.
The shared guard recognizes that the following token is another flag from the
same launch `FlagSet` and exits with an actionable error. The CLI reference now
documents the required explicit account name and the `--account=<name>` escape
hatch for an intentional dash-prefixed name.

### Test and evidence

- RED on `4ccce635`: `TestIssue2053AccountGuardCoversSessionCreationCommands/launch`
  reported that launch accepted the malformed argv and launched a session.
- GREEN on the fixed branch: both the `add` and `launch` enumeration cases
  reject the malformed argv, name the swallowed flag, and prove that neither
  state nor tmux side effects occurred.
- PASS: `go test ./cmd/agent-deck -run 'TestIssue2053|TestIssue1923|TestIssue1928' -count=1 -v`
- PASS: `go build ./... && go vet ./...`

All Go commands above ran only in a `golang:1.25` container.

# PR #2053 round-3 verification results

## Blocking finding: launch account persistence had no effective test

### Reproduction

The verifier selectively removed the assignment at
`cmd/agent-deck/launch_cmd.go:452-454` while retaining flag registration and
parsing. The old help-only test remained green, proving it did not cover the
user-visible persistence behavior.

### Root cause

`cmd/agent-deck/issue2045_accounts_cli_test.go:12` exercised only `launch
--help`; it never entered the creation path or inspected the saved
`Instance.Account`. The production assignment itself is at
`cmd/agent-deck/launch_cmd.go:452-454`.

### Fix

`TestIssue2045LaunchPersistsNamedAccountSlot` now configures the `work` Claude
account, drives the real `launch` command through creation, start, and save with
an isolated fake tmux executable, opens the isolated profile storage, and
asserts that exactly one saved instance has `Account == "work"`.

The test also contains the requested focused `RemoteSession` skip marker.
`launch` creates a local session and has no remote target argument or
RemoteSession dispatch branch, so remote runtime behavior is not applicable.

### Revert-proof

All commands below ran in `golang:1.25` containers with
`GOFLAGS=-buildvcs=false` for the bind-mounted linked worktree.

- GREEN with the production assignment present:
  `TestIssue2045LaunchPersistsNamedAccountSlot` exited 0.
- RED after replacing only `launch_cmd.go:452-454` with `_ = account`:
  `issue2045_accounts_cli_test.go:55: persisted launch account = "", want work`.
- The assignment was then restored before the final verification.
- Final combined container command passed:
  `go test ./cmd/agent-deck -run "TestIssue2045|TestIssue2053" -count=1 -v && go build ./... && go vet ./...`.

### Preserved invariant

The change is test-only and does not alter launch ordering, persistence, or
concurrency behavior. The test reaches the existing targeted
`InsertSessionAndVerify` path, so it verifies account persistence without
replacing the bounded/concurrency-safe launch save mechanism. The existing
missing-value test continues to prove fail-closed ordering: malformed
`--account --no-wait` input is rejected before state or tmux side effects.

# PR #2052 round-5 fix results

## Decision and fix

The round-4 blocker was valid. `RespawnPane` serialized pane replacement with
the startup-timeout generation claim, but its `respawn-pane` client had no
deadline while `s.mu` was held. The mutation now uses
`context.WithTimeout(..., tmuxMutationTimeout)`, `s.tmuxCmdContext`, and
`annotateDeadline`, matching the established bounded mutation path. Failure
unlocks without publishing a new generation; success still publishes the
fresh startup clock and clears timeout state before unlock.

`TestIssue1892_RespawnPaneMutationIsBounded` is the permanent deterministic
probe. Its fake tmux answers setup probes immediately and sleeps only for
`respawn-pane`; with a 50 ms mutation deadline, the call must return an error
within the 500 ms test ceiling and leave `s.mu` acquirable.

## Rebase and ancestry

- Reviewed round-4 head: `47124312bd533c935d0fd85edb8f29a61c7c29af`.
- Rebased onto current `github/main` `bf506898` before diagnosis.
- The fixed history has `0` main-only commits, preserving all prior issue-1892
  generation, banner, hold-idempotence, return-revalidation, and manual-restart
  fixes.

## Mutation and gate receipts

- Predecessor-red synthetic commit `4b70204f50ed6cc2e5bf9c221a63ebf57528ebc2`
  contains the permanent probe on the rebased predecessor production code.
  Build-service gate `20260823T185717-fix_2052_r5_predecessor-4b70204f-container-targeted`
  failed the probe after 1.01 s because the unbounded respawn succeeded.
- Fixed production commit `c157089c25c84db6f09f9845b6f102af53e99409`
  no longer reports that probe among failures in the same package gate; the
  package gate remains environment-red only because the build-service image
  lacks the real `tmux` binary required by pre-existing integration tests.
- Build-service build/vet receipt for the fixed production commit:
  `c157089c25c84db6f09f9845b6f102af53e99409/build`, green on g14.

The final result JSON records the final documentation commit, exact-head
build/test receipts, pushed branch ancestry, and GitHub CI state.
