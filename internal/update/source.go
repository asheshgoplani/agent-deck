package update

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// sourceGitCmd runs a git command in the configured source directory.
func sourceGitCmd(sourceDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = sourceDir
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// splitRef splits "origin/main" into ("origin", "main").
func splitRef(ref string) (remote, branch string) {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, "main"
}

// checkForSourceUpdate compares local HEAD vs remote HEAD in a local git checkout.
func checkForSourceUpdate(currentVersion string, forceCheck bool, settings session.UpdateSettings) (*UpdateInfo, error) {
	info := &UpdateInfo{
		Available:      false,
		CurrentVersion: currentVersion,
	}

	// Validate source directory is a git repo
	if _, err := os.Stat(filepath.Join(settings.SourceDir, ".git")); err != nil {
		return info, fmt.Errorf("source_dir %q is not a git repository", settings.SourceDir)
	}

	// Try to use cache first (unless force check)
	if !forceCheck {
		cache, err := loadCache()
		if err == nil && time.Since(cache.CheckedAt) < checkInterval {
			info.LatestVersion = cache.LatestVersion
			info.Available = cache.LatestVersion != cache.CurrentVersion
			return info, nil
		}
	}

	remote, branch := splitRef(settings.SourceRef)

	// Fetch latest from remote
	if _, err := sourceGitCmd(settings.SourceDir, "fetch", remote, branch); err != nil {
		return info, fmt.Errorf("git fetch failed: %w", err)
	}

	// Extract version from remote main.go
	remoteVersion := currentVersion
	remoteFileContent, err := sourceGitCmd(settings.SourceDir, "show", settings.SourceRef+":cmd/agent-deck/main.go")
	if err == nil {
		for _, line := range strings.Split(remoteFileContent, "\n") {
			if strings.Contains(line, "const Version") {
				parts := strings.Split(line, `"`)
				if len(parts) >= 2 {
					remoteVersion = parts[1]
				}
				break
			}
		}
	}

	// Only offer update if HEAD is strictly behind the remote (no local-only commits).
	// This avoids prompting when on a feature branch that has diverged.
	behindCount, _ := sourceGitCmd(settings.SourceDir, "rev-list", "--count", "HEAD.."+settings.SourceRef)
	aheadCount, _ := sourceGitCmd(settings.SourceDir, "rev-list", "--count", settings.SourceRef+"..HEAD")
	behind := behindCount != "" && behindCount != "0"
	ahead := aheadCount != "" && aheadCount != "0"
	available := behind && !ahead

	cache := &UpdateCache{
		CheckedAt:      time.Now(),
		LatestVersion:  remoteVersion,
		CurrentVersion: currentVersion,
	}
	_ = saveCache(cache)

	info.LatestVersion = remoteVersion
	if available {
		info.ReleaseURL = fmt.Sprintf("%s new commit(s) on %s", behindCount, settings.SourceRef)
	}
	info.Available = available

	return info, nil
}

// performSourceUpdate pulls the latest source and builds from a local git checkout.
func performSourceUpdate(settings session.UpdateSettings) error {
	if _, err := os.Stat(filepath.Join(settings.SourceDir, ".git")); err != nil {
		return fmt.Errorf("source_dir %q is not a git repository", settings.SourceDir)
	}

	remote, branch := splitRef(settings.SourceRef)

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to find current executable: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("failed to resolve executable path: %w", err)
	}

	// Pull latest from remote
	fmt.Printf("Pulling latest from %s...\n", settings.SourceRef)
	pullOut, err := sourceGitCmd(settings.SourceDir, "pull", remote, branch)
	if err != nil {
		return fmt.Errorf("git pull failed: %w", err)
	}
	fmt.Println(pullOut)

	// Build directly to the install path.
	// On macOS, go build applies an ad-hoc code signature. Copying the binary
	// afterward (ReadFile+WriteFile or cp) creates a new file whose signature
	// doesn't match, causing the kernel to SIGKILL the process on launch.
	// Building in-place avoids this entirely.
	fmt.Println("Building from source...")

	version, _ := sourceGitCmd(settings.SourceDir, "describe", "--tags", "--always", "--dirty")
	commit, _ := sourceGitCmd(settings.SourceDir, "rev-parse", "--short", "HEAD")
	ldflags := fmt.Sprintf("-X main.Version=%s -X main.Commit=%s", version, commit)

	buildCmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", execPath, "./cmd/agent-deck/")
	buildCmd.Dir = settings.SourceDir
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	// Re-sign on macOS to ensure a valid ad-hoc signature
	if codesign, err := exec.LookPath("codesign"); err == nil {
		signCmd := exec.Command(codesign, "--force", "--sign", "-", execPath)
		signCmd.Stdout = os.Stdout
		signCmd.Stderr = os.Stderr
		_ = signCmd.Run()
	}

	fmt.Println("Update complete (built from source)!")
	return nil
}
