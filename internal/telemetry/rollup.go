package telemetry

import (
	"strings"
	"sync"
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
// sampler lock. The TUI goroutine feeds it; the exit path may close it from
// another goroutine, so every method takes mu.
type Sampler struct {
	mu         sync.Mutex
	unlock     func()
	sawRunning bool
	now        func() time.Time
	emit       func(h hourSample)
	hour       hourSample
}

// hourSample accumulates one local hour. A sampler that closes mid-hour
// stores it in State.OpenHour (local only), so the hour is emitted once, when
// it is over, however many TUIs were opened in it.
type hourSample struct {
	Start      time.Time `json:"start"`
	lastSample time.Time
	Running    int    `json:"running,omitempty"`
	Waiting    int    `json:"waiting,omitempty"`
	Idle       int    `json:"idle,omitempty"`
	Errored    int    `json:"error,omitempty"`
	Minutes    int    `json:"minutes,omitempty"`
	Human      bool   `json:"human,omitempty"`
	Tools      uint32 `json:"tools,omitempty"`
}

func (h *hourSample) active() bool { return h.Minutes > 0 || h.Human }

// merge adds another part of the same hour: peaks stay peaks, minutes add up.
func (h *hourSample) merge(o hourSample) {
	h.Running = max(h.Running, o.Running)
	h.Waiting = max(h.Waiting, o.Waiting)
	h.Idle = max(h.Idle, o.Idle)
	h.Errored = max(h.Errored, o.Errored)
	h.Minutes += o.Minutes
	h.Human = h.Human || o.Human
	h.Tools |= o.Tools
}

func (h *hourSample) props() map[string]any {
	return map[string]any{
		"running": CountBucket(h.Running), "waiting": CountBucket(h.Waiting),
		"idle": CountBucket(h.Idle), "error": CountBucket(h.Errored),
		"sampled_min": CountBucket(h.Minutes), "human_active": h.Human,
		"tools_running": int(h.Tools),
	}
}

// keepsOpenHour reports whether an open hour may be stored or emitted:
// activity.hourly is a full-only event, so below full nothing is kept.
func (s *State) keepsOpenHour() bool { return EffectiveLevel(s) == LevelFull }

// dropOpenHourBelowFull forgets the stored open hour when the effective level
// is below full (a config cap does not go through SetLevel), and reports
// whether state changed. Callers hold the state lock.
func (s *State) dropOpenHourBelowFull() bool {
	if s.OpenHour == nil || s.keepsOpenHour() {
		return false
	}
	s.OpenHour = nil
	return true
}

// emitOpenHour spools the stored open hour with write once now is past it,
// and reports whether state changed. Below full it only forgets it. Callers
// hold the state lock.
func (s *State) emitOpenHour(now time.Time, write func(spoolLine) error) bool {
	if s.dropOpenHourBelowFull() {
		return true
	}
	h := s.OpenHour
	if h == nil || !h.Start.Before(localHourStart(now)) {
		return false
	}
	s.OpenHour = nil
	if h.active() {
		s.spoolTo(write, SurfaceTUI, "activity.hourly", h.props(), "", h.Start)
	}
	return true
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
	sp := &Sampler{unlock: unlock, now: nowFn, emit: recordHour}
	sp.resume()
	return sp
}

// recordHour spools a finished hour. A part of the same hour that an earlier
// TUI stored and this sampler did not adopt (its resume lost the state lock)
// is merged in and cleared under the same lock, so the hour ships once.
func recordHour(h hourSample) {
	recordLocked("activity.hourly", h.props(), h.Start, func(s *State, at time.Time) bool {
		merged := false
		if o := s.OpenHour; o != nil && o.Start.Equal(h.Start) {
			h.merge(*o)
			s.OpenHour, merged = nil, true
		}
		return s.spoolFrom(surface, "activity.hourly", h.props(), "", at) || merged
	})
}

// resume continues the hour an earlier TUI left open, or emits it when that
// hour is over. Below full it forgets it.
func (sp *Sampler) resume() {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	withState(func(s *State, now time.Time) bool {
		if s.OpenHour == nil {
			return false
		}
		if s.dropOpenHourBelowFull() {
			return true
		}
		if !s.emitOpenHour(now, appendSpool) {
			sp.hour, s.OpenHour = *s.OpenHour, nil
		}
		return true
	})
}

// Observe feeds the status poll. It samples at most once a minute (calling
// sessions only then) and emits the previous hour when the local hour changes.
func (sp *Sampler) Observe(sessions func() []SessionSample) {
	if sp == nil {
		return
	}
	runningTool := sp.observe(sessions)
	if runningTool != "" {
		SessionRunning(runningTool)
	}
}

// observe samples under the lock and returns the tool of the first running
// session ever seen ("" after that), for the first_session_running milestone.
func (sp *Sampler) observe(sessions func() []SessionSample) string {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	now := sp.now()
	sp.rollHour(now)
	h := &sp.hour
	if !h.lastSample.IsZero() && now.Sub(h.lastSample) < time.Minute {
		return ""
	}
	h.lastSample = now
	var run, wait, idle, errd int
	runningTool := ""
	for _, s := range sessions() {
		switch s.Status {
		case StatusRunning:
			run++
			h.Tools |= ToolBit(s.Tool)
			runningTool = s.Tool
		case StatusWaiting:
			wait++
		case StatusIdle:
			idle++
		case StatusError:
			errd++
		}
	}
	h.Running = max(h.Running, run)
	h.Waiting = max(h.Waiting, wait)
	h.Idle = max(h.Idle, idle)
	h.Errored = max(h.Errored, errd)
	h.Minutes++
	if runningTool == "" || sp.sawRunning {
		return ""
	}
	sp.sawRunning = true
	return runningTool
}

// KeyPressed marks a human key press in the current hour.
func (sp *Sampler) KeyPressed() {
	if sp == nil {
		return
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.rollHour(sp.now())
	sp.hour.Human = true
}

// Reset forgets the current hour without emitting it. Called when consent is
// granted mid-hour, so nothing observed before the grant is ever recorded.
func (sp *Sampler) Reset() {
	if sp == nil {
		return
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.hour = hourSample{Start: localHourStart(sp.now())}
}

// localHourStart is the start of t's local wall-clock hour. Truncate would
// align to UTC hours, which are not local hours in UTC+5:30 and similar zones.
func localHourStart(t time.Time) time.Time {
	l := t.Local()
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), 0, 0, 0, time.Local)
}

// rollHour emits the previous hour when the local hour changed. Callers hold mu.
func (sp *Sampler) rollHour(now time.Time) {
	h := localHourStart(now)
	if sp.hour.Start.IsZero() {
		sp.hour.Start = h
		return
	}
	if h.Equal(sp.hour.Start) {
		return
	}
	if sp.hour.active() {
		sp.emit(sp.hour)
	}
	sp.hour = hourSample{Start: h}
}

// Close emits a finished hour, stores the still open one in State for the
// next TUI or upload to continue or emit, and releases the lock. Below full
// the open hour is not stored (it could ship after the level is raised).
func (sp *Sampler) Close() {
	if sp == nil {
		return
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.rollHour(sp.now())
	if h := sp.hour; h.active() {
		withState(func(s *State, now time.Time) bool {
			changed := s.emitOpenHour(now, appendSpool)
			if !s.keepsOpenHour() {
				return changed
			}
			if s.OpenHour != nil {
				h.merge(*s.OpenHour)
			}
			s.OpenHour = &h
			return true
		})
	}
	sp.hour = hourSample{}
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
