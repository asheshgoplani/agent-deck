package health

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }
func TestReportBudgetsUnknownAndPartial(t *testing.T) {
	d := t.TempDir()
	now := time.Now().UTC()
	p := filepath.Join(d, "tui-1-test.jsonl")
	samples := []Sample{{Version: 1, Timestamp: now.Add(-time.Minute), Role: "tui", PID: 1, StartedAt: now.Add(-time.Hour), OpenFDs: ptr(400), StatusPassMS: ptr(100.0)}, {Version: 1, Timestamp: now, Role: "tui", PID: 1, StartedAt: now.Add(-time.Hour), OpenFDs: ptr(600), StatusPassMS: ptr(300.0), Sessions: ptr(2), TmuxCalls: ptr(int64(5)), Remotes: map[string]Remote{"test": {LatencyMS: 2500, Outcome: "error"}}}}
	var data []byte
	for _, s := range samples {
		b, _ := json.Marshal(s)
		data = append(data, append(b, '\n')...)
	}
	data = append(data, []byte("{partial")...)
	os.WriteFile(p, data, 0600)
	report, err := Report(d, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Processes) != 1 {
		t.Fatalf("%+v", report)
	}
	process := report.Processes[0]
	if process.Latest.CPUPercent != nil {
		t.Fatal("unknown CPU fabricated")
	}
	if process.Stats["status_pass_ms"].P50 != 200 {
		t.Fatalf("stats: %+v", process.Stats)
	}
	text := Format(report)
	for _, want := range []string{"descriptor", "status pass", "tmux", "remote", "incomplete"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
}
func TestRetentionRotationAndIsolation(t *testing.T) {
	d := t.TempDir()
	old := filepath.Join(d, "tui-1-old.jsonl")
	os.WriteFile(old, []byte("old"), 0600)
	os.Chtimes(old, time.Now().Add(-8*24*time.Hour), time.Now().Add(-8*24*time.Hour))
	unrelated := filepath.Join(d, "keep.txt")
	os.WriteFile(unrelated, []byte("keep"), 0600)
	clean(d, time.Now())
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old sample retained")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d, "tui-1-current.jsonl")
	os.WriteFile(path, make([]byte, maxFileBytes), 0600)
	if err := appendSample(path, Sample{Version: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal("rotation missing", err)
	}
}
func TestSamplerAndPositiveSince(t *testing.T) {
	d := t.TempDir()
	stop := Start(d, "tui", t.TempDir())
	if !Enabled() {
		t.Fatal("sampler not enabled")
	}
	RecordStatusPass(10*time.Millisecond, 3, 2)
	RecordDBQuery(time.Millisecond)
	RecordRemote("test", time.Second, "ok")
	stop()
	stop()
	if Enabled() {
		t.Fatal("sampler still enabled after stop")
	}
	r, err := Report(d, time.Hour)
	if err != nil || len(r.Processes) != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if r.Processes[0].Latest.Goroutines == nil {
		t.Fatal("missing runtime count")
	}
	latest := r.Processes[0].Latest
	if latest.OpenFDs == nil || *latest.OpenFDs <= 0 || latest.CPUPercent == nil || *latest.CPUPercent < 0 {
		t.Fatalf("missing native process observations: %+v", latest)
	}
	if runtime.GOOS == "linux" && (latest.RSSBytes == nil || *latest.RSSBytes == 0) {
		t.Fatalf("missing current Linux RSS: %+v", latest)
	}
	if _, err := Report(d, 0); err == nil {
		t.Fatal("accepted invalid since")
	}
}

func TestHookCountsAndMissingHistory(t *testing.T) {
	d := t.TempDir()
	if n := countHooks(d); n == nil || *n != 0 {
		t.Fatalf("empty hook dir: %v", n)
	}
	if n := countHooks(filepath.Join(d, "missing")); n != nil {
		t.Fatal("missing hook dir must be unknown")
	}
	for i := 0; i < 4097; i++ {
		if err := os.WriteFile(filepath.Join(d, fmt.Sprint(i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if n := countHooks(d); n != nil {
		t.Fatal("oversized hook directory must be unknown")
	}
	r, err := Report(filepath.Join(d, "missing"), time.Hour)
	if err != nil || !strings.Contains(Format(r), "unknown") {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestReportStaleAndWindow(t *testing.T) {
	d := t.TempDir()
	now := time.Now().UTC()
	p := filepath.Join(d, "tui-1-window.jsonl")
	for _, s := range []Sample{{Version: 1, Timestamp: now.Add(-2 * time.Hour), StartedAt: now.Add(-3 * time.Hour), PID: 1, Role: "tui", StatusPassMS: ptr(900.0)}, {Version: 1, Timestamp: now.Add(-3 * time.Minute), StartedAt: now.Add(-3 * time.Hour), PID: 1, Role: "tui", StatusPassMS: ptr(10.0)}} {
		if err := appendSample(p, s); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Report(d, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if r.Processes[0].Stats["status_pass_ms"].Max != 10 {
		t.Fatal("out-of-window value included")
	}
	if !strings.Contains(Format(r), "stale") {
		t.Fatal("missing stale flag")
	}
}

func TestFormatEscapesControlCharacters(t *testing.T) {
	d := t.TempDir()
	now := time.Now().UTC()
	if err := appendSample(filepath.Join(d, "test.jsonl"), Sample{Version: 1, Timestamp: now, StartedAt: now, PID: 1, Role: "tui\x1b[31m", Remotes: map[string]Remote{"remote\nname": {LatencyMS: 3000, Outcome: "bad\x1b[0m"}}}); err != nil {
		t.Fatal(err)
	}
	r, err := Report(d, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	out := Format(r)
	if strings.Contains(out, "\x1b") || strings.Contains(out, "remote\nname") {
		t.Fatalf("unescaped terminal controls: %q", out)
	}
}

func TestRetentionCapPreservesRecentWriters(t *testing.T) {
	d := t.TempDir()
	now := time.Now()
	for i := 0; i < 140; i++ {
		p := filepath.Join(d, fmt.Sprintf("tui-%d-old.jsonl", i))
		if err := os.WriteFile(p, nil, 0600); err != nil {
			t.Fatal(err)
		}
		mtime := now.Add(-time.Duration(i+10) * time.Minute)
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	active := filepath.Join(d, "tui-active.jsonl")
	if err := os.WriteFile(active, nil, 0600); err != nil {
		t.Fatal(err)
	}
	clean(d, now)
	entries, err := os.ReadDir(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 128 {
		t.Fatalf("retained %d files, want 128", len(entries))
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatal("active file removed", err)
	}
}
func TestSamplerConsumesIntervalObservations(t *testing.T) {
	d := t.TempDir()
	RecordStatusPass(time.Millisecond, 1, 1)
	RecordDBQuery(time.Millisecond)
	RecordRemote("test", time.Millisecond, "ok")
	stop := Start(d, "tui", t.TempDir())
	stop()
	r, err := Report(d, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p := r.Processes[0]
	if p.Latest.StatusPassMS != nil || p.Latest.DBQueryMS != nil || p.Latest.Sessions != nil || p.Latest.TmuxCalls != nil || len(p.Latest.Remotes) != 0 {
		t.Fatal("observations leaked into next interval")
	}
	if p.Stats["status_pass_ms"].Count != 1 {
		t.Fatal("initial observation missing")
	}
}

// journal_dropped must reach health --json's Sample so an operator can see a
// wedged volume even without reading the daemon's own log.
func TestSamplerReportsJournalDropped(t *testing.T) {
	d := t.TempDir()
	before := JournalDropped()
	journalDropped.Add(2)
	stop := Start(d, "tui", t.TempDir())
	stop()
	r, err := Report(d, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Processes[0].Latest.JournalDropped; got < before+2 {
		t.Fatalf("want journal_dropped >= %d, got %d", before+2, got)
	}
}
