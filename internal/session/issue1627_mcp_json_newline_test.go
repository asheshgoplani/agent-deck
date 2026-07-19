package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteJSONFileAtomic_AppendsTrailingNewline verifies the helper always
// leaves exactly one trailing newline. json.MarshalIndent emits none, which was
// the root cause of issue #1627 (a persistent one-byte git diff after restart).
func TestWriteJSONFileAtomic_AppendsTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")

	if err := writeJSONFileAtomic(path, []byte(`{"mcpServers":{}}`), 0644); err != nil {
		t.Fatalf("writeJSONFileAtomic: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got) == 0 || got[len(got)-1] != '\n' {
		t.Fatalf("expected trailing newline, got %q", string(got))
	}
	if strings.HasSuffix(string(got), "\n\n") {
		t.Fatalf("expected exactly one trailing newline, got %q", string(got))
	}
}

// TestWriteJSONFileAtomic_PreservesExistingNewline verifies data that already
// ends in a newline is not double-terminated.
func TestWriteJSONFileAtomic_PreservesExistingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")

	if err := writeJSONFileAtomic(path, []byte("{}\n"), 0644); err != nil {
		t.Fatalf("writeJSONFileAtomic: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "{}\n" {
		t.Fatalf("expected %q, got %q", "{}\n", string(got))
	}
}

// TestWriteJSONFileAtomic_SkipsUnchangedWrite verifies the atomic replace is
// skipped when the on-disk bytes already match. This is the second half of the
// issue #1627 fix: no needless filesystem churn (and no spurious inotify/mtime
// event) when nothing changed. The check is behavioral: after the first write,
// the directory is made non-writable, so any attempt to create the temp file
// would fail — a successful (nil) second call with identical content proves the
// write was skipped, while a call with different content must fail.
func TestWriteJSONFileAtomic_SkipsUnchangedWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")

	// data has no trailing newline; the helper appends one on the first write.
	data := []byte(`{"mcpServers":{}}`)
	if err := writeJSONFileAtomic(path, data, 0644); err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Make the directory non-writable so any temp-file creation would fail.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// Identical content (helper re-appends the same newline) => must skip, no write.
	if err := writeJSONFileAtomic(path, data, 0644); err != nil {
		t.Fatalf("expected unchanged write to be skipped, got error: %v", err)
	}

	// Different content => not skipped => must attempt a write, which fails on
	// the non-writable directory. This proves the skip is content-gated, not
	// unconditional.
	if err := writeJSONFileAtomic(path, []byte(`{"mcpServers":{"x":{}}}`), 0644); err == nil {
		t.Fatalf("expected changed write to attempt (and fail on read-only dir), got nil")
	}
}

// TestWriteMergedMcpJSONFile_NewlineAndIdempotent is the end-to-end regression
// for issue #1627: restarting a session (which calls the .mcp.json refresh) must
// leave the file newline-terminated and must not rewrite it when nothing
// changed. It starts from a newline-terminated file, runs the refresh once to
// reach canonical form, then asserts a second refresh is byte-identical and
// still newline-terminated.
func TestWriteMergedMcpJSONFile_NewlineAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	mcpFile := filepath.Join(dir, ".mcp.json")

	// Seed a project .mcp.json holding an entry agent-deck does not manage, so
	// it is preserved across refreshes. Terminate it with a newline, mirroring a
	// git-committed file.
	seed := "{\n  \"mcpServers\": {\n    \"external\": {\n      \"command\": \"foo\"\n    }\n  }\n}\n"
	if err := os.WriteFile(mcpFile, []byte(seed), 0644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	// First refresh: reach the canonical serialization.
	if err := WriteMergedMcpJSONFile(mcpFile, nil, ""); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	first, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatalf("read after first refresh: %v", err)
	}
	if len(first) == 0 || first[len(first)-1] != '\n' {
		t.Fatalf("expected trailing newline after refresh, got %q", string(first))
	}

	// Second refresh with no config change must produce byte-identical output —
	// the persistent-diff bug is exactly this call diverging by one byte.
	if err := WriteMergedMcpJSONFile(mcpFile, nil, ""); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	second, err := os.ReadFile(mcpFile)
	if err != nil {
		t.Fatalf("read after second refresh: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("refresh is not idempotent:\nfirst:  %q\nsecond: %q", string(first), string(second))
	}
}
