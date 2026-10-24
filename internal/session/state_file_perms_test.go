package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// LOC-8: agent-deck state (inboxes, dead letters, transition notifier state
// and logs, the hooks dir) must not depend on the process umask to stay
// private. Every assertion below uses the real write paths against a temp
// HOME; under the common 022/002 umask the pre-fix 0644/0755 modes leak
// group/other read bits.

func assertPrivateMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s mode = %#o, want %#o", path, got, want)
	}
}

func setupStatePermsHome(t *testing.T) string {
	t.Helper()
	home := setupDirLocalHome(t)
	ResetInboxFingerprintCacheForTest()
	t.Cleanup(ResetInboxFingerprintCacheForTest)
	return home
}

func permsTestEvent(child, to string) TransitionNotificationEvent {
	return TransitionNotificationEvent{
		ChildSessionID: child,
		ChildTitle:     "kid",
		Profile:        "_test",
		FromStatus:     "running",
		ToStatus:       to,
		Timestamp:      time.Now(),
	}
}

func TestStateFilePerms_InboxAndRootCreatedPrivate(t *testing.T) {
	setupStatePermsHome(t)

	root, err := GetAgentDeckDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("state root %s should not exist before the first write (err=%v)", root, err)
	}

	if _, err := WriteInboxEventIfNew("parent-1", permsTestEvent("child-a", "waiting")); err != nil {
		t.Fatalf("WriteInboxEventIfNew: %v", err)
	}
	if _, err := WriteInboxEventIfNew("parent-1", permsTestEvent("child-b", "waiting")); err != nil {
		t.Fatalf("WriteInboxEventIfNew: %v", err)
	}

	inbox := InboxPathFor("parent-1")
	if !strings.HasPrefix(inbox, root+string(filepath.Separator)) {
		t.Fatalf("inbox %s not under state root %s", inbox, root)
	}
	assertPrivateMode(t, root, 0o700)
	assertPrivateMode(t, filepath.Dir(inbox), 0o700)
	assertPrivateMode(t, inbox, 0o600)

	// The sweep rewrites the inbox through a temp file + rename; the
	// rewritten file must stay private too.
	if n, err := SweepInboxByTuple("parent-1", "child-a", "running", "waiting"); err != nil || n != 1 {
		t.Fatalf("SweepInboxByTuple = %d, %v; want 1, nil", n, err)
	}
	assertPrivateMode(t, inbox, 0o600)
}

func TestStateFilePerms_CommitToInboxAndDeadLetterPrivate(t *testing.T) {
	setupStatePermsHome(t)

	if err := CommitToInbox("parent-2", permsTestEvent("child-c", "waiting")); err != nil {
		t.Fatalf("CommitToInbox: %v", err)
	}
	inbox := InboxPathFor("parent-2")
	assertPrivateMode(t, filepath.Dir(inbox), 0o700)
	assertPrivateMode(t, inbox, 0o600)

	if _, err := writeDeadLetter(permsTestEvent("child-d", "error")); err != nil {
		t.Fatalf("writeDeadLetter: %v", err)
	}
	dl := DeadLetterPathFor("child-d")
	assertPrivateMode(t, filepath.Dir(dl), 0o700)
	assertPrivateMode(t, dl, 0o600)

	missed := filepath.Join(t.TempDir(), "sub", "missed.log")
	sink := NewDeadLetterSink(missed)
	ev := permsTestEvent("child-e", "error")
	for i := 0; i < MaxUnresolvedAttempts+1; i++ {
		sink.RecordUnresolvable(ev)
	}
	assertPrivateMode(t, filepath.Dir(missed), 0o700)
	assertPrivateMode(t, missed, 0o600)
}

func TestStateFilePerms_TransitionNotifierPrivate(t *testing.T) {
	setupStatePermsHome(t)
	dir := t.TempDir()
	n := &TransitionNotifier{
		statePath:  filepath.Join(dir, "state", "notify-state.json"),
		logPath:    filepath.Join(dir, "log", "notify.log"),
		missedPath: filepath.Join(dir, "missed", "missed.log"),
		orphanPath: filepath.Join(dir, "orphan", "orphan.log"),
		state:      transitionNotifyState{Records: map[string]transitionNotifyRecord{}},
	}
	if err := n.saveStateLocked(); err != nil {
		t.Fatalf("saveStateLocked: %v", err)
	}
	ev := permsTestEvent("child-f", "waiting")
	n.logEvent(ev)
	n.logMissed(ev, "test")
	n.logOrphanOnce(ev, ev.ChildSessionID)

	for _, p := range []string{n.statePath, n.logPath, n.missedPath, n.orphanPath} {
		assertPrivateMode(t, filepath.Dir(p), 0o700)
		assertPrivateMode(t, p, 0o600)
	}
}

func TestStateFilePerms_HooksDirPrivate(t *testing.T) {
	setupStatePermsHome(t)
	w, err := NewStatusFileWatcher(func() {})
	if err != nil {
		t.Fatalf("NewStatusFileWatcher: %v", err)
	}
	t.Cleanup(w.Stop)
	assertPrivateMode(t, GetHooksDir(), 0o700)
}
