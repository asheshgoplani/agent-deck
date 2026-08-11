package testutil_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestNoUncleanedMkdirTempInTests is the lint that prevents regression of the
// 2026-08-10 temp-leak incident, where ~130 GB of $TMPDIR was stranded across
// 1278 directories on one developer machine.
//
// The leaks were not exotic. Each was an os.MkdirTemp call in a test file whose
// result nothing ever removed:
//
//   - cmd/agent-deck and internal/session each had a private isolatePackageHome
//     that created a temp HOME and returned nothing to clean it up (~1.5 GB per
//     run each, because both packages shell out to `go build` and the go command
//     then materialises a module+build cache inside the redirected HOME).
//   - channels_cmd_test.go built the CLI into a temp dir with no owner.
//
// Rule: in any *_test.go, every identifier assigned from os.MkdirTemp must be
// passed to a removal call (os.RemoveAll or testutil.RemoveTempTree) somewhere
// in the SAME FILE. File scope, not function scope, is deliberate — a build dir
// that must outlive individual tests is legitimately removed by a sibling
// helper that TestMain calls (see removeChannelsCLIBinDir), and that pattern
// must stay expressible.
//
// os.MkdirTemp in NON-test files is out of scope: production temp dirs have
// their own lifecycles and are covered by the packages that own them.
func TestNoUncleanedMkdirTempInTests(t *testing.T) {
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
			// .worktrees holds checked-out copies of this same repo; scanning
			// them would report another branch's files as this branch's bugs.
			case ".git", ".claude", ".worktrees", ".planning", "vendor", "node_modules":
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
		if !strings.Contains(string(data), "os.MkdirTemp") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, data, 0)
		if parseErr != nil {
			return parseErr
		}
		removed := removalTargets(file)
		for _, name := range mkdirTempTargets(file) {
			if removed[name] {
				continue
			}
			rel, _ := filepath.Rel(repoRoot, path)
			offenders = append(offenders, rel+": "+name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo root %q: %v", repoRoot, err)
	}

	if len(offenders) > 0 {
		t.Fatalf(
			"The following test files call os.MkdirTemp and never remove the directory. "+
				"macOS does not reap $TMPDIR between runs, so each `go test` invocation "+
				"strands the tree forever — and when the temp dir is used as HOME, a "+
				"subprocess `go build` fills it with a ~1.5 GB Go module+build cache "+
				"(2026-08-10 temp-leak incident, ~130 GB across 1278 directories).\n\n"+
				"Offending sites:\n  - %s\n\n"+
				"Fix: for a per-test dir use t.TempDir(). For a dir that must outlive "+
				"individual tests, remove it from TestMain after m.Run() via "+
				"testutil.RemoveTempTree — plain os.RemoveAll cannot delete a read-only "+
				"Go module cache. For an isolated package HOME use "+
				"testutil.IsolatePackageHome, which owns its own cleanup.",
			strings.Join(offenders, "\n  - "),
		)
	}
}

// mkdirTempTargets returns the identifiers assigned the directory result of an
// os.MkdirTemp call. It covers both `d, err := os.MkdirTemp(...)` and the
// single-value `d = os.MkdirTemp(...)` retry form. "_" is skipped: discarding
// the path is its own, louder bug and not what this lint is measuring.
func mkdirTempTargets(file *ast.File) []string {
	var names []string
	ast.Inspect(file, func(n ast.Node) bool {
		var lhs []ast.Expr
		var rhs []ast.Expr
		switch node := n.(type) {
		case *ast.AssignStmt:
			lhs, rhs = node.Lhs, node.Rhs
		default:
			return true
		}
		if len(rhs) != 1 || len(lhs) == 0 {
			return true
		}
		if !isMkdirTempCall(rhs[0]) {
			return true
		}
		if ident, ok := lhs[0].(*ast.Ident); ok && ident.Name != "_" {
			names = append(names, ident.Name)
		}
		return true
	})
	return names
}

func isMkdirTempCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "MkdirTemp" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "os"
}

// removalTargets returns every identifier passed to a removal call anywhere in
// the file. It accepts the identifier used directly (RemoveAll(dir)) or as the
// base of a path expression (RemoveAll(filepath.Dir(dir))), which is how a
// helper that appends a subdirectory releases the tree it owns.
func removalTargets(file *ast.File) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "RemoveAll", "RemoveTempTree":
		default:
			return true
		}
		ast.Inspect(call.Args[0], func(inner ast.Node) bool {
			if ident, ok := inner.(*ast.Ident); ok {
				out[ident.Name] = true
			}
			return true
		})
		return true
	})
	return out
}
