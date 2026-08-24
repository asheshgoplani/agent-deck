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
place. Rebase reconciliation added main's new `accounts` command to the registry
after exact-head CI exposed the omission; this restores the registry/dispatch
invariant without changing command behavior. No dispatch, parsing, or mutation
ordering changed in round 5.

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
- The first exact-head GitHub full-suite run at `07358c32` correctly failed only
  `TestCommandRegistryMatchesMainDispatch` because rebased main added `accounts`.
  All other checks, including `test`, `eval_smoke suite`, lint, vulnerability,
  snapshot, performance, and CodeRabbit, passed. The final tip includes the
  registry reconciliation and receives a completely fresh exact-head rollup.

# PR #2055 round-6 verification results

No code defect was found at the head, and no production behavior changed in
round 6. This section records the independent audit that the rebase dropped
nothing and that CI is green on the exact head.

## Rebase currency

`origin/main` is `bf50689893053c6dd33a29b21e12eb36e251d94b`.

```text
git merge-base --is-ancestor origin/main HEAD   -> ok
git rev-list --left-right --count origin/main...HEAD -> 0  11
```

The branch is zero commits behind main, so the eleven PR commits replay on
current main with no merge required.

## No round-2/3/4 fix was dropped

The pre-rebase tip `cc4009fc333b204c012b49674ca5537cd013c654` (based on
`47bb210373c87aa1a90f2a319acf9174ea4b3dae`) was diffed against its own base and
the resulting patch was reverse-applied against the rebased head. A clean
reverse-apply proves every pre-rebase hunk is still present verbatim:

```text
git diff 47bb2103..cc4009fc -- ':!RESULTS.md' ':!cmd/agent-deck/main.go' \
    ':!tests/eval/helpconsent/cli_test.go' | git apply --check -R   -> clean
```

Seventeen of the nineteen non-receipt files reverse-apply verbatim. The two
exceptions were inspected line by line and are both additive, not losses:

- `cmd/agent-deck/main.go` carries main's own post-rebase content (`Version`
  `1.15.0`, the `accounts` dispatch case, the `accounts` line in `printHelp`)
  plus this PR's `"accounts": true` registry entry. The registry entry is
  required by this PR's own `TestCommandRegistryMatchesMainDispatch`
  invariant; without it the invariant would be violated by main's new command.
- `tests/eval/helpconsent/cli_test.go` carries the round-5 tightening that
  requires hooks help to name its own `help` subcommand. That assertion is
  strictly stronger than the round-4 one it replaced.

File-set comparison confirms the same shape: the post-rebase diff is a superset
of the pre-rebase diff, adding only `cmd/agent-deck/hooks_help_test.go` and the
round-5 growth in `hook_handler.go` and `main.go`.

## Exact-head CI

All fourteen checks on `bdbe8c9a5a932ee2ea86c7bebb7182169a97f434` completed
successfully: `intake`, `diffscope`, `golangci`, `govulncheck`, `analyze`
(CodeQL), `CodeQL`, `test` (telegram-reliability), `Full test suite (PR gate)`,
`eval_smoke suite`, `reap-stale-tmux.sh DRY_RUN smoke`, `snapshot`,
`TestPerf_ walltime regressions (-race, multiplier=2.0)`,
`Benchmark ns/op trend (advisory)`, and `CodeRabbit`. `mergeable` is
`MERGEABLE`. `mergeStateStatus` is `BLOCKED` solely because `reviewDecision` is
`REVIEW_REQUIRED`; that is a human approval gate, not a CI or conflict failure.

## Independent container re-verification at the head

Run in `golang:1.25` with `-buildvcs=false`; nothing was tested on the host.

```text
go build ./...                                                          BUILD_OK
go vet ./...                                                            VET_OK
go test ./cmd/agent-deck -run 'TestCommandRegistryMatchesMainDispatch|
  TestEveryRegisteredCommandHelpIsReadOnly|TestIssue2025' -count=1      PASS (3.698s)
go test ./cmd/agent-deck -run 'TestEveryRegisteredCommandFamilyBareHelp
  IsReadOnly|TestHooks...|TestSiblingHooksInstallHelpDoesNotInstall'    PASS (0.844s)
go test -tags eval_smoke ./tests/eval/helpconsent/... -count=1          PASS (1.415s)
```

The working tree was clean before and after; the container used named cache
volumes so no build artifacts landed in the repository.

## CodeRabbit's remaining actionable comment is invalid

CodeRabbit asked for an `agents` entry in the family table of
`cmd/agent-deck/issue2025_help_consent_test.go`, described as an alias of
`agent` sharing its usage string. It is not an alias. `main.go:346` routes
`agents` to `handleAgents` (a read-only listing of adopted agents) while
`main.go:349` routes `agent` to `handleAgent` (the subcommand family).
`agents` owns no subcommand family and defines no command-position `help`
token. Executed against the built binary in a sandboxed `HOME`:

```text
$ agent-deck agents help
No agents adopted yet.
Run `agent-deck agent adopt <conductor-dir|session|plist|unit>` to make an existing setup visible.
exit=0

$ agent-deck agent help
Usage: agent-deck agent <command>
```

Adding `agents` to that table would assert `Usage: agent-deck agent <command>`
against the listing output and make the test RED. The table is deliberately
scoped to dispatchers that document a command-position `help`. `agents` is
already covered for `--help` and `-h` by
`TestEveryRegisteredCommandHelpIsReadOnly`, which enumerates the whole
registry. The comment is declined with that reason and no change was made.

## Scope note on the tmux mutation-deadline defect

The verification brief carried a `RespawnPane` finding scoped to PR #2052: an
unbounded `CombinedOutput()` executed while holding `s.mu`. On this branch's
head that mutex half does not exist. `RespawnPane` begins at
`internal/tmux/tmux.go:3427`; the external command runs at line 3476 and the
only `s.mu` critical section is lines 3506-3512, covering four field
assignments after the command has already returned. The remaining unbounded
execution at line 3476 is pre-existing on `main` and unrelated to this PR,
which touches only CLI help consent. Changing it here would break diff scope,
so it is recorded and left to PR #2052.
