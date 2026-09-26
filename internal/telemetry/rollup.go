package telemetry

import (
	"strings"
	"syscall"
	"time"
)

const (
	// dailyEventCap bounds spooled events per local day (rollups are exempt).
	dailyEventCap = 60
	// dailyErrorCap bounds error events per local day.
	dailyErrorCap = 20
	// maxCounter saturates every local counter (v1 rule).
	maxCounter = 1000
)

// DailyRollup holds one local day of counters. Map keys are allow-listed
// enum values only, so nothing here is free text.
type DailyRollup struct {
	Emitted     int    `json:"emitted,omitempty"`
	Dropped     int    `json:"dropped,omitempty"`
	SchemaDrops int    `json:"schema_drops,omitempty"`
	HumanHours  uint32 `json:"human_hours,omitempty"`
	AgentHours  uint32 `json:"agent_hours,omitempty"`
	CLIStarts   uint32 `json:"cli_start_hours,omitempty"`
	CLICmds     int    `json:"cli_cmds,omitempty"`
	TUIStarts   int    `json:"tui_starts,omitempty"`
	Sends       int    `json:"sends,omitempty"`
	Attaches    int    `json:"attaches,omitempty"`
	Errors      int    `json:"errors,omitempty"`
	EnvSnapshot bool   `json:"env_snapshot,omitempty"`

	ErrorKeys map[string]bool          `json:"error_keys,omitempty"`
	Features  map[string]*FeatureCount `json:"features,omitempty"`
	SendsBy   map[string]*SendCount    `json:"sends_by,omitempty"`
	AttachBy  map[string]*AttachCount  `json:"attach_by,omitempty"`
}

// FeatureCount is one feature's use that day.
type FeatureCount struct {
	Count  int `json:"count"`
	Errors int `json:"errors,omitempty"`
}

// SendCount is one (tool, via) message count that day.
type SendCount struct {
	Count  int            `json:"count"`
	Queued int            `json:"queued,omitempty"`
	Len    map[string]int `json:"len,omitempty"`
}

// AttachCount is one (tool, via) attach count and time that day.
type AttachCount struct {
	Count    int   `json:"count"`
	TotalSec int64 `json:"total_sec,omitempty"`
}

func inc(n *int) {
	if *n < maxCounter {
		*n++
	}
}

// day returns (creating) the rollup for a local day.
func (s *State) day(d string) *DailyRollup {
	if s.Daily == nil {
		s.Daily = map[string]*DailyRollup{}
	}
	r := s.Daily[d]
	if r == nil {
		r = &DailyRollup{}
		s.Daily[d] = r
	}
	return r
}

// pruneDaily keeps at most maxDailyDays of rollups (newest).
func (s *State) pruneDaily() {
	if len(s.Daily) <= maxDailyDays {
		return
	}
	days := sortedKeys(s.Daily)
	for _, d := range days[:len(days)-maxDailyDays] {
		delete(s.Daily, d)
	}
}

func pairKey(a, b string) string { return a + "|" + b }

func splitPair(k string) (string, string) {
	a, b, _ := strings.Cut(k, "|")
	return a, b
}

// rollupEvents turns one completed day of counters into rollup events.
// Only rollups allowed at the given level are built.
func rollupEvents(r *DailyRollup, level Level) []struct {
	name  string
	key   string
	props map[string]any
} {
	type ev = struct {
		name  string
		key   string
		props map[string]any
	}
	var out []ev
	if r.HumanHours|r.AgentHours != 0 || r.CLICmds+r.TUIStarts+r.Sends+r.Attaches+r.Dropped > 0 {
		out = append(out, ev{"usage.daily", "", map[string]any{
			"human_hours": int(r.HumanHours), "agent_hours": int(r.AgentHours),
			"cli_cmds": CountBucket(r.CLICmds), "tui_starts": CountBucket(r.TUIStarts),
			"sends": CountBucket(r.Sends), "attaches": CountBucket(r.Attaches),
			"dropped": CountBucket(r.Dropped),
		}})
	}
	if level == LevelBasic {
		return out
	}
	for _, f := range sortedKeys(r.Features) {
		c := r.Features[f]
		out = append(out, ev{"feature.daily", f, map[string]any{
			"feature": f, "count": CountBucket(c.Count), "errors": CountBucket(c.Errors),
		}})
	}
	for _, k := range sortedKeys(r.SendsBy) {
		c := r.SendsBy[k]
		tool, via := splitPair(k)
		out = append(out, ev{"send.daily", k, map[string]any{
			"tool": tool, "via": via, "count": CountBucket(c.Count),
			"len_mode": modeLen(c.Len), "queued": CountBucket(c.Queued),
		}})
	}
	for _, k := range sortedKeys(r.AttachBy) {
		c := r.AttachBy[k]
		tool, via := splitPair(k)
		out = append(out, ev{"attach.daily", k, map[string]any{
			"tool": tool, "via": via, "count": CountBucket(c.Count),
			"total_dur": DurBucket(time.Duration(c.TotalSec) * time.Second),
		}})
	}
	return out
}

// modeLen returns the most common length bucket (ties: the shorter one).
func modeLen(m map[string]int) string {
	best, bestN := bucketLabels[BucketLen][0], -1
	for _, l := range bucketLabels[BucketLen] {
		if m[l] > bestN {
			best, bestN = l, m[l]
		}
	}
	return best
}

// SessionStatus is the status a sampled session was in.
type SessionStatus string

const (
	StatusRunning SessionStatus = "running"
	StatusWaiting SessionStatus = "waiting"
	StatusIdle    SessionStatus = "idle"
	StatusError   SessionStatus = "error"
)

// SessionSample is one session as seen by the TUI status poll.
type SessionSample struct {
	Tool   string
	Status SessionStatus
}

// Sampler builds activity.hourly in the one TUI per machine that holds the
// sampler lock. It is used from the TUI goroutine only.
type Sampler struct {
	unlock     func()
	hourStart  time.Time
	lastSample time.Time
	running    int
	waiting    int
	idle       int
	errored    int
	minutes    int
	human      bool
	tools      uint32
	sawRunning bool
	now        func() time.Time
	emit       func(props map[string]any, at time.Time)
}

// SamplerLockFileName is held for the lifetime of the sampling TUI.
const SamplerLockFileName = "telemetry-sampler.lock"

// NewSampler returns a sampler if this process wins the sampler lock, else
// nil (another TUI samples, so hours are never double counted). Nothing is
// sampled unless recording is possible at all.
func NewSampler() *Sampler {
	if !canRecord() {
		return nil
	}
	path, err := siblingPath(SamplerLockFileName)
	if err != nil {
		return nil
	}
	unlock, err := flockFile(path, syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		return nil
	}
	return &Sampler{unlock: unlock, now: nowFn, emit: func(p map[string]any, at time.Time) {
		recordAt("activity.hourly", p, "", at)
	}}
}

// Observe feeds the status poll. It samples at most once a minute (calling
// sessions only then) and emits the previous hour when the local hour changes.
func (sp *Sampler) Observe(sessions func() []SessionSample) {
	if sp == nil {
		return
	}
	now := sp.now()
	sp.rollHour(now)
	if !sp.lastSample.IsZero() && now.Sub(sp.lastSample) < time.Minute {
		return
	}
	sp.lastSample = now
	var run, wait, idle, errd int
	runningTool := ""
	for _, s := range sessions() {
		switch s.Status {
		case StatusRunning:
			run++
			sp.tools |= ToolBit(s.Tool)
			runningTool = s.Tool
		case StatusWaiting:
			wait++
		case StatusIdle:
			idle++
		case StatusError:
			errd++
		}
	}
	sp.running = max(sp.running, run)
	sp.waiting = max(sp.waiting, wait)
	sp.idle = max(sp.idle, idle)
	sp.errored = max(sp.errored, errd)
	sp.minutes++
	if runningTool != "" && !sp.sawRunning {
		sp.sawRunning = true
		SessionRunning(runningTool)
	}
}

// KeyPressed marks a human key press in the current hour.
func (sp *Sampler) KeyPressed() {
	if sp == nil {
		return
	}
	sp.rollHour(sp.now())
	sp.human = true
}

func (sp *Sampler) rollHour(now time.Time) {
	h := now.Truncate(time.Hour)
	if sp.hourStart.IsZero() {
		sp.hourStart = h
		return
	}
	if h.Equal(sp.hourStart) {
		return
	}
	sp.flush()
	sp.hourStart = h
}

func (sp *Sampler) flush() {
	if sp.minutes > 0 || sp.human {
		sp.emit(map[string]any{
			"running": CountBucket(sp.running), "waiting": CountBucket(sp.waiting),
			"idle": CountBucket(sp.idle), "error": CountBucket(sp.errored),
			"sampled_min": CountBucket(sp.minutes), "human_active": sp.human,
			"tools_running": int(sp.tools),
		}, sp.hourStart)
	}
	*sp = Sampler{unlock: sp.unlock, now: sp.now, emit: sp.emit, sawRunning: sp.sawRunning}
}

// Close flushes the current hour to the spool and releases the lock.
func (sp *Sampler) Close() {
	if sp == nil {
		return
	}
	sp.flush()
	if sp.unlock != nil {
		sp.unlock()
		sp.unlock = nil
	}
}

// DailyEventCap is the published per-day event cap (rollups exempt).
const DailyEventCap = dailyEventCap

// CapUsage reports today's spooled and dropped event counts.
func CapUsage(s *State, now time.Time) (emitted, dropped int) {
	if r := s.Daily[dayOf(now)]; r != nil {
		return r.Emitted, r.Dropped
	}
	return 0, 0
}
