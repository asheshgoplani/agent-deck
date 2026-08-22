package artifact

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckRereadsEveryGatePassSignal(t *testing.T) {
	for _, gateID := range []GateID{G1, G2, G3, G4, G5} {
		t.Run(string(gateID), func(t *testing.T) {
			repo := t.TempDir()
			if err := os.WriteFile(filepath.Join(repo, "owned.go"), []byte("package owned\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			tree := NewTree(repo, "", "feature")
			writePassingGateTree(t, tree)

			verdict, err := Build(BuildInput{
				Tree: tree, Slug: tree.Slug, Sentence: "works", Owns: []string{"owned.go"},
				Tool: "test", Now: time.Unix(0, 0), G0Status: StatusPass,
			})
			if err != nil {
				t.Fatal(err)
			}
			if verdict.Overall != StatusPass {
				t.Fatalf("fixture verdict = %s, want PASS", verdict.Overall)
			}
			if err := Write(tree, verdict); err != nil {
				t.Fatal(err)
			}

			gate, _ := GateByID(gateID)
			matches, err := filepath.Glob(filepath.Join(tree.GateDir(gate), filepath.FromSlash(gate.PassSignal)))
			if err != nil || len(matches) == 0 {
				t.Fatalf("locate %s pass signal: matches=%v err=%v", gateID, matches, err)
			}
			if err := os.WriteFile(matches[0], []byte("{\"pass\":false}\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			checked, err := Check(tree, []string{"owned.go"})
			if err != nil {
				t.Fatal(err)
			}
			if checked.OK() {
				t.Fatalf("Check accepted a stale PASS after %s pass signal changed to false", gateID)
			}
			if !strings.Contains(strings.Join(checked.Problems, "\n"), "gate "+string(gateID)) {
				t.Fatalf("problems %q do not identify %s", checked.Problems, gateID)
			}
		})
	}
}

func writePassingGateTree(t *testing.T, tree Tree) {
	t.Helper()
	files := map[string]string{
		ScriptFile:                      "slug: feature\n",
		"G1-drive/run/transcript.md":    "transcript\n",
		"G1-drive/run/run.json":         "{\"pass\":true}\n",
		"G1-drive/run/final.screen.txt": "screen\n",
		"G2-assert/results.json":        "{\"pass\":true}\n",
		"G2-assert/results.md":          "results\n",
		"G3-matrix/matrix.json":         "{\"pass\":true}\n",
		"G3-matrix/matrix.md":           "matrix\n",
		"G4-oracle/parity.json":         "{\"pass\":true}\n",
		"G4-oracle/parity.md":           "parity\n",
		"G5-coldeye/brief.md":           "brief\n",
		"G5-coldeye/report.md":          "report\n",
		"G5-coldeye/outcome.json":       "{\"pass\":true}\n",
	}
	for rel, contents := range files {
		path := filepath.Join(tree.Root(), filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
