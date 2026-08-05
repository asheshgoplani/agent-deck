package session

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

const defaultRemoteFleetTimeout = 15 * time.Second

// RemoteFleetSnapshot is a read-only view of every configured remote. It is
// deliberately separate from local storage: remote sessions remain owned by
// their remote Agent Deck instance.
type RemoteFleetSnapshot struct {
	ObservedAt time.Time           `json:"observedAt"`
	Remotes    []RemoteFleetRemote `json:"remotes"`
	Counts     RemoteFleetCounts   `json:"counts"`
}

type RemoteFleetRemote struct {
	Name      string              `json:"name"`
	Online    bool                `json:"online"`
	LatencyMS int                 `json:"latencyMs,omitempty"`
	Issue     string              `json:"issue,omitempty"`
	Sessions  []RemoteSessionInfo `json:"sessions"`
}

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

// RemoteFleetScanner projects the existing remote registry and SSH session
// API into a web-safe snapshot. Dependencies are injectable so tests never
// open SSH connections.
type RemoteFleetScanner struct {
	loadConfig func() (*UserConfig, error)
	newRunner  func(string, RemoteConfig) remoteFleetRunner
	now        func() time.Time
	timeout    time.Duration
}

func NewRemoteFleetScanner() *RemoteFleetScanner {
	return &RemoteFleetScanner{
		loadConfig: LoadUserConfig,
		newRunner: func(name string, config RemoteConfig) remoteFleetRunner {
			return NewSSHRunner(name, config)
		},
		now:     time.Now,
		timeout: defaultRemoteFleetTimeout,
	}
}

func (s *RemoteFleetScanner) Scan(ctx context.Context) (RemoteFleetSnapshot, error) {
	if s == nil || s.loadConfig == nil || s.newRunner == nil {
		return RemoteFleetSnapshot{}, fmt.Errorf("remote fleet scanner is not configured")
	}
	config, err := s.loadConfig()
	if err != nil {
		return RemoteFleetSnapshot{}, fmt.Errorf("load remote config: %w", err)
	}
	if config == nil {
		return RemoteFleetSnapshot{}, fmt.Errorf("remote config is unavailable")
	}

	timeout := s.timeout
	if timeout <= 0 {
		timeout = defaultRemoteFleetTimeout
	}
	now := s.now
	if now == nil {
		now = time.Now
	}
	snapshot := RemoteFleetSnapshot{
		ObservedAt: now().UTC(),
		Remotes:    make([]RemoteFleetRemote, 0, len(config.Remotes)),
	}
	if len(config.Remotes) == 0 {
		return snapshot, nil
	}

	CleanStaleSSHSockets()
	results := make(chan RemoteFleetRemote, len(config.Remotes))
	var wg sync.WaitGroup
	for name, remoteConfig := range config.Remotes {
		wg.Add(1)
		go func(name string, remoteConfig RemoteConfig) {
			defer wg.Done()
			remoteCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			remote := RemoteFleetRemote{
				Name: name, Sessions: make([]RemoteSessionInfo, 0),
			}
			runner := s.newRunner(name, remoteConfig)
			sessions, fetchErr := runner.FetchSessions(remoteCtx)
			if fetchErr != nil {
				// SSH errors can include command lines and private paths. The web
				// API exposes only this stable classification.
				remote.Issue = "unavailable"
				results <- remote
				return
			}
			remote.Online = true
			for i := range sessions {
				sessions[i].RemoteName = name
			}
			if sessions != nil {
				remote.Sessions = sessions
			}
			if latency, latencyErr := runner.MeasureLatency(remoteCtx); latencyErr == nil {
				remote.LatencyMS = int(latency.Round(time.Millisecond) / time.Millisecond)
			}
			results <- remote
		}(name, remoteConfig)
	}
	wg.Wait()
	close(results)

	for remote := range results {
		snapshot.Remotes = append(snapshot.Remotes, remote)
	}
	sort.Slice(snapshot.Remotes, func(i, j int) bool {
		return snapshot.Remotes[i].Name < snapshot.Remotes[j].Name
	})
	snapshot.Counts = countRemoteFleet(snapshot.Remotes)
	return snapshot, nil
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
