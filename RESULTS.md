# Issue #2025 results

## Reproduction

Reproduced on current `main` at `47bb2103` in `golang:1.25`, using executable-level subprocesses with isolated `HOME` and XDG directories.

The new `TestIssue2025TrailingHelpIsReadOnly` failed before the fix for all four reported commands:

- `deepseek sessions help` resolved `help` as `/src/cmd/agent-deck/help` and printed a sessions result instead of usage.
- `remote add test example.invalid help` persisted the remote and attempted SSH installation.
- `notify-daemon help` did not return within two seconds and entered the daemon path.
- `creds-refresh --config-dir <fixture> help` did not return within two seconds and entered the credential refresh loop.

## Root cause

Help recognition was decentralized and inconsistent. The hook family had a shared predicate, but the other dispatchers interpreted arguments before applying the same consent boundary:

- `cmd/agent-deck/deepseek_cmd.go:27` treated trailing bare `help` as a workspace positional.
- `cmd/agent-deck/remote_cmd.go:18` dispatched `add` before recognizing trailing help, reaching config persistence and SSH setup.
- `cmd/agent-deck/notify_daemon_cmd.go:23` let Go flag parsing leave bare `help` positional, then initialized logging and ran the daemon.
- `cmd/agent-deck/creds_refresh_cmd.go:59` likewise parsed around bare `help`, resolved credentials, and ran the refresher.
- The top-level command list was duplicated manually and had already drifted from the dispatch switch (`fleet`, `agents`, `agent`, and flag aliases were absent), so it could not safely drive exhaustive coverage.

## Fix

- Promoted the hooks-only predicate to the shared `helpRequested` helper and applied it before parsing or dispatch in all four reported handlers.
- Added explicit help usage for `creds-refresh`.
- Converted the top-level list into `commandRegistry`, corrected its existing drift, and added an AST registry-vs-switch agreement test.
- Added an executable enumeration test that probes `--help` and `-h` for every human-facing registered command in a sandboxed home. Protocol/version entry points that intentionally have no nested help surface are explicit exclusions. Bare `help` remains a legitimate data value on commands such as `launch`, so focused tests pin it only on dispatchers that define it as help.
- The enumeration test also exposed and fixed unsafe flag-help handling in `mcp-proxy` and `debug-dump`, plus inconsistent help exits in `costs` and `inbox`.
- Updated the DeepSeek documentation and the bundled `skills/agent-deck` CLI reference.

## Proof

Passing targeted container test:

```text
go test ./cmd/agent-deck -run '^(TestCommandRegistryMatchesMainDispatch|TestEveryRegisteredCommandHelpIsReadOnly|TestIssue2025TrailingHelpIsReadOnly)$' -count=1
ok github.com/asheshgoplani/agent-deck/cmd/agent-deck
```

The four focused subprocess cases now return usage in under two seconds without creating config or daemon log files. The registry sweep checks every declared human-facing command in a fresh home and the AST test fails if a future switch case is not registered.

Required repository proof passed in a Go 1.25 container (run as the checkout owner so Git VCS stamping accepts the bind mount):

```text
docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -e GOCACHE=/tmp/go-build -e GOPATH=/tmp/go -v "$PWD:/src" -w /src golang:1.25 sh -c "go build ./... && go vet ./..."
```

The broader `cmd/agent-deck` suite was also attempted. Its issue-focused tests passed, while unrelated integration/performance tests require host facilities absent from the stock Go container (`tmux`, expected debug logging, and the host cold-start performance budget).

## Pull request

https://github.com/asheshgoplani/agent-deck/pull/2055

The PR includes `Closes #2025`, credits reporter @asheshgoplani, and remains unmerged.

## Round 2 reviewer finding: nested remote help

### Reproduction

After confirming the branch was already up to date with `origin/main`, added `TestRemoteSubcommandHelpRoutesToSelectedCommand`. Before the fix, all 27 cases failed: `--help`, `-h`, and `help` for every remote subcommand and alias (`add`, `remove`/`rm`, `list`/`ls`, `sessions`, `attach`, `rename`, and `update`) printed only `Usage: agent-deck remote <command> [options]` instead of the selected command's usage.

### Root cause

`cmd/agent-deck/remote_cmd.go:23` recognized help across the entire nested argument list but returned through the generic parent usage function before the switch at line 28 could select a subcommand. That made the help request read-only, but discarded the selected command and its required arguments/options.

### Fix

`cmd/agent-deck/remote_cmd.go:23` now routes help through `printRemoteSubcommandUsage(args[0])`. The dispatcher at line 50 covers the complete switch family and both aliases, prints each selected command's invocation, and documents the flags for `add`, `list`, and `sessions`. Unknown selections retain generic remote help. Routing still occurs before any config or SSH work.

### Test and evidence

`TestRemoteSubcommandHelpRoutesToSelectedCommand` is an executable-level enumeration test. Each case runs with isolated HOME/XDG directories, requires exit status 0, asserts the exact selected usage line, and asserts no files were created. It failed before the production change and passes after it together with the original issue tests:

```text
go test ./cmd/agent-deck -run '^(TestRemoteSubcommandHelpRoutesToSelectedCommand|TestIssue2025TrailingHelpIsReadOnly|TestEveryRegisteredCommandHelpIsReadOnly|TestCommandRegistryMatchesMainDispatch)$' -count=1
ok github.com/asheshgoplani/agent-deck/cmd/agent-deck 11.541s
```

The bundled CLI reference already lists every remote invocation and option at `skills/agent-deck/references/cli-reference.md:597-660`; the corrected runtime help now agrees with it.

Required repository proof also passed in `golang:1.25`:

```text
go build ./... && go vet ./...
exit 0
```
