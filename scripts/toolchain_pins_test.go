package scripts

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// operationalPinFiles lists every operative file that pins the Go toolchain
// by exact version, either as go1.X.Y (GOTOOLCHAIN values, image tags) or as a
// bare X.Y.Z (setup-go `go-version:` inputs, `golang:` base images). A file
// with no pin left in it (for example after moving to go-version-file) passes.
var operationalPinFiles = []string{
	"Makefile",
	".goreleaser.yml",
	".github/workflows/lighthouse-ci.yml",
	".github/workflows/release.yml",
	".github/workflows/eval-smoke.yml",
	".github/workflows/functional-check.yml",
	".github/workflows/web-tests.yml",
	".github/workflows/weekly-regression.yml",
	".github/workflows/tmux-systemd-user-acceptance.yml",
	".github/workflows/golangci-lint.yml",
	".github/workflows/govulncheck.yml",
	".github/workflows/perf-smoke.yml",
	".github/workflows/hooks-fd-macos.yml",
	".github/workflows/bench-fleet.yml",
	".flox/env/manifest.toml",
	".flox/env/manifest.lock",
	"scripts/verify-preview-ansi-bleed.sh",
	"scripts/verify-watcher-framework.sh",
	"tests/comms_matrix/Dockerfile",
	"bench/Dockerfile",
	"bench/run-docker.sh",
	"bench/README.md",
	"scripts/runtime-health/Dockerfile",
	"scripts/ci/status-pass-regression.sh",
	"tests/web/helpers/global-setup.js",
	"tests/web/e2e/mcp-auth.spec.js",
	"tests/eval/README.md",
	"tests/lighthouse/README.md",
	"docs/perf-budget-suite.md",
}

var (
	goModDirective = regexp.MustCompile(`(?m)^go (\d+\.\d+\.\d+)$`)
	goToolchainPin = regexp.MustCompile(`go(\d+\.\d+\.\d+)`)
	bareGoPin      = regexp.MustCompile(`(?:go-version:\s*['"]?|golang:)(\d+\.\d+\.\d+)`)

	makefileGolangciVersion = regexp.MustCompile(`(?m)^GOLANGCI_LINT_VERSION\s*[:?]?=\s*(\S+)\s*$`)
	golangciActionUse       = regexp.MustCompile(`uses:\s*golangci/golangci-lint-action@`)
	golangciActionVersion   = regexp.MustCompile(`(?m)(?:^|[\s{,])version:\s*['"]?([^\s,'"}]+)`)

	gotestsumInstall = regexp.MustCompile(`gotest\.tools/gotestsum@(v\d+\.\d+\.\d+)`)
)

// toolchainPinProblems reports every exact Go pin in files that differs from
// the go directive in repoRoot/go.mod.
func toolchainPinProblems(repoRoot string, files []string) ([]string, error) {
	goMod, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		return nil, fmt.Errorf("read go.mod: %w", err)
	}
	directive := goModDirective.FindSubmatch(goMod)
	if directive == nil {
		return nil, fmt.Errorf("go.mod has no three-part go directive")
	}
	want := string(directive[1])

	var problems []string
	for _, name := range files {
		contents, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		text := string(contents)
		pinsWant := false
		for _, pattern := range []*regexp.Regexp{goToolchainPin, bareGoPin} {
			for _, m := range pattern.FindAllStringSubmatch(text, -1) {
				if m[1] != want {
					problems = append(problems, fmt.Sprintf("%s pins %q, want go%s from go.mod", name, m[0], want))
				} else {
					pinsWant = true
				}
			}
		}
		// GOTOOLCHAIN=local is fine when setup-go pins the version instead.
		if strings.Contains(text, "GOTOOLCHAIN") && !pinsWant {
			problems = append(problems, fmt.Sprintf("%s configures GOTOOLCHAIN without the go.mod version go%s", name, want))
		}
	}
	return problems, nil
}

// golangciLintVersionProblems reports a mismatch between the Makefile's
// GOLANGCI_LINT_VERSION and the version input of the golangci-lint action.
func golangciLintVersionProblems(repoRoot string) ([]string, error) {
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		return nil, fmt.Errorf("read Makefile: %w", err)
	}
	local := makefileGolangciVersion.FindSubmatch(makefile)
	if local == nil {
		return []string{"Makefile does not set GOLANGCI_LINT_VERSION"}, nil
	}

	const workflowPath = ".github/workflows/golangci-lint.yml"
	workflow, err := os.ReadFile(filepath.Join(repoRoot, workflowPath))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", workflowPath, err)
	}
	use := golangciActionUse.FindIndex(workflow)
	if use == nil {
		return []string{workflowPath + " does not use golangci/golangci-lint-action"}, nil
	}
	ci := golangciActionVersion.FindSubmatch(workflow[use[1]:])
	if ci == nil {
		return []string{workflowPath + " golangci-lint-action has no version input"}, nil
	}

	if string(local[1]) != string(ci[1]) {
		return []string{fmt.Sprintf("golangci-lint versions disagree: Makefile GOLANGCI_LINT_VERSION=%s, %s version: %s", local[1], workflowPath, ci[1])}, nil
	}
	return nil, nil
}

// gotestsumVersionProblems reports when the workflows install more than one
// gotestsum version, or when a listed workflow no longer installs it at all.
func gotestsumVersionProblems(repoRoot string, files []string) ([]string, error) {
	var problems []string
	seen := map[string][]string{}
	for _, name := range files {
		contents, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		matches := gotestsumInstall.FindAllSubmatch(contents, -1)
		if len(matches) == 0 {
			problems = append(problems, name+" installs no pinned gotestsum@vX.Y.Z")
		}
		for _, m := range matches {
			v := string(m[1])
			if where := seen[v]; len(where) == 0 || where[len(where)-1] != name {
				seen[v] = append(where, name)
			}
		}
	}
	if len(seen) > 1 {
		versions := make([]string, 0, len(seen))
		for v, where := range seen {
			versions = append(versions, fmt.Sprintf("%s in %s", v, strings.Join(where, ", ")))
		}
		sort.Strings(versions)
		problems = append(problems, "gotestsum versions disagree: "+strings.Join(versions, "; "))
	}
	return problems, nil
}

func TestOperationalToolchainPinsMatchGoMod(t *testing.T) {
	repoRoot := filepath.Clean("..")
	problems, err := toolchainPinProblems(repoRoot, operationalPinFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}

	workflow, err := os.ReadFile(filepath.Join(repoRoot, ".github/workflows/lighthouse-ci.yml"))
	if err != nil {
		t.Fatalf("read Lighthouse workflow: %v", err)
	}
	const forcedBuild = `run: make GOTOOLCHAIN="$GOTOOLCHAIN" build`
	if got := strings.Count(string(workflow), forcedBuild); got != 2 {
		t.Errorf("Lighthouse workflow has %d forced-toolchain build commands, want 2", got)
	}
}

func TestGolangciLintVersionAgreement(t *testing.T) {
	problems, err := golangciLintVersionProblems(filepath.Clean(".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func TestGotestsumVersionAgreement(t *testing.T) {
	problems, err := gotestsumVersionProblems(filepath.Clean(".."), []string{
		".github/workflows/go-test.yml",
		".github/workflows/release.yml",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}
