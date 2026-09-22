# Behaviour-freeze goldens

These files record TODAY's (2026-09-22) behaviour of the `agent-deck` CLI,
frozen before the command-registry refactor (CORE-PLAN.md section 7,
"Behaviour freeze"). The refactor must reproduce every one of these files
byte for byte.

**The rule: a diff is a behaviour change; it needs a reviewer's PASS.**
Nobody regenerates these files to make a red suite green. If
`TestCLIGoldens` or `TestStorageBytesGoldens` fails, the first question is
"is this an intended behaviour change?" — not "let me update the golden."
Only after a reviewer has read the diff and signed off does regeneration
happen.

## What is covered

- `cmd/agent-deck/goldens_test.go` (`TestCLIGoldens`): every top-level
  command's `--help`, every documented subcommand's `--help` (`--help` never
  touches tmux/ssh/network or mutates the store, so it is safe for every
  command with no exceptions), plus every read-only/safely-repeatable command
  run for real against a seeded store, with separate plain and `--json`
  variants where supported. Stdout and stderr are checked independently;
  nonempty stderr lives in a `.stderr.golden` file. `TestCLIGoldensCoverageReport` (`go test -run
  TestCLIGoldensCoverageReport -v ./cmd/agent-deck/`) prints the exact N
  covered / M excluded counts and every exclusion's reason (also in
  `excludedCommands` in that file).
- `cmd/agent-deck/goldens_storage_test.go` (`TestStorageBytesGoldens`): a
  canonical JSON dump of every persisted column of every `instances` and `groups` row in `state.db`
  after each of `session start`, `session stop`, `session restart`, `list`,
  `group list`, run against a private tmux server (`storage_00_seeded.golden`
  through `storage_05_after_group_list.golden`).

Commands that would touch tmux/ssh/network are either run only in their
`--help` form (excluded from real execution — see `excludedCommands`) or, for
`session start/stop/restart`, covered instead by the storage-bytes goldens
above, which use a private tmux server for exactly that purpose.

## The seeded fixture

`goldens_fixture_test.go` seeds one profile (`goldens`, never `default`) with:

- 3 groups: `my-sessions` (default), `backend`, and `backend/api` (a subgroup
  of `backend`).
- 6 sessions with mixed statuses (idle, running, waiting, error, stopped,
  queued) and tools (claude, codex, gemini, shell), IDs `golden-sess-1`
  through `golden-sess-6`.
- 1 remote entry (`[remotes.golden-remote]` in `config.toml`, host
  `golden@remote.invalid` — an address that can never resolve, so any code
  path that accidentally tried to reach it fails fast instead of hanging).

IDs, titles, and paths are fixed literals chosen at seed time, not generated
at run time, so most of the store's shape needs no scrubbing at all — a
change to an ID's *value* would show up as a diff on its own.

## The scrub list

Applied by `scrub()` in `goldens_test.go`, in this order:

1. **Sandbox HOME.** Every occurrence of the sandbox's `$HOME` (and its
   resolved-symlink form, e.g. macOS `/tmp` → `/private/tmp`) is replaced
   with `<SANDBOX_HOME>`. This is the only path-scrubbing rule: real content
   paths in the fixture (`/repo/app`, `/repo/backend`, ...) are fixed
   literals and are asserted verbatim.
2. **Timestamps.** RFC3339-ish (`2026-09-22T12:00:00Z`, with or without
   fractional seconds/zone) → `<TIMESTAMP>`; `YYYY-MM-DD HH:MM:SS` →
   `<TIMESTAMP>`; bare 10–13 digit epoch seconds/millis inside a CLI JSON value
   position → `<EPOCH>`. Storage keeps fixed seeded epoch values exact.
   Only the shell fixture's `last_accessed` value becomes
   `<VOLATILE_LAST_ACCESSED>` because the CLI updates it while acting.
3. **Version.** Only the built binary's known version literal (with optional
   `v` prefix) → `<VERSION>`, so dotted addresses remain exact.
4. **Process IDs.** `pid 12345` → `pid <PID>`.
5. **The goldens binary's own path.** The suite builds `agent-deck` to a
   fresh temp directory per test run (outside the sandbox HOME, so it
   survives every sub-test's `t.TempDir()` cleanup); a command that echoes
   its own resolved binary path (e.g. `hooks status`'s "This binary: ...")
   has that path replaced with `<AGENT_DECK_BINARY>`.
6. **Live tmux session names.** A started or restarted session's random
   `agentdeck_<slug>_<hex>` name → `<TMUX_SESSION>` in storage goldens.

Everything else — every table column, every JSON field, every line of
`--help` usage text, exit codes — is asserted exact. If you need to add a
scrub rule, it goes in `scrubRules` (or the HOME-specific branch) in
`goldens_test.go`, and this list must be updated in the same change.

## Regenerating

```
make goldens-update
```

or directly:

```
AGENTDECK_UPDATE_GOLDENS=1 go test ./cmd/agent-deck/ -run 'TestCLIGoldens$|TestStorageBytesGoldens$' -v
```

Then `git diff` the changed `.golden` files and get a reviewer's PASS before
committing.
