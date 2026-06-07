package testutil_test

import (
	"go/ast"
	"go/parser"
	"go/token"
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

// TestNoTestMainLeaksCleanupBehindOsExit guards the 2026-06-07 pty-exhaustion
// incident. A package TestMain that registers `defer cleanup()` (e.g.
// bootstrapTmuxServer's kill-server, IsolateTmuxSocket's RemoveAll, IsolateHome's
// restore) and then ends with os.Exit(code) leaks on every run: os.Exit does NOT
// run deferred functions, so the bootstrap tmux server (holding a pty) and the
// per-run ad-tmux-*/home temp dirs survive the test binary. Accumulated across
// runs this exhausted the macOS pty pool (kern.tty.ptmx_max=511) and denied new
// terminals.
//
// This audit parses every *_test.go, finds the TestMain FuncDecl, and fails when
// its OWN body contains both a defer statement and an os.Exit call — the
// defer-never-runs anti-pattern. The required fix moves the defers into a
// separate function so they actually run:
//
//	func TestMain(m *testing.M) { os.Exit(runTestMain(m)) }
//	func runTestMain(m *testing.M) int {
//	    cleanup := testutil.IsolateTmuxSocket(); defer cleanup()
//	    return m.Run()
//	}
//
// After that refactor TestMain's body holds only os.Exit(runTestMain(m)) with no
// defer, so it passes.
func TestNoTestMainLeaksCleanupBehindOsExit(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed — cannot locate repo root")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	var offenders []string
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", ".worktrees", ".planning", "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		// Cheap prefilter: only the handful of files that define TestMain need
		// AST parsing. This keeps full-repo coverage (TestMain also lives in
		// tests/ and in non-testmain_test.go files like experiments_test.go and
		// guard_test.go, so we can't restrict by directory or filename) while
		// avoiding the cost of parsing every *_test.go in the repo.
		if !strings.Contains(string(data), "func TestMain") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, data, 0)
		if parseErr != nil {
			return parseErr
		}
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}
			if testMainBodyDefersBehindOsExit(fn.Body) {
				rel, _ := filepath.Rel(repoRoot, path)
				offenders = append(offenders, rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo root %q: %v", repoRoot, err)
	}

	if len(offenders) > 0 {
		t.Fatalf(
			"The following TestMain functions register a `defer` AND call os.Exit() in the "+
				"same body. os.Exit does not run deferred functions, so cleanups (tmux "+
				"kill-server, RemoveAll of the isolated TMUX_TMPDIR, HOME restore) NEVER run "+
				"— leaking a tmux server (1 pty) and temp dirs on every test run. This is the "+
				"2026-06-07 macOS pty-exhaustion incident.\n\n"+
				"Offending files:\n  - %s\n\n"+
				"Fix: move setup+defers into `func runTestMain(m *testing.M) int { ... return "+
				"m.Run() }` and make TestMain `os.Exit(runTestMain(m))`. See the pattern in "+
				"internal/tmux/testmain_test.go.",
			strings.Join(offenders, "\n  - "),
		)
	}
}

// testMainBodyDefersBehindOsExit reports whether a TestMain body contains both a
// defer statement and a call to os.Exit(...). Either alone is fine; together they
// are the leak anti-pattern, because os.Exit skips deferred functions. Nested
// function literals are ignored — a defer inside a closure is unrelated to the
// TestMain-level exit path.
func testMainBodyDefersBehindOsExit(body *ast.BlockStmt) bool {
	var hasDefer, hasOsExit bool
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncLit:
			// Don't descend into closures; their defers run when the closure
			// returns, independent of os.Exit.
			return false
		case *ast.DeferStmt:
			hasDefer = true
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" && sel.Sel.Name == "Exit" {
					hasOsExit = true
				}
			}
		}
		return true
	})
	return hasDefer && hasOsExit
}
