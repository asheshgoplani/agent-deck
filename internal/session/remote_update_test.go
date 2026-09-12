package session

import (
	"context"
	"errors"
	"strings"
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

// #2164: a pre-release controller ("1.16.4-switch-preview.<sha>") is older
// than release 1.16.4, so a remote on that release is current, a remote on
// 1.16.3 is behind, and a remote on the next release is never touched.
func TestPlanRemoteUpdates_PreReleaseControllerIsOlderThanItsRelease(t *testing.T) {
	versions := map[string]RemoteVersionState{
		"on-release":   {Version: "1.16.4", Found: true},
		"behind":       {Version: "1.16.3", Found: true},
		"ahead":        {Version: "1.16.5", Found: true},
		"same-preview": {Version: "1.16.4-switch-preview.abc1234", Found: true},
		// The old comparator read both sides as 1.16.4 and called this
		// current; only a pre-release-aware order marks it behind.
		"older-preview": {Version: "1.16.4-switch-preview.aaa0000", Found: true},
	}
	actions := PlanRemoteUpdates(versions, "1.16.4-switch-preview.abc1234")
	kinds := map[string]RemoteUpdateKind{}
	for _, a := range actions {
		kinds[a.Name] = a.Kind
	}
	want := map[string]RemoteUpdateKind{
		"on-release":    RemoteUpdateCurrent,
		"behind":        RemoteUpdateUpgrade,
		"ahead":         RemoteUpdateCurrent,
		"same-preview":  RemoteUpdateCurrent,
		"older-preview": RemoteUpdateUpgrade,
	}
	for name, kind := range want {
		if kinds[name] != kind {
			t.Errorf("%s: kind = %s, want %s", name, kinds[name], kind)
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
		{"preview controller does not flag its release", RemoteVersionState{Version: "1.16.4", Found: true}, "1.16.4-switch-preview.abc1234", false},
		{"preview controller flags the previous release", RemoteVersionState{Version: "1.16.3", Found: true}, "1.16.4-switch-preview.abc1234", true},
		{"remote on a pre-release of the controller's release is behind", RemoteVersionState{Version: "1.16.4-rc.1", Found: true}, "1.16.4", true},
		{"unparseable remote version never flags", RemoteVersionState{Version: "development build", Found: true}, "1.16.4", false},
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

// #2164: a remote whose install path the deploy cannot write (root-owned
// /usr/local/bin without passwordless sudo) is reported as failed with the
// path, the user and the remedy; the typed error survives the wrapping.
func TestUpdateRemotes_NotWritableInstallPathIsReportedWithRemedy(t *testing.T) {
	setupSessionXDGPathEnv(t)
	notWritable := &update.InstallPathNotWritableError{Path: "/usr/local/bin/agent-deck", User: "daniel"}
	stubs := map[string]*stubInstaller{"locked": {version: "1.15.0", found: true, platformOK: true, installErr: notWritable}}
	results := UpdateRemotes(context.Background(), map[string]RemoteConfig{"locked": {Host: "daniel@locked"}}, "1.16.0", stubReleaseOptions(stubs, false))
	if len(results) != 1 || results[0].Outcome != RemoteUpdateOutcomeFailed {
		t.Fatalf("results = %+v, want one failure", results)
	}
	var got *update.InstallPathNotWritableError
	if !errors.As(results[0].Err, &got) || got.Path != notWritable.Path || got.User != "daniel" {
		t.Fatalf("Err = %v, want the typed not-writable error", results[0].Err)
	}
	line := results[0].String()
	for _, want := range []string{"locked: failed", "install path /usr/local/bin/agent-deck is not writable by daniel", "~/.local/bin", "symlink", "sudo"} {
		if !strings.Contains(line, want) {
			t.Errorf("report %q lacks %q", line, want)
		}
	}
	if CountRemoteUpdateFailures(results) != 1 {
		t.Errorf("failures = %d, want 1", CountRemoteUpdateFailures(results))
	}
	if cached := LoadRemoteVersions()["locked"]; cached.Version != "1.15.0" {
		t.Errorf("cache = %+v, want the unchanged version", cached)
	}
}

// A remote whose agent-deck answers with something that is not a version
// ("development build": parseRemoteVersion hands raw output back, and the
// old comparator read it as 0.0.0, older than everything) is never deployed
// onto, not even by the explicit CLI that installs onto missing remotes.
func TestUpdateRemotes_UnparseableVersionIsSkippedNeverInstalled(t *testing.T) {
	setupSessionXDGPathEnv(t)
	if PlanRemoteUpdates(map[string]RemoteVersionState{"odd": {Version: "development build", Found: true}}, "1.16.0")[0].Kind != RemoteUpdateUnknown {
		t.Fatal("an unparseable version must plan as unknown, not as an upgrade")
	}
	for _, installMissing := range []bool{false, true} {
		stubs := map[string]*stubInstaller{"odd": {version: "development build", found: true, platformOK: true}}
		results := UpdateRemotes(context.Background(), map[string]RemoteConfig{"odd": {Host: "a@odd"}}, "1.16.0", stubReleaseOptions(stubs, installMissing))
		if len(results) != 1 || results[0].Outcome != RemoteUpdateOutcomeSkipped || !errors.Is(results[0].Err, ErrRemoteVersionUnknown) {
			t.Fatalf("installMissing=%v: results = %+v, want skipped as unknown", installMissing, results)
		}
		if stubs["odd"].installs != 0 {
			t.Fatalf("installMissing=%v: installs = %d, want 0", installMissing, stubs["odd"].installs)
		}
		if !strings.Contains(results[0].String(), `"development build"`) {
			t.Errorf("report must quote what the remote said: %s", results[0])
		}
	}
}

// The controller's tag may have no release (a preview build), in which case
// the deploy falls back to the latest release. That fallback must never
// move a remote backwards: controller 1.17.0-preview.2, remote
// 1.17.0-preview.1, latest release 1.16.5 is a skip, not a downgrade.
func TestUpdateRemotes_FallbackReleaseNeverDowngrades(t *testing.T) {
	setupSessionXDGPathEnv(t)
	stub := &stubInstaller{version: "1.17.0-preview.1", found: true, platformOK: true}
	opts := RemoteUpdateOptions{
		NewRunner:    func(string, RemoteConfig) RemoteBinaryInstaller { return stub },
		FetchRelease: func(string) (*update.Release, error) { return &update.Release{TagName: "v1.16.5"}, nil },
		Download:     func(*update.Release, string, string) ([]byte, error) { return []byte("binary-for-linux-amd64"), nil },
	}
	results := UpdateRemotes(context.Background(), map[string]RemoteConfig{"lab": {Host: "a@lab"}}, "1.17.0-preview.2", opts)
	if len(results) != 1 || results[0].Outcome != RemoteUpdateOutcomeSkipped || !errors.Is(results[0].Err, ErrRemoteReleaseNotNewer) {
		t.Fatalf("results = %+v, want skipped (no newer release)", results)
	}
	if stub.installs != 0 {
		t.Fatalf("installs = %d, want 0: a fallback release older than the remote must not be deployed", stub.installs)
	}
	if !strings.Contains(results[0].Err.Error(), "v1.16.5 is not newer than the remote's v1.17.0-preview.1") {
		t.Errorf("reason must name both versions: %v", results[0].Err)
	}
	if cached := LoadRemoteVersions()["lab"]; cached.Version != "1.17.0-preview.1" {
		t.Errorf("cache = %+v, want the remote's unchanged version", cached)
	}
}

// DeployRemoteBinary without a known current version (a missing binary the
// explicit CLI installs) has nothing to compare against and deploys.
func TestDeployRemoteBinary_NoCurrentVersionDeploys(t *testing.T) {
	stub := &stubInstaller{platformOK: true}
	opts := stubReleaseOptions(map[string]*stubInstaller{"x": stub}, true)
	opts.NewRunner = nil
	if _, err := DeployRemoteBinary(context.Background(), stub, "1.16.0", opts); err != nil || stub.installs != 1 {
		t.Fatalf("err = %v, installs = %d", err, stub.installs)
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
