# Account slots over SSH

When two logins share one macOS user and profile, set `AGENTDECK_ACCOUNT` before
starting the CLI. A session stores that slot, and later starts, restarts, forks,
and nested commands continue using the saved slot instead of the caller's
ambient credentials.

```sh
AGENTDECK_ACCOUNT=person_a agent-deck launch . --cmd claude
agent-deck launch . --cmd claude --account person_b
```

`session list`, `session show`, JSON output, and the TUI display the saved slot
as `account` and `owner`. `owner` identifies the credential slot, not a human.
An empty account clears the override. Unknown or tool-incompatible slots fail
before a session or conversation is changed.

Remote commands execute on the selected host. Use `agent-deck remote NAME
session show TITLE --json` to inspect the server's saved slot and
`agent-deck remote NAME session send TITLE --message-file task.md` to send a
message without moving the file to the controller.

Worktree cleanup reads every local profile before removing an unregistered
worktree. A live session from another profile therefore protects its checkout.
