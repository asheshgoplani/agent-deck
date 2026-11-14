package session

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Install kind: the one rule that decides which default an unset safety
// switch gets (owner ruling 2026-10-24: safe defaults for NEW installs only,
// existing installs keep exactly today's behaviour).
//
//   - "new": this release (or a later one) created the install. Unset
//     switches resolve to the safe value.
//   - "existing": the install predates this release. Unset switches resolve
//     to the value they had before (today's behaviour).
//
// The decision is stored in a write-once marker file, <data root>/install-kind.
// With no marker it is derived from evidence that only an older release can
// have left behind, because this release records "new" right before it
// creates the first config.toml (SaveUserConfig, CreateExampleConfig) or the
// first state.db (statedb.Open's create hook):
//
//   - a config.toml (XDG or legacy path), or
//   - a profile store: profiles/<any>/state.db or sessions.json, or the
//     pre-profile sessions.json, under the XDG data root or ~/.agent-deck.
//
// Directories, logs, config.json and the like are not evidence, so a config
// dir made by another command before the first TUI start does not misfire.
// Reading the kind never writes anything.
const (
	InstallKindNew      = "new"
	InstallKindExisting = "existing"
)

// InstallKindMarkerName is the marker file at the data root.
const InstallKindMarkerName = "install-kind"

// installKindOverride is a test seam for packages whose tests cannot lay out
// a home of a given kind; "" means decide from disk.
var installKindOverride atomic.Value // string

func installKindPinned() string {
	v, _ := installKindOverride.Load().(string)
	return v
}

// SetInstallKindForTest pins InstallKind for a test and returns the restore
// func. Only for tests.
func SetInstallKindForTest(kind string) func() {
	prev := installKindPinned()
	installKindOverride.Store(kind)
	return func() { installKindOverride.Store(prev) }
}

// InstallKind returns InstallKindNew or InstallKindExisting. A marker that
// cannot be read, or holds anything unknown, counts as no marker.
func InstallKind() string {
	if v := installKindPinned(); v != "" {
		return v
	}
	if v := readInstallKindMarker(); v != "" {
		return v
	}
	if installHasHistory() {
		return InstallKindExisting
	}
	return InstallKindNew
}

// isExistingInstall is the predicate the legacy defaults key on.
func isExistingInstall() bool { return InstallKind() == InstallKindExisting }

// installKindMarkerCandidates lists where the marker may live: the XDG data
// root and the legacy ~/.agent-deck root.
func installKindMarkerCandidates() []string {
	var out []string
	for _, get := range []func() (string, error){agentpaths.DataDir, agentpaths.LegacyDir} {
		if dir, err := get(); err == nil && dir != "" {
			out = append(out, filepath.Join(dir, InstallKindMarkerName))
		}
	}
	return out
}

// installKindMarkerWritePath is the marker's home for a new write: the data
// root this install uses (legacy ~/.agent-deck for a legacy install).
func installKindMarkerWritePath() (string, error) {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, InstallKindMarkerName), nil
}

func readInstallKindMarker() string {
	for _, p := range installKindMarkerCandidates() {
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		switch v := strings.TrimSpace(string(data)); v {
		case InstallKindNew, InstallKindExisting:
			return v
		}
	}
	return ""
}

// installHasHistory reports evidence left by a release older than this one.
func installHasHistory() bool {
	for _, get := range []func() (string, error){agentpaths.ConfigDir, agentpaths.LegacyDir} {
		if dir, err := get(); err == nil && pathPresent(filepath.Join(dir, UserConfigFileName)) {
			return true
		}
	}
	for _, get := range []func() (string, error){agentpaths.DataDir, agentpaths.LegacyDir} {
		root, err := get()
		if err != nil || root == "" {
			continue
		}
		if pathPresent(filepath.Join(root, storeRootLegacySessionsJSON)) {
			return true
		}
		entries, err := os.ReadDir(filepath.Join(root, ProfilesDirName))
		if err != nil {
			continue
		}
		for _, e := range entries {
			dir := filepath.Join(root, ProfilesDirName, e.Name())
			// Follow a symlinked profile dir (another disk, dotfiles): the
			// DirEntry type of a link is not a directory.
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				continue
			}
			if pathPresent(filepath.Join(dir, storeRootStateDBName)) || pathPresent(filepath.Join(dir, storeRootLegacySessionsJSON)) {
				return true
			}
		}
	}
	return false
}

// pathPresent is Lstat-exists; an error other than not-exist counts as
// present (fail toward today's behaviour, never toward a silent flip).
func pathPresent(p string) bool {
	_, err := os.Lstat(p)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

// recordInstallKindBeforeCreate runs right before this release creates the
// first config.toml or a state.db. With no marker and no older evidence it
// records "new"; it never writes over a marker and never writes "existing"
// (that is ApplyInstallDefaults' job, together with the notice). Best effort:
// a failure is logged.
func recordInstallKindBeforeCreate() {
	if installKindPinned() != "" || readInstallKindMarker() != "" || installHasHistory() {
		return
	}
	path, err := installKindMarkerWritePath()
	if err == nil {
		_, err = createInstallKindMarker(path, InstallKindNew)
	}
	if err != nil {
		sessionLog.Warn("install_kind_record_failed", slog.String("error", err.Error()))
	}
}

func init() {
	statedb.BeforeCreate = func(string) { recordInstallKindBeforeCreate() }
}

// createInstallKindMarker creates the marker only if it does not exist yet
// (a complete temp file hard-linked into place). It reports false, with no
// error, when another writer won.
func createInstallKindMarker(path, value string) (bool, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-"+InstallKindMarkerName+"-*")
	if err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	_, werr := tmp.WriteString(value + "\n")
	if serr := tmp.Sync(); werr == nil {
		werr = serr
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return false, fmt.Errorf("write %s: %w", path, werr)
	}
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// legacyDefault is one safety switch whose unset value differs between new
// and existing installs.
type legacyDefault struct {
	table, key string
	isSet      func(*UserConfig) bool
	meaning    string
}

// legacyDefaults is every switch the install kind decides. Each one's
// existing-install value is true.
var legacyDefaults = []legacyDefault{
	{table: "claude", key: "dangerous_mode",
		isSet:   func(c *UserConfig) bool { return c.Claude.DangerousMode != nil },
		meaning: "Claude sessions skip permission prompts (--dangerously-skip-permissions)"},
	{table: "security", key: "auto_trust_folders",
		isSet:   func(c *UserConfig) bool { return c.Security.AutoTrustFolders != nil },
		meaning: "agent-deck marks every folder it starts a session in as trusted, and sources a relative env_file from it"},
}

// installDefaultsLockWait bounds the wait for a parallel start.
const installDefaultsLockWait = 10 * time.Second

// ApplyInstallDefaults runs once per existing install, at an interactive
// start (main calls it only when stdin and stdout are a terminal, never from
// daemons, hooks or scripts). It returns the one-time notice, or "".
//
//   - Marker present: already decided, nothing to do.
//   - No marker and no older evidence: a new install. Nothing is written;
//     the create hooks record "new" when this release creates its files.
//   - Existing install: each switch config.toml leaves unset is written into
//     it with today's value (true), as one line under its table, so the rest
//     of the file, comments included, stays byte for byte; config.toml.bak
//     holds the previous file. Without a config.toml nothing is created (the
//     TUI wizard keys off its absence). A layout the line insert cannot edit
//     safely is left alone. Either way the marker becomes "existing", which
//     keeps today's values for every unset switch from then on.
//
// A config.toml that fails to parse is never rewritten: the error is
// returned, no marker is written, and the next start tries again.
func ApplyInstallDefaults() (string, error) {
	if installKindPinned() != "" || readInstallKindMarker() != "" || !installHasHistory() {
		return "", nil
	}
	markerPath, err := installKindMarkerWritePath()
	if err != nil {
		return "", fmt.Errorf("install defaults: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o700); err != nil {
		return "", fmt.Errorf("install defaults: %w", err)
	}
	lock, err := AcquireConfigFileLockTimeout(markerPath, installDefaultsLockWait)
	if err != nil {
		return "", fmt.Errorf("install defaults: %w", err)
	}
	defer lock.Release()
	if readInstallKindMarker() != "" {
		return "", nil // a parallel start finished first
	}

	configPath, err := GetUserConfigPath()
	if err != nil {
		return "", fmt.Errorf("install defaults: %w", err)
	}
	var kept []legacyDefault
	written, haveConfig := false, pathPresent(configPath)
	if haveConfig {
		content, err := os.ReadFile(configPath)
		if err != nil {
			return "", fmt.Errorf("install defaults: read %s: %w", configPath, err)
		}
		var cfg UserConfig
		if _, err := toml.Decode(string(content), &cfg); err != nil {
			return "", fmt.Errorf("install defaults: %s does not parse, left unchanged: %w", configPath, err)
		}
		for _, d := range legacyDefaults {
			if !d.isSet(&cfg) {
				kept = append(kept, d)
			}
		}
		if len(kept) > 0 {
			if updated, ok := insertLegacyDefaults(content, kept); ok {
				if err := writeConfigInPlace(configPath, updated); err != nil {
					return "", fmt.Errorf("install defaults: %w", err)
				}
				written = true
			} else {
				sessionLog.Warn("install_defaults_config_not_edited", slog.String("path", configPath))
			}
		}
	} else {
		kept = legacyDefaults
	}
	if _, err := createInstallKindMarker(markerPath, InstallKindExisting); err != nil {
		return "", fmt.Errorf("install defaults: %w", err)
	}
	sessionLog.Info("install_kind_recorded", slog.String("kind", InstallKindExisting),
		slog.Int("kept", len(kept)), slog.Bool("config_written", written))
	if len(kept) == 0 {
		return "", nil
	}
	return installDefaultsNotice(kept, configPath, haveConfig, written), nil
}

func installDefaultsNotice(kept []legacyDefault, configPath string, haveConfig, written bool) string {
	var b strings.Builder
	b.WriteString("agent-deck: new installs now start with safer defaults. This install already\n")
	b.WriteString("existed, so it keeps its current behaviour:\n")
	for _, d := range kept {
		fmt.Fprintf(&b, "  [%s] %s = true: %s\n", d.table, d.key, d.meaning)
	}
	switch {
	case written:
		fmt.Fprintf(&b, "These lines were added to %s (the previous file is config.toml.bak).\n", configPath)
		b.WriteString("Set a value to false to turn it off.\n")
	case haveConfig:
		fmt.Fprintf(&b, "%s could not be edited safely, so it was left as it is; the values above\n", configPath)
		b.WriteString("still apply. To turn one off, add it with false, for example:\n")
		fmt.Fprintf(&b, "  [%s] %s = false\n", kept[0].table, kept[0].key)
	default:
		b.WriteString("There is no config.toml, so nothing was written. To turn one off, add it to\n")
		fmt.Fprintf(&b, "%s with false, for example:\n", configPath)
		fmt.Fprintf(&b, "  [%s]\n  %s = false\n", kept[0].table, kept[0].key)
	}
	b.WriteString("This notice is shown once.")
	return b.String()
}

// tomlTableHeader matches a standard table header line for one table name,
// with an optional trailing comment.
func tomlTableHeader(table string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^[ \t]*\[[ \t]*` + regexp.QuoteMeta(table) + `[ \t]*\][ \t]*(#.*)?$`)
}

// insertAfterTableHeader puts line right after the [table] header line. It
// reports false when the content has no such header.
func insertAfterTableHeader(content []byte, table, line string) ([]byte, bool) {
	loc := tomlTableHeader(table).FindIndex(content)
	if loc == nil {
		return nil, false
	}
	end := loc[1]
	var buf bytes.Buffer
	buf.Write(content[:end])
	if end < len(content) && content[end] == '\r' {
		buf.WriteByte('\r')
		end++
	}
	if end < len(content) && content[end] == '\n' {
		end++
	}
	buf.WriteByte('\n')
	buf.WriteString(line)
	buf.Write(content[end:])
	return buf.Bytes(), true
}

// writeConfigInPlace replaces config.toml with an in-place text edit of it:
// config.toml.bak first, the file's permission bits kept, a symlink written
// through, atomic and durable.
func writeConfigInPlace(configPath string, updated []byte) error {
	if err := backupConfigFile(configPath); err != nil {
		return fmt.Errorf("back up %s: %w", configPath, err)
	}
	perm := os.FileMode(0o600)
	if info, err := os.Stat(configPath); err == nil {
		perm = info.Mode().Perm()
	}
	if err := atomicfile.WriteFileDurable(configPath, updated, perm); err != nil {
		return fmt.Errorf("write %s: %w", configPath, err)
	}
	ClearUserConfigCache()
	return nil
}

// insertLegacyDefaults adds "key = true" for each switch: on the line right
// after its table's header, or as a new table at the end. The result must
// decode to exactly the old document plus those keys; otherwise (an inline
// table, dotted keys, a header inside a multi-line string) it reports false
// and the caller leaves the file alone.
func insertLegacyDefaults(content []byte, kept []legacyDefault) ([]byte, bool) {
	out := append([]byte(nil), content...)
	var tail bytes.Buffer
	for _, d := range kept {
		line := d.key + " = true\n"
		if updated, ok := insertAfterTableHeader(out, d.table, line); ok {
			out = updated
			continue
		}
		fmt.Fprintf(&tail, "\n[%s]\n%s", d.table, line)
	}
	if tail.Len() > 0 {
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, tail.Bytes()...)
	}

	before, after := map[string]any{}, map[string]any{}
	if _, err := toml.Decode(string(content), &before); err != nil {
		return nil, false
	}
	if _, err := toml.Decode(string(out), &after); err != nil {
		return nil, false
	}
	for _, d := range kept {
		tbl, ok := after[d.table].(map[string]any)
		if !ok || tbl[d.key] != true {
			return nil, false
		}
		delete(tbl, d.key)
		if len(tbl) == 0 {
			if _, existed := before[d.table]; !existed {
				delete(after, d.table)
			}
		}
	}
	if !reflect.DeepEqual(before, after) {
		return nil, false
	}
	return out, true
}
