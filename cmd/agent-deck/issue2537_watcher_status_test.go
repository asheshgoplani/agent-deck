package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// TestIssue2537_WatcherStatusJSONSaysWhenTheQueueCannotBeRead: when the send
// queue cannot be read, `watcher status --json` reports undelivered as null
// with the error, as the text output reports it unknown, instead of an empty
// list a monitor would read as nothing waiting. A readable empty queue is
// still an empty list.
func TestIssue2537_WatcherStatusJSONSaysWhenTheQueueCannotBeRead(t *testing.T) {
	profile := "_test_2537_status_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	dbPath, err := session.GetDBPathForProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	db, err := openWatcherDB(profile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.SaveWatcher(&statedb.WatcherRow{ID: "w-hook", Name: "hook-2537", Type: "webhook", Status: "stopped", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	type status struct {
		Undelivered      *[]json.RawMessage `json:"undelivered"`
		UndeliveredError string             `json:"undelivered_error"`
	}
	read := func() status {
		t.Helper()
		var st status
		out := captureStdout(t, func() { handleWatcherStatus(profile, []string{"hook-2537", "--json"}) })
		if err := json.Unmarshal([]byte(out), &st); err != nil {
			t.Fatalf("watcher status --json: %v\n%s", err, out)
		}
		return st
	}

	if st := read(); st.Undelivered == nil || len(*st.Undelivered) != 0 || st.UndeliveredError != "" {
		t.Fatalf("with an empty queue: undelivered %v, error %q; want [] and no error", st.Undelivered, st.UndeliveredError)
	}

	// The queue directory is a file, so listing it fails.
	queue := sendqueue.Dir(filepath.Dir(dbPath))
	if err := os.RemoveAll(queue); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queue, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(queue) })
	if st := read(); st.Undelivered != nil || st.UndeliveredError == "" {
		t.Fatalf("with an unreadable queue: undelivered %v, error %q; want null and the error", st.Undelivered, st.UndeliveredError)
	}
}
