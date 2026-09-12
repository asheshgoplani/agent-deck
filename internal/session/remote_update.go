package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/update"
)

// remoteVersionCacheFile is the cache-dir file that remembers the last
// agent-deck version each remote reported. The TUI poll, `remote list` and
// `remote update` share it so a version learned by one is shown by the others
// without another SSH round trip (issue #2164).
const remoteVersionCacheFile = "remote-versions.json"

// RemoteVersionState is what a remote last reported for `agent-deck version`.
// Found is false when the binary could not be executed (missing, not on
// $PATH, or the host was unreachable); Version is then empty.
type RemoteVersionState struct {
	Version   string    `json:"version,omitempty"`
	Found     bool      `json:"found"`
	CheckedAt time.Time `json:"checked_at"`
}

// Outdated reports whether the remote runs something older than controller.
// Unknown versions and non-release controller builds never count as drift:
// CompareVersions treats "dev" or "0.0.0" as older than any release, so a
// developer build must not flag every remote. A pre-release controller
// ("1.16.4-preview.abc") is older than release 1.16.4, so a remote on that
// release is not flagged either (#2164).
func (s RemoteVersionState) Outdated(controller string) bool {
	if !s.Found || !isVersionString(s.Version) || !isReleaseVersion(controller) {
		return false
	}
	return update.CompareVersions(s.Version, controller) < 0
}

// isVersionString reports whether v is something CompareVersions can order:
// a dotted numeric core with an optional pre-release tag. parseRemoteVersion
// hands back raw output ("development build") when it finds no version
// token, and CompareVersions would read that as 0.0.0, older than anything;
// an unattended sweep must never act on it (#2164).
func isVersionString(v string) bool {
	return versionStringRe.MatchString(strings.TrimSpace(v))
}

// versionStringRe is remoteVersionRe anchored to the whole string.
var versionStringRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:[.\-+][0-9A-Za-z.\-]+)?$`)

func isReleaseVersion(v string) bool {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return false
	}
	var major, minor, patch int
	n, err := fmt.Sscanf(v, "%d.%d.%d", &major, &minor, &patch)
	return err == nil && n == 3 && (major > 0 || minor > 0 || patch > 0)
}

// remoteVersionCache is the on-disk shape of remoteVersionCacheFile.
type remoteVersionCache struct {
	Remotes map[string]RemoteVersionState `json:"remotes"`
	// AutoUpdateRanAt throttles the startup auto-update sweep.
	AutoUpdateRanAt time.Time `json:"auto_update_ran_at,omitempty"`
}

var remoteVersionCacheMu sync.Mutex

func remoteVersionCachePath() (string, error) {
	return agentpaths.CachePath(remoteVersionCacheFile)
}

func loadRemoteVersionCache() remoteVersionCache {
	var cache remoteVersionCache
	path, err := remoteVersionCachePath()
	if err != nil {
		return cache
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cache
	}
	_ = json.Unmarshal(data, &cache)
	if cache.Remotes == nil {
		cache.Remotes = map[string]RemoteVersionState{}
	}
	return cache
}

func saveRemoteVersionCache(cache remoteVersionCache) error {
	path, err := remoteVersionCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// LoadRemoteVersions returns the cached per-remote version states. Missing or
// unreadable cache yields an empty map, never an error: the cache is a hint.
func LoadRemoteVersions() map[string]RemoteVersionState {
	remoteVersionCacheMu.Lock()
	defer remoteVersionCacheMu.Unlock()
	cache := loadRemoteVersionCache()
	out := make(map[string]RemoteVersionState, len(cache.Remotes))
	for name, state := range cache.Remotes {
		out[name] = state
	}
	return out
}

// RecordRemoteVersions merges freshly observed states into the cache. A
// dropped write is recoverable on the next observation, so errors are
// returned for logging only.
func RecordRemoteVersions(states map[string]RemoteVersionState) error {
	if len(states) == 0 {
		return nil
	}
	remoteVersionCacheMu.Lock()
	defer remoteVersionCacheMu.Unlock()
	cache := loadRemoteVersionCache()
	if cache.Remotes == nil {
		cache.Remotes = map[string]RemoteVersionState{}
	}
	for name, state := range states {
		cache.Remotes[name] = state
	}
	return saveRemoteVersionCache(cache)
}

// RemoteAutoUpdateRanAt returns when the background remote auto-update sweep
// last ran (zero when never).
func RemoteAutoUpdateRanAt() time.Time {
	remoteVersionCacheMu.Lock()
	defer remoteVersionCacheMu.Unlock()
	return loadRemoteVersionCache().AutoUpdateRanAt
}

// MarkRemoteAutoUpdateRan stamps the sweep time used by ShouldAutoUpdateRemotes.
func MarkRemoteAutoUpdateRan(at time.Time) error {
	remoteVersionCacheMu.Lock()
	defer remoteVersionCacheMu.Unlock()
	cache := loadRemoteVersionCache()
	cache.AutoUpdateRanAt = at
	return saveRemoteVersionCache(cache)
}

// ShouldAutoUpdateRemotes is the pure decision behind the startup sweep:
// the key must not be off (it is on by default), there must be remotes, and
// the previous sweep must be older than the update check interval (so a TUI
// restarted ten times in a row does not SSH into every remote ten times). A
// zero lastRun always runs.
func ShouldAutoUpdateRemotes(settings UpdateSettings, remoteCount int, lastRun, now time.Time) bool {
	if !settings.GetAutoUpdateRemotes() || remoteCount == 0 {
		return false
	}
	if lastRun.IsZero() {
		return true
	}
	hours := settings.CheckIntervalHours
	if hours <= 0 {
		hours = 24
	}
	return now.Sub(lastRun) >= time.Duration(hours)*time.Hour
}

// RemoteUpdateKind is what PlanRemoteUpdates decided for one remote.
type RemoteUpdateKind int

const (
	// RemoteUpdateCurrent: the remote runs the controller's version or newer.
	RemoteUpdateCurrent RemoteUpdateKind = iota
	// RemoteUpdateUpgrade: the remote reported an older release.
	RemoteUpdateUpgrade
	// RemoteUpdateMissing: no runnable agent-deck was found on the remote
	// (or the host was unreachable). Installing is a caller's choice.
	RemoteUpdateMissing
	// RemoteUpdateUnknown: the remote ran agent-deck but reported something
	// that is not a version ("development build"). Never deployed onto.
	RemoteUpdateUnknown
)

func (k RemoteUpdateKind) String() string {
	switch k {
	case RemoteUpdateCurrent:
		return "current"
	case RemoteUpdateUpgrade:
		return "upgrade"
	case RemoteUpdateMissing:
		return "missing"
	case RemoteUpdateUnknown:
		return "unknown"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// RemoteUpdateAction is one row of a remote update plan.
type RemoteUpdateAction struct {
	Name    string
	Version string // as reported by the remote; empty when Missing
	Kind    RemoteUpdateKind
}

// PlanRemoteUpdates decides, per remote, whether the controller's version
// should be pushed. Pure: it takes the observed versions and the controller
// version and returns the actions sorted by remote name so reports and
// sequential runs are deterministic.
func PlanRemoteUpdates(versions map[string]RemoteVersionState, controller string) []RemoteUpdateAction {
	actions := make([]RemoteUpdateAction, 0, len(versions))
	for name, state := range versions {
		action := RemoteUpdateAction{Name: name, Version: state.Version}
		switch {
		case !state.Found || state.Version == "":
			action.Kind = RemoteUpdateMissing
			action.Version = ""
		case !isVersionString(state.Version):
			action.Kind = RemoteUpdateUnknown
		case update.CompareVersions(state.Version, controller) < 0:
			action.Kind = RemoteUpdateUpgrade
		default:
			action.Kind = RemoteUpdateCurrent
		}
		actions = append(actions, action)
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].Name < actions[j].Name })
	return actions
}

// RemoteUpdateOutcome is how one remote fared in an update run.
type RemoteUpdateOutcome int

const (
	RemoteUpdateOutcomeCurrent RemoteUpdateOutcome = iota
	RemoteUpdateOutcomeUpdated
	RemoteUpdateOutcomeSkipped
	RemoteUpdateOutcomeFailed
)

// RemoteUpdateResult is the per-remote report line of an update run.
type RemoteUpdateResult struct {
	Name    string
	Host    string
	From    string // version before the run; empty when unknown
	To      string // target version
	Outcome RemoteUpdateOutcome
	Err     error
}

// String renders the one-line report used by the CLI and the log.
func (r RemoteUpdateResult) String() string {
	switch r.Outcome {
	case RemoteUpdateOutcomeUpdated:
		if r.From == "" {
			return fmt.Sprintf("%s: installed v%s", r.Name, r.To)
		}
		return fmt.Sprintf("%s: updated v%s -> v%s", r.Name, r.From, r.To)
	case RemoteUpdateOutcomeCurrent:
		return fmt.Sprintf("%s: already current (v%s)", r.Name, r.From)
	case RemoteUpdateOutcomeSkipped:
		return fmt.Sprintf("%s: skipped (%v)", r.Name, r.Err)
	default:
		return fmt.Sprintf("%s: failed (%v)", r.Name, r.Err)
	}
}

// CountRemoteUpdateFailures returns how many results failed; callers map a
// non-zero count to a non-zero exit code.
func CountRemoteUpdateFailures(results []RemoteUpdateResult) int {
	n := 0
	for _, r := range results {
		if r.Outcome == RemoteUpdateOutcomeFailed {
			n++
		}
	}
	return n
}

// RemoteBinaryInstaller is the slice of SSHRunner an update run needs. Tests
// substitute a stub; production passes NewSSHRunner.
type RemoteBinaryInstaller interface {
	CheckBinary(ctx context.Context) (version string, found bool)
	DetectPlatform(ctx context.Context) (goos, goarch string, err error)
	InstallBinary(ctx context.Context, binaryData []byte, expectedVersion string) error
}

// RemoteUpdateOptions tunes UpdateRemotes.
type RemoteUpdateOptions struct {
	// NewRunner builds the installer for one remote. Nil means NewSSHRunner.
	NewRunner func(name string, rc RemoteConfig) RemoteBinaryInstaller
	// InstallMissing deploys onto remotes with no runnable binary. The
	// explicit CLI does this (it always has); the unattended sweep must not
	// push binaries onto hosts it cannot even version.
	InstallMissing bool
	// Progress receives human-readable step lines ("Platform: linux/amd64").
	// Nil discards them.
	Progress func(string)
	// OnResult is called after each remote finishes, before the next starts.
	OnResult func(RemoteUpdateResult)
	// FetchRelease resolves the release to deploy for a target version. Nil
	// means FetchRemoteUpdateRelease (GitHub).
	FetchRelease func(target string) (*update.Release, error)
	// Download fetches and verifies the binary for a platform. Nil means
	// update.DownloadVerifiedBinary.
	Download func(release *update.Release, goos, goarch string) ([]byte, error)
	// CurrentVersion is what the remote runs now, when known. DeployRemoteBinary
	// refuses to install a release that is not newer than it: the fallback
	// from a missing tag to the latest release must never downgrade a remote
	// (#2164). UpdateRemotes sets it per remote.
	CurrentVersion string
}

func (o RemoteUpdateOptions) withDefaults() RemoteUpdateOptions {
	if o.NewRunner == nil {
		o.NewRunner = func(name string, rc RemoteConfig) RemoteBinaryInstaller { return NewSSHRunner(name, rc) }
	}
	if o.Progress == nil {
		o.Progress = func(string) {}
	}
	if o.OnResult == nil {
		o.OnResult = func(RemoteUpdateResult) {}
	}
	if o.FetchRelease == nil {
		o.FetchRelease = FetchRemoteUpdateRelease
	}
	if o.Download == nil {
		o.Download = update.DownloadVerifiedBinary
	}
	return o
}

// FetchRemoteUpdateRelease resolves the release that matches the controller's
// version so a remote lands on exactly what the controller runs. When that
// tag has no release (a source build ahead of the last tag), the latest
// installable release is used instead and the deploy verifies against it.
func FetchRemoteUpdateRelease(target string) (*update.Release, error) {
	if isReleaseVersion(target) {
		if release, err := update.FetchReleaseByTag(target); err == nil {
			return release, nil
		}
	}
	release, err := update.FetchLatestRelease()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch release info: %w", err)
	}
	return release, nil
}

// DeployRemoteBinary installs the release for targetVersion onto one remote:
// detect platform, download and checksum-verify the archive, deploy, and
// verify the remote actually runs the new version. An unverified artifact is
// never piped to a remote (#1206), and a false "installed" is never reported
// (#1171). Returns the version the remote now runs.
func DeployRemoteBinary(ctx context.Context, runner RemoteBinaryInstaller, targetVersion string, opts RemoteUpdateOptions) (string, error) {
	opts = opts.withDefaults()
	goos, goarch, err := runner.DetectPlatform(ctx)
	if err != nil {
		return "", err
	}
	opts.Progress(fmt.Sprintf("Platform: %s/%s", goos, goarch))

	release, err := opts.FetchRelease(targetVersion)
	if err != nil {
		return "", err
	}
	deployed := strings.TrimPrefix(release.TagName, "v")
	if deployed == "" {
		deployed = strings.TrimPrefix(targetVersion, "v")
	}
	if current := strings.TrimPrefix(opts.CurrentVersion, "v"); isVersionString(current) && update.CompareVersions(deployed, current) <= 0 {
		return "", fmt.Errorf("%w: release v%s is not newer than the remote's v%s", ErrRemoteReleaseNotNewer, deployed, current)
	}

	opts.Progress(fmt.Sprintf("Downloading + verifying %s/%s binary for v%s...", goos, goarch, deployed))
	binaryData, err := opts.Download(release, goos, goarch)
	if err != nil {
		return "", fmt.Errorf("download/verify failed: %w", err)
	}

	opts.Progress("Deploying...")
	if err := runner.InstallBinary(ctx, binaryData, deployed); err != nil {
		return "", fmt.Errorf("deploy failed: %w", err)
	}
	return deployed, nil
}

// ErrRemoteBinaryMissing is the Skipped reason when a sweep finds no
// runnable agent-deck on a remote and InstallMissing is off.
var ErrRemoteBinaryMissing = errors.New("agent-deck not found on remote or host unreachable")

// ErrRemoteVersionUnknown is the Skipped reason when the remote's agent-deck
// reported something that is not a version; nothing is ever deployed onto
// a remote whose version cannot be compared.
var ErrRemoteVersionUnknown = errors.New("remote reported an unrecognised version")

// ErrRemoteReleaseNotNewer is the Skipped reason when the release resolved
// for the controller's version (the tag, or latest when the tag has no
// release) is not newer than what the remote already runs.
var ErrRemoteReleaseNotNewer = errors.New("no newer release to deploy")

// UpdateRemotes checks every remote in remotes and pushes targetVersion to the
// ones that are older, one remote at a time, in name order. It records the
// versions it learns in the shared cache and returns one result per remote.
// A remote that fails stays on its version and is reported; the run
// continues with the next remote.
func UpdateRemotes(ctx context.Context, remotes map[string]RemoteConfig, targetVersion string, opts RemoteUpdateOptions) []RemoteUpdateResult {
	opts = opts.withDefaults()
	target := strings.TrimPrefix(targetVersion, "v")

	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)

	results := make([]RemoteUpdateResult, 0, len(names))
	learned := make(map[string]RemoteVersionState, len(names))
	for _, name := range names {
		rc := remotes[name]
		runner := opts.NewRunner(name, rc)
		result := RemoteUpdateResult{Name: name, Host: rc.Host, To: target}

		version, found := runner.CheckBinary(ctx)
		state := RemoteVersionState{Version: version, Found: found, CheckedAt: time.Now()}
		plan := PlanRemoteUpdates(map[string]RemoteVersionState{name: state}, target)[0]
		result.From = plan.Version

		switch {
		case plan.Kind == RemoteUpdateCurrent:
			result.Outcome = RemoteUpdateOutcomeCurrent
		case plan.Kind == RemoteUpdateUnknown:
			result.Outcome = RemoteUpdateOutcomeSkipped
			result.Err = fmt.Errorf("%w: %q", ErrRemoteVersionUnknown, state.Version)
		case plan.Kind == RemoteUpdateMissing && !opts.InstallMissing:
			result.Outcome = RemoteUpdateOutcomeSkipped
			result.Err = ErrRemoteBinaryMissing
		default:
			remoteOpts := opts
			remoteOpts.CurrentVersion = plan.Version
			deployed, err := DeployRemoteBinary(ctx, runner, target, remoteOpts)
			if errors.Is(err, ErrRemoteReleaseNotNewer) {
				result.Outcome = RemoteUpdateOutcomeSkipped
				result.Err = err
			} else if err != nil {
				result.Outcome = RemoteUpdateOutcomeFailed
				result.Err = err
			} else {
				result.Outcome = RemoteUpdateOutcomeUpdated
				result.To = deployed
				state = RemoteVersionState{Version: deployed, Found: true, CheckedAt: time.Now()}
			}
		}
		learned[name] = state
		results = append(results, result)
		opts.OnResult(result)
	}
	_ = RecordRemoteVersions(learned)
	return results
}
