package session

import (
	"os"
	"path/filepath"
	"testing"
)

// LOC-11: a dir-local .agent-deck/config.toml that someone other than the
// user could have written must not be applied. It is skipped (never fails
// the launch) and the user's own private files keep working.

func TestResolveWorktreeSettingsForDir_SkipsWorldWritableDirLocalFile(t *testing.T) {
	home := setupDirLocalHome(t)
	target := filepath.Join(home, "projects", "ww")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeDirLocalConfig(t, target, "[worktree]\nsparse_checkout = \"planted\"\n")
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}

	settings, sources, _, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v (a skipped file must not fail the launch)", err)
	}
	if settings.SparseCheckout == "planted" || sources[WorktreeKeySparseCheckout] == path {
		t.Fatalf("world-writable dir-local file %s was applied: sparse=%q source=%q",
			path, settings.SparseCheckout, sources[WorktreeKeySparseCheckout])
	}
}

func TestResolveWorktreeSettingsForDir_SkipsFileInWorldWritableDir(t *testing.T) {
	home := setupDirLocalHome(t)
	target := filepath.Join(home, "projects", "wwdir")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeDirLocalConfig(t, target, "[worktree]\nsparse_checkout = \"planted\"\n")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o777); err != nil {
		t.Fatal(err)
	}

	settings, sources, _, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.SparseCheckout == "planted" || sources[WorktreeKeySparseCheckout] == path {
		t.Fatalf("dir-local file in world-writable dir %s was applied", filepath.Dir(path))
	}
}

func TestResolveWorktreeSettingsForDir_UntrustedInvalidFileDoesNotFailLaunch(t *testing.T) {
	home := setupDirLocalHome(t)
	target := filepath.Join(home, "projects", "wwbad")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeDirLocalConfig(t, target, "[shell]\nenv_files = [\"/etc/passwd\"]\n")
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := GetWorktreeSettingsForDir(target); err != nil {
		t.Fatalf("GetWorktreeSettingsForDir: %v; an untrusted file must be skipped, not parsed", err)
	}
}

func TestResolveWorktreeSettingsForDir_OwnPrivateFileInHomeStillApplies(t *testing.T) {
	home := setupDirLocalHome(t)
	target := filepath.Join(home, "projects", "mine")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeDirLocalConfig(t, target, "[worktree]\nsparse_checkout = \"mine\"\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	settings, sources, _, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.SparseCheckout != "mine" || sources[WorktreeKeySparseCheckout] != path {
		t.Fatalf("own private dir-local file not applied: sparse=%q source=%q", settings.SparseCheckout, sources[WorktreeKeySparseCheckout])
	}
}
