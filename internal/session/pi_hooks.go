package session

import (
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// piHookExtensionSource is the pi extension agent-deck installs into pi's
// global extension directory. It is the only copy: the file on disk is
// generated from these bytes, never hand-edited (see PiHookExtensionSource).
//
//go:embed assets/pi/agent-deck.ts
var piHookExtensionSource string

const (
	// piHookExtensionFileName is the basename inside pi's extensions dir. pi
	// auto-discovers every *.ts there, so writing the file is the whole
	// install — there is no settings.json entry to merge (unlike Cursor's
	// hooks.json or Codex's config.toml).
	piHookExtensionFileName = "agent-deck.ts"
	// piHookExtensionMarker identifies a file as ours. Uninstall refuses to
	// delete a file without it, so a user's own agent-deck.ts is never lost.
	piHookExtensionMarker = "AGENTDECK PI HOOK EXTENSION"
	// piHookExtensionVersion is bumped whenever the emitted event set or the
	// payload shape changes, so `pi-hooks status` can report drift and
	// `pi-hooks install` can upgrade in place.
	piHookExtensionVersion = 2
)

var piHookExtensionVersionRe = regexp.MustCompile(piHookExtensionMarker + ` v(\d+)`)

// PiHookExtensionSource returns the extension source agent-deck installs.
func PiHookExtensionSource() string { return piHookExtensionSource }

// PiHookExtensionVersion returns the version agent-deck ships.
func PiHookExtensionVersion() int { return piHookExtensionVersion }

// PiExtensionsDir returns pi's global extension directory. pi reads its config
// directory from PI_CODING_AGENT_DIR and defaults to ~/.pi/agent; extensions
// live in the "extensions" subdirectory of it.
func PiExtensionsDir() string {
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
		return filepath.Join(dir, "extensions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".pi", "agent", "extensions")
	}
	return filepath.Join(home, ".pi", "agent", "extensions")
}

// PiHookExtensionPath returns the full path of the installed extension file.
func PiHookExtensionPath(extensionsDir string) string {
	return filepath.Join(extensionsDir, piHookExtensionFileName)
}

// PiHooksState describes what is currently on disk at PiHookExtensionPath.
type PiHooksState int

const (
	// PiHooksNotInstalled: no file at all.
	PiHooksNotInstalled PiHooksState = iota
	// PiHooksInstalled: our file, current version, byte-identical.
	PiHooksInstalled
	// PiHooksOutdated: our file, but an older version or locally modified.
	PiHooksOutdated
	// PiHooksForeign: a file we did not write. Install and uninstall both
	// refuse to touch it.
	PiHooksForeign
)

// InspectPiHooks reports the on-disk state of the pi extension, plus the
// installed version when the file is one of ours.
func InspectPiHooks(extensionsDir string) (PiHooksState, int) {
	data, err := os.ReadFile(PiHookExtensionPath(extensionsDir))
	if err != nil {
		return PiHooksNotInstalled, 0
	}
	content := string(data)
	match := piHookExtensionVersionRe.FindStringSubmatch(content)
	if match == nil {
		return PiHooksForeign, 0
	}
	version, _ := strconv.Atoi(match[1])
	if version == piHookExtensionVersion && content == piHookExtensionSource {
		return PiHooksInstalled, version
	}
	return PiHooksOutdated, version
}

// CheckPiHooksInstalled reports whether the current extension is installed.
func CheckPiHooksInstalled(extensionsDir string) bool {
	state, _ := InspectPiHooks(extensionsDir)
	return state == PiHooksInstalled
}

// InstallPiHooks writes (or upgrades) the agent-deck pi extension. It returns
// false with no error when the current version is already in place, and an
// error when a foreign agent-deck.ts is in the way — clobbering somebody
// else's extension is never the right call, and the caller tells the user to
// move it aside.
func InstallPiHooks(extensionsDir string) (bool, error) {
	state, _ := InspectPiHooks(extensionsDir)
	switch state {
	case PiHooksInstalled:
		return false, nil
	case PiHooksForeign:
		return false, fmt.Errorf("%s exists and was not written by agent-deck; move it aside and retry",
			PiHookExtensionPath(extensionsDir))
	}

	if err := os.MkdirAll(extensionsDir, 0755); err != nil {
		return false, fmt.Errorf("create pi extensions dir: %w", err)
	}
	if err := atomicfile.WriteFile(PiHookExtensionPath(extensionsDir), []byte(piHookExtensionSource), 0644); err != nil {
		return false, fmt.Errorf("write pi extension: %w", err)
	}
	sessionLog.Info("pi_hooks_installed",
		slog.String("extensions_dir", extensionsDir),
		slog.Int("version", piHookExtensionVersion))
	return true, nil
}

// RemovePiHooks deletes the agent-deck pi extension. It returns false with no
// error when there is nothing of ours to remove, and an error for a foreign
// agent-deck.ts (same reasoning as InstallPiHooks).
func RemovePiHooks(extensionsDir string) (bool, error) {
	state, _ := InspectPiHooks(extensionsDir)
	switch state {
	case PiHooksNotInstalled:
		return false, nil
	case PiHooksForeign:
		return false, fmt.Errorf("%s was not written by agent-deck; refusing to remove it",
			PiHookExtensionPath(extensionsDir))
	}

	if err := os.Remove(PiHookExtensionPath(extensionsDir)); err != nil {
		return false, fmt.Errorf("remove pi extension: %w", err)
	}
	sessionLog.Info("pi_hooks_removed", slog.String("extensions_dir", extensionsDir))
	return true, nil
}
