# Worktrees and sandboxes

[Back to the README](../../README.md) . [Guide index](README.md)

## Git Worktrees

Multiple agents can work on the same repo without conflicts. Each worktree is an isolated working directory with its own branch.

- `agent-deck add . -c claude --worktree feature/a --new-branch` creates a session in a new worktree
- `agent-deck add . --worktree feature/b -b --location subdirectory` places the worktree under `.worktrees/` inside the repo
- `agent-deck worktree finish "My Session"` merges the branch, removes the worktree, and deletes the session
- `agent-deck worktree cleanup` finds and removes orphaned worktrees

Configure the default worktree location in `$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/config.toml`):

```toml
[worktree]
default_location = "subdirectory"  # "sibling" (default), "subdirectory", or a custom path
```

`sibling` creates worktrees next to the repo (`repo-branch`). `subdirectory` creates them inside it (`repo/.worktrees/branch`). A custom path like `~/worktrees` or `/tmp/worktrees` creates repo-namespaced worktrees at `<path>/<repo_name>/<branch>`. The `--location` flag overrides the config per session.

### Sparse Checkout (large monorepos)

If the session you create the worktree from uses [sparse checkout](https://git-scm.com/docs/git-sparse-checkout), a new worktree normally checks out the *whole* repository first — minutes of I/O on a monorepo with hundreds of thousands of files. Opt into inheriting the sparse configuration instead:

```toml
[worktree]
sparse_checkout = "inherit"   # "off" (default) keeps git's normal checkout
```

With `inherit`, agent-deck reads the sparse mode (cone / non-cone, sparse index) and patterns from the worktree you invoked from, creates the new worktree with `--no-checkout`, and materializes it directly with those patterns — the excluded tree is never written. `.worktreeinclude` and `.agent-deck/worktree-setup.sh` still run afterwards, so the setup script sees the same sparse paths the source session has.

A non-sparse source, or `off`/unset, keeps today's behavior exactly. Inheritance replays the patterns through `git sparse-checkout set` and pins the sparse-index setting explicitly, so it needs git 2.32 or newer; the default (`off`) has no version requirement.

### Copying Gitignored Files (`.worktreeinclude`)

Gitignored files (`.env`, `.mcp.json`, etc.) aren't copied into new worktrees by default.
To declare which gitignored files should be copied automatically, create a `.worktreeinclude` file in your repo root:

```gitignore
# .worktreeinclude — gitignore-syntax patterns
.env
.env.local
.mcp.json
secrets/
```

Only files that are both pattern-matched AND gitignored get copied — tracked files are never duplicated.
Directories are copied recursively and merged into existing destinations.
Existing files in the worktree are not overwritten.

This works for both single-repo and multi-repo worktree sessions.
Matches [Claude Code Desktop semantics](https://code.claude.com/docs/en/worktrees#copy-gitignored-files-into-worktrees).

### Worktree Setup Script

For imperative setup tasks (installing dependencies, running migrations, etc.), create a script at `.agent-deck/worktree-setup.sh`.
Once you have approved it (see [Approving worktree hooks](#approving-worktree-hooks)), agent-deck runs it after creating a worktree and processing `.worktreeinclude`.

```sh
#!/bin/sh
npm install
```

The script receives two environment variables:
- `AGENT_DECK_REPO_ROOT` — path to the main repository
- `AGENT_DECK_WORKTREE_PATH` — path to the new worktree

It runs in the new worktree with your full environment. An executable script runs directly (its `#!` line picks the interpreter); a non-executable one runs via `sh -e`. Under the default approval policy, agent-deck runs a private copy of the approved bytes, so a hook changed while you answer the prompt cannot execute instead. Use the working directory and the two environment variables above to locate project files; the script's own `$0` path points to that temporary copy. The timeout is 60 seconds (`[worktree] setup_timeout_seconds`). If it fails or is skipped, the worktree is still created: you'll see a warning and the session proceeds normally.

### Worktree Destruction Script

For imperative teardown tasks (stopping containers, removing volumes, releasing ports, etc.), create a script at `.agent-deck/worktree-destruction.sh`.
Once approved, agent-deck runs it *just before* removing a worktree, while the worktree still exists.

```sh
#!/bin/sh
docker compose -p "$(basename "$AGENT_DECK_WORKTREE_PATH")" down
```

It receives the same environment variables as the setup script (`AGENT_DECK_REPO_ROOT`, `AGENT_DECK_WORKTREE_PATH`) and uses the same dispatch and a 60-second timeout. If it fails or is skipped, removal proceeds anyway and you'll see a warning. It does not run for sessions that reuse the main working tree (nothing is removed there).

### Approving worktree hooks

These two hooks are code from the repository, and they run as you. Creating or removing a worktree does not by itself approve them, so a repository you just cloned cannot run anything on your machine until you say so.

**What is approved.** An approval covers one hook (setup and destruction are separate) in one repository, and is bound to the script's exact bytes (sha256), its real location (symlinks resolved) and how it runs (directly via `#!`, or `sh -e`). Editing the script, retargeting a symlink or flipping its executable bit asks again. Approvals live in agent-deck's data directory (`~/.local/share/agent-deck/worktree-script-consent.json`), never in the repository.

**When you are asked.** The first time a hook would run, and after any change:

- **TUI:** a dialog shows the repository, the hook, the command that will run, its sha256 and up to the first 20 lines (with an omitted-line count when space is tight), with **Run once**, **Always trust this version** and **Skip** (the default).
- **CLI on a terminal:** the same details, then `[o]nce, [a]lways trust this version, [N]o/skip`.
- **CLI without a terminal** (scripts, CI, `remote`), and requests from the web UI: the hook is skipped with a one-line notice naming the command that approves it.

**Approving ahead of time.**

```bash
agent-deck worktree trust-hooks .                          # show each hook, ask y/N
agent-deck worktree trust-hooks . --hook setup --yes       # scripts: approve after reviewing
agent-deck worktree trust-hooks . --revoke                 # forget the approvals
agent-deck launch . -w feat -b --run-hooks                 # run unapproved hooks this once (prints sha256)
agent-deck launch . -w feat -b --run-hooks --trust         # ...and remember that version
```

`[worktree] run_repo_scripts` sets the policy: `"prompt"` (default), `"never"` (hooks never run), or `"always"` (every hook runs without asking; only for machines where you own every repository you open, because any repository you create a worktree in can then run code as you). Remote hosts apply their own policy and approvals: run `trust-hooks` on the remote host.

### Bare repositories and worktrees

Agent-deck supports two flavors of the [bare-repo layout](https://git-scm.com/docs/git-worktree) where every worktree is a peer (no "main" checkout). The two are distinguished by convention — the basename of the bare git dir.

#### Nested `.bare/` layout

The bare git metadata sits inside a normal-looking project dir at `.bare/`:

```
project/
├── .bare/                         # bare git repo (holds refs, objects, HEAD)
├── .agent-deck/
│   └── worktree-setup.sh          # shared setup script (optional)
├── worktree-a/                    # linked worktree on branch-a
│   └── .git                       # file: gitdir: ../.bare/worktrees/worktree-a
└── worktree-b/                    # linked worktree on branch-b
    └── .git
```

#### True-bare-at-root layout

The result of a plain `git clone --bare repo.git`: the directory itself *is* the bare repo and linked worktrees live as direct children alongside its internal files:

```
project.git/                       # this dir IS the bare repo
├── HEAD, config, objects/, refs/, packed-refs, worktrees/, ...
├── .agent-deck/
│   └── worktree-setup.sh          # shared setup script (optional)
├── main/                          # linked worktree on main
│   └── .git                       # file: gitdir: ../worktrees/main
└── feature-x/                     # linked worktree on feature-x
    └── .git
```

How agent-deck resolves these layouts (v1.7.58+ for nested, v1.9.10+ for at-root):

- **All three handles work.** `agent-deck add <project-root>`, `agent-deck add <bare-dir>`, and `agent-deck add <linked-worktree>` all resolve to the same project root. For the nested layout that's the dir holding `.bare/`. For the at-root layout that's the bare dir itself (`project.git/`). Every linked worktree is treated as equal.
- **The project root is where shared config lives.** Place `.agent-deck/worktree-setup.sh` at `<projectRoot>/.agent-deck/worktree-setup.sh`. Agent-deck looks for it at exactly that path — it does not search individual worktrees. In the at-root layout that means `.agent-deck/` lives *inside* the bare dir alongside `HEAD` and `objects/`.
- **`AGENT_DECK_REPO_ROOT` inside the setup script points to the project root.** Same as above — for at-root that's the bare dir itself.
- **New worktree location** depends on the layout:
  - **Nested:** follows your `[worktree]` setting. `default_location = "subdirectory"` lands worktrees at `<projectRoot>/.worktrees/<branch>`; `"sibling"` lands them next to the project dir.
  - **At-root:** auto-overrides `sibling`/`subdirectory` and lands new worktrees as direct children of the bare dir (`<bareDir>/<branch>`) — neither default makes sense when the project root *is* the bare repo. The `path_template` config still wins if you want a fully custom path.

Example — create a new worktree against a bare repo from anywhere:

```sh
# Nested .bare/ layout
agent-deck add project/ -c claude --worktree feature/c --new-branch
agent-deck add project/.bare -c claude --worktree feature/c --new-branch
agent-deck add project/worktree-a -c claude --worktree feature/c --new-branch
# All three resolve to project/, create project/.worktrees/feature-c/, run project/.agent-deck/worktree-setup.sh.

# True-bare-at-root layout
agent-deck add project.git/ -c claude --worktree feature/c --new-branch
agent-deck add project.git/main -c claude --worktree feature/c --new-branch
# Both resolve to project.git/, create project.git/feature-c/, run project.git/.agent-deck/worktree-setup.sh.
```

`agent-deck worktree list` and `agent-deck worktree finish` work from any of those locations.

Common gotchas:

- **`.agent-deck/` must live at the project root.** For nested layouts that's next to `.bare/`; for at-root layouts that's inside the bare dir. If you commit `.agent-deck/` into a specific branch's worktree instead, agent-deck will not find it — the lookup resolves to the project root, not the current worktree.
- **Detection is by convention: basename `.bare` ⇒ nested layout, anything else ⇒ at-root.** A bare repo named something other than `.bare` inside a project dir (e.g. `project/.git-bare/`) is treated as at-root if you point at it directly. For a fully nested layout, use the canonical name `.bare`.
- **If you keep a `.git` file at the project root** pointing to `.bare/` (a variant some tutorials recommend), point `agent-deck add` at `.bare/` or at a linked worktree rather than at the project root — the `.git` file shadows the bare-repo detection path.

## Docker Sandbox

Run sessions inside isolated Docker containers. The project directory is bind-mounted read-write, so agents work on your code while the rest of the system stays protected.

- Check "Run in Docker sandbox" when creating a session, or set `default_enabled = true` in config
- Press `E` on a sandboxed session to open a container shell
- `agent-deck try <name> --sandbox` starts a quick experiment session (dated folder) in a sandbox

Host tool auth (Claude, Gemini, Codex, etc.) is automatically shared into containers via shared sandbox directories — no re-authentication needed. The exception is the Claude Code login on macOS, which lives in the Keychain: copying it would fork the host's OAuth refresh chain and log the host out, so the sandbox keeps a login of its own. Run `/login` once in your first sandbox session (or pass a `claude setup-token` credential as `CLAUDE_CODE_OAUTH_TOKEN` via `environment`); see the single-owner rule in the sandbox reference.

```toml
[docker]
default_enabled = true
mount_ssh = true
auto_cleanup = true    # Remove containers when sessions end (default: true)
```

Set `auto_cleanup = false` to keep containers alive after session termination, which is useful for debugging container state or inspecting logs.

See the [Docker Sandbox Guide](../../skills/agent-deck/references/sandbox.md) for the full reference including overlay details, custom images, and troubleshooting.
