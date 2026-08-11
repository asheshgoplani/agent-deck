package session

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWithInstancesAbsentLetsBlockedStaleWriterFinishWithoutResurrection(t *testing.T) {
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
			return err
		case <-time.After(5 * time.Second):
			return errors.New("stale writer did not finish after durable tombstone commit")
		}
	})
	if err != nil || !absent {
		t.Fatalf("WithInstancesAbsent = %v, %v", absent, err)
	}
	exists, err := guard.InstanceExists(resurrected.ID)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("blocked stale writer resurrected tombstoned instance after lock release")
	}
}
