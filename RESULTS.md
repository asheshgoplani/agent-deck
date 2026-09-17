# PR #2085 rebase — results

## What was done
- Cloned `asheshgoplani/agent-deck`, fetched `pull/2085/head` as `pr-2085`, branched `carry/2085`.
- Rebased `carry/2085` onto `origin/main` (4 commits carried, contributor authorship preserved).

## Conflicts resolved (kept both intents)
1. `cmd/agent-deck/main.go` — `commandRegistry` map. `main` had added `--version`/`-v`/`--help`/`-h` and `telemetry` entries; the PR added `completion` and `__complete`. Merged into one map containing all of them.
2. `skills/agent-deck/references/cli-reference.md` — both sides added a new doc section at the same insertion point (`main` added the "update - Check for and install a new release" section, the PR added "Shell Completion"). Kept both sections back to back, update first then Shell Completion, no content dropped.
3. `README.md` — auto-merged cleanly by git (no manual edit needed despite being flagged conflicting in the review).

No other files conflicted. `cmd/agent-deck/completion_cmd.go`, `completion_cmd_test.go`, and the `remote_cmd.go` changes from the PR applied without conflict.

## Verification
- `go build ./...` — clean, no output/errors.
- `go vet ./...` — clean, no output/errors.
- `golangci-lint run ./cmd/agent-deck/...` — skipped: host golangci-lint is v1, repo config requires v2 (`Error: you are using a configuration file for golangci-lint v2 with golangci-lint v1`). Not run.
- Docker (`golang:1.25`, `--network none`, serialized via `/tmp/agentdeck-docker.lock.d`): `go test ./cmd/agent-deck/... -run "Completion|Complete" -v`
  - All completion-related tests pass: `TestCompletionTree_Consistent`, `TestCompletionTopLevelNames_IncludesCoreCommands`, `TestCompletionRules_NoDuplicateKeys`, `TestCompletionRules_KnownCases`, `TestBashCompletionScript_WellFormed`, `TestZshCompletionScript_WellFormed`, `TestFishCompletionScript_WellFormed`, `TestCompletionScripts_ShellSyntaxIsValid` (bash passes; zsh/fish subtests skip — shells not installed in the base `golang:1.25` image, not a code defect), `TestCompletionScripts_CarryEverySubcommandList`, `TestHandleComplete_*` (Sessions/UnknownProfileIsSilent/Remotes/RemotesListsConfigured/RemoteSessionsUnknownRemoteIsSilent/RemoteSessionsMissingRemoteArgIsSilent/Agents/Groups), `TestPrintProfileCompletions_FiltersInternalNames`, `TestPrintCompletionHelp_WritesUsage`, `TestHandleCompletion_DispatchesEachShellAndHelp` (all subtests), `TestCompletionHelperProcess`, `TestHandleCompletion_NoArgsExitsNonZeroWithUsageOnStderr`, `TestHandleCompletion_UnknownShellExitsNonZeroWithMessage`.
  - Full package run: `ok github.com/asheshgoplani/agent-deck/cmd/agent-deck 45.752s`, no failures.

## State
- Branch `carry/2085` sits on top of current `origin/main` (through `7d2302fb chore(release): v1.16.10 (#2276)`), no longer marked CONFLICTING.
- Not pushed anywhere (per task's no-push, no-GitHub-writes rule). Local only, at `/tmp/exec-heldpr-2085/src`, branch `carry/2085`, HEAD `06f72a12`.
- No `go test` was run against the real agent-deck data directory or host tmux; all testing was Docker-only, per the repo's tmux-hygiene rules.
