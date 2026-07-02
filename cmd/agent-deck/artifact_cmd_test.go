package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/artifact"
)

// `agent-deck artifact stamp <html>` writes a sidecar carrying the provenance
// the Fleet Console routes on. Explicit flags win; the artifact id and group
// derive from the path when omitted.
func TestRunArtifactStampWritesSidecar(t *testing.T) {
	root := t.TempDir()
	html := filepath.Join(root, "agent-deck", "perf.html")
	if err := os.MkdirAll(filepath.Dir(html), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(html, []byte("<h1>perf</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	args := []string{"stamp", html,
		"--session", "sess-123",
		"--title", "ARD Import Perf",
		"--profile", "personal",
	}
	if err := runArtifact(&out, "personal", args); err != nil {
		t.Fatalf("runArtifact: %v", err)
	}

	meta, ok, err := artifact.ReadMeta(html)
	if err != nil || !ok {
		t.Fatalf("sidecar not written: ok=%v err=%v", ok, err)
	}
	if meta.SessionID != "sess-123" {
		t.Fatalf("expected session sess-123, got %q", meta.SessionID)
	}
	if meta.Title != "ARD Import Perf" {
		t.Fatalf("expected title, got %q", meta.Title)
	}
	// Derived from the path when not passed explicitly.
	if meta.ArtifactID != "perf" {
		t.Fatalf("expected artifact id derived from filename (perf), got %q", meta.ArtifactID)
	}
	if meta.Group != "agent-deck" {
		t.Fatalf("expected group derived from conductor dir (agent-deck), got %q", meta.Group)
	}
	if meta.CreatedAt == "" {
		t.Fatalf("expected a created_at timestamp")
	}
}

func TestRunArtifactStampRequiresFile(t *testing.T) {
	var out bytes.Buffer
	if err := runArtifact(&out, "personal", []string{"stamp"}); err == nil {
		t.Fatal("expected error when no html path is given")
	}
}
