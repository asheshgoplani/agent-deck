// Command check-fix-has-test flags a fix PR that ships without a regression test.
//
// A PR needs a test when its title is a conventional fix (fix:, fix(scope):, fix!:)
// or when it closes an issue labelled bug (Closes/Fixes/Resolves #n, or GitHub's
// linked issues). It has one when it adds lines to a test file: *_test.go, a file
// under a tests/ or testdata/ directory, or a *.test.* / *.spec.* file. A PR that
// changes only docs (docs/, documentation/, site/, top-level *.md and LICENSE) is
// not asked for a test. Markdown elsewhere, such as embedded templates, is code.
//
// Usage:
//
//	check-fix-has-test --pr 123 [--repo owner/name] [--advisory]
//	check-fix-has-test --range origin/main..HEAD --title "fix: ..." [--body-file f] [--advisory]
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
	Labels func(int) ([]string, error) // labels of an issue (gh in main, a fixture in tests)
}

var (
	fixTitle = regexp.MustCompile(`(?i)^\s*fix(\([^)]*\))?!?:`)
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

// isDoc reports whether p is documentation only. A .md file outside the docs
// trees and the repo root can ship in the binary (go:embed templates, skills),
// so it is not a doc.
func isDoc(p string) bool {
	if strings.HasPrefix(p, "docs/") || strings.HasPrefix(p, "documentation/") || strings.HasPrefix(p, "site/") {
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
	out, err := run("gh", "pr", "view", strconv.Itoa(pr), "-R", repo, "--json", "title,body,changedFiles,closingIssuesReferences")
	if err != nil {
		return Input{}, err
	}
	var v struct {
		Title        string `json:"title"`
		Body         string `json:"body"`
		ChangedFiles int    `json:"changedFiles"`
		Linked       []struct {
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stdout, "check-fix-has-test: %v\n", err)
		return 2
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
	in.Labels = ghLabels(runner, *repo)
	reason, err := Check(in, *repo)
	if err != nil {
		return fail(err)
	}
	if reason == "" {
		fmt.Fprintln(stdout, "ok: no regression test needed or one is included")
		return 0
	}
	fmt.Fprintln(stdout, reason)
	if *advisory {
		return 0
	}
	return 1
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, execRunner))
}
