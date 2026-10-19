package harness

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const harnessImportPath = "github.com/asheshgoplani/agent-deck/tests/eval/harness"

// TestEveryNewSandboxPackageRemovesBuildArtifacts guards the leak that
// RemoveBuildArtifacts exists for. NewSandbox builds agent-deck once per test
// binary into an agent-deck-eval-bin-* temp dir (~15 MB) that outlives every
// test, so only the package's TestMain can remove it. A test package that calls
// harness.NewSandbox but whose TestMain does not call
// harness.RemoveBuildArtifacts leaves that dir in TMPDIR on every run
// (cmd/agent-deck did, until this test).
//
// The check is per test binary: a directory's in-package and external (_test)
// files link into one binary, so a TestMain in either covers both. Build tags
// are ignored on purpose; a tagged package leaks just the same when its tag is
// set.
func TestEveryNewSandboxPackageRemovesBuildArtifacts(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}

	type pkgInfo struct {
		sandboxUser string // first file calling harness.NewSandbox
		testMain    string // file declaring TestMain ("" if none)
		removes     bool   // that TestMain calls harness.RemoveBuildArtifacts
	}
	pkgs := map[string]*pkgInfo{} // keyed by directory
	fset := token.NewFileSet()

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		alias := harnessAlias(f)
		if alias == "" {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		dir := filepath.Dir(rel)
		if filepath.ToSlash(dir) == "tests/eval/harness" {
			return nil // the harness's own tests never call NewSandbox
		}
		p := pkgs[dir]
		if p == nil {
			p = &pkgInfo{}
			pkgs[dir] = p
		}
		if p.sandboxUser == "" && callsSelector(f, alias, "NewSandbox") {
			p.sandboxUser = rel
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" {
				continue
			}
			p.testMain = rel
			// TestMain may delegate to a helper (cmd/agent-deck uses
			// runTestMain), so accept the call anywhere in the file.
			if callsSelector(f, alias, "RemoveBuildArtifacts") {
				p.removes = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	dirs := make([]string, 0, len(pkgs))
	for dir := range pkgs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	checked := 0
	for _, dir := range dirs {
		p := pkgs[dir]
		if p.sandboxUser == "" {
			continue
		}
		checked++
		switch {
		case p.testMain == "":
			t.Errorf("%s: %s calls harness.NewSandbox but the package has no TestMain calling harness.RemoveBuildArtifacts after m.Run (agent-deck-eval-bin-* leaks per run)", dir, p.sandboxUser)
		case !p.removes:
			t.Errorf("%s: TestMain in %s does not call harness.RemoveBuildArtifacts after m.Run, but %s calls harness.NewSandbox (agent-deck-eval-bin-* leaks per run)", dir, p.testMain, p.sandboxUser)
		}
	}
	if checked == 0 {
		t.Fatal("found no package calling harness.NewSandbox; the scan is broken")
	}
}

// harnessAlias returns the name the file uses for the harness package, or ""
// when the file does not import it.
func harnessAlias(f *ast.File) string {
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != harnessImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "harness"
	}
	return ""
}

// callsSelector reports whether f contains a call <pkg>.<name>(...).
func callsSelector(f *ast.File, pkg, name string) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != name {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == pkg {
			found = true
		}
		return true
	})
	return found
}
