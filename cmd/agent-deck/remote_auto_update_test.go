package main

import (
	"context"
	"errors"
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
// sweep does, so a failed probe never turns into a blind install.
func TestPostUpdateInstallsMissing_OnlyWhenPrompted(t *testing.T) {
	if postUpdateInstallsMissing(true) {
		t.Error("unattended post-update sweep must not install onto unversioned remotes")
	}
	if !postUpdateInstallsMissing(false) {
		t.Error("a prompted, confirmed run keeps the explicit CLI contract of installing missing binaries")
	}
}
