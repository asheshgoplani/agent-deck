package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBudgetWarningTmuxCallsNeedSustainedBreach(t *testing.T) {
	ResetStatusPassBreachState()
	t.Cleanup(ResetStatusPassBreachState)

	// One Ctrl+Q-sized spike must not flash the footer. A pass with no
	// sessions is not a ratio, so the fixed cache probes do not warn either.
	if got := BudgetWarning(10*time.Millisecond, 26, 57); got != "" {
		t.Fatalf("single overage warned: %q", got)
	}
	if got := BudgetWarning(10*time.Millisecond, 0, 2); got != "" {
		t.Fatalf("zero-session pass warned: %q", got)
	}
	if got := BudgetWarning(10*time.Millisecond, 26, 40); got != "" {
		t.Fatalf("under-budget pass warned: %q", got)
	}

	for i := 0; i < 2; i++ {
		if got := BudgetWarning(10*time.Millisecond, 26, 57); got != "" {
			t.Fatalf("breach %d warned early: %q", i+1, got)
		}
	}
	got := BudgetWarning(10*time.Millisecond, 26, 57)
	if got != "Health: tmux calls exceed twice the session count" {
		t.Fatalf("three consecutive breaches: got %q", got)
	}

	// One sample back under budget clears it, same as the status-pass timer.
	if got := BudgetWarning(10*time.Millisecond, 26, 5); got != "" {
		t.Fatalf("recovery left warning set: %q", got)
	}
	if got := BudgetWarning(10*time.Millisecond, 26, 57); got != "" {
		t.Fatalf("warning returned on the first sample after recovery: %q", got)
	}
}

// The report applies the same per-sample rule as the footer: a pass with no
// sessions has no ratio to exceed, so health and doctor must not flag it while
// the footer stays quiet.
func TestReportTmuxBudgetMatchesFooterRule(t *testing.T) {
	cases := []struct {
		name     string
		sessions int
		calls    int64
		flagged  bool
	}{
		{"zero sessions with cache probes", 0, 2, false},
		{"at budget", 26, 52, false},
		{"over budget", 26, 57, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := t.TempDir()
			now := time.Now().UTC()
			s := Sample{Version: 1, Timestamp: now, Role: "web", PID: 1, StartedAt: now.Add(-time.Hour), Sessions: ptr(tc.sessions), TmuxCalls: ptr(tc.calls)}
			b, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d, "web-1-test.jsonl"), append(b, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			report, err := Report(d, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			flagged := false
			for _, p := range report.Processes {
				for _, f := range p.Flags {
					if f == "tmux calls exceed twice the session count" {
						flagged = true
					}
				}
			}
			if flagged != tc.flagged {
				t.Fatalf("sessions=%d calls=%d flagged=%v, want %v", tc.sessions, tc.calls, flagged, tc.flagged)
			}
		})
	}
}
