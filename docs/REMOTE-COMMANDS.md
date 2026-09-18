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
| `session viewers task --json` | `remote lab session viewers task --json` |
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
| `session switch-preview task --to-account alice --json` | `remote lab session switch-preview task --to-account alice --json` |
| `session switch task --to-harness codex --confirm-context-loss` | `remote lab session switch task --to-harness codex --confirm-context-loss` |
| `session switch-account task alice` | `remote lab session switch-account task alice` |
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

## PATH for sessions the server starts

A remote runs `session start` under the PATH of a non-login, non-interactive SSH shell, and the tmux server it starts inherits that PATH. A login shell would add `~/.local/bin` (where `claude` and most user-installed tools live); this environment does not, so without help the session's `exec claude ...` fails with "command not found" and the row goes to error after about 250ms.

Every session agent-deck starts, on a remote or locally, therefore begins with a short PATH prelude that prepends, in this order and only when the directory is missing from the pane's PATH: `$HOME/.local/bin`, `$HOME/bin`, the directory of the running `agent-deck` binary, and `/opt/homebrew/bin` (macOS only). A directory is prepended only when it is an absolute path to an existing directory that is not world-writable and is owned by the user or root: these directories go in front of every tool the session and its children launch, so running the deck from `/tmp` or another user's checkout must never put that directory there. Entries the user already has are never moved or duplicated, and the prelude is idempotent, so a pane whose tmux server already had a full login PATH is unchanged. A `--ssh` session's command runs on the other host through the same kind of non-login shell, so it carries the prelude too, with `$HOME/.local/bin` and `$HOME/bin` expanded there and the directory of that remote's configured `agent_deck_path` added; the remote shell adds a directory only when it exists and is owned by the user the session runs as.

If the tool still cannot be found, the session fails with an explicit reason rather than a generic fast death: `session show <id> --json` reports `spawn_failure.reason = "tool not found on PATH: claude (searched: <PATH>)"`, and the preview and `remote lab session start <id>` say the same. That verdict comes from the pane itself: right after the prelude the pane's shell checks whether it can resolve the tool (`command -v`), so a launch shell's aliases and functions, a tmux server with a richer PATH than the deck process, and the deck's own prepends all count, and the `searched:` list is the PATH the pane actually had. The deck process's PATH is never consulted. When the command's shape leaves room for doubt (an env file or init script is sourced before the tool, PATH is set in the command, the first word is a shell builtin), no check is made and a fast death keeps the generic reason with the pane's dying output. Install the tool into one of the searched directories or use its full path as the session command.

`remote update` still notes when the deployed binary is off the remote's non-interactive PATH, because a bare `ssh <host> agent-deck ...` needs the entry; when the deployed directory is `~/.local/bin` or `~/bin` the note says that the sessions the remote starts already add it themselves.

## Creating a session from the TUI

Pressing `n` on a remote group or session opens the same new-session dialog as for a local session and creates the session on that remote through its own `add`. The dialog forwards what you set in it; a field left alone is decided by the server's own configuration, so a dialog you do not touch behaves exactly as before.

A session created this way is a session of the remote's own agent-deck: it gets the same `AGENTDECK_*` environment and the same identity block (`documentation/HARNESS_IDENTITY.md`) as a session created on that host directly, with the identity file written on the remote by the remote's agent-deck (`$AGENTDECK_IDENTITY_FILE` there is a remote path). Plain shell sessions get the environment and the file too, on a remote and locally, through one export line typed into the pane shell at start. A remote running an agent-deck older than this simply lacks the export in its shell sessions; nothing on the controller depends on it.

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

When the server's `session start` reports that the new session's process died at once (for example the tool is not installed there), the dialog does not delete the session: the row appears under the remote with the error light, the preview shows the server's own explanation (the same `spawn_failure` block `remote lab show <id> --json` returns), and you can fix the cause and press `R` to retry, or `d` to delete it, exactly as after `remote lab add` followed by `remote lab session start`. A server too old to report the reason in that shape still keeps the CLI behaviour of the time: the create is rolled back and the error shown, so no row is ever drawn for a session the server does not have.

Fields the remote `add` cannot express are refused with a message in the dialog rather than dropped silently: a startup query (send it with `remote lab send` once the session runs), multi-repo paths, a reasoning effort for Codex or a Codex-compatible custom tool, and Hermes YOLO mode. Skills are not a dialog field because `add` takes no skill flag; attach them after creation with `remote lab skill attach`.

Remote-management commands such as `remote list` and `remote remove lab` operate on your local configuration. New remote names cannot match those command names. For an existing conflicting name, use the explicit execution form, for example `remote exec remove list --json`; ambiguous shorthand refuses to act. Rename the conflicting entry in your configuration before using the matching management command.

If a worktree operation needs an approved repository setup script, pass `--allow-repo-scripts` after the remote name. The server applies that explicit consent. Normal cleanup confirmation still applies to destructive cleanup.

## Switching a remote session's account or harness

`Shift+P` on a remote row opens the Edit Session dialog for that session, bound to its remote. The account row lists the remote's own slots (read over SSH with `accounts --json` and `accounts --harness codex --json` when the dialog opens, names only); the harness row offers the same harnesses as locally. Saving a changed harness or account asks the remote for `session switch-preview --json` and shows the same "Switch Account?" or "Transfer Context?" confirmation a local session gets, fed by that answer: the remote's capability, its loss disclosure and its warnings. A refusal the remote reports (a slot that exists locally but not there, a target harness that is not on the remote's `PATH`, a managed conductor or watcher bridge target on the remote, a session with no recorded conversation id) is shown verbatim and nothing runs; the missing-harness refusal happens before anything is staged, journaled, persisted or archived. Every forwarded selector, harness and account name is validated to a strict `[A-Za-z0-9._-]` shape (titles may contain spaces) before SSH, and no config directory is ever sent to, or retained from, the remote. A verified cross-harness transfer archives the source on the remote as superseded by the new target (reversible); a pending or failed transfer, and every same-harness account switch, leaves the source unchanged, and the result notice states which. Confirming runs the remote's own `session switch` (`--confirm-context-loss` for a cross-harness transfer), with the same ownership revalidation, parent routing, managed-source refusal and journal the local switch has, on the remote. The result is reported as verified, pending or failed with the remote's status and `recovery_required` flag, and the row updates through the pushed remote events.

The whole switch runs on the remote: its transcripts, config directories and credentials never leave that host, and this computer's account slots are never offered for it. A slot must therefore exist in the remote's `config.toml`; a harness must be installed on the remote. The title row still renames through the remote's `rename`; a title edit and a switch are saved separately, as locally. Runtime flags that live only in the remote's registry (skip permissions, auto mode, extra args, plugins, pin) are edited on the remote with `remote lab session set`. From the CLI the same operations are `remote lab session switch-preview`, `remote lab session switch` and `remote lab session switch-account`; only the session selector, the explicit `--to-harness`/`--to-account` target and the documented options are forwarded, so no local path can ever be handed to the remote's switch engine. Both sides need agent-deck 1.16.10 or newer; an older remote answers the preview with an unknown-command error, which is reported as such.

## Reordering remote groups from the TUI

Shift+Up/Down (and the K/J aliases) on a remote group header run `group reorder <path> --up|--down` on that remote, the same command its own TUI runs, so the new order is stored in the remote's state DB and is shown to everyone who lists that remote. Remote group headers follow the order the remote reports in `group list --json`; the footer says when the remote refused because the group is already first or last among its siblings. A remote running an agent-deck older than `group list --json` keeps listing its groups by name. Sessions inside a remote group keep their local, per-viewer order. The remote host header itself follows the `[remotes]` order in `config.toml`.
