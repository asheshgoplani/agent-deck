package session

import (
	"flag"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestIssue2481_MeasureDaemonFrameCost replays the two measured waste
// patterns through the daemon path and reports per-frame cost. It uses only
// APIs that predate the fix, so the same file measures BEFORE and AFTER.
// Opt-in: it runs only when -test.bench is set (e.g. -bench=NONE).
//
//  1. A top-level conductor's own turns (41.7% of all transition frames on
//     the measured host): emitTurn with an observed flip, classified
//     (transcript) and legacy (no transcript) variants.
//  2. A hash-less child flapping running->waiting every 100 s whose parent
//     drains (consumes) the turn after every wake.
func TestIssue2481_MeasureDaemonFrameCost(t *testing.T) {
	if f := flag.Lookup("test.bench"); f == nil || f.Value.String() == "" {
		t.Skip("measurement: run with -bench=NONE")
	}
	const frames = 300

	for _, tool := range []string{"claude", "shell"} {
		f := selfConductorFixture(t, tool)
		f.appendTurn(t, fxHuman("u0", "status?"), fxAssistantText("a0", "All lanes green."))
		start := time.Now().Add(-24 * time.Hour)
		ns, allocs, bytes := measureFrames(frames, func(i int) {
			f.d.emitTurn("default", f.child, f.byID, "running", "waiting", start.Add(time.Duration(i)*time.Minute), true)
		})
		journal, _ := ReadTurnJournal(f.child.ID, 0)
		t.Logf("MEASURE self_conductor tool=%s frames=%d ns/frame=%d allocs/frame=%d B/frame=%d classified_passes=%d journal_lines=%d",
			tool, frames, ns, allocs, bytes, classifiedPasses(t, UnownedInboxID), len(journal))
	}

	n, parentID, base := newWakeNudgeFixture(t)
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    time.Now,
		isIdle: func(*Instance, string) bool { return true },
		send:   func(*Instance, string, string) error { return nil },
	}
	logPath := t.TempDir() + "/transition-notifier.log"
	n.logPath = logPath
	start := time.Now().Add(-24 * time.Hour)
	flap := func(i int) {
		ev := base
		ev.DoneStatus, ev.DoneSummary = "", ""
		ev.FromStatus, ev.ToStatus, ev.Substate = "running", "waiting", "running"
		ev.Timestamp = start.Add(time.Duration(i) * 100 * time.Second)
		n.NotifyTransition(ev)
	}
	flap(0)
	first, _ := DrainInboxForParent(parentID)
	written := 0
	ns, allocs, bytes := measureFrames(frames, func(i int) {
		flap(i + 1)
		pending, _ := ReadInboxEvents(parentID)
		written += len(pending)
		_, _ = DrainInboxForParent(parentID) // the woken parent drains at once
	})
	raw, _ := os.ReadFile(logPath)
	fp := ""
	if len(first) == 1 {
		fp = first[0].TurnFingerprint
	}
	t.Logf("MEASURE flap_consumed frames=%d ns/frame=%d allocs/frame=%d B/frame=%d log_lines_for_fp=%d duplicate_records_written=%d",
		frames, ns, allocs, bytes, strings.Count(string(raw), fp), written)
}

func measureFrames(frames int, frame func(i int)) (nsPerFrame, allocsPerFrame, bytesPerFrame int64) {
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	t0 := time.Now()
	for i := 0; i < frames; i++ {
		frame(i)
	}
	elapsed := time.Since(t0)
	runtime.ReadMemStats(&after)
	f := int64(frames)
	return elapsed.Nanoseconds() / f, int64(after.Mallocs-before.Mallocs) / f, int64(after.TotalAlloc-before.TotalAlloc) / f
}
