package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests drive the ownership / writability rule through seams for the
// cases a normal (non-root) test cannot create on disk: a file owned by
// another uid, and a group-writable file whose group is not the user's
// private group.

func withPermWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := SetPermissionWarningWriter(&buf)
	t.Cleanup(func() { SetPermissionWarningWriter(prev) })
	return &buf
}

func withTrustUID(t *testing.T, uid int) {
	t.Helper()
	prev := permTrustUID
	permTrustUID = func() int { return uid }
	t.Cleanup(func() { permTrustUID = prev })
}

func withPrivateGroup(t *testing.T, private bool) {
	t.Helper()
	prev := isUserPrivateGroup
	isUserPrivateGroup = func(uint32, int) bool { return private }
	t.Cleanup(func() { isUserPrivateGroup = prev })
}

func writeTrustDirLocal(t *testing.T, home, name, value string, fileMode, dirMode os.FileMode) (string, string) {
	t.Helper()
	target := filepath.Join(home, "projects", name)
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	path := writeDirLocalConfig(t, target, "[worktree]\nsparse_checkout = \""+value+"\"\n")
	if err := os.Chmod(path, fileMode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), dirMode); err != nil {
		t.Fatal(err)
	}
	return target, path
}

func TestDirLocalTrust_ForeignOwnerSkippedWithWarning(t *testing.T) {
	home := setupDirLocalHome(t)
	buf := withPermWarnings(t)
	target, path := writeTrustDirLocal(t, home, "foreign", "planted", 0o600, 0o700)
	withTrustUID(t, os.Getuid()+4242)

	settings, sources, _, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.SparseCheckout == "planted" || sources[WorktreeKeySparseCheckout] == path {
		t.Fatalf("dir-local file owned by another uid was applied")
	}
	if out := buf.String(); !strings.Contains(out, path) || !strings.Contains(out, "owned by uid") {
		t.Fatalf("warning = %q, want it to name %s and the owner", out, path)
	}
}

func TestDirLocalTrust_GroupWritableSharedGroupSkipped(t *testing.T) {
	home := setupDirLocalHome(t)
	buf := withPermWarnings(t)
	withPrivateGroup(t, false)
	target, path := writeTrustDirLocal(t, home, "groupw", "planted", 0o664, 0o775)

	settings, _, _, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.SparseCheckout == "planted" {
		t.Fatalf("group-writable (shared group) dir-local file was applied")
	}
	if out := buf.String(); !strings.Contains(out, path) || !strings.Contains(out, "group-writable") {
		t.Fatalf("warning = %q, want group-writable for %s", out, path)
	}
}

// A umask-002 user (user-private group, as on Debian/Ubuntu) gets 0664/0775
// files; those are still the user's own and must keep applying.
func TestDirLocalTrust_GroupWritablePrivateGroupApplies(t *testing.T) {
	home := setupDirLocalHome(t)
	buf := withPermWarnings(t)
	withPrivateGroup(t, true)
	target, path := writeTrustDirLocal(t, home, "upg", "mine", 0o664, 0o775)

	settings, sources, _, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.SparseCheckout != "mine" || sources[WorktreeKeySparseCheckout] != path {
		t.Fatalf("user-private-group file not applied: sparse=%q", settings.SparseCheckout)
	}
	if buf.Len() != 0 {
		t.Fatalf("unexpected warning: %q", buf.String())
	}
}

func TestDirLocalTrust_OutsideHomeOwnPrivateApplies(t *testing.T) {
	setupDirLocalHome(t)
	withPermWarnings(t)
	outside := t.TempDir() // sibling of HOME, i.e. outside it
	target, path := writeTrustDirLocal(t, outside, "ext", "ext", 0o600, 0o700)

	settings, sources, _, err := ResolveWorktreeSettingsForDir(target)
	if err != nil {
		t.Fatalf("ResolveWorktreeSettingsForDir: %v", err)
	}
	if settings.SparseCheckout != "ext" || sources[WorktreeKeySparseCheckout] != path {
		t.Fatalf("own private file outside HOME not applied: sparse=%q", settings.SparseCheckout)
	}
}

func writeGlobalUserConfig(t *testing.T, contents string, mode os.FileMode) string {
	t.Helper()
	path, err := GetUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	ClearUserConfigCache()
	return path
}

func TestUserConfigPerms_WorldWritableWarnsButLoads(t *testing.T) {
	setupDirLocalHome(t)
	buf := withPermWarnings(t)
	path := writeGlobalUserConfig(t, "[worktree]\nsparse_checkout = \"g\"\n", 0o666)

	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if cfg.Worktree.SparseCheckout != "g" {
		t.Fatalf("config.toml was not applied (warn, do not refuse): %+v", cfg.Worktree)
	}
	if out := buf.String(); !strings.Contains(out, path) || !strings.Contains(out, "world-writable") {
		t.Fatalf("warning = %q, want world-writable for %s", out, path)
	}
}

func TestUserConfigPerms_ForeignOwnerWarns(t *testing.T) {
	setupDirLocalHome(t)
	buf := withPermWarnings(t)
	path := writeGlobalUserConfig(t, "", 0o600)
	withTrustUID(t, os.Getuid()+4242)

	if _, err := LoadUserConfig(); err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, path) || !strings.Contains(out, "owned by uid") {
		t.Fatalf("warning = %q, want owner warning for %s", out, path)
	}
}

func TestUserConfigPerms_PrivateFileNoWarning(t *testing.T) {
	setupDirLocalHome(t)
	buf := withPermWarnings(t)
	writeGlobalUserConfig(t, "", 0o600)
	if _, err := LoadUserConfig(); err != nil {
		t.Fatalf("LoadUserConfig: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("unexpected warning: %q", buf.String())
	}
}

func TestStateRootPerms_TightenedOnceWithNotice(t *testing.T) {
	setupDirLocalHome(t)
	root, err := GetAgentDeckDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if !tightenStateRoot(root, &buf) {
		t.Fatalf("tightenStateRoot reported no change for a 0755 root")
	}
	assertPrivateMode(t, root, 0o700)
	if out := buf.String(); !strings.Contains(out, root) || !strings.Contains(out, "0700") {
		t.Fatalf("notice = %q, want it to name %s and 0700", out, root)
	}

	buf.Reset()
	if tightenStateRoot(root, &buf) || buf.Len() != 0 {
		t.Fatalf("second call changed something / printed %q", buf.String())
	}
}

func TestStateRootPerms_ForeignOwnedRootLeftAlone(t *testing.T) {
	setupDirLocalHome(t)
	root, err := GetAgentDeckDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	withTrustUID(t, os.Getuid()+4242)

	var buf bytes.Buffer
	if tightenStateRoot(root, &buf) {
		t.Fatalf("tightened a root not owned by the current uid")
	}
	assertPrivateMode(t, root, 0o755)
}

func TestStateRootPerms_MissingRootNotCreated(t *testing.T) {
	setupDirLocalHome(t)
	root, err := GetAgentDeckDir()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if tightenStateRoot(root, &buf) {
		t.Fatalf("reported a change for a missing root")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("missing root was created (err=%v)", err)
	}
}
