package events

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// busDirName is the marker/subdirectory name for the bus's data, resolved
// through agentpaths so it follows the same XDG/legacy-dir rules (and the
// same per-profile isolation via XDG_DATA_HOME) as every other agent-deck
// data path. Documented in docs/events.md.
const busDirName = "bus"

var selectedProfile atomic.Value

// SetProfile selects the CLI/TUI process profile before its first publish.
func SetProfile(profile string) { selectedProfile.Store(profile) }

// busDir returns "<profile-data-dir>/bus", creating no directories itself.
func busDir() (string, error) {
	if selected := selectedProfile.Load(); selected != nil {
		return busDirFor(selected.(string))
	}
	return busDirFor(os.Getenv("AGENTDECK_PROFILE"))
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
