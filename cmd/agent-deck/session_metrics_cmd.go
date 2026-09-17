package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// journalWindow is one read of the profile's journal for [since, until).
// journal tells the reader whether an empty result means "nothing happened"
// or "nothing is recorded" (kill switch off).
type journalWindow struct {
	events  []health.Event
	journal string
	since   time.Time
	until   time.Time
	flags   []string
}

func readSessionEvents(profile string, since time.Duration) (journalWindow, error) {
	now := time.Now().UTC()
	w := journalWindow{journal: health.JournalOK, since: now.Add(-since), until: now}
	if config, err := session.LoadUserConfig(); err == nil && !config.Health.SessionEventsEnabled() {
		w.journal = health.JournalDisabled
	}
	dir, err := session.HealthLogDir(profile)
	if err != nil {
		return w, err
	}
	events, incomplete, err := health.ReadEvents(dir, w.since, w.until)
	if err != nil {
		return w, err
	}
	w.events = events
	if incomplete {
		w.flags = append(w.flags, "some session events are unknown: incomplete or corrupt journal lines")
	}
	return w, nil
}

// deadLettersBySession groups the host's dead-letter records by session.
func deadLettersBySession() (map[string]int, error) {
	records, err := session.InspectDeadLetters("all")
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, r := range records {
		if r.ChildSessionID != "" {
			counts[r.ChildSessionID]++
		}
	}
	return counts, nil
}

// workerFromRecords fills a finished task-worker completion from the durable
// record when the journal has no worker_done event for it.
func workerFromRecords(records []session.CompletionRecord, sessionID string) *health.WorkerMetrics {
	for _, rec := range records {
		if rec.ChildID != sessionID || strings.TrimSpace(rec.Status) == "" || rec.FinishedAt.IsZero() {
			continue
		}
		w := &health.WorkerMetrics{Status: rec.Status, FinishedAt: rec.FinishedAt.UTC().Format(time.RFC3339)}
		if !rec.CreatedAt.IsZero() && rec.FinishedAt.After(rec.CreatedAt) {
			d := health.Milliseconds(rec.FinishedAt.Sub(rec.CreatedAt))
			w.DurationMS = &d
		}
		return w
	}
	return nil
}

func finishSessionMetrics(m *health.SessionMetrics, w journalWindow, deadLetters map[string]int, deadLetterErr error, records []session.CompletionRecord) {
	// A switched-off journal is reported as such even when older lines exist:
	// the numbers below are then history, not the current state.
	if w.journal != health.JournalOK {
		m.Journal = w.journal
	}
	m.Flags = append(m.Flags, w.flags...)
	if deadLetterErr != nil {
		m.Flags = append(m.Flags, "dead letters unknown: "+deadLetterErr.Error())
	} else {
		n := deadLetters[m.SessionID]
		m.DeadLetters = &n
	}
	if m.Worker == nil {
		m.Worker = workerFromRecords(records, m.SessionID)
	}
}

func handleSessionMetrics(profile string, args []string) {
	fs := flag.NewFlagSet("session metrics", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "Output session metrics as JSON")
	all := fs.Bool("all", false, "Every session with events in the window (one journal read)")
	since := fs.Duration("since", 24*time.Hour, "History window (positive Go duration, e.g. 1h or 24h)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: agent-deck session metrics <id|title> [--json] [--since 24h]\n       agent-deck session metrics --all [--json] [--since 24h]\n\nDerive per-session numbers (turns, turn duration, waiting time, send outcomes, restarts, dead letters) from the local session event journal. Unknown values are null, never 0.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if err == flag.ErrHelp {
			return
		}
		os.Exit(2)
	}
	if *since <= 0 || (*all && fs.NArg() != 0) || (!*all && fs.NArg() != 1) {
		fs.Usage()
		os.Exit(2)
	}
	out := NewCLIOutput(*jsonOutput, false)
	w, err := readSessionEvents(profile, *since)
	if err != nil {
		out.Error(fmt.Sprintf("session metrics: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	deadLetters, deadLetterErr := deadLettersBySession()
	records, _ := session.LoadCompletionRecords(profile)

	if *all {
		metrics := health.ComputeAllSessionMetrics(w.events, w.since, w.until)
		for i := range metrics {
			finishSessionMetrics(&metrics[i], w, deadLetters, deadLetterErr, records)
		}
		if *jsonOutput {
			encodeMetricsJSON(metrics)
			return
		}
		if len(metrics) == 0 {
			fmt.Printf("Session metrics: journal %s in the last %s\n", w.journal, since)
		}
		for _, m := range metrics {
			fmt.Print(formatSessionMetrics(m))
		}
		return
	}

	identifier := fs.Arg(0)
	sessionID, title := identifier, ""
	// The registry is only consulted for the title and id resolution: a
	// removed session still has a journal history worth reading.
	if _, instances, _, loadErr := loadSessionData(profile); loadErr == nil {
		if inst, _, _ := ResolveSession(identifier, instances); inst != nil {
			sessionID, title = inst.ID, inst.Title
		}
	}
	m := health.ComputeSessionMetrics(sessionID, w.events, w.since, w.until)
	m.Title = title
	finishSessionMetrics(&m, w, deadLetters, deadLetterErr, records)
	if title == "" && m.Events == 0 {
		out.Error(fmt.Sprintf("session '%s' not found in the registry and has no journal events in the last %s", identifier, since), ErrCodeNotFound)
		os.Exit(2)
	}
	if *jsonOutput {
		encodeMetricsJSON(m)
		return
	}
	fmt.Print(formatSessionMetrics(m))
}

func encodeMetricsJSON(v any) {
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func formatSessionMetrics(m health.SessionMetrics) string {
	var b strings.Builder
	name := m.SessionID
	if m.Title != "" {
		name = fmt.Sprintf("%s (%s)", m.Title, m.SessionID)
	}
	fmt.Fprintf(&b, "Session metrics for %s, window %s to %s, journal %s, %d events\n", name, m.Since.Format(time.RFC3339), m.Until.Format(time.RFC3339), m.Journal, m.Events)
	fmt.Fprintf(&b, "  turns: %d (%d with a measured duration); p50/p95 %s / %s\n", m.Turns.Count, m.Turns.Measured, health.FormatMS(m.Turns.P50MS), health.FormatMS(m.Turns.P95MS))
	fmt.Fprintf(&b, "  waiting on input: %s\n", health.FormatMS(m.WaitingMS))
	fmt.Fprintf(&b, "  sends: %d (confirmed %d, unconfirmed %d, failed %d, unknown %d); unconfirmed rate %s; ack p50/p95 %s / %s\n", m.Sends.Count, m.Sends.Confirmed, m.Sends.Unconfirmed, m.Sends.Failed, m.Sends.Unknown, health.FormatRate(m.Sends.UnconfirmedRate), health.FormatMS(m.Sends.AckP50MS), health.FormatMS(m.Sends.AckP95MS))
	fmt.Fprintf(&b, "  restarts: %d\n", m.Restarts)
	fmt.Fprintf(&b, "  dead letters: %s\n", health.FormatCount(m.DeadLetters))
	if m.Worker != nil {
		fmt.Fprintf(&b, "  worker completion: %s in %s (finished %s)\n", m.Worker.Status, health.FormatMS(m.Worker.DurationMS), m.Worker.FinishedAt)
	}
	if m.LastStatusChange != nil {
		fmt.Fprintf(&b, "  last status change: %s, %.0f s ago\n", m.LastStatusChange.Status, m.LastStatusChange.AgeMS/1000)
	} else {
		b.WriteString("  last status change: unknown\n")
	}
	for _, flag := range m.Flags {
		fmt.Fprintf(&b, "  %s\n", flag)
	}
	return b.String()
}

// sessionAggregateForHealth is the per-profile roll-up `health` reports next
// to the process samples, computed from one journal read.
func sessionAggregateForHealth(profile string, since time.Duration) health.SessionAggregate {
	w, err := readSessionEvents(profile, since)
	if err != nil {
		return health.SessionAggregate{Journal: "unknown: " + err.Error(), WindowHours: since.Hours()}
	}
	a := health.AggregateSessions(w.events, w.since, w.until)
	if w.journal != health.JournalOK {
		a.Journal = w.journal
	}
	if counts, err := deadLettersBySession(); err == nil {
		n := len(counts)
		a.SessionsWithDeadLetters = &n
	}
	return a
}

// journalSendOutcome maps the send path's delivery classification onto the
// journal's three outcomes. It keys on the strings so a delivery value added
// by another change (issue #1793's confirmation work) still classifies: a
// positive submit is confirmed, "delivered but nothing seen" is unconfirmed,
// and an error or anything else is failed.
func journalSendOutcome(delivery string, sendErr error) string {
	if sendErr != nil {
		return health.SendFailed
	}
	switch delivery {
	case deliverySubmitted, deliveryQueued:
		return health.SendConfirmed
	case deliveryUnverified, deliveryQueuedSocket, "delivered":
		return health.SendUnconfirmed
	}
	return health.SendFailed
}

// recordSendEvent journals one send. ack_ms is only known for a confirmed
// send: the time from the first byte to the observed acceptance.
func recordSendEvent(profile, sessionID string, res sendDeliveryResult, sendErr error, sentAt time.Time) {
	outcome := journalSendOutcome(res.delivery, sendErr)
	detail := map[string]any{"outcome": outcome, "delivery": res.delivery, "transport": res.transport}
	if outcome == health.SendConfirmed {
		detail["ack_ms"] = health.Milliseconds(time.Since(sentAt))
	}
	session.RecordSessionEvent(profile, sessionID, health.KindSend, detail)
}

// remoteMetricsUnsupported turns an older remote's "unknown session command"
// reply to `session metrics` into one clear line. Any other failure passes
// through as the remote printed it.
func remoteMetricsUnsupported(remote string, args []string, code int, stderr string) (string, bool) {
	if code == 0 || !isSessionMetricsArgs(args) || !strings.Contains(stderr, "unknown session command: metrics") {
		return "", false
	}
	return fmt.Sprintf("remote %q does not support 'session metrics' (its agent-deck predates session metrics; update it with 'agent-deck remote %s update')", remote, remote), true
}

func isSessionMetricsArgs(args []string) bool {
	return len(args) > 1 && args[0] == "session" && args[1] == "metrics"
}
