package statedb

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func tombstoneTestRow(id string) *InstanceRow {
	return &InstanceRow{ID: id, Title: id, ProjectPath: "/tmp", GroupPath: "my-sessions", Tool: "shell", Status: "stopped", CreatedAt: time.Now()}
}

func TestWithInstancesAbsentRetriesSQLiteWriterContention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	guard, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	if err := guard.Migrate(); err != nil {
		t.Fatal(err)
	}
	row := tombstoneTestRow("busy-absence")
	if err := guard.CreateInstance(row); err != nil {
		t.Fatal(err)
	}
	blocker, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err := guard.db.Exec("PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	tx, err := blocker.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec("UPDATE metadata SET value=value WHERE key='schema_version'"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := guard.WithInstancesAbsent([]string{row.ID}, func() error { return nil })
		done <- err
	}()
	time.Sleep(35 * time.Millisecond) // force at least two 0-timeout BUSY attempts
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if exists, err := guard.InstanceExists(row.ID); err != nil || exists {
		t.Fatalf("instance after retried absence = %v, %v", exists, err)
	}
}

func TestLifecycleIntentPersistsAcrossReopenUntilCompleted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	intent, err := db.PrepareLifecycleIntent(LifecycleIntent{InstanceID: "intent-row", Kind: "worktree-finish", Payload: "/tmp/wt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	intents, err := db.LifecycleIntents()
	if err != nil || len(intents) != 1 || intents[0].InstanceID != "intent-row" || intents[0].Payload != "/tmp/wt" {
		t.Fatalf("reloaded intents = %#v, %v", intents, err)
	}
	if err := db.CompleteLifecycleIntent("intent-row", intent.Token); err != nil {
		t.Fatal(err)
	}
	intents, err = db.LifecycleIntents()
	if err != nil || len(intents) != 0 {
		t.Fatalf("completed intents = %#v, %v", intents, err)
	}
}

func TestLifecycleIntentRejectsOverlapAndStaleOwnership(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateInstance(tombstoneTestRow("owned")); err != nil {
		t.Fatal(err)
	}
	first, err := db.PrepareLifecycleIntent(LifecycleIntent{InstanceID: "owned", Kind: "remove", Payload: "metadata"})
	if err != nil || first.Token == "" || first.Generation == 0 || first.Phase != "prepared" {
		t.Fatalf("first prepare = %#v, %v", first, err)
	}
	retry, err := db.PrepareLifecycleIntent(LifecycleIntent{InstanceID: "owned", Kind: "remove", Payload: "metadata"})
	if err != nil || retry.Token != first.Token || retry.Generation != first.Generation {
		t.Fatalf("idempotent prepare = %#v, %v", retry, err)
	}
	if _, err := db.PrepareLifecycleIntent(LifecycleIntent{InstanceID: "owned", Kind: "archive", Payload: "metadata"}); !errors.Is(err, ErrLifecycleIntentConflict) {
		t.Fatalf("incompatible prepare = %v", err)
	}
	if err := db.AdvanceLifecycleIntent("owned", "stale-token", "row-deleted", "metadata"); !errors.Is(err, ErrLifecycleIntentOwnership) {
		t.Fatalf("stale advance = %v", err)
	}
	if err := db.CompleteLifecycleIntent("owned", "stale-token"); !errors.Is(err, ErrLifecycleIntentOwnership) {
		t.Fatalf("stale completion = %v", err)
	}
	if err := db.AdvanceLifecycleIntent("owned", first.Token, "row-deleted", "metadata"); err != nil {
		t.Fatal(err)
	}
	active, err := db.LifecycleIntents()
	if err != nil || len(active) != 1 || active[0].Phase != "row-deleted" || active[0].Token != first.Token || active[0].Generation != first.Generation {
		t.Fatalf("advanced intent = %#v, %v", active, err)
	}
	if err := db.CompleteLifecycleIntent("owned", first.Token); err != nil {
		t.Fatal(err)
	}
	newer, err := db.PrepareLifecycleIntent(LifecycleIntent{InstanceID: "owned", Kind: "archive", Payload: "new metadata", Generation: first.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if newer.Token == first.Token {
		t.Fatal("new lifecycle operation reused stale token")
	}
	if err := db.CompleteLifecycleIntent("owned", first.Token); !errors.Is(err, ErrLifecycleIntentOwnership) {
		t.Fatalf("stale completion removed newer intent: %v", err)
	}
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
