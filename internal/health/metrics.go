package health

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Journal states reported alongside derived metrics so a reader can tell
// "nothing happened" from "nothing was recorded".
const (
	JournalOK       = "ok"
	JournalNoEvents = "no events"
	JournalDisabled = "disabled"
)

// Turn boundaries: a turn starts when a session is observed running and ends
// at the next non-running status.
func isTerminalStatus(status string) bool { return status != "" && status != "running" }

type TurnMetrics struct {
	Count int `json:"count"`
	// Measured is how many turns had an observed start; the rest are terminal
	// edges seen without a preceding running observation (daemon restart, first
	// pass) and contribute no duration.
	Measured int      `json:"measured"`
	P50MS    *float64 `json:"p50_ms"`
	P95MS    *float64 `json:"p95_ms"`
}

type SendMetrics struct {
	Count       int `json:"count"`
	Confirmed   int `json:"confirmed"`
	Unconfirmed int `json:"unconfirmed"`
	Failed      int `json:"failed"`
	// Unknown counts sends whose outcome was not recorded or not recognised;
	// they are excluded from UnconfirmedRate's denominator.
	Unknown         int      `json:"unknown"`
	UnconfirmedRate *float64 `json:"unconfirmed_rate"`
	AckP50MS        *float64 `json:"ack_p50_ms"`
	AckP95MS        *float64 `json:"ack_p95_ms"`
}

type WorkerMetrics struct {
	Status     string   `json:"status"`
	DurationMS *float64 `json:"duration_ms"`
	FinishedAt string   `json:"finished_at"`
}

type StatusChange struct {
	Status string  `json:"status"`
	At     string  `json:"at"`
	AgeMS  float64 `json:"age_ms"`
}

// SessionMetrics is the per-session derivation of the journal for one window.
// DeadLetters is filled by the caller (it comes from the dead-letter stores,
// not the journal) and stays null when it was not computed.
type SessionMetrics struct {
	Version          int            `json:"version"`
	SessionID        string         `json:"session_id"`
	Title            string         `json:"title,omitempty"`
	Since            time.Time      `json:"since"`
	Until            time.Time      `json:"until"`
	Journal          string         `json:"journal"`
	Events           int            `json:"events"`
	Turns            TurnMetrics    `json:"turns"`
	WaitingMS        *float64       `json:"waiting_ms"`
	Sends            SendMetrics    `json:"sends"`
	Restarts         int            `json:"restarts"`
	DeadLetters      *int           `json:"dead_letters"`
	Worker           *WorkerMetrics `json:"worker"`
	LastStatusChange *StatusChange  `json:"last_status_change"`
	Flags            []string       `json:"flags"`
}

// SessionAggregate is the per-profile roll-up `health --json` carries.
type SessionAggregate struct {
	Journal                 string   `json:"journal"`
	WindowHours             float64  `json:"window_hours"`
	SessionsObserved        int      `json:"sessions_observed"`
	Turns                   int      `json:"turns"`
	TurnsPerDay             *float64 `json:"turns_per_day"`
	MedianTurnMS            *float64 `json:"median_turn_ms"`
	UnconfirmedSendRate     *float64 `json:"unconfirmed_send_rate"`
	Restarts                int      `json:"restarts"`
	RestartsPerDay          *float64 `json:"restarts_per_day"`
	SessionsWithDeadLetters *int     `json:"sessions_with_dead_letters"`
}

// Milliseconds is the unit every duration in the metrics JSON is reported in.
func Milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// percentiles returns the interpolated median and the nearest-rank p95.
func percentiles(v []float64) (p50, p95 float64) {
	sorted := append([]float64(nil), v...)
	sort.Float64s(sorted)
	n := len(sorted)
	p50 = sorted[n/2]
	if n%2 == 0 {
		p50 = (sorted[n/2-1] + p50) / 2
	}
	rank := int(math.Ceil(0.95*float64(n))) - 1
	if rank < 0 {
		rank = 0
	}
	return p50, sorted[rank]
}

// nullablePercentiles is percentiles for the JSON contract: nil, not 0, when
// nothing was measured.
func nullablePercentiles(v []float64) (p50, p95 *float64) {
	if len(v) == 0 {
		return nil, nil
	}
	a, b := percentiles(v)
	return &a, &b
}

// unconfirmedRate is unconfirmed over the classified sends (confirmed,
// unconfirmed, failed); nil when no send was classified.
func unconfirmedRate(unconfirmed, classified int) *float64 {
	if classified == 0 {
		return nil
	}
	rate := float64(unconfirmed) / float64(classified)
	return &rate
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func stringDetail(e Event, key string) string {
	s, _ := e.Detail[key].(string)
	return s
}

// sessionFold is the single pass over one session's events that every
// derived number comes from.
type sessionFold struct {
	turns         int
	turnDurations []float64
	waiting       time.Duration
	sends         SendMetrics
	acks          []float64
	restarts      int
	worker        *WorkerMetrics
	last          *Event
}

func (f sessionFold) classifiedSends() int {
	return f.sends.Confirmed + f.sends.Unconfirmed + f.sends.Failed
}

func foldSession(events []Event, until time.Time) sessionFold {
	var f sessionFold
	var runStart, waitStart time.Time
	for i, e := range events {
		switch e.Kind {
		case KindStatus:
			if !waitStart.IsZero() {
				f.waiting += e.TS.Sub(waitStart)
				waitStart = time.Time{}
			}
			if e.To == "running" {
				runStart = e.TS
			} else if isTerminalStatus(e.To) {
				f.turns++
				if !runStart.IsZero() {
					f.turnDurations = append(f.turnDurations, Milliseconds(e.TS.Sub(runStart)))
					runStart = time.Time{}
				}
			}
			if e.To == "waiting" {
				waitStart = e.TS
			}
			f.last = &events[i]
		case KindSend:
			f.sends.Count++
			switch stringDetail(e, "outcome") {
			case SendConfirmed:
				f.sends.Confirmed++
			case SendUnconfirmed:
				f.sends.Unconfirmed++
			case SendFailed:
				f.sends.Failed++
			default:
				f.sends.Unknown++
			}
			if ack, ok := number(e.Detail["ack_ms"]); ok {
				f.acks = append(f.acks, ack)
			}
		case KindRestart:
			f.restarts++
		case KindWorkerDone:
			w := &WorkerMetrics{Status: stringDetail(e, "status"), FinishedAt: e.TS.Format(time.RFC3339)}
			if d, ok := number(e.Detail["duration_ms"]); ok {
				w.DurationMS = &d
			}
			f.worker = w
		}
	}
	if !waitStart.IsZero() && until.After(waitStart) {
		f.waiting += until.Sub(waitStart)
	}
	return f
}

func (f sessionFold) turnMetrics() TurnMetrics {
	t := TurnMetrics{Count: f.turns, Measured: len(f.turnDurations)}
	t.P50MS, t.P95MS = nullablePercentiles(f.turnDurations)
	return t
}

func (f sessionFold) sendMetrics() SendMetrics {
	s := f.sends
	s.UnconfirmedRate = unconfirmedRate(s.Unconfirmed, f.classifiedSends())
	s.AckP50MS, s.AckP95MS = nullablePercentiles(f.acks)
	return s
}

func eventsBySession(events []Event) map[string][]Event {
	grouped := map[string][]Event{}
	for _, e := range events {
		grouped[e.SessionID] = append(grouped[e.SessionID], e)
	}
	return grouped
}

// ComputeSessionMetrics derives one session's numbers from events already
// filtered to [since, until). Events must be in timestamp order.
func ComputeSessionMetrics(sessionID string, events []Event, since, until time.Time) SessionMetrics {
	return computeSessionMetrics(sessionID, eventsBySession(events)[sessionID], since, until)
}

func computeSessionMetrics(sessionID string, own []Event, since, until time.Time) SessionMetrics {
	m := SessionMetrics{Version: 1, SessionID: sessionID, Since: since, Until: until, Journal: JournalNoEvents, Flags: []string{}}
	if len(own) == 0 {
		return m
	}
	f := foldSession(own, until)
	m.Journal = JournalOK
	m.Events = len(own)
	m.Turns = f.turnMetrics()
	m.Sends = f.sendMetrics()
	m.Restarts = f.restarts
	m.Worker = f.worker
	if f.last != nil {
		w := Milliseconds(f.waiting)
		m.WaitingMS = &w
		m.LastStatusChange = &StatusChange{Status: f.last.To, At: f.last.TS.Format(time.RFC3339), AgeMS: Milliseconds(until.Sub(f.last.TS))}
	}
	if m.Turns.Count > m.Turns.Measured {
		m.Flags = append(m.Flags, "some turns have unknown duration: no running observation before the terminal status")
	}
	if m.Sends.Unknown > 0 {
		m.Flags = append(m.Flags, "some sends have an unknown outcome")
	}
	return m
}

// ComputeAllSessionMetrics derives every session seen in the window from one
// read of the journal, sorted by session id.
func ComputeAllSessionMetrics(events []Event, since, until time.Time) []SessionMetrics {
	grouped := eventsBySession(events)
	ids := make([]string, 0, len(grouped))
	for id := range grouped {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]SessionMetrics, 0, len(ids))
	for _, id := range ids {
		out = append(out, computeSessionMetrics(id, grouped[id], since, until))
	}
	return out
}

// AggregateSessions rolls the whole profile's window into the numbers
// `health --json` reports. Per-day rates are normalised by the window length.
func AggregateSessions(events []Event, since, until time.Time) SessionAggregate {
	a := SessionAggregate{Journal: JournalNoEvents, WindowHours: until.Sub(since).Hours()}
	if len(events) == 0 || a.WindowHours <= 0 {
		return a
	}
	a.Journal = JournalOK
	var durations []float64
	classified, unconfirmed := 0, 0
	for _, own := range eventsBySession(events) {
		f := foldSession(own, until)
		a.SessionsObserved++
		a.Turns += f.turns
		a.Restarts += f.restarts
		durations = append(durations, f.turnDurations...)
		classified += f.classifiedSends()
		unconfirmed += f.sends.Unconfirmed
	}
	days := a.WindowHours / 24
	turnsPerDay, restartsPerDay := float64(a.Turns)/days, float64(a.Restarts)/days
	a.TurnsPerDay, a.RestartsPerDay = &turnsPerDay, &restartsPerDay
	a.MedianTurnMS, _ = nullablePercentiles(durations)
	a.UnconfirmedSendRate = unconfirmedRate(unconfirmed, classified)
	return a
}

// FormatMS renders a nullable millisecond value for human output.
func FormatMS(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.0f ms", *v)
}

// FormatRate renders a nullable ratio as a percentage for human output.
func FormatRate(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.0f%%", *v*100)
}

// FormatCount renders a nullable count for human output.
func FormatCount(v *int) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%d", *v)
}

func formatPerDay(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.1f", *v)
}

func formatSessionAggregate(a *SessionAggregate) string {
	if a == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Session metrics (journal %s, %.1f h window, %d sessions observed)\n", a.Journal, a.WindowHours, a.SessionsObserved)
	fmt.Fprintf(&b, "  turns/day: %s; median turn: %s; unconfirmed send rate: %s; restarts/day: %s\n", formatPerDay(a.TurnsPerDay), FormatMS(a.MedianTurnMS), FormatRate(a.UnconfirmedSendRate), formatPerDay(a.RestartsPerDay))
	fmt.Fprintf(&b, "  sessions with dead letters: %s\n", FormatCount(a.SessionsWithDeadLetters))
	return b.String()
}
