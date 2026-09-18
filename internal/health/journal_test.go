package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJournalAppendsOneLinePerEventAndDailyFiles(t *testing.T) {
	d := t.TempDir()
	j := NewJournal(d)
	day1 := time.Date(2026, 9, 16, 23, 59, 0, 0, time.UTC)
	day2 := day1.Add(2 * time.Minute)
	if err := j.Append(Event{TS: day1, SessionID: "a", Kind: KindStatus, From: "running", To: "waiting"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(Event{TS: day2, SessionID: "a", Kind: KindSend, Detail: map[string]any{"outcome": SendConfirmed}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sessions-20260916.jsonl", "sessions-20260917.jsonl"} {
		data, err := os.ReadFile(filepath.Join(d, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(data), "\n") != 1 {
			t.Fatalf("%s: want one line, got %q", name, data)
		}
	}
	events, incomplete, err := ReadEvents(d, day1.Add(-time.Hour), day2.Add(time.Hour))
	if err != nil || incomplete {
		t.Fatalf("read: %v incomplete=%v", err, incomplete)
	}
	if len(events) != 2 || events[0].Kind != KindStatus || events[1].Detail["outcome"] != SendConfirmed {
		t.Fatalf("events: %+v", events)
	}
}

func TestJournalRotatesAtSizeCapAndKeepsOnePredecessor(t *testing.T) {
	d := t.TempDir()
	j := NewJournal(d)
	ts := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(d, "sessions-20260917.jsonl")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxFileBytes-10)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := j.Append(Event{TS: ts, SessionID: "a", Kind: KindRestart}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + ".1"); err != nil || info.Size() != maxFileBytes-10 {
		t.Fatalf("rotated predecessor: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Count(string(data), "\n") != 1 {
		t.Fatalf("fresh file after rotation: %v %q", err, data)
	}
}

func TestJournalEventOmitsUnknownFields(t *testing.T) {
	d := t.TempDir()
	j := NewJournal(d)
	ts := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	if err := j.Append(Event{TS: ts, SessionID: "a", Kind: KindStop}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(d, "sessions-20260917.jsonl"))
	line := string(data)
	for _, absent := range []string{`"from"`, `"to"`, `"detail"`, `:0,`, `:0}`} {
		if strings.Contains(line, absent) {
			t.Errorf("unknown field written as a value: %s", line)
		}
	}
}

func TestJournalRejectsMalformedEvents(t *testing.T) {
	j := NewJournal(t.TempDir())
	if err := j.Append(Event{SessionID: "a", Kind: KindStop}); err == nil {
		t.Fatal("zero timestamp accepted")
	}
	if err := j.Append(Event{TS: time.Now(), Kind: KindStop}); err == nil {
		t.Fatal("empty session accepted")
	}
	if err := j.Append(Event{TS: time.Now(), SessionID: "a", Kind: "bogus"}); err == nil {
		t.Fatal("unknown kind accepted")
	}
}

func TestNilJournalIsAKillSwitch(t *testing.T) {
	var j *Journal
	if err := j.Append(Event{TS: time.Now(), SessionID: "a", Kind: KindStop}); err != nil {
		t.Fatal(err)
	}
}

func TestReadEventsToleratesPartialLinesAndIgnoresHealthSamples(t *testing.T) {
	d := t.TempDir()
	ts := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	good := `{"ts":"2026-09-17T12:00:00Z","session_id":"a","kind":"status","from":"running","to":"waiting"}` + "\n"
	os.WriteFile(filepath.Join(d, "sessions-20260917.jsonl"), []byte(good+`{"ts":"2026-09-17T12:00:01Z","sess`), 0600)
	sample := Sample{Version: 1, Timestamp: time.Now().UTC(), Role: "tui", PID: 1, StartedAt: time.Now().UTC()}
	encoded, _ := json.Marshal(sample)
	os.WriteFile(filepath.Join(d, "tui-1-1.jsonl"), append(encoded, '\n'), 0600)
	events, incomplete, err := ReadEvents(d, ts.Add(-time.Hour), ts.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !incomplete || len(events) != 1 {
		t.Fatalf("incomplete=%v events=%+v", incomplete, events)
	}
	// The health report must not count session events as corrupt samples.
	report, err := Report(d, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range report.Flags {
		if strings.Contains(flag, "corrupt") {
			t.Fatalf("session journal counted as corrupt health sample: %v", report.Flags)
		}
	}
}

func TestReadEventsFiltersWindow(t *testing.T) {
	d := t.TempDir()
	j := NewJournal(d)
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		j.Append(Event{TS: base.Add(time.Duration(i) * time.Hour), SessionID: "a", Kind: KindRestart})
	}
	events, _, err := ReadEvents(d, base.Add(30*time.Minute), base.Add(90*time.Minute))
	if err != nil || len(events) != 1 {
		t.Fatalf("window filter: %v %+v", err, events)
	}
	if _, _, err := ReadEvents(filepath.Join(d, "missing"), base, base.Add(time.Hour)); err != nil {
		t.Fatalf("missing dir must read as empty, got %v", err)
	}
}
