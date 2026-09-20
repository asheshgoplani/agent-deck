package session

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Profile store root selection (2026-09-19 / 2026-09-20 stray XDG store
// incidents).
//
// The profile stores (profiles/<name>/state.db) live under ONE data root: the
// XDG data dir ($XDG_DATA_HOME/agent-deck, default ~/.local/share/agent-deck)
// or the legacy ~/.agent-deck. Before this file, the root was picked by a bare
// marker stat: an XDG `profiles/` directory won as soon as it existed, however
// empty. Twice a foreign process (a sandboxed worker whose XDG_DATA_HOME still
// pointed at the real home) created an EMPTY ~/.local/share/agent-deck/
// profiles/personal/state.db next to the populated legacy store, and from that
// moment every new CLI process saw zero sessions while the running TUI kept
// the legacy store.
//
// The rule is deterministic across processes and never depends on row counts
// beyond "empty or not":
//
//	only one root holds profiles/        -> that root
//	neither holds profiles/              -> XDG (fresh install)
//	both hold profiles/:
//	  XDG profiles/.active-root present  -> XDG  (written by `migrate-paths`;
//	                                        the legacy copy is ignored)
//	  one root EMPTY, the other not      -> the non-empty root, WARN
//	                                        stray_xdg_store / stray_legacy_store
//	  one root UNREADABLE, other readable-> the readable root, WARN
//	  otherwise (no marker)              -> legacy (pre-migration default), WARN
//	                                        with the migrate-paths remedy
//
// "Empty" means every store under the root is readable and holds 0 session
// rows; an unreadable store is unknown and never counts as empty. A marker is
// only ever written by `agent-deck migrate-paths`, so a `profiles/` directory
// that a stray process created never carries one.
//
// The choice is emitted once per process: as `store_selected` (plus a WARN
// `stray_xdg_store` / `stray_legacy_store`) through LogStoreRootSelection once
// logging is up, and as one stderr WARNING line in CLI processes through
// WarnStoreRootDivergence. When both roots hold profiles the decision is
// memoized for the process lifetime, so a long-running TUI never flips roots
// mid-run.

// StoreRootReason values reported in `store_selected` and by doctor/health.
const (
	StoreRootReasonLegacyOnly       = "legacy_only"
	StoreRootReasonXDGOnly          = "xdg_only"
	StoreRootReasonDefaultNew       = "default_new"
	StoreRootReasonMarker           = "active_root_marker"
	StoreRootReasonStrayXDG         = "stray_xdg_store"
	StoreRootReasonStrayLegacy      = "stray_legacy_store"
	StoreRootReasonXDGUnreadable    = "xdg_store_unreadable"
	StoreRootReasonLegacyUnreadable = "legacy_store_unreadable"
	StoreRootReasonNoMarkerLegacy   = "no_marker_legacy"
)

// StoreRootMarkerName is the file `migrate-paths` writes inside the XDG
// profiles/ directory to pin the XDG root; it travels with the directory, so
// moving profiles/ aside also removes the pin.
const StoreRootMarkerName = ".active-root"

const (
	storeRootLegacyLabel         = "legacy"
	storeRootXDGLabel            = "xdg"
	storeRootStateDBName         = "state.db"
	storeRootLegacySessionsJSON  = "sessions.json"
	storeRootLegacyJSONMinLength = 3 // "[]" or "{}" plus newline is still empty
)

// StoreRootInfo describes one candidate data root.
type StoreRootInfo struct {
	Kind        string         `json:"kind"`         // "xdg" or "legacy"
	Path        string         `json:"path"`         // the data root (parent of profiles/)
	HasProfiles bool           `json:"has_profiles"` // profiles/ dir or legacy sessions.json present
	Marker      bool           `json:"marker"`       // profiles/.active-root present (XDG only)
	Counted     bool           `json:"counted"`      // Sessions/Profiles were read (false: stat only)
	Sessions    int            `json:"sessions"`     // readable session rows summed over every profile store
	Unreadable  int            `json:"unreadable"`   // profile stores that could not be opened or counted
	Profiles    map[string]int `json:"profiles"`     // profile name -> session rows (-1: unreadable)
}

// Empty reports a counted root whose every store is readable and holds no
// session rows: the shape of a stray store. An uncounted or unreadable root
// is never empty.
func (r StoreRootInfo) Empty() bool {
	return r.Counted && r.Sessions == 0 && r.Unreadable == 0
}

// Unknown reports a counted root with no readable rows and at least one
// unreadable store.
func (r StoreRootInfo) Unknown() bool {
	return r.Counted && r.Sessions == 0 && r.Unreadable > 0
}

// StoreRootSelection is the outcome of the rule above, for doctor/health.
type StoreRootSelection struct {
	Active    string        `json:"active"`    // the selected data root
	Kind      string        `json:"kind"`      // "xdg" or "legacy"
	Reason    string        `json:"reason"`    // one of the StoreRootReason* values
	Divergent bool          `json:"divergent"` // both roots hold profiles
	XDG       StoreRootInfo `json:"xdg"`
	Legacy    StoreRootInfo `json:"legacy"`
}

// Warning is the operator-facing line doctor/health print and CLI processes
// echo once to stderr for a divergent layout that needs the user's hand; ""
// when the layout is clean or pinned by the marker.
func (s StoreRootSelection) Warning() string {
	if !s.Divergent {
		return ""
	}
	xdgProfiles := filepath.Join(s.XDG.Path, ProfilesDirName)
	legacyProfiles := filepath.Join(s.Legacy.Path, ProfilesDirName)
	switch s.Reason {
	case StoreRootReasonMarker:
		return ""
	case StoreRootReasonStrayXDG:
		return fmt.Sprintf("stray empty XDG profile store at %s (legacy %s holds %s and stays active); run 'agent-deck migrate-paths' to move to the XDG layout (it sets the stray aside), or move %s aside to stay on legacy",
			xdgProfiles, s.Legacy.Path, formatStoreRootRows(s.Legacy), xdgProfiles)
	case StoreRootReasonStrayLegacy:
		return fmt.Sprintf("stray empty legacy profile store at %s (XDG %s holds %s and stays active); move %s aside",
			legacyProfiles, s.XDG.Path, formatStoreRootRows(s.XDG), legacyProfiles)
	case StoreRootReasonXDGUnreadable:
		return fmt.Sprintf("XDG profile store under %s is unreadable (%d store(s)); legacy %s (%d sessions) stays active until it is fixed",
			xdgProfiles, s.XDG.Unreadable, s.Legacy.Path, s.Legacy.Sessions)
	case StoreRootReasonLegacyUnreadable:
		return fmt.Sprintf("legacy profile store under %s is unreadable (%d store(s)); XDG %s (%d sessions) stays active until it is fixed",
			legacyProfiles, s.Legacy.Unreadable, s.XDG.Path, s.XDG.Sessions)
	}
	return fmt.Sprintf("profile stores exist under both %s (%s, active: no migration marker, legacy is the pre-migration default) and %s (%s, ignored); to stay on legacy move %s aside; if the XDG copy is the one you want, run 'agent-deck migrate-paths --force' (it keeps existing XDG files, copies missing ones from legacy and pins the XDG root)",
		s.Legacy.Path, formatStoreRootRows(s.Legacy), s.XDG.Path, formatStoreRootRows(s.XDG), xdgProfiles)
}

// Note is the doctor-only line for a layout that is divergent but resolved by
// the marker: the legacy copy is ignored and may be moved aside.
func (s StoreRootSelection) Note() string {
	if s.Reason != StoreRootReasonMarker {
		return ""
	}
	return fmt.Sprintf("migrated to the XDG root (%s); the legacy copy at %s (%s) is ignored and can be moved aside",
		filepath.Join(s.XDG.Path, ProfilesDirName, StoreRootMarkerName), filepath.Join(s.Legacy.Path, ProfilesDirName), formatStoreRootRows(s.Legacy))
}

func formatStoreRootRows(r StoreRootInfo) string {
	switch {
	case !r.Counted:
		return "not counted"
	case r.Unknown():
		return "an unreadable store"
	case r.Unreadable > 0:
		return fmt.Sprintf("%d sessions and %d unreadable store(s)", r.Sessions, r.Unreadable)
	}
	return fmt.Sprintf("%d sessions", r.Sessions)
}

var (
	storeRootMu        sync.Mutex
	storeRootDecided   = map[string]StoreRootSelection{} // xdg+"\x00"+legacy -> memoized divergent decision
	storeRootLogged    bool                              // store_selected emitted this process
	storeRootWarned    bool                              // stderr WARNING emitted this process
	storeRootSelectLog = logging.ForComponent(logging.CompStorage)
)

// ResetStoreRootSelection forgets memoized decisions and emit-once state.
// Commands that change the layout in-process (migrate-paths) and tests that
// reshape the layout under one HOME call it between phases.
func ResetStoreRootSelection() {
	storeRootMu.Lock()
	storeRootDecided = map[string]StoreRootSelection{}
	storeRootLogged = false
	storeRootWarned = false
	storeRootMu.Unlock()
}

// selectProfileDataRoot applies the rule and returns the data root that holds
// (or will hold) profiles/.
func selectProfileDataRoot() (string, error) {
	sel, err := SelectStoreRoot()
	if err != nil {
		return "", err
	}
	return sel.Active, nil
}

// SelectStoreRoot inspects both candidate roots and returns the selection.
// The path resolvers call it for the decision; rows are only counted when
// both roots hold profiles.
func SelectStoreRoot() (StoreRootSelection, error) {
	return selectStoreRoot(false)
}

// StoreRootReport is SelectStoreRoot with every root counted, for doctor and
// health: the session counts of a clean single-root layout are real, not 0.
func StoreRootReport() (StoreRootSelection, error) {
	return selectStoreRoot(true)
}

func selectStoreRoot(countAll bool) (StoreRootSelection, error) {
	xdgDir, err := agentpaths.DataDir()
	if err != nil {
		return StoreRootSelection{}, err
	}
	legacyDir, err := agentpaths.LegacyDir()
	if err != nil {
		return StoreRootSelection{}, err
	}
	key := xdgDir + "\x00" + legacyDir

	storeRootMu.Lock()
	sel, decided := storeRootDecided[key]
	storeRootMu.Unlock()
	if decided {
		return sel, nil
	}

	xdg, err := inspectStoreRoot(storeRootXDGLabel, xdgDir, countAll)
	if err != nil {
		return StoreRootSelection{}, err
	}
	legacy, err := inspectStoreRoot(storeRootLegacyLabel, legacyDir, countAll)
	if err != nil {
		return StoreRootSelection{}, err
	}

	sel = StoreRootSelection{XDG: xdg, Legacy: legacy}
	switch {
	case xdg.HasProfiles && legacy.HasProfiles:
		sel.Divergent = true
		if !countAll {
			// Only now is the (comparatively costly) row count needed.
			if sel.XDG, err = inspectStoreRoot(storeRootXDGLabel, xdgDir, true); err != nil {
				return StoreRootSelection{}, err
			}
			if sel.Legacy, err = inspectStoreRoot(storeRootLegacyLabel, legacyDir, true); err != nil {
				return StoreRootSelection{}, err
			}
		}
		switch {
		case sel.XDG.Marker:
			sel.Active, sel.Kind, sel.Reason = xdgDir, storeRootXDGLabel, StoreRootReasonMarker
		case sel.XDG.Empty() && !sel.Legacy.Empty():
			sel.Active, sel.Kind, sel.Reason = legacyDir, storeRootLegacyLabel, StoreRootReasonStrayXDG
		case sel.Legacy.Empty() && !sel.XDG.Empty():
			sel.Active, sel.Kind, sel.Reason = xdgDir, storeRootXDGLabel, StoreRootReasonStrayLegacy
		case sel.XDG.Unknown() && !sel.Legacy.Unknown():
			sel.Active, sel.Kind, sel.Reason = legacyDir, storeRootLegacyLabel, StoreRootReasonXDGUnreadable
		case sel.Legacy.Unknown() && !sel.XDG.Unknown():
			sel.Active, sel.Kind, sel.Reason = xdgDir, storeRootXDGLabel, StoreRootReasonLegacyUnreadable
		default:
			sel.Active, sel.Kind, sel.Reason = legacyDir, storeRootLegacyLabel, StoreRootReasonNoMarkerLegacy
		}
	case legacy.HasProfiles:
		sel.Active, sel.Kind, sel.Reason = legacyDir, storeRootLegacyLabel, StoreRootReasonLegacyOnly
	case xdg.HasProfiles:
		sel.Active, sel.Kind, sel.Reason = xdgDir, storeRootXDGLabel, StoreRootReasonXDGOnly
	default:
		sel.Active, sel.Kind, sel.Reason = xdgDir, storeRootXDGLabel, StoreRootReasonDefaultNew
	}

	if sel.Divergent {
		storeRootMu.Lock()
		if prior, ok := storeRootDecided[key]; ok {
			// Another goroutine decided first; keep the process consistent.
			storeRootMu.Unlock()
			return prior, nil
		}
		storeRootDecided[key] = sel
		storeRootMu.Unlock()
	}
	return sel, nil
}

// LogStoreRootSelection emits `store_selected` (and the stray/unreadable WARN)
// exactly once per process. It must run after logging.Init: the resolvers
// call SelectStoreRoot long before the log file is open, so the line is
// emitted from here, not from the decision.
func LogStoreRootSelection() {
	if !storeRootClaimOnce(&storeRootLogged) {
		return
	}
	sel, err := SelectStoreRoot()
	if err != nil {
		storeRootSelectLog.Error("store_select_failed", slog.String("error", err.Error()))
		return
	}
	storeRootSelectLog.Info("store_selected",
		slog.String("path", sel.Active),
		slog.String("reason", sel.Reason),
		slog.Bool("divergent", sel.Divergent),
		slog.String("xdg", sel.XDG.Path),
		slog.Int("xdg_sessions", sel.XDG.Sessions),
		slog.Int("xdg_unreadable", sel.XDG.Unreadable),
		slog.String("legacy", sel.Legacy.Path),
		slog.Int("legacy_sessions", sel.Legacy.Sessions),
		slog.Int("legacy_unreadable", sel.Legacy.Unreadable),
	)
	if warning := sel.Warning(); warning != "" {
		inactive := sel.XDG
		if sel.Kind == storeRootXDGLabel {
			inactive = sel.Legacy
		}
		storeRootSelectLog.Warn(sel.Reason,
			slog.String("path", filepath.Join(inactive.Path, ProfilesDirName)),
			slog.String("active", sel.Active),
			slog.String("warning", warning),
		)
	}
}

// WarnStoreRootDivergence writes the layout WARNING once per process to w
// (stderr in CLI processes, which never open the debug log). Silent for a
// clean or marker-pinned layout.
func WarnStoreRootDivergence(w io.Writer) {
	if !storeRootClaimOnce(&storeRootWarned) {
		return
	}
	sel, err := SelectStoreRoot()
	if err != nil {
		return
	}
	if warning := sel.Warning(); warning != "" {
		fmt.Fprintf(w, "WARNING: %s\n", warning)
	}
}

// storeRootClaimOnce flips an emit-once flag under the lock and reports
// whether this caller is the first to do so in the process.
func storeRootClaimOnce(done *bool) bool {
	storeRootMu.Lock()
	defer storeRootMu.Unlock()
	if *done {
		return false
	}
	*done = true
	return true
}

// MarkXDGStoreActive writes profiles/.active-root under the XDG data dir so
// every later process resolves to the XDG root regardless of what the legacy
// copy holds. It is the last step of `migrate-paths`; without an XDG
// profiles/ directory there is nothing to pin and it returns "", nil.
func MarkXDGStoreActive() (string, error) {
	xdgDir, err := agentpaths.DataDir()
	if err != nil {
		return "", err
	}
	profilesDir := filepath.Join(xdgDir, ProfilesDirName)
	if ok, err := pathExists(profilesDir); err != nil || !ok {
		return "", err
	}
	marker := filepath.Join(profilesDir, StoreRootMarkerName)
	if err := os.WriteFile(marker, []byte(storeRootXDGLabel+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", marker, err)
	}
	ResetStoreRootSelection()
	return marker, nil
}

// SetAsideStrayXDGStore renames an XDG profiles/ directory that the rule
// classified as a stray (every store readable and empty beside a populated
// legacy root) to profiles.stray-<timestamp>, so `migrate-paths` can copy the
// legacy stores into a clean XDG layout. It returns the new path, or "" when
// the layout is not the stray shape. Nothing is deleted.
func SetAsideStrayXDGStore() (string, error) {
	sel, err := StoreRootReport()
	if err != nil {
		return "", err
	}
	if sel.Reason != StoreRootReasonStrayXDG {
		return "", nil
	}
	profilesDir := filepath.Join(sel.XDG.Path, ProfilesDirName)
	aside := profilesDir + ".stray-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(profilesDir, aside); err != nil {
		return "", fmt.Errorf("set aside %s: %w", profilesDir, err)
	}
	storeRootSelectLog.Warn("stray_xdg_store_set_aside",
		slog.String("path", profilesDir),
		slog.String("moved_to", aside),
	)
	ResetStoreRootSelection()
	return aside, nil
}

// inspectStoreRoot stats one root; with countRows it also opens every profile
// store read-only (no file is created or touched) and sums the session rows.
func inspectStoreRoot(kind, root string, countRows bool) (StoreRootInfo, error) {
	info := StoreRootInfo{Kind: kind, Path: root, Profiles: map[string]int{}}
	profilesDir := filepath.Join(root, ProfilesDirName)
	hasProfilesDir, err := pathExists(profilesDir)
	if err != nil {
		return info, err
	}
	hasLegacyJSON, err := pathExists(filepath.Join(root, storeRootLegacySessionsJSON))
	if err != nil {
		return info, err
	}
	info.HasProfiles = hasProfilesDir || hasLegacyJSON
	if kind == storeRootXDGLabel && hasProfilesDir {
		if info.Marker, err = pathExists(filepath.Join(profilesDir, StoreRootMarkerName)); err != nil {
			return info, err
		}
	}
	if !countRows {
		return info, nil
	}
	info.Counted = true
	if !hasProfilesDir {
		return info, nil
	}
	entries, err := os.ReadDir(profilesDir)
	if err != nil {
		return info, fmt.Errorf("read %s: %w", profilesDir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(profilesDir, entry.Name())
		n, ok := countProfileSessions(dir)
		if !ok {
			continue // no store in this directory
		}
		info.Profiles[entry.Name()] = n
		if n < 0 {
			info.Unreadable++
		} else {
			info.Sessions += n
		}
	}
	return info, nil
}

// countProfileSessions returns the session rows of one profile directory and
// whether it holds a store at all. A pre-SQLite sessions.json that is not
// empty counts as one session (it auto-migrates on open); an unreadable
// state.db reports -1 so doctor shows it, and counts as populated.
func countProfileSessions(dir string) (int, bool) {
	dbPath := filepath.Join(dir, storeRootStateDBName)
	if _, err := os.Stat(dbPath); err == nil {
		db, err := statedb.OpenReadOnlyLive(dbPath)
		if err != nil {
			return -1, true
		}
		defer func() { _ = db.Close() }()
		n, err := db.InstanceCount()
		if err != nil {
			return -1, true
		}
		return n, true
	}
	if st, err := os.Stat(filepath.Join(dir, storeRootLegacySessionsJSON)); err == nil {
		if st.Size() >= storeRootLegacyJSONMinLength {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// otherStoreRoot returns the candidate root that is NOT active, for the
// second-store guard in NewStorageWithProfile.
func otherStoreRoot(active string) (string, error) {
	xdgDir, err := agentpaths.DataDir()
	if err != nil {
		return "", err
	}
	legacyDir, err := agentpaths.LegacyDir()
	if err != nil {
		return "", err
	}
	if filepath.Clean(active) == filepath.Clean(legacyDir) {
		return xdgDir, nil
	}
	return legacyDir, nil
}

// ErrStoreExistsElsewhere is returned when a profile store would be CREATED
// under the active root while the same profile already has a store under the
// other root. A second store for one profile is never created implicitly;
// moving the directory (or `agent-deck migrate-paths --force`) is the
// explicit way to consolidate.
var ErrStoreExistsElsewhere = errors.New("profile store exists under the other data root")

// guardNewProfileStore is called before a state.db is created. It refuses
// when the same profile already has a store under the other root and logs
// `store_created` (both roots named) when a genuinely new store is about to
// be made, so the next stray store has a traceable origin.
func guardNewProfileStore(profile, profileDir string) error {
	dbPath := filepath.Join(profileDir, storeRootStateDBName)
	if _, err := os.Stat(dbPath); err == nil {
		return nil // opening an existing store
	}
	if _, err := os.Stat(filepath.Join(profileDir, storeRootLegacySessionsJSON)); err == nil {
		return nil // pre-SQLite profile about to auto-migrate in place
	}
	active := filepath.Dir(filepath.Dir(profileDir))
	other, err := otherStoreRoot(active)
	if err != nil {
		return err
	}
	otherDir := filepath.Join(other, ProfilesDirName, filepath.Base(profileDir))
	if _, ok := countProfileSessions(otherDir); ok {
		storeRootSelectLog.Error("store_create_refused",
			slog.String("profile", profile),
			slog.String("path", dbPath),
			slog.String("existing", filepath.Join(otherDir, storeRootStateDBName)),
		)
		return fmt.Errorf("%w: profile %q already has a store at %s; refusing to create %s (move that profile directory under %s, or run 'agent-deck migrate-paths --force' to consolidate under the XDG root)",
			ErrStoreExistsElsewhere, profile, otherDir, dbPath, filepath.Dir(profileDir))
	}
	storeRootSelectLog.Info("store_created",
		slog.String("profile", profile),
		slog.String("path", dbPath),
		slog.String("other_root", other),
		slog.String("argv0", filepath.Base(os.Args[0])),
	)
	return nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat %q: %w", path, err)
}
