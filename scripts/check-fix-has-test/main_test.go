package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeGH answers `gh pr view N` from testdata/<fixture>.json (files cut at 100, as gh
// does), `gh api --paginate
// repos/R/pulls/N/files` with every file of that fixture (one JSON object per line, as
// gh prints after --jq), `gh api repos/R/issues/N` from testdata/labels.json and
// `git diff --numstat` from numstat. No network, no git.
func fakeGH(t *testing.T, fixture, numstat string) (Runner, *[]string) {
	t.Helper()
	labels := map[string][]string{}
	raw, err := os.ReadFile(filepath.Join("testdata", "labels.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &labels); err != nil {
		t.Fatal(err)
	}
	var calls []string
	return func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch {
		case name == "gh" && len(args) > 1 && args[0] == "pr" && args[1] == "view":
			raw, err := os.ReadFile(filepath.Join("testdata", fixture+".json"))
			if err != nil {
				return nil, err
			}
			var pr map[string]json.RawMessage
			if err := json.Unmarshal(raw, &pr); err != nil {
				return nil, err
			}
			var files []json.RawMessage
			if err := json.Unmarshal(pr["files"], &files); err != nil {
				return nil, err
			}
			if len(files) > 100 { // like gh pr view --json files
				pr["files"], _ = json.Marshal(files[:100])
			}
			return json.Marshal(pr)
		case name == "gh" && len(args) > 2 && args[0] == "api" && args[1] == "--paginate" && strings.HasSuffix(args[2], "/files"):
			raw, err := os.ReadFile(filepath.Join("testdata", fixture+".json"))
			if err != nil {
				return nil, err
			}
			var pr struct {
				Files []File `json:"files"`
			}
			if err := json.Unmarshal(raw, &pr); err != nil {
				return nil, err
			}
			var b bytes.Buffer
			for _, f := range pr.Files {
				line, _ := json.Marshal(f)
				b.Write(append(line, '\n'))
			}
			return b.Bytes(), nil
		case name == "gh" && len(args) > 1 && args[0] == "api":
			n := args[1][strings.LastIndex(args[1], "/")+1:]
			l, ok := labels[n]
			if !ok {
				return nil, fmt.Errorf("HTTP 404 for issue %s", n)
			}
			return json.Marshal(l)
		case name == "git" && len(args) > 0 && args[0] == "diff":
			return []byte(numstat), nil
		}
		return nil, errors.New("unexpected command " + name)
	}, &calls
}

func TestCheckFixHasTest_PRFixtures(t *testing.T) {
	cases := []struct {
		fixture string
		code    int
		want    string // substring of the output
	}{
		{"fix-no-test", 1, "needs-regression-test: title is a fix and closes bug #2206 but no test file"},
		{"fix-with-test", 0, "ok:"},
		{"fix-test-deleted", 1, "needs-regression-test: title is a fix but no test file"},
		{"feat-closes-bug", 1, "needs-regression-test: closes bug #2741 but"},
		{"feat-closes-enhancement", 0, "ok:"},
		{"linked-bug-only", 0, "ok:"}, // testdata/ golden added: counts as the regression test
		{"fix-docs-only", 0, "ok:"},
		// 168 files, the test is file 160: gh pr view would stop at 100 and miss it.
		{"fix-test-after-file-100", 0, "ok:"},
		// Linked to #2206 of another repo; the local #2206 is a bug but is not the one closed.
		{"linked-other-repo", 0, "ok:"},
		// An embedded .md template is shipped behaviour, not docs.
		{"fix-embedded-template-only", 1, "needs-regression-test: title is a fix but no test file"},
		// The PR has 3 changed files but only 2 could be read: never a pass.
		{"files-incomplete", 2, "list is incomplete"},
		// GitHub drops line counts on a very large PR: the test file cannot be judged, never a pass or a flag.
		{"large-pr-no-counts", 2, "no line counts for internal/ui/help_test.go"},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			gh, _ := fakeGH(t, c.fixture, "")
			var out bytes.Buffer
			code := run([]string{"--pr", "42"}, &out, gh)
			if code != c.code || !strings.Contains(out.String(), c.want) {
				t.Fatalf("exit %d, output %q; want exit %d with %q", code, out.String(), c.code, c.want)
			}
			if c.code == 1 && strings.Count(strings.TrimSpace(out.String()), "\n") != 0 {
				t.Fatalf("reason must be one line, got %q", out.String())
			}
		})
	}
}

func TestCheckFixHasTest_AdvisoryExitsZero(t *testing.T) {
	gh, _ := fakeGH(t, "fix-no-test", "")
	var out bytes.Buffer
	if code := run([]string{"--pr", "42", "--advisory"}, &out, gh); code != 0 {
		t.Fatalf("advisory exit %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), "needs-regression-test:") {
		t.Fatalf("advisory must still print the finding, got %q", out.String())
	}
}

func TestCheckFixHasTest_LinkedBugWithoutTest(t *testing.T) {
	// The bug is only linked in the sidebar (closingIssuesReferences), not named in the body.
	in := Input{Title: "status filter counts remote rows", Linked: []int{2519},
		Files:  []File{{Path: "internal/ui/filter.go", Additions: 4}},
		Labels: func(n int) ([]string, error) { return []string{"bug"}, nil }}
	reason, err := Check(in, "asheshgoplani/agent-deck")
	if err != nil || !strings.Contains(reason, "closes bug #2519") {
		t.Fatalf("got %q, %v; want a closes-bug finding", reason, err)
	}
}

func TestCheckFixHasTest_RangeMode(t *testing.T) {
	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte("Fixes #2206"), 0o600); err != nil {
		t.Fatal(err)
	}
	noTest := "12\t3\tinternal/session/restart.go\n-\t-\tassets/logo.png\n"
	withTest := noTest + "40\t0\tinternal/session/restart_test.go\n"
	for _, c := range []struct {
		numstat string
		code    int
	}{{noTest, 1}, {withTest, 0}} {
		gh, calls := fakeGH(t, "", c.numstat)
		var out bytes.Buffer
		code := run([]string{"--range", "origin/main..HEAD", "--title", "chore: tidy", "--body-file", body}, &out, gh)
		if code != c.code {
			t.Fatalf("numstat %q: exit %d (%s), want %d", c.numstat, code, out.String(), c.code)
		}
		if (*calls)[0] != "git diff --numstat --no-renames origin/main...HEAD" {
			t.Fatalf("range must diff from the merge base, ran %q", (*calls)[0])
		}
	}
}

func TestCheckFixHasTest_UnreadableInputsExitTwo(t *testing.T) {
	// An issue whose labels cannot be read must not pass silently, even in advisory mode's caller.
	in := Input{Title: "feat: x", Body: "Fixes #999", Files: []File{{Path: "a.go", Additions: 1}},
		Labels: func(int) ([]string, error) { return nil, errors.New("HTTP 404") }}
	if _, err := Check(in, "asheshgoplani/agent-deck"); err == nil {
		t.Fatal("want an error when the labels of a closed issue cannot be read")
	}
	var out bytes.Buffer
	if code := run([]string{"--pr", "1", "--range", "a..b"}, &out, nil); code != 2 {
		t.Fatalf("--pr with --range: exit %d, want 2", code)
	}
}

func TestClosedIssues(t *testing.T) {
	body := "Fixes #1, closes: #2\nResolved https://github.com/asheshgoplani/agent-deck/issues/3\n" +
		"fixes asheshgoplani/agent-deck#4 and fixes other/repo#5 and https://github.com/other/repo/issues/6\n" +
		"see #7, prefix#8, CLOSES #1"
	got := ClosedIssues(body, "asheshgoplani/agent-deck")
	if want := []int{1, 2, 3, 4}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ClosedIssues = %v, want %v", got, want)
	}
}

func TestIsTestFile(t *testing.T) {
	for p, want := range map[string]bool{
		"internal/session/restart_test.go": true,
		"tests/e2e/send.sh":                true,
		"internal/ui/testdata/pane.txt":    true,
		"web/src/app.test.ts":              true,
		"web/src/app.spec.js":              true,
		"internal/session/restart.go":      false,
		"cmd/tests.go":                     false,
		"docs/testing.md":                  false,
		"test":                             false,
		"README.md":                        false,
	} {
		if got := IsTestFile(p); got != want {
			t.Errorf("IsTestFile(%q) = %v, want %v", p, got, want)
		}
	}
}
