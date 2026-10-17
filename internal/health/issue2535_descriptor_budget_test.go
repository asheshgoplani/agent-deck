package health

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// #2535: a TUI managing a large, healthy fleet holds descriptors that scale
// with the session count (one pool client socket per pooled MCP per session),
// and the count is flat over time. In a Docker reproduction, 100 sessions with
// 5 pooled MCPs held 554 descriptors, steady for minutes. The fixed 512 budget
// put a red "descriptor count exceeds 512 budget" in the footer and flagged it
// in `agent-deck health`, with no leak and no guidance. The budget must scale
// with the sessions the process manages, and one sample must not light the
// footer.
func TestIssue2535_LargeHealthyFleetIsWithinDescriptorBudget(t *testing.T) {
	const fleetFDs, fleetSessions = 554, 100
	withOpenFDs(t, func() (*int, bool) { return ptr(fleetFDs), true })
	RecordStatusPass(10*time.Millisecond, fleetSessions, fleetSessions)
	d := t.TempDir()
	stop := Start(d, "tui", t.TempDir(), "test")
	warning := CurrentWarning()
	stop()
	if strings.Contains(warning, "descriptor") {
		t.Fatalf("footer warns for a steady %d-session fleet holding %d descriptors: %q", fleetSessions, fleetFDs, warning)
	}
	p := reportOf(t, d).Processes[0]
	for _, flag := range p.Flags {
		if strings.Contains(flag, "descriptor count exceeds") {
			t.Fatalf("health report flags a steady %d-session fleet holding %d descriptors: %v", fleetSessions, fleetFDs, p.Flags)
		}
	}
}

func withDescriptorLimit(t *testing.T, limit uint64) {
	t.Helper()
	previous := sampleDescriptorLimit
	sampleDescriptorLimit = func() uint64 { return limit }
	t.Cleanup(func() { sampleDescriptorLimit = previous })
}

func withSampleInterval(t *testing.T, d time.Duration) {
	t.Helper()
	previous := sampleInterval
	sampleInterval = d
	t.Cleanup(func() { sampleInterval = previous })
}

// waitForWarning polls the footer warning while the sampler ticks quickly.
func waitForWarning(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if w := CurrentWarning(); w != "" {
			return w
		}
		time.Sleep(time.Millisecond)
	}
	return ""
}

func TestIssue2535_DescriptorBudgetScalesWithSessionsAndCapsAtLimit(t *testing.T) {
	for _, tc := range []struct {
		sessions int
		limit    uint64
		want     int
	}{
		{0, 0, DescriptorBudget},
		{-3, 0, DescriptorBudget},
		{100, 0, DescriptorBudget + 100*DescriptorsPerSession},
		{100, 1 << 20, DescriptorBudget + 100*DescriptorsPerSession},
		// Near a low soft limit the budget is four fifths of the limit.
		{100, 1024, 820},
		{0, 256, 205},
	} {
		if got := DescriptorBudgetFor(tc.sessions, tc.limit); got != tc.want {
			t.Errorf("DescriptorBudgetFor(%d, %d) = %d, want %d", tc.sessions, tc.limit, got, tc.want)
		}
	}
}

// A sustained breach still reaches the footer, and the text says what was
// counted against which budget and where to look next.
func TestIssue2535_SustainedBreachWarnsWithGuidance(t *testing.T) {
	withOpenFDs(t, func() (*int, bool) { return ptr(700), true })
	withDescriptorLimit(t, 0)
	withSampleInterval(t, 2*time.Millisecond)
	RecordStatusPass(time.Millisecond, 10, 10)
	stop := Start(t.TempDir(), "tui", t.TempDir(), "test")
	defer stop()
	got := waitForWarning(t)
	want := "Health: 700 open descriptors exceed the budget of 672; run agent-deck health for details"
	if got != want {
		t.Fatalf("warning = %q, want %q", got, want)
	}
}

// A process close to its descriptor limit warns whatever its session count.
func TestIssue2535_CountNearDescriptorLimitWarns(t *testing.T) {
	withOpenFDs(t, func() (*int, bool) { return ptr(210), true })
	withDescriptorLimit(t, 256)
	withSampleInterval(t, 2*time.Millisecond)
	RecordStatusPass(time.Millisecond, 100, 100)
	stop := Start(t.TempDir(), "tui", t.TempDir(), "test")
	defer stop()
	if got := waitForWarning(t); !strings.Contains(got, "210 open descriptors exceed the budget of 205") {
		t.Fatalf("warning = %q", got)
	}
}

// A fleet that stays under its scaled budget never warns, across many samples.
func TestIssue2535_SteadyFleetNeverWarnsAcrossSamples(t *testing.T) {
	withOpenFDs(t, func() (*int, bool) { return ptr(554), true })
	withDescriptorLimit(t, 0)
	withSampleInterval(t, time.Millisecond)
	RecordStatusPass(time.Millisecond, 100, 100)
	d := t.TempDir()
	stop := Start(d, "tui", t.TempDir(), "test")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if w := CurrentWarning(); w != "" {
			stop()
			t.Fatalf("steady fleet warned: %q", w)
		}
		if r, err := Report(d, time.Hour); err == nil && len(r.Processes) == 1 && r.Processes[0].Stats["open_fds"].Count >= 5 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	p := reportOf(t, d).Processes[0]
	if n := p.Stats["open_fds"].Count; n < 5 {
		t.Fatalf("only %d samples taken", n)
	}
	// Later samples, taken before another status pass, still size the
	// budget by the last known fleet.
	if p.Latest.OpenFDsBudget == nil || *p.Latest.OpenFDsBudget != 2112 {
		t.Fatalf("latest sample lost the fleet size: budget=%v", p.Latest.OpenFDsBudget)
	}
}

// Records written before the budget was recorded are judged by the budget
// their session count implies.
func TestIssue2535_ReportJudgesOlderRecordsBySessionCount(t *testing.T) {
	d := t.TempDir()
	now := time.Now().UTC()
	line := func(fds, sessions int, at time.Time) string {
		return `{"version":1,"timestamp":"` + at.Format(time.RFC3339Nano) + `","role":"tui","pid":7,"started_at":"` +
			now.Add(-time.Hour).Format(time.RFC3339Nano) + `","open_fds":` + strconv.Itoa(fds) + `,"session_count":` + strconv.Itoa(sessions) + "}\n"
	}
	data := line(554, 100, now.Add(-2*time.Minute)) + line(600, 2, now.Add(-time.Minute))
	if err := os.WriteFile(filepath.Join(d, "tui-7-old.jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	p := reportOf(t, d).Processes[0]
	want := "descriptor count exceeds budget (600 open, budget 544)"
	if !slices.Contains(p.Flags, want) {
		t.Fatalf("flags = %v, want %q", p.Flags, want)
	}
	for _, flag := range p.Flags {
		if strings.Contains(flag, "554 open") {
			t.Fatalf("in-budget fleet sample flagged: %v", p.Flags)
		}
	}
}
