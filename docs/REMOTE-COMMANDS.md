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
| `session fork task -t "task 2"` | `remote lab session fork task -t "task 2"` |
| `session archive task` | `remote lab session archive task` |
| `session set task title "task 2"` | `remote lab session set task title "task 2"` |
| `session unarchive task` | `remote lab session unarchive task` |
| `worktree list --json` | `remote lab worktree list --json` |
| `worktree info task --json` | `remote lab worktree info task --json` |
| `worktree cleanup --json` | `remote lab worktree cleanup --json` |
| `mcp list --json` | `remote lab mcp list --json` |
| `mcp attach task memory` | `remote lab mcp attach task memory` |
| `skill list --json` | `remote lab skill list --json` |
| `skill attach task review` | `remote lab skill attach task review` |
| `group list --json` | `remote lab group list --json` |
| `group reorder work --up` | `remote lab group reorder work --up` |

Prefix every entry with `agent-deck`. The full `session show/output/send` forms also work remotely. Interactive attach uses the existing `agent-deck remote attach lab task` command. Other commands are rejected before execution; there is no local fallback. Interactive options such as `session start --attach` need a terminal and are better run from an SSH login.

`--message-file` is the path exception: the file is read on your computer and streamed through SSH stdin. Use `--message-file -` for a pipeline. Inline messages and all other path arguments retain their ordinary meanings on the server. Repeated message-file options use the last value, matching the local flag parser.

The configured remote profile selects the server registry. An explicit `--account` on add/launch selects a server account slot. Your computer's account environment is not copied to the server: no config directory, credentials, MCP definition or skill source travels over SSH, only the names, which the server resolves against its own `config.toml`. An account given as a directory path is refused before the command is sent. Long-running sends are not limited by the background remote status-probe timeout.

## Creating a session from the TUI

Pressing `n` on a remote group or session opens the same new-session dialog as for a local session and creates the session on that remote through its own `add`. The dialog forwards what you set in it; a field left alone is decided by the server's own configuration, so a dialog you do not touch behaves exactly as before.

| Dialog field | Sent to the server as |
| --- | --- |
| Name, path, tool, group | `-t`, positional path, `-c`, `-g` (path is a server path, `~` is not expanded locally) |
| Claude account slot | `--account <name>`; the row lists the server's own slots (read over SSH with `accounts --json` when the dialog opens, names only), so a slot configured only on the server can be picked and a slot configured only locally is never offered |
| Model | `--model <id>` |
| Claude effort, skip permissions, auto mode, Chrome, teammate mode, extra args | one `--extra-arg` per token, the same flags a local session launches with; these add to the server's own `[claude]` defaults and cannot switch a server default off |
| Claude session mode | resume with an id: `--resume-session <id>`; continue or bare resume: `--extra-arg -c` / `--extra-arg --resume` |
| Docker sandbox | `-sandbox`; the image comes from the server's config |
| MCPs | one `--mcp <name>` per pick; the row lists the server's own MCPs (read over SSH with `mcp list --quiet` when the dialog opens, which prints names only, so no command, URL or env of a server MCP definition ever travels), so an MCP defined only on the server can be picked and one defined only locally is never offered. The row is absent when the server defines no MCPs, is too old to report them, or when the selected tool cannot attach MCPs |
| Worktree | `-w <branch>`; the worktree and, when missing, the branch are created in the server's repository. An auto-filled branch travels as the bare slug and the server applies its own `[worktree].branch_prefix` once; a branch you type is sent as entered |
| Codex and Gemini YOLO | `--yolo` |
| A path that does not exist on the server | the server refuses the create; the TUI then asks "create this directory on remote `<name>`?" and, only if you confirm, retries with `--create-dir` so the server runs the equivalent of `mkdir -p`. A remote running an agent-deck older than `--create-dir` refuses the retry with an unknown-flag error; create the directory there by hand |

Because the server applies its own defaults, the dialog opens with these options cleared for a remote target instead of pre-filled from your local `config.toml`; a local `[claude].dangerous_mode` or `default_model` never reaches a remote unless you set it in the dialog.

Fields the remote `add` cannot express are refused with a message in the dialog rather than dropped silently: a startup query (send it with `remote lab send` once the session runs), multi-repo paths, a reasoning effort for Codex or a Codex-compatible custom tool, and Hermes YOLO mode. Skills are not a dialog field because `add` takes no skill flag; attach them after creation with `remote lab skill attach`.

Remote-management commands such as `remote list` and `remote remove lab` operate on your local configuration. New remote names cannot match those command names. For an existing conflicting name, use the explicit execution form, for example `remote exec remove list --json`; ambiguous shorthand refuses to act. Rename the conflicting entry in your configuration before using the matching management command.

If a worktree operation needs an approved repository setup script, pass `--allow-repo-scripts` after the remote name. The server applies that explicit consent. Normal cleanup confirmation still applies to destructive cleanup.

## Reordering remote groups from the TUI

Shift+Up/Down (and the K/J aliases) on a remote group header run `group reorder <path> --up|--down` on that remote, the same command its own TUI runs, so the new order is stored in the remote's state DB and is shown to everyone who lists that remote. Remote group headers follow the order the remote reports in `group list --json`; the footer says when the remote refused because the group is already first or last among its siblings. A remote running an agent-deck older than `group list --json` keeps listing its groups by name. Sessions inside a remote group keep their local, per-viewer order. The remote host header itself follows the `[remotes]` order in `config.toml`.
