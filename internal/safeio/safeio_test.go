package safeio

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// Backup
// ─────────────────────────────────────────────────────────────────────────────

func TestBackup_CopiesExistingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	want := []byte("original = true\n")
	if err := os.WriteFile(p, want, 0o600); err != nil {
		t.Fatal(err)
	}
	bak, err := Backup(p)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if bak != p+".bak" {
		t.Errorf("bak path = %q, want %q", bak, p+".bak")
	}
	got, err := os.ReadFile(bak)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("backup content = %q, want %q", got, want)
	}
}

func TestBackup_MissingSourceIsNoop(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "absent.toml")
	bak, err := Backup(p)
	if err != nil {
		t.Fatalf("Backup of missing file should be a no-op nil, got %v", err)
	}
	if bak != "" {
		t.Errorf("bak path for missing source = %q, want empty", bak)
	}
	if _, statErr := os.Stat(p + ".bak"); !os.IsNotExist(statErr) {
		t.Error("no .bak should be created for a missing source")
	}
}

func TestBackup_NoTornBakOnRename(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.db")
	if err := os.WriteFile(p, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(p); err != nil {
		t.Fatal(err)
	}
	// No leftover .tmp staging file.
	matches, _ := filepath.Glob(p + ".bak.tmp*")
	if len(matches) != 0 {
		t.Errorf("leftover temp files: %v", matches)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SafeOverwrite
// ─────────────────────────────────────────────────────────────────────────────

func TestSafeOverwrite_WritesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("v=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	newBytes := []byte("v=2")
	if err := SafeOverwrite(p, newBytes, Options{}); err != nil {
		t.Fatalf("SafeOverwrite: %v", err)
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, newBytes) {
		t.Errorf("content = %q, want %q", got, newBytes)
	}
	bak, _ := os.ReadFile(p + ".bak")
	if !bytes.Equal(bak, []byte("v=1")) {
		t.Errorf("backup = %q, want %q", bak, "v=1")
	}
}

func TestSafeOverwrite_NewFileNoBackup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "new.toml")
	if err := SafeOverwrite(p, []byte("hello"), Options{}); err != nil {
		t.Fatalf("SafeOverwrite new file: %v", err)
	}
	if _, statErr := os.Stat(p + ".bak"); !os.IsNotExist(statErr) {
		t.Error("a brand-new file should not produce a .bak")
	}
}

func TestSafeOverwrite_RefusesEmptyOverPopulated(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("a lot of important config"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := SafeOverwrite(p, []byte{}, Options{RefuseEmpty: true})
	if !errors.Is(err, ErrRefusingEmptyOverwrite) {
		t.Fatalf("want ErrRefusingEmptyOverwrite, got %v", err)
	}
	// The original must be untouched.
	got, _ := os.ReadFile(p)
	if string(got) != "a lot of important config" {
		t.Errorf("original was modified: %q", got)
	}
}

func TestSafeOverwrite_AllowsEmptyOverEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SafeOverwrite(p, []byte{}, Options{RefuseEmpty: true}); err != nil {
		t.Fatalf("empty-over-empty should be allowed: %v", err)
	}
}

func TestSafeOverwrite_RefusesViaGuard(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("populated"), 0o600); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("section drop")
	guard := func(old, next []byte) error {
		if len(old) > 0 && len(next) < len(old) {
			return sentinel
		}
		return nil
	}
	err := SafeOverwrite(p, []byte("x"), Options{Guard: guard})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel from guard, got %v", err)
	}
	// Original untouched, and the guard ran BEFORE any backup/write.
	got, _ := os.ReadFile(p)
	if string(got) != "populated" {
		t.Errorf("original modified despite guard refusal: %q", got)
	}
	if _, statErr := os.Stat(p + ".bak"); !os.IsNotExist(statErr) {
		t.Error("no backup should be made when the guard refuses")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SafeRemove
// ─────────────────────────────────────────────────────────────────────────────

func TestSafeRemove_RemovesWhenNotReferenced(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "victim")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SafeRemove(p, RemoveOptions{}); err != nil {
		t.Fatalf("SafeRemove: %v", err)
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Error("path should be gone")
	}
}

func TestSafeRemove_RefusesWhenReferenced(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "shared")
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	err := SafeRemove(p, RemoveOptions{
		StillReferenced: func(path string) (bool, string) {
			return true, "a sibling session still uses it"
		},
	})
	if !errors.Is(err, ErrStillReferenced) {
		t.Fatalf("want ErrStillReferenced, got %v", err)
	}
	if _, statErr := os.Stat(p); statErr != nil {
		t.Error("referenced path must NOT be removed")
	}
}

func TestSafeRemove_MissingPathIsNoop(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ghost")
	if err := SafeRemove(p, RemoveOptions{}); err != nil {
		t.Errorf("removing a missing path should be a no-op nil, got %v", err)
	}
}

func TestSafeRemove_RefusesEmptyPath(t *testing.T) {
	if err := SafeRemove("", RemoveOptions{}); err == nil {
		t.Error("SafeRemove of an empty path must error, not RemoveAll(\"\")")
	}
}
