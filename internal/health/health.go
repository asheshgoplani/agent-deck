// Package health collects bounded, process-local runtime observations.
package health

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	StatusPassBudget = 250 * time.Millisecond
	// DescriptorBudget is the descriptor budget of a process managing no
	// sessions. A process managing sessions gets DescriptorsPerSession more
	// for each one (see DescriptorBudgetFor).
	DescriptorBudget = 512
	// DescriptorsPerSession is the per-session allowance on top of
	// DescriptorBudget. A TUI legitimately holds descriptors in proportion to
	// its fleet: with the MCP pool, one client socket per pooled MCP per
	// session (#2535: 100 sessions with 5 pooled MCPs held a steady 554).
	DescriptorsPerSession = 16
	RemotePollBudget      = 2 * time.Second
	maxFileBytes          = 1 << 20
	retention             = 7 * 24 * time.Hour

	// statusHistoryWindow and statusConsecutiveBreach gate the footer's status
	// pass budget warning behind a sustained breach, so a single slow pass
	// (e.g. the first status refresh of a freshly opened deck) never shows it.
	statusHistoryWindow     = 5
	statusConsecutiveBreach = 3

	// descriptorConsecutiveBreach gates the footer's descriptor warning the
	// same way: the count must stay over budget for this many consecutive
	// samples (one per minute) before the footer shows it.
	descriptorConsecutiveBreach = 2
)

// DescriptorBudgetFor returns the descriptor budget of a process managing
// sessions sessions whose soft RLIMIT_NOFILE is limit (0 when unknown).
// The budget never exceeds four fifths of the limit, so a count that is
// approaching the point where opening a file fails always breaches it.
func DescriptorBudgetFor(sessions int, limit uint64) int {
	if sessions < 0 {
		sessions = 0
	}
	budget := DescriptorBudget + DescriptorsPerSession*sessions
	if ceiling := limit - limit/5; limit > 0 && ceiling < uint64(budget) {
		budget = int(ceiling) //nolint:gosec // G115: ceiling < budget, which is an int
	}
	return budget
}

// descriptorBudgetOf is the budget a sample was checked against: the one it
// recorded, or for records written before the budget was recorded, the
// budget its session count implies.
func descriptorBudgetOf(s Sample) int {
	if s.OpenFDsBudget != nil {
		return *s.OpenFDsBudget
	}
	sessions := 0
	if s.Sessions != nil {
		sessions = *s.Sessions
	}
	return DescriptorBudgetFor(sessions, 0)
}

func descriptorWarning(openFDs, budget int) string {
	return fmt.Sprintf("Health: %d open descriptors exceed the budget of %d; run agent-deck health for details", openFDs, budget)
}

type Remote struct {
	LatencyMS float64 `json:"latency_ms"`
	Outcome   string  `json:"outcome"`
	// StatusPassMS and TmuxCalls are the remote's own status-pass timing for
	// this poll, when it answered with one (#2331: an older remote binary,
	// or one that could not be asked, leaves both zero — LatencyMS is then
	// the only number available, same as before this field existed).
	StatusPassMS int64 `json:"status_pass_ms,omitempty"`
	TmuxCalls    int64 `json:"tmux_calls,omitempty"`
	Sessions     int   `json:"sessions,omitempty"`
}
type Sample struct {
	Version       int       `json:"version"`
	BinaryVersion string    `json:"binary_version,omitempty"`
	Timestamp     time.Time `json:"timestamp"`
	Role          string    `json:"role"`
	PID           int       `json:"pid"`
	StartedAt     time.Time `json:"started_at"`
	CPUPercent    *float64  `json:"cpu_percent"`
	RSSBytes      *uint64   `json:"rss_bytes"`
	OpenFDs       *int      `json:"open_fds"`
	Goroutines    *int      `json:"goroutines"`
	HookFiles     *int      `json:"hook_files"`
	StatusPassMS  *float64  `json:"status_pass_ms"`
	Sessions      *int      `json:"session_count"`
	// TmuxCalls counts process-wide native command starts during the pass; concurrent work can overlap.
	TmuxCalls *int64            `json:"tmux_calls"`
	DBQueryMS *float64          `json:"session_list_db_ms"`
	Remotes   map[string]Remote `json:"remotes"`
	// JournalDropped is the process-wide, cumulative count of session-journal
	// events an AsyncWriter dropped because its queue was full. Not a pointer
	// like the fields above: it is always known, since a process with no
	// journal writer has simply dropped zero.
	JournalDropped int64 `json:"journal_dropped"`
	// OpenFDsSupport says why OpenFDs is or is not set: OpenFDsSampled, or
	// OpenFDsUnsupported on a platform with no native descriptor count.
	// Empty on older records and when a supported sample failed (unknown).
	OpenFDsSupport string `json:"open_fds_support,omitempty"`
	// OpenFDsLimit is the process's soft RLIMIT_NOFILE, when known.
	OpenFDsLimit *uint64 `json:"open_fds_limit,omitempty"`
	// OpenFDsBudget is the descriptor budget this sample was checked
	// against: DescriptorBudgetFor the latest known session count and
	// OpenFDsLimit.
	OpenFDsBudget *int `json:"open_fds_budget,omitempty"`
}

const (
	OpenFDsSampled     = "sampled"
	OpenFDsUnsupported = "unsupported"
)

// sampleOpenFDs counts this process's open descriptors natively. It returns
// false when the platform has no way to count them. Tests replace it.
var sampleOpenFDs = openFDs

// sampleDescriptorLimit returns the soft RLIMIT_NOFILE, 0 when unknown.
// Tests replace it.
var sampleDescriptorLimit = func() uint64 {
	var limit unix.Rlimit
	if unix.Getrlimit(unix.RLIMIT_NOFILE, &limit) != nil {
		return 0
	}
	return uint64(limit.Cur)
}

// sampleInterval is how often Start samples after the first sample. Tests
// shorten it.
var sampleInterval = time.Minute

var observations struct {
	sync.Mutex
	status   *float64
	sessions *int
	tmux     *int64
	db       *float64
	remotes  map[string]Remote
	warning  string
	active   int
}

// Enabled reports whether this process owns an active sampler.
func Enabled() bool { observations.Lock(); defer observations.Unlock(); return observations.active > 0 }

// CurrentWarning returns the latest sampler budget warning without I/O.
func CurrentWarning() string {
	observations.Lock()
	defer observations.Unlock()
	return observations.warning
}

func RecordStatusPass(d time.Duration, sessions int, tmuxCalls int64) {
	v := float64(d) / float64(time.Millisecond)
	observations.Lock()
	defer observations.Unlock()
	observations.status = &v
	observations.sessions = &sessions
	observations.tmux = &tmuxCalls
}
func RecordDBQuery(d time.Duration) {
	v := float64(d) / float64(time.Millisecond)
	observations.Lock()
	defer observations.Unlock()
	observations.db = &v
}

// RecordRemote records one poll's outcome. statusPassMS/tmuxCalls/sessions
// are the remote's own status-pass timing for this poll (#2331), zero when
// it did not answer with one — an older remote binary, or a call that
// failed before it got that far.
func RecordRemote(name string, d time.Duration, outcome string, statusPassMS, tmuxCalls int64, sessions int) {
	observations.Lock()
	defer observations.Unlock()
	if observations.remotes == nil {
		observations.remotes = make(map[string]Remote)
	}
	observations.remotes[name] = Remote{
		LatencyMS:    float64(d) / float64(time.Millisecond),
		Outcome:      outcome,
		StatusPassMS: statusPassMS,
		TmuxCalls:    tmuxCalls,
		Sessions:     sessions,
	}
}

// remoteWarning names the stage a slow or failed poll spent its time in
// (#2331). Without StatusPassMS (an older remote binary, or a call that
// failed before answering with one) it falls back to the old generic
// message — the only number anyone ever had before this field existed.
func remoteWarning(name string, r Remote) string {
	if r.StatusPassMS <= 0 {
		return fmt.Sprintf("Health: remote %s poll is slow or failed", strconv.QuoteToASCII(name))
	}
	return fmt.Sprintf("Health: remote %s list --json status pass is slow (%.1fs, %d tmux calls, %d sessions)",
		strconv.QuoteToASCII(name), float64(r.StatusPassMS)/1000, r.TmuxCalls, r.Sessions)
}

func BudgetWarning(d time.Duration, sessions int, tmuxCalls int64) string {
	if statusPassSustainedBreach(d) {
		return "Health: status pass exceeds 250 ms budget"
	}
	if tmuxCalls > int64(2*sessions) {
		return "Health: tmux calls exceed twice the session count"
	}
	return ""
}

var statusBreach struct {
	sync.Mutex
	recent          []float64
	consecutiveOver int
}

// statusPassSustainedBreach tracks recent status-pass durations and reports
// whether the budget warning should be active: three consecutive breaches, or
// the median of the last five samples, over budget. Any single sample under
// budget clears it immediately.
func statusPassSustainedBreach(d time.Duration) bool {
	ms := float64(d) / float64(time.Millisecond)
	budgetMS := float64(StatusPassBudget / time.Millisecond)
	over := ms >= budgetMS
	statusBreach.Lock()
	defer statusBreach.Unlock()
	statusBreach.recent = append(statusBreach.recent, ms)
	if len(statusBreach.recent) > statusHistoryWindow {
		statusBreach.recent = statusBreach.recent[len(statusBreach.recent)-statusHistoryWindow:]
	}
	if !over {
		statusBreach.consecutiveOver = 0
		return false
	}
	statusBreach.consecutiveOver++
	if statusBreach.consecutiveOver >= statusConsecutiveBreach {
		return true
	}
	if len(statusBreach.recent) < statusHistoryWindow {
		return false
	}
	sorted := append([]float64(nil), statusBreach.recent...)
	sort.Float64s(sorted)
	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + median) / 2
	}
	return median >= budgetMS
}

// ResetStatusPassBreachState clears the sustained-breach tracker. Exposed for tests only.
func ResetStatusPassBreachState() {
	statusBreach.Lock()
	defer statusBreach.Unlock()
	statusBreach.recent = nil
	statusBreach.consecutiveOver = 0
}

// Start samples immediately and once per minute. The returned idempotent stop
// function waits for the writer and records a final sample. Collection errors
// never affect the caller. dir must be the selected profile's health directory.
func Start(dir, role, hooksDir, binaryVersion string) func() {
	started := time.Now().UTC()
	role = filepath.Base(role)
	if role == "." || role == string(filepath.Separator) {
		role = "process"
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d-%d.jsonl", role, os.Getpid(), started.UnixNano()))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return func() {}
	}
	observations.Lock()
	observations.active++
	observations.Unlock()
	var previousCPU *float64
	previousTime := started
	countFDs := sampleOpenFDs
	fdLimit := sampleDescriptorLimit
	interval := sampleInterval
	// lastSessions is the latest session count a status pass recorded. It
	// sizes the descriptor budget of samples taken before the next pass,
	// which leave session_count itself unknown.
	lastSessions := 0
	descriptorBreaches := 0
	sample := func() {
		now := time.Now().UTC()
		s := Sample{Version: 1, BinaryVersion: binaryVersion, Timestamp: now, Role: role, PID: os.Getpid(), StartedAt: started, RSSBytes: residentBytes()}
		g := runtime.NumGoroutine()
		s.Goroutines = &g
		if n, supported := countFDs(); n != nil {
			s.OpenFDs = n
			s.OpenFDsSupport = OpenFDsSampled
		} else if !supported {
			s.OpenFDsSupport = OpenFDsUnsupported
		}
		s.HookFiles = countHooks(hooksDir)
		s.JournalDropped = JournalDropped()

		var usage unix.Rusage
		if unix.Getrusage(unix.RUSAGE_SELF, &usage) == nil {
			cpu := float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
			if previousCPU != nil && now.After(previousTime) {
				v := (cpu - *previousCPU) / now.Sub(previousTime).Seconds() * 100
				s.CPUPercent = &v
			}
			previousCPU = &cpu
			previousTime = now
		}
		observations.Lock()
		s.StatusPassMS = observations.status
		s.Sessions = observations.sessions
		if s.Sessions != nil {
			lastSessions = *s.Sessions
		}
		s.TmuxCalls = observations.tmux
		s.DBQueryMS = observations.db
		if len(observations.remotes) > 0 {
			s.Remotes = make(map[string]Remote, len(observations.remotes))
			for k, v := range observations.remotes {
				s.Remotes[k] = v
			}
		}
		observations.warning = ""
		if s.OpenFDs != nil {
			limit := fdLimit()
			if limit > 0 {
				s.OpenFDsLimit = &limit
			}
			budget := DescriptorBudgetFor(lastSessions, limit)
			s.OpenFDsBudget = &budget
			if *s.OpenFDs >= budget {
				descriptorBreaches++
			} else {
				descriptorBreaches = 0
			}
			if descriptorBreaches >= descriptorConsecutiveBreach {
				observations.warning = descriptorWarning(*s.OpenFDs, budget)
			}
		}
		for name, r := range s.Remotes {
			if r.LatencyMS >= 2000 || r.Outcome != "ok" {
				observations.warning = remoteWarning(name, r)
				break
			}
		}
		observations.status = nil
		observations.sessions = nil
		observations.tmux = nil
		observations.db = nil
		observations.remotes = nil
		observations.Unlock()
		clean(dir, now)
		_ = appendSample(path, s)
	}
	sample()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sample()
			case <-done:
				sample()
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			<-finished
			observations.Lock()
			observations.active--
			if observations.active == 0 {
				observations.warning = ""
			}
			observations.Unlock()
		})
	}
}
func appendSample(path string, s Sample) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("health path is not a regular file")
		}
		if info.Size()+int64(len(data)) > maxFileBytes {
			if err := os.Rename(path, path+".1"); err != nil {
				return err
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	// Close can be the call that surfaces a failed append; report it.
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
func healthFile(name string) bool {
	return strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl.1")
}
func clean(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	files := make([]os.FileInfo, 0, len(entries))
	for _, entry := range entries {
		if !healthFile(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		if info, err := entry.Info(); err == nil {
			files = append(files, info)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModTime().After(files[j].ModTime()) })
	for i, info := range files {
		age := now.Sub(info.ModTime())
		if age > retention || (i >= 128 && age > 2*time.Minute) {
			_ = os.Remove(filepath.Join(dir, info.Name()))
		}
	}

}

// Bound allocation and work when hook directories grow unexpectedly large.
// A count exceeding this limit is unknown, never a truncated count.
func countHooks(dir string) *int {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	entries, err := f.ReadDir(4097)
	if err != nil && err != io.EOF {
		return nil
	}
	if len(entries) > 4096 {
		return nil
	}
	n := 0
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			n++
		}
	}
	return &n
}
