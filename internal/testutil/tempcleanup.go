package testutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// StaleTempDirAge is how old an abandoned temp dir must be before
// ReapStaleTempDirs will remove it. It is deliberately far longer than any
// plausible `go test` run (the repo's suites use -timeout 10m) so the janitor
// can never delete a directory a concurrently running test binary still owns.
const StaleTempDirAge = 24 * time.Hour

// noTempReapEnv disables the janitor entirely. Set it when auditing leftovers
// by hand, so an inspection run does not destroy the evidence it is measuring.
const noTempReapEnv = "AGENT_DECK_TEST_NO_TEMP_REAP"

// ownedTempNamespaces are agent-deck's temp-name namespaces. A pattern must
// start with one AND extend it strictly, so the janitor can never be pointed at
// "*", "tmp-*", or the go toolchain's scratch dirs — and equally never at a bare
// namespace like "agent-deck-", which would sweep every agent-deck temp dir
// including production ones (agent-deck-mergeback-* holds a live git worktree).
// "ad-home-" and "agent-deck-cmd-tests-home-" both qualify; "ad-" and
// "agent-deck-" alone do not.
var ownedTempNamespaces = []string{"ad-", "agent-deck-"}

// RemoveTempTree deletes a temp directory tree created by a test helper,
// including content the Go toolchain deliberately makes read-only.
//
// WHY os.RemoveAll IS NOT ENOUGH (2026-08-10 temp-leak incident, ~130 GB):
//
// Test isolation points HOME at a temp dir. Any test that shells out to `go
// build`/`go test` (cmd/agent-deck builds its own CLI; the eval harness builds
// the binary under test) hands that redirected HOME to the go command, which
// then resolves GOMODCACHE to <temp-home>/go/pkg/mod and materialises a fresh
// module cache there — ~1.5 GB per run. The go command writes module cache
// files at 0444 inside directories at 0555, on purpose, so a stale build can
// never mutate a verified module.
//
// Unlinking a file requires write permission on its PARENT directory, so those
// 0555 directories make os.RemoveAll fail with EACCES on the first module it
// reaches. The pre-fix cleanup wrote `_ = os.RemoveAll(dir)`, discarding that
// error, so every run silently stranded its entire temp HOME.
//
// RemoveTempTree tries the cheap path first, then walks the tree making every
// directory writable and searchable and retries. WalkDir visits a directory
// before reading its children, so chmod-then-descend also recovers dirs whose
// mode denies traversal outright.
//
// Errors are RETURNED, never swallowed. Callers that cannot fail a test (a
// TestMain cleanup running after m.Run) must still report them — see
// reportTempTreeRemoval.
func RemoveTempTree(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.RemoveAll(dir); err == nil {
		return nil
	}

	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Could not stat or read this entry. Chmod-ing its parent may have
			// already fixed the underlying cause, and any residue surfaces as a
			// real error from the RemoveAll below, so keep walking rather than
			// aborting the whole tree on one bad entry.
			return nil
		}
		if d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove temp tree %s: %w", dir, err)
	}
	return nil
}

// reportTempTreeRemoval removes dir and, on failure, writes a diagnostic to
// stderr naming the leaked path. Cleanup runs after m.Run() where there is no
// *testing.T left to fail, but a silent failure is exactly what let 130 GB
// accumulate unnoticed — so it must at least be loud.
func reportTempTreeRemoval(what, dir string) {
	if err := RemoveTempTree(dir); err != nil {
		fmt.Fprintf(os.Stderr, "testutil: LEAKED %s temp dir: %v\n", what, err)
	}
}

// ReapStaleTempDirs removes abandoned temp directories left by test runs that
// were killed or crashed before their cleanup could run. It returns how many
// entries it removed.
//
// The fixed cleanup path handles every NORMAL exit; this janitor exists only
// for the abnormal ones, which is why it is deliberately timid:
//
//   - base must be an explicit directory (the caller's os.TempDir()), never a
//     recursive sweep.
//   - pattern's literal prefix must strictly extend one of ownedTempNamespaces,
//     so only agent-deck's own namespace is in scope and a bare namespace is
//     itself refused. Anything else is a no-op, not a best-effort guess.
//   - only directories are considered; files sharing the prefix are left alone.
//   - only entries whose modification time is older than maxAge, so a
//     concurrently running test binary's live dir is never touched.
//   - names must match the prefix followed by MkdirTemp's random suffix, so
//     "agent-deck-reap-testing-else" is not swept by "agent-deck-reap-test-*".
//
// Set AGENT_DECK_TEST_NO_TEMP_REAP=1 to disable it while auditing leftovers.
func ReapStaleTempDirs(base, pattern string, maxAge time.Duration) int {
	if os.Getenv(noTempReapEnv) != "" {
		return 0
	}
	prefix, ok := ownedTempPrefix(pattern)
	if !ok {
		return 0
	}

	entries, err := os.ReadDir(base)
	if err != nil {
		return 0
	}

	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		// MkdirTemp appends a random decimal suffix. Requiring the remainder to
		// be all digits keeps a longer, differently-named prefix out of scope.
		if !isMkdirTempSuffix(name[len(prefix):]) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := RemoveTempTree(filepath.Join(base, name)); err == nil {
			removed++
		}
	}
	return removed
}

// ownedTempPrefix extracts the literal prefix from an os.MkdirTemp pattern
// (the part before the "*", or the whole string when there is none) and reports
// whether it strictly extends one of agent-deck's temp namespaces.
func ownedTempPrefix(pattern string) (string, bool) {
	prefix := pattern
	if i := strings.IndexByte(pattern, '*'); i >= 0 {
		prefix = pattern[:i]
	}
	for _, ns := range ownedTempNamespaces {
		if len(prefix) > len(ns) && strings.HasPrefix(prefix, ns) {
			return prefix, true
		}
	}
	return "", false
}

// isMkdirTempSuffix reports whether s is the random decimal suffix os.MkdirTemp
// appends. An empty or non-numeric remainder means the entry belongs to some
// other, longer prefix and must not be reaped.
func isMkdirTempSuffix(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
