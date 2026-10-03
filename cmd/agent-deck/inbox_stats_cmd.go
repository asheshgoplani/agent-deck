package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// runInboxStats prints the per-parent communication counters (issue #2469,
// design principle 7): wakeups, suppressed turns, digests, bytes injected.
// Read-only; nothing is consumed.
func runInboxStats(stdout io.Writer, args []string, explicitProfile string) error {
	fs := flag.NewFlagSet("inbox stats", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit the counters as JSON")
	all := fs.Bool("all", false, "every parent with counters")
	reset := fs.Bool("reset", false, "zero the counters for the selected parent")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck inbox stats [--json] [--all] [--reset] [<session-id>|self]")
		fmt.Fprintln(stdout, "Communication counters for a parent: records by tier, noise and dedup")
		fmt.Fprintln(stdout, "suppressed, wakeups fired and withheld, bytes injected, urgent latency.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	var stats []session.InboxStats
	if *all {
		list, err := session.ListInboxStats()
		if err != nil {
			return fmt.Errorf("list inbox stats: %w", err)
		}
		stats = list
	} else {
		sessionID, err := resolveDrainTarget(fs.Args())
		if err != nil {
			fs.Usage()
			return err
		}
		sessionID, err = resolveInboxDrainSessionInProfile(sessionID, explicitProfile)
		if err != nil {
			return err
		}
		if *reset {
			if err := session.ResetInboxStats(sessionID); err != nil {
				return fmt.Errorf("reset inbox stats: %w", err)
			}
		}
		st, err := session.ReadInboxStats(sessionID)
		if err != nil {
			return fmt.Errorf("read inbox stats: %w", err)
		}
		stats = []session.InboxStats{st}
	}
	if *asJSON {
		if *all {
			if stats == nil {
				stats = []session.InboxStats{}
			}
			return json.NewEncoder(stdout).Encode(stats)
		}
		return json.NewEncoder(stdout).Encode(stats[0])
	}
	for _, st := range stats {
		printInboxStats(stdout, st)
	}
	return nil
}

func printInboxStats(w io.Writer, st session.InboxStats) {
	fmt.Fprintf(w, "parent %s\n", st.Parent)
	if st.StartedAt.IsZero() {
		fmt.Fprintln(w, "  no counters yet")
		return
	}
	fmt.Fprintf(w, "  since %s (%s)\n", st.StartedAt.Format(time.RFC3339), time.Since(st.StartedAt).Round(time.Minute))
	total := st.RecordsUrgent + st.RecordsInfo + st.RecordsLegacy
	suppressed := st.NoiseSuppressed + st.DedupSuppressed
	fmt.Fprintf(w, "  records      urgent=%d info=%d legacy=%d  (suppressed: noise=%d dedup=%d)\n",
		st.RecordsUrgent, st.RecordsInfo, st.RecordsLegacy, st.NoiseSuppressed, st.DedupSuppressed)
	if total+suppressed > 0 {
		fmt.Fprintf(w, "  signal ratio %.0f%% of observed turns became records\n", 100*float64(total)/float64(total+suppressed))
	}
	fmt.Fprintf(w, "  wakeups      urgent=%d digest=%d withheld(info)=%d\n", st.WakeupsUrgent, st.WakeupsDigest, st.WakeupsSuppressed)
	fmt.Fprintf(w, "  delivered    drains=%d records=%d bytes_injected=%d text_bytes=%d fleet_block_skips=%d\n",
		st.Drains, st.RecordsDelivered, st.BytesInjected, st.TextBytes, st.FleetBlockSkips)
	if st.LastUrgentLatencyMS > 0 {
		fmt.Fprintf(w, "  last urgent latency %d ms\n", st.LastUrgentLatencyMS)
	}
}
