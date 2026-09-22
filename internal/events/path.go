package events

import "github.com/asheshgoplani/agent-deck/internal/agentpaths"

// busDirName is the marker/subdirectory name for the bus's data, resolved
// through agentpaths so it follows the same XDG/legacy-dir rules (and the
// same per-profile isolation via XDG_DATA_HOME) as every other agent-deck
// data path. Documented in docs/events.md.
const busDirName = "bus"

// busDir returns "<profile-data-dir>/bus", creating no directories itself.
func busDir() (string, error) {
	return agentpaths.EffectiveDataPath(busDirName, busDirName)
}
