package scripts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestOperationalToolchainPinsMatchGoMod(t *testing.T) {
	repoRoot := filepath.Clean("..")
	goMod, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	goDirective := regexp.MustCompile(`(?m)^go (\d+\.\d+\.\d+)$`).FindSubmatch(goMod)
	if goDirective == nil {
		t.Fatal("go.mod has no three-part go directive")
	}
	want := "go" + string(goDirective[1])
	pinPattern := regexp.MustCompile(`go\d+\.\d+\.\d+`)

	files := []string{
		"Makefile",
		".github/workflows/lighthouse-ci.yml",
		".flox/env/manifest.toml",
		".flox/env/manifest.lock",
		"scripts/verify-preview-ansi-bleed.sh",
		"scripts/verify-watcher-framework.sh",
		"tests/web/helpers/global-setup.js",
		"tests/eval/README.md",
		"tests/lighthouse/README.md",
		"docs/perf-budget-suite.md",
	}

	for _, name := range files {
		contents, err := os.ReadFile(filepath.Join(repoRoot, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, pin := range pinPattern.FindAllString(string(contents), -1) {
			if pin != want {
				t.Errorf("%s pins %s, want %s from go.mod", name, pin, want)
			}
		}
		if strings.Contains(string(contents), "GOTOOLCHAIN") && !strings.Contains(string(contents), want) {
			t.Errorf("%s configures GOTOOLCHAIN without the go.mod version %s", name, want)
		}
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
