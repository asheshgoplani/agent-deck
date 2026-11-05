package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
)

// Per-socket tmux binaries, [tmux.binaries] in config.toml.
//
// A tmux client only talks to a server of its own version: a tmux 3.4
// client gets "server exited unexpectedly" from a 3.6a server, and a 3.6a
// client attaching to a 3.4 server fails with "open terminal failed: not a
// terminal". So a newer tmux cannot go first on PATH while sessions still
// live on an older server, and agent-deck cannot reach a newer server
// through the bare "tmux" on PATH.
//
// The map names, per -L socket, the binary agent-deck runs for every tmux
// call on that socket: commands, new-session (direct or under
// systemd-run), attach, control-mode pipes, and the version probe that
// gates features such as codepoint-widths. Sockets not in the map, and the
// default server, keep the bare "tmux" from PATH, so sessions move to a new
// server one at a time (see `session set <id> tmux-socket <name>`).
var (
	socketBinariesMu sync.RWMutex
	socketBinaries   map[string]string
)

// defaultTmuxBinary is what every socket without an entry runs.
const defaultTmuxBinary = "tmux"

// SetSocketBinaries seeds the per-socket binaries once at program start from
// [tmux.binaries]. Keys and values are trimmed; an entry with an empty key
// or value is dropped. A nil or empty map clears it.
func SetSocketBinaries(m map[string]string) {
	clean := make(map[string]string, len(m))
	for k, v := range m {
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		clean[k] = v
	}
	socketBinariesMu.Lock()
	defer socketBinariesMu.Unlock()
	socketBinaries = clean
}

// BinaryFor returns the tmux binary for the -L socket socketName: the
// configured path, or "tmux" (resolved through PATH).
func BinaryFor(socketName string) string {
	if bin, ok := ConfiguredBinary(socketName); ok {
		return bin
	}
	return defaultBinary()
}

// ConfiguredBinary returns the [tmux.binaries] entry for the -L socket
// socketName and true, or "" and false when the socket has none (the
// default server, or a socket served by the bare "tmux" on PATH). Clients
// that run tmux themselves (the Mac app's Terminal pane over ssh) read it
// from `list --json` / `session show --json` as tmux_binary.
func ConfiguredBinary(socketName string) (string, bool) {
	name := strings.TrimSpace(socketName)
	if name == "" {
		return "", false
	}
	socketBinariesMu.RLock()
	defer socketBinariesMu.RUnlock()
	bin, ok := socketBinaries[name]
	return bin, ok
}

// BinaryForSocketPath maps a socket path (the first field of $TMUX, or a
// -S value) to its binary. A -L <name> socket lives at
// $TMUX_TMPDIR/tmux-<uid>/<name>, so the path's base name is the socket name.
func BinaryForSocketPath(socketPath string) string {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return defaultBinary()
	}
	return BinaryFor(filepath.Base(socketPath))
}

var (
	binaryVersionMu    sync.Mutex
	binaryVersionCache = map[string]string{}
)

// socketTmuxVersionString is hostTmuxVersionString for the binary that
// serves socketName. A socket without its own binary reads the host's
// version; one with a binary probes `<binary> -V` once per path. A failed
// probe gives "" (unknown), as the host probe does, and is retried next
// time.
func socketTmuxVersionString(socketName string) string {
	bin, ok := ConfiguredBinary(socketName)
	if !ok {
		return hostTmuxVersionString()
	}
	binaryVersionMu.Lock()
	defer binaryVersionMu.Unlock()
	if v, ok := binaryVersionCache[bin]; ok {
		return v
	}
	out, err := commandCombinedOutput(exec.Command(bin, "-V")) // #nosec G204 -- configured binary path, fixed argv
	if err != nil {
		return ""
	}
	v := parseTmuxVersion(strings.TrimSpace(string(out)))
	binaryVersionCache[bin] = v
	return v
}

// withSocketBinaryPATH returns env with the directory of socketName's
// binary first on PATH, for the tmux CLIENT agent-deck runs on that socket.
// tmux gives a pane started by an unattached client (new-session,
// respawn-pane: agent-deck's start and restart) that client's PATH, over
// the server's and the session's environment (measured on 3.6a: neither
// `set-environment -g PATH` nor `new-session -e PATH=` survives a
// respawn-pane). So this is how a bare `tmux` run inside those panes (an
// agent, a hook, Claude Code itself) becomes the client that matches the
// server $TMUX names, not the older one first on PATH. Put the binary in a
// directory of its own: everything in it goes first on PATH in those
// panes. env is returned unchanged for a socket without an absolute
// binary.
func withSocketBinaryPATH(socketName string, env []string) []string {
	bin, ok := ConfiguredBinary(socketName)
	if !ok || !filepath.IsAbs(bin) {
		return env
	}
	if env == nil {
		// nil means "inherit" (childenv.WithoutClaudeJobDir returns nil when
		// there is nothing to drop). Editing PATH needs the whole inherited
		// environment: a client left with only PATH loses TMUX_TMPDIR and
		// HOME and starts its server in the wrong socket directory.
		env = childenv.ForSelfReexec()
	}
	dir := filepath.Dir(bin)
	out := make([]string, 0, len(env)+1)
	found := false
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			found = true
			kv = "PATH=" + dir
			if v != "" {
				kv += string(os.PathListSeparator) + v
			}
		}
		out = append(out, kv)
	}
	if !found {
		out = append(out, "PATH="+dir)
	}
	return out
}

// newSessionSpawnCommand is newSpawnCommand for a session's start on
// socketName: the spawned client (tmux, or systemd-run --scope execing
// it) carries the socket binary's PATH to the new pane.
func newSessionSpawnCommand(socketName, launcher string, args ...string) *exec.Cmd {
	cmd := newSpawnCommand(launcher, args...)
	cmd.Env = withSocketBinaryPATH(socketName, cmd.Env)
	return cmd
}

// isDirectTmuxLauncher reports whether launcher (from startCommandSpec) is a
// direct tmux spawn rather than systemd-run: "tmux", or the socket's
// [tmux.binaries] path. Start keys its stale-socket recovery on it, so a
// socket with its own binary keeps that recovery.
func isDirectTmuxLauncher(socketName, launcher string) bool {
	return launcher == defaultTmuxBinary || launcher == defaultBinary() || launcher == BinaryFor(socketName)
}

// tmuxEnvNamesConfiguredSocket reports whether $TMUX names a server on a
// socket that has its own [tmux.binaries] entry: this process runs inside a
// pane of a server agent-deck moved sessions to. Only then is a socket-less
// tmux call pinned to the default server; a user who runs agent-deck inside
// their own private server keeps tmux's usual $TMUX routing.
func tmuxEnvNamesConfiguredSocket(lookupEnv func(string) string) bool {
	path := socketPathFromTmuxEnv(lookupEnv("TMUX"))
	if path == "" {
		return false
	}
	name := filepath.Base(path)
	if name == defaultTmuxSocketName {
		return false
	}
	_, ok := ConfiguredBinary(name)
	return ok
}

// defaultBinary is the tmux binary for the default server and every socket
// without a [tmux.binaries] entry: "tmux" from PATH, skipping the directories
// of the configured binaries. A pane on a configured socket gets its binary's
// directory first on PATH (withSocketBinaryPATH), so inside such a pane the
// bare "tmux" is the newer client. Run against the default server it would
// fail on an older server, or, when no default server runs yet, start the
// default server as the newer version and lock out every older client.
func defaultBinary() string {
	skip := configuredBinaryDirs()
	if len(skip) == 0 {
		return defaultTmuxBinary
	}
	first, err := exec.LookPath(defaultTmuxBinary)
	if err != nil || !skip[filepath.Clean(filepath.Dir(first))] {
		return defaultTmuxBinary
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || !filepath.IsAbs(dir) || skip[filepath.Clean(dir)] {
			continue
		}
		candidate := filepath.Join(dir, defaultTmuxBinary)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	return defaultTmuxBinary
}

// configuredBinaryDirs is the set of directories holding an absolute
// [tmux.binaries] path.
func configuredBinaryDirs() map[string]bool {
	socketBinariesMu.RLock()
	defer socketBinariesMu.RUnlock()
	if len(socketBinaries) == 0 {
		return nil
	}
	dirs := make(map[string]bool, len(socketBinaries))
	for _, bin := range socketBinaries {
		if filepath.IsAbs(bin) {
			dirs[filepath.Clean(filepath.Dir(bin))] = true
		}
	}
	return dirs
}
