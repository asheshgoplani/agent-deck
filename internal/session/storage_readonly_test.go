package session

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestNewReadOnlyStorageMissingProfileHasZeroFilesystemEffect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	before := hashFilesystemTree(t, home)
	if storage, err := NewReadOnlyStorageWithProfile("fresh"); err == nil {
		_ = storage.Close()
		t.Fatal("missing state database unexpectedly opened")
	}
	after := hashFilesystemTree(t, home)
	if before != after {
		t.Fatalf("read-only storage changed filesystem tree: before=%x after=%x", before, after)
	}
}

func TestReadOnlyStorageExistingDatabaseHasZeroFilesystemEffect(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	seed, err := NewStorageWithProfile("existing")
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	before := hashFilesystemTree(t, home)
	reader, err := NewReadOnlyStorageWithProfile("existing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Load(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	after := hashFilesystemTree(t, home)
	if before != after {
		t.Fatalf("read-only load changed filesystem tree: before=%x after=%x", before, after)
	}
}

func hashFilesystemTree(t *testing.T, root string) [32]byte {
	t.Helper()
	var records []byte
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		records = append(records, []byte(rel+"|"+info.Mode().String()+"\n")...)
		if !entry.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			records = append(records, body...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(records)
}
