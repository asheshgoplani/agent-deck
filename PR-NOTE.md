# Note for the contributor (PR #2120)

Thanks for the fix — the sizing-policy propagation logic was solid and needed
no code changes. Here's what we did on top:

## What we did

1. Rebased `fix/2061-shell-window-sizing` onto current `origin/main` (it was 60
   commits behind). The rebase was clean, no conflicts, your commit carried
   over verbatim.
2. Re-ran build, vet, and lint on the rebased tree, and re-ran the touched
   package's tests in an isolated Docker container.
3. Trimmed the PR body below to plain, checkable evidence — the original draft
   included specific benchmark numbers, named review models, and per-run
   receipt counts that we can't independently verify from this pass, so
   they're replaced with what we actually confirmed here.

## Result

- The gosec G702 lint finding that was showing on `internal/tmux/socket.go:212`
  is gone after the rebase — it was a stale-branch artifact (current `main`
  already carries the `#nosec G204,G702` annotation on that call site your
  branch predates). Confirmed with `golangci-lint run ./internal/tmux/...` →
  0 issues.
- `go build ./...` and `go vet ./...` are clean.
- Your new test, `TestSession_NewShellWindowSizePolicy` (and its 5 subtests),
  passes reliably in the Docker tmux sandbox, run twice back-to-back.
- One unrelated test in the same package, `TestKill_LiveSessionThenSecondKillBothSucceed`,
  failed once under full-package concurrent load and then passed 3/3 times in
  isolation — a pre-existing flake, not something your change touches or
  causes.

## Suggested replacement PR body

---

**What problem does this solve?**

Open Shell Here in window mode loses the managed session's sizing policy: the
initial window has `largest/on`, while the new shell window inherits
`latest/off`. Explicit sizing overrides are also lost. This addresses the
Deck-created-window portion of #2061, reported by @rafi-rr.

**Why this change**

Configure the exact window ID returned by `new-window`, using the same sizing
defaults and explicit option values as session creation. `set-window-option
-oq` preserves local options installed by a user's hook while still applying
the next option. Actual window-creation errors remain errors; optional
post-creation configuration failures are logged, not fatal.

**User impact**

Deck-created shell windows retain their intended sizing policy. Global
defaults and existing windows are untouched. Native `prefix c` and
agent-created windows keep the existing documented workaround. The
macOS/tmux 3.7a height-collapse observation from #2061 stays open and
unaddressed by this PR.

**Testing**

- `go build ./...`, `go vet ./...`: clean.
- `golangci-lint run ./internal/tmux/...`: 0 issues.
- `go test ./internal/tmux/...` in the project's sandboxed Docker tmux
  container: the new `TestSession_NewShellWindowSizePolicy` suite (5 subtests)
  passes reliably.
- Rebased onto current `main`; no conflicts.

**AI disclosure**

- [x] AI-assisted implementation and review; commit authorship and issue
  credit as stated above.

---
