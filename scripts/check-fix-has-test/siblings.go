package main

// Siblings: a fix PR says where else the same pattern lives.
//
// Most mode 6 defects after 2026-10-19 came from fixes whose test was real and
// red on the base, but covered one call site while a sibling kept the bug
// (#3127 fixed status counts but not `group list`; #3132 unwrapped pasted
// prompts in two classifiers but not TurnOriginForPrompt). AGENTS.md review
// rule 1 asks for the siblings; this makes the search visible and checkable.
//
// The PR body carries a section headed "## Siblings" with one or more lines
//
//	grep: <extended regexp>
//
// The check runs `git grep -l -E` for each pattern over the head's non-test
// files and reports every matching file the section does not name. Naming a
// file with a word on why it is fine ("cmd/x.go: different problem") is enough.

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	siblingsHeading = regexp.MustCompile(`(?im)^##\s+siblings\s*$`)
	nextHeading     = regexp.MustCompile(`(?m)^##\s`)
	grepLine        = regexp.MustCompile("(?m)^[\\s>*`-]*grep:\\s*(.+?)\\s*`?\\s*$")
)

// SiblingsSection returns the body text under "## Siblings", and whether it exists.
func SiblingsSection(body string) (string, bool) {
	loc := siblingsHeading.FindStringIndex(body)
	if loc == nil {
		return "", false
	}
	rest := body[loc[1]:]
	if n := nextHeading.FindStringIndex(rest); n != nil {
		rest = rest[:n[0]]
	}
	return rest, true
}

// CheckSiblings returns the findings for a fix PR's body (empty when it is fine).
func CheckSiblings(body string, run Runner) ([]string, error) {
	sec, ok := SiblingsSection(body)
	if !ok {
		return []string{"siblings: no \"## Siblings\" section; add `grep: <regexp>` lines for the pattern you fixed and name each file it finds"}, nil
	}
	pats := grepLine.FindAllStringSubmatch(sec, -1)
	if len(pats) == 0 {
		return []string{"siblings: the \"## Siblings\" section has no `grep: <regexp>` line"}, nil
	}
	var out []string
	for _, m := range pats {
		pat := strings.Trim(m[1], "`")
		if _, err := regexp.Compile(pat); err != nil {
			out = append(out, fmt.Sprintf("siblings: grep %q is not a valid regexp: %v", pat, err))
			continue
		}
		res, err := run("git", "grep", "-l", "-E", "-e", pat, "--", ".", ":(exclude)*_test.go", ":(exclude)**/testdata/**")
		if err != nil && len(res) > 0 {
			return nil, fmt.Errorf("git grep %q: %w", pat, err)
		} // exit 1 with no output is "no match"
		var missing []string
		for _, f := range strings.Split(strings.TrimSpace(string(res)), "\n") {
			if f != "" && !strings.Contains(sec, f) {
				missing = append(missing, f)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			out = append(out, fmt.Sprintf("siblings: grep %q also matches %s; name each in the section with what you did there",
				pat, strings.Join(missing, ", ")))
		}
	}
	return out, nil
}
