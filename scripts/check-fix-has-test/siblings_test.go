package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// siblingsTree writes a small head checkout into a temp dir, makes it the
// working directory and returns a runner answering git ls-files and git diff.
func siblingsTree(t *testing.T, calls *[]string) Runner {
	t.Helper()
	head := t.TempDir()
	t.Chdir(head)
	files := map[string]string{
		"internal/ui/status_counts.go":      "if s.IsArchived() { continue }\n",
		"internal/core/group_list.go":       "if s.IsArchived() { skip() }\n",
		"internal/ui/status_counts_test.go": "s.IsArchived()\n",
		"internal/ui/testdata/golden.txt":   "IsArchived()\n",
	}
	var ls []string
	for p, s := range files {
		writeFile(t, head, p, s)
		ls = append(ls, p)
	}
	return func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		switch {
		case name == "git" && args[0] == "ls-files":
			return []byte(strings.Join(ls, "\x00") + "\x00"), nil
		case name == "git" && args[0] == "diff":
			return []byte("3\t1\tinternal/ui/status_counts.go\n9\t0\tinternal/ui/status_counts_test.go\n"), nil
		}
		return nil, errors.New("unexpected " + name)
	}
}

func TestCheckSiblings(t *testing.T) {
	changed := []string{"internal/ui/status_counts.go"}
	cases := []struct {
		name, body string
		want       []string // substrings, one per finding; empty means no findings
	}{
		{"no section", "## Why this change\nfixes counts\n", []string{`no "## Siblings" section`}},
		{"section without grep", "## Siblings\nI looked around.\n## User impact\n", []string{"has no `grep: <regexp>` line"}},
		// #3127 vs #3140: the status counts were fixed, group list was a sibling nobody named.
		{"sibling not named", "## Siblings\n- grep: `IsArchived\\(`\n- internal/ui/status_counts.go: fixed here\n",
			[]string{"also matches internal/core/group_list.go"}},
		{"every hit named", "## Siblings\ngrep: IsArchived\\(\n- internal/ui/status_counts.go: fixed\n- internal/core/group_list.go: fixed too\n## User impact\n", nil},
		// A file named only after the section ends does not count.
		{"named outside the section", "## Siblings\ngrep: IsArchived\\(\ninternal/ui/status_counts.go\n## Notes\ninternal/core/group_list.go\n",
			[]string{"also matches internal/core/group_list.go"}},
		{"matches nothing", "## Siblings\ngrep: none-here\n", []string{"matches none of the files this PR changes"}},
		{"bad regexp", "## Siblings\ngrep: (unclosed\n", []string{"is not a valid Go regexp"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			got, err := CheckSiblings(c.body, changed, siblingsTree(t, &calls))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("findings %q, want %d matching %q", got, len(c.want), c.want)
			}
			for i, w := range c.want {
				if !strings.Contains(got[i], w) {
					t.Fatalf("finding %q, want %q", got[i], w)
				}
			}
			for _, f := range got {
				if strings.Contains(f, "_test.go") || strings.Contains(f, "testdata") {
					t.Fatalf("test files and test data are not siblings: %q", f)
				}
			}
		})
	}
}

func TestSiblingsFlag(t *testing.T) {
	body := "## Siblings\ngrep: IsArchived\\(\n- internal/ui/status_counts.go: fixed\n"
	cases := []struct {
		name, title string
		flags       []string
		code        int
		want, not   string
	}{
		{"advisory by default", "fix(status): archived rows", []string{"--siblings"}, 0, "advisory: siblings: grep", ""},
		{"required fails", "fix(status): archived rows", []string{"--siblings-required"}, 1, "\nsiblings: grep", "advisory"},
		{"not a fix is not asked", "feat(status): archived rows", []string{"--siblings-required"}, 0, "ok:", "siblings"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			runner := siblingsTree(t, &calls)
			var out bytes.Buffer
			args := append([]string{"--range", "abc..HEAD", "--title", c.title, "--body", body}, c.flags...)
			code := run(args, &out, runner)
			o := "\n" + out.String()
			if code != c.code || !strings.Contains(o, c.want) || (c.not != "" && strings.Contains(o, c.not)) {
				t.Fatalf("exit %d, output %q; want exit %d with %q and without %q", code, out.String(), c.code, c.want, c.not)
			}
		})
	}
}
