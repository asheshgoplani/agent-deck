package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionShowJSONReportsOMPForkCapability(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runAgentDeck(t, home, "add", "-t", "omp-show", "-c", "omp", "--no-parent", "--json", project)
	if code != 0 {
		t.Fatalf("add failed (%d): %s\n%s", code, stdout, stderr)
	}
	var added struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &added); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runAgentDeck(t, home, "session", "show", added.ID, "--json")
	if code != 0 {
		t.Fatalf("show failed (%d): %s\n%s", code, stdout, stderr)
	}
	var shown map[string]any
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatal(err)
	}
	if _, ok := shown["can_fork"]; !ok {
		t.Fatalf("OMP session show omitted can_fork: %s", stdout)
	}
}
