package main

// Red on base: a fix PR's test must fail on the base commit and pass on the head.
//
// A test file that changes is not proof by itself: the "has a test file" check
// (2026-10-19) also passes a test that cannot fail. This step copies the PR's
// changed test files and test data onto a checkout of the base commit, runs the
// changed Go tests there and on the head, and passes only when at least one of
// them fails on the base by assertion (not by a build or setup error) and all
// of them pass on the head. A fix that changes Go code must change a Go test
// function.
//
// Limits (it proves the test can fail, not what it checks):
//   - A source-grep test (read a .go file, look for a string the fix added) is
//     red on base and green on head, so it passes. Review must still reject it.
//   - Only test files and test data are copied onto the base. A test that fails
//     on the base because a NEW non-test file it reads (a fixture outside
//     testdata/) is missing there also counts as red.
//   - A test that calls a function the fix adds does not build on the base and
//     is refused: test through code that exists on the base, or a maintainer
//     adds base-red-exempt.
//   - It says nothing about paths the test does not cover (the missed-path class
//     in mode 6); see siblings.go for the advisory part of that.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/scanner"
	"go/token"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Change is one changed file between base and head.
type Change struct {
	Status string // A, M or D (git diff --name-status, renames off)
	Path   string
}

// Target is a package and the Go tests in it that the PR added or changed.
type Target struct {
	Pkg   string // ./internal/ui
	Tests []string
}

// testFuncs returns the top-level TestXxx(t *testing.T) functions of a Go file,
// name to its body as a token stream (comments and layout dropped, so such an
// edit is no change; whitespace inside a string literal is kept).
// TestMain is the package entry hook, not a test: go test never reports it.
func testFuncs(src []byte) (map[string]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x_test.go", src, 0)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || fn.Name.Name == "TestMain" || !isTestName(fn.Name.Name) || fn.Type.Params.NumFields() != 1 {
			continue
		}
		var b bytes.Buffer
		if err := printer.Fprint(&b, fset, fn.Body); err != nil {
			return nil, err
		}
		// Layout is not behaviour: compare tokens, not text. Whitespace between
		// tokens is dropped, whitespace inside a literal ("a b" vs "ab") is kept.
		out[fn.Name.Name] = tokenString(b.Bytes())
	}
	return out, nil
}

// tokenString joins the Go tokens of src, one separator between tokens.
// Mode 0 drops comments. Every semicolon, written or inserted at a line
// break, is the same ";" token, so equal strings mean an equal parse (a real
// edit never compares equal); a layout edit that go/printer keeps and that
// moves a statement boundary only makes a test count as changed.
func tokenString(src []byte) string {
	fset := token.NewFileSet()
	var s scanner.Scanner
	s.Init(fset.AddFile("", fset.Base(), len(src)), src, nil, 0)
	var b strings.Builder
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.SEMICOLON {
			lit = ";"
		}
		if lit != "" {
			b.WriteString(lit)
		} else {
			b.WriteString(tok.String())
		}
		b.WriteByte(0)
	}
	return b.String()
}

// isTestName is go test's rule: Test, then nothing or a non-lowercase rune.
func isTestName(n string) bool {
	if !strings.HasPrefix(n, "Test") {
		return false
	}
	rest := n[len("Test"):]
	return rest == "" || !(rest[0] >= 'a' && rest[0] <= 'z')
}

// ChangedTests lists, per package, the tests a PR added or changed. A test
// function counts when it is new or its body changed. When a test file changed
// but none of its test bodies did (a table, a helper or a fixture var moved),
// every test in that file counts, since any of them may carry the new case.
// base(p) returns the file at the base commit, or ok=false when it is new.
func ChangedTests(changes []Change, head func(string) ([]byte, error), base func(string) ([]byte, bool, error)) ([]Target, error) {
	byPkg := map[string]map[string]bool{}
	for _, c := range changes {
		if c.Status == "D" || !strings.HasSuffix(c.Path, "_test.go") {
			continue
		}
		hsrc, err := head(c.Path)
		if err != nil {
			return nil, err
		}
		hf, err := testFuncs(hsrc)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", c.Path, err)
		}
		bf := map[string]string{}
		if bsrc, ok, err := base(c.Path); err != nil {
			return nil, err
		} else if ok {
			if bf, err = testFuncs(bsrc); err != nil {
				bf = map[string]string{} // base did not parse: treat every test as changed
			}
		}
		var picked []string
		for name, body := range hf {
			if old, ok := bf[name]; !ok || old != body {
				picked = append(picked, name)
			}
		}
		if len(picked) == 0 {
			for name := range hf {
				picked = append(picked, name)
			}
		}
		pkg := "./" + path.Dir(c.Path)
		if path.Dir(c.Path) == "." {
			pkg = "."
		}
		if byPkg[pkg] == nil {
			byPkg[pkg] = map[string]bool{}
		}
		for _, n := range picked {
			byPkg[pkg][n] = true
		}
	}
	var out []Target
	for pkg, set := range byPkg {
		if len(set) == 0 {
			continue
		}
		t := Target{Pkg: pkg}
		for n := range set {
			t.Tests = append(t.Tests, n)
		}
		sort.Strings(t.Tests)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pkg < out[j].Pkg })
	return out, nil
}

// Outcome of one test in one run.
const (
	Pass    = "pass"
	Fail    = "fail"     // failed by assertion: --- FAIL
	NoBuild = "no-build" // the package did not build or set up, so the test never ran
	NotRun  = "not-run"  // built, but the test did not run (skipped or not matched)
)

// Outcomes reads `go test -json` output and returns the result of each wanted test.
func Outcomes(out []byte, tests []string) map[string]string {
	res := map[string]string{}
	for _, t := range tests {
		res[t] = NotRun
	}
	pkgFailed, anyTestEvent := false, false
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var ev struct{ Action, Test, Output string }
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue // build errors arrive as plain text on older toolchains
		}
		if ev.Test == "" {
			if ev.Action == "fail" || ev.Action == "build-fail" {
				pkgFailed = true
			}
			continue
		}
		anyTestEvent = true
		top, _, _ := strings.Cut(ev.Test, "/")
		if _, want := res[top]; !want || top != ev.Test {
			continue
		}
		switch ev.Action {
		case "pass":
			res[top] = Pass
		case "fail":
			res[top] = Fail
		case "skip":
			res[top] = NotRun
		}
	}
	if pkgFailed && !anyTestEvent {
		for t, r := range res {
			if r == NotRun {
				res[t] = NoBuild
			}
		}
	}
	return res
}

// runPattern is the -run pattern for exactly these top-level tests.
func runPattern(tests []string) string {
	return "^(" + strings.Join(tests, "|") + ")$"
}

// RedReport is the result of the red-on-base step.
type RedReport struct {
	Lines []string
	OK    bool
}

// RedOnBase overlays the changed test files onto baseDir, runs the changed tests
// there and in headDir, and judges them. copyFile copies a head file into baseDir.
func RedOnBase(targets []Target, overlay []string, headDir, baseDir string, run Runner, copyFile func(rel string) error) (RedReport, error) {
	var r RedReport
	for _, rel := range overlay {
		if err := copyFile(rel); err != nil {
			return r, fmt.Errorf("copy %s onto the base: %w", rel, err)
		}
	}
	red, baseRed, headBad, noBuild := 0, 0, 0, 0
	for _, t := range targets {
		args := []string{"-C", "", "test", "-count=1", "-json", "-run", runPattern(t.Tests), t.Pkg}
		args[1] = baseDir
		bout, _ := run("go", args...) // a red run exits non-zero; the JSON is the evidence
		args[1] = headDir
		hout, _ := run("go", args...)
		b, h := Outcomes(bout, t.Tests), Outcomes(hout, t.Tests)
		for _, name := range t.Tests {
			verdict := "not red on base"
			switch {
			case b[name] == Fail && h[name] == Pass:
				verdict = "red on base, green on head"
				red++
			case b[name] == Fail:
				baseRed++
				verdict = "red on base, but " + h[name] + " on head"
			case b[name] == NoBuild:
				noBuild++
				verdict = "base does not build with this test (a build error is not red evidence)"
			}
			if h[name] != Pass {
				headBad++
			}
			r.Lines = append(r.Lines, fmt.Sprintf("%s %s: base=%s head=%s: %s", t.Pkg, name, b[name], h[name], verdict))
		}
	}
	switch {
	case red == 0 && baseRed == 0:
		r.Lines = append(r.Lines, "test-not-red-on-base: none of the changed tests fails on the base by assertion; "+
			"a fix needs a test that reproduces the bug (it must FAIL on the base commit)")
		if noBuild > 0 {
			r.Lines = append(r.Lines, "hint: a test that calls code the fix adds cannot run on the base; "+
				"test the fix through code that exists on the base (the path the user hit), "+
				"or ask a maintainer for the base-red-exempt label and say in the PR body how it was verified")
		}
	case red == 0 || headBad > 0:
		r.Lines = append(r.Lines, fmt.Sprintf("test-not-green-on-head: %d changed test(s) do not pass on the head", headBad))
	default:
		r.OK = true
		r.Lines = append(r.Lines, fmt.Sprintf("ok: %d changed test(s) red on base and green on head", red))
	}
	return r, nil
}

// parseNameStatus reads `git diff --name-status --no-renames` output.
func parseNameStatus(out []byte) []Change {
	var cs []Change
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		st, p, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		cs = append(cs, Change{Status: st[:1], Path: p})
	}
	return cs
}

// redOnBaseMode runs the red-on-base step for base..HEAD in headDir (the working directory).
func redOnBaseMode(run Runner, baseSHA, baseDir string, stdout io.Writer) (bool, error) {
	headDir, err := os.Getwd()
	if err != nil {
		return false, err
	}
	out, err := run("git", "diff", "--name-status", "--no-renames", baseSHA+"...HEAD")
	if err != nil {
		return false, fmt.Errorf("git diff: %w", err)
	}
	changes := parseNameStatus(out)
	head := func(p string) ([]byte, error) { return os.ReadFile(filepath.Join(headDir, p)) }
	base := func(p string) ([]byte, bool, error) {
		b, err := run("git", "show", baseSHA+":"+p)
		if err != nil {
			return nil, false, nil // new in the PR
		}
		return b, true, nil
	}
	targets, err := ChangedTests(changes, head, base)
	if err != nil {
		return false, err
	}
	if len(targets) == 0 {
		for _, c := range changes {
			if c.Status != "D" && strings.HasSuffix(c.Path, ".go") && !IsTestFile(c.Path) {
				fmt.Fprintf(stdout, "no-go-test: this fix changes Go code (%s) but adds or changes no Go test function (func TestXxx); "+
					"add one that reproduces the bug (a maintainer can add base-red-exempt for a fix covered only by non-Go tests)\n", c.Path)
				return false, nil
			}
		}
		var other []string
		for _, c := range changes {
			if c.Status != "D" && IsTestFile(c.Path) {
				other = append(other, c.Path)
			}
		}
		fmt.Fprintf(stdout, "red-on-base: no Go test function changed (test files: %s); base-red is checked for Go tests only\n",
			strings.Join(other, ", "))
		return len(other) > 0, nil
	}
	var overlay []string
	for _, c := range changes {
		if c.Status != "D" && IsTestFile(c.Path) {
			overlay = append(overlay, c.Path)
		}
	}
	copyFile := func(rel string) error {
		b, err := os.ReadFile(filepath.Join(headDir, rel))
		if err != nil {
			return err
		}
		dst := filepath.Join(baseDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	}
	rep, err := RedOnBase(targets, overlay, headDir, baseDir, run, copyFile)
	if err != nil {
		return false, err
	}
	for _, l := range rep.Lines {
		fmt.Fprintln(stdout, l)
	}
	return rep.OK, nil
}
