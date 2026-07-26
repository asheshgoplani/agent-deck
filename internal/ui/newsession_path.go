package ui

import (
	"fmt"
	"path/filepath"
)

// absLocalProjectPath resolves a user-entered LOCAL project path to an
// absolute, cleaned path.
//
// #1706: the New Session dialog accepts whatever the user types and, until
// this normalization, handed it to session creation verbatim. A relative entry
// ("dev/app", "app", ".", "../app") then meant two different directories at
// once:
//
//   - the TUI resolved it against its OWN process cwd for the
//     "Directory Not Found" check and the os.MkdirAll that follows, so the
//     folder was created next to wherever agent-deck happened to be started;
//   - tmux resolved `new-session -c <relative>` against the tmux SERVER's cwd,
//     which is inherited from whichever client first started that server and is
//     usually somewhere else entirely (and may no longer exist).
//
// So the session did not land in the directory the user named, and a folder
// appeared in a third, unrelated place. A relative path stored as
// instances.project_path also breaks everything that treats it as identity:
// group derivation, the Claude project slug, and the #1731 hook-cwd ownership
// comparison (which resolves symlinks on both sides and can never match a
// relative string).
//
// The CLI has always absolutized here (cmd/agent-deck/cli_utils.go
// resolveProjectPathArg); this brings the TUI in line. Remote paths must NEVER
// pass through this function — they belong to the remote host's filesystem and
// travel via NewDialog.GetRemoteValues instead.
//
// An empty path is returned unchanged: multi-repo mode legitimately submits an
// empty single-path field, and the emptiness check belongs to Validate.
// Otherwise the returned path is guaranteed absolute, or an error is returned
// and the caller must refuse the submission — falling back to the relative
// string would reintroduce exactly the split-directory bug above.
func absLocalProjectPath(path string) (string, error) {
	if path == "" {
		return path, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		// filepath.Abs only fails when the process cwd is unavailable (e.g. the
		// directory agent-deck was started in has been deleted), so there is no
		// anchor to resolve a relative path against.
		return "", fmt.Errorf("resolve %q: %w", path, err)
	}
	return abs, nil
}
