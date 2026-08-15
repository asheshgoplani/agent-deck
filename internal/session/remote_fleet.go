package session

import (
	"context"
	"sort"
	"sync"
	"time"
)

const (
	defaultRemoteFleetTimeout     = 15 * time.Second
	defaultRemoteFleetRefresh     = 5 * time.Second
	defaultRemoteFleetConcurrency = 4
	defaultRemoteFleetMaxBackoff  = time.Minute
)

// RemoteFleetSnapshot is a read-only view of every configured remote. It is
// deliberately separate from local storage: remote sessions remain owned by
// their remote Agent Deck instance.
type RemoteFleetSnapshot struct {
	ObservedAt time.Time           `json:"observedAt"`
	Remotes    []RemoteFleetRemote `json:"remotes"`
	Counts     RemoteFleetCounts   `json:"counts"`
}

// RemoteFleetRemote is the last observation of one configured remote. Stale
// records retain their last known sessions after a failed refresh.
type RemoteFleetRemote struct {
	Name       string              `json:"name"`
	Online     bool                `json:"online"`
	LatencyMS  int                 `json:"latencyMs,omitempty"`
	Issue      string              `json:"issue,omitempty"`
	Sessions   []RemoteSessionInfo `json:"sessions"`
	ObservedAt time.Time           `json:"observedAt"`
	AgeSeconds int64               `json:"ageSeconds"`
	Stale      bool                `json:"stale,omitempty"`
}

// RemoteFleetCounts contains aggregate remote and session status counts.
type RemoteFleetCounts struct {
	RemotesOnline  int `json:"remotesOnline"`
	RemotesOffline int `json:"remotesOffline"`
	Sessions       int `json:"sessions"`
	Running        int `json:"running"`
	Waiting        int `json:"waiting"`
	Idle           int `json:"idle"`
	Error          int `json:"error"`
	Stopped        int `json:"stopped"`
}

type remoteFleetRunner interface {
	FetchSessions(context.Context) ([]RemoteSessionInfo, error)
	MeasureLatency(context.Context) (time.Duration, error)
}

type remoteFleetState struct {
	remote      RemoteFleetRemote
	nextAttempt time.Time
	backoff     time.Duration
}

// RemoteFleetScanner owns the single background scan loop used by a web
// server. HTTP requests only read Snapshot and can never initiate SSH work.
type RemoteFleetScanner struct {
	loadConfig  func() (*UserConfig, error)
	newRunner   func(string, RemoteConfig) remoteFleetRunner
	now         func() time.Time
	timeout     time.Duration
	refresh     time.Duration
	maxBackoff  time.Duration
	concurrency int

	mu       sync.RWMutex
	snapshot RemoteFleetSnapshot
	states   map[string]remoteFleetState
	start    sync.Once
}

// NewRemoteFleetScanner creates an idle scanner. Start ties its refresh loop
// to the caller's lifecycle context.
func NewRemoteFleetScanner() *RemoteFleetScanner {
	return &RemoteFleetScanner{
		loadConfig: LoadUserConfig,
		newRunner: func(name string, config RemoteConfig) remoteFleetRunner {
			return NewSSHRunner(name, config)
		},
		now: time.Now, timeout: defaultRemoteFleetTimeout,
		refresh: defaultRemoteFleetRefresh, maxBackoff: defaultRemoteFleetMaxBackoff,
		concurrency: defaultRemoteFleetConcurrency, states: make(map[string]remoteFleetState),
		snapshot: RemoteFleetSnapshot{Remotes: make([]RemoteFleetRemote, 0)},
	}
}

// Start begins the server-owned refresh loop. Repeated calls are harmless.
func (s *RemoteFleetScanner) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.start.Do(func() { go s.run(ctx) })
}

func (s *RemoteFleetScanner) run(ctx context.Context) {
	s.refreshOnce(ctx)
	refresh := s.refresh
	if refresh <= 0 {
		refresh = defaultRemoteFleetRefresh
	}
	ticker := time.NewTicker(refresh)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshOnce(ctx)
		}
	}
}

// Snapshot returns a copy of the latest in-memory result without doing I/O.
func (s *RemoteFleetScanner) Snapshot() RemoteFleetSnapshot {
	if s == nil {
		return RemoteFleetSnapshot{Remotes: make([]RemoteFleetRemote, 0)}
	}
	s.mu.RLock()
	snapshot := cloneRemoteFleetSnapshot(s.snapshot)
	s.mu.RUnlock()
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	for i := range snapshot.Remotes {
		if !snapshot.Remotes[i].ObservedAt.IsZero() {
			age := now.Sub(snapshot.Remotes[i].ObservedAt)
			if age > 0 {
				snapshot.Remotes[i].AgeSeconds = int64(age / time.Second)
			}
		}
	}
	return snapshot
}

func (s *RemoteFleetScanner) refreshOnce(ctx context.Context) {
	if s.loadConfig == nil || s.newRunner == nil || ctx.Err() != nil {
		return
	}
	config, err := s.loadConfig()
	if err != nil || config == nil {
		return
	}
	now := s.now
	if now == nil {
		now = time.Now
	}
	observedAt := now().UTC()

	s.mu.Lock()
	for name := range s.states {
		if _, configured := config.Remotes[name]; !configured {
			delete(s.states, name)
		}
	}
	type job struct {
		name   string
		config RemoteConfig
	}
	jobs := make([]job, 0, len(config.Remotes))
	for name, remoteConfig := range config.Remotes {
		state, ok := s.states[name]
		if !ok || !observedAt.Before(state.nextAttempt) {
			jobs = append(jobs, job{name, remoteConfig})
		}
	}
	s.mu.Unlock()

	if len(jobs) > 0 {
		CleanStaleSSHSockets()
		limit := s.concurrency
		if limit <= 0 {
			limit = defaultRemoteFleetConcurrency
		}
		if limit > len(jobs) {
			limit = len(jobs)
		}
		jobCh := make(chan job)
		resultCh := make(chan remoteFleetState, len(jobs))
		var workers sync.WaitGroup
		for range limit {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for item := range jobCh {
					resultCh <- s.scanRemote(ctx, item.name, item.config, observedAt)
				}
			}()
		}
		go func() {
			defer close(jobCh)
			for _, item := range jobs {
				select {
				case jobCh <- item:
				case <-ctx.Done():
					return
				}
			}
		}()
		workers.Wait()
		close(resultCh)
		s.mu.Lock()
		for state := range resultCh {
			s.states[state.remote.Name] = state
		}
		s.mu.Unlock()
	}
	s.publishSnapshot(observedAt, config.Remotes)
}

func (s *RemoteFleetScanner) scanRemote(ctx context.Context, name string, config RemoteConfig, observedAt time.Time) remoteFleetState {
	timeout := s.timeout
	if timeout <= 0 {
		timeout = defaultRemoteFleetTimeout
	}
	remoteCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	runner := s.newRunner(name, config)
	sessions, err := runner.FetchSessions(remoteCtx)
	if err != nil {
		s.mu.RLock()
		prior, hadPrior := s.states[name]
		s.mu.RUnlock()
		backoff := defaultRemoteFleetRefresh
		if hadPrior && prior.backoff > 0 {
			backoff = prior.backoff * 2
		}
		maxBackoff := s.maxBackoff
		if maxBackoff <= 0 {
			maxBackoff = defaultRemoteFleetMaxBackoff
		}
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		completedAt := time.Now().UTC()
		if s.now != nil {
			completedAt = s.now().UTC()
		}
		remote := RemoteFleetRemote{Name: name, Issue: "unavailable", Sessions: make([]RemoteSessionInfo, 0), ObservedAt: observedAt}
		if hadPrior {
			remote = prior.remote
			remote.Online = false
			remote.Issue = "unavailable"
			remote.Stale = true
		}
		return remoteFleetState{remote: remote, backoff: backoff, nextAttempt: completedAt.Add(backoff)}
	}
	for i := range sessions {
		sessions[i].RemoteName = name
	}
	if sessions == nil {
		sessions = make([]RemoteSessionInfo, 0)
	}
	remote := RemoteFleetRemote{Name: name, Online: true, Sessions: sessions, ObservedAt: observedAt}
	if latency, latencyErr := runner.MeasureLatency(remoteCtx); latencyErr == nil {
		remote.LatencyMS = int(latency.Round(time.Millisecond) / time.Millisecond)
	}
	return remoteFleetState{remote: remote, nextAttempt: observedAt.Add(s.refresh)}
}

func (s *RemoteFleetScanner) publishSnapshot(observedAt time.Time, configured map[string]RemoteConfig) {
	s.mu.Lock()
	snapshot := RemoteFleetSnapshot{ObservedAt: observedAt, Remotes: make([]RemoteFleetRemote, 0, len(configured))}
	for name := range configured {
		if state, ok := s.states[name]; ok {
			snapshot.Remotes = append(snapshot.Remotes, state.remote)
		}
	}
	sort.Slice(snapshot.Remotes, func(i, j int) bool { return snapshot.Remotes[i].Name < snapshot.Remotes[j].Name })
	snapshot.Counts = countRemoteFleet(snapshot.Remotes)
	s.snapshot = snapshot
	s.mu.Unlock()
}

func cloneRemoteFleetSnapshot(in RemoteFleetSnapshot) RemoteFleetSnapshot {
	out := in
	out.Remotes = append([]RemoteFleetRemote(nil), in.Remotes...)
	for i := range out.Remotes {
		out.Remotes[i].Sessions = append([]RemoteSessionInfo(nil), in.Remotes[i].Sessions...)
		if out.Remotes[i].Sessions == nil && in.Remotes[i].Sessions != nil {
			out.Remotes[i].Sessions = make([]RemoteSessionInfo, 0)
		}
	}
	if out.Remotes == nil {
		out.Remotes = make([]RemoteFleetRemote, 0)
	}
	return out
}

func countRemoteFleet(remotes []RemoteFleetRemote) RemoteFleetCounts {
	var counts RemoteFleetCounts
	for _, remote := range remotes {
		if remote.Online {
			counts.RemotesOnline++
		} else {
			counts.RemotesOffline++
		}
		counts.Sessions += len(remote.Sessions)
		for _, remoteSession := range remote.Sessions {
			switch remoteSession.Status {
			case "running", "starting":
				counts.Running++
			case "waiting":
				counts.Waiting++
			case "idle":
				counts.Idle++
			case "error":
				counts.Error++
			case "stopped":
				counts.Stopped++
			}
		}
	}
	return counts
}
