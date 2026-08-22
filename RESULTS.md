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
