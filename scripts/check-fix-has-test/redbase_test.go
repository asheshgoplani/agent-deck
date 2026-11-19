package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const baseSrc = `package ui

// helper used by both tests
func want() int { return 1 }

func TestKept(t *testing.T) { if want() != 1 { t.Fatal("x") } }

func TestEdited(t *testing.T) { _ = 1 }

func TestCommentOnly(t *testing.T) { _ = 2 }
`

const headSrc = `package ui

// helper used by both tests
func want() int { return 1 }

func TestKept(t *testing.T) { if want() != 1 { t.Fatal("x") } }

func TestEdited(t *testing.T) { _ = 3 }

func TestCommentOnly(t *testing.T) {
	// only this comment is new
	_ = 2
}

func TestAdded(t *testing.T) {}

func Testlowercase(t *testing.T) {} // not a test to go test

func TestHelperShape(a, b int) {} // two params: not a test
`

func srcs(files map[string]string) (func(string) ([]byte, error), func(string) ([]byte, bool, error)) {
	head := func(p string) ([]byte, error) {
		s, ok := files["head:"+p]
		if !ok {
			return nil, errors.New("no head file " + p)
		}
		return []byte(s), nil
	}
	base := func(p string) ([]byte, bool, error) {
		s, ok := files["base:"+p]
		return []byte(s), ok, nil
	}
	return head, base
}

func TestChangedTests(t *testing.T) {
	head, base := srcs(map[string]string{
		"base:internal/ui/a_test.go":   baseSrc,
		"head:internal/ui/a_test.go":   headSrc,
		"head:internal/ui/new_test.go": "package ui\nfunc TestBrandNew(t *testing.T) {}\n",
		// Only a table var changed: every test in the file may carry the new case.
		"base:internal/session/table_test.go": "package session\nvar cases = []int{1}\nfunc TestTable(t *testing.T) {}\nfunc TestOther(t *testing.T) {}\n",
		"head:internal/session/table_test.go": "package session\nvar cases = []int{1, 2}\nfunc TestTable(t *testing.T) {}\nfunc TestOther(t *testing.T) {}\n",
		"head:root_test.go":                   "package main\nfunc TestRoot(t *testing.T) {}\n",
	})
	changes := []Change{
		{"M", "internal/ui/a_test.go"},
		{"A", "internal/ui/new_test.go"},
		{"M", "internal/session/table_test.go"},
		{"A", "root_test.go"},
		{"D", "internal/ui/gone_test.go"}, // deleted: nothing to run
		{"M", "internal/ui/home.go"},      // not a test file
		{"A", "internal/ui/testdata/golden.txt"},
	}
	got, err := ChangedTests(changes, head, base)
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{
		{Pkg: ".", Tests: []string{"TestRoot"}},
		{Pkg: "./internal/session", Tests: []string{"TestOther", "TestTable"}},
		{Pkg: "./internal/ui", Tests: []string{"TestAdded", "TestBrandNew", "TestEdited"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedTests:\n got %+v\nwant %+v", got, want)
	}
}

// goJSON builds `go test -json` output: each entry is test=action, or ""=action for the package.
func goJSON(pkg string, events ...string) []byte {
	var b strings.Builder
	for _, e := range events {
		test, action, _ := strings.Cut(e, "=")
		if test == "" {
			b.WriteString(`{"Action":"` + action + `","Package":"` + pkg + `"}` + "\n")
			continue
		}
		b.WriteString(`{"Action":"` + action + `","Package":"` + pkg + `","Test":"` + test + `"}` + "\n")
	}
	return []byte(b.String())
}

// buildFailJSON is what go 1.24+ prints when the test package does not compile.
const buildFailJSON = `{"ImportPath":"x/internal/ui [x/internal/ui.test]","Action":"build-output","Output":"undefined: newHelper\n"}
{"ImportPath":"x/internal/ui [x/internal/ui.test]","Action":"build-fail"}
{"Action":"start","Package":"x/internal/ui"}
{"Action":"output","Package":"x/internal/ui","Output":"FAIL\tx/internal/ui [build failed]\n"}
{"Action":"fail","Package":"x/internal/ui","FailedBuild":"x/internal/ui [x/internal/ui.test]"}
`

func TestOutcomes(t *testing.T) {
	out := goJSON("p", "TestA=run", "TestA/sub=run", "TestA/sub=fail", "TestA=fail",
		"TestB=run", "TestB=pass", "TestC=run", "TestC=skip", "=fail")
	got := Outcomes(out, []string{"TestA", "TestB", "TestC", "TestD"})
	want := map[string]string{"TestA": Fail, "TestB": Pass, "TestC": NotRun, "TestD": NotRun}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := Outcomes([]byte(buildFailJSON), []string{"TestA"}); got["TestA"] != NoBuild {
		t.Fatalf("build failure: got %v, want %s", got, NoBuild)
	}
	// A setup failure (TestMain exits before any test) is not red evidence either.
	if got := Outcomes(goJSON("p", "=start", "=fail"), []string{"TestA"}); got["TestA"] != NoBuild {
		t.Fatalf("setup failure: got %v, want %s", got, NoBuild)
	}
}

// fakeGo answers `go -C <dir> test ...` from per-dir canned output.
func fakeGo(byDir map[string][]byte, calls *[]string) Runner {
	return func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		if name != "go" || len(args) < 2 || args[0] != "-C" {
			return nil, errors.New("unexpected command " + name)
		}
		return byDir[args[1]], errors.New("exit status 1") // the red run exits 1; output still counts
	}
}

func TestRedOnBase(t *testing.T) {
	targets := []Target{{Pkg: "./internal/ui", Tests: []string{"TestA"}}}
	cases := []struct {
		name       string
		base, head []byte
		ok         bool
		want       string
	}{
		{"red then green", goJSON("p", "TestA=fail"), goJSON("p", "TestA=pass"), true, "ok: 1 changed test(s) red on base and green on head"},
		{"passes on base too", goJSON("p", "TestA=pass"), goJSON("p", "TestA=pass"), false, "test-not-red-on-base"},
		{"base does not build", []byte(buildFailJSON), goJSON("p", "TestA=pass"), false, "a build error is not red evidence"},
		{"red on base, red on head", goJSON("p", "TestA=fail"), goJSON("p", "TestA=fail"), false, "test-not-green-on-head"},
		{"skipped on base", goJSON("p", "TestA=skip"), goJSON("p", "TestA=pass"), false, "test-not-red-on-base"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls, copied []string
			run := fakeGo(map[string][]byte{"/base": c.base, "/head": c.head}, &calls)
			rep, err := RedOnBase(targets, []string{"internal/ui/a_test.go", "internal/ui/testdata/g.txt"}, "/head", "/base", run,
				func(rel string) error { copied = append(copied, rel); return nil })
			if err != nil {
				t.Fatal(err)
			}
			text := strings.Join(rep.Lines, "\n")
			if rep.OK != c.ok || !strings.Contains(text, c.want) {
				t.Fatalf("ok=%v lines:\n%s\nwant ok=%v with %q", rep.OK, text, c.ok, c.want)
			}
			if len(copied) != 2 {
				t.Fatalf("test files and test data must be copied onto the base, copied %v", copied)
			}
			wantCall := "go -C /base test -count=1 -json -run ^(TestA)$ ./internal/ui"
			if calls[0] != wantCall {
				t.Fatalf("first run %q, want %q", calls[0], wantCall)
			}
		})
	}
}

// TestRedOnBase_Run drives the whole flag path: a fix PR, its test file on disk
// in the head checkout, git answered by a fake, and go test by canned JSON.
func TestRedOnBase_Run(t *testing.T) {
	headDir, baseDir := t.TempDir(), t.TempDir()
	t.Chdir(headDir)
	testFile := "internal/ui/footer_test.go"
	if err := os.MkdirAll(filepath.Join(headDir, "internal/ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(headDir, testFile), []byte("package ui\nfunc TestFooterShowsError(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	type answer struct{ base, head []byte }
	cases := []struct {
		name, title, numstat string
		goOut                answer
		code                 int
		want                 string
		ranGo                bool
	}{
		{"fix with red test", "fix(ui): show footer errors", "3\t1\tinternal/ui/footer.go\n9\t0\t" + testFile + "\n",
			answer{goJSON("p", "TestFooterShowsError=fail"), goJSON("p", "TestFooterShowsError=pass")}, 0, "red on base and green on head", true},
		{"fix whose test passes on base", "fix(ui): show footer errors", "3\t1\tinternal/ui/footer.go\n9\t0\t" + testFile + "\n",
			answer{goJSON("p", "TestFooterShowsError=pass"), goJSON("p", "TestFooterShowsError=pass")}, 1, "test-not-red-on-base", true},
		{"fix without a test", "fix(ui): show footer errors", "3\t1\tinternal/ui/footer.go\n",
			answer{}, 1, "needs-regression-test: title is a fix", false},
		{"not a fix", "feat(ui): footer colours", "3\t1\tinternal/ui/footer.go\n9\t0\t" + testFile + "\n",
			answer{}, 0, "red-on-base not required", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			runner := func(name string, args ...string) ([]byte, error) {
				calls = append(calls, name+" "+strings.Join(args, " "))
				switch {
				case name == "git" && args[0] == "diff" && args[1] == "--numstat":
					return []byte(c.numstat), nil
				case name == "git" && args[0] == "diff" && args[1] == "--name-status":
					var b bytes.Buffer
					for _, l := range strings.Split(strings.TrimSpace(c.numstat), "\n") {
						f := strings.Split(l, "\t")
						st := "M"
						if strings.HasSuffix(f[2], "_test.go") {
							st = "A"
						}
						b.WriteString(st + "\t" + f[2] + "\n")
					}
					return b.Bytes(), nil
				case name == "git" && args[0] == "show":
					return nil, errors.New("fatal: path does not exist in base")
				case name == "go" && args[1] == baseDir:
					return c.goOut.base, errors.New("exit status 1")
				case name == "go":
					return c.goOut.head, nil
				}
				return nil, errors.New("unexpected " + name)
			}
			var out bytes.Buffer
			code := run([]string{"--range", "abc123..HEAD", "--title", c.title, "--red-on-base", baseDir}, &out, runner)
			if code != c.code || !strings.Contains(out.String(), c.want) {
				t.Fatalf("exit %d, output:\n%s\nwant exit %d with %q", code, out.String(), c.code, c.want)
			}
			ranGo := strings.Contains(strings.Join(calls, "\n"), "go -C "+baseDir+" test")
			if ranGo != c.ranGo {
				t.Fatalf("ran go test on the base: %v, want %v (calls %q)", ranGo, c.ranGo, calls)
			}
			if c.ranGo {
				if _, err := os.Stat(filepath.Join(baseDir, testFile)); err != nil {
					t.Fatalf("the PR's test file was not copied onto the base: %v", err)
				}
				if !strings.Contains(strings.Join(calls, "\n"), "git diff --name-status --no-renames abc123...HEAD") {
					t.Fatalf("must diff from the merge base of the range's base, calls %q", calls)
				}
			}
		})
	}
}
