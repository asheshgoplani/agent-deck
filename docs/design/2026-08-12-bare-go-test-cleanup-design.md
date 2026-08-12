# Bare `go test` Cleanup

## Motivation

Agent Deck's test suite must remove its own temporary homes, tmux sockets,
test binaries, browser fixtures, and nested Go-tool artifacts when contributors
run `go test` directly. The existing wrapper contains a whole test invocation,
but correct normal-exit cleanup must not depend on contributors remembering it.

The previous leak was especially costly because tests that redirected `HOME`
then ran nested `go build` or `go test`. Go materialised a read-only module
cache beneath that temporary home, and a plain `os.RemoveAll` silently failed.

## Decision

1. Every Agent Deck-owned temporary tree created by test code is owned by a
   cleanup function that makes directories writable before removal.
2. Every package-level `TestMain` uses the return-code pattern:
   `os.Exit(runTestMain(m))`, with setup and cleanup defers held inside
   `runTestMain`; this ensures cleanup runs after passing, failing, and panicing
   tests.
3. Repository audits fail when a test-created `os.MkdirTemp` tree lacks a
   `RemoveTempTree`/`os.RemoveAll` owner, or when a `TestMain` bypasses its
   cleanup path.
4. A behavioral regression test runs representative packages through bare
   `go test` and confirms that their Agent Deck-owned temporary roots disappear
   after the child process exits.
5. `scripts/run-tests.sh` remains documented as optional containment for the
   Go toolchain's pre-test scratch space and for forced process termination.

## Architecture

`internal/testutil.RemoveTempTree` is the sole removal primitive for test
directories that can contain a Go module cache. It first uses `os.RemoveAll`;
when Go's read-only module-cache permissions block it, it repairs directory
permissions and retries removal.

Package TestMains use `testutil.IsolatePackageHome` or `IsolateHome`, which
registers the returned cleanup inside `runTestMain`. Per-test temporary paths
continue to use `t.TempDir`, while explicit `os.MkdirTemp` call sites either
defer `RemoveTempTree` or delegate their lifetime to a TestMain cleanup.

The audit and behavior test cover both static ownership and real process exit.
The behavior test intentionally calls bare `go test`, rather than the wrapper,
so the regression would fail if a package leaked after a standard contributor
command.

## Limits and out of scope

This cannot remove Go compiler directories created before an Agent Deck test
binary starts, nor can it run cleanup after `SIGKILL`, a machine crash, or a
power loss. Go does not expose a repository-wide post-suite hook for
`go test ./...`; each package has an independent test binary.

The wrapper remains the containment and stale-reaping mechanism for those
unavoidable cases. This change does not delete generic macOS, Chrome, or other
application temporary files.

## Verification

- Add a failing behavioral regression test for a bare child `go test` run that
  leaves an owned temporary root.
- Make it pass after the TestMain/cleanup correction.
- Run the focused testutil and affected-package tests with raw `go test`.
- Run the repository suite through the controlled wrapper, then assert that no
  Agent Deck-owned roots remain under its temporary base.
