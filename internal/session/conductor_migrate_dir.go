package session

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

// ConductorDefaultDir returns the default conductor base directory, IGNORING any
// [conductor].dir override. ConductorDir() consults the override first; this
// resolves only the underlying <data-dir>/conductor (XDG with legacy
// ~/.agent-deck/conductor fallback). migrate-dir and the split-brain detector
// need the pre-override location to find homes that did not move when the key
// flipped.
func ConductorDefaultDir() (string, error) {
	return dataPath("conductor", "conductor")
}

// sameConductorPath reports whether two conductor paths are the same after
// lexical cleaning. Both inputs are expected to be already expanded/absolute.
func sameConductorPath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

// isTransientConductorArtifact reports whether a base-level entry is a runtime
// log or staging temp that should NOT be migrated (it is regenerated/recreated
// at the new base, or is meaningless once moved).
func isTransientConductorArtifact(name string) bool {
	switch {
	case name == ".DS_Store":
		return true
	case strings.HasSuffix(name, ".log"):
		return true
	case strings.HasSuffix(name, ".tmp"):
		return true
	case strings.Contains(name, ".tmp."): // meta.json.tmp.*, etc.
		return true
	case strings.HasPrefix(name, ".agentdeck-migrate-"):
		return true
	default:
		return false
	}
}

// isConductorHome reports whether path is a conductor home: a directory (symlink
// targets resolved, matching ListConductors semantics) that contains meta.json.
func isConductorHome(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	_, err = os.Stat(filepath.Join(path, "meta.json"))
	return err == nil
}

// pathExistsLocal reports whether a path exists (lstat, so dangling symlinks
// count as existing).
func pathExistsLocal(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// conductorNamesIn returns the names of conductor homes directly under base
// (sorted). A missing base yields an empty slice, not an error.
func conductorNamesIn(base string) ([]string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if isConductorHome(filepath.Join(base, entry.Name())) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// countConductorsIn returns how many conductor homes live directly under base
// (0 on any error, since the detector that uses it must be side-effect-free).
func countConductorsIn(base string) int {
	names, err := conductorNamesIn(base)
	if err != nil {
		return 0
	}
	return len(names)
}

// DetectConductorDirSplitBrain reports the split-brain condition introduced by a
// declarative [conductor].dir flip: the key resolves immediately, but a
// populated fleet's physical homes do not move with it. It fires ONLY when the
// resolved conductor dir is empty AND the pre-override default base still holds
// conductor homes, returning a one-line warning pointing at migrate-dir.
//
// Detection-only, no side effects (mirrors HeartbeatDaemonStale). Returns
// ("", false) when there is no override in play, the resolved dir is already
// populated, or the default base is empty.
func DetectConductorDirSplitBrain() (string, bool) {
	resolved, err := ConductorDir()
	if err != nil {
		return "", false
	}
	if countConductorsIn(resolved) > 0 {
		return "", false
	}
	def, err := ConductorDefaultDir()
	if err != nil {
		return "", false
	}
	if sameConductorPath(resolved, def) {
		// No override (or override == default): nothing to reconcile.
		return "", false
	}
	n := countConductorsIn(def)
	if n == 0 {
		return "", false
	}
	plural := "home"
	if n != 1 {
		plural = "homes"
	}
	msg := fmt.Sprintf(
		"conductor dir resolves to %q (empty) but %d conductor %s remain at the default base %q — run 'agent-deck conductor migrate-dir %s --apply' to relocate them",
		resolved, n, plural, def, resolved,
	)
	return msg, true
}

// ConductorDirMigrateOptions configures a conductor-dir relocation.
type ConductorDirMigrateOptions struct {
	// Target is the destination base dir (tilde/$VAR expanded by the migrator).
	Target string
	// From optionally overrides the auto-detected source base.
	From string
	// Apply performs the move; when false the migration is a dry-run that
	// mutates nothing.
	Apply bool
	// Force merges into an existing destination per-file (destination wins on
	// per-file conflicts) instead of skipping it.
	Force bool
}

// ConductorDirMigrateAction records the disposition of a single base-level entry.
type ConductorDirMigrateAction struct {
	Name     string // entry name (conductor name or base file)
	IsHome   bool   // conductor home (dir with meta.json) vs base file/symlink
	Action   string // "move" | "merge" | "skip-exists" | "skip-transient"
	Conflict bool   // a destination already existed (preserved)
}

// ConductorDirMigrateResult summarizes a relocation for reporting.
type ConductorDirMigrateResult struct {
	DryRun            bool
	Source            string
	Target            string
	Actions           []ConductorDirMigrateAction
	Conductors        []string // conductor homes present in target afterward
	ConfigWritten     bool
	BridgeReinstalled bool
}

// MigrateConductorDir relocates the conductor base from its current/source
// location to Target as one explicit transaction:
//
//  1. Move/merge every base-level entry source→target using non-destructive,
//     no-clobber merge semantics (existing destination files preserved;
//     conflicts reported). Conductor homes and shared base files move together.
//  2. Persist [conductor].dir = target so the resolver points at the new base.
//  3. Reconcile path-baked artifacts: re-render heartbeat.sh per conductor (it
//     bakes the resolved CONDUCTOR_ROOT) and reinstall base bridge.py.
//
// Daemon reloads (launchctl/systemctl) are deliberately NOT done here — they
// belong to the CLI handler so this function stays unit-testable without a
// service manager. The returned Conductors list is the reconcile/reload set.
//
// A dry-run (Apply=false) builds the action plan and changes nothing.
func MigrateConductorDir(opts ConductorDirMigrateOptions) (*ConductorDirMigrateResult, error) {
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		return nil, fmt.Errorf("target conductor dir is required")
	}
	target = ExpandPath(target)

	source, err := resolveMigrateSource(opts.From, target)
	if err != nil {
		return nil, err
	}

	res := &ConductorDirMigrateResult{DryRun: !opts.Apply, Source: source, Target: target}

	if !sameConductorPath(source, target) {
		if err := planAndMove(source, target, opts, res); err != nil {
			return nil, err
		}
	}

	if res.DryRun {
		// Report the conductor homes that will live in target afterward without
		// touching config or the filesystem.
		res.Conductors = plannedTargetConductors(target, res.Actions)
		return res, nil
	}

	// Persist the override so ConductorDir() resolves to target from here on.
	cfg, err := LoadUserConfig()
	if err != nil {
		return res, fmt.Errorf("load user config: %w", err)
	}
	cfg.Conductor.Dir = target
	if err := SaveUserConfig(cfg); err != nil {
		return res, fmt.Errorf("write [conductor].dir: %w", err)
	}
	res.ConfigWritten = true

	// Reconcile path-baked artifacts against the now-resolved target.
	names, err := conductorNamesIn(target)
	if err != nil {
		return res, fmt.Errorf("scan target conductors: %w", err)
	}
	res.Conductors = names
	for _, name := range names {
		meta, err := LoadConductorMeta(name)
		if err != nil {
			continue
		}
		if err := InstallHeartbeatScript(name, meta.Profile); err != nil {
			return res, fmt.Errorf("re-render heartbeat.sh for %q: %w", name, err)
		}
	}
	// bridge.py is fully regenerable; a failure here must not abort the move.
	if err := InstallBridgeScript(); err != nil {
		sessionLog.Warn("conductor_migrate_dir_bridge_reinstall_failed", slog.String("error", err.Error()))
	} else {
		res.BridgeReinstalled = true
	}

	return res, nil
}

// resolveMigrateSource picks the source base. An explicit From wins; otherwise
// the current ConductorDir() is used, unless the user already pointed the key at
// target (in which case the homes still live at the default base).
func resolveMigrateSource(from, target string) (string, error) {
	if s := strings.TrimSpace(from); s != "" {
		return ExpandPath(s), nil
	}
	cur, err := ConductorDir()
	if err != nil {
		return "", fmt.Errorf("resolve current conductor dir: %w", err)
	}
	if sameConductorPath(cur, target) {
		def, err := ConductorDefaultDir()
		if err != nil {
			return "", fmt.Errorf("resolve default conductor dir: %w", err)
		}
		return def, nil
	}
	return cur, nil
}

// planAndMove enumerates source entries and (unless dry-run) moves/merges each
// into target, recording an action per entry.
func planAndMove(source, target string, opts ConductorDirMigrateOptions, res *ConductorDirMigrateResult) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no source homes to move
		}
		return fmt.Errorf("read source conductor dir %q: %w", source, err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if isTransientConductorArtifact(name) {
			res.Actions = append(res.Actions, ConductorDirMigrateAction{Name: name, Action: "skip-transient"})
			continue
		}
		srcPath := filepath.Join(source, name)
		dstPath := filepath.Join(target, name)
		act := ConductorDirMigrateAction{Name: name, IsHome: isConductorHome(srcPath)}

		dstExists, err := pathExistsLocal(dstPath)
		if err != nil {
			return fmt.Errorf("stat destination %q: %w", dstPath, err)
		}

		switch {
		case !dstExists:
			act.Action = "move"
			if !res.DryRun {
				if err := moveConductorTree(srcPath, dstPath); err != nil {
					return fmt.Errorf("move %q -> %q: %w", srcPath, dstPath, err)
				}
			}
		case opts.Force:
			act.Action = "merge"
			if !res.DryRun {
				conflicted, err := agentpaths.MergeTree(srcPath, dstPath)
				if err != nil {
					return fmt.Errorf("merge %q -> %q: %w", srcPath, dstPath, err)
				}
				act.Conflict = conflicted
				if err := os.RemoveAll(srcPath); err != nil {
					return fmt.Errorf("remove migrated source %q: %w", srcPath, err)
				}
			} else {
				act.Conflict = true
			}
		default:
			act.Action = "skip-exists"
			act.Conflict = true
		}
		res.Actions = append(res.Actions, act)
	}
	return nil
}

// moveConductorTree moves src to dst, preferring an atomic rename and falling
// back to a no-clobber copy + remove when rename fails (e.g. cross-device).
func moveConductorTree(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := agentpaths.CopyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

// plannedTargetConductors returns the conductor homes that would exist under
// target after a dry-run: those already present plus source homes slated to
// move/merge.
func plannedTargetConductors(target string, actions []ConductorDirMigrateAction) []string {
	set := map[string]struct{}{}
	if existing, err := conductorNamesIn(target); err == nil {
		for _, n := range existing {
			set[n] = struct{}{}
		}
	}
	for _, a := range actions {
		if a.IsHome && (a.Action == "move" || a.Action == "merge") {
			set[a.Name] = struct{}{}
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
