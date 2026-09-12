package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// #2164: selection is a pure function of the observed versions and the
// controller version, sorted by name so sequential runs are deterministic.
func TestPlanRemoteUpdates(t *testing.T) {
	versions := map[string]RemoteVersionState{
		"zeta":  {Version: "1.16.0", Found: true},
		"alpha": {Version: "1.15.0", Found: true},
		"mid":   {Found: false},
		"newer": {Version: "1.17.0", Found: true},
		"blank": {Version: "", Found: true},
	}
	actions := PlanRemoteUpdates(versions, "v1.16.0")

	want := []RemoteUpdateAction{
		{Name: "alpha", Version: "1.15.0", Kind: RemoteUpdateUpgrade},
		{Name: "blank", Kind: RemoteUpdateMissing},
		{Name: "mid", Kind: RemoteUpdateMissing},
		{Name: "newer", Version: "1.17.0", Kind: RemoteUpdateCurrent},
		{Name: "zeta", Version: "1.16.0", Kind: RemoteUpdateCurrent},
	}
	if len(actions) != len(want) {
		t.Fatalf("got %d actions, want %d: %+v", len(actions), len(want), actions)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Errorf("action[%d] = %+v, want %+v", i, actions[i], want[i])
		}
	}
}

func TestRemoteVersionState_Outdated(t *testing.T) {
	cases := []struct {
		name       string
		state      RemoteVersionState
		controller string
		want       bool
	}{
		{"older", RemoteVersionState{Version: "1.15.0", Found: true}, "1.16.0", true},
		{"equal", RemoteVersionState{Version: "1.16.0", Found: true}, "1.16.0", false},
		{"newer remote", RemoteVersionState{Version: "1.16.1", Found: true}, "1.16.0", false},
		{"not found", RemoteVersionState{Found: false}, "1.16.0", false},
		{"dev controller never flags", RemoteVersionState{Version: "1.15.0", Found: true}, "dev", false},
		{"zero controller never flags", RemoteVersionState{Version: "1.15.0", Found: true}, "0.0.0", false},
		{"v prefix", RemoteVersionState{Version: "v1.15.0", Found: true}, "v1.16.0", true},
	}
	for _, tc := range cases {
		if got := tc.state.Outdated(tc.controller); got != tc.want {
			t.Errorf("%s: Outdated(%q) = %v, want %v", tc.name, tc.controller, got, tc.want)
		}
	}
}

func TestShouldAutoUpdateRemotes(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	on := UpdateSettings{AutoUpdateRemotes: boolPtr(true), CheckIntervalHours: 24}
	off := UpdateSettings{AutoUpdateRemotes: boolPtr(false), CheckIntervalHours: 24}
	cases := []struct {
		name     string
		settings UpdateSettings
		remotes  int
		lastRun  time.Time
		want     bool
	}{
		{"on by default", UpdateSettings{CheckIntervalHours: 24}, 2, time.Time{}, true},
		{"opted out", off, 2, time.Time{}, false},
		{"on, never ran", on, 2, time.Time{}, true},
		{"on, no remotes", on, 0, time.Time{}, false},
		{"on, ran an hour ago", on, 2, now.Add(-time.Hour), false},
		{"on, ran a day ago", on, 2, now.Add(-24 * time.Hour), true},
		{"zero interval falls back to 24h", UpdateSettings{AutoUpdateRemotes: boolPtr(true)}, 1, now.Add(-2 * time.Hour), false},
		{"short interval", UpdateSettings{AutoUpdateRemotes: boolPtr(true), CheckIntervalHours: 1}, 1, now.Add(-2 * time.Hour), true},
	}
	for _, tc := range cases {
		if got := ShouldAutoUpdateRemotes(tc.settings, tc.remotes, tc.lastRun, now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// stubInstaller records what UpdateRemotes asks of one remote.
type stubInstaller struct {
	version    string
	found      bool
	platformOK bool
	installErr error
	installs   int
}

func (s *stubInstaller) CheckBinary(context.Context) (string, bool) { return s.version, s.found }
func (s *stubInstaller) DetectPlatform(context.Context) (string, string, error) {
	if !s.platformOK {
		return "", "", errors.New("ssh: connect timed out")
	}
	return "linux", "amd64", nil
}
func (s *stubInstaller) InstallBinary(_ context.Context, data []byte, expected string) error {
	s.installs++
	if string(data) != "binary-for-linux-amd64" {
		return errors.New("unexpected payload")
	}
	if expected != "1.16.0" {
		return errors.New("unexpected expected version " + expected)
	}
	return s.installErr
}

func stubReleaseOptions(stubs map[string]*stubInstaller, installMissing bool) RemoteUpdateOptions {
	return RemoteUpdateOptions{
		InstallMissing: installMissing,
		NewRunner: func(name string, _ RemoteConfig) RemoteBinaryInstaller {
			return stubs[name]
		},
		FetchRelease: func(target string) (*update.Release, error) {
			return &update.Release{TagName: "v" + target}, nil
		},
		Download: func(_ *update.Release, goos, goarch string) ([]byte, error) {
			return []byte("binary-for-" + goos + "-" + goarch), nil
		},
	}
}

// UpdateRemotes: only older remotes are deployed, results come back in name
// order, a failure is reported and does not stop the run, and the versions
// it learned land in the shared cache.
func TestUpdateRemotes_SelectionAndReporting(t *testing.T) {
	setupSessionXDGPathEnv(t)

	stubs := map[string]*stubInstaller{
		"current": {version: "1.16.0", found: true, platformOK: true},
		"old":     {version: "1.15.0", found: true, platformOK: true},
		"broken":  {version: "1.14.0", found: true, platformOK: true, installErr: errors.New("permission denied")},
		"offline": {found: false},
	}
	remotes := map[string]RemoteConfig{
		"current": {Host: "a@current"},
		"old":     {Host: "a@old"},
		"broken":  {Host: "a@broken"},
		"offline": {Host: "a@offline"},
	}

	var seen []string
	opts := stubReleaseOptions(stubs, false)
	opts.OnResult = func(r RemoteUpdateResult) { seen = append(seen, r.Name) }

	results := UpdateRemotes(context.Background(), remotes, "v1.16.0", opts)

	wantOrder := []string{"broken", "current", "offline", "old"}
	if len(results) != len(wantOrder) {
		t.Fatalf("got %d results, want %d", len(results), len(wantOrder))
	}
	for i, name := range wantOrder {
		if results[i].Name != name || seen[i] != name {
			t.Fatalf("result order = %v (callbacks %v), want %v", names(results), seen, wantOrder)
		}
	}

	byName := map[string]RemoteUpdateResult{}
	for _, r := range results {
		byName[r.Name] = r
	}
	if r := byName["old"]; r.Outcome != RemoteUpdateOutcomeUpdated || r.From != "1.15.0" || r.To != "1.16.0" {
		t.Errorf("old: %+v, want updated 1.15.0 -> 1.16.0", r)
	}
	if r := byName["current"]; r.Outcome != RemoteUpdateOutcomeCurrent || r.From != "1.16.0" {
		t.Errorf("current: %+v, want already current", r)
	}
	if r := byName["broken"]; r.Outcome != RemoteUpdateOutcomeFailed || r.Err == nil {
		t.Errorf("broken: %+v, want failed with reason", r)
	}
	if r := byName["offline"]; r.Outcome != RemoteUpdateOutcomeSkipped || !errors.Is(r.Err, ErrRemoteBinaryMissing) {
		t.Errorf("offline: %+v, want skipped (missing) when InstallMissing is off", r)
	}
	if stubs["current"].installs != 0 || stubs["offline"].installs != 0 {
		t.Errorf("current/offline must not be deployed: installs = %d/%d", stubs["current"].installs, stubs["offline"].installs)
	}
	if stubs["old"].installs != 1 || stubs["broken"].installs != 1 {
		t.Errorf("old/broken must be deployed once: installs = %d/%d", stubs["old"].installs, stubs["broken"].installs)
	}
	if CountRemoteUpdateFailures(results) != 1 {
		t.Errorf("failures = %d, want 1", CountRemoteUpdateFailures(results))
	}

	cached := LoadRemoteVersions()
	if cached["old"].Version != "1.16.0" || !cached["old"].Found {
		t.Errorf("cache for old = %+v, want the deployed version", cached["old"])
	}
	if cached["broken"].Version != "1.14.0" {
		t.Errorf("cache for broken = %+v, want its unchanged version", cached["broken"])
	}
	if cached["offline"].Found {
		t.Errorf("cache for offline = %+v, want not found", cached["offline"])
	}
}

func TestUpdateRemotes_InstallMissingDeploysAbsentBinary(t *testing.T) {
	setupSessionXDGPathEnv(t)
	stubs := map[string]*stubInstaller{"fresh": {found: false, platformOK: true}}
	results := UpdateRemotes(context.Background(), map[string]RemoteConfig{"fresh": {Host: "a@fresh"}}, "1.16.0", stubReleaseOptions(stubs, true))
	if len(results) != 1 || results[0].Outcome != RemoteUpdateOutcomeUpdated || results[0].From != "" {
		t.Fatalf("results = %+v, want one install", results)
	}
	if stubs["fresh"].installs != 1 {
		t.Fatalf("installs = %d, want 1", stubs["fresh"].installs)
	}
	if got := results[0].String(); got != "fresh: installed v1.16.0" {
		t.Errorf("String() = %q", got)
	}
}

func TestRemoteVersionCache_RoundTrip(t *testing.T) {
	setupSessionXDGPathEnv(t)
	if got := LoadRemoteVersions(); len(got) != 0 {
		t.Fatalf("fresh cache = %v, want empty", got)
	}
	at := time.Now().Truncate(time.Second)
	if err := RecordRemoteVersions(map[string]RemoteVersionState{"lab": {Version: "1.15.0", Found: true, CheckedAt: at}}); err != nil {
		t.Fatal(err)
	}
	if err := RecordRemoteVersions(map[string]RemoteVersionState{"box": {Found: false, CheckedAt: at}}); err != nil {
		t.Fatal(err)
	}
	got := LoadRemoteVersions()
	if got["lab"].Version != "1.15.0" || !got["lab"].CheckedAt.Equal(at) {
		t.Errorf("lab = %+v", got["lab"])
	}
	if got["box"].Found {
		t.Errorf("box = %+v, want not found", got["box"])
	}
	if !RemoteAutoUpdateRanAt().IsZero() {
		t.Fatal("auto update stamp must start zero")
	}
	if err := MarkRemoteAutoUpdateRan(at); err != nil {
		t.Fatal(err)
	}
	if !RemoteAutoUpdateRanAt().Equal(at) {
		t.Errorf("stamp = %v, want %v", RemoteAutoUpdateRanAt(), at)
	}
	// The stamp must not wipe the remotes and vice versa.
	if LoadRemoteVersions()["lab"].Version != "1.15.0" {
		t.Error("marking the sweep dropped cached versions")
	}
}

func names(results []RemoteUpdateResult) []string {
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Name
	}
	return out
}
