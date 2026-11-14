# FAQ and asking an AI

[Back to the README](../../README.md) . [Guide index](README.md)

## Ask AI about Agent Deck

**Option 1: Claude Code Skill** (recommended for Claude Code users)
```bash
/plugin marketplace add asheshgoplani/agent-deck
/plugin install agent-deck@agent-deck
```
Then ask: *"How do I set up MCP pooling?"*

**Option 2: OpenCode** (has built-in Claude skill compatibility)
```bash
# Create skill directory
mkdir -p ~/.claude/skills/agent-deck/references

# Download skill and references
curl -sL https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/skills/agent-deck/SKILL.md \
  > ~/.claude/skills/agent-deck/SKILL.md
for f in cli-reference config-reference tui-reference troubleshooting capabilities session-communication sub-agents conductors session-workflows autonomy gotchas goal sandbox self-improvement fleet session-share recall retro retro-metrics repro repro-contract; do
  curl -sL "https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/skills/agent-deck/references/${f}.md" \
    > ~/.claude/skills/agent-deck/references/${f}.md
done
```
OpenCode will auto-discover the skill from `~/.claude/skills/`.

**Option 3: Any LLM** (ChatGPT, Claude, Gemini, etc.)
```
Read https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/llms-full.txt
and answer: How do I fork a session?
```

## FAQ

<details>
<summary><b>How is this different from just using tmux?</b></summary>

Agent Deck adds AI-specific intelligence on top of tmux: smart status detection (knows when Claude is thinking vs. waiting), session forking with context inheritance, MCP management, global search across conversations, and organized groups. Think of it as tmux plus AI awareness.

</details>

<details>
<summary><b>Can I use it on Windows?</b></summary>

Yes, via WSL (Windows Subsystem for Linux). [Install WSL](https://learn.microsoft.com/en-us/windows/wsl/install), then run the installer inside WSL. WSL2 is recommended for full feature support including MCP socket pooling.

</details>

<details>
<summary><b>Can I use different Claude accounts/configs per profile?</b></summary>

Yes. Set a global Claude config dir, then add optional per-profile overrides in `$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/config.toml`):

```toml
[claude]
config_dir = "~/.claude"             # Global default

[profiles.work.claude]
config_dir = "~/.claude-team"        # Work account
```

Run with the target profile:

```bash
agent-deck -p work
```

You can verify which Claude config path is active with:

```bash
agent-deck hooks status
agent-deck hooks status -p work
```

See [Configuration Reference](../../skills/agent-deck/references/config-reference.md#claude-section) for full details. To move an *existing* session to another account — conversation included — use `agent-deck session switch-account <session> <account>`.

</details>

<details>
<summary><b>Will it interfere with my existing tmux setup?</b></summary>

No. Agent Deck creates its own tmux sessions with the prefix `agentdeck_*`. Your existing sessions are untouched. The installer backs up your `~/.tmux.conf` before adding optional config, and you can skip it with `--skip-tmux-config`.

</details>
