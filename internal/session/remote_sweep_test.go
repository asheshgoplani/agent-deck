package session

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// #2244: a remote refused by another deploy's lock is not a failure of this
// run; it is reported as skipped so `remote update --all` exits 0 and the
// startup sweep only logs it.
func TestUpdateRemotes_BusyRemoteIsSkippedNotFailed(t *testing.T) {
	setupSessionXDGPathEnv(t)
	stubs := map[string]*stubInstaller{"busy": {version: "1.15.0", found: true, platformOK: true, installErr: ErrRemoteDeployBusy}}
	results := UpdateRemotes(context.Background(), map[string]RemoteConfig{"busy": {Host: "a@busy"}}, "1.16.0", stubReleaseOptions(stubs, false))
	if len(results) != 1 || results[0].Outcome != RemoteUpdateOutcomeSkipped || !errors.Is(results[0].Err, ErrRemoteDeployBusy) {
		t.Fatalf("results = %+v, want skipped (busy)", results)
	}
	if CountRemoteUpdateFailures(results) != 0 {
		t.Fatal("a busy remote must not count as a failure")
	}
}

// #2244: the version a deploy establishes is written to the cache before the
// next remote is touched (and before OnResult), so `remote list` never
// shows the old version once a remote is updated, even mid-run.
func TestUpdateRemotes_RecordsEachVersionBeforeMovingOn(t *testing.T) {
	setupSessionXDGPathEnv(t)
	stubs := map[string]*stubInstaller{
		"a-old": {version: "1.15.0", found: true, platformOK: true},
		"b-old": {version: "1.15.0", found: true, platformOK: true},
	}
	opts := stubReleaseOptions(stubs, false)
	seen := map[string]string{}
	opts.OnResult = func(r RemoteUpdateResult) { seen[r.Name] = LoadRemoteVersions()[r.Name].Version }
	UpdateRemotes(context.Background(), map[string]RemoteConfig{"a-old": {Host: "x@a"}, "b-old": {Host: "x@b"}}, "1.16.0", opts)
	for _, name := range []string{"a-old", "b-old"} {
		if seen[name] != "1.16.0" {
			t.Errorf("%s: cache read inside OnResult = %q, want 1.16.0 already recorded", name, seen[name])
		}
	}
}

// #2244: where the deploy put the binary travels with the result.
func TestUpdateRemotes_CarriesTheInstallReport(t *testing.T) {
	setupSessionXDGPathEnv(t)
	stub := &reportingStub{stubInstaller: stubInstaller{version: "1.15.0", found: true, platformOK: true}, report: "deployed to /home/x/.local/bin/agent-deck"}
	opts := stubReleaseOptions(nil, false)
	opts.NewRunner = func(string, RemoteConfig) RemoteBinaryInstaller { return stub }
	results := UpdateRemotes(context.Background(), map[string]RemoteConfig{"lab": {Host: "x@lab"}}, "1.16.0", opts)
	if results[0].Note != stub.report || !strings.Contains(results[0].String(), stub.report) {
		t.Fatalf("result = %+v (%s), want the install report", results[0], results[0])
	}
}

type reportingStub struct {
	stubInstaller
	report string
}

func (s *reportingStub) LastInstallReport() string { return s.report }

// #2244: a sweep marks itself in progress in the cache so another update on
// the same controller can see it: the marker names a live pid and is gone
// once the sweep ends or when its process died.
func TestRemoteSweepMarker(t *testing.T) {
	setupSessionXDGPathEnv(t)
	if _, ok := RemoteSweepInProgress(); ok {
		t.Fatal("no sweep must be in progress in a fresh cache")
	}
	end, err := BeginRemoteSweep([]string{"lab", "prod"})
	if err != nil {
		t.Fatal(err)
	}
	sweep, ok := RemoteSweepInProgress()
	if !ok || sweep.PID != os.Getpid() || len(sweep.Remotes) != 2 || sweep.StartedAt.IsZero() {
		t.Fatalf("marker = %+v, %v; want this pid and both remotes", sweep, ok)
	}
	if _, err := BeginRemoteSweep([]string{"lab"}); !errors.Is(err, ErrRemoteSweepRunning) {
		t.Fatalf("a second sweep while one runs: err = %v, want ErrRemoteSweepRunning", err)
	}
	end()
	if _, ok := RemoteSweepInProgress(); ok {
		t.Fatal("marker must be cleared when the sweep ends")
	}

	// A marker whose process is gone is stale and ignored.
	if err := updateRemoteVersionCache(func(c *remoteVersionCache) {
		c.Sweep = &remoteSweepMarker{PID: 2147483000, StartedAt: time.Now(), Remotes: []string{"lab"}}
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := RemoteSweepInProgress(); ok {
		t.Fatal("a marker from a dead process must be ignored")
	}
	if _, err := BeginRemoteSweep([]string{"lab"}); err != nil {
		t.Fatalf("a stale marker must not block a new sweep: %v", err)
	}
}
