# Round-4 results — PR #2055

## Verdict

PASS. Both round-4 blockers are fixed. The reviewed predecessor was
`1fca383db1827999f9901c9a819e510f2c05ee66`; the round-4 baseline commit was
`287e14db9e6f3649e54d5e9921395acfb6bdd575`. The exact pushed tip is recorded
in the adjacent structured `RESULT.json` receipt because a commit cannot
contain its own hash.

All Go commands below ran through the required Go 1.25 container gate. No Go
test or build command ran on the host.

## Blocking finding 1 — bare `hooks help`

- Reproduction: at the reviewed predecessor, `agent-deck hooks help` printed
  `Unknown hooks subcommand: help`, printed hooks usage, and exited 1.
- Root cause: `cmd/agent-deck/hook_handler.go` changed from its local helper,
  which recognized bare `help`, to the shared flag-only `helpRequested` helper,
  but did not retain a command-position `case "help"` as the Codex, Gemini,
  Hermes, and Cursor hook dispatchers do.
- Fix: `handleHooks` now explicitly handles command-position `help` and writes
  its detailed family usage to stdout. The shared helper remains flag-only, so
  positional data named `help` is still preserved.
- Family audit: `TestEveryRegisteredCommandFamilyBareHelpIsReadOnly` enumerates
  every registered top-level subcommand family that documents bare help,
  including aliases. Each case requires exit 0, family-specific usage (not
  generic top-level usage), and an empty temporary HOME.
- Mutation proof: removing only the new `case "help"` made the `hooks` row RED:
  exit 1 with `Unknown hooks subcommand: help`. The mutation was restored.

## Blocking finding 2 — non-discriminating creds-refresh test

- Decision: keep the production pre-parse guard; it is not redundant. Go's
  flag package happens to make a normal trailing `--help` successful, but does
  not satisfy the stronger CLI contract when an earlier unknown flag exists.
- Strengthened test: the creds case now invokes
  `creds-refresh --not-a-real-flag --help`. The explicit help-consent guard must
  win before flag parsing, print usage, exit 0, and leave the full fixture tree
  unchanged.
- Mutation proof: removing only the `helpRequested(args)` production guard made
  `TestIssue2025TrailingHelpIsReadOnly/creds_refresh` RED with exit 2 and
  `flag provided but not defined: -not-a-real-flag`. The mutation was restored.

## Permanent behavioral eval

`tests/eval/helpconsent` builds and executes the real CLI binary in the eval
sandbox. `TestEval_HooksBareHelpIsDetailedAndReadOnly` requires successful
`hooks help`, detailed hooks usage, and a byte-for-byte stable sandbox tree.
Removing the production hooks case made this eval RED with exit 1; restoring
the production case made it green. The eval is tagged `eval_smoke`, so it runs
in the permanent PR/release evaluator gauntlet.

## Fixed-tip receipts

The consistency gauntlet completed successfully in `golang:1.25`:

```text
go build ./...                                                PASS
go vet ./...                                                  PASS
go test ./cmd/agent-deck -run '<8 help-consent tests>'        PASS (6.631s)
go test -tags eval_smoke ./tests/eval/helpconsent/...         PASS (1.416s)
```

The working tree was checked with `git diff --check`. Production mutation
proofs were applied one at a time and restored before the fixed-tip gauntlet.
