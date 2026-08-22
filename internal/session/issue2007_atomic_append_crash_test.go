package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestIssue2007_CheckedAppendCapacityRejectsOverflow(t *testing.T) {
	if _, err := checkedInboxAppendCapacity(maxInt(), 1); err == nil {
		t.Fatal("overflowing inbox append capacity must be rejected")
	}
	if got, err := checkedInboxAppendCapacity(10, 20); err != nil || got != 31 {
		t.Fatalf("ordinary capacity = %d, %v; want 31, nil", got, err)
	}
}

const issue2007CrashHelperEnv = "AGENTDECK_TEST_2007_CRASH_APPEND"

// TestIssue2007_AppendCrashHelper is invoked as a subprocess by the test below.
// It blocks after the replacement file has been written and fsync'd but before
// rename, giving the parent an exact SIGKILL boundary inside the append.
func TestIssue2007_AppendCrashHelper(t *testing.T) {
	if os.Getenv(issue2007CrashHelperEnv) != "1" {
		return
	}
	marker := os.Getenv("AGENTDECK_TEST_2007_CRASH_MARKER")
	restore := SetFsyncHookForTest(func(f *os.File) error {
		if filepath.Ext(f.Name()) == ".tmp" {
			if err := os.WriteFile(marker, []byte("fsynced-before-rename"), 0o600); err != nil {
				return err
			}
			select {}
		}
		return f.Sync()
	})
	defer restore()
	ResetInboxFingerprintCacheForTest()
	err := WriteInboxEvent("parent-atomic-2007", TransitionNotificationEvent{
		ChildSessionID: "new-child-2007", Profile: "default",
		FromStatus: "running", ToStatus: "error", Timestamp: time.Unix(200, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestIssue2007_SIGKILLDuringAppendLeavesOldOrNewCompleteFile(t *testing.T) {
	inboxTestHome(t)
	parent := "parent-atomic-2007"
	old := TransitionNotificationEvent{
		ChildSessionID: "old-child-2007", Profile: "default",
		FromStatus: "running", ToStatus: "waiting", Timestamp: time.Unix(100, 0),
	}
	if err := WriteInboxEvent(parent, old); err != nil {
		t.Fatalf("seed old inbox: %v", err)
	}

	marker := filepath.Join(t.TempDir(), "append-fsync.marker")
	cmd := exec.Command(os.Args[0], "-test.run=^TestIssue2007_AppendCrashHelper$")
	cmd.Env = append(os.Environ(), issue2007CrashHelperEnv+"=1", "AGENTDECK_TEST_2007_CRASH_MARKER="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start append helper: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("helper never reached fsync-before-rename boundary")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL append helper: %v", err)
	}
	err := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("helper should die by SIGKILL, got %v", err)
	}

	ResetInboxFingerprintCacheForTest()
	events, err := ReadAndTruncateInbox(parent)
	if err != nil {
		t.Fatalf("drain after crash: %v", err)
	}
	if len(events) != 1 || events[0].ChildSessionID != old.ChildSessionID {
		t.Fatalf("SIGKILL before rename must leave complete old inbox, got %+v", events)
	}

	// The producer did not observe success, so its normal retry must install the
	// new complete record without being poisoned by a partial tail/temp file.
	if err := WriteInboxEvent(parent, TransitionNotificationEvent{
		ChildSessionID: "new-child-2007", Profile: "default",
		FromStatus: "running", ToStatus: "error", Timestamp: time.Unix(200, 0),
	}); err != nil {
		t.Fatalf("retry after crash: %v", err)
	}
	events, err = ReadAndTruncateInbox(parent)
	if err != nil || len(events) != 1 || events[0].ChildSessionID != "new-child-2007" {
		t.Fatalf("retry after crash did not produce one complete record: events=%+v err=%v", events, err)
	}
}
