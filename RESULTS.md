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

Pending.
