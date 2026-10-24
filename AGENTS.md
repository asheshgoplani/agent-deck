# AGENTS.md

For coding agents (Codex, Cursor, OpenCode, Gemini, Claude Code...) in this repo. Humans:
[CONTRIBUTING.md](CONTRIBUTING.md) says the same at more length.

## What this is

Agent Deck is a terminal session manager for AI coding agents (Claude Code, Codex,
Gemini, OpenCode and others). Each session runs in tmux; one Go binary, `agent-deck`,
provides the TUI (Bubble Tea), the CLI, a web UI and the conductor.

## Safety rule: tests never touch a real HOME or your tmux server

The tests start real tmux servers and processes and write agent-deck state. Run
`go test` only inside a container, never on your host. That also rules out `make test`,
`make ci` and the lefthook `pre-push` hook on a host: all three run `go test`.

```bash
# Gates, PR body checks and tests. On a host it skips go test and prints the docker run command.
FULL_TESTS=1 skills/agent-deck/scripts/contributing/self-check.sh pr-body.md
# CI's Go checks (gofmt, build, vet, golangci-lint, race tests) in throwaway containers.
make verify-docker
make verify-docker VERIFY_ARGS="--stages test --pkgs ./internal/session/..."
```

`self-check.sh` counts as inside a container when `/.dockerenv` or `/run/.containerenv`
exists or `AGENTDECK_TEST_CONTAINER=1` is set. Set that variable only when it is true.

What guards the code if a test still runs somewhere it should not:

- Packages with a `testmain_test.go` (not every package has one) call, in `TestMain`,
  `testutil.IsolateHome()` (temp HOME, cleared `XDG_*` and tool config dirs such as
  `CLAUDE_CONFIG_DIR`, `AGENTDECK_PROFILE=_test`) and `testutil.IsolateTmuxSocket()`
  (unsets `TMUX`/`TMUX_PANE`, private `TMUX_TMPDIR`). `internal/testutil/testmain_audit_test.go`
  fails any `testmain_test.go` defining `TestMain` that skips them.
- `internal/sandboxguard` panics when a test binary resolves a path under the real home
  (passwd home or the HOME the binary started with) and names the env var it came through.
- `internal/tmux` panics before spawning tmux when a test binary would reach your default
  tmux socket (`test_isolation_guard.go`, `default_socket_guard.go`). There is no opt-out.

These guards are the last line of defence, not permission to run tests on a host. A
guard panic aborts the package's test binary; fix the isolation, never the guard.
Do not export a real `HOME`, `CLAUDE_CONFIG_DIR` or `TMUX` into a test. Do not run
`claude`, `claude -p` or other agent CLIs against your real config to check a change;
reproduce in a container as [the repro workflow](skills/agent-deck/references/repro.md) says.

## Build and check exactly as CI does

- Go 1.26.9 (from `go.mod`; the Makefile sets `GOTOOLCHAIN=go1.26.9`).
- Build `go build ./...` (or `make build`: runs `make css` first, downloads Tailwind to
  `~/.local/bin`). Then `gofmt -l .` prints nothing (worktree dirs aside) and `go vet ./...`
  passes, as the lefthook pre-commit hook checks. All safe on a host.
- Lint: `make lint` runs golangci-lint v2.14.0 (CI: same version, `--timeout=5m`) with
  `.golangci.yml`: standard linters plus gosec and forbidigo, staticcheck limited to `SA*`,
  gofmt formatter. Safe on a host; the first run downloads the linter via `go run`.
- Tests (`.github/workflows/go-test.yml`, required check `Full test suite (PR gate)`): Ubuntu
  with tmux and zoxide, `AGENTDECK_SKIP_UPDATE_CHECK=1`, `PERF_BUDGET_MULTIPLIER=2.0`, sharded
  by package, plus `scripts/ci-shared-state-proof.sh`. Each shard runs
  `gotestsum --rerun-fails=2 --rerun-fails-abort-on-data-race --packages="<pkgs>" -- -race -timeout 20m`.
  `make verify-docker` repeats this except the proof. Go diffs also run `Required native SSH
  acceptance` (Linux) and `Required macOS native SSH acceptance`.
- Docs-only diffs skip the Go suite; the gate still reports. `pr-diffscope.yml` fails a PR
  touching more than 200 files.

## Where code lives

Map of packages and data flow: [docs/architecture.md](docs/architecture.md). Short version:

- `cmd/agent-deck/`: CLI entry point and subcommands (`main.go` dispatches them).
- `internal/session/`: sessions, groups, profiles, tool presets, user config.
- `internal/tmux/`: tmux layer, status detection. `internal/ui/`: TUI. `internal/web/`: web UI.
- `internal/testutil/`: test isolation helpers. `tests/`: e2e, eval and CI script tests.
- `skills/`: the product's agent skill. Its contributor workflow is
  [skills/agent-deck/references/contributing.md](skills/agent-deck/references/contributing.md)
  (not auto-loaded by any agent; read it before opening a PR).
- User docs, all hand-edited (nothing generates them): `README.md`, `llms.txt`,
  `llms-full.txt`, `site/index.html` (deployed by `pages.yml`), `docs/`,
  `skills/agent-deck/references/`. A docs fact usually has copies in several of these.

## Conventions

- Errors: lowercase, no trailing punctuation, wrap with `%w` and say what failed:
  `fmt.Errorf("checking whether inferred profile %q exists: %w", name, err)`. Capitals
  only for proper names (`MCP`, `Codex`). Review enforces this, not lint (ST checks are off).
- Logging: `log/slog` through `internal/logging`. Declare
  `var fooLog = logging.ForComponent(logging.CompSession)` and log snake_case event names
  with typed attrs: `commsLog.Warn("dead_letter_missed_log_write_failed", slog.String("error", err.Error()))`.
- Child processes: build their env with `internal/childenv`. `os.Environ()` is banned by
  forbidigo outside the exempt paths in `.golangci.yml`.
- Config keys: `~/.config/agent-deck/config.toml` maps onto `UserConfig` in
  `internal/session/userconfig.go`. A new key is a field with a snake_case `toml:"key,omitempty"`
  tag and a doc comment giving valid values and the default; document it in
  `skills/agent-deck/references/config-reference.md`.
- Tests fail first: write the regression test, watch it fail on the unfixed code, then
  fix. `self-check.sh` runs a revert-check (your tests must fail without your change).
  Bug fixes also need a reproduction: run [the repro workflow](skills/agent-deck/references/repro.md), put its
  verdict in `## Evidence` and pass its `result.json` as `REPRO_REPORT=<path>` to `self-check.sh`.
  Match the neighbours: table-driven tests, existing helpers before new ones.
- Goldens under `cmd/agent-deck/testdata/goldens/`: run `make goldens-update` only for an
  intended, reviewed behaviour change, never to turn a red suite green.

## Review rules (bug classes that keep coming back)

1. Fix the siblings. When you fix a call site (or a docs fact), grep for every other copy
   of the same pattern. Fix the same bug in the same PR; list in the PR body any copy that
   is a different problem or needs a separate area's review. Several recent bugs were siblings.
2. Resolve the profile first. Never key a lock, lookup or path on the raw `-p` string or
   a literal `"default"`. Use `session.GetEffectiveProfile` (or
   `session.ResolveProfileForStorage` before creating or opening state) and pass the result on.
3. No byte cuts on display text. Never `s[:n]` a string a user sees. Use `truncateWidth`
   in `cmd/agent-deck` or `truncateDisplay` / `truncateDisplayLeft` in `internal/ui`;
   elsewhere `runewidth.Truncate` or `ansi.Truncate`.
4. Join what you start. Every goroutine, ticker or timer you start must be stopped and
   waited for before the owning `Stop`/`Close` returns. Say who joins it in the PR.

## Before you start: claim or discuss

- Search open issues and PRs, then comment on the issue before starting so nobody
  duplicates work. Look for `good first issue`.
- No issue yet: blank issues are off. Bugs, docs errors included, go through the bug form;
  features start as a Discussion. Questions: Discussions or Discord.
- Security: the private route in [SECURITY.md](SECURITY.md), never a public issue or PR.
- A ~3k-line diff without prior discussion gets flagged for a conversation first.
  Max 5 open PRs per author.

## Do not touch without asking first (in the issue, and wait for a yes before the PR)

- `CHANGELOG.md`: never edit it in a PR; entries are added at landing.
- `.github/` (workflows, intake, templates): human-gated, never auto-merged; keep out of unrelated PRs.
- Release files: `.goreleaser.yml`, `.github/workflows/release*.yml`, `install.sh`,
  `uninstall.sh`, `scripts/release-readiness.sh`, the `Version` variable in
  `cmd/agent-deck/main.go`, `.claude/release-tests.yaml`.
- `go.mod` / `go.sum`: new or bumped modules get extra scrutiny; no `replace` or `GOPROXY`.
- `.claude/plans/` and `.claude/commands/`: leftover session notes carried in by an earlier
  PR (mostly February 2026), not instructions for this repo. Do not follow or update them.

## Pull request

Use `.github/PULL_REQUEST_TEMPLATE.md` verbatim; [.github/INTAKE.md](.github/INTAKE.md) is
the field contract and the intake bot (`.github/workflows/pr-intake.yml`) checks it.
A filled example: [contributing-pr-body-template.md](skills/agent-deck/references/contributing-pr-body-template.md).

- Required headings with content, exactly as written (`self-check.sh` matches `^## Heading`):
  `## What problem does this solve?`, `## Why this change`, `## User impact`,
  `## AI disclosure`, `## What actually bothered you`, `## Checklist`.
  `## Evidence` is required for behaviour changes (real output, not mock-only proof).
- AI disclosure: check exactly one box (Human-written / AI-assisted / AI-authored) and name
  the model (`claude-opus-4-x`, `gpt-5-codex`, ...; `unsure` is fine; `none` if Human-written).
- What actually bothered you: quote what your human asked for. Intake never accepts it blank.
- Checklist: check only what is true. Hot paths (list, status, session output, startup,
  tmux layer) need before/after timing.
- Last line, matching the checked box:
  `<!-- gate:ai=<human|assisted|authored> model=<name|unsure|none> intent=<yes|no> -->`
- One problem per PR, branch `fix/...`, `feature/...`, `docs/...`, conventional commit
  messages (`fix: ...`, `docs: ...`), "Allow edits from maintainers" on.
- Run `self-check.sh pr-body.md` and open only when it ends with `Ready to open`. With `.go`
  changes that means the in-container run; a docs-only diff gets there on the host. It
  diffs against `BASE_REF` (default `origin/main`). In a fork, `origin` is your fork:
  `git fetch upstream main`, then `BASE_REF=upstream/main`.
