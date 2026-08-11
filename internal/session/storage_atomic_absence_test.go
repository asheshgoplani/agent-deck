package session

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWithInstancesAbsentBlocksResurrectionThroughConfirmedCallback(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	guard, err := NewStorageWithProfile("_test_atomic_absence_guard")
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	writer, err := NewStorageWithProfile("_test_atomic_absence_guard")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	resurrected := &Instance{ID: "already-observed", Title: "resurrected", ProjectPath: "/tmp", GroupPath: DefaultGroupPath, Tool: "shell", Command: "shell", Status: StatusStopped}
	writerStarted := make(chan struct{})
	writerDone := make(chan error, 1)
	absent, err := guard.WithInstancesAbsent([]string{resurrected.ID, "later-id"}, func() error {
		go func() {
			close(writerStarted)
			writerDone <- writer.SaveWithGroups([]*Instance{resurrected}, NewGroupTree([]*Instance{resurrected}))
		}()
		<-writerStarted
		select {
		case err := <-writerDone:
			t.Fatalf("resurrection writer completed inside confirmed callback: %v", err)
		case <-time.After(200 * time.Millisecond):
			return nil
		}
		return nil
	})
	if err != nil || !absent {
		t.Fatalf("WithInstancesAbsent = %v, %v", absent, err)
	}
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("resurrection writer remained blocked after callback transaction committed")
	}
}
