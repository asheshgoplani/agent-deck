package session

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// Nudging remotes instead of pushing bytes.
//
// After an unattended install the controller used to SSH the new binary
// onto every configured remote (UpdateRemotes/DeployRemoteBinary). That
// push model does not scale to a near-event-driven world where a release
// can land at any moment: NudgeRemotes instead tells each remote "check
// now" over the same SSH connection it already uses, and the remote pulls
// and verifies the release itself, exactly the way `agent-deck update`
// always has. The nudge is fire-and-forget (SSHRunner.NudgeCheckNow
// backgrounds the remote command and returns as soon as it is launched) and
// never transfers the release bytes itself.
//
// A remote whose last known version predates NudgeMinVersion has no
// `--check-now` flag to receive the nudge with, so it gets the
// compatibility fallback instead: a blocking `agent-deck update
// --unattended` that downloads and verifies on the remote, same as before
// this feature existed. Either way, the bytes are fetched BY the remote.

// NudgeMinVersion is the first agent-deck release that understands
// `agent-deck update --check-now` as a nudge target. A remote whose cached
// version is older than this (or was never observed) gets the
// compatibility fallback.
const NudgeMinVersion = "1.16.17"

// RemoteNudger is the slice of SSHRunner NudgeRemotes needs. Tests
// substitute a stub; production passes NewSSHRunner.
type RemoteNudger interface {
	NudgeCheckNow(ctx context.Context) error
	FallbackUpdate(ctx context.Context) ([]byte, error)
}

// NudgeOutcome is how one remote's nudge went.
type NudgeOutcome int

const (
	// NudgeOutcomeSent: the remote understood --check-now; it checks (and,
	// if a release is available, installs) on its own schedule.
	NudgeOutcomeSent NudgeOutcome = iota
	// NudgeOutcomeFallback: the remote's version predates --check-now, so
	// the compatibility path (`agent-deck update --unattended`) ran
	// instead, blocking until the remote finished checking/installing.
	NudgeOutcomeFallback
	// NudgeOutcomeFailed: the nudge itself could not be sent (host
	// unreachable, ssh failed to even launch the backgrounded command).
	NudgeOutcomeFailed
)

// NudgeResult is one remote's outcome from a NudgeRemotes pass.
type NudgeResult struct {
	Name    string
	Outcome NudgeOutcome
	Err     error
}

// String renders the one-line report used by the CLI and the log.
func (r NudgeResult) String() string {
	switch r.Outcome {
	case NudgeOutcomeSent:
		return fmt.Sprintf("%s: nudged (check now)", r.Name)
	case NudgeOutcomeFallback:
		if r.Err != nil {
			return fmt.Sprintf("%s: fallback pull failed: %v", r.Name, r.Err)
		}
		return fmt.Sprintf("%s: fallback pull (does not understand check-now)", r.Name)
	default:
		return fmt.Sprintf("%s: nudge failed: %v", r.Name, r.Err)
	}
}

// NudgeRemoteOptions tunes NudgeRemotes.
type NudgeRemoteOptions struct {
	// NewRunner builds the nudger for one remote. Nil means NewSSHRunner.
	NewRunner func(name string, rc RemoteConfig) RemoteNudger
	// Versions is the cached per-remote version state NudgeRemotes uses to
	// decide nudge vs. fallback. Nil means LoadRemoteVersions.
	Versions map[string]RemoteVersionState
}

func (o NudgeRemoteOptions) withDefaults() NudgeRemoteOptions {
	if o.NewRunner == nil {
		o.NewRunner = func(name string, rc RemoteConfig) RemoteNudger { return NewSSHRunner(name, rc) }
	}
	if o.Versions == nil {
		o.Versions = LoadRemoteVersions()
	}
	return o
}

// NudgeRemotes tells every remote in remotes to check for an update now,
// one at a time in name order. It never transfers release bytes itself: a
// remote that understands the nudge pulls and verifies on its own; an older
// remote gets the blocking compatibility fallback. Every outcome is
// best-effort — a remote that cannot be reached is reported, never fatal to
// the caller or to the remaining remotes.
func NudgeRemotes(ctx context.Context, remotes map[string]RemoteConfig, log *slog.Logger, opts NudgeRemoteOptions) []NudgeResult {
	opts = opts.withDefaults()

	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)

	results := make([]NudgeResult, 0, len(names))
	for _, name := range names {
		runner := opts.NewRunner(name, remotes[name])
		result := NudgeResult{Name: name}
		if SupportsNudge(opts.Versions[name]) {
			if err := runner.NudgeCheckNow(ctx); err != nil {
				result.Outcome = NudgeOutcomeFailed
				result.Err = err
			}
		} else {
			result.Outcome = NudgeOutcomeFallback
			if _, err := runner.FallbackUpdate(ctx); err != nil {
				result.Err = err
			}
		}
		if log != nil {
			log.Info("remote_nudge", slog.String("remote", name), slog.String("outcome", result.String()))
		}
		results = append(results, result)
	}
	return results
}

// SupportsNudge reports whether a remote's last known version understands
// `agent-deck update --check-now`. An unknown or unparsable version is
// treated as unsupported, so a remote never seen before gets the safe
// (if slower) compatibility fallback rather than a nudge it might ignore.
func SupportsNudge(state RemoteVersionState) bool {
	if !state.Found || !isVersionString(state.Version) {
		return false
	}
	return update.CompareVersions(state.Version, NudgeMinVersion) >= 0
}
