package session

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"

	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// LOC-4: inline env values from config ([tools.X].env, [groups.X.claude].env,
// [conductors.X.claude].env, conductor meta.json env) and one-shot
// `session restart --env` overrides used to be spliced into the pane command
// as `export KEY='value' && …`. That command is the argv of the tmux client,
// of systemd-run (and its persisted unit), and of the pane's leader bash for
// the life of the session — readable by any local user via ps / /proc and
// retained in tmux's #{pane_start_command}. The values are now written to a
// 0600 file in the user's private runtime dir and the command only carries
// `source '<file>'`.
//
// The file name is random (<id>-<kind>-<random>.sh), so the command line does
// not carry anything derived from the values. A build whose exports match an
// existing file of the same instance and kind reuses it, so a command built
// twice is byte-identical and a re-run of the same command (systemd
// Restart=, a respawn of #{pane_start_command}) still finds its file.
//
// Files are removed so a one-shot `restart --env` value or a rotated token
// does not stay on disk: an instance's superseded files of a kind, and its
// restart file once a build no longer uses restart overrides, after
// launchEnvPruneGrace (a saved command or a racing build may still source
// them); all of a session's files when it is deleted; and, in the startup
// hook-artifact sweep, files of instances that no longer exist. The command
// sources a file only if it is there and says so in the pane otherwise, so a
// missing file never kills a session start.
//
// Remote (--ssh) and sandboxed (docker) sessions keep the inline form: their
// command runs on another host / in a container where a local file does not
// exist.

const launchEnvDirName = "launch-env"

// Launch env kinds: one file slot per instance and kind. A launch can source
// several kinds at once, so pruning is per kind, never per instance.
const (
	launchEnvKindClaude    = "claude"
	launchEnvKindTool      = "tool"
	launchEnvKindConductor = "conductor"
	launchEnvKindRestart   = "restart"
)

var launchEnvNoticeOnce sync.Once

// offloadInlineEnv returns a command fragment that applies exports (a chain of
// `export K='V'` / `source …` statements joined by " && ") without placing the
// values in argv. It falls back to returning exports unchanged when the
// session cannot see a local file or the file cannot be written.
func (i *Instance) offloadInlineEnv(kind, exports string) string {
	if exports == "" || i.IsSandboxed() || i.IsSSH() {
		return exports
	}
	path, err := writeLaunchEnvFile(i.ID, kind, exports)
	if err != nil {
		sessionLog.Warn("launch_env_file_write_failed; inline env stays in the command line",
			slog.String("instance_id", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(i.ID), "\n", ""), "\r", "")),
			slog.String("error", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(err.Error()), "\n", ""), "\r", "")))
		return exports
	}
	launchEnvNoticeOnce.Do(func() {
		sessionLog.Info("launch_env_offloaded: inline env values are now passed via a private 0600 file instead of the command line",
			slog.String("dir", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(filepath.Dir(path)), "\n", ""), "\r", "")))
	})
	return launchEnvSourceCommand(path)
}

// launchEnvSourceCommand sources path when it exists and otherwise prints a
// notice to the pane and goes on; it always succeeds, so the && chain of the
// launch command continues either way.
func launchEnvSourceCommand(path string) string {
	q := shellescape.Quote(path)
	return "{ if [ -f " + q + " ]; then source " + q + "; else echo 'agent-deck: launch env file missing, env from config not applied; restart the session' >&2; fi; }"
}

// launchEnvDir returns the private directory holding launch env files,
// creating it 0700 (and tightening an existing one).
func launchEnvDir() (string, error) {
	dir, err := runtimeDataPath(launchEnvDirName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// launchEnvPruneGrace is how long a superseded launch env file is kept: a
// saved command (a systemd unit, a session recreated from its stored
// command) or a racing TUI/CLI build may still source it.
const launchEnvPruneGrace = 10 * time.Minute

// launchEnvKinds lists every kind, for whole-instance removal.
var launchEnvKinds = []string{launchEnvKindClaude, launchEnvKindTool, launchEnvKindConductor, launchEnvKindRestart}

// writeLaunchEnvFile writes exports to <runtime>/launch-env/<id>-<kind>-<random>.sh
// with mode 0600 and returns its path. An existing file of the same instance
// and kind with the same content is reused (and its time refreshed); the
// instance's other files of that kind are removed once older than the grace.
func writeLaunchEnvFile(instanceID, kind, exports string) (string, error) {
	dir, err := launchEnvDir()
	if err != nil {
		return "", err
	}
	body := []byte(exports + "\n")
	prefix := launchEnvFileStem(instanceID) + "-" + kind + "-"
	path := findLaunchEnvFile(dir, prefix, body)
	if path != "" {
		// Refresh its time so the grace runs from this build. A concurrent
		// build may have pruned it since it was found; then write a fresh one.
		now := time.Now()
		if err := os.Chtimes(path, now, now); err != nil {
			path = ""
		}
	}
	if path == "" {
		var rnd [8]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return "", err
		}
		path = filepath.Join(dir, prefix+hex.EncodeToString(rnd[:])+".sh")
		if err := atomicfile.WriteFileDurable(path, body, 0o600); err != nil {
			return "", err
		}
	}
	// Files without an instance id share the stem; never prune those here,
	// the startup sweep removes them once old.
	if instanceID != "" {
		removeLaunchEnvFilesMatching(dir, prefix, filepath.Base(path), time.Now().Add(-launchEnvPruneGrace))
	}
	return path, nil
}

// findLaunchEnvFile returns the 0600 file in dir named prefix*.sh whose
// content is body, or "".
func findLaunchEnvFile(dir, prefix string, body []byte) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".sh") {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			continue
		}
		if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, body) {
			return path
		}
	}
	return ""
}

// pruneLaunchEnvKind removes the instance's launch env files of kind once
// older than the grace, used when a build no longer sources that kind (a
// restart without --env).
func pruneLaunchEnvKind(instanceID, kind string) {
	if instanceID == "" {
		return
	}
	dir, err := runtimeDataPath(launchEnvDirName)
	if err != nil {
		return
	}
	removeLaunchEnvFilesMatching(dir, launchEnvFileStem(instanceID)+"-"+kind+"-", "", time.Now().Add(-launchEnvPruneGrace))
}

// removeLaunchEnvFiles removes every launch env file of a deleted instance,
// with no grace: the session is gone.
func removeLaunchEnvFiles(instanceID string) {
	if instanceID == "" {
		return
	}
	dir, err := runtimeDataPath(launchEnvDirName)
	if err != nil {
		return
	}
	for _, kind := range launchEnvKinds {
		removeLaunchEnvFilesMatching(dir, launchEnvFileStem(instanceID)+"-"+kind+"-", "", time.Time{})
	}
}

// sweepLaunchEnvOrphans removes launch env files, older than cutoff, of
// instances not in ids (deleted while agent-deck was not running, dropped by
// a reconcile, or written without an id). Part of the hook-artifact sweep.
func sweepLaunchEnvOrphans(ids map[string]bool, cutoff time.Time) {
	dir, err := runtimeDataPath(launchEnvDirName)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	live := make(map[string]bool, len(ids))
	for id := range ids {
		if id != "" {
			live[launchEnvFileStem(id)] = true
		}
	}
	for _, e := range entries {
		stem, ok := launchEnvFileStemOf(e.Name())
		if !ok || e.IsDir() || live[stem] {
			continue
		}
		removeLaunchEnvFilesMatching(dir, e.Name(), "", cutoff)
	}
}

// launchEnvFileStemOf parses <stem>-<kind>-<random>.sh and returns stem.
func launchEnvFileStemOf(name string) (string, bool) {
	base, ok := strings.CutSuffix(name, ".sh")
	if !ok {
		return "", false
	}
	i := strings.LastIndexByte(base, '-')
	if i < 0 {
		return "", false
	}
	base = base[:i]
	for _, kind := range launchEnvKinds {
		if stem, ok := strings.CutSuffix(base, "-"+kind); ok && stem != "" {
			return stem, true
		}
	}
	return "", false
}

// removeLaunchEnvFilesMatching removes the .sh files in dir whose name
// starts with prefix, except keep, whose time is before cutoff (a zero cutoff
// removes regardless of age). Best effort: a missing dir is nothing to do.
func removeLaunchEnvFilesMatching(dir, prefix, keep string, cutoff time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == keep || e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".sh") {
			continue
		}
		path := filepath.Join(dir, name)
		if !cutoff.IsZero() {
			info, err := os.Lstat(path)
			if err != nil || !info.ModTime().Before(cutoff) {
				continue
			}
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			sessionLog.Warn("launch_env_file_remove_failed",
				slog.String("file", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(name), "\n", ""), "\r", "")),
				slog.String("error", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(err.Error()), "\n", ""), "\r", "")))
		}
	}
}

// launchEnvFileStem reduces an instance id to a safe file-name stem.
func launchEnvFileStem(id string) string {
	stem := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '_'
	}, id)
	if stem == "" {
		stem = "noid"
	}
	if len(stem) > 64 {
		stem = stem[:64]
	}
	return stem
}

// warnInvalidEnvKey reports an env name that was dropped because it is not a
// valid shell identifier (^[A-Za-z_][A-Za-z0-9_]*$). Such a name would be
// spliced unquoted into `export <name>=…`, so it is never emitted.
//
// A rejected name can be a mis-parsed entry or a bare token that IS the
// secret ('ghp_…', 'API_KEY sk-…'), so no part of it is logged: only why it
// was rejected and its length. Line breaks are stripped in the form CodeQL
// models (go/log-injection).
func warnInvalidEnvKey(source, key string) {
	sessionLog.Warn("invalid_env_key_skipped: env names must match ^[A-Za-z_][A-Za-z0-9_]*$",
		slog.String("source", strings.ReplaceAll(strings.ReplaceAll(logging.SanitizeValue(source), "\n", ""), "\r", "")),
		slog.String("reason", EnvKeyRejectReason(key)),
		slog.Int("key_len", len(key)))
}
