# Live tmux Removal Guard Design

**Date:** 2026-08-05  
**Status:** Approved for implementation

## Problem

The TUI's `X` and Ctrl+X paths remove a registry row when its cached status is
`stopped` or `error`. They do not verify that the associated tmux session is
gone immediately before deletion. A stale status can therefore remove the
only registry reference to a live Claude process.

## Decision

Before each TUI registry-only removal, check whether the instance's tmux
session still exists:

- If it is gone, retain the existing metadata-only removal behavior.
- If it is live, retain the registry row and show a direct message to close or
  destructively delete the session instead.

The single-session and bulk-erred paths use the same guard.

## Out of scope

- Killing sessions from the registry-only action.
- Changing CLI removal semantics or worktree behavior.
- Reaping already-orphaned processes automatically.

## Verification

- A regression test proves a live tmux session is blocked from registry-only
  removal.
- The existing stopped-session behavior remains covered.
