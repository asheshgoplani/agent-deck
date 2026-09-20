package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/procowner"
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
	Version       string    `json:"version,omitempty"`
	InstalledFrom string    `json:"installed_from,omitempty"`
	Found         bool      `json:"found"`
	CheckedAt     time.Time `json:"checked_at"`
	// StatsSupported records whether this remote's `list --json` accepts
	// --stats, learned by SSHRunner.FetchSessions the first time it tries
	// the flag against this exact Version (#2333: v1.16.13 and earlier
	// reject it outright). nil means "not yet probed against this
	// version" — a poll still sends --stats optimistically and records the
	// answer. Keyed to Version (see RecordRemoteVersions) so an upgrade
	// forces one fresh probe instead of inheriting a stale verdict.
	StatsSupported *bool `json:"stats_supported,omitempty"`
}

// Outdated reports whether the remote runs something older than controller.
// Unknown versions and non-release controller builds never count as drift:
// CompareVersions treats "dev" or "0.0.0" as older than any release, so a
// developer build must not flag every remote. A pre-release controller
// ("1.16.4-preview.abc") is older than release 1.16.4, so a remote on that
// release is not flagged either (#2164).
//
// Outdated must agree with Compare, i.e. it is exactly
// Compare(controller) == RemoteVersionOlder. A local build at an identical
// version number is a separate "redeploy the release binary" signal
// (localBuildNeedsRelease, still used by PlanRemoteUpdates and the deploy's
// ReplaceLocal option), not "older": ORing it in here made `remote list
// --check --json` emit a self-contradictory
// {"outdated":true,"version_state":"same"}.
func (s RemoteVersionState) Outdated(controller string) bool {
	if !s.Found || !isVersionString(s.Version) || !isReleaseVersion(controller) {
		return false
	}
	return update.CompareVersions(s.Version, controller) < 0
}

// RemoteVersionCompare is how a remote's reported version compares with this
// controller's, per update.CompareVersions. Build metadata after "+" is
// stripped before comparing (splitPreRelease), so a "+local" build compares
// equal to its base version: RemoteVersionSame, not RemoteVersionNewer.
type RemoteVersionCompare int

const (
	// RemoteVersionUnknown: the remote never answered, or reported something
	// CompareVersions cannot order. A first-class state, never a guess.
	RemoteVersionUnknown RemoteVersionCompare = iota
	RemoteVersionSame
	RemoteVersionOlder
	RemoteVersionNewer
)

// String renders the compare result the way `remote list --json` and the
// remote preview panel spell it (version_state).
func (c RemoteVersionCompare) String() string {
	switch c {
	case RemoteVersionSame:
		return "same"
	case RemoteVersionOlder:
		return "older"
	case RemoteVersionNewer:
		return "newer"
	default:
		return "unknown"
	}
}

// Compare reports how s compares with the controller's version. Unlike
// Outdated (which never flags drift against a non-release controller build,
// so a developer's "dev"/"0.0.0" build does not report every remote as
// outdated), Compare answers the plain question the preview panel and
// `remote list --json` ask: same, older, newer, or unknown when either side
// cannot be parsed as a version.
func (s RemoteVersionState) Compare(controller string) RemoteVersionCompare {
	if !s.Found || !isVersionString(s.Version) || !isVersionString(controller) {
		return RemoteVersionUnknown
	}
	switch update.CompareVersions(s.Version, controller) {
	case 0:
		return RemoteVersionSame
	case -1:
		return RemoteVersionOlder
	default:
		return RemoteVersionNewer
	}
}

// BuildDiffers reports whether s and controller describe the same release
// (RemoteVersionSame) but differ in their raw version string: build metadata
// after "+", or its presence on only one side. CompareVersions ignores build
// metadata entirely (so the update decision is unaffected), but a label that
// only says "same" would overclaim when the two builds are not, in fact,
// identical (walk defect #1).
func (s RemoteVersionState) BuildDiffers(controller string) bool {
	if s.Compare(controller) != RemoteVersionSame {
		return false
	}
	return strings.TrimPrefix(strings.TrimSpace(s.Version), "v") != strings.TrimPrefix(strings.TrimSpace(controller), "v")
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
var versionStringRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:[.\-][0-9A-Za-z.\-]+)?(?:\+[0-9A-Za-z.\-]+)?$`)

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
	Polls   map[string]RemotePollState    `json:"polls,omitempty"`
	Remotes map[string]RemoteVersionState `json:"remotes"`
	// AutoUpdateRanAt throttles the startup auto-update sweep;
	// AutoUpdateRanVersion is the controller version that sweep pushed, so
	// a controller that restarted into a newer release sweeps again at
	// once instead of waiting out the interval (2026-09-19: the sweep the
	// install should have run died with its updater, and the stamp from
	// the morning kept every later start from catching up).
	AutoUpdateRanAt      time.Time `json:"auto_update_ran_at,omitempty"`
	AutoUpdateRanVersion string    `json:"auto_update_ran_version,omitempty"`
	// Sweep marks a sweep this controller is running right now, so a
	// `remote update --all` started meanwhile waits for it instead of
	// racing it to the remotes' deploy locks (#2244).
	Sweep *remoteSweepMarker `json:"sweep,omitempty"`
}

// remoteSweepMarker is the on-disk shape of RemoteSweep.
type remoteSweepMarker struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Remotes   []string  `json:"remotes"`
}

// RemoteSweep describes a sweep in progress on this controller.
type RemoteSweep struct {
	PID       int
	StartedAt time.Time
	Remotes   []string
}

// Covers reports whether the sweep is updating the named remote.
func (s RemoteSweep) Covers(name string) bool {
	for _, r := range s.Remotes {
		if r == name {
			return true
		}
	}
	return false
}

// ErrRemoteSweepRunning is returned by BeginRemoteSweep while another sweep
// from this controller is still running.
var ErrRemoteSweepRunning = errors.New("a remote sweep is already in progress on this controller")

// remoteSweepStale bounds how long a marker whose process is still alive is
// trusted: a sweep hung on SSH for this long is not one to wait for.
const remoteSweepStale = 30 * time.Minute

// BeginRemoteSweep records that this process is sweeping the named remotes
// and returns the function that clears the marker. A live marker from
// another process yields ErrRemoteSweepRunning; a marker whose process is
// gone or which is older than remoteSweepStale is replaced.
func BeginRemoteSweep(remotes []string) (end func(), err error) {
	var running bool
	err = updateRemoteVersionCache(func(cache *remoteVersionCache) {
		if _, ok := liveSweep(cache.Sweep); ok {
			running = true
			return
		}
		names := append([]string(nil), remotes...)
		sort.Strings(names)
		cache.Sweep = &remoteSweepMarker{PID: os.Getpid(), StartedAt: time.Now(), Remotes: names}
	})
	if err != nil {
		return nil, err
	}
	if running {
		return nil, ErrRemoteSweepRunning
	}
	return func() {
		_ = updateRemoteVersionCache(func(cache *remoteVersionCache) {
			if cache.Sweep != nil && cache.Sweep.PID == os.Getpid() {
				cache.Sweep = nil
			}
		})
	}, nil
}

// RemoteSweepInProgress reports the sweep this controller is running, if
// its process is still alive and it started recently.
func RemoteSweepInProgress() (RemoteSweep, bool) {
	remoteVersionCacheMu.Lock()
	defer remoteVersionCacheMu.Unlock()
	return liveSweep(loadRemoteVersionCache().Sweep)
}

func liveSweep(m *remoteSweepMarker) (RemoteSweep, bool) {
	if m == nil || m.PID <= 0 || time.Since(m.StartedAt) > remoteSweepStale || !sweepProcessAlive(m.PID) {
		return RemoteSweep{}, false
	}
	return RemoteSweep{PID: m.PID, StartedAt: m.StartedAt, Remotes: append([]string(nil), m.Remotes...)}, true
}

// sweepProcessAlive reports whether the marker's process can still finish
// the sweep. It is procowner.Alive, not a bare kill(pid, 0): a sweep child
// that died under its re-exec'd parent stays a zombie until something waits
// for it, and a zombie answers the signal (v1.16.11 rollout: four remotes
// were "being updated by" a defunct pid). A seam so tests can script the
// state.
var sweepProcessAlive = procowner.Alive

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

// saveRemoteVersionCache writes the cache through a temp file and a rename
// so a reader never sees a truncated file and two writers never interleave
// bytes; the last complete write wins.
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
	tmp, err := os.CreateTemp(filepath.Dir(path), remoteVersionCacheFile+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// remoteVersionCacheLockStale bounds how long a lock file left behind by a
// crashed process blocks the next writer.
const remoteVersionCacheLockStale = time.Minute

// remoteVersionCacheLockWait is how long a writer waits for another
// process to release the lock before giving up; writes are small, so a
// holder is gone within milliseconds.
const remoteVersionCacheLockWait = 2 * time.Second

// ErrRemoteVersionCacheBusy is returned when another process held the
// cache lock for longer than remoteVersionCacheLockWait.
var ErrRemoteVersionCacheBusy = errors.New("remote version cache is locked by another process")

// withRemoteVersionCacheLock runs fn while holding the cross-process lock
// file next to the cache (O_EXCL create, removed afterwards), waiting up to
// remoteVersionCacheLockWait for a holder to finish. Every read-modify-write
// of the cache goes through it, so a process that read the cache before
// another stamped it can never replace that stamp with its stale copy. A
// lock older than remoteVersionCacheLockStale is treated as abandoned.
// Callers hold remoteVersionCacheMu.
func withRemoteVersionCacheLock(fn func()) error {
	path, err := remoteVersionCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lockPath := path + ".claim"
	deadline := time.Now().Add(remoteVersionCacheLockWait)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			defer os.Remove(lockPath)
			fn()
			return nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) >= remoteVersionCacheLockStale {
			_ = os.Remove(lockPath) // abandoned by a crashed holder
			continue
		}
		if time.Now().After(deadline) {
			return ErrRemoteVersionCacheBusy
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// updateRemoteVersionCache is the one read-modify-write of the cache file:
// load, let fn change it, save, all under both locks.
func updateRemoteVersionCache(fn func(*remoteVersionCache)) error {
	remoteVersionCacheMu.Lock()
	defer remoteVersionCacheMu.Unlock()
	var saveErr error
	err := withRemoteVersionCacheLock(func() {
		cache := loadRemoteVersionCache()
		if cache.Remotes == nil {
			cache.Remotes = map[string]RemoteVersionState{}
		}
		fn(&cache)
		saveErr = saveRemoteVersionCache(cache)
	})
	if err != nil {
		return err
	}
	return saveErr
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
	return updateRemoteVersionCache(func(cache *remoteVersionCache) {
		for name, state := range states {
			previous := cache.Remotes[name]
			if state.InstalledFrom == "" && state.Found && strings.Contains(state.Version, "+local.") {
				state.InstalledFrom = "local-build"
			}
			if state.InstalledFrom == "" && previous.Version == state.Version {
				state.InstalledFrom = previous.InstalledFrom
			}
			if state.StatsSupported == nil && previous.Version == state.Version {
				state.StatsSupported = previous.StatsSupported
			}
			cache.Remotes[name] = state
		}
	})
}

// RecordRemoteStatsSupport remembers whether name's `list --json` accepts
// --stats, keyed to the exact remote Version this was learned against
// (#2333): a version bump between now and the next probe means a stale
// verdict for the old binary must not survive the upgrade. If the remote's
// cached version has moved on since this probe started (a concurrent
// version check landed first), the answer is dropped rather than pinned to
// the wrong version — the next poll simply probes again.
func RecordRemoteStatsSupport(name, version string, supported bool) error {
	return updateRemoteVersionCache(func(cache *remoteVersionCache) {
		state, ok := cache.Remotes[name]
		if !ok {
			state = RemoteVersionState{Version: version, CheckedAt: time.Now()}
		}
		if state.Version != version {
			return
		}
		state.StatsSupported = &supported
		cache.Remotes[name] = state
	})
}

// RemoteAutoUpdateRanAt returns when the background remote auto-update sweep
// last ran (zero when never).
func RemoteAutoUpdateRanAt() time.Time {
	remoteVersionCacheMu.Lock()
	defer remoteVersionCacheMu.Unlock()
	return loadRemoteVersionCache().AutoUpdateRanAt
}

// MarkRemoteAutoUpdateRan stamps the sweep time and the controller version
// it pushed, both read by ShouldAutoUpdateRemotes.
func MarkRemoteAutoUpdateRan(at time.Time, version string) error {
	return updateRemoteVersionCache(func(cache *remoteVersionCache) {
		cache.AutoUpdateRanAt = at
		cache.AutoUpdateRanVersion = version
	})
}

// RemoteAutoUpdateRanVersion returns the controller version the last sweep
// pushed ("" when never, or stamped by a build before this field).
func RemoteAutoUpdateRanVersion() string {
	remoteVersionCacheMu.Lock()
	defer remoteVersionCacheMu.Unlock()
	return loadRemoteVersionCache().AutoUpdateRanVersion
}

// ClaimRemoteAutoUpdateRun is the startup sweep's check-and-stamp in one
// step: under the cache locks (see updateRemoteVersionCache) it re-reads
// the stamp, applies ShouldAutoUpdateRemotes, and writes the new
// stamp before returning true. Two TUIs starting at once therefore agree on
// a single sweep, and a stamp that cannot be written yields false, so a
// broken cache dir never causes a sweep on every startup (#2164).
func ClaimRemoteAutoUpdateRun(settings UpdateSettings, remoteCount int, version string, now time.Time) bool {
	claimed := false
	err := updateRemoteVersionCache(func(cache *remoteVersionCache) {
		if ShouldAutoUpdateRemotes(settings, remoteCount, cache.AutoUpdateRanAt, cache.AutoUpdateRanVersion, version, now) {
			cache.AutoUpdateRanAt = now
			cache.AutoUpdateRanVersion = version
			claimed = true
		}
	})
	return claimed && err == nil
}

// ShouldAutoUpdateRemotes is the pure decision behind the startup sweep:
// the key must not be off (it is on by default), there must be remotes, and
// the previous sweep must be older than the update check interval (so a TUI
// restarted ten times in a row does not SSH into every remote ten times), or
// have pushed a different controller version than this one (a controller
// that just restarted into a new release sweeps at once, even when the
// sweep its install should have run never happened). A zero lastRun always
// runs; an empty lastVersion (older stamp) defers to the interval alone.
func ShouldAutoUpdateRemotes(settings UpdateSettings, remoteCount int, lastRun time.Time, lastVersion, version string, now time.Time) bool {
	if !settings.GetAutoUpdateRemotes() || remoteCount == 0 {
		return false
	}
	if lastRun.IsZero() {
		return true
	}
	if lastVersion != "" && version != "" && update.CompareVersions(lastVersion, version) != 0 {
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
		case update.CompareVersions(state.Version, controller) < 0 || localBuildNeedsRelease(state, controller):
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
	// Note is where the deploy put the binary (the installer's report), or
	// why a remote was left to another run; appended to the report line.
	Note string
}

// String renders the one-line report used by the CLI and the log.
func (r RemoteUpdateResult) String() string {
	line := ""
	switch r.Outcome {
	case RemoteUpdateOutcomeUpdated:
		if r.From == "" {
			line = fmt.Sprintf("%s: installed v%s", r.Name, r.To)
		} else {
			line = fmt.Sprintf("%s: updated v%s -> v%s", r.Name, r.From, r.To)
		}
	case RemoteUpdateOutcomeCurrent:
		line = fmt.Sprintf("%s: already current (v%s)", r.Name, r.From)
	case RemoteUpdateOutcomeSkipped:
		if r.Err == nil {
			line = fmt.Sprintf("%s: skipped", r.Name)
		} else {
			line = fmt.Sprintf("%s: skipped (%v)", r.Name, r.Err)
		}
	default:
		line = fmt.Sprintf("%s: failed (%v)", r.Name, r.Err)
	}
	if r.Note != "" {
		line += "; " + r.Note
	}
	return line
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

// installReporter is the optional part of an installer that can say where
// it put the binary; SSHRunner implements it.
type installReporter interface {
	LastInstallReport() string
}

// RemoteUpdateOptions tunes UpdateRemotes.
type RemoteUpdateOptions struct {
	LocalBuild *LocalBuild
	Force      bool
	DryRun     bool
	// ReplaceLocal permits replacing a local build with its equal-core release.
	ReplaceLocal bool
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
	if opts.LocalBuild != nil {
		return deployLocalBuild(ctx, runner, opts)
	}
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
	current := strings.TrimPrefix(opts.CurrentVersion, "v")
	precedence := update.CompareVersions(deployed, current)
	if isVersionString(current) && !opts.Force && (precedence < 0 || (precedence == 0 && !opts.ReplaceLocal)) {
		return "", fmt.Errorf("%w: release v%s is not newer than the remote's v%s", ErrRemoteReleaseNotNewer, deployed, current)
	}

	opts.Progress(fmt.Sprintf("Downloading + verifying %s/%s binary for v%s...", goos, goarch, deployed))
	binaryData, err := opts.Download(release, goos, goarch)
	if err != nil {
		return "", fmt.Errorf("download/verify failed: %w", err)
	}

	if opts.DryRun {
		preview, ok := runner.(installPreviewer)
		if !ok {
			return "", fmt.Errorf("SSH runner does not support install preview")
		}
		if err := preview.PreviewInstallWithForce(ctx, deployed, opts.Force); err != nil {
			return "", err
		}
		return deployed, nil
	}
	opts.Progress("Deploying...")
	var installErr error
	if installer, ok := runner.(interface {
		InstallBinaryWithForce(context.Context, []byte, string, bool) error
	}); ok {
		installErr = installer.InstallBinaryWithForce(ctx, binaryData, deployed, opts.Force)
	} else {
		installErr = runner.InstallBinary(ctx, binaryData, deployed)
	}
	if installErr != nil {
		return "", fmt.Errorf("deploy failed: %w", installErr)
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
	if opts.LocalBuild != nil {
		target = opts.LocalBuild.Version
	}
	cached := LoadRemoteVersions()

	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)

	results := make([]RemoteUpdateResult, 0, len(names))
	for _, name := range names {
		rc := remotes[name]
		runner := opts.NewRunner(name, rc)
		result := RemoteUpdateResult{Name: name, Host: rc.Host, To: target}

		version, found := runner.CheckBinary(ctx)
		state := RemoteVersionState{Version: version, Found: found, CheckedAt: time.Now()}
		if cached[name].Version == version {
			state.InstalledFrom = cached[name].InstalledFrom
		}
		plan := PlanRemoteUpdates(map[string]RemoteVersionState{name: state}, target)[0]
		result.From = plan.Version

		switch {
		case plan.Kind == RemoteUpdateCurrent && opts.LocalBuild == nil && !opts.Force:
			result.Outcome = RemoteUpdateOutcomeCurrent
			if state.BuildDiffers(target) {
				result.Note = "same release, different build"
			}
		case plan.Kind == RemoteUpdateUnknown:
			result.Outcome = RemoteUpdateOutcomeSkipped
			result.Err = fmt.Errorf("%w: %q", ErrRemoteVersionUnknown, state.Version)
		case plan.Kind == RemoteUpdateMissing && !opts.InstallMissing:
			result.Outcome = RemoteUpdateOutcomeSkipped
			result.Err = ErrRemoteBinaryMissing
		default:
			remoteOpts := opts
			remoteOpts.CurrentVersion = plan.Version
			remoteOpts.ReplaceLocal = localBuildNeedsRelease(state, target)
			deployed, err := DeployRemoteBinary(ctx, runner, target, remoteOpts)
			if reporter, ok := runner.(installReporter); ok {
				result.Note = reporter.LastInstallReport()
			}
			switch {
			case errors.Is(err, ErrRemoteReleaseNotNewer), errors.Is(err, ErrRemoteDeployBusy), errors.Is(err, ErrRemoteProbeFailed):
				// Nothing wrong with this remote: another deploy holds it,
				// there is nothing newer to give it, or it could not say
				// what it runs and was left alone (#2244).
				result.Outcome = RemoteUpdateOutcomeSkipped
				result.Err = err
			case err != nil:
				result.Outcome = RemoteUpdateOutcomeFailed
				result.Err = err
			default:
				result.To = deployed
				if opts.DryRun {
					result.Outcome = RemoteUpdateOutcomeSkipped
					result.Note = "dry run: would install v" + deployed + "; " + result.Note
				} else {
					result.Outcome = RemoteUpdateOutcomeUpdated
					source := "release"
					if opts.LocalBuild != nil {
						source = "local-build"
					}
					state = RemoteVersionState{Version: deployed, Found: true, CheckedAt: time.Now(), InstalledFrom: source}
				}
			}
		}
		// Record what this remote runs now, before the next remote and
		// before the caller hears about it, so `remote list` never shows a
		// version a finished deploy has already replaced (#2244).
		if !opts.DryRun {
			if err := RecordRemoteVersions(map[string]RemoteVersionState{name: state}); err != nil {
				result.Note += "; could not record remote version cache: " + err.Error()
			}
		}
		results = append(results, result)
		opts.OnResult(result)
	}
	return results
}
