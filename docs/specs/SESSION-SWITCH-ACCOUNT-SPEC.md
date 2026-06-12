# Session Account Switch with Conversation Migration

**Status:** Approved design (2026-06-10)
**Branch:** `feat/session-switch-account-conversation` (off origin/main v1.9.55)

## Problem

agent-deck sessions have a per-session `Account` field (#924) that selects a Claude
config dir via `[profiles.<name>.claude].config_dir`. Switching account today loses
the conversation: Claude stores history at
`<config-dir>/projects/<encoded-project-path>/<session-id>.jsonl`, so after a switch
`--resume` looks in the new config dir and finds nothing.

## Empirically validated mechanism (2026-06-10, two real accounts)

1. Conversation created under account A (`~/.claude-work`), codeword planted.
2. `.jsonl` copied to account B's config dir (`~/.claude-buddii`), same encoded
   project folder, same filename.
3. `claude --resume <id>` under B recalled the codeword. Conversation continued and
   evolved under B.
4. Evolved file copied back to A; resume under A recalled both codewords.

Findings that constrain the design:

- The session id is account-independent; `--resume` is a pure file lookup.
- **Resume can assign a new session id** (observed: print-mode resume renamed the
  conversation file to a new UUID). The stored `ClaudeSessionID` may therefore be
  stale; migration must locate the newest conversation file for the project, the
  same way `ensureClaudeSessionIDFromDisk` does.
- The encoded project dir name (`ConvertToClaudeDirName`) depends only on the
  project path, so it is identical across config dirs.
- MCP/plugin availability and usage limits follow the new account; history follows
  the file.

## Design

### Core: `MigrateConversation` (internal/session)

`MigrateConversation(inst *Instance, targetConfigDir string) (migratedPath string, err error)`

1. Resolve source config dir with the existing resolver
   (`GetClaudeConfigDirForInstance`).
2. No-op (nil error, empty path) if source and target resolve to the same dir.
3. Locate the conversation file: `<src>/projects/<ConvertToClaudeDirName(ProjectPath)>/`,
   prefer `<ClaudeSessionID>.jsonl`; if missing or stale, fall back to the newest
   `.jsonl` in that dir (existing disk-scan behavior). Update `inst.ClaudeSessionID`
   if the newest file's id differs.
4. **Copy-only. Never delete the source.**
5. If the destination file already exists and differs, back it up first as
   `<name>.jsonl.bak-<unix-ts>` before overwriting.
6. Verify the copy (size match) before reporting success.

### CLI: `agent-deck session switch-account <session> <account>`

1. Validate `<account>` exists as `[profiles.<account>.claude]` with a `config_dir`;
   on failure list available accounts.
2. If running: stop the session.
3. `MigrateConversation` to the target account's config dir.
4. `SetField(inst, FieldAccount, <account>)`.
5. Restart (existing `--resume` path picks up the migrated file). `--no-restart`
   skips step 5; a stopped session is not started.

### `session set <s> account <name>`

After the existing field update, also run `MigrateConversation`. Copy failure does
not roll back the field; it prints a warning that the conversation will not follow
until migrated.

### Out of scope (v1)

TUI picker; `--move` (delete source after verify); cross-machine transfer.

## Safety (post-incident rules apply)

- Copy-only with pre-overwrite backup; no destructive operation anywhere.
- All tests run under sandboxed `HOME` + cleared `XDG_*`; no test may touch a real
  config dir (`isolatePackageHome` pattern).

## Tests

- Unit: path resolution; same-dir no-op; missing source error; stale-id fallback to
  newest file; backup-on-conflict; size verification.
- CLI: switch-account happy path, unknown account, `--no-restart`.
- E2E (manual/smoke, already executed by hand 2026-06-10): codeword round-trip
  between two real accounts.
