// Package releasetests pins the integrity of .claude/release-tests.yaml
// (the accumulating regression manifest). Two invariants:
//
//  1. The file must be parseable by a strict YAML 1.2 parser. PR #610
//     shipped a manifest that truncated mid-scalar because inline "#610"
//     triggered YAML comment rules. TestManifestIsValidYAML catches this
//     class of error at build time.
//
//  2. Every non-manual entry with a `file:` field must point at a real
//     source file containing a Go test function matching `test_name:`.
//     PR #640 silently deleted two referenced #598 regression tests
//     during a rebase conflict. TestManifestReferencesExistInSource
//     blocks that class of regression.
package releasetests

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// repoRoot walks up from this test file until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(self)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not locate go.mod walking up from " + self)
	return ""
}

type manifestEntry struct {
	ID       string `yaml:"id"`
	Task     string `yaml:"task"`
	File     string `yaml:"file"`
	TestName string `yaml:"test_name"`
	Manual   bool   `yaml:"manual"`
}

type manifestDoc struct {
	Version int             `yaml:"version"`
	Tests   []manifestEntry `yaml:"tests"`
}

func loadManifest(t *testing.T) (*manifestDoc, string) {
	t.Helper()
	root := repoRoot(t)
	path := filepath.Join(root, ".claude", "release-tests.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var doc manifestDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return &doc, root
}

// TestManifestIsValidYAML asserts .claude/release-tests.yaml is parseable
// by a strict YAML 1.2 parser (gopkg.in/yaml.v3) and deserialises into the
// expected shape. Guards against inline "# NNN" comment truncation and
// similar foot-guns.
func TestManifestIsValidYAML(t *testing.T) {
	doc, _ := loadManifest(t)
	if doc.Version != 1 {
		t.Errorf("manifest version: got %d want 1", doc.Version)
	}
	if len(doc.Tests) == 0 {
		t.Fatal("manifest has zero tests; parser likely truncated silently")
	}
	// Sanity: at least one purpose/source field must be readable on a
	// known-good entry. If a scalar truncation happened, later entries
	// would have corrupted task/file fields.
	for _, e := range doc.Tests {
		if e.ID == "" {
			t.Errorf("entry with empty id: %+v", e)
		}
	}
}

// funcDeclRE matches a test-function declaration at a line start (ignoring
// leading whitespace). Supports both Go (`func <name>(`) with an optional
// receiver, and Python (`def <name>(`) — the latter indented inside a
// TestCase class. The Python branch is needed because v1.7.63 added the
// first repo-gated Python tests under scripts/watchdog/ and those entries
// would otherwise fail the drift check with spurious "missing func" errors.
func funcDeclForName(name string) *regexp.Regexp {
	quoted := regexp.QuoteMeta(name)
	return regexp.MustCompile(
		`(?m)^func(?:\s+\([^)]*\))?\s+` + quoted + `\s*\(` +
			`|(?m)^\s*def\s+` + quoted + `\s*\(`,
	)
}

// TestManifestReferencesExistInSource asserts that for every non-manual
// manifest entry with a concrete file path, the file exists AND contains
// a Go function declaration matching test_name. Fails the build when the
// manifest drifts from source (e.g. a rebase silently deletes a peer's
// test).
func TestManifestReferencesExistInSource(t *testing.T) {
	doc, root := loadManifest(t)
	var drifts []string
	for _, e := range doc.Tests {
		if e.Manual {
			continue
		}
		if e.File == "" {
			continue
		}
		// Skip entries that explicitly mark themselves as non-file
		// manual artefacts (defensive; schema says manual:true should
		// cover this, but some older entries used "(manual — …)" in
		// the file field).
		if strings.HasPrefix(strings.TrimSpace(e.File), "(") ||
			strings.EqualFold(strings.TrimSpace(e.File), "manual-live-test") {
			continue
		}
		absFile := filepath.Join(root, e.File)
		src, err := os.ReadFile(absFile)
		if err != nil {
			drifts = append(drifts, "id="+e.ID+" file="+e.File+" MISSING ("+err.Error()+")")
			continue
		}
		if e.TestName == "" {
			drifts = append(drifts, "id="+e.ID+" EMPTY test_name")
			continue
		}
		re := funcDeclForName(e.TestName)
		if !re.Match(src) {
			drifts = append(drifts, "id="+e.ID+" file="+e.File+" missing func "+e.TestName)
		}
	}
	if len(drifts) > 0 {
		t.Fatalf("manifest drift detected (%d entries):\n  %s",
			len(drifts), strings.Join(drifts, "\n  "))
	}
}
