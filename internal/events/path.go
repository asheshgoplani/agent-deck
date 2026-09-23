package events

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// busDirName is the shared XDG/legacy data root marker. Each validated
// profile gets a child directory under it. See docs/events.md.
const busDirName = "bus"

var selectedProfile atomic.Value

// SetProfile selects the CLI/TUI process profile before its first publish.
func SetProfile(profile string) { selectedProfile.Store(profile) }

// CurrentProfile is the profile selected for this process and its event writers.
func CurrentProfile() string {
	if selected := selectedProfile.Load(); selected != nil {
		return selected.(string)
	}
	if profile := os.Getenv("AGENTDECK_PROFILE"); profile != "" {
		return profile
	}
	return "default"
}

// busDir returns "<data-dir>/bus/<profile>" without creating directories.
func busDir() (string, error) {
	return busDirFor(CurrentProfile())
}

func busDirFor(profile string) (string, error) {
	if profile == "" {
		profile = "default"
	}
	if !filepath.IsLocal(profile) || filepath.Base(profile) != profile {
		return "", fmt.Errorf("events: invalid profile %q", profile)
	}
	root, err := agentpaths.EffectiveDataPath(busDirName, busDirName)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, profile), nil
}
