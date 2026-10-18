package health

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// An over-budget descriptor flag comes with guidance on what it means and
// how to find what holds the descriptors; an in-budget process gets none
// (#2668).
func TestIssue2668_FormatExplainsDescriptorBudgetBreach(t *testing.T) {
	d := t.TempDir()
	now := time.Now().UTC()
	write := func(pid, fds int) {
		line := `{"version":1,"timestamp":"` + now.Add(-time.Minute).Format(time.RFC3339Nano) + `","role":"tui","pid":` + strconv.Itoa(pid) +
			`,"started_at":"` + now.Add(-time.Hour).Format(time.RFC3339Nano) + `","open_fds":` + strconv.Itoa(fds) +
			`,"session_count":12,"open_fds_limit":10240}` + "\n"
		name := filepath.Join(d, "tui-"+strconv.Itoa(pid)+".jsonl")
		if err := os.WriteFile(name, []byte(line), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(60507, 720)
	write(4242, 100)
	text := Format(reportOf(t, d))
	for _, want := range []string{
		"descriptor count exceeds budget (720 open, budget 704)",
		"hint: the budget is 512 + 16 per session, capped at 80% of the soft open-file limit (ulimit -n = 10240)",
		"advisory",
		"lsof -p 60507 | awk 'NR>1 {print $5}' | sort | uniq -c | sort -rn",
		"docs/runtime-health.md#descriptor-budget",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Count(text, "hint:") != 1 || strings.Contains(text, "lsof -p 4242") {
		t.Errorf("in-budget process got a descriptor hint:\n%s", text)
	}
}

// writeFDSamples writes one tui sample per count, a minute apart and ending
// a minute ago, for pid.
func writeFDSamples(t *testing.T, d string, pid int, counts ...int) {
	t.Helper()
	now := time.Now().UTC()
	var b strings.Builder
	for i, fds := range counts {
		ts := now.Add(-time.Duration(len(counts)-i) * time.Minute)
		b.WriteString(`{"version":1,"timestamp":"` + ts.Format(time.RFC3339Nano) + `","role":"tui","pid":` + strconv.Itoa(pid) +
			`,"started_at":"` + now.Add(-time.Hour).Format(time.RFC3339Nano) + `","open_fds":` + strconv.Itoa(fds) +
			`,"session_count":12,"open_fds_limit":10240}` + "\n")
	}
	if err := os.WriteFile(filepath.Join(d, "tui-"+strconv.Itoa(pid)+".jsonl"), []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
}

// A breach seen only in an earlier sample is historical: a live lsof of the
// latest PID cannot show what held those descriptors, so the hint says so
// instead of recommending it.
func TestIssue2668_HistoricalBreachDoesNotSuggestLiveBreakdown(t *testing.T) {
	d := t.TempDir()
	writeFDSamples(t, d, 60507, 720, 600)
	text := Format(reportOf(t, d))
	if !strings.Contains(text, "descriptor count exceeds budget (720 open, budget 704)") {
		t.Fatalf("flag missing:\n%s", text)
	}
	if !strings.Contains(text, "The breach is historical: the latest sample (600 open) is back within its budget of 704.") {
		t.Errorf("historical breach not called out:\n%s", text)
	}
	if strings.Contains(text, "lsof -p") {
		t.Errorf("historical breach suggests a live breakdown:\n%s", text)
	}
}

// A count that is over budget and still rising is a likely leak (#2668
// follow-up: the count kept climbing to 3500 with no change in sessions), so
// the hint names it as one and asks for the breakdown instead of only
// calling the budget advisory.
func TestIssue2668_GrowingBreachIsCalledALeak(t *testing.T) {
	d := t.TempDir()
	writeFDSamples(t, d, 60507, 650, 690, 720)
	text := Format(reportOf(t, d))
	for _, want := range []string{
		"descriptor growth observed",
		"The count is still rising (from 650 to 720 open in this window).",
		"descriptor leak, not a limit to raise",
		"lsof -p 60507",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	// A flat over-budget count keeps the plain rerun advice.
	flat := t.TempDir()
	writeFDSamples(t, flat, 60507, 720, 720)
	if text := Format(reportOf(t, flat)); strings.Contains(text, "descriptor leak") || !strings.Contains(text, "lsof -p 60507") {
		t.Errorf("flat breach hint wrong:\n%s", text)
	}
}

// The health code itself must not be what grows the count: sampling and
// reading the report many times leaves the process's descriptor count where
// it was.
func TestIssue2668_SamplingAndReportDoNotLeakDescriptors(t *testing.T) {
	before, _ := sampleOpenFDs()
	if before == nil {
		t.Skip("open_fds not sampled on this platform")
	}
	d := t.TempDir()
	for i := 0; i < 50; i++ {
		sampleOnce(t, d, "tui")
		if _, err := Report(d, time.Hour); err != nil {
			t.Fatal(err)
		}
		_ = Format(reportOf(t, d))
	}
	after, _ := sampleOpenFDs()
	// Allow a little slack for runtime-owned descriptors (netpoller, etc).
	if *after > *before+4 {
		t.Fatalf("open descriptors grew from %d to %d across 50 sample/report cycles", *before, *after)
	}
}
