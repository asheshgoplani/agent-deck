package session

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestSaveConductorMeta_NoLeftoverOwnTemp asserts that a successful
// SaveConductorMeta leaves no `meta.json.tmp.*` staging file behind — proving
// the implementation uses temp+rename and renames the file (rather than
// leaking an unrenamed temp on the happy path). Pre-fix: os.WriteFile
// writes meta.json in place, so this passes vacuously; post-fix: we
// affirmatively assert the temp got renamed away.
func TestSaveConductorMeta_NoLeftoverOwnTemp(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "conductor-no-leftover"
	if err := SaveConductorMeta(&ConductorMeta{
		Name: name, Agent: "claude", Profile: "default",
	}); err != nil {
		t.Fatalf("SaveConductorMeta: %v", err)
	}

	dir := filepath.Join(tmpHome, ".agent-deck", "conductor", name)
	matches, err := filepath.Glob(filepath.Join(dir, "meta.json.tmp.*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("unrenamed temps left after save: %v", matches)
	}

	got, err := LoadConductorMeta(name)
	if err != nil {
		t.Fatalf("LoadConductorMeta: %v", err)
	}
	if got.Name != name {
		t.Errorf("Name = %q, want %q", got.Name, name)
	}
}

// TestSaveConductorMeta_PartialWriteRecoverable simulates a crash mid-write
// by pre-creating a corrupt meta.json (zero-length, simulating a truncated
// non-atomic write). A subsequent successful SaveConductorMeta with the
// atomic implementation must overwrite cleanly and yield a parseable file.
func TestSaveConductorMeta_PartialWriteRecoverable(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "conductor-recovery"
	dir := filepath.Join(tmpHome, ".agent-deck", "conductor", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	metaPath := filepath.Join(dir, "meta.json")

	// Simulate a crash that truncated the file mid-write.
	if err := os.WriteFile(metaPath, []byte(""), 0o644); err != nil {
		t.Fatalf("seed empty: %v", err)
	}

	// Also seed a stale .tmp from a prior crash — the atomic save should not
	// be confused by it.
	if err := os.WriteFile(metaPath+".tmp", []byte("garbage"), 0o644); err != nil {
		t.Fatalf("seed stale tmp: %v", err)
	}

	if err := SaveConductorMeta(&ConductorMeta{
		Name: name, Agent: "claude", Profile: "default",
	}); err != nil {
		t.Fatalf("SaveConductorMeta: %v", err)
	}

	// File must be parseable now.
	got, err := LoadConductorMeta(name)
	if err != nil {
		t.Fatalf("LoadConductorMeta after recovery: %v", err)
	}
	if got.Name != name {
		t.Errorf("Name = %q, want %q", got.Name, name)
	}
}

// TestSaveConductorMeta_ConcurrentWriters runs many concurrent saves to the
// same conductor name. With atomic temp-rename, every reader between writes
// sees a fully-formed file (never partial). The post-condition we check is
// that the final file is parseable and not a partial mash of bytes.
func TestSaveConductorMeta_ConcurrentWriters(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "conductor-conc"
	const N = 16
	var wg sync.WaitGroup
	errCh := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			meta := &ConductorMeta{
				Name:    name,
				Agent:   "claude",
				Profile: "default",
			}
			if err := SaveConductorMeta(meta); err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent SaveConductorMeta: %v", err)
	}

	got, err := LoadConductorMeta(name)
	if err != nil {
		t.Fatalf("LoadConductorMeta: %v", err)
	}
	if got.Name != name {
		t.Errorf("Name = %q, want %q", got.Name, name)
	}
}
