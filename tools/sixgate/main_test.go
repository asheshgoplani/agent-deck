package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestScaffoldRejectsPathsOutsideRepository(t *testing.T) {
	repo := t.TempDir()
	escape := filepath.Clean(filepath.Join(repo, "../../sixgate-escape"))
	code, err := cmdScaffold([]string{"../../" + filepath.Base(escape), "-repo", repo})
	if err == nil || code != exitUsage {
		t.Fatalf("cmdScaffold returned code=%d err=%v, want usage error", code, err)
	}
	if _, statErr := os.Stat(filepath.Join(escape, "G0-script.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("scaffold wrote outside repository at %s", escape)
	}
}

func TestResolveTreeRejectsUnsafePathParts(t *testing.T) {
	repo := t.TempDir()
	cases := []struct{ gates, slug string }{
		{"../gates", "feature"},
		{filepath.Join(repo, "absolute"), "feature"},
		{"docs/gates", "../feature"},
		{"docs/gates", "nested/feature"},
		{"docs/gates", `nested\feature`},
	}
	for _, tc := range cases {
		if tree, err := resolveTree(repo, tc.gates, tc.slug); err == nil {
			t.Errorf("resolveTree(%q, %q) = %q, want error", tc.gates, tc.slug, tree.Root())
		}
	}
}

func TestContextInspectorReadmeReferencesCommittedEvidence(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Clean(filepath.Join(filepath.Dir(file), "../../docs/gates/context-inspector"))
	raw, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(raw)
	link := regexp.MustCompile(`\[[^]]+\]\(([^)]+)\)`)
	for _, match := range link.FindAllStringSubmatch(readme, -1) {
		if strings.Contains(match[1], "://") || strings.HasPrefix(match[1], "#") {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(match[1]))); err != nil {
			t.Errorf("README link target %s is absent: %v", match[1], err)
		}
	}
	if strings.Contains(readme, "all six SIXGATE gates") {
		for _, name := range []string{"G1-drive", "G2-assert/results.json", "G3-matrix/matrix.json", "G4-oracle/parity.json", "G5-coldeye/outcome.json", "VERDICT.json", "VERDICT.md"} {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
				t.Errorf("README claims a completed six-gate example but %s is absent: %v", name, err)
			}
		}
	}
}
