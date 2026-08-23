# Round-3 Results — PR #2055

Rebased `fix/2025-help-consent` on current `origin/main` before work (`3 0`; no
new main commits to replay). All verification and mutation runs below used
`golang:1.25` containers; no Go command ran on the host.

## Finding 1 — bare `help` consumed as data

- Reproduction: the shared `helpRequested` helper returned true for
  `deepseek sessions help`, remote/session arguments named `help`, and
  `inbox help`, so callers returned usage before interpreting the value.
- Root cause: `cmd/agent-deck/cli_utils.go:178` classified bare `help` globally
  instead of limiting it to a dispatcher's command position.
- Fix: `helpRequested` now recognizes only unambiguous `--help` and `-h` flags.
  DeepSeek and hook dispatchers retain their existing explicit `case "help"`
  command handling, and `remote help` is now explicit in the remote command
  switch. Documentation now states the same positional rule.
- Revert proof: after temporarily restoring `arg == "help"` in the shared
  helper, `TestBareHelpRemainsADataValue` was RED with
  `bare help in a value position was classified as a help flag: [sessions help]`.
  The restored test also executes `deepseek sessions help --json` and proves it
  returns a workspace report rather than usage.
- Preserved invariant: unambiguous help flags remain read-only at any argument
  position. `TestEveryRegisteredCommandHelpIsReadOnly`,
  `TestIssue2025TrailingHelpIsReadOnly`, and the remote nested-help routing test
  all pass for `--help`/`-h`; explicit bare help still works in actual command
  positions.

## Finding 2 — nested costs help intercepted by its parent

- Reproduction: `agent-deck costs recompute --help` printed only
  `Usage: agent-deck costs <sync|summary|recompute>`.
- Root cause: `cmd/agent-deck/costs_cmd.go:17` searched all arguments for help
  before dispatching the selected costs subcommand.
- Fix: parent costs help is now recognized only at `args[0]`, allowing
  `recompute --help` to reach its detailed handler.
- Revert proof: after temporarily restoring `if helpRequested(args)`,
  `TestCostsRecomputeHelpIsDetailed` was RED: the output lacked
  `Usage: agent-deck costs recompute [--dry-run]` and showed only parent usage.
- Preserved invariant: parent `costs help`, `costs --help`, and `costs -h` remain
  read-only, while detailed recompute help exposes `--dry-run`, unknown-model
  behavior, and idempotence without opening storage.

## Finding 3 — incomplete filesystem read-only assertion

- Reproduction: the prior test checked only two absent hard-coded paths and did
  not compare its existing credential fixture or any other file under the test
  HOME/XDG roots.
- Root cause: `cmd/agent-deck/issue2025_help_consent_test.go:139` had no complete
  before/after fixture snapshot.
- Fix: each case snapshots the entire temporary HOME (which contains all four
  configured XDG roots) before and after execution, including entry type,
  permissions, size, nanosecond mtime, regular-file contents, and symlink target.
- Revert proof: with a temporary production mutation that rewrote
  `creds/.credentials.json` during DeepSeek help, the focused test was RED and
  reported both snapshots: original `size=2 contents={}` versus mutated
  `size=7 contents=changed`.
- Preserved invariant: help remains prompt and read-only; the existing timeout
  and usage assertions remain, while the stronger snapshot covers creation,
  deletion, metadata/content modification, and symlink retargeting.

## Finding 4 — remote help used single-dash long options

- Reproduction: nested remote help printed `-agent-deck-path`, `-profile`, and
  `-json`, inconsistent with the public examples and CLI convention.
- Root cause: hard-coded strings in `cmd/agent-deck/remote_cmd.go:57` used the
  spelling emitted by Go's default flag formatter rather than the documented
  public spelling.
- Fix: generated nested help now prints `--agent-deck-path`, `--profile`, and
  `--json`.
- Revert proof: after temporarily reverting the add-help strings,
  `TestRemoteHelpUsesDocumentedLongFlags` was RED with
  `missing "--agent-deck-path"`; captured help showed `-agent-deck-path` and
  `-profile`.
- Preserved invariant: nested help remains routed to the selected command.
  Independently reverting that routing made
  `TestRemoteSubcommandHelpRoutesToSelectedCommand` RED for every `--help` and
  `-h` case because generic remote usage was printed.

## Final verification

The following completed successfully in a clean Go 1.25 container:

```text
go build ./...
go vet ./...
go test ./cmd/agent-deck -run '^(TestCommandRegistryMatchesMainDispatch|TestEveryRegisteredCommandHelpIsReadOnly|TestIssue2025TrailingHelpIsReadOnly|TestBareHelpRemainsADataValue|TestCostsRecomputeHelpIsDetailed|TestRemoteHelpUsesDocumentedLongFlags|TestRemoteSubcommandHelpRoutesToSelectedCommand)$' -count=1
ok github.com/asheshgoplani/agent-deck/cmd/agent-deck 5.160s
```
