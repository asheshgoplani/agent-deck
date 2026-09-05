# Run commands on a remote deck

Configure a remote host that already has agent-deck and SSH key authentication. Verify its host key with a normal SSH connection first. Both hosts need an agent-deck version that supports the commands you use.

```toml
[remotes.lab]
host = "developer@shared-mac"
agent_deck_path = "/opt/homebrew/bin/agent-deck"
profile = "default"
```

Run the same command through `remote lab`. Output, JSON fields, diagnostics and exit status come from the remote command. Session names, project paths, worktrees, account slots, MCP names and skill sources are resolved on the server.

| Local command on the server | From your computer |
| --- | --- |
| `list --json` | `remote lab list --json` |
| `status --json` | `remote lab status --json` |
| `session show task --json` | `remote lab show task --json` |
| `session output task` | `remote lab output task` |
| `session send task --message-file prompt.md` | `remote lab send task --message-file prompt.md` |
| `add /srv/project --account alice` | `remote lab add /srv/project --account alice` |
| `launch /srv/project -w repair -b --account alice` | `remote lab launch /srv/project -w repair -b --account alice` |
| `session start task` | `remote lab session start task` |
| `session stop task` | `remote lab session stop task` |
| `session restart task` | `remote lab session restart task` |
| `worktree list --json` | `remote lab worktree list --json` |
| `worktree info task --json` | `remote lab worktree info task --json` |
| `worktree cleanup --json` | `remote lab worktree cleanup --json` |
| `mcp attach task memory` | `remote lab mcp attach task memory` |
| `skill attach task review` | `remote lab skill attach task review` |

Prefix every entry with `agent-deck`. The full `session show/output/send` forms also work remotely. Interactive attach uses the existing `agent-deck remote attach lab task` command. Other commands are rejected before execution; there is no local fallback. Interactive options such as `session start --attach` need a terminal and are better run from an SSH login.

`--message-file` is the path exception: the file is read on your computer and streamed through SSH stdin. Use `--message-file -` for a pipeline. Inline messages and all other path arguments retain their ordinary meanings on the server. Repeated message-file options use the last value, matching the local flag parser.

The configured remote profile selects the server registry. An explicit `--account` on add/launch selects a server account slot. Your computer's account environment is not copied to the server. Long-running sends are not limited by the background remote status-probe timeout.

Remote-management commands such as `remote list` and `remote remove lab` operate on your local configuration. New remote names cannot match those command names. For an existing conflicting name, use the explicit execution form, for example `remote exec remove list --json`; ambiguous shorthand refuses to act. Rename the conflicting entry in your configuration before using the matching management command.

If a worktree operation needs an approved repository setup script, pass `--allow-repo-scripts` after the remote name. The server applies that explicit consent. Normal cleanup confirmation still applies to destructive cleanup.
