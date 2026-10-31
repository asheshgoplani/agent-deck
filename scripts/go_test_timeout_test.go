package scripts

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// localWholeSuiteEntryPoints lists the files that hand a contributor (or the
// pre-push hook, or a local release) a `go test` over the whole module or a
// computed package list. Go's default per-package timeout is 10m, and with
// -race the internal/session package alone runs about 815s on a hosted CI
// runner (go-test.yml session shard, run 41158827633), so a bare
// `go test -race ./...` panics with "test timed out after 10m0s" and reports
// `FAIL .../internal/session 60x.xxxs` even though nothing is broken. CI
// never sees this because go-test.yml passes -timeout 20m; these entry points
// must do the same.
var localWholeSuiteEntryPoints = []string{
	"Makefile",
	"lefthook.yml",
	"skills/agent-deck/scripts/contributing/self-check.sh",
}

var (
	goTestInvocation = regexp.MustCompile(`\bgo test\b[^\n]*`)
	goTestTimeoutArg = regexp.MustCompile(`(?:^|\s)-{1,2}timeout[= ](\S+)`)
	goTestRunArg     = regexp.MustCompile(`(?:^|\s)-{1,2}run[= ]`)
	ciGotestsumLine  = regexp.MustCompile(`gotestsum [^\n]*-- [^\n]*-timeout (\S+)`)
	// The shards may pass the go test flags through a bash array:
	//   args=(-race -timeout 20m) ... gotestsum ... -- "${args[@]}"
	ciGotestsumArgs  = regexp.MustCompile(`gotestsum [^\n]*-- "\$\{args\[@\]\}"`)
	ciArgsTimeout    = regexp.MustCompile(`\bargs=\([^)\n]*-timeout ([^\s)]+)`)
	shellPackageList = regexp.MustCompile(`\$\{?[A-Za-z_][A-Za-z0-9_]*`)
)

// ciPackageTimeout returns the per-package -timeout the go-test.yml shards use.
func ciPackageTimeout(repoRoot string) (time.Duration, error) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".github/workflows/go-test.yml"))
	if err != nil {
		return 0, err
	}
	return ciTimeoutFromWorkflow(raw)
}

// ciTimeoutFromWorkflow finds the -timeout in the shards' gotestsum call,
// written either inline after `--` or in the bash array that call expands.
func ciTimeoutFromWorkflow(raw []byte) (time.Duration, error) {
	if m := ciGotestsumLine.FindSubmatch(raw); m != nil {
		return time.ParseDuration(string(m[1]))
	}
	if ciGotestsumArgs.Match(raw) {
		if m := ciArgsTimeout.FindSubmatch(raw); m != nil {
			return time.ParseDuration(string(m[1]))
		}
	}
	return 0, fmt.Errorf("go-test.yml: no gotestsum ... -- ... -timeout <d> line")
}

func TestCITimeoutFromWorkflowReadsInlineAndArrayForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want time.Duration
		ok   bool
	}{
		{"inline", "gotestsum --packages=\"$P\" -- -race -timeout 20m\n", 20 * time.Minute, true},
		{"bash array", "args=(-race -timeout 25m)\nif x; then args+=(-run y); fi\ngotestsum --packages=\"$P\" -- \"${args[@]}\"\n", 25 * time.Minute, true},
		{"array without timeout", "args=(-race)\ngotestsum -- \"${args[@]}\"\n", 0, false},
		{"no gotestsum", "args=(-race -timeout 20m)\ngo test \"${args[@]}\"\n", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ciTimeoutFromWorkflow([]byte(tc.in))
			if (err == nil) != tc.ok || got != tc.want {
				t.Fatalf("got %v, %v; want %v, ok=%v", got, err, tc.want, tc.ok)
			}
		})
	}
}

// joinContinuations folds backslash-continued lines so a `go test` spread
// over several Makefile or shell lines is checked as one command.
func joinContinuations(s string) string {
	return regexp.MustCompile(`\\\r?\n\s*`).ReplaceAllString(s, " ")
}

// goTestTimeoutProblems reports every `go test` in contents that runs the
// whole module (`./...`) or a computed package list (`$VAR`) without an
// explicit -timeout, and every such unfiltered (no -run) invocation whose
// -timeout is below want.
func goTestTimeoutProblems(name, contents string, want time.Duration) []string {
	var problems []string
	for _, cmd := range goTestInvocation.FindAllString(joinContinuations(contents), -1) {
		args := strings.TrimPrefix(cmd, "go test")
		if !strings.Contains(args, "./...") && !shellPackageList.MatchString(args) {
			continue // a named package or a single filtered run: bounded
		}
		m := goTestTimeoutArg.FindStringSubmatch(args)
		if m == nil {
			problems = append(problems, fmt.Sprintf("%s: %q has no -timeout; Go's 10m default kills internal/session under -race (CI uses -timeout %s)", name, strings.TrimSpace(cmd), want))
			continue
		}
		if goTestRunArg.MatchString(args) {
			continue // a -run filter picks a small subset; its own bound is fine
		}
		got, err := time.ParseDuration(m[1])
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %q has an unparsable -timeout %q", name, strings.TrimSpace(cmd), m[1]))
			continue
		}
		if got < want {
			problems = append(problems, fmt.Sprintf("%s: %q uses -timeout %s, below CI's %s", name, strings.TrimSpace(cmd), got, want))
		}
	}
	return problems
}

func TestLocalWholeSuiteGoTestSetsCITimeout(t *testing.T) {
	repoRoot := filepath.Clean("..")
	want, err := ciPackageTimeout(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range localWholeSuiteEntryPoints {
		raw, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !goTestInvocation.Match(raw) {
			t.Errorf("%s no longer runs go test; drop it from localWholeSuiteEntryPoints", name)
		}
		for _, p := range goTestTimeoutProblems(name, string(raw), want) {
			t.Error(p)
		}
	}
}

func TestGoTestTimeoutProblemsClassifiesInvocations(t *testing.T) {
	const want = 20 * time.Minute
	cases := []struct {
		name    string
		in      string
		problem bool
	}{
		{"bare whole module", "\tgo test -race -v ./...\n", true},
		{"whole module with CI timeout", "\tgo test -race -timeout 20m ./...\n", false},
		{"equals form", "\tgo test -race -timeout=20m ./...\n", false},
		{"whole module below CI", "\tgo test -race -timeout 10m ./...\n", true},
		{"computed package list", "OUT=$(sandboxed go test $PKGS 2>&1)\n", true},
		{"computed list with timeout", "OUT=$(sandboxed go test -timeout 20m $PKGS 2>&1)\n", false},
		{"continued line without timeout", "\tgo test -race \\\n\t\t./...\n", true},
		{"continued line with timeout", "\tgo test -race -timeout 20m \\\n\t\t./...\n", false},
		{"filtered run with short timeout", "\tgo test -run '^TestPerf_' -timeout 120s \\\n\t\t./...\n", false},
		{"named package", "\tgo test ./cmd/agent-deck/ -run 'TestX$$' -v\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := goTestTimeoutProblems("fixture", tc.in, want)
			if (len(got) > 0) != tc.problem {
				t.Fatalf("problems = %q, want problem=%v", got, tc.problem)
			}
		})
	}
}
