package comms

import (
	"math"
	"sort"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// Measurement (`agent-deck msg stats`). Every record is a measurement row,
// so the acceptance targets of issue #2482 are computed from one scan of
// the ledger over a time window, with no second store. Each target reports
// its value, the denominator it was computed over, the target and whether
// it is met; a target with no data has no value and is not met or failed
// (met is null), so an empty window never reads as a pass.

// Call states: what a KindCall record says was run.
const (
	CallSessionOutput = "session_output"
	CallInboxDrain    = "inbox_drain"
	CallMsgRead       = "msg_read"
)

// Targets from issue #2482.
const (
	TargetWakesPerParentHour = 4.0
	TargetTextPct            = 95.0
	TargetRecordsPerFinished = 3.0
	TargetDuplicatePct       = 0.0
	TargetCallsPerWake       = 0.0
	TargetSendSenderTextPct  = 100.0
	TargetCrossHostLatency   = 0.0 // records with a measured latency: more than this
)

// duplicateSuspectWindow: two turn records of one child with the same full
// text hash signalled this close together are one turn committed twice
// under two keys (the #2481 "info then urgent" class), not two answers.
const duplicateSuspectWindow = 5 * time.Second

// Target is one acceptance number.
type Target struct {
	Value  *float64 `json:"value"` // nil: no data in the window
	Target float64  `json:"target"`
	Op     string   `json:"op"`  // "<=", ">=", ">"
	Met    *bool    `json:"met"` // nil when Value is nil
	N      int      `json:"n"`   // the denominator (or the count for ">")
	Note   string   `json:"note,omitempty"`
}

// Targets are the seven #2482 numbers, in the issue's order.
type Targets struct {
	WakesPerParentHour Target `json:"wakes_per_parent_hour"`
	TextPct            Target `json:"records_with_text_pct"`
	RecordsPerFinished Target `json:"records_per_finished"`
	DuplicatePct       Target `json:"duplicate_turn_pct"`
	CallsPerWake       Target `json:"output_and_drain_calls_per_wake"`
	SendSenderTextPct  Target `json:"send_with_sender_and_text_pct"`
	CrossHostLatency   Target `json:"cross_host_records_with_latency"`
}

// ParentStats is the per-parent breakdown: wakes it received (by path and
// transport), records addressed to it, read calls it made.
type ParentStats struct {
	ID           string         `json:"id"`
	Wakes        int            `json:"wakes"`
	WakesPerHour float64        `json:"wakes_per_hour"`
	WakesBy      map[string]int `json:"wakes_by,omitempty"` // "<path>/<via>" -> n (inbox/tmux, inbox/stop, ledger/tmux, ...)
	Records      int            `json:"records"`            // deliverable records addressed to it
	Calls        int            `json:"calls"`              // session output + inbox drain it ran
	WakeBytes    int            `json:"wake_bytes"`         // bytes typed or injected by its wakes
	// PeakHour is the most wakes it got in any 60 minutes of the window.
	PeakHour int     `json:"peak_hour"`
	wakeAt   []int64 // commit times of its wakes, for PeakHour
}

// Stats is the `msg stats --json` document.
type Stats struct {
	Profile  string         `json:"profile,omitempty"`
	Store    string         `json:"store,omitempty"`
	Epoch    int64          `json:"epoch,omitempty"`
	SinceMS  int64          `json:"since_ms"`
	UntilMS  int64          `json:"until_ms"`
	Hours    float64        `json:"hours"` // the measured span (window clipped to the first record)
	Records  int            `json:"records"`
	ByKind   map[string]int `json:"by_kind"`
	ByTier   map[string]int `json:"by_tier"`
	ByTool   map[string]int `json:"by_tool,omitempty"`
	Imported int            `json:"imported"` // records pulled from another host
	Targets  Targets        `json:"targets"`
	Parents  []ParentStats  `json:"parents"`
}

// StatsOptions selects the window and, optionally, one parent.
type StatsOptions struct {
	Since  time.Time
	Until  time.Time
	Parent string // only records from or to this parent (its wakes, calls, children's records, sends it observes)
}

func f64(v float64) *float64 { return &v }

func (t *Target) set(value float64, n int) {
	t.Value, t.N = f64(math.Round(value*100)/100), n
	var met bool
	switch t.Op {
	case "<=":
		met = value <= t.Target
	case ">=":
		met = value >= t.Target
	case ">":
		met = value > t.Target
	}
	t.Met = &met
}

// NewTargets returns the seven targets with their definitions and no
// values (what an empty window or a missing ledger reports).
func NewTargets() Targets {
	return Targets{
		WakesPerParentHour: Target{Target: TargetWakesPerParentHour, Op: "<=", Note: "the busiest parent's machine wakes per hour over the window (never divided by less than one hour; parents[].peak_hour is its busiest 60 minutes); counts the wakes agent-deck records (inbox typed nudge, inbox digest, inbox Stop-hook block, the ledger's own wakes), not heartbeats, timers or direct session sends, so it is a lower bound of the #2482 baseline's machine wakes"},
		TextPct:            Target{Target: TargetTextPct, Op: ">=", Note: "turn, status, send and human records (noise excluded) that carry text"},
		RecordsPerFinished: Target{Target: TargetRecordsPerFinished, Op: "<=", Note: "deliverable turn and status records per turn carrying a completion sentinel"},
		DuplicatePct:       Target{Target: TargetDuplicatePct, Op: "<=", Note: "turn records repeating a key, or a child's same text signalled within 5 s under another key"},
		CallsPerWake:       Target{Target: TargetCallsPerWake, Op: "<=", Note: "session output and inbox drain calls made by sessions (heartbeat drains included), per recorded wake; calls with no wake at all are not met"},
		SendSenderTextPct:  Target{Target: TargetSendSenderTextPct, Op: ">=", Note: "local send records with a sender (a session, or cli for a person at a shell or an unattributed --no-tag send), a text hash and a final delivery state in the window"},
		CrossHostLatency:   Target{Target: TargetCrossHostLatency, Op: ">", Note: "imported records whose cross-host latency was measured (offset-corrected, uncertainty below the estimate)"},
	}
}

// ComputeStats scans the ledger records committed inside the window and
// returns the measurement document.
func ComputeStats(bus *events.Bus, store StoreIdentity, opts StatsOptions) (Stats, error) {
	if opts.Until.IsZero() {
		opts.Until = time.Now()
	}
	if opts.Since.IsZero() {
		opts.Since = opts.Until.Add(-24 * time.Hour)
	}
	since, until := opts.Since.UnixMilli(), opts.Until.UnixMilli()
	st := Stats{Store: store.ID, Epoch: store.Epoch, SinceMS: since, UntilMS: until,
		ByKind: map[string]int{}, ByTier: map[string]int{}, ByTool: map[string]int{}, Targets: NewTargets()}

	after, err := bus.CursorBefore(opts.Since)
	if err != nil {
		return st, err
	}
	records, _, err := ReadAfter(bus, after, 0)
	if err != nil {
		return st, err
	}

	parents := map[string]*ParentStats{}
	parent := func(id string) *ParentStats {
		p := parents[id]
		if p == nil {
			p = &ParentStats{ID: id, WakesBy: map[string]int{}}
			parents[id] = p
		}
		return p
	}
	wanted := func(id string) bool { return opts.Parent == "" || id == opts.Parent }

	var (
		first                     int64
		textual, textTotal        int
		deliverable, finished     int
		turns, dupExact, dupNear  int
		sends, sendsWhole         int
		imported, importedTimed   int
		keys                      = map[string]bool{}
		lastByChild               = map[string]Record{}
		totalWakes, wakingParents int
		sendReqs                  = map[string]bool{} // sends with a sender and a text hash
		finalReqs                 = map[string]bool{} // requests with a delivery state
	)
	for _, r := range records {
		at := r.TRecord
		if r.TImport > 0 {
			at = r.TImport // a pulled record is local news when it arrived
		}
		if at < since || at > until {
			continue
		}
		if r.Kind == KindDelivery && r.Req != "" && r.State != "" {
			// A final state settles its send whoever is looking (a parent
			// observing its children's exchange included).
			finalReqs[r.Req] = true
		}
		if opts.Parent != "" && r.From != opts.Parent && !containsTo(r.To, opts.Parent) {
			continue // --parent: only records from or to that parent count
		}
		if first == 0 || at < first {
			first = at
		}
		st.Records++
		st.ByKind[r.Kind]++
		if r.Tier != "" {
			st.ByTier[r.Tier]++
		}
		if r.Tool != "" {
			st.ByTool[r.Tool]++
		}
		if r.Origin != "" {
			imported++
			if r.XLatencyMS > 0 {
				importedTimed++
			}
		}
		switch r.Kind {
		case KindTurn, KindStatus, KindSend, KindHuman:
			if r.Tier != TierNoise {
				textTotal++
				if r.Bytes > 0 || r.Text != "" {
					textual++
				}
			}
		}
		switch r.Kind {
		case KindTurn:
			turns++
			if k := r.DedupKey(); k != "" {
				if keys[k] {
					dupExact++
				}
				keys[k] = true
			}
			if prev, ok := lastByChild[r.Origin+"|"+r.From]; ok && r.TH != "" && prev.TH == r.TH && prev.Key != r.Key &&
				absMS(r.TSignal-prev.TSignal) <= duplicateSuspectWindow.Milliseconds() {
				dupNear++
			}
			lastByChild[r.Origin+"|"+r.From] = r
			if r.Done != "" && (len(r.To) > 0 && wanted(r.To[0])) {
				finished++
			}
		case KindSend:
			if r.Origin != "" {
				break // A pulled send is measured on its origin host.
			}
			sends++
			if r.From != "" && r.TH != "" {
				sendReqs[r.Req] = true
			}

		case KindWake:
			if len(r.To) == 0 || !wanted(r.To[0]) {
				continue
			}
			p := parent(r.To[0])
			p.Wakes++
			path := r.Trigger
			if path == "" {
				path = "ledger"
			}
			p.WakesBy[path+"/"+r.Via]++
			p.WakeBytes += r.Bytes
			p.wakeAt = append(p.wakeAt, at)
		case KindCall:
			if !wanted(r.From) || (r.State != CallSessionOutput && r.State != CallInboxDrain) {
				continue
			}
			parent(r.From).Calls++
		}
		if (r.Kind == KindTurn || r.Kind == KindStatus) && r.Tier != TierNoise && len(r.To) > 0 && wanted(r.To[0]) {
			deliverable++
			if r.To[0] != "" {
				parent(r.To[0]).Records++
			}
		}
	}
	st.Imported = imported

	st.SinceMS = since
	span := until - since
	if first > since {
		span = until - first
	}
	st.Hours = math.Max(float64(span)/float64(time.Hour.Milliseconds()), 1.0/60)
	st.Hours = math.Round(st.Hours*1000) / 1000

	var calls int
	maxRate := 0.0
	// A rate never extrapolates a window shorter than an hour: 3 wakes in
	// 10 minutes are 3 per hour, not 18.
	rateHours := math.Max(st.Hours, 1)
	for _, p := range parents {
		p.WakesPerHour = math.Round(float64(p.Wakes)/rateHours*100) / 100
		p.PeakHour = peakPerHour(p.wakeAt)
		calls += p.Calls
		if p.Wakes > 0 {
			wakingParents++
			totalWakes += p.Wakes
			if p.WakesPerHour > maxRate {
				maxRate = p.WakesPerHour
			}
		}
		if len(p.WakesBy) == 0 {
			p.WakesBy = nil
		}
		st.Parents = append(st.Parents, *p)
	}
	sort.Slice(st.Parents, func(i, j int) bool {
		if st.Parents[i].Wakes != st.Parents[j].Wakes {
			return st.Parents[i].Wakes > st.Parents[j].Wakes
		}
		return st.Parents[i].ID < st.Parents[j].ID
	})
	if st.Parents == nil {
		st.Parents = []ParentStats{}
	}
	if len(st.ByTool) == 0 {
		st.ByTool = nil
	}

	t := &st.Targets
	if totalWakes > 0 {
		t.WakesPerParentHour.set(maxRate, wakingParents)
	} else if st.Records > 0 {
		// No wake was recorded: zero wakes, but a daemon too old to record
		// them looks the same, so no verdict.
		t.WakesPerParentHour.Value, t.WakesPerParentHour.N = f64(0), 0
	}
	if textTotal > 0 {
		t.TextPct.set(100*float64(textual)/float64(textTotal), textTotal)
	}
	if finished > 0 {
		t.RecordsPerFinished.set(float64(deliverable)/float64(finished), finished)
	}
	if turns > 0 {
		t.DuplicatePct.set(100*float64(dupExact+dupNear)/float64(turns), turns)
	}
	switch {
	case totalWakes > 0:
		t.CallsPerWake.set(float64(calls)/float64(totalWakes), totalWakes)
	case calls > 0:
		// Re-reads with no recorded wake: the per-wake ratio is undefined,
		// and calls exist, so the target is not met.
		t.CallsPerWake.set(float64(calls), 0)
	}
	for req := range sendReqs {
		if finalReqs[req] {
			sendsWhole++
		}
	}
	if sends > 0 {
		t.SendSenderTextPct.set(100*float64(sendsWhole)/float64(sends), sends)
	}
	if imported > 0 {
		t.CrossHostLatency.set(float64(importedTimed), imported)
	}
	return st, nil
}

// peakPerHour is the largest number of times inside any 60-minute span.
func peakPerHour(at []int64) int {
	sort.Slice(at, func(i, j int) bool { return at[i] < at[j] })
	best, lo := 0, 0
	for hi := range at {
		for at[hi]-at[lo] >= time.Hour.Milliseconds() {
			lo++
		}
		best = max(best, hi-lo+1)
	}
	return best
}

func containsTo(to []string, id string) bool {
	for _, t := range to {
		if t == id {
			return true
		}
	}
	return false
}

func absMS(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
