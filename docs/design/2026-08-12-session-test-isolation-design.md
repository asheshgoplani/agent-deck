# Session test environment isolation design

## Motivation

`internal/session` tests that create a temporary `HOME` assume Codex resolves
its home as `$HOME/.codex`. A developer-provided `CODEX_HOME` is intentionally
more specific in production, so those tests instead mutate or inspect the
external home and fail. The stale-SSH-socket test can also exceed macOS's
Unix-domain socket path length when `t.TempDir()` has a long prefix.

## Decisions

- Tests that assert the default `$HOME/.codex` behavior will explicitly clear
  `CODEX_HOME` with `t.Setenv`. Tests that exercise an explicit Codex home
  continue to set it to their own temporary directory.
- The SSH socket fixture will create its directory beneath a short,
  test-owned temporary base, preserving its existing stale/live/regular-file
  assertions while remaining within the platform path limit.
- Production Codex-home precedence and SSH cleanup behavior are out of scope;
  they already implement the intended behavior.

## Verification

- Reproduce the failures with an externally supplied `CODEX_HOME`.
- Run the five formerly failing session tests under `-race` with the external
  value present.
- Run the complete `internal/session` race package and the repository test
  suite to identify any remaining unrelated failures.

## Design check

The change adds no abstraction, option, or production branch. Each test owns
its process environment and filesystem fixture directly, which keeps the
test intent explicit and avoids duplication of environment assumptions.
