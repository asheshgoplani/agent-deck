package agentpaths

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

var allowedLegacyPathFiles = map[string]bool{
	"internal/agentpaths/paths.go":   true,
	"internal/agentpaths/migrate.go": true,
}

func TestNoDirectHomeAgentDeckPathConstruction(t *testing.T) {
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

		rel := slashRelPath(t, root, path)
		if allowedLegacyPathFiles[rel] {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		filepathAliases := importedFilepathAliases(file)
		if len(filepathAliases) == 0 {
			return nil
		}

		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isFilepathJoin(call, filepathAliases) {
				return true
			}
			for idx, arg := range call.Args {
				if stringLiteralValue(arg) == ".agent-deck" {
					if !hasLegacyHomeRoot(call.Args[:idx]) {
						continue
					}
					pos := fset.Position(arg.Pos())
					failures = append(failures, pos.String())
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
		t.Fatalf("direct filepath.Join(..., %q, ...) legacy path construction found outside agentpaths:\n%s",
			".agent-deck", strings.Join(failures, "\n"))
	}
}

func findModuleRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod above %s", file)
		}
		dir = parent
	}
}

func slashRelPath(t *testing.T, root, path string) string {
	t.Helper()

	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("rel path for %q: %v", path, err)
	}
	return filepath.ToSlash(rel)
}

func importedFilepathAliases(file *ast.File) map[string]bool {
	aliases := make(map[string]bool)
	for _, imp := range file.Imports {
		if stringLiteralValue(imp.Path) != "path/filepath" {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name != "_" && imp.Name.Name != "." {
				aliases[imp.Name.Name] = true
			}
			continue
		}
		aliases["filepath"] = true
	}
	return aliases
}

func isFilepathJoin(call *ast.CallExpr, filepathAliases map[string]bool) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Join" {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && filepathAliases[ident.Name]
}

func hasLegacyHomeRoot(args []ast.Expr) bool {
	for _, arg := range args {
		if isLegacyHomeRoot(arg) {
			return true
		}
	}
	return false
}

func isLegacyHomeRoot(expr ast.Expr) bool {
	switch value := expr.(type) {
	case *ast.Ident:
		name := strings.ToLower(value.Name)
		return strings.Contains(name, "home") || strings.Contains(name, "tmp")
	case *ast.CallExpr:
		return isOSCall(value, "TempDir") || isOSGetenvHome(value)
	}
	return false
}

func isOSCall(call *ast.CallExpr, method string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != method {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == "os"
}

func isOSGetenvHome(call *ast.CallExpr) bool {
	if !isOSCall(call, "Getenv") || len(call.Args) != 1 {
		return false
	}
	return stringLiteralValue(call.Args[0]) == "HOME"
}

func stringLiteralValue(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return value
}
