package session

import (
	"os"
	"path/filepath"
	"strings"

	"al.essio.dev/pkg/shellescape"
)

// configDirShellExpr renders a resolved CLAUDE_CONFIG_DIR as a shell VALUE
// EXPRESSION for the payload this instance's spawn command becomes.
//
// Issue #1858 (reporter @yuvalsc): for an --ssh session that payload runs on
// the REMOTE host, as the remote user, but the path was built from the LOCAL
// machine's home and passed verbatim:
//
//	export CLAUDE_CONFIG_DIR=/Users/yuvals/.local/share/agent-deck/worker-scratch/<id>
//
// With local user `yuvals` and remote user `cloudlydr`, /Users/yuvals does not
// exist on the remote host, claude cannot create its config dir, and the
// session exits after ~250ms.
//
// The rule: a path under the LOCAL home is only meaningful relative to a home
// directory, so it is emitted relative to $HOME and left for the remote shell
// to expand against the remote user's home. A path OUTSIDE the local home is
// the user's own absolute declaration — that is the reporter's working
// `[profiles.<account>.claude].config_dir = /Users/cloudlydr/.claude` case —
// and passes through unchanged. Local sessions are unaffected and keep the
// exact literal path they always had.
//
// The result is shell-safe: everything except the deliberate `"$HOME"` is
// single-quoted, so a config dir containing ;/$() cannot inject into the
// `bash -c` payload (which wrapForSSH then quotes exactly once into
// `ssh -t <cmd>` — see the escaping note there).
func (i *Instance) configDirShellExpr(dir string) string {
	if i == nil || !i.IsSSH() {
		return shellescape.Quote(dir)
	}
	rel, ok := pathRelativeToLocalHome(dir)
	if !ok {
		return shellescape.Quote(dir)
	}
	if rel == "" {
		return `"$HOME"`
	}
	return `"$HOME"` + shellescape.Quote("/"+rel)
}

// pathRelativeToLocalHome reports whether dir lives under this machine's home
// directory and, if so, its path relative to it ("" when dir IS the home dir).
func pathRelativeToLocalHome(dir string) (string, bool) {
	home := strings.TrimSpace(os.Getenv("HOME"))
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil || home == "" {
			return "", false
		}
	}
	home = filepath.Clean(home)
	dir = filepath.Clean(dir)
	if dir == home {
		return "", true
	}
	if !strings.HasPrefix(dir, home+string(os.PathSeparator)) {
		return "", false
	}
	rel, err := filepath.Rel(home, dir)
	if err != nil || rel == "" || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return rel, true
}
