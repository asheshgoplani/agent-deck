package agentpaths

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// projectScopedNames are directory names whose contents belong to one
// repository. They must resolve through session.ProjectDataPath (which lands
// them in <project-root>/.agent-deck/), never through the machine-global data
// dir.
//
// The distinction is ownership, not size. state.db, logs, sockets, and
// conductor state describe this machine and are global by nature. A handoff
// prompt, a design doc, an orchestrate run, and its evidence describe one
// project: pooled globally by session id they become an unattributable pile
// that outlives the project it came from and cannot travel with it.
var projectScopedNames = map[string]string{
	"handoff":     "session.EnsureHandoffDir / session.HandoffPromptPath",
	"orchestrate": "session.ProjectDataPath",
	"evidence":    "session.ProjectDataPath",
	"design":      "session.ProjectDataPath",
	"plan":        "session.ProjectDataPath",
}

// globalPathResolvers are the functions that hand back a machine-global path.
// Feeding a project-scoped name into any of them is the exact regression this
// test exists to catch.
var globalPathResolvers = map[string]bool{
	"EffectiveDataPath": true,
	"EffectiveDataDir":  true,
	"LegacyDir":         true,
	"DataDir":           true,
	// internal/session's package-local wrappers around the above.
	"dataPath":        true,
	"runtimeDataPath": true,
	"logDataPath":     true,
}

func TestNoProjectScopedNamesUnderGlobalDataDir(t *testing.T) {
	root := findModuleRoot(t)
	fset := token.NewFileSet()
	var failures []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".worktrees", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isGlobalPathResolver(call) {
				return true
			}
			for _, arg := range call.Args {
				for _, literal := range stringLiteralsIn(arg) {
					want, scoped := projectScopedNames[literal]
					if !scoped {
						continue
					}
					pos := fset.Position(arg.Pos())
					failures = append(failures, pos.String()+": "+literal+" is project-scoped; use "+want)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk production Go files: %v", err)
	}

	if len(failures) > 0 {
		t.Fatalf("project-scoped artifacts resolved under the machine-global data dir:\n%s",
			strings.Join(failures, "\n"))
	}
}

// A lint that matches nothing passes forever and protects nothing. Feed the
// matcher the exact call shape that was wrong before this rule existed, plus
// the shape that is now right, and require it to tell them apart.
func TestProjectScopedLintMatchesTheRegressionItGuards(t *testing.T) {
	const src = `package p

import (
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

func regressed(id string) (string, error) {
	return agentpaths.EffectiveDataPath(filepath.Join("handoff", id), "handoff")
}

func fine(id string) (string, error) {
	return agentpaths.EffectiveDataPath(filepath.Join("inboxes", id), "inboxes")
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "lint_fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	var flagged []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || !isGlobalPathResolver(call) {
			return true
		}
		for _, arg := range call.Args {
			for _, literal := range stringLiteralsIn(arg) {
				if _, scoped := projectScopedNames[literal]; scoped {
					flagged = append(flagged, literal)
				}
			}
		}
		return true
	})

	// "handoff" appears twice in the regressed call: inside the Join and as
	// the legacy marker argument. Both are the same mistake.
	if len(flagged) == 0 {
		t.Fatal("lint did not flag a project-scoped name passed to a global resolver; it would never catch the regression")
	}
	for _, name := range flagged {
		if name != "handoff" {
			t.Errorf("lint flagged %q; only the project-scoped call should match", name)
		}
	}
}

// isGlobalPathResolver matches both agentpaths.EffectiveDataPath(...) and the
// bare-identifier package-local wrappers such as dataPath(...).
func isGlobalPathResolver(call *ast.CallExpr) bool {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return globalPathResolvers[fun.Sel.Name]
	case *ast.Ident:
		return globalPathResolvers[fun.Name]
	}
	return false
}

// stringLiteralsIn collects string literals from an argument, descending into
// filepath.Join(...) so a nested Join("handoff", id) is caught too.
func stringLiteralsIn(expr ast.Expr) []string {
	if literal := stringLiteralValue(expr); literal != "" {
		return []string{literal}
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil
	}
	var out []string
	for _, arg := range call.Args {
		out = append(out, stringLiteralsIn(arg)...)
	}
	return out
}
