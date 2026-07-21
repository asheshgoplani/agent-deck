# Single-Action Session Archive Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make one archive action stop and archive a session even when tmux teardown races with a stale positive liveness-cache entry.

**Architecture:** Preserve `tmux.Session.Kill` as the shared idempotent teardown boundary used by TUI, web, and CLI callers. When `kill-session` returns an error, bypass the shared cache and query the target's own tmux socket directly before deciding whether the error is genuine.

**Tech Stack:** Go, tmux integration tests, standard `testing` package.

## Global Constraints

- Keep genuine kill failures visible so callers never archive a live session.
- Do not change archive persistence or presentation behavior.
- Preserve the unrelated `internal/web/static/styles.css` working-tree change.

---

### Task 1: Make post-kill verification cache-independent

**Files:**
- Modify: `internal/tmux/kill_idempotent_test.go`
- Modify: `internal/tmux/tmux.go:2642`

**Interfaces:**
- Consumes: `tmuxSessionExistsOnSocket(socketName, name string) bool`
- Produces: unchanged `(*Session).Kill() error` behavior with direct post-error liveness verification.

- [ ] **Step 1: Write the failing regression test**

Add a test that primes the default-socket cache with a nonexistent session name, calls `Kill`, and requires a nil result:

```go
func TestKill_NonexistentSessionIgnoresStalePositiveCache(t *testing.T) {
	skipIfNoTmuxBinary(t)
	t.Cleanup(func() {
		sessionCacheMu.Lock()
		sessionCacheData = nil
		sessionCacheTime = time.Time{}
		sessionCacheMu.Unlock()
	})

	const name = "agent-deck-kill-stale-positive-absent"
	sessionCacheMu.Lock()
	sessionCacheData = map[string]int64{name: time.Now().Unix()}
	sessionCacheTime = time.Now()
	sessionCacheMu.Unlock()

	s := NewSession(name, t.TempDir())
	if err := s.Kill(); err != nil {
		t.Fatalf("Kill() trusted a stale positive cache entry: %v", err)
	}
}
```

- [ ] **Step 2: Run the regression test and verify RED**

Run:

```bash
go test ./internal/tmux -run '^TestKill_NonexistentSessionIgnoresStalePositiveCache$' -count=1
```

Expected: FAIL because `Kill` calls cache-aware `Session.Exists()`, which returns true from the primed stale entry and preserves the `kill-session` error.

- [ ] **Step 3: Implement the minimal production fix**

Replace the post-kill `s.Exists()` check with the existing direct socket helper:

```go
if err != nil && !tmuxSessionExistsOnSocket(s.SocketName, s.Name) {
	return nil
}
```

Update the adjacent comment to state that post-kill verification deliberately bypasses the shared cache.

- [ ] **Step 4: Verify GREEN and related behavior**

Run:

```bash
go test ./internal/tmux -run 'TestKill' -count=1
go test ./internal/tmux -count=1
```

Expected: PASS. Existing live-session, repeated-kill, nonexistent-session, and socket behavior remains green.

- [ ] **Step 5: Run repository verification**

Run the repository's standard Go checks:

```bash
make test
make lint
```

Expected: PASS with no new failures. If a pre-existing environment failure appears, isolate it and report exact evidence rather than masking it.

- [ ] **Step 6: Review and commit**

Inspect `git diff --check`, the scoped diff, and `git status`. Fix only concrete archive/kill issues found by tests or review, leave the stylesheet unstaged, then commit:

```bash
git add internal/tmux/kill_idempotent_test.go internal/tmux/tmux.go
git commit -m "fix(tmux): bypass stale cache after session kill"
```
