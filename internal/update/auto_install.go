package update

import (
	"os"
	"runtime"
	"strings"
)

const UpdatedSentinelEnv = "AGENTDECK_UPDATED"

func ReexecLoopGuard(version string) bool {
	return strings.TrimPrefix(os.Getenv(UpdatedSentinelEnv), "v") == strings.TrimPrefix(version, "v") && version != ""
}

var (
	autoCheckRelease   = func(current string) (*UpdateInfo, error) { return CheckForUpdate(current, false) }
	autoFetchRelease   = FetchReleaseByTag
	autoInstallRelease = func(release *Release) error { return PerformVerifiedUpdate(release, runtime.GOOS, runtime.GOARCH) }
)

// AutoInstallAvailable is deliberately orchestration only: it reuses the
// existing check and verified `agent-deck update` installer rather than owning
// a downloader or swap implementation of its own.
func AutoInstallAvailable(current string) (*UpdateInfo, error) {
	info, err := autoCheckRelease(current)
	if err != nil || info == nil || !info.Available {
		return info, err
	}
	SaveAutoState("installing", info.LatestVersion, "verified download and install in progress")
	release, err := autoFetchRelease(info.LatestVersion)
	if err != nil {
		SaveAutoState("failed", info.LatestVersion, err.Error())
		return info, err
	}
	if err := autoInstallRelease(release); err != nil {
		SaveAutoState("failed", info.LatestVersion, err.Error())
		return info, err
	}
	SaveAutoState("installed", info.LatestVersion, "waiting for safe in-place TUI re-exec")
	return info, nil
}
