package statedb

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func tombstoneTestRow(id string) *InstanceRow {
	return &InstanceRow{ID: id, Title: id, ProjectPath: "/tmp", GroupPath: "my-sessions", Tool: "shell", Status: "stopped", CreatedAt: time.Now()}
}

func TestWithInstancesAbsentBlockedStaleWriterCannotResurrectAfterCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	guard, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if err := guard.Migrate(); err != nil {
		t.Fatal(err)
	}
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := writer.Migrate(); err != nil {
		t.Fatal(err)
	}
	row := tombstoneTestRow("blocked-stale-writer")
	writerDone := make(chan error, 1)
	guard.beforeInstancesAbsentCommit = func() {
		go func() { writerDone <- writer.UpsertInstances([]*InstanceRow{row}) }()
		select {
		case err := <-writerDone:
			t.Fatalf("stale writer was not blocked by deletion transaction: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
	absent, err := guard.WithInstancesAbsent([]string{row.ID}, func() error { return nil })
	if err != nil || !absent {
		t.Fatalf("WithInstancesAbsent = %v, %v", absent, err)
	}
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked stale writer did not finish after deletion commit")
	}
	if exists, err := guard.InstanceExists(row.ID); err != nil || exists {
		t.Fatalf("tombstoned id after stale writer = %v, %v", exists, err)
	}
}

func TestBlockedStaleWriterCannotOverwriteExplicitlyRecreatedIncarnation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	owner, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := owner.Migrate(); err != nil {
		t.Fatal(err)
	}
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := writer.Migrate(); err != nil {
		t.Fatal(err)
	}

	old := tombstoneTestRow("recreated-incarnation")
	old.Title = "old incarnation"
	if err := owner.CreateInstance(old); err != nil {
		t.Fatal(err)
	}
	captured := *old
	writerCaptured := make(chan struct{})
	releaseWriter := make(chan struct{})
	writer.beforeSaveInstancesWrite = func() {
		close(writerCaptured)
		<-releaseWriter
	}
	writerDone := make(chan error, 1)
	go func() { writerDone <- writer.UpsertInstances([]*InstanceRow{&captured}) }()
	<-writerCaptured

	if err := owner.DeleteInstance(old.ID); err != nil {
		t.Fatal(err)
	}
	newRow := tombstoneTestRow(old.ID)
	newRow.Title = "new incarnation"
	if err := owner.CreateInstance(newRow); err != nil {
		t.Fatal(err)
	}
	close(releaseWriter)
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}

	rows, err := owner.LoadInstances()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Title != newRow.Title {
		t.Fatalf("stale writer overwrote recreated incarnation: %#v", rows)
	}
}

func TestDeletedInstanceTombstoneRejectsStaleWritesAndExplicitCreateClearsIt(t *testing.T) {
	db := newTestDB(t)
	row := tombstoneTestRow("reused-id")
	if err := db.SaveInstance(row); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteInstance(row.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertInstances([]*InstanceRow{row}); err != nil {
		t.Fatal(err)
	}
	if exists, _ := db.InstanceExists(row.ID); exists {
		t.Fatal("stale batch upsert recreated tombstoned id")
	}
	if err := db.SaveInstance(row); !errors.Is(err, ErrInstanceTombstoned) {
		t.Fatalf("SaveInstance error = %v", err)
	}
	if err := db.CreateInstance(row); err != nil {
		t.Fatal(err)
	}
	if exists, _ := db.InstanceExists(row.ID); !exists {
		t.Fatal("explicit creation did not clear tombstone")
	}
}

func TestDeleteInstanceCommitFailurePreservesRow(t *testing.T) {
	db := newTestDB(t)
	row := tombstoneTestRow("commit-failure")
	if err := db.SaveInstance(row); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`CREATE TABLE commit_parent (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`CREATE TABLE commit_child (parent_id INTEGER, FOREIGN KEY(parent_id) REFERENCES commit_parent(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().Exec(`CREATE TRIGGER fail_tombstone_commit AFTER INSERT ON instance_tombstones BEGIN INSERT INTO commit_child(parent_id) VALUES (999); END`); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteInstance(row.ID); err == nil {
		t.Fatal("delete unexpectedly survived deferred commit failure")
	}
	if exists, err := db.InstanceExists(row.ID); err != nil || !exists {
		t.Fatalf("row after failed commit = %v, %v", exists, err)
	}
}
