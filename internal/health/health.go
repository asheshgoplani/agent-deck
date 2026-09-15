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
	DescriptorBudget = 512
	RemotePollBudget = 2 * time.Second
	maxFileBytes     = 1 << 20
	retention        = 7 * 24 * time.Hour
)

type Remote struct {
	LatencyMS float64 `json:"latency_ms"`
	Outcome   string  `json:"outcome"`
}
type Sample struct {
	Version      int       `json:"version"`
	Timestamp    time.Time `json:"timestamp"`
	Role         string    `json:"role"`
	PID          int       `json:"pid"`
	StartedAt    time.Time `json:"started_at"`
	CPUPercent   *float64  `json:"cpu_percent"`
	RSSBytes     *uint64   `json:"rss_bytes"`
	OpenFDs      *int      `json:"open_fds"`
	Goroutines   *int      `json:"goroutines"`
	HookFiles    *int      `json:"hook_files"`
	StatusPassMS *float64  `json:"status_pass_ms"`
	Sessions     *int      `json:"session_count"`
	// TmuxCalls counts process-wide native command starts during the pass; concurrent work can overlap.
	TmuxCalls *int64            `json:"tmux_calls"`
	DBQueryMS *float64          `json:"session_list_db_ms"`
	Remotes   map[string]Remote `json:"remotes"`
}

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
func RecordRemote(name string, d time.Duration, outcome string) {
	observations.Lock()
	defer observations.Unlock()
	if observations.remotes == nil {
		observations.remotes = make(map[string]Remote)
	}
	observations.remotes[name] = Remote{float64(d) / float64(time.Millisecond), outcome}
}
func BudgetWarning(d time.Duration, sessions int, tmuxCalls int64) string {
	if d >= StatusPassBudget {
		return "Health: status pass exceeds 250 ms budget"
	}
	if tmuxCalls > int64(2*sessions) {
		return "Health: tmux calls exceed twice the session count"
	}
	return ""
}

// Start samples immediately and once per minute. The returned idempotent stop
// function waits for the writer and records a final sample. Collection errors
// never affect the caller. dir must be the selected profile's health directory.
func Start(dir, role, hooksDir string) func() {
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
	sample := func() {
		now := time.Now().UTC()
		s := Sample{Version: 1, Timestamp: now, Role: role, PID: os.Getpid(), StartedAt: started, RSSBytes: residentBytes()}
		g := runtime.NumGoroutine()
		s.Goroutines = &g
		if entries, err := os.ReadDir(fdDirectory()); err == nil {
			n := len(entries)
			s.OpenFDs = &n
		}
		s.HookFiles = countHooks(hooksDir)

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
		s.TmuxCalls = observations.tmux
		s.DBQueryMS = observations.db
		if len(observations.remotes) > 0 {
			s.Remotes = make(map[string]Remote, len(observations.remotes))
			for k, v := range observations.remotes {
				s.Remotes[k] = v
			}
		}
		observations.warning = ""
		if s.OpenFDs != nil && *s.OpenFDs >= DescriptorBudget {
			observations.warning = "Health: descriptor count exceeds 512 budget"
		}
		for name, r := range s.Remotes {
			if r.LatencyMS >= 2000 || r.Outcome != "ok" {
				observations.warning = fmt.Sprintf("Health: remote %s poll is slow or failed", strconv.QuoteToASCII(name))
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
		ticker := time.NewTicker(time.Minute)
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
func fdDirectory() string {
	if runtime.GOOS == "linux" {
		return "/proc/self/fd"
	}
	return "/dev/fd"
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
	defer f.Close()
	_, err = f.Write(data)
	return err
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
