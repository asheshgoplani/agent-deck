package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeFleetRemoteRunner struct {
	sessions []RemoteSessionInfo
	latency  time.Duration
	err      error
}

type trackingFleetRunner struct {
	active  *atomic.Int32
	peak    *atomic.Int32
	release <-chan struct{}
}

func (f trackingFleetRunner) FetchSessions(ctx context.Context) ([]RemoteSessionInfo, error) {
	active := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		peak := f.peak.Load()
		if active <= peak || f.peak.CompareAndSwap(peak, active) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.release:
		return []RemoteSessionInfo{}, nil
	}
}

func (trackingFleetRunner) MeasureLatency(context.Context) (time.Duration, error) { return 0, nil }

func (f fakeFleetRemoteRunner) FetchSessions(context.Context) ([]RemoteSessionInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]RemoteSessionInfo(nil), f.sessions...), nil
}

func (f fakeFleetRemoteRunner) MeasureLatency(context.Context) (time.Duration, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.latency, nil
}

func TestRemoteFleetScannerReturnsSortedSnapshot(t *testing.T) {
	scanner := NewRemoteFleetScanner()
	scanner.loadConfig = func() (*UserConfig, error) {
		return &UserConfig{Remotes: map[string]RemoteConfig{
			"zeta":  {Host: "zeta@example"},
			"alpha": {Host: "alpha@example"},
		}}, nil
	}
	scanner.newRunner = func(name string, _ RemoteConfig) remoteFleetRunner {
		return fakeFleetRemoteRunner{
			latency: 27 * time.Millisecond,
			sessions: []RemoteSessionInfo{{
				ID: "session-1", Title: "Build", Path: "/work/" + name,
				Group: name, Tool: "codex", Status: "waiting",
			}},
		}
	}
	scanner.now = func() time.Time {
		return time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	}

	scanner.refreshOnce(context.Background())
	snapshot := scanner.Snapshot()
	if len(snapshot.Remotes) != 2 || snapshot.Remotes[0].Name != "alpha" ||
		snapshot.Remotes[1].Name != "zeta" {
		t.Fatalf("remotes = %#v", snapshot.Remotes)
	}
	alpha := snapshot.Remotes[0]
	if !alpha.Online || alpha.LatencyMS != 27 || len(alpha.Sessions) != 1 {
		t.Fatalf("alpha = %#v", alpha)
	}
	if alpha.Sessions[0].RemoteName != "alpha" {
		t.Fatalf("session remote = %q, want alpha", alpha.Sessions[0].RemoteName)
	}
	if snapshot.Counts.RemotesOnline != 2 || snapshot.Counts.Sessions != 2 ||
		snapshot.Counts.Waiting != 2 {
		t.Fatalf("counts = %#v", snapshot.Counts)
	}
}

func TestRemoteFleetScannerKeepsOfflineRemoteWithoutLeakingError(t *testing.T) {
	scanner := NewRemoteFleetScanner()
	scanner.loadConfig = func() (*UserConfig, error) {
		return &UserConfig{Remotes: map[string]RemoteConfig{
			"offline": {Host: "offline@example"},
		}}, nil
	}
	scanner.newRunner = func(string, RemoteConfig) remoteFleetRunner {
		return fakeFleetRemoteRunner{err: errors.New("secret ssh command and path")}
	}

	scanner.refreshOnce(context.Background())
	snapshot := scanner.Snapshot()
	remote := snapshot.Remotes[0]
	if remote.Online || remote.Issue != "unavailable" || remote.Sessions == nil {
		t.Fatalf("offline remote = %#v", remote)
	}
	if snapshot.Counts.RemotesOffline != 1 {
		t.Fatalf("counts = %#v", snapshot.Counts)
	}
}

func TestRemoteFleetScannerReturnsEmptySnapshotWithoutRemotes(t *testing.T) {
	scanner := NewRemoteFleetScanner()
	scanner.loadConfig = func() (*UserConfig, error) {
		return &UserConfig{}, nil
	}
	scanner.refreshOnce(context.Background())
	snapshot := scanner.Snapshot()
	if snapshot.Remotes == nil || len(snapshot.Remotes) != 0 {
		t.Fatalf("remotes = %#v, want non-nil empty slice", snapshot.Remotes)
	}
}

func TestRemoteFleetScannerBoundsConcurrencyAndHonorsLifecycleCancellation(t *testing.T) {
	scanner := NewRemoteFleetScanner()
	scanner.concurrency = 2
	remotes := make(map[string]RemoteConfig)
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		remotes[name] = RemoteConfig{Host: name}
	}
	scanner.loadConfig = func() (*UserConfig, error) { return &UserConfig{Remotes: remotes}, nil }
	var active, peak atomic.Int32
	release := make(chan struct{})
	scanner.newRunner = func(string, RemoteConfig) remoteFleetRunner {
		return trackingFleetRunner{active: &active, peak: &peak, release: release}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { scanner.refreshOnce(ctx); close(done) }()
	deadline := time.Now().Add(time.Second)
	for peak.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := peak.Load(); got != 2 {
		t.Fatalf("peak concurrency = %d, want 2", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scan did not stop with lifecycle context")
	}
}

type sequenceFleetRunner struct {
	mu      *sync.Mutex
	calls   *int
	onFetch func()
}

func (f sequenceFleetRunner) FetchSessions(context.Context) ([]RemoteSessionInfo, error) {
	if f.onFetch != nil {
		f.onFetch()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	*f.calls++
	if *f.calls > 1 {
		return nil, errors.New("offline")
	}
	return []RemoteSessionInfo{{ID: "kept", Status: "waiting"}}, nil
}

func TestRemoteFleetScannerStartsFailureBackoffAfterScanCompletes(t *testing.T) {
	scanner := NewRemoteFleetScanner()
	startedAt := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	current := startedAt
	scanner.now = func() time.Time { return current }
	scanner.loadConfig = func() (*UserConfig, error) {
		return &UserConfig{Remotes: map[string]RemoteConfig{"slow": {Host: "slow"}}}, nil
	}
	var mu sync.Mutex
	calls := 1 // sequence runner fails when calls advances above one
	scanner.newRunner = func(string, RemoteConfig) remoteFleetRunner {
		return sequenceFleetRunner{
			mu: &mu, calls: &calls,
			onFetch: func() { current = current.Add(10 * time.Second) },
		}
	}

	scanner.refreshOnce(context.Background())
	scanner.mu.RLock()
	state := scanner.states["slow"]
	scanner.mu.RUnlock()
	want := startedAt.Add(10*time.Second + defaultRemoteFleetRefresh)
	if !state.nextAttempt.Equal(want) {
		t.Fatalf("next attempt = %v, want scan completion + backoff = %v", state.nextAttempt, want)
	}
}

func (sequenceFleetRunner) MeasureLatency(context.Context) (time.Duration, error) { return 0, nil }

func TestRemoteFleetScannerRetainsStaleResultsAndBacksOffFailures(t *testing.T) {
	scanner := NewRemoteFleetScanner()
	current := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	scanner.now = func() time.Time { return current }
	scanner.refresh = time.Second
	scanner.loadConfig = func() (*UserConfig, error) {
		return &UserConfig{Remotes: map[string]RemoteConfig{"build": {Host: "build"}}}, nil
	}
	var mu sync.Mutex
	calls := 0
	scanner.newRunner = func(string, RemoteConfig) remoteFleetRunner { return sequenceFleetRunner{mu: &mu, calls: &calls} }
	scanner.refreshOnce(context.Background())
	current = current.Add(time.Second)
	scanner.refreshOnce(context.Background())
	snapshot := scanner.Snapshot()
	if len(snapshot.Remotes) != 1 || !snapshot.Remotes[0].Stale || snapshot.Remotes[0].Online || len(snapshot.Remotes[0].Sessions) != 1 {
		t.Fatalf("stale snapshot = %#v", snapshot)
	}
	current = current.Add(time.Second)
	scanner.refreshOnce(context.Background())
	mu.Lock()
	gotCalls := calls
	mu.Unlock()
	if gotCalls != 2 {
		t.Fatalf("calls during failure backoff = %d, want 2", gotCalls)
	}
}
