package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeFleetRemoteRunner struct {
	sessions []RemoteSessionInfo
	latency  time.Duration
	err      error
}

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

	snapshot, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
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

	snapshot, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
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
	snapshot, err := scanner.Scan(context.Background())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if snapshot.Remotes == nil || len(snapshot.Remotes) != 0 {
		t.Fatalf("remotes = %#v, want non-nil empty slice", snapshot.Remotes)
	}
}
