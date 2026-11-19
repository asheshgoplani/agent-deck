package main

// Tests for the review findings on the first head (R-2, R-3, R-5, R-6, R-8).
// Each one goes red without its fix.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R-6: a fix is a fix with or without a conventional colon.
func TestFixTitleWithoutColon(t *testing.T) {
	code := []File{{Path: "internal/session/x.go", Additions: 3}}
	for title, fix := range map[string]bool{
		"Fix crash when group is empty":  true,
		"fix: crash":                     true,
		"fix(ui)!: crash":                true,
		"bugfix: crash":                  true,
		"Hotfix(session): crash":         true,
		"fix crash":                      true,
		"fixture: add a golden":          false,
		"prefix the title":               false,
		"feat: fix-like word in a title": false,
	} {
		reason, err := Check(Input{Title: title, Files: code}, "o/r")
		if err != nil {
			t.Fatal(err)
		}
		if got := reason != ""; got != fix {
			t.Errorf("%q: needs a test = %v, want %v (%q)", title, got, fix, reason)
		}
	}
}

// R-3: a fix that changes only CI files (.github/) has no Go test to write.
func TestCIOnlyFixNeedsNoTest(t *testing.T) {
	in := Input{Title: "fix(ci): pin action", Files: []File{{Path: ".github/workflows/x.yml", Additions: 1}}}
	if reason, err := Check(in, "o/r"); err != nil || reason != "" {
		t.Fatalf("CI-only fix: reason %q, err %v; want no finding", reason, err)
	}
	// A product file next to it still needs a test.
	in.Files = append(in.Files, File{Path: "internal/session/x.go", Additions: 1})
	if reason, _ := Check(in, "o/r"); reason == "" {
		t.Fatal("a fix that changes Go code and CI must still need a test")
	}
}

// withExemptLabel wraps fakeGH: the PR carries base-red-exempt, added by actor
// whose repo permission is perm.
func withExemptLabel(inner Runner, actor, perm string) Runner {
	return func(name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "gh" && len(args) > 1 && args[0] == "pr" && args[1] == "view":
			out, err := inner(name, args...)
			if err != nil {
				return nil, err
			}
			var pr map[string]json.RawMessage
			if err := json.Unmarshal(out, &pr); err != nil {
				return nil, err
			}
			pr["labels"] = json.RawMessage(`[{"name":"base-red-exempt"}]`)
			return json.Marshal(pr)
		case name == "gh" && strings.Contains(joined, "/events"):
			return []byte(actor + "\n"), nil
		case name == "gh" && strings.Contains(joined, "/collaborators/"+actor+"/permission"):
			return []byte(perm + "\n"), nil
		}
		return inner(name, args...)
	}
}

// R-3: the exempt label skips the whole gate, but only when a maintainer added it,
// and the output says who.
func TestExemptLabel(t *testing.T) {
	cases := []struct {
		perm string
		code int
		want string
	}{
		{"admin", 0, "exempt: base-red-exempt added by alice (admin)"},
		{"maintain", 0, "exempt: base-red-exempt added by alice (maintain)"},
		{"triage", 1, "base-red-exempt ignored: added by alice (triage), not a maintainer"},
		{"write", 1, "base-red-exempt ignored: added by alice (write), not a maintainer"},
	}
	for _, c := range cases {
		t.Run(c.perm, func(t *testing.T) {
			gh, _ := fakeGH(t, "fix-no-test", "")
			var out bytes.Buffer
			code := run([]string{"--pr", "42", "--exempt-label", "base-red-exempt"}, &out, withExemptLabel(gh, "alice", c.perm))
			if code != c.code || !strings.Contains(out.String(), c.want) {
				t.Fatalf("exit %d, output %q; want exit %d with %q", code, out.String(), c.code, c.want)
			}
		})
	}
}

// R-1 / R-7: the token step classifies the PR and writes the answer; the red
// step runs without gh and trusts that answer.
func TestFixOutAndAssumeFix(t *testing.T) {
	cases := []struct{ fixture, want string }{
		{"fix-with-test", "yes"},          // a fix with a test file: run the red step
		{"feat-closes-enhancement", "no"}, // not a fix
		{"fix-no-test", "no"},             // already failed here, nothing to run
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			gh, _ := fakeGH(t, c.fixture, "")
			f := filepath.Join(t.TempDir(), "fix.txt")
			var out bytes.Buffer
			run([]string{"--pr", "42", "--fix-out", f}, &out, gh)
			b, err := os.ReadFile(f)
			if err != nil || strings.TrimSpace(string(b)) != c.want {
				t.Fatalf("fix-out %q (%v), want %q; output %s", b, err, c.want, out.String())
			}
		})
	}

	// --assume-fix never calls gh: the red step runs with no token.
	headDir, baseDir := t.TempDir(), t.TempDir()
	t.Chdir(headDir)
	writeFile(t, headDir, "internal/ui/f_test.go", "package ui\nfunc TestF(t *testing.T) {}\n")
	var calls []string
	runner := func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch {
		case name == "gh":
			return nil, errors.New("gh must not run in the red step")
		case name == "git" && args[0] == "diff":
			return []byte("M\tinternal/ui/f.go\nA\tinternal/ui/f_test.go\n"), nil
		case name == "git" && args[0] == "show":
			return nil, errors.New("new file")
		case name == "go" && args[1] == baseDir:
			return goJSON("p", "TestF=fail"), errors.New("exit status 1")
		case name == "go":
			return goJSON("p", "TestF=pass"), nil
		}
		return nil, errors.New("unexpected " + name)
	}
	var out bytes.Buffer
	code := run([]string{"--range", "abc..HEAD", "--assume-fix", "--red-on-base", baseDir}, &out, runner)
	if code != 0 || !strings.Contains(out.String(), "red on base, green on head") {
		t.Fatalf("assume-fix: exit %d, output %s, calls %q", code, out.String(), calls)
	}
}

func writeFile(t *testing.T, dir, rel, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// diffOnly answers git diff --name-status and git show, nothing else.
func diffOnly(nameStatus string) Runner {
	return func(name string, args ...string) ([]byte, error) {
		if name == "git" && args[0] == "diff" {
			return []byte(nameStatus), nil
		}
		if name == "git" && args[0] == "show" {
			return nil, errors.New("new file")
		}
		return nil, errors.New("unexpected " + name + " " + strings.Join(args, " "))
	}
}

// R-2: a Go fix needs a Go test function, not just a testdata file or a helper.
func TestRedOnBase_GoFixNeedsATestFunction(t *testing.T) {
	cases := []struct {
		name, nameStatus string
		files            map[string]string
		ok               bool
		want             string
	}{
		{"testdata only", "M\tinternal/session/x.go\nA\tinternal/session/testdata/note.txt\n",
			map[string]string{"internal/session/testdata/note.txt": "x"}, false, "no-go-test"},
		{"helper-only test file", "M\tinternal/session/x.go\nA\tinternal/session/helpers_test.go\n",
			map[string]string{"internal/session/helpers_test.go": "package session\n\nfunc helper() {}\n"}, false, "no-go-test"},
		// A web fix tested by a web test has no Go test to run: it passes with a note.
		{"non-Go fix with a non-Go test", "M\tweb/src/app.js\nA\tweb/src/app.test.js\n",
			map[string]string{"web/src/app.test.js": "test('x', () => {})"}, true, "base-red is checked for Go tests only"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			head := t.TempDir()
			t.Chdir(head)
			for p, s := range c.files {
				writeFile(t, head, p, s)
			}
			var out bytes.Buffer
			ok, err := redOnBaseMode(diffOnly(c.nameStatus), "base", t.TempDir(), &out)
			if err != nil || ok != c.ok || !strings.Contains(out.String(), c.want) {
				t.Fatalf("ok=%v err=%v output %q; want ok=%v with %q", ok, err, out.String(), c.ok, c.want)
			}
		})
	}
}

// R-5: a test that cannot build on the base says how to fix that.
func TestRedOnBase_NoBuildSaysWhatToDo(t *testing.T) {
	var calls []string
	run := fakeGo(map[string][]byte{"/base": []byte(buildFailJSON), "/head": goJSON("p", "TestA=pass")}, &calls)
	rep, err := RedOnBase([]Target{{Pkg: "./internal/ui", Tests: []string{"TestA"}}}, nil, "/head", "/base", run,
		func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(rep.Lines, "\n")
	if rep.OK || !strings.Contains(text, "test the fix through code that exists on the base") ||
		!strings.Contains(text, "base-red-exempt") {
		t.Fatalf("ok=%v lines:\n%s\nwant a hint naming the way out", rep.OK, text)
	}
}

// R-8: sibling patterns are Go regexps end to end, and a pattern must match a
// file the PR changes (a pattern that matches nothing proves nothing).
func TestSiblings_GoRegexpAndMustMatchTheFix(t *testing.T) {
	head := t.TempDir()
	t.Chdir(head)
	writeFile(t, head, "internal/session/status.go", "func count() { n := 42 }\n")
	writeFile(t, head, "internal/core/group_list.go", "func list() { total := 7 }\n")
	writeFile(t, head, "internal/session/status_test.go", "n := 1\n")
	runner := func(name string, args ...string) ([]byte, error) {
		switch {
		case name == "git" && args[0] == "diff":
			return []byte("3\t1\tinternal/session/status.go\n9\t0\tinternal/session/status_test.go\n"), nil
		case name == "git" && args[0] == "ls-files":
			return []byte("internal/session/status.go\x00internal/core/group_list.go\x00internal/session/status_test.go\x00"), nil
		}
		return nil, errors.New("unexpected " + name + " " + strings.Join(args, " "))
	}
	cases := []struct {
		name, section string
		code          int
		want          string
	}{
		// \d is RE2 (and PCRE), not POSIX ERE: git grep -E would read it differently.
		{"RE2 class finds the sibling", "grep: := \\d+\n- internal/session/status.go: fixed\n", 1,
			"also matches internal/core/group_list.go"},
		{"every hit named", "grep: := \\d+\n- internal/session/status.go: fixed\n- internal/core/group_list.go: fixed\n", 0, "ok:"},
		{"pattern matches nothing the PR changed", "grep: no-such-text\n", 1,
			"matches none of the files this PR changes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			code := run([]string{"--range", "abc..HEAD", "--title", "fix: counts", "--body", "## Siblings\n" + c.section,
				"--siblings-required"}, &out, runner)
			if code != c.code || !strings.Contains(out.String(), c.want) {
				t.Fatalf("exit %d, output %q; want exit %d with %q", code, out.String(), c.code, c.want)
			}
		})
	}
}
