package testutil_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestAllTestMainsIsolateTmuxSocket is the lint that prevents regression of the
// 2026-04-17 incident, where `go test ./...` on a live conductor host killed
// every managed user session because 7 of 10 test packages spawned tmux on the
// shared default socket.
//
// Any testmain_test.go that defines TestMain MUST call testutil.IsolateTmuxSocket()
// before m.Run(). This test walks the whole repo and fails if any file forgets.
//
// See internal/testutil/tmuxenv.go for the full postmortem and copy-paste pattern.
func TestAllTestMainsIsolateTmuxSocket(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate repo root")
	}
	// thisFile = <repo>/internal/testutil/testmain_audit_test.go
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	testMainRe := regexp.MustCompile(`(?m)^func TestMain\s*\(`)
	isolateRe := regexp.MustCompile(`IsolateTmuxSocket`)
	isolateHomeRe := regexp.MustCompile(`IsolateHome`)

	var offenders []string
	var homeOffenders []string
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			// Skip anything that would duplicate testmain files from checked-out
			// worktrees, vendored deps, or planning metadata.
			switch name {
			case ".git", ".claude", ".worktrees", ".planning", "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "testmain_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		content := string(data)
		if !testMainRe.MatchString(content) {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		if !isolateRe.MatchString(content) {
			offenders = append(offenders, rel)
		}
		if !isolateHomeRe.MatchString(content) {
			homeOffenders = append(homeOffenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo root %q: %v", repoRoot, err)
	}

	if len(offenders) > 0 {
		t.Fatalf(
			"The following TestMain files are missing a call to testutil.IsolateTmuxSocket(). "+
				"Without it, `go test ./...` on a host running agent-deck will spawn tmux "+
				"sessions on the user's default socket and destabilize live sessions "+
				"(2026-04-17 incident — PR #623 completion).\n\n"+
				"Offending files:\n  - %s\n\n"+
				"Fix: copy the pattern from internal/tmux/testmain_test.go. "+
				"See internal/testutil/tmuxenv.go for the postmortem.",
			strings.Join(offenders, "\n  - "),
		)
	}

	if len(homeOffenders) > 0 {
		t.Fatalf(
			"The following TestMain files are missing a call to testutil.IsolateHome(). "+
				"Without it, `go test` resolves agent-deck runtime paths via the real "+
				"$HOME and can WIPE the live ~/.agent-deck (config.json, profile index, "+
				"worker-scratch, logs) — the 2026-06-04 data-loss incident (S5 safeguard).\n\n"+
				"Offending files:\n  - %s\n\n"+
				"Fix: add `cleanupHome := testutil.IsolateHome(); defer cleanupHome()` at "+
				"the top of TestMain. See internal/testutil/homeenv.go for the postmortem.",
			strings.Join(homeOffenders, "\n  - "),
		)
	}
}
