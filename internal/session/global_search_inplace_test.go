package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/fswatch"
)

// On macOS the global search watcher polls. Claude appends to a transcript
// in place, which leaves the project directory's mtime alone; the index must
// still pick the new message up, not wait for the directory to change.
func TestGlobalSearchPollingSeesInPlaceAppend(t *testing.T) {
	defer fswatch.SetForcePollingForTest(true)()
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "projects", "-Users-test-project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projectDir, "d4e5f6a7-b8c9-0123-def4-56789012345a.jsonl")
	first := `{"sessionId":"d4e5f6a7-b8c9-0123-def4-56789012345a","type":"user","message":{"role":"user","content":"first question about kittens"},"cwd":"/Users/test/project"}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	for _, p := range []string{path, projectDir, filepath.Dir(projectDir)} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	index, err := NewGlobalSearchIndex(tmpDir, GlobalSearchSettings{Enabled: boolPtr(true), Tier: "auto", MemoryLimitMB: 100, IndexRateLimit: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	deadline := time.Now().Add(5 * time.Second)
	for index.IsLoading() || index.EntryCount() != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("initial load: %d entries, loading=%v", index.EntryCount(), index.IsLoading())
		}
		time.Sleep(20 * time.Millisecond)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"sessionId":"d4e5f6a7-b8c9-0123-def4-56789012345a","type":"assistant","message":{"role":"assistant","content":"zebrafish answer"}}` + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if info, _ := os.Lstat(projectDir); !info.ModTime().Equal(old) {
		t.Fatal("the append moved the directory mtime; the test needs an idle directory")
	}

	deadline = time.Now().Add(5 * time.Second)
	for len(index.Search("zebrafish")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("an in-place append to a transcript in an idle project directory was never indexed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
