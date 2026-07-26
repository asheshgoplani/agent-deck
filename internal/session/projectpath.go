package session

import (
	"fmt"
	"path/filepath"
)

// ResolveProjectPath turns a user-supplied project path into the canonical form
// stored in instances.project_path: environment variables and `~` expanded, then
// made absolute.
//
// Related to #1706. project_path is identity (#1731 made it immutable outside
// the declared multi-repo set and this explicit mutation path), and a relative
// value cannot serve as identity:
//
//   - tmux resolves `new-session -c <relative>` against the tmux SERVER's cwd,
//     inherited from whichever client first started it, so the session lands
//     somewhere other than the directory the user meant;
//   - the Claude project slug derived from it points at a different transcript
//     directory depending on who resolves it, which breaks resume;
//   - the #1731 hook-cwd ownership check resolves symlinks on both sides, so a
//     relative stored path can never match a hook-reported cwd and every event
//     is refused.
//
// `agent-deck add` has always resolved its positional path argument this way
// (cmd/agent-deck/cli_utils.go resolveAddPath); this is the same rule for every
// later write. An empty value is returned unchanged — emptiness is the caller's
// validation concern, not this function's.
//
// Callers are the machine-local writers (the CLI acting on its own profile, the
// TUI, the web mutator). Paths that belong to a remote host must not pass
// through here: they are resolved on that host by the CLI running there.
// Whitespace is NOT trimmed: a directory name may legally begin or end with a
// space, and `add` does not trim either. Callers that read a text field trim it
// themselves before calling.
func ResolveProjectPath(path string) (string, error) {
	if path == "" {
		return path, nil
	}
	abs, err := filepath.Abs(ExpandPath(path))
	if err != nil {
		// filepath.Abs only fails when the process has no usable cwd, so there
		// is no anchor for a relative path. Refuse rather than store a value
		// that resolves differently for every reader.
		return "", fmt.Errorf("cannot resolve project path %q: %w", path, err)
	}
	return abs, nil
}
