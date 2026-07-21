# Single-Action Session Archive

## Problem

Archiving an active session from the TUI can require two attempts. The first
attempt stops the tmux session but reports `failed to kill tmux session: exit
status 1`, so the archive timestamp is not persisted. The second attempt sees
the now-stopped session and archives it.

The current kill path already treats a failed `tmux kill-session` as success
when the target no longer exists. However, its post-kill verification calls
`Session.Exists()`, which may return a fresh positive entry from the shared
session cache even after the kill removed the session. That stale positive
turns a successful teardown race into an archive failure.

## Design

Keep the existing public behavior and change only the post-kill error check.
When `tmux kill-session` returns an error, verify the target directly against
its own tmux socket, bypassing the shared cache. Return success if that direct
probe confirms the session is gone; return the original error if the target is
still alive or the bounded probe is indeterminate.

This belongs in `tmux.Session.Kill`, not in the TUI archive handler, because the
same idempotent teardown contract is shared by TUI, web, and CLI callers. The
archive handler must continue refusing to persist the archive flag after a
genuine kill failure so it cannot hide a live session.

## Testing

Add a deterministic regression test that:

1. Primes the default-socket session cache with a positive entry for a tmux
   session that does not actually exist.
2. Calls `Session.Kill()`.
3. Asserts the stale cached entry does not turn the already-absent target into
   an error.

Run the focused tmux tests first, then the repository's standard Go test suite.
Any additional changes must be limited to concrete archive or kill regressions
revealed by those checks.

## Success Criteria

- Given an archive target whose tmux session disappears during teardown, when
  archive is invoked once, then teardown succeeds and the caller can persist
  the archive timestamp.
- Given a stale positive liveness-cache entry, when kill verifies a failed tmux
  command, then it uses live socket state rather than the cached entry.
- Given a target that remains alive after a failed kill, when verification
  runs, then the original error remains visible and archive does not proceed.
- Existing unrelated working-tree changes remain untouched and uncommitted.
