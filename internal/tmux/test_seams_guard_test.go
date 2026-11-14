package tmux

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// crossPackageTestSeams lists the exported *ForTest helpers that production
// files in this package still declare because tests in other packages
// (internal/session, internal/ui) call them, and an export_test.go file is
// only visible to this package's own tests. Do not add to it: a helper only
// this package's tests use belongs in export_test.go (issue #2986 item 6).
var crossPackageTestSeams = map[string]bool{
	"ExpirePaneInfoCacheForTest":     true,
	"ExpireStartupWindowForTest":     true,
	"ExpireToolDetectionForTest":     true,
	"ResetSocketSessionCacheForTest": true,
	"ResetVersionWarningOnceForTest": true,
	"ResetViewersCacheForTest":       true,
	"SeedPaneInfoCacheForTest":       true,
	"SeedSessionCacheForTest":        true,
	"SeedViewersCacheForTest":        true,
	"SeedWindowCacheForTest":         true,
	"SetServerAliveForTest":          true,
	"SetStartupAtForTest":            true,
	"StaleSocketForTest":             true,
}

// TestNoNewExportedTestSeamsInProductionFiles parses every non-test .go file
// in this package and fails on an exported identifier ending in "ForTest"
// that is not in crossPackageTestSeams.
func TestNoNewExportedTestSeamsInProductionFiles(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	fset := token.NewFileSet()
	var found []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		check := func(name *ast.Ident) {
			if name.IsExported() && strings.HasSuffix(name.Name, "ForTest") && !crossPackageTestSeams[name.Name] {
				found = append(found, f+": "+name.Name)
			}
		}
		for _, decl := range parsed.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				check(d.Name)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.ValueSpec:
						for _, n := range s.Names {
							check(n)
						}
					case *ast.TypeSpec:
						check(s.Name)
					}
				}
			}
		}
	}
	sort.Strings(found)
	if len(found) > 0 {
		t.Fatalf("production files export test-only seams; move them to export_test.go:\n%s", strings.Join(found, "\n"))
	}
}
