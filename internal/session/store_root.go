package session

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

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
// The rule now is deterministic and content-aware:
//
//   - only one root holds profiles         -> that root
//   - neither holds profiles               -> XDG (a fresh install)
//   - both hold profiles                   -> the root with MORE session rows;
//     an equal count (including 0/0) keeps the XDG default. An empty XDG store
//     beside a populated legacy one is a stray: the legacy root is used and a
//     WARN `stray_xdg_store` names the path.
//
// The choice is logged once per process as `store_selected`. When both roots
// hold profiles the decision is memoized for the process lifetime, so a
// long-running TUI never flips roots mid-run and every process that started in
// the same state agrees.

// StoreRootReason values reported in `store_selected` and by doctor/health.
const (
	StoreRootReasonLegacyOnly   = "legacy_only"
	StoreRootReasonXDGOnly      = "xdg_only"
	StoreRootReasonDefaultNew   = "default_new"
	StoreRootReasonStrayXDG     = "stray_xdg_store"
	StoreRootReasonLegacyLarger = "legacy_more_sessions"
	StoreRootReasonXDGLarger    = "xdg_more_sessions"
	StoreRootReasonTiePreferXDG = "tie_prefer_xdg"
)

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
	Sessions    int            `json:"sessions"`     // session rows summed over every profile store
	Profiles    map[string]int `json:"profiles"`     // profile name -> session rows (-1: unreadable)
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

// Warning is the operator-facing line doctor/health print for a divergent
// layout, "" when the layout is clean.
func (s StoreRootSelection) Warning() string {
	if !s.Divergent {
		return ""
	}
	inactive := s.XDG
	if s.Kind == storeRootXDGLabel {
		inactive = s.Legacy
	}
	if s.Reason == StoreRootReasonStrayXDG {
		return fmt.Sprintf("stray empty XDG profile store at %s (legacy %s holds %d sessions and stays active); move the stray profiles/ aside or run 'agent-deck migrate-paths'",
			filepath.Join(s.XDG.Path, ProfilesDirName), s.Legacy.Path, s.Legacy.Sessions)
	}
	return fmt.Sprintf("profile stores exist under both %s (%d sessions, active) and %s (%d sessions, ignored); consolidate with 'agent-deck migrate-paths' or move the inactive profiles/ aside",
		s.Active, s.activeSessions(), inactive.Path, inactive.Sessions)
}

func (s StoreRootSelection) activeSessions() int {
	if s.Kind == storeRootXDGLabel {
		return s.XDG.Sessions
	}
	return s.Legacy.Sessions
}

var (
	storeRootMu        sync.Mutex
	storeRootDecided   = map[string]StoreRootSelection{} // xdg+"\x00"+legacy -> memoized divergent decision
	storeRootLogged    = map[string]bool{}               // same key -> store_selected already logged
	storeRootSelectLog = logging.ForComponent(logging.CompStorage)
)

// ResetStoreRootSelection forgets memoized decisions and log-once state.
// Commands that change the layout in-process (migrate-paths) and tests that
// reshape the layout under one HOME call it between phases.
func ResetStoreRootSelection() {
	storeRootMu.Lock()
	storeRootDecided = map[string]StoreRootSelection{}
	storeRootLogged = map[string]bool{}
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

// SelectStoreRoot inspects both candidate roots and returns the selection,
// logging it once per process. Doctor and health call it for the report; the
// path resolvers call it for the decision.
func SelectStoreRoot() (StoreRootSelection, error) {
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
	if sel, ok := storeRootDecided[key]; ok {
		storeRootMu.Unlock()
		return sel, nil
	}
	storeRootMu.Unlock()

	xdg, err := inspectStoreRoot(storeRootXDGLabel, xdgDir, false)
	if err != nil {
		return StoreRootSelection{}, err
	}
	legacy, err := inspectStoreRoot(storeRootLegacyLabel, legacyDir, false)
	if err != nil {
		return StoreRootSelection{}, err
	}

	sel := StoreRootSelection{XDG: xdg, Legacy: legacy}
	switch {
	case xdg.HasProfiles && legacy.HasProfiles:
		sel.Divergent = true
		// Only now is the (comparatively costly) row count needed.
		if sel.XDG, err = inspectStoreRoot(storeRootXDGLabel, xdgDir, true); err != nil {
			return StoreRootSelection{}, err
		}
		if sel.Legacy, err = inspectStoreRoot(storeRootLegacyLabel, legacyDir, true); err != nil {
			return StoreRootSelection{}, err
		}
		switch {
		case sel.XDG.Sessions == 0 && sel.Legacy.Sessions > 0:
			sel.Active, sel.Kind, sel.Reason = legacyDir, storeRootLegacyLabel, StoreRootReasonStrayXDG
		case sel.Legacy.Sessions > sel.XDG.Sessions:
			sel.Active, sel.Kind, sel.Reason = legacyDir, storeRootLegacyLabel, StoreRootReasonLegacyLarger
		case sel.XDG.Sessions > sel.Legacy.Sessions:
			sel.Active, sel.Kind, sel.Reason = xdgDir, storeRootXDGLabel, StoreRootReasonXDGLarger
		default:
			sel.Active, sel.Kind, sel.Reason = xdgDir, storeRootXDGLabel, StoreRootReasonTiePreferXDG
		}
	case legacy.HasProfiles:
		sel.Active, sel.Kind, sel.Reason = legacyDir, storeRootLegacyLabel, StoreRootReasonLegacyOnly
	case xdg.HasProfiles:
		sel.Active, sel.Kind, sel.Reason = xdgDir, storeRootXDGLabel, StoreRootReasonXDGOnly
	default:
		sel.Active, sel.Kind, sel.Reason = xdgDir, storeRootXDGLabel, StoreRootReasonDefaultNew
	}

	storeRootMu.Lock()
	if sel.Divergent {
		if prior, ok := storeRootDecided[key]; ok {
			// Another goroutine decided first; keep the process consistent.
			storeRootMu.Unlock()
			return prior, nil
		}
		storeRootDecided[key] = sel
	}
	logIt := !storeRootLogged[key]
	storeRootLogged[key] = true
	storeRootMu.Unlock()

	if logIt {
		storeRootSelectLog.Info("store_selected",
			slog.String("path", sel.Active),
			slog.String("reason", sel.Reason),
			slog.String("xdg", xdgDir),
			slog.Int("xdg_sessions", sel.XDG.Sessions),
			slog.String("legacy", legacyDir),
			slog.Int("legacy_sessions", sel.Legacy.Sessions),
		)
		if sel.Reason == StoreRootReasonStrayXDG {
			storeRootSelectLog.Warn("stray_xdg_store",
				slog.String("path", filepath.Join(xdgDir, ProfilesDirName)),
				slog.String("active", sel.Active),
				slog.Int("legacy_sessions", sel.Legacy.Sessions),
			)
		}
	}
	return sel, nil
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
	if !countRows || !hasProfilesDir {
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
		if n > 0 {
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
// `agent-deck migrate-paths` is the explicit way to move it.
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
		return fmt.Errorf("%w: profile %q already has a store at %s; refusing to create %s (run 'agent-deck migrate-paths' to move it, or move the other profiles/ aside)",
			ErrStoreExistsElsewhere, profile, otherDir, dbPath)
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
