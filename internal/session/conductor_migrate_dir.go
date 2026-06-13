package session

import (
	"bytes"
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
	Name   string // entry name (conductor name or base file)
	IsHome bool   // conductor home (dir with meta.json) vs base file/symlink
	// Action is one of:
	//   "move"           dest absent → copy then remove source
	//   "merge"          dest exists + --force, safe → merge (dest wins per-file)
	//   "skip-exists"    dest exists, no --force → left in place (blocks the migration)
	//   "skip-transient" runtime log/temp → ignored (regenerated at the new base)
	//   "reject-conflict" dest exists + --force, but merging would clobber the
	//                     source's durable meta.json → refused (blocks the migration)
	Action   string
	Conflict bool   // a destination already existed (preserved)
	Reason   string // why a reject-conflict was refused (empty otherwise)
}

// ConductorDirMigrateResult summarizes a relocation for reporting.
type ConductorDirMigrateResult struct {
	DryRun        bool
	Source        string
	Target        string
	Actions       []ConductorDirMigrateAction
	Conductors    []string // conductor homes present in target afterward
	ConfigWritten bool
	// Refused is true when the migration was aborted because the plan was not
	// clean — a home whose destination exists without --force, or a conductor
	// whose durable record would be clobbered. When Refused, nothing was mutated
	// and [conductor].dir was NOT repointed (the flip is all-or-nothing).
	Refused  bool
	Blockers []string // human-readable reasons the migration was refused
	// SourceRemovalWarnings records sources that could not be removed AFTER the
	// config was already committed (non-fatal: the durable record exists at the
	// committed target; the leftover is a harmless duplicate at the old base).
	SourceRemovalWarnings []string
	BridgeReinstalled     bool
}

// migratePlanEntry is the internal, mutation-free classification of one
// base-level source entry plus the absolute paths the copy/remove phases act on.
type migratePlanEntry struct {
	action  ConductorDirMigrateAction
	srcPath string
	dstPath string
}

// MigrateConductorDir relocates the conductor base from its current/source
// location to Target as one explicit, transactional relocation. The mutating
// path follows copy → verify → commit-config → remove-source so a failure before
// the config commit leaves every source intact (fully recoverable) and a failure
// after leaves the verified durable record at the committed target — there is
// never a window where a conductor's only meta.json exists at a half-applied
// target. The whole operation runs under the conductor base lock so no
// concurrent setup/meta write can be stranded or deleted.
//
//  1. Plan: classify every base-level entry (move/merge/skip/reject) with NO
//     mutation. If any home cannot move cleanly (destination exists without
//     --force, or a --force merge would clobber the source's durable meta.json),
//     refuse the WHOLE migration and mutate nothing — the [conductor].dir flip is
//     all-or-nothing.
//  2. Copy every entry source→target (non-destructive, no source removal yet).
//  3. Verify each migrated home's meta.json landed readable at the target.
//  4. Commit [conductor].dir = target.
//  5. Remove the migrated sources.
//  6. Reconcile path-baked artifacts: re-render heartbeat.sh per conductor and
//     reinstall base bridge.py.
//
// Daemon reloads (launchctl/systemctl) are deliberately NOT done here — they
// belong to the CLI handler so this function stays unit-testable without a
// service manager. The returned Conductors list is the reconcile/reload set.
//
// A dry-run (Apply=false) builds and reports the full plan — including every
// home it would skip, overwrite, or reject — and changes nothing.
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

	// Finding #4: reject source/target overlap up front. Containment in either
	// direction self-copies (target inside source) or merges destructively
	// (source inside target). Only the exact no-op is allowed through.
	if err := validateMigratePaths(source, target); err != nil {
		return nil, err
	}

	res := &ConductorDirMigrateResult{DryRun: !opts.Apply, Source: source, Target: target}
	noop := sameConductorPath(source, target)

	// Finding #7: a dry-run builds the full plan and reports every move/merge/
	// skip/reject BEFORE touching anything.
	if res.DryRun {
		var plan []migratePlanEntry
		if !noop {
			plan, err = planMigration(source, target, opts)
			if err != nil {
				return nil, err
			}
		}
		res.Actions = actionsOf(plan)
		res.Blockers = planBlockers(plan)
		res.Refused = len(res.Blockers) > 0
		res.Conductors = plannedTargetConductors(target, res.Actions)
		return res, nil
	}

	// Finding #5: hold the conductor base lock across the ENTIRE apply
	// (enumerate→copy→verify→commit→remove) so no concurrent setup/meta write
	// interleaves and gets stranded at the old base or deleted with the source.
	lock, err := acquireConductorBaseLock()
	if err != nil {
		return res, err
	}
	defer lock.release()

	// Re-plan under the lock so the plan reflects state no concurrent writer can
	// change out from under us.
	var plan []migratePlanEntry
	if !noop {
		plan, err = planMigration(source, target, opts)
		if err != nil {
			return res, err
		}
	}
	res.Actions = actionsOf(plan)

	// Findings #1 + #2: refuse the WHOLE migration (mutate nothing) if any entry
	// can't move cleanly. We never repoint [conductor].dir while a home is left
	// behind, and we never merge-then-delete a source whose durable record differs.
	if blockers := planBlockers(plan); len(blockers) > 0 {
		res.Refused = true
		res.Blockers = blockers
		return res, nil
	}

	// Finding #3: copy → verify → commit-config → remove-source.
	if !noop {
		// 1. COPY every entry source→target (non-destructive; no source removal yet).
		if err := copyPlan(plan); err != nil {
			return res, fmt.Errorf("copy conductor homes to target: %w", err)
		}
		// 2. VERIFY each migrated home's meta.json landed readable at the target
		//    BEFORE we commit the config or remove any source.
		if err := verifyPlan(plan, target); err != nil {
			return res, fmt.Errorf("verify migrated conductor homes: %w", err)
		}
		// Reflect the per-file merge conflicts discovered during copy.
		res.Actions = actionsOf(plan)
	}

	// 3. COMMIT the config — only after every copy is verified. A failure here
	//    still leaves all sources intact (nothing removed yet) → recoverable.
	cfg, err := LoadUserConfig()
	if err != nil {
		return res, fmt.Errorf("load user config: %w", err)
	}
	cfg.Conductor.Dir = target
	if err := SaveUserConfig(cfg); err != nil {
		return res, fmt.Errorf("write [conductor].dir: %w", err)
	}
	res.ConfigWritten = true

	// 4. REMOVE the migrated sources — only after the config is committed. A
	//    failure here is non-fatal: the durable records already exist (verified)
	//    at the committed target; a leftover source is a harmless duplicate.
	if !noop {
		if warns := removePlanSources(plan); len(warns) > 0 {
			res.SourceRemovalWarnings = warns
			for _, w := range warns {
				sessionLog.Warn("conductor_migrate_dir_source_removal_failed", slog.String("detail", w))
			}
		}
	}

	// 5. Reconcile path-baked artifacts against the now-resolved target.
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

// validateMigratePaths rejects a source/target pair that overlaps (finding #4):
// the exact no-op is allowed, but containment in either direction is not. A
// target inside the source tree would self-copy as the walk descends into it; a
// source inside the target tree would merge the target into itself destructively.
func validateMigratePaths(source, target string) error {
	sAbs, err := filepath.Abs(filepath.Clean(source))
	if err != nil {
		return fmt.Errorf("resolve source path %q: %w", source, err)
	}
	tAbs, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return fmt.Errorf("resolve target path %q: %w", target, err)
	}
	if sAbs == tAbs {
		return nil // exact no-op
	}
	if pathContains(sAbs, tAbs) {
		return fmt.Errorf("target %q is inside the source conductor dir %q; choose a target outside the source tree", target, source)
	}
	if pathContains(tAbs, sAbs) {
		return fmt.Errorf("source %q is inside the target conductor dir %q; choose a source outside the target tree", source, target)
	}
	return nil
}

// pathContains reports whether child is strictly nested under parent. Both must
// be cleaned absolute paths. Equal paths return false (the caller handles the
// no-op separately).
func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return false
	}
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}

// conductorMetaConflict reports whether a --force merge of a conductor home
// present in BOTH source and target would clobber the source's durable record
// (finding #1). MergeTree is destination-wins, so when both homes carry a
// meta.json that differ, the destination's wins and the source's only copy is
// then deleted — destroying the source conductor's identity. Such a conductor is
// rejected (not merged-then-deleted). When the destination has no meta.json, the
// merge brings the source's over with no conflict, so it is safe.
func conductorMetaConflict(srcPath, dstPath string) (bool, string) {
	dstMeta := filepath.Join(dstPath, "meta.json")
	if _, err := os.Stat(dstMeta); err != nil {
		// No destination meta.json to lose to — the source's copies over cleanly.
		return false, ""
	}
	srcBytes, srcErr := os.ReadFile(filepath.Join(srcPath, "meta.json"))
	dstBytes, dstErr := os.ReadFile(dstMeta)
	if srcErr != nil || dstErr != nil {
		// Can't prove the records match → refuse rather than risk a silent loss.
		return true, "meta.json unreadable on source or destination"
	}
	if !bytes.Equal(srcBytes, dstBytes) {
		return true, "meta.json differs between source and destination"
	}
	return false, ""
}

// planMigration enumerates source entries and classifies each with NO mutation.
// It is shared by the dry-run report and the apply path so both see the exact
// same plan.
func planMigration(source, target string, opts ConductorDirMigrateOptions) ([]migratePlanEntry, error) {
	entries, err := os.ReadDir(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no source homes to move
		}
		return nil, fmt.Errorf("read source conductor dir %q: %w", source, err)
	}

	var plan []migratePlanEntry
	for _, entry := range entries {
		name := entry.Name()
		if isTransientConductorArtifact(name) {
			plan = append(plan, migratePlanEntry{action: ConductorDirMigrateAction{Name: name, Action: "skip-transient"}})
			continue
		}
		srcPath := filepath.Join(source, name)
		dstPath := filepath.Join(target, name)
		act := ConductorDirMigrateAction{Name: name, IsHome: isConductorHome(srcPath)}

		dstExists, err := pathExistsLocal(dstPath)
		if err != nil {
			return nil, fmt.Errorf("stat destination %q: %w", dstPath, err)
		}

		switch {
		case !dstExists:
			act.Action = "move"
		case !opts.Force:
			act.Action = "skip-exists"
			act.Conflict = true
		default: // --force and the destination already exists
			if act.IsHome {
				if conflict, reason := conductorMetaConflict(srcPath, dstPath); conflict {
					act.Action = "reject-conflict"
					act.Conflict = true
					act.Reason = reason
					break
				}
			}
			act.Action = "merge"
			act.Conflict = true // dest existed; the precise per-file result is set during copy
		}
		plan = append(plan, migratePlanEntry{action: act, srcPath: srcPath, dstPath: dstPath})
	}
	return plan, nil
}

// actionsOf projects the reportable actions out of a plan.
func actionsOf(plan []migratePlanEntry) []ConductorDirMigrateAction {
	out := make([]ConductorDirMigrateAction, 0, len(plan))
	for _, e := range plan {
		out = append(out, e.action)
	}
	return out
}

// planBlockers returns a human-readable reason for every entry that prevents a
// clean migration (skip-exists or reject-conflict). A non-empty result means the
// apply must refuse the whole migration and the config flip must not happen.
func planBlockers(plan []migratePlanEntry) []string {
	var blockers []string
	for _, e := range plan {
		label := e.action.Name
		if e.action.IsHome {
			label += " (conductor)"
		}
		switch e.action.Action {
		case "skip-exists":
			blockers = append(blockers, fmt.Sprintf(
				"%s: destination already exists — re-run with --force to merge, or move the destination aside", label))
		case "reject-conflict":
			blockers = append(blockers, fmt.Sprintf(
				"%s: %s — refusing to overwrite the source's durable record; reconcile the two homes manually", label, e.action.Reason))
		}
	}
	return blockers
}

// copyPlan copies every move/merge entry source→target WITHOUT removing any
// source. move entries use CopyTree (destination absent); merge entries use
// MergeTree (destination present, destination wins per-file). A TOCTOU
// destination that appeared since planning surfaces as an error and aborts the
// migration before the config commit, leaving all sources intact.
func copyPlan(plan []migratePlanEntry) error {
	for i := range plan {
		e := &plan[i]
		switch e.action.Action {
		case "move":
			if err := agentpaths.CopyTree(e.srcPath, e.dstPath); err != nil {
				return fmt.Errorf("copy %q -> %q: %w", e.srcPath, e.dstPath, err)
			}
		case "merge":
			conflicted, err := agentpaths.MergeTree(e.srcPath, e.dstPath)
			if err != nil {
				return fmt.Errorf("merge %q -> %q: %w", e.srcPath, e.dstPath, err)
			}
			e.action.Conflict = conflicted
		}
	}
	return nil
}

// verifyPlan confirms every migrated conductor home has a readable, non-empty
// meta.json at the target before the config is committed or any source removed.
func verifyPlan(plan []migratePlanEntry, target string) error {
	for _, e := range plan {
		if !e.action.IsHome {
			continue
		}
		if e.action.Action != "move" && e.action.Action != "merge" {
			continue
		}
		metaPath := filepath.Join(target, e.action.Name, "meta.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			return fmt.Errorf("conductor %q: meta.json not readable at target %q: %w", e.action.Name, metaPath, err)
		}
		if len(data) == 0 {
			return fmt.Errorf("conductor %q: meta.json empty at target %q", e.action.Name, metaPath)
		}
	}
	return nil
}

// removePlanSources removes the source of every copied (move/merge) entry. It is
// called ONLY after the config is committed; any failure is returned as a
// non-fatal warning (the durable record already exists at the committed target).
func removePlanSources(plan []migratePlanEntry) []string {
	var warns []string
	for _, e := range plan {
		if e.action.Action != "move" && e.action.Action != "merge" {
			continue
		}
		if err := os.RemoveAll(e.srcPath); err != nil {
			warns = append(warns, fmt.Sprintf("%s: failed to remove migrated source %q: %v", e.action.Name, e.srcPath, err))
		}
	}
	return warns
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
