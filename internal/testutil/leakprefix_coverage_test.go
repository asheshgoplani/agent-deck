package testutil_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// ownedPrefixLiteral finds the temp-name prefixes agent-deck's own code asks
// for: os.MkdirTemp patterns and IsolatePackageHome patterns. The trailing "*"
// of an MkdirTemp pattern is not part of the prefix.
var ownedPrefixLiteral = regexp.MustCompile(
	`(?:os\.MkdirTemp\([^,]+,\s*|IsolatePackageHome\()"((?:ad-|agent-deck-)[^"*]*)\*?"`)

// TestLeakPrefixesCoverEveryOwnedPrefix is the guard that keeps the leak
// sentinels from going quietly vacuous.
//
// leakedTempEntries only reports directory names matching leakPrefixes. A
// missing entry therefore does not weaken an assertion — it DELETES it, and the
// sentinel reports a clean run over a real leak, with no signal anywhere that
// it checked nothing.
//
// That is not hypothetical: leakPrefixes was first written for the fixture
// package and listed ad-home-, ad-tmux-, ad-sock- and the fixture's own prefix.
// cmd/agent-deck isolates under agent-deck-cmd-tests-home-* and builds into
// agent-deck-channels-bin-* / agent-deck-eval-bin-*, none of which were listed —
// so TestHelperSubprocessesLeaveNoTempDirs' cmd case could not have detected
// the very leak it was added to catch.
//
// So rather than trusting the list to be maintained by hand, this derives the
// prefixes from the source that creates them and fails when one is unlisted.
func TestLeakPrefixesCoverEveryOwnedPrefix(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate repo root")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	found := map[string]string{} // prefix -> first file that asks for it
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			// .worktrees holds checked-out copies of this same repo; scanning
			// them would attribute another branch's prefixes to this one.
			case ".git", ".claude", ".worktrees", ".planning", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range ownedPrefixLiteral.FindAllStringSubmatch(string(data), -1) {
			prefix := m[1]
			if prefix == "" {
				continue
			}
			if _, seen := found[prefix]; !seen {
				rel, _ := filepath.Rel(repoRoot, path)
				found[prefix] = rel
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo root %q: %v", repoRoot, err)
	}

	if len(found) == 0 {
		t.Fatal("found no owned temp prefixes in the repo — the scanner is broken, " +
			"which would make this guard pass vacuously")
	}

	listed := map[string]bool{}
	for _, p := range leakPrefixes {
		listed[p] = true
	}

	var missing []string
	for prefix, file := range found {
		if !listed[prefix] {
			missing = append(missing, prefix+"  (created in "+file+")")
		}
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Fatalf("these temp prefixes are created by agent-deck's own code but are NOT in "+
			"leakPrefixes, so every leak sentinel is blind to them:\n  - %s\n\n"+
			"Add each to leakPrefixes in templeak_e2e_test.go. An unlisted prefix does "+
			"not weaken the sentinels — it silently removes the assertion.",
			strings.Join(missing, "\n  - "))
	}
}

// TestLeakedTempEntriesReportsEveryListedPrefix proves the detector actually
// fires for each listed prefix, so a prefix cannot be present in the list yet
// unmatched because of a typo or a naming mismatch.
func TestLeakedTempEntriesReportsEveryListedPrefix(t *testing.T) {
	for _, prefix := range leakPrefixes {
		t.Run(prefix, func(t *testing.T) {
			dir := t.TempDir()
			planted := prefix + "1234567"
			if err := os.MkdirAll(filepath.Join(dir, planted), 0o755); err != nil {
				t.Fatalf("plant %s: %v", planted, err)
			}
			// A foreign dir must not be reported.
			if err := os.MkdirAll(filepath.Join(dir, "some-other-tool-99"), 0o755); err != nil {
				t.Fatalf("plant foreign: %v", err)
			}

			leaked := leakedTempEntries(t, dir)
			if len(leaked) != 1 || leaked[0] != planted {
				t.Fatalf("leakedTempEntries = %v, want exactly [%s]", leaked, planted)
			}
		})
	}
}
