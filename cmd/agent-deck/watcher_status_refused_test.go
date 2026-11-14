package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// TestWatcherStatus_ReportsRefusedDeliveries: routed events the outbox
// refused because the watcher's queue for a conductor was full are never
// delivered; `watcher status` shows how many, per conductor, with the first
// and last refusal, in --json ("refused", [] when none) and in the text.
func TestWatcherStatus_ReportsRefusedDeliveries(t *testing.T) {
	profile := "_test_refused_status_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	db, err := openWatcherDB(profile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, w := range []*statedb.WatcherRow{
		{ID: "w-hook", Name: "hook-refused", Type: "webhook", Status: "running", CreatedAt: now, UpdatedAt: now},
		{ID: "w-gh", Name: "gh-refused", Type: "github", Status: "running", CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.SaveWatcher(w); err != nil {
			t.Fatal(err)
		}
	}
	first := time.Unix(1_800_000_000, 0)
	last := first.Add(5 * time.Minute)
	for _, at := range []time.Time{first, last} {
		if _, err := db.RecordWatcherDeliveryRefusal("w-hook", "demo", at); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	type refused struct {
		Conductor      string `json:"conductor"`
		Count          int    `json:"count"`
		FirstRefusedAt string `json:"first_refused_at"`
		LastRefusedAt  string `json:"last_refused_at"`
	}
	read := func(name string) *[]refused {
		t.Helper()
		var st struct {
			Refused *[]refused `json:"refused"`
		}
		out := captureStdout(t, func() { handleWatcherStatus(profile, []string{name, "--json"}) })
		if err := json.Unmarshal([]byte(out), &st); err != nil {
			t.Fatalf("watcher status --json: %v\n%s", err, out)
		}
		return st.Refused
	}
	got := read("hook-refused")
	want := refused{Conductor: "demo", Count: 2, FirstRefusedAt: first.UTC().Format(time.RFC3339), LastRefusedAt: last.UTC().Format(time.RFC3339)}
	if got == nil || len(*got) != 1 || (*got)[0] != want {
		t.Fatalf("refused = %+v, want [%+v]", got, want)
	}
	if got := read("gh-refused"); got == nil || len(*got) != 0 {
		t.Fatalf("refused for a watcher with none = %+v, want []", got)
	}
	text := captureStdout(t, func() { handleWatcherStatus(profile, []string{"hook-refused"}) })
	if !strings.Contains(text, "Refused: 2 for demo") {
		t.Fatalf("text status does not report the refusals:\n%s", text)
	}
}
