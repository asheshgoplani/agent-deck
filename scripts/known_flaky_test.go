package scripts

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// tests/known-flaky.txt labels failing tests "known flake" in the go-test
// job summary (scripts/ci-test-summary.py). A wrong entry fails silently: a
// misspelled, renamed or deleted test, a subtest path, or a module-path
// package never matches, so a real flake shows up as "new failure" and a
// stale entry hides nothing but also never goes away. These tests keep the
// list honest: it parses the way the summary script parses it, and every
// entry names a top-level test that still exists.

const knownFlakyPath = "tests/known-flaky.txt"

// knownFlakyEntry is one parsed line. Pkg is "" for a bare test name,
// otherwise the "./"-prefixed relative package, exactly as
// ci-test-summary.py's load_known_flaky stores it.
type knownFlakyEntry struct {
	Pkg  string
	Test string
	Line int
}

// topLevelTestName is what is_known_flaky compares against: the part of a
// go test name before the first "/".
var topLevelTestName = regexp.MustCompile(`^Test[A-Za-z0-9_]*$`)

// parseKnownFlaky mirrors load_known_flaky: drop everything after "#", trim,
// skip blank lines, one field is a bare test name, two fields are
// "<package> <TestName>" with the package normalised to "./pkg". It also
// reports lines the script would accept but could never match.
func parseKnownFlaky(text string) ([]knownFlakyEntry, []string) {
	var entries []knownFlakyEntry
	var problems []string
	for i, raw := range strings.Split(text, "\n") {
		n := i + 1
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		var e knownFlakyEntry
		switch len(parts) {
		case 1:
			e = knownFlakyEntry{Test: parts[0], Line: n}
		case 2:
			pkg := parts[0]
			if !strings.HasPrefix(pkg, "./") && pkg != "." {
				pkg = "./" + pkg
			}
			e = knownFlakyEntry{Pkg: pkg, Test: parts[1], Line: n}
		default:
			problems = append(problems, fmt.Sprintf("line %d: %q has %d fields; want \"TestName\" or \"<package> TestName\" (the summary only reads the first two)", n, line, len(parts)))
			continue
		}
		if !topLevelTestName.MatchString(e.Test) {
			problems = append(problems, fmt.Sprintf("line %d: %q is not a top-level test name; the summary matches only the part before the first \"/\"", n, e.Test))
			continue
		}
		entries = append(entries, e)
	}
	return entries, problems
}

// testFuncsInDir returns the top-level Test functions declared in the
// _test.go files directly inside dir.
func testFuncsInDir(dir string) (map[string]bool, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	funcs := map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				funcs[fn.Name.Name] = true
			}
		}
	}
	return funcs, nil
}

// testFuncsInRepo returns every top-level Test function in the module,
// skipping hidden directories, testdata, vendor and node_modules.
func testFuncsInRepo(repoRoot string) (map[string]bool, error) {
	all := map[string]bool{}
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != repoRoot && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules") {
			return filepath.SkipDir
		}
		funcs, err := testFuncsInDir(path)
		if err != nil {
			return err
		}
		for f := range funcs {
			all[f] = true
		}
		return nil
	})
	return all, err
}

// knownFlakyProblems parses repoRoot/tests/known-flaky.txt and reports
// every entry that is malformed or names a test that does not exist.
func knownFlakyProblems(repoRoot string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, knownFlakyPath))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", knownFlakyPath, err)
	}
	entries, problems := parseKnownFlaky(string(data))

	var repoFuncs map[string]bool
	for _, e := range entries {
		if e.Pkg == "" {
			if repoFuncs == nil {
				if repoFuncs, err = testFuncsInRepo(repoRoot); err != nil {
					return nil, err
				}
			}
			if !repoFuncs[e.Test] {
				problems = append(problems, fmt.Sprintf("line %d: no package declares %s", e.Line, e.Test))
			}
			continue
		}
		dir := filepath.Join(repoRoot, filepath.FromSlash(e.Pkg))
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			problems = append(problems, fmt.Sprintf("line %d: package %s is not a directory in this module (use the path relative to the repo root, e.g. internal/session)", e.Line, e.Pkg))
			continue
		}
		funcs, err := testFuncsInDir(dir)
		if err != nil {
			return nil, err
		}
		if !funcs[e.Test] {
			problems = append(problems, fmt.Sprintf("line %d: %s declares no %s", e.Line, e.Pkg, e.Test))
		}
	}
	return problems, nil
}

func TestKnownFlakyListNamesLiveTests(t *testing.T) {
	problems, err := knownFlakyProblems(filepath.Clean(".."))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Fatalf("%s has entries the go-test summary can never match; fix or remove them:\n  %s",
			knownFlakyPath, strings.Join(problems, "\n  "))
	}
}

// writeFixture writes files (path -> contents) under a new temp dir.
func writeFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestKnownFlakyProblemsCatchesRot(t *testing.T) {
	pkgA := "package a\n\nimport \"testing\"\n\nfunc TestAlive(t *testing.T) {}\n\ntype s struct{}\n\nfunc (s) TestMethod(t *testing.T) {}\n"
	pkgB := "package b\n\nimport \"testing\"\n\nfunc TestOther(t *testing.T) {}\n"
	cases := []struct {
		name string
		list string
		want []string // substrings, one per expected problem, in order
	}{
		{"valid scoped, bare and comments", "# header\n\ninternal/a TestAlive  # trailing note\n./internal/b TestOther\nTestOther\n", nil},
		{"renamed or deleted test", "internal/a TestGone\n", []string{"line 1: ./internal/a declares no TestGone"}},
		{"test lives in another package", "internal/b TestAlive\n", []string{"./internal/b declares no TestAlive"}},
		{"bare name nowhere in the module", "TestGone\n", []string{"no package declares TestGone"}},
		{"method is not a test", "internal/a TestMethod\n", []string{"declares no TestMethod"}},
		{"subtest path", "internal/a TestAlive/case\n", []string{"not a top-level test name"}},
		{"module path instead of relative package", "github.com/x/mod/internal/a TestAlive\n", []string{"is not a directory in this module"}},
		{"three fields", "internal/a TestAlive TestOther\n", []string{"has 3 fields"}},
		{"not a test name", "internal/a Alive\n", []string{"not a top-level test name"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := writeFixture(t, map[string]string{
				"internal/a/a_test.go": pkgA,
				"internal/b/b_test.go": pkgB,
				knownFlakyPath:         tc.list,
			})
			got, err := knownFlakyProblems(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d problems %q, want %d matching %q", len(got), got, len(tc.want), tc.want)
			}
			for i, w := range tc.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("problem %d = %q, want it to contain %q", i, got[i], w)
				}
			}
		})
	}
}

// TestKnownFlakyParseMatchesSummaryScript runs the real load_known_flaky
// from scripts/ci-test-summary.py on the repo's list plus a fixture that
// covers every format rule, and requires the same (package, test) set as
// parseKnownFlaky. If the script's format changes, this check must change
// with it instead of validating a format nobody reads.
func TestKnownFlakyParseMatchesSummaryScript(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	repoList, err := os.ReadFile(filepath.Join("..", knownFlakyPath))
	if err != nil {
		t.Fatal(err)
	}
	fixture := "# comment\n\nTestBare\ninternal/tui TestScoped # note\n./internal/web TestDotted\n. TestRoot\n  \tinternal/x\tTestTabs  \n"
	for name, list := range map[string]string{"repo list": string(repoList), "format fixture": fixture} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "known-flaky.txt")
			if err := os.WriteFile(path, []byte(list), 0o644); err != nil {
				t.Fatal(err)
			}
			const loader = `import importlib.util, json, sys
spec = importlib.util.spec_from_file_location("summary", sys.argv[1])
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
print(json.dumps(sorted(list(e) for e in mod.load_known_flaky(sys.argv[2]))))`
			out, err := exec.Command(py, "-I", "-c", loader, "ci-test-summary.py", path).Output()
			if err != nil {
				t.Fatalf("run load_known_flaky: %v", err)
			}
			var fromScript [][]string
			if err := json.Unmarshal(out, &fromScript); err != nil {
				t.Fatalf("decode %q: %v", out, err)
			}

			entries, problems := parseKnownFlaky(list)
			if len(problems) > 0 {
				t.Fatalf("parseKnownFlaky problems: %q", problems)
			}
			seen := map[[2]string]bool{}
			fromGo := [][]string{}
			for _, e := range entries {
				k := [2]string{e.Pkg, e.Test}
				if !seen[k] {
					seen[k] = true
					fromGo = append(fromGo, []string{e.Pkg, e.Test})
				}
			}
			sort.Slice(fromGo, func(i, j int) bool {
				if fromGo[i][0] != fromGo[j][0] {
					return fromGo[i][0] < fromGo[j][0]
				}
				return fromGo[i][1] < fromGo[j][1]
			})
			if !reflect.DeepEqual(fromGo, fromScript) {
				t.Fatalf("parse disagrees with ci-test-summary.py:\n  go:     %q\n  script: %q", fromGo, fromScript)
			}
		})
	}
}
