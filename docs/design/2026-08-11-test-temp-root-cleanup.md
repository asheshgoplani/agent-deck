# Agent-Deck Test Temporary-Root Cleanup

## Motivation

Agent-deck's Go, shell, and browser tests create temporary homes, module
caches, tmux isolation directories, browser profiles, and fixture artifacts.
Normal test cleanup removes these paths, but a killed test runner or abandoned
agent session bypasses process-local cleanup. On macOS these artifacts collect
directly under the user's system temporary directory; one observed machine had
more than 53,000 stale entries consuming roughly 208 GiB.

The repository needs a safe default test entry point that contains its
artifacts and can remove abandoned runs without guessing ownership from generic
temporary filenames.

Human request: "agent-deck repository fix"

## Decisions

1. Official agent-deck test invocations use one uniquely named temporary root
   per run.
2. Every removable root carries an agent-deck-owned marker. Cleanup never
   targets unmarked paths or application-wide temporary directories.
3. Normal exit and catchable termination remove the current run root. A later
   invocation reaps marked roots abandoned for at least 24 hours.
4. Existing tmux/evaluator reaping remains separate because it manages live
   processes and sockets, while this design manages filesystem artifacts.
5. The wrapped command's exit status is preserved.

## Architecture

An official test wrapper creates a direct child of the resolved system
temporary directory named with an `agent-deck-test-run-` prefix plus a
timestamp, process ID, and random suffix. It writes a versioned marker that
records the repository identity, creation time, and owning process ID.

The wrapper exports the run root through `TMPDIR`, `GOTMPDIR`, and the
repository's browser/e2e artifact environment variables before executing the
requested test command. Go temporary directories, compiler files, sandbox
homes, and browser profiles therefore share one ownership boundary.

On normal exit, `INT`, or `TERM`, cleanup restores owner-write permission
within the run root and removes it without following symlinks. This handles Go
module caches whose downloaded content is intentionally read-only.

Before a new run begins, stale cleanup examines only direct sibling directories
with the managed prefix. A candidate is removable only when all of these are
true:

- its marker exists and has the expected schema and repository identity;
- the directory and marker are owned by the current user;
- its canonical path is a direct child of the resolved temporary base;
- its marker creation time is at least 24 hours old;
- its recorded process is no longer running;
- it is not the current run root.

Malformed markers, symlinked candidates, fresh roots, live roots, paths outside
the temporary base, and filesystem errors are skipped with diagnostics. The
wrapper must never broaden cleanup to generic `TemporaryDirectory-*`, Chrome,
or macOS service paths.

## Interfaces and integration

- `make test` invokes the official wrapper.
- Repository verification and end-to-end entry points use the same wrapper or
  its shared temp-root helper where applicable.
- CI uses the official wrapped path.
- Contributor documentation directs developers and agents to the wrapped
  command instead of bare `go test ./...`.
- The wrapper accepts a command and arguments and returns exactly that command's
  exit status.

The default stale age is 24 hours. A test-only override may be exposed for
deterministic tests, but this design adds no general user-facing configuration
surface.

## Verification

Automated tests cover:

- cleanup after a successful command;
- cleanup after a failing command, with exit status preserved;
- cleanup of read-only files;
- removal of a stale, marked, abandoned root;
- preservation of fresh and live roots;
- rejection of malformed or foreign markers;
- rejection of symlink and path-containment attacks;
- preservation of unrelated temporary directories.

An end-to-end check runs the wrapper against a controlled temporary base,
observes all child artifacts beneath the run root, simulates an abandoned run,
and confirms the next invocation removes only that eligible root.

## Out of scope

- Removing arbitrary Chrome or macOS temporary files.
- Cleaning unmarked legacy directories.
- Installing a launchd job or other machine-wide scheduler.
- Guaranteeing containment when a developer bypasses repository test entry
  points and invokes test tools directly.
- Combining process/socket reaping with filesystem cleanup.

## Principles pass

The design uses one ownership boundary and one marker format, avoiding
duplicated filename knowledge. It introduces no plugin system, daemon, or
general-purpose temporary-file framework. Each component exists for the stated
requirement: containment, normal cleanup, safe stale cleanup, or integration
with existing test entry points.
