# Command registry (`internal/core`)

Slice 1 of the one-core plan. Five commands now run through a typed command
registry instead of calling `internal/session` directly from their CLI
handlers:

| Command id | CLI | Class | Remote |
|---|---|---|---|
| `session.start` | `agent-deck session start` | mutate | allow |
| `session.stop` | `agent-deck session stop` | mutate | allow |
| `session.restart` | `agent-deck session restart` | mutate | allow |
| `session.list` | `agent-deck list` / `ls` | query | allow |
| `group.list` | `agent-deck group list` / `group` | query | allow |

Everything else keeps its existing code path.

## Shape

- `internal/core`: `Registry`, `Def{ID, CLI, Class, Remote, In, Out, Exec}`,
  the executor (`Registry.Run`, `Invoke`), the response `Envelope`, stable
  error codes, progress `Event`s and deferred work (`AfterFunc`,
  `Result.Finish`). It never prints, reads `os.Args` or calls `os.Exit`. The
  command bodies wrap the same `internal/session` calls the CLI made; the two
  CLI-only collaborators (session resolution, `--yolo`) are injected through
  `core.Deps`.
- `internal/core/schema`: JSON Schema by reflection. Field docs come from a
  `doc:"..."` struct tag. Goldens: `internal/core/testdata/schema/<id>.{in,out}.json`
  (`go test ./internal/core -run TestCommandSchemasGolden -update` rewrites them).
- `cmd/agent-deck/core_cli*.go`: the adapter. argv to typed input, result to
  the exact text or JSON the legacy handler printed, and the only `os.Exit`.

Adding a command later means one `Def` plus one adapter; `internal/ui` is not
involved.

## `--json=envelope`

The five commands accept `--json=envelope` in addition to `--json`. It prints
one response envelope instead of the legacy JSON:

```json
{
  "schema": "agent-deck/session.stop/v1",
  "request_id": "9f1c2a7b5e3d4c10",
  "ok": true,
  "data": { "id": "…", "title": "…" },
  "warnings": [],
  "revision": null
}
```

A failure has `"ok": false`, `"data": null` and an `error` object with a
stable `code` (`NOT_FOUND`, `AMBIGUOUS`, `ALREADY_RUNNING`, `NOT_RUNNING`,
`SPAWN_FAILED`, `MISSING_ARGUMENT`, `STORAGE_UNAVAILABLE`,
`NO_ACTIVE_SESSIONS`, `INVALID_OPERATION`, `INVALID_INPUT`, …), a `message`
and optional `data`. `data` follows the output schema of the command.
`revision` is always `null` until the store has a revision counter.

Notes: exit statuses are the same as with `--json`; the envelope is printed
even with `-q`; `restart --all` with failed sessions still returns `ok: true`
(the sweep ran) with the per-session outcome in `data.all` and exit status 1.
Plain `--json` output is unchanged, byte for byte.

## Daemon

`agent-deck daemon serve` runs this same registry behind a unix socket; a
`call` frame returns the envelope `--json=envelope` prints. See
docs/daemon-protocol.md.

## Rollback

`AGENT_DECK_CORE_REGISTRY=0` routes the five commands back to the legacy
handlers, which are kept unchanged until the registry path has soaked.

## Behaviour proof

`scripts/core-capture/capture.sh <binary> <out-dir>` runs the five commands
through a fixed script in a sandbox. Captures from the origin/main binary and
the branch binary must `diff -r` empty. In the repo,
`TestCoreRegistryMatchesLegacyHandlers` runs the same kind of script through
both paths of one binary, and `TestCoreCommandHelpGolden` pins `--help`.
