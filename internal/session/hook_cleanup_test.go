package session

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func hookCleanupStorage(t *testing.T) *Storage {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AGENTDECK_PROFILE", "")
	s, err := NewStorageWithProfile("default")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestHookCleanupDeleteInstance(t *testing.T) {
	s := hookCleanupStorage(t)
	root := GetHooksDir()
	artifacts := []string{"gone.json", "gone.sid", "gone.sid.tmp", "gone.generation.json", ".gone.json.tmp-123", "gone.codex-writer.lock", "gone.lock", "gone.projectdir-missing", "gone.events.jsonl", ".gone.events.jsonl.tmp-456", "sandbox/gone/gone.json", ".codex-consumed/gone/evidence.json", ".codex-consumed/gone.lock"}
	for _, name := range artifacts {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "keep.json"), []byte("{}"), 0600))
	require.NoError(t, s.DeleteInstance("gone"))
	for _, name := range artifacts {
		_, err := os.Lstat(filepath.Join(root, name))
		require.True(t, os.IsNotExist(err), "artifact remains: %s", name)
	}
	_, err := os.Stat(filepath.Join(root, "keep.json"))
	require.NoError(t, err)
}

func TestHookCleanupSweep(t *testing.T) {
	_ = hookCleanupStorage(t)
	root := GetHooksDir()
	require.NoError(t, os.MkdirAll(root, 0700))
	for _, name := range []string{"old.json", "old.sid", "recent.json", "recent.sid", "notes.txt"} {
		path := filepath.Join(root, name)
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0600))
		require.NoError(t, os.Chtimes(path, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour)))
	}
	require.NoError(t, os.Chtimes(filepath.Join(root, "recent.sid"), time.Now(), time.Now()))
	// Full orphan sweeping is reserved for startup and explicit pruning.
	require.NoError(t, PruneHookArtifacts())
	_, err := os.Stat(filepath.Join(root, "old.json"))
	require.True(t, os.IsNotExist(err))
	for _, name := range []string{"recent.json", "recent.sid", "notes.txt"} {
		_, err := os.Stat(filepath.Join(root, name))
		require.NoError(t, err)
	}
}

func TestHookCleanupProtectsAllRegistries(t *testing.T) {
	s := hookCleanupStorage(t)
	other, err := NewStorageWithProfile("other")
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })
	instances := []*Instance{{ID: "shared", Title: "archived", Tool: "claude", Status: StatusStopped, CreatedAt: time.Now(), ArchivedAt: time.Now()}}
	require.NoError(t, other.SaveWithGroups(instances, NewGroupTree(instances)))
	root := GetHooksDir()
	require.NoError(t, os.MkdirAll(root, 0700))
	path := filepath.Join(root, "shared.sid")
	require.NoError(t, os.WriteFile(path, []byte("anchor"), 0600))
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))
	require.NoError(t, s.DeleteInstance("shared"))
	_, err = os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, PruneHookArtifacts())
	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestHookCleanupUnknownRegistryPreservesArtifacts(t *testing.T) {
	s := hookCleanupStorage(t)
	dir, err := GetProfileDir("broken")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.db"), []byte("broken"), 0600))
	root := GetHooksDir()
	require.NoError(t, os.MkdirAll(root, 0700))
	path := filepath.Join(root, "gone.json")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0600))
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))
	require.Error(t, PruneHookArtifacts())
	require.NoError(t, s.DeleteInstance("gone"))
	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestHookCleanupNoSymlinkEscape(t *testing.T) {
	s := hookCleanupStorage(t)
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0600))
	root := GetHooksDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sandbox", "gone"), 0700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "sandbox", "gone", "link")))
	require.NoError(t, s.DeleteInstance("gone"))
	_, err := os.Stat(sentinel)
	require.NoError(t, err)
	require.NoError(t, s.DeleteInstance("../"+filepath.Base(outside)))
	_, err = os.Stat(sentinel)
	require.NoError(t, err)
}

func TestHookCleanupRespectsSandboxWriterLock(t *testing.T) {
	s := hookCleanupStorage(t)
	dir := filepath.Join(GetHooksDir(), "sandbox", "gone")
	require.NoError(t, os.MkdirAll(dir, 0700))
	path := filepath.Join(dir, "gone.lock")
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	require.NoError(t, err)
	defer lock.Close()
	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	require.NoError(t, s.DeleteInstance("gone"))
	_, err = os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_UN))
	require.NoError(t, s.DeleteInstance("gone"))
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err))
}

func TestHookCleanupPreservesPendingLegacyMigration(t *testing.T) {
	_ = hookCleanupStorage(t)
	dir, err := GetProfileDir("default")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sessions.json"), []byte(`{"instances":[{"id":"legacy"}]}`), 0600))
	root := GetHooksDir()
	require.NoError(t, os.MkdirAll(root, 0700))
	path := filepath.Join(root, "legacy.sid")
	require.NoError(t, os.WriteFile(path, []byte("anchor"), 0600))
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))
	require.NoError(t, PruneHookArtifacts())
	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestHookCleanupStartupThousandOrphans(t *testing.T) {
	s := hookCleanupStorage(t)
	instances := []*Instance{{ID: "registered", Title: "registered", Tool: "claude", Status: StatusStopped, CreatedAt: time.Now()}}
	require.NoError(t, s.SaveWithGroups(instances, NewGroupTree(instances)))
	require.NoError(t, s.Close())
	root := GetHooksDir()
	require.NoError(t, os.MkdirAll(root, 0700))
	old := time.Now().Add(-48 * time.Hour)
	for i := 0; i < 1000; i++ {
		suffix := ".json"
		if i%2 == 1 {
			suffix = ".sid"
		}
		path := filepath.Join(root, fmt.Sprintf("orphan-%04d%s", i, suffix))
		require.NoError(t, os.WriteFile(path, []byte("{}"), 0600))
		require.NoError(t, os.Chtimes(path, old, old))
	}
	registered := filepath.Join(root, "registered.json")
	require.NoError(t, os.WriteFile(registered, []byte("{}"), 0600))
	require.NoError(t, os.Chtimes(registered, old, old))
	require.NoError(t, os.WriteFile(filepath.Join(root, "fresh.sid"), []byte("anchor"), 0600))
	// Simulate a new process while keeping the fixture's registry on disk.
	registry, err := profileDataRootDir()
	require.NoError(t, err)
	hookStartupCleanup.Lock()
	delete(hookStartupCleanup.completed, hookCleanupRoots{hooks: root, registry: registry})
	hookStartupCleanup.Unlock()
	before, beforeErr := os.ReadDir("/proc/self/fd")
	restarted, err := NewStorageWithProfile("default")
	require.NoError(t, err)
	require.NoError(t, restarted.Close())
	after, afterErr := os.ReadDir("/proc/self/fd")
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	_, err = os.Stat(registered)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "fresh.sid"))
	require.NoError(t, err)
	if beforeErr == nil && afterErr == nil {
		require.LessOrEqual(t, len(after), len(before)+2)
		t.Logf("1000 old orphan files removed; registered + fresh preserved; descriptors before=%d after=%d", len(before), len(after))
	} else {
		t.Log("1000 old orphan files removed; registered + fresh preserved; /proc descriptor measurement unavailable")
	}
}

func TestHookCleanupRepeatedStorageOpenDoesNotSweep(t *testing.T) {
	_ = hookCleanupStorage(t)
	root := GetHooksDir()
	require.NoError(t, os.MkdirAll(root, 0700))
	path := filepath.Join(root, "arrived-after-startup.json")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0600))
	old := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(path, old, old))
	reopened, err := NewStorageWithProfile("default")
	require.NoError(t, err)
	require.NoError(t, reopened.Close())
	_, err = os.Stat(path)
	require.NoError(t, err, "routine storage reads must not repeat startup cleanup")
	require.NoError(t, PruneHookArtifacts())
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err), "explicit cleanup must still run")
}

func TestHookCleanupDeferredDeletion(t *testing.T) {
	s := hookCleanupStorage(t)
	root := GetHooksDir()
	require.NoError(t, os.MkdirAll(root, 0700))
	path := filepath.Join(root, "gone.json")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0600))
	cleanup, err := s.DeleteInstanceDeferredCleanup("gone")
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	require.FileExists(t, path)
	require.NoError(t, s.Close())
	cleanup()
	require.NoFileExists(t, path, "cleanup must work after storage closes")
}

func TestHookCleanupDeferredDeletionError(t *testing.T) {
	s := &Storage{}
	cleanup, err := s.DeleteInstanceDeferredCleanup("gone")
	require.Error(t, err)
	require.Nil(t, cleanup, "failed registry deletion must not schedule artifact cleanup")
}
