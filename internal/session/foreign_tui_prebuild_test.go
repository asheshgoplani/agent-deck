package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The background build of the shared agent-deck binary must start whenever a
// -run filter can reach a test that needs it. CI's session shards run with
// -run '^Test[A-C]' and '^Test[D-I]'; before this the build started only
// without any -run, so those two shards paid a cold build inside one test
// (TestCrashedSession_TUIOnItsOwnServerShowsError 77.6 s on CI run
// 43678168178, 14.3 s with the build overlapped).
func TestRunFilterReachesForeignTUIBinaryTest(t *testing.T) {
	for _, c := range []struct {
		pattern string
		want    bool
	}{
		{"", true},
		{"^Test[A-C]", true}, // CI session-a
		{"^Test[D-I]", true}, // CI session-b
		{"^Test[A-H]", true}, // a local split half
		{"^TestP", true},     // TestPiWakeExtension_...
		{"TestCrashedSession_TUIOnItsOwnServerShowsError/own_server", true},
		{"^TestForeignTmuxServer_(Nested|Unlistable)/x", true},
		{"[", true}, // does not compile: build anyway
		{"^TestStatusPassKnownEnvironmentRotation$", false},
		{"^Test[J-O]", false},
		{"^TestQ", false},
		{"^TestCrashedSession_Other$", false},
		{"^TestStatus/TestCrashedSession", false}, // the name after '/' is a subtest filter
	} {
		if got := runFilterReachesForeignTUIBinaryTest(c.pattern); got != c.want {
			t.Errorf("runFilterReachesForeignTUIBinaryTest(%q) = %v, want %v", c.pattern, got, c.want)
		}
	}
}

func TestTopLevelRunPattern(t *testing.T) {
	for in, want := range map[string]string{
		"^TestA$":         "^TestA$",
		"^TestA/sub":      "^TestA",
		"^Test[/]x/sub":   "^Test[/]x",
		"^Test(a/b)/sub":  "^Test(a/b)",
		`^Test\/x/sub`:    `^Test\/x`,
		"/onlysubtests":   "",
		"^Test[A-C]":      "^Test[A-C]",
		"^TestA/sub/deep": "^TestA",
	} {
		if got := topLevelRunPattern(in); got != want {
			t.Errorf("topLevelRunPattern(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every top-level test that calls foreignTUIBinary is listed in
// foreignTUIBinaryTests, and every listed name is such a test, so the -run
// check above never misses one.
func TestForeignTUIBinaryTestsListed(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var callers []string
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil {
				continue
			}
			calls := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "foreignTUIBinary" {
						calls = true
					}
				}
				return !calls
			})
			if !calls {
				continue
			}
			if !strings.HasPrefix(fn.Name.Name, "Test") {
				t.Errorf("%s calls foreignTUIBinary from helper %s; call it from the test itself so foreignTUIBinaryTests can name it", f, fn.Name.Name)
				continue
			}
			callers = append(callers, fn.Name.Name)
		}
	}
	listed := append([]string(nil), foreignTUIBinaryTests...)
	sort.Strings(callers)
	sort.Strings(listed)
	if strings.Join(callers, ",") != strings.Join(listed, ",") {
		t.Errorf("foreignTUIBinaryTests = %v, but the tests calling foreignTUIBinary are %v", listed, callers)
	}
}
