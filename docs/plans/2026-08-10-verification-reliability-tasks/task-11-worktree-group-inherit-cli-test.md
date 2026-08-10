# Task 11 — Linked-worktree group inheritance subprocess test

**tier: mid**
**Parallelism:** independent; production implementation remains unchanged.

## Approved design extract (verbatim)

> The current inheritance implementation remains unchanged. Add a subprocess regression test using a real linked worktree, a scrubbed environment, automatic parenting, and a nested parent group. Any compatible pre-existing local test may be incorporated, but unrelated user work must not be overwritten.

## Change

Create `cmd/agent-deck/worktree_group_inheritance_test.go` in package `main`. Skip on `testing.Short()`, missing `git`, or missing `tmux`. Reuse `channelsCLIBinary(t)` and issue-1031 isolated tmux helpers, including strict teardown.

Create temp HOME and Git repo with an initial commit, then `git worktree add .worktrees/feature-x -b feature-x`. Build one explicit environment helper removing every `TMUX*` and `AGENTDECK_*`, setting HOME/XDG roots, `AGENTDECK_PROFILE=worktree_group_inheritance_test`, `TERM=dumb`, and allowing extra variables. Use it for all subprocesses. Seed with `list --json`; `add -t parent -g acme/backend -c shell <main-repo>`; parse parent ID. From the linked worktree run `launch --tmux-socket <isolated> -t child --json <linked-path>` with `AGENTDECK_INSTANCE_ID=<parent-id>` and no group/parent flags. Assert child `group_path == "acme/backend"` and `parent_session_id == parent ID`. Use `exec.Cmd.Env`, not `runAgentDeck`. No production edits.

## Acceptance criteria

- Real linked worktree inherits nested group through automatic parenting.
- Environment is scrubbed/consistent and no tmux server survives.
- Production diff is empty.

## Verification

```sh
go test ./cmd/agent-deck -run '^TestLaunchLinkedWorktreeInheritsAutomaticParentNestedGroup$' -count=1 -v
```

Expected after red/green: `PASS` (or explicit prerequisite skip), exit 0.

## Interfaces

consumes:
- `channelsCLIBinary(*testing.T) string`
- isolated tmux helpers in `cmd/agent-deck/issue1031_launch_race_test.go`
- CLI `add`, `list --json`, `launch`; env `AGENTDECK_INSTANCE_ID`

produces:
- `cmd/agent-deck/worktree_group_inheritance_test.go` regression test only

## Record (append-only)
