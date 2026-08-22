# Complete named case catalogue

Every case receives only `skills/agent-deck/SKILL.md` and `skills/agent-deck/references/cli-reference.md` (the skill's routed CLI reference). The common success oracle is: the deterministic agent selects the exact expected command from the supplied skill, runs it once in its isolated container, and it exits zero. No trial-and-error or fallback command is allowed.

| ID | Task prompt given to agent | Success oracle / exact end state | Cheap-path expectation |
|---|---|---|---|
| create-session | Create a Claude session named Eval Project in /work. | `agent-deck add -t 'Eval Project' -c claude /work` exits 0 | `add` |
| find-session | Find the session named Eval Project without scanning transcript files. | `agent-deck session search 'Eval Project'` exits 0 | `session search` |
| read-output | Read the latest output from session Eval Project. | `agent-deck session output 'Eval Project'` exits 0 | `session output` |
| attach-detach | Attach to Eval Project and identify the documented detach key. | `agent-deck session attach 'Eval Project'` exits 0 | `session attach`; Ctrl+Q |
| switch-model | Switch Eval Project to the opus model. | `agent-deck session set 'Eval Project' model opus` exits 0 | `session set` |
| drain-inbox | Drain pending inbox messages until processing is complete. | `agent-deck inbox drain --until-done` exits 0 | `inbox drain --until-done` |
| add-remote | Register SSH host build@example as remote buildbox. | `agent-deck remote add buildbox build@example` exits 0 | `remote add` |
| recover-errored-session | Recover the errored session Eval Project. | `agent-deck fleet recover 'Eval Project'` exits 0 | `fleet recover` |
| top-add | Create a session named Demo in /work. | `agent-deck add -t Demo /work` exits 0 | `add` |
| top-launch | Create and start a Claude session in /work. | `agent-deck launch /work -c claude` exits 0 | `launch` |
| top-list | List all sessions as JSON. | `agent-deck list --json` exits 0 | `list --json` |
| top-remove | Remove the session Demo. | `agent-deck remove Demo` exits 0 | `remove` |
| top-status | Get the cheap machine-readable fleet status. | `agent-deck status --json` exits 0 | `status --json` |
| top-migrate-paths | Preview migration to XDG paths without changing data. | `agent-deck migrate-paths --dry-run` exits 0 | `migrate-paths --dry-run` |
| top-web | Inspect how to start the browser UI. | `agent-deck web --help` exits 0 | `web` |
| top-session | Show session Demo as JSON. | `agent-deck session show Demo --json` exits 0 | `session show` |
| top-fleet | Show fleet status. | `agent-deck fleet status` exits 0 | `fleet status` |
| top-worktree | List managed worktrees. | `agent-deck worktree list` exits 0 | `worktree list` |
| top-mcp | List available MCP servers. | `agent-deck mcp list` exits 0 | `mcp list` |
| top-skill | List available skills. | `agent-deck skill list` exits 0 | `skill list` |
| top-group | List groups. | `agent-deck group list` exits 0 | `group list` |
| top-profile | List profiles. | `agent-deck profile list` exits 0 | `profile list` |
| top-conductor | Show conductor status. | `agent-deck conductor status` exits 0 | `conductor status` |
| top-remote | List registered remotes. | `agent-deck remote list` exits 0 | `remote list` |
| top-codex-hooks | Inspect Codex hook setup options. | `agent-deck codex-hooks --help` exits 0 | `codex-hooks` |
| top-deepseek | Inspect DeepSeek setup options. | `agent-deck deepseek --help` exits 0 | `deepseek` |

The `top-*` rows cover every top-level command family documented by `cli-reference.md`; the first eight are the standing programme's realistic core cases.
