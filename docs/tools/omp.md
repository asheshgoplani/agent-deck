# Oh My Pi (`omp`)

Agent Deck recognizes the `omp` binary from [Oh My Pi](https://github.com/can1357/oh-my-pi) as a built-in tool. The integration was verified against `omp` v17.3.8.

Each instance launches with `--continue --session-dir "$HOME/.omp/agent-deck/<instance-id>"`. This isolates conversations in the same workspace and lets restart resume the same conversation. Forking starts a child from the newest JSONL file in the parent instance directory.

## Configuration

```toml
[omp]
command = "omp"
env_file = "~/.config/omp.env"
default_model = "anthropic/claude-sonnet-4-6"
approval_mode = "write" # always-ask | write | yolo
```

The new-session model field accepts any model identifier and passes it as `--model`; a per-session selection takes precedence over `default_model`. Other OMP flags can be included in `command`.

## Status detection

Agent Deck treats OMP's captured `⟨esc⟩` footer as busy and its `Allow tool:` dialog as waiting for input. These patterns came from v17.3.8; custom patterns remain available if a later OMP release changes its TUI vocabulary.
