package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// fakeGrep answers `git grep -l -E -e <pat>` from a pattern-to-files map.
func fakeGrep(hits map[string]string, calls *[]string) Runner {
	return func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		if name == "git" && len(args) > 4 && args[0] == "grep" {
			if out, ok := hits[args[4]]; ok {
				return []byte(out), nil
			}
			return nil, errors.New("exit status 1") // git grep: no match
		}
		if name == "git" && args[0] == "diff" {
			return []byte("3\t1\tinternal/session/status.go\n9\t0\tinternal/session/status_test.go\n"), nil
		}
		return nil, errors.New("unexpected " + name)
	}
}

func TestCheckSiblings(t *testing.T) {
	hits := map[string]string{
		"IsArchived\\(": "internal/ui/status_counts.go\ninternal/core/group_list.go\n",
		"none-here":     "",
	}
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
		{"no match", "## Siblings\ngrep: none-here\n", nil},
		{"bad regexp", "## Siblings\ngrep: (unclosed\n", []string{"is not a valid regexp"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			got, err := CheckSiblings(c.body, fakeGrep(hits, &calls))
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
			for _, call := range calls {
				if !strings.Contains(call, ":(exclude)*_test.go") {
					t.Fatalf("grep must skip test files, ran %q", call)
				}
			}
		})
	}
}

func TestSiblingsFlag(t *testing.T) {
	body := "## Siblings\ngrep: IsArchived\\(\n- internal/ui/status_counts.go: fixed\n"
	hits := map[string]string{"IsArchived\\(": "internal/ui/status_counts.go\ninternal/core/group_list.go\n"}
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
			var out bytes.Buffer
			args := append([]string{"--range", "abc..HEAD", "--title", c.title, "--body", body}, c.flags...)
			code := run(args, &out, fakeGrep(hits, &calls))
			o := "\n" + out.String()
			if code != c.code || !strings.Contains(o, c.want) || (c.not != "" && strings.Contains(o, c.not)) {
				t.Fatalf("exit %d, output %q; want exit %d with %q and without %q", code, out.String(), c.code, c.want, c.not)
			}
		})
	}
}
