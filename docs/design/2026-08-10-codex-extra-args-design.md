# Codex Extra Args Design

## Motivation

The orchestrate workflow launches read-only Codex reviewers with native Codex
flags such as `--sandbox read-only`. Agent Deck currently exposes repeatable
`--extra-arg` tokens only for Claude and rejects the same launch for Codex,
even though Codex already has a central command builder that can safely append
quoted flags. Requiring callers to replace `-c codex` with a custom command is
an unnecessary compatibility workaround.

## Decisions

- Support persisted extra arguments for Claude-compatible and
  Codex-compatible sessions through the existing `--extra-arg` and
  `session set ... extra-args` interfaces.
- Continue rejecting extra arguments for tools that have no builder support.
- Append each Codex extra-argument token with shell quoting in the central
  Codex command builder.
- Apply the tokens consistently to fresh starts, resumes, and Codex forks.
- Preserve custom-command passthrough semantics. A custom command already
  carries its own arguments and remains unchanged by persisted extra args.
- Remove the orchestrate fallback based on
  `-c 'codex --sandbox read-only'`; the documented primary path uses native
  `--extra-arg` tokens.

## Interfaces

These forms become valid for Codex:

```bash
agent-deck launch . -c codex \
  --extra-arg --sandbox --extra-arg read-only

agent-deck session set <codex-session> extra-args \
  -- --sandbox read-only
```

`agent-deck add` accepts the same repeatable launch flags. Existing JSON and
storage fields remain unchanged: `extra_args` continues to be a token array.

## Command construction

The Codex builder converts `Instance.ExtraArgs` into a shell-quoted suffix and
places it with Codex's other global options before any `resume` or `fork`
subcommand. This keeps option parsing valid across every lifecycle path.
Custom `-c` command strings retain their current verbatim passthrough behavior.

## Verification

- CLI tests prove Codex accepts and persists extra args while an unsupported
  shell session is still rejected.
- Session builder tests prove quoting and placement on fresh, resume, and fork
  commands.
- Regression tests prove Claude extra args remain unchanged.
- The sandboxed repository test suite, formatting, vet, and build checks run
  before completion.

## Out of scope

- No new generic flag or per-provider argument field.
- No migration: the existing persisted `extra_args` representation is reused.
- No legacy launcher fallback in the orchestrate skill.
- No change to custom-command passthrough behavior.
