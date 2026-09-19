# Functional build check

Run `make check-functional` before installing a local build. It builds a Linux
binary and exercises real CLI commands against a synthetic fleet. The Markdown
table explains each operation, what the user sees, its outcome, and duration.
`funccheck-report.json` retains the same evidence and the selected binary's
SHA256 digest for review. Any FAIL exits 1.
Skipped and unknown checks remain visible and are not passes.

## Local use

```sh
make check-functional
```

Docker is required. The first run builds an image with Go, git, tmux, and cached
Go modules, so image preparation needs network access. The actual checks run as
UID 1000 with no network and all Linux capabilities dropped. The repository is
mounted to write the JSON report. No host `go test` runs. A named Docker
volume, `agentdeck-funccheck-gocache`, retains compiled Go packages between
runs. The first cold compilation can take substantially longer than a warm
check, especially on a busy machine.

To check an existing **Linux binary** stored in this checkout:

```sh
make check-functional FUNCCHECK_BINARY=build/candidate-linux
```

The path is relative to the repository (or an absolute path inside `/src`). A
macOS binary cannot execute in the Linux container. Override `FUNCCHECK_IMAGE`
when selecting another compatible image. Docker image preparation can be run
separately with `make funccheck-image`.

## CI and direct invocation

The advisory workflow builds and checks natively on a disposable Ubuntu runner,
verifies the tool's deliberately broken fake binary tests, and uploads the JSON
report and Markdown output under an artifact named with the commit SHA.

```sh
# Inside Docker or disposable CI:
go run ./tools/funccheck /path/to/agent-deck

# Include source golden checks from the repository root:
FUNCCHECK_SOURCE_CHECKS=1 go run ./tools/funccheck /path/to/agent-deck
```

`make check-functional FUNCCHECK_MODE=native` requires `CI=true`. Direct invocation
is intended for Docker or disposable CI too, not the maintainer's host.

## Isolation and coverage

The runner uses a throwaway HOME and XDG directories, a private tmux socket and
socket directory, fake tool/account/MCP configuration, and synthetic project
repositories. The update-check kill switch stays enabled for every command,
including the cached update probe. In v1.16.10 that switch bypasses the cache
path, so the probe reports UNKNOWN instead of claiming that the cache worked.
Teardown addresses only the runner's private tmux server. No account credentials,
real model calls, host fleet, or external SSH machine are needed.

Named checks cover session creation, pane existence, send/output, status,
stop/restart, fork/removal; groups; worktrees; child completion and inbox
consumption; MCP and account records; remote commands through a fake transport;
and cached updates. Optional build capabilities, including health and remote
switch, are skipped with a reason when absent. Refer to the generated report for
the precise checks that executed and their result on the selected build.

TUI checks have two distinct evidence classes:

* **TUI source** compares headless home-list and remote-state frames with
  `internal/ui/testdata/funccheck`, and reuses the existing new-session dialog
  frames in `internal/ui/testdata/newdialog_flow`. They validate the checkout's
  rendering code. The runner requires an explicit passing test event, so absent
  or skipped tests cannot pass. These checks require `FUNCCHECK_SOURCE_CHECKS=1`,
  which the Make target sets automatically.
* **TUI binary** remains skipped: source golden tests cannot prove the supplied
  executable renders those frames. A passing source check is never presented as
  binary visual verification. These plain-text frames also do not prove color,
  terminal emulation, interactive input, or physical display appearance.

The stubbed remote lane validates CLI dispatch and records, not real SSH
connectivity or remote authentication. Fake account switching does not prove
provider authentication. This suite supplements the existing eval-smoke,
native SSH acceptance, and systemd acceptance workflows; it does not replace
their transport or service-manager evidence. Advisory CI is not a release gate.

## Reviewing frame changes

Golden changes must be reviewed as user-visible changes. Regenerate only inside
Docker or disposable CI, then rerun without `UPDATE_GOLDEN`:

```sh
UPDATE_GOLDEN=1 go test ./internal/ui \
  -run '^(TestFunctional(Home|Remote)Golden|TestNewSessionFlow_Golden)$' -count=1
go test ./internal/ui \
  -run '^(TestFunctional(Home|Remote)Golden|TestNewSessionFlow_Golden)$' -count=1
```

## Baseline finding

On the v1.16.10 baseline, removing a synthetic Claude session leaves its hook
status file behind. The fork-removal and child-removal checks retain FAIL for
that defect. The runner exits 1. The advisory workflow publishes the failed
report and emits a warning while allowing other CI work to continue. A green
advisory job is not a passing functionality report.

The fixture uses `session send --no-wait` and verifies an actual synthetic
response. It models active and idle terminal states and emits public hooks;
it does not validate model quality or provider login. Shell-specific delivery
is not covered by that lane.
