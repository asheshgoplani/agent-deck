# Install and update

[Back to the README](../../README.md) . [Guide index](README.md)

## Installation

**Works on:** macOS, Linux, Windows (WSL)

```bash
curl -fsSL https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/install.sh | bash
```

Then run: `agent-deck`

<details>
<summary>Other install methods</summary>

**Homebrew**
```bash
brew install asheshgoplani/tap/agent-deck
```

**Go**
```bash
go install github.com/asheshgoplani/agent-deck/cmd/agent-deck@latest
```

**From Source**
```bash
git clone https://github.com/asheshgoplani/agent-deck.git && cd agent-deck && make install
```

</details>

<details>
<summary>Uninstalling</summary>

```bash
agent-deck uninstall              # Interactive uninstall
agent-deck uninstall --keep-data  # Remove binary only, keep sessions
```

See [Troubleshooting](../../skills/agent-deck/references/troubleshooting.md#uninstalling) for full details.

</details>

<details>
<summary>Shell completion</summary>

`agent-deck completion <bash|zsh|fish>` prints a completion script for commands and subcommands (`agent-deck se<Tab>` → `session`, `agent-deck session st<Tab>` → `start`/`stop`), plus live resource names where it matters — `agent-deck remote update <Tab>` offers your configured remotes, `agent-deck session set-parent <Tab> <Tab>` offers session titles at both positions, and `agent-deck -p <Tab>` offers your profiles.

```bash
# bash
echo 'source <(agent-deck completion bash)' >> ~/.bashrc

# zsh
echo 'source <(agent-deck completion zsh)' >> ~/.zshrc

# fish
agent-deck completion fish > ~/.config/fish/completions/agent-deck.fish
```

Open a new shell (or re-source the config file) afterwards.

</details>

## Updates

Agent Deck checks for updates automatically.
- Standalone/manual install: run `agent-deck update` to install.
- Homebrew install: run `brew upgrade asheshgoplani/tap/agent-deck`.
- Unattended: `[updates] auto_install` is on by default, so the TUI installs an available update without asking (and restarts itself when `auto_restart` is on). For machines where the TUI is not open every day, `agent-deck update --install-timer` adds a daily run (launchd on macOS, systemd user timer on Linux); `--timer-status` and `--uninstall-timer` manage it and `--dry-run` shows what would be written. Set `auto_install = false` in [config.toml](../../skills/agent-deck/references/config-reference.md) to go back to installing by hand.
- Optional: set `auto_update = true` for a Y/n prompt before the TUI opens.
- Scripts, tests and CI: the automatic install and restart never fire under `go test`, with `CI=true`, with `AGENTDECK_SKIP_UPDATE_CHECK=1`, or when the TUI has no terminal; set the variable in any script that drives `agent-deck` and must not be interrupted by a release.
- macOS note: launchd agents that run the agent-deck binary (`notify-daemon`, `web --no-tui`) crash-loop with `EX_CONFIG` after the binary is replaced, because macOS ties their identity to the file. Every install re-registers the `com.agentdeck.*` agents automatically (retrying the bootstrap with backoff, and once more from the plist when the agent is registered but not running) and prints the `launchctl bootout`/`bootstrap` commands if one does not come back. An updater that runs inside one of those services (the web daemon's own unattended run) never boots that service out: it is left in `<cache dir>/launchd-rebootstrap-pending.json` for the next update run outside it (the timer, or the TUI's). The same marker remembers an agent that was booted out but never accepted back by launchd (with the attempts so far); every update run retries it until it is running again, and `agent-deck update --check` (`--json`: `pending_launch_agents`) shows what is still waiting. An agent on launchd's disabled list (`launchctl print-disabled gui/<uid>`, for example after `launchctl disable`) is left alone: no bootout, no bootstrap, one line naming the `launchctl enable` command that brings it back, and it is never kept in the marker (an older entry for it is dropped; `--check` shows such an entry as `disabled`).
- Audit trail: every unattended run writes `<cache dir>/update.log` (append-only) as well as `debug.log`, each line with the trigger, pid, ppid, launchd service and binary version. Every open TUI writes `<cache dir>/tui/<pid>.json`; `agent-deck update --check` lists TUIs still running an older image than the binary on disk and why they have not restarted (`--json`: `running_tuis`), and a TUI that has waited two hours logs `tui_restart_overdue` with the reason and says so in its banner.
