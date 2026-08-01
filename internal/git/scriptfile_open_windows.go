//go:build windows

package git

import (
	"fmt"
	"os"
)

// openScriptFileForHashing is the Windows counterpart of the Unix
// non-blocking-open guard (scriptfile_open_unix.go). Windows has no
// filesystem-path equivalent of a Unix FIFO — named pipes live under the
// separate \\.\pipe\ namespace, not as ordinary files a `git checkout`
// could place at .agent-deck/worktree-setup.sh — so the specific hang this
// guards against on Unix is not reachable the same way here. A plain
// Open+Stat is used, with the regular-file check applied to the opened
// descriptor rather than a fresh path lookup, keeping the same intent
// (reject non-regular content) even though the O_NONBLOCK-based race
// closure isn't available. CI for this repo does not run a Windows job.
func openScriptFileForHashing(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a regular file (refusing to read a symlink/device target)", path)
	}
	return f, nil
}
