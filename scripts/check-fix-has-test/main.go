// Command check-fix-has-test flags a fix PR that ships without a regression test.
//
// A PR needs a test when its title starts with fix, bugfix or hotfix (with or
// without a conventional colon: "fix:", "fix(scope):", "Fix crash ...") or when
// it closes an issue labelled bug (Closes/Fixes/Resolves #n, or GitHub's
// linked issues). It has one when it adds lines to a test file: *_test.go, a file
// under a tests/ or testdata/ directory, or a *.test.* / *.spec.* file. A PR that
// changes only docs (docs/, documentation/, site/, top-level *.md and LICENSE)
// or only CI (.github/) is not asked for a test. Markdown elsewhere, such as embedded templates, is code.
//
// Usage:
//
//	check-fix-has-test --pr 123 [--repo owner/name] [--advisory]
//	check-fix-has-test --range origin/main..HEAD --title "fix: ..." [--body-file f] [--advisory]
//	check-fix-has-test --pr 123 --base <sha> --red-on-base <base checkout> [--siblings]
//
// --red-on-base (run from the head checkout) also requires a fix PR's changed Go
// tests to fail on the base commit by assertion and pass on the head (redbase.go).
// --siblings reports, as advisory lines, files that the "## Siblings" grep lines
// of a fix PR's body find and the section does not name; --siblings-required
// makes them fail the check (siblings.go).
//
// In CI the gate runs in two steps (fix-regression-gate.yml), both with this
// tool built from the BASE branch, never from the PR:
//
//	check-fix-has-test --pr N --siblings --exempt-label base-red-exempt --fix-out f   (has a token)
//	check-fix-has-test --range base..HEAD --assume-fix --red-on-base <dir>         (no token; runs the PR's tests)
//
// --fix-out writes "yes" when the PR is a fix with a test file (run the red step)
// and "no" otherwise. --exempt-label skips the whole gate when a maintainer
// (admin or maintain permission) added that label; the output names who.
//
// --pr reads the PR through gh (read-only), with every changed file paged in from
// the API (gh pr view stops at 100). --range reads the changed files from
// git and the issue labels through gh. Exit 1 with a one-line reason when a test
// is missing, 0 otherwise; --advisory prints the same line and exits 0. Exit 2
// when the inputs cannot be read: a file list shorter than the PR's file count, an
// issue whose labels fail to load, or test files GitHub sends without line counts.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// File is one changed file and the lines it adds.
type File struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Status    string `json:"status,omitempty"`   // GitHub's added/modified/removed/renamed; empty from git
	NoCounts  bool   `json:"noCounts,omitempty"` // GitHub gave no line counts (it drops them on very large PRs)
}

// Input is everything the check looks at.
type Input struct {
	Title  string
	Body   string
	Files  []File
	Linked []int                       // issues in the same repo GitHub links as closed by the PR
	PRLabs []string                    // labels on the PR itself (--pr only)
	Labels func(int) ([]string, error) // labels of an issue (gh in main, a fixture in tests)
}

var (
	fixTitle = regexp.MustCompile(`(?i)^\s*(bug|hot)?fix\b`)
	// GitHub closing keywords followed by #n, owner/repo#n or an issue URL.
	closeRef = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+` +
		`(?:https://github\.com/([\w.-]+/[\w.-]+)/issues/|([\w.-]+/[\w.-]+)?#)(\d+)\b`)
	docExt = map[string]bool{".md": true, ".txt": true, ".rst": true, ".png": true, ".jpg": true, ".gif": true, ".svg": true}
)

// ClosedIssues returns the issue numbers the body closes in repo (or in no named repo), sorted.
func ClosedIssues(body, repo string) []int {
	seen := map[int]bool{}
	for _, m := range closeRef.FindAllStringSubmatch(body, -1) {
		other := m[1] + m[2]
		if other != "" && !strings.EqualFold(other, repo) {
			continue
		}
		if n, err := strconv.Atoi(m[3]); err == nil {
			seen[n] = true
		}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// IsTestFile reports whether p is test code or test data.
func IsTestFile(p string) bool {
	base := path.Base(p)
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	for _, seg := range strings.Split(path.Dir(p), "/") {
		if seg == "tests" || seg == "testdata" || seg == "__tests__" {
			return true
		}
	}
	parts := strings.Split(base, ".") // name.test.ts, name.spec.js: a middle part
	for i := 1; i < len(parts)-1; i++ {
		if parts[i] == "test" || parts[i] == "spec" {
			return true
		}
	}
	return false
}

// isDoc reports whether p needs no regression test: documentation, or CI
// configuration under .github/ (a CI fix has no Go test to write). A .md file
// outside the docs trees and the repo root can ship in the binary (go:embed
// templates, skills), so it is not a doc.
func isDoc(p string) bool {
	if strings.HasPrefix(p, ".github/") || strings.HasPrefix(p, "docs/") || strings.HasPrefix(p, "documentation/") || strings.HasPrefix(p, "site/") {
		return true
	}
	return !strings.Contains(p, "/") && (docExt[strings.ToLower(path.Ext(p))] || p == "LICENSE")
}

// IsBugLabel reports whether a label marks a bug report.
func IsBugLabel(l string) bool {
	l = strings.ToLower(strings.TrimSpace(l))
	return l == "bug" || strings.HasSuffix(l, ":bug") || strings.HasSuffix(l, "/bug") || strings.HasSuffix(l, ": bug")
}

// Check returns ("", nil) when the PR is fine and a one-line reason when it needs a test.
func Check(in Input, repo string) (string, error) {
	var why []string
	if fixTitle.MatchString(in.Title) {
		why = append(why, "title is a fix")
	}
	issues := map[int]bool{}
	for _, n := range in.Linked {
		issues[n] = true
	}
	for _, n := range ClosedIssues(in.Body, repo) {
		issues[n] = true
	}
	nums := make([]int, 0, len(issues))
	for n := range issues {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var bugs []string
	for _, n := range nums {
		bug := false
		if in.Labels != nil {
			labels, err := in.Labels(n)
			if err != nil {
				return "", fmt.Errorf("labels of #%d: %w", n, err)
			}
			for _, l := range labels {
				bug = bug || IsBugLabel(l)
			}
		}
		if bug {
			bugs = append(bugs, "#"+strconv.Itoa(n))
		}
	}
	if len(bugs) > 0 {
		why = append(why, "closes bug "+strings.Join(bugs, ", "))
	}
	if len(why) == 0 {
		return "", nil
	}
	docsOnly := len(in.Files) > 0
	var uncounted []string
	for _, f := range in.Files {
		if IsTestFile(f.Path) && (f.Additions > 0 || f.Status == "added") {
			return "", nil
		}
		if IsTestFile(f.Path) && f.NoCounts && f.Status != "removed" {
			uncounted = append(uncounted, f.Path)
		}
		if !isDoc(f.Path) {
			docsOnly = false
		}
	}
	if docsOnly {
		return "", nil
	}
	if len(uncounted) > 0 {
		return "", fmt.Errorf("GitHub gives no line counts for %s (large PR): check by hand or use --range", strings.Join(uncounted, ", "))
	}
	return fmt.Sprintf("needs-regression-test: %s but no test file was added or changed (*_test.go, tests/, testdata/, *.test.*)",
		strings.Join(why, " and ")), nil
}

// Runner runs an external command and returns its stdout.
type Runner func(name string, args ...string) ([]byte, error)

func execRunner(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func ghLabels(run Runner, repo string) func(int) ([]string, error) {
	return func(n int) ([]string, error) {
		out, err := run("gh", "api", fmt.Sprintf("repos/%s/issues/%d", repo, n), "--jq", "[.labels[].name]")
		if err != nil {
			return nil, err
		}
		var labels []string
		err = json.Unmarshal(out, &labels)
		return labels, err
	}
}

func fromPR(run Runner, repo string, pr int) (Input, error) {
	out, err := run("gh", "pr", "view", strconv.Itoa(pr), "-R", repo, "--json", "title,body,changedFiles,closingIssuesReferences,labels")
	if err != nil {
		return Input{}, err
	}
	var v struct {
		Title        string `json:"title"`
		Body         string `json:"body"`
		ChangedFiles int    `json:"changedFiles"`
		Labels       []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Linked []struct {
			Number     int `json:"number"`
			Repository struct {
				Name  string `json:"name"`
				Owner struct {
					Login string `json:"login"`
				} `json:"owner"`
			} `json:"repository"`
		} `json:"closingIssuesReferences"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return Input{}, fmt.Errorf("gh pr view: %w", err)
	}
	in := Input{Title: v.Title, Body: v.Body}
	for _, l := range v.Labels {
		in.PRLabs = append(in.PRLabs, l.Name)
	}
	for _, l := range v.Linked {
		if r := l.Repository; r.Name != "" && !strings.EqualFold(r.Owner.Login+"/"+r.Name, repo) {
			continue // closes an issue in another repo: its number means something else here
		}
		in.Linked = append(in.Linked, l.Number)
	}
	// gh pr view lists at most 100 files; the REST endpoint pages through all of them.
	out, err = run("gh", "api", "--paginate", fmt.Sprintf("repos/%s/pulls/%d/files", repo, pr),
		"--jq", ".[] | {path: .filename, additions, status, noCounts: (.changes == 0 and .patch == null)}")
	if err != nil {
		return Input{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var f File
		if err := dec.Decode(&f); err == io.EOF {
			break
		} else if err != nil {
			return Input{}, fmt.Errorf("pull files: %w", err)
		}
		in.Files = append(in.Files, f)
	}
	if len(in.Files) != v.ChangedFiles {
		return Input{}, fmt.Errorf("read %d changed files, the PR has %d: the list is incomplete", len(in.Files), v.ChangedFiles)
	}
	return in, nil
}

func fromRange(run Runner, rng string) ([]File, error) {
	if !strings.Contains(rng, "..") {
		return nil, fmt.Errorf("--range wants base..head, got %q", rng)
	}
	base, head, _ := strings.Cut(strings.Replace(rng, "...", "..", 1), "..")
	out, err := run("git", "diff", "--numstat", "--no-renames", base+"..."+head)
	if err != nil {
		return nil, err
	}
	var files []File
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		add, _ := strconv.Atoi(f[0]) // "-" for binary files counts as 0
		files = append(files, File{Path: f[2], Additions: add})
	}
	return files, nil
}

func run(args []string, stdout io.Writer, runner Runner) int {
	fs := flag.NewFlagSet("check-fix-has-test", flag.ContinueOnError)
	fs.SetOutput(stdout)
	pr := fs.Int("pr", 0, "PR number (read with gh)")
	repo := fs.String("repo", "asheshgoplani/agent-deck", "owner/name")
	rng := fs.String("range", "", "base..head git range (with --title)")
	title := fs.String("title", "", "PR title (with --range)")
	body := fs.String("body", "", "PR body (with --range)")
	bodyFile := fs.String("body-file", "", "file holding the PR body (with --range)")
	advisory := fs.Bool("advisory", false, "print the finding and exit 0")
	redBase := fs.String("red-on-base", "", "checkout of the base commit: also require the changed Go tests to fail there and pass here (run from the head checkout)")
	siblings := fs.Bool("siblings", false, "fix PRs: report (advisory) files a \"## Siblings\" grep finds that the section does not name")
	siblingsRequired := fs.Bool("siblings-required", false, "like --siblings, but a finding fails the check")
	exemptLabel := fs.String("exempt-label", "", "with --pr: a PR label that skips the gate when a maintainer added it")
	fixOut := fs.String("fix-out", "", "write yes (a fix with a test file: run the red step) or no to this file")
	assumeFix := fs.Bool("assume-fix", false, "with --range and --red-on-base: skip classification (an earlier --fix-out said yes) and never call gh")
	baseSHA := fs.String("base", "", "base commit for --red-on-base with --pr (with --range the range's base is used)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stdout, "check-fix-has-test: %v\n", err)
		return 2
	}
	writeFix := func(v string) int {
		if *fixOut == "" {
			return 0
		}
		if err := os.WriteFile(*fixOut, []byte(v+"\n"), 0o600); err != nil {
			return fail(err)
		}
		return 0
	}
	if *assumeFix {
		if *rng == "" || *redBase == "" || *pr != 0 {
			return fail(errors.New("--assume-fix needs --range base..HEAD and --red-on-base, and no --pr"))
		}
		base, _, _ := strings.Cut(strings.Replace(*rng, "...", "..", 1), "..")
		ok, err := redOnBaseMode(runner, base, *redBase, stdout)
		if err != nil {
			return fail(err)
		}
		if !ok {
			return 1
		}
		return 0
	}
	var in Input
	switch {
	case *pr > 0 && *rng == "":
		var err error
		if in, err = fromPR(runner, *repo, *pr); err != nil {
			return fail(err)
		}
	case *pr == 0 && *rng != "":
		files, err := fromRange(runner, *rng)
		if err != nil {
			return fail(err)
		}
		in = Input{Title: *title, Body: *body, Files: files}
		if *bodyFile != "" {
			b, err := os.ReadFile(*bodyFile)
			if err != nil {
				return fail(err)
			}
			in.Body = string(b)
		}
	default:
		return fail(errors.New("give either --pr N or --range base..head --title T"))
	}
	if *exemptLabel != "" && *pr > 0 {
		exempt, line, err := exemptBy(runner, *repo, *pr, *exemptLabel, in.PRLabs)
		if err != nil {
			return fail(err)
		}
		if line != "" {
			fmt.Fprintln(stdout, line)
		}
		if exempt {
			return writeFix("no")
		}
	}
	in.Labels = ghLabels(runner, *repo)
	reason, err := Check(in, *repo)
	if err != nil {
		return fail(err)
	}
	if *fixOut != "" {
		fix, err := isFix(in, *repo)
		if err != nil {
			return fail(err)
		}
		v := "no"
		if fix && reason == "" {
			v = "yes"
		}
		if c := writeFix(v); c != 0 {
			return c
		}
	}
	code := 0
	if *siblings || *siblingsRequired {
		fix, err := isFix(in, *repo)
		if err != nil {
			return fail(err)
		}
		if fix {
			var changed []string
			for _, f := range in.Files {
				if f.Status != "removed" && !IsTestFile(f.Path) {
					changed = append(changed, f.Path)
				}
			}
			found, err := CheckSiblings(in.Body, changed, runner)
			if err != nil {
				return fail(err)
			}
			for _, f := range found {
				if *siblingsRequired {
					fmt.Fprintln(stdout, f)
				} else {
					fmt.Fprintln(stdout, "advisory: "+f)
				}
			}
			if len(found) > 0 && *siblingsRequired {
				code = 1
			}
		}
	}
	switch {
	case reason != "":
		fmt.Fprintln(stdout, reason)
		code = 1
	case *redBase != "":
		if c := redStep(in, *repo, *rng, *baseSHA, *redBase, stdout, runner); c > code {
			code = c
		}
	default:
		fmt.Fprintln(stdout, "ok: no regression test needed or one is included")
	}
	if *advisory && code == 1 {
		return 0
	}
	return code
}

// isFix reports whether the PR is a fix that is not docs-only: Check asks for a
// test for the same PR with its test files taken out exactly then.
func isFix(in Input, repo string) (bool, error) {
	code := in
	code.Files = nil
	for _, f := range in.Files {
		if !IsTestFile(f.Path) {
			code.Files = append(code.Files, f)
		}
	}
	why, err := Check(code, repo)
	return why != "", err
}

// redStep runs the red-on-base check for a PR that passed the has-a-test check.
// It returns the exit code: 0 ok or not a fix, 1 not red on base, 2 unreadable.
func redStep(in Input, repo, rng, baseSHA, baseDir string, stdout io.Writer, runner Runner) int {
	fix, err := isFix(in, repo)
	if err != nil {
		fmt.Fprintf(stdout, "check-fix-has-test: %v\n", err)
		return 2
	}
	if !fix {
		fmt.Fprintln(stdout, "ok: not a fix PR (or docs and tests only): red-on-base not required")
		return 0
	}
	if rng != "" && baseSHA == "" {
		baseSHA, _, _ = strings.Cut(strings.Replace(rng, "...", "..", 1), "..")
	}
	if baseSHA == "" {
		fmt.Fprintln(stdout, "check-fix-has-test: --red-on-base needs --base SHA (or --range)")
		return 2
	}
	ok, err := redOnBaseMode(runner, baseSHA, baseDir, stdout)
	if err != nil {
		fmt.Fprintf(stdout, "check-fix-has-test: %v\n", err)
		return 2
	}
	if !ok {
		return 1
	}
	return 0
}

// exemptBy reports whether label is on the PR and was added by someone with
// admin or maintain permission, with a line saying who. Anyone with triage
// access can add a label, so the label alone proves nothing.
func exemptBy(run Runner, repo string, pr int, label string, labels []string) (bool, string, error) {
	on := false
	for _, l := range labels {
		on = on || l == label
	}
	if !on {
		return false, "", nil
	}
	out, err := run("gh", "api", "--paginate", fmt.Sprintf("repos/%s/issues/%d/events", repo, pr),
		"--jq", fmt.Sprintf(`.[] | select(.event == "labeled" and .label.name == %q) | .actor.login`, label))
	if err != nil {
		return false, "", fmt.Errorf("reading who added %s: %w", label, err)
	}
	lines := strings.Fields(string(out))
	if len(lines) == 0 {
		return false, label + " ignored: no labeled event found", nil
	}
	actor := lines[len(lines)-1] // the latest add counts
	out, err = run("gh", "api", fmt.Sprintf("repos/%s/collaborators/%s/permission", repo, actor), "--jq", ".permission")
	if err != nil {
		return false, "", fmt.Errorf("reading %s's permission: %w", actor, err)
	}
	perm := strings.TrimSpace(string(out))
	if perm == "admin" || perm == "maintain" {
		return true, fmt.Sprintf("exempt: %s added by %s (%s); the gate is skipped and the PR body must say how the fix was verified", label, actor, perm), nil
	}
	return false, fmt.Sprintf("%s ignored: added by %s (%s), not a maintainer", label, actor, perm), nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, execRunner))
}
