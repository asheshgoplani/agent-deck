package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type autoUpdateStub struct {
	version  string
	found    bool
	installs int
}

func (s *autoUpdateStub) CheckBinary(context.Context) (string, bool) { return s.version, s.found }
func (s *autoUpdateStub) DetectPlatform(context.Context) (string, string, error) {
	return "", "", errors.New("stub must not reach the deploy step")
}
func (s *autoUpdateStub) InstallBinary(context.Context, []byte, string) error {
	s.installs++
	return nil
}

// #2164: the startup sweep never installs onto a remote it cannot version
// (offline or missing binary), reports current remotes as such, and stamps
// the run so the next startup within the interval skips it.
func TestRunRemoteAutoUpdate_SkipsMissingAndStamps(t *testing.T) {
	setupTask6XDGEnv(t)

	stubs := map[string]*autoUpdateStub{
		"current": {version: "1.16.0", found: true},
		"offline": {found: false},
	}
	orig := remoteAutoUpdateRunner
	remoteAutoUpdateRunner = func(name string, _ session.RemoteConfig) session.RemoteBinaryInstaller { return stubs[name] }
	t.Cleanup(func() { remoteAutoUpdateRunner = orig })

	if !session.RemoteAutoUpdateRanAt().IsZero() {
		t.Fatal("stamp must start zero in an isolated home")
	}
	settings := session.UpdateSettings{CheckIntervalHours: 24} // on by default
	if !session.ClaimRemoteAutoUpdateRun(settings, 2, time.Now()) {
		t.Fatal("the first startup must claim the sweep")
	}
	results := runRemoteAutoUpdate(map[string]session.RemoteConfig{
		"current": {Host: "a@current"},
		"offline": {Host: "a@offline"},
	}, "1.16.0")

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Name != "current" || results[0].Outcome != session.RemoteUpdateOutcomeCurrent {
		t.Errorf("current: %+v", results[0])
	}
	if results[1].Name != "offline" || results[1].Outcome != session.RemoteUpdateOutcomeSkipped {
		t.Errorf("offline: %+v, want skipped, never installed", results[1])
	}
	if stubs["offline"].installs != 0 {
		t.Errorf("offline installs = %d, want 0", stubs["offline"].installs)
	}
	if session.RemoteAutoUpdateRanAt().IsZero() {
		t.Error("the claim must stamp the run time")
	}
	if session.ClaimRemoteAutoUpdateRun(settings, 2, time.Now()) {
		t.Error("a second startup right after the sweep must not sweep again")
	}
}

// #2164: the sweep that follows a successful `agent-deck update` installs
// onto remotes it could not version only when a person answered the prompt;
// the unattended run (auto_update_remotes on) skips them like the startup
// sweep does, so a failed probe never turns into a blind install. Driven
// through the production sweep with a stubbed runner, so a hardcoded
// InstallMissing at the call site would fail it.
func TestRunPostUpdateRemoteSweep_UnattendedNeverInstallsBlind(t *testing.T) {
	setupTask6XDGEnv(t)
	orig := remoteUpdateRunner
	t.Cleanup(func() { remoteUpdateRunner = orig })

	for _, tc := range []struct {
		name       string
		unattended bool
		want       session.RemoteUpdateOutcome
		installs   int
	}{
		{"unattended skips the unversioned remote", true, session.RemoteUpdateOutcomeSkipped, 0},
		{"prompted run installs onto it", false, session.RemoteUpdateOutcomeFailed, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// found=false: the probe failed. DetectPlatform errors in the stub,
			// so a prompted install is attempted (installs counted by the
			// platform step failing) but never completes.
			stub := &probeFailedStub{}
			remoteUpdateRunner = func(string, session.RemoteConfig) session.RemoteBinaryInstaller { return stub }
			results := runPostUpdateRemoteSweep(context.Background(), map[string]session.RemoteConfig{"offline": {Host: "a@offline"}}, "1.16.0", tc.unattended)
			if len(results) != 1 || results[0].Outcome != tc.want {
				t.Fatalf("results = %+v, want outcome %d", results, tc.want)
			}
			if stub.platformProbes != tc.installs {
				t.Fatalf("install attempts = %d, want %d", stub.platformProbes, tc.installs)
			}
			if session.RemoteAutoUpdateRanAt().IsZero() {
				t.Error("the post-update sweep must stamp its run")
			}
		})
	}
}

// probeFailedStub is a remote whose version probe fails; an install attempt
// shows up as a platform probe.
type probeFailedStub struct{ platformProbes int }

func (s *probeFailedStub) CheckBinary(context.Context) (string, bool) { return "", false }
func (s *probeFailedStub) DetectPlatform(context.Context) (string, string, error) {
	s.platformProbes++
	return "", "", errors.New("stub: no platform")
}
func (s *probeFailedStub) InstallBinary(context.Context, []byte, string) error { return nil }

// #2244: `remote update --all` while this controller's startup sweep is still
// running waits for it (bounded) and, if it is still going, reports each
// remote the sweep covers as "being updated by <pid>" without failing, so
// the exit status is 0 and no second deploy races the first.
func TestRunRemoteUpdatesCLI_WaitsForRunningSweepThenReportsIt(t *testing.T) {
	setupTask6XDGEnv(t)
	orig := remoteUpdateRunner
	t.Cleanup(func() { remoteUpdateRunner = orig })
	// Both remotes answer as current so the only thing that decides their
	// outcome is the sweep coordination.
	stub := &autoUpdateStub{version: "1.16.0", found: true}
	remoteUpdateRunner = func(string, session.RemoteConfig) session.RemoteBinaryInstaller { return stub }
	remotes := map[string]session.RemoteConfig{"lab": {Host: "a@lab"}, "other": {Host: "a@other"}}

	end, err := session.BeginRemoteSweep([]string{"lab"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(end)
	results := runRemoteUpdatesCLI(context.Background(), remotes, "1.16.0", 50*time.Millisecond)
	byName := map[string]session.RemoteUpdateResult{}
	for _, r := range results {
		byName[r.Name] = r
	}
	lab := byName["lab"]
	if lab.Outcome != session.RemoteUpdateOutcomeSkipped || !strings.Contains(lab.String(), "sweep already in progress") || !strings.Contains(lab.String(), fmt.Sprintf("being updated by %d", os.Getpid())) {
		t.Fatalf("lab = %+v (%s), want skipped as being updated by the sweep", lab, lab)
	}
	if other := byName["other"]; other.Outcome != session.RemoteUpdateOutcomeCurrent {
		t.Fatalf("a remote the sweep does not cover must be handled normally: %s", other)
	}
	if session.CountRemoteUpdateFailures(results) != 0 {
		t.Fatal("waiting on our own sweep is not a failure")
	}

	// Once the sweep ends within the wait, the update proceeds normally.
	end()
	results = runRemoteUpdatesCLI(context.Background(), remotes, "1.16.0", 50*time.Millisecond)
	for _, r := range results {
		if strings.Contains(r.String(), "sweep") {
			t.Fatalf("no sweep is running any more: %s", r)
		}
	}
}

// The sweeps mark themselves so the CLI can see them.
func TestRunRemoteAutoUpdate_MarksItselfInProgress(t *testing.T) {
	setupTask6XDGEnv(t)
	orig := remoteAutoUpdateRunner
	t.Cleanup(func() { remoteAutoUpdateRunner = orig })
	var seen bool
	remoteAutoUpdateRunner = func(string, session.RemoteConfig) session.RemoteBinaryInstaller {
		_, seen = session.RemoteSweepInProgress()
		return &autoUpdateStub{version: "1.16.0", found: true}
	}
	runRemoteAutoUpdate(map[string]session.RemoteConfig{"lab": {Host: "a@lab"}}, "1.16.0")
	if !seen {
		t.Fatal("the startup sweep must be marked in progress while it runs")
	}
	if _, ok := session.RemoteSweepInProgress(); ok {
		t.Fatal("the marker must be cleared afterwards")
	}
}
