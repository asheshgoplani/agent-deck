package session

import (
	"os"
	"path/filepath"
	"testing"
)

// claudeProjectDir is the bound between a caller-supplied project path (the
// web API accepts one in a request body) and the directory the transcript
// lookups read. Only the single-component alphabet ConvertToClaudeDirName
// emits may pass; anything with a separator or ".." must be refused.
func TestClaudeProjectDir_AcceptsOnlyEncodedNames(t *testing.T) {
	configDir := t.TempDir()
	for _, encoded := range []string{
		ConvertToClaudeDirName("/Users/me/Code cloud/!Project"),
		ConvertToClaudeDirName("../../etc"),
		"-",
	} {
		got, ok := claudeProjectDir(configDir, encoded)
		if !ok {
			t.Fatalf("claudeProjectDir(%q) refused an encoded name", encoded)
		}
		want := filepath.Join(configDir, "projects", encoded)
		if got != want {
			t.Fatalf("claudeProjectDir(%q) = %q, want %q", encoded, got, want)
		}
	}
	for _, raw := range []string{"", "..", "../x", "a/b", "a\\b", ".", "a b", "\x00"} {
		if got, ok := claudeProjectDir(configDir, raw); ok {
			t.Fatalf("claudeProjectDir(%q) = %q, want refused", raw, got)
		}
	}
}

// A traversal-shaped project path is encoded to a single component before
// any read, so a transcript planted outside the projects tree is never seen
// and never counts as uncertain evidence.
func TestTranscriptEvidenceAcrossRoots_TraversalNeverLeavesProjectsTree(t *testing.T) {
	configDir := t.TempDir()
	outside := filepath.Join(configDir, "etc")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(outside, "12345678-1234-1234-1234-123456789abc.jsonl")
	if err := os.WriteFile(planted, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, uncertain := transcriptEvidenceAcrossRoots("../etc", []string{configDir})
	if found || uncertain {
		t.Fatalf("found=%v uncertain=%v, want a confirmed absence for a traversal path", found, uncertain)
	}
	encoded := filepath.Join(configDir, "projects", ConvertToClaudeDirName("../etc"))
	if err := os.MkdirAll(encoded, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(encoded, "12345678-1234-1234-1234-123456789abc.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, uncertain = transcriptEvidenceAcrossRoots("../etc", []string{configDir})
	if !found || uncertain {
		t.Fatalf("found=%v uncertain=%v, want the encoded project dir to be read", found, uncertain)
	}
}
