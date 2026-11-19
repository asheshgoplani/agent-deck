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
// For each pattern the check lists the head's tracked non-test files
// (`git ls-files`) and matches the pattern with Go's regexp (RE2) against
// each one, so what it validates is what it runs. A pattern must match at least
// one non-test file the PR changes (it names the code you fixed; a pattern that
// matches nothing would silence the check). Every other file it matches must be
// named in the section; a word on why it is fine ("cmd/x.go: different
// problem") is enough.
//
// Limit: it finds siblings that share the fixed pattern's text. A sibling that
// does the same job with other words (#3132 vs TurnOriginForPrompt) is found
// only if the author greps for the behaviour, not the token.

import (
	"fmt"
	"os"
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

// maxSiblingFile skips huge files (vendored or generated data) when matching.
const maxSiblingFile = 2 << 20

// CheckSiblings returns the findings for a fix PR's body (empty when it is fine).
// changed are the PR's changed non-test files; files are read from the working
// directory (the head checkout).
func CheckSiblings(body string, changed []string, run Runner) ([]string, error) {
	sec, ok := SiblingsSection(body)
	if !ok {
		return []string{"siblings: no \"## Siblings\" section; add `grep: <regexp>` lines for the pattern you fixed and name each file it finds"}, nil
	}
	pats := grepLine.FindAllStringSubmatch(sec, -1)
	if len(pats) == 0 {
		return []string{"siblings: the \"## Siblings\" section has no `grep: <regexp>` line"}, nil
	}
	out, err := run("git", "ls-files", "-z")
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []string
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" && !IsTestFile(f) {
			files = append(files, f)
		}
	}
	isChanged := map[string]bool{}
	for _, f := range changed {
		isChanged[f] = true
	}
	var found []string
	for _, m := range pats {
		pat := strings.Trim(m[1], "`")
		re, err := regexp.Compile(pat)
		if err != nil {
			found = append(found, fmt.Sprintf("siblings: grep %q is not a valid Go regexp: %v", pat, err))
			continue
		}
		var missing []string
		hitsChange := false
		for _, f := range files {
			info, err := os.Stat(f)
			if err != nil || !info.Mode().IsRegular() || info.Size() > maxSiblingFile {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil || !re.Match(b) {
				continue
			}
			if isChanged[f] {
				hitsChange = true
			} else if !strings.Contains(sec, f) {
				missing = append(missing, f)
			}
		}
		if !hitsChange {
			found = append(found, fmt.Sprintf("siblings: grep %q matches none of the files this PR changes; grep for the code you fixed", pat))
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			found = append(found, fmt.Sprintf("siblings: grep %q also matches %s; name each in the section with what you did there",
				pat, strings.Join(missing, ", ")))
		}
	}
	return found, nil
}
