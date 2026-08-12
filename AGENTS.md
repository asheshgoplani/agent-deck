# Agent guidance

## Agent Deck-managed Codex homes

Treat `~/.agent-deck/codex/*/config.toml` as generated runtime state. Do not
edit these files directly. For Agent Deck configuration changes, identify and
modify the source configuration or supported Agent Deck command first, then
verify the generated group homes after reconciliation.

## Test temporary files

Agent Deck's package-level `TestMain` cleanup must make a normal bare `go test`
remove every Agent Deck-owned temporary home, tmux socket, test binary, and
nested Go-tool artifact. Preserve and extend the raw-`go test` regression tests
when changing test lifecycles.

Use the repository wrapper for full suites and long-running verification:

```sh
AGENT_DECK_TEST_TMP_BASE=/private/tmp/agent-deck-tests \
  scripts/run-tests.sh <test command> [arguments...]
```

The wrapper contains Go toolchain scratch files created before a test binary
starts and reaps runs interrupted by `SIGKILL` or a crash—cases repository test
code cannot clean itself. It is a safety net, not a substitute for test cleanup.
