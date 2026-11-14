package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"golang.org/x/sync/errgroup"
)

var hookStatusLog = logging.ForComponent(logging.CompWeb)

type storageLoader interface {
	LoadWithGroups() ([]*session.Instance, []*session.GroupData, error)
	GetUpdatedAt() (time.Time, error)
	Close() error
	// Profile returns the profile name actually opened — which, per the
	// #1790 guard inside NewStorageWithProfile, may differ from the raw
	// name requested (e.g. an unrecognized CLAUDE_CONFIG_DIR-inferred name
	// falls back to the configured default). See NewSessionDataService.
	Profile() string
}

type storageOpener func(profile string) (storageLoader, error)

// SessionDataService loads profile session data and transforms it into web-friendly DTOs.
type SessionDataService struct {
	// profileMu guards profile — LoadMenuSnapshot/LoadArchivedMenuSnapshot
	// can be called concurrently (HTTP handlers + the push-service poller
	// share one SessionDataService), and resolveAndOpenStorage writes the
	// resolved name back to profile on every call (#1822 F3 follow-up:
	// Codex found this was an unsynchronized data race).
	profileMu        sync.Mutex
	profile          string
	openStorage      storageOpener
	now              func() time.Time
	refreshLiveState bool
	loadHookStatuses func() map[string]*session.HookStatus
	// Test seams for the menu status pass; zero values mean the defaults
	// (refreshMenuTmuxCaches, menuCacheRefreshAge).
	refreshTmuxCaches func()
	cacheRefreshAge   time.Duration
}

// NewSessionDataService creates a SessionDataService for a profile.
//
// #1790/#1822 F3: profile is stored RAW (possibly empty/possibly a
// CLAUDE_CONFIG_DIR-inferred name) rather than pre-resolved via
// GetEffectiveProfile. Pre-resolving here and handing the concrete result to
// openStorage/NewStorageWithProfile would make it indistinguishable from an
// explicit -p selection, bypassing NewStorageWithProfile's own #1790 guard a
// second hop downstream — every caller of NewSessionDataService (web server
// bootstrap, the standalone test-server binary, internal/web.NewServer's
// fallback) inherited that hole. Real resolution, including the guard,
// happens once inside the shared storage layer when the profile is actually
// opened; s.profile is synced to the profile that was actually opened
// afterwards (see resolveAndOpenStorage), so Profile()/BuildMenuSnapshot
// still report the true effective name rather than the raw input.
func NewSessionDataService(profile string) *SessionDataService {
	return &SessionDataService{
		profile:          profile,
		openStorage:      defaultStorageOpener,
		now:              time.Now,
		refreshLiveState: true,
		loadHookStatuses: defaultLoadHookStatuses,
	}
}

func defaultStorageOpener(profile string) (storageLoader, error) {
	return session.NewStorageWithProfile(profile)
}

// Profile returns the effective profile this service reads from. Before the
// first successful Load*MenuSnapshot call this may be the raw (possibly
// empty/inferred) value passed to NewSessionDataService rather than the
// guard-resolved effective name — see NewSessionDataService.
func (s *SessionDataService) Profile() string {
	s.profileMu.Lock()
	defer s.profileMu.Unlock()
	return s.profile
}

// RefreshesLiveState reports that snapshots include tmux-derived process state,
// which can change without a persisted menu-data revision.
func (s *SessionDataService) RefreshesLiveState() bool {
	return s != nil && s.refreshLiveState
}

// resolveAndOpenStorage opens storage for the raw profile this service was
// constructed with (see NewSessionDataService) and syncs s.profile to
// whatever profile was actually opened, so callers built on Profile() and
// BuildMenuSnapshot's profile label observe the true effective name (post
// #1790 guard/fallback) rather than the raw input.
//
// profileMu guards the read-then-write below: LoadMenuSnapshot and
// LoadArchivedMenuSnapshot can run concurrently against one
// SessionDataService (HTTP handlers + the push-service poller share one
// instance), and without a lock two concurrent calls racing through this
// read-modify-write would trip Go's race detector and could interleave a
// stale profile into an error message or BuildMenuSnapshot call.
func (s *SessionDataService) resolveAndOpenStorage() (storageLoader, string, error) {
	s.profileMu.Lock()
	requested := s.profile
	s.profileMu.Unlock()

	storage, err := s.openStorage(requested)
	if err != nil {
		return nil, "", fmt.Errorf("open storage for profile %q: %w", requested, err)
	}
	resolved := requested
	if opened := storage.Profile(); opened != "" {
		resolved = opened
		s.profileMu.Lock()
		s.profile = opened
		s.profileMu.Unlock()
	}
	return storage, resolved, nil
}

// LoadMenuSnapshot loads sessions/groups and returns a deterministic flattened menu DTO.
func (s *SessionDataService) LoadMenuSnapshot() (*MenuSnapshot, error) {
	if s == nil {
		return nil, fmt.Errorf("session data service is nil")
	}
	if s.openStorage == nil {
		return nil, fmt.Errorf("storage opener is not configured")
	}
	if s.now == nil {
		s.now = time.Now
	}

	storage, profile, err := s.resolveAndOpenStorage()
	if err != nil {
		return nil, err
	}
	defer func() { _ = storage.Close() }()

	instances, groupsData, err := storage.LoadWithGroups()
	if err != nil {
		return nil, fmt.Errorf("load sessions for profile %q: %w", profile, err)
	}
	if s.refreshLiveState {
		s.refreshStatuses(instances)
	}

	active := session.FilterInstancesByArchive(instances, false)
	return BuildMenuSnapshot(profile, active, groupsData, s.now()), nil
}

// MenuDataRevision returns the storage revision that changes after a persisted mutation.
func (s *SessionDataService) MenuDataRevision() (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("session data service is nil")
	}
	if s.openStorage == nil {
		return 0, fmt.Errorf("storage opener is not configured")
	}

	storage, profile, err := s.resolveAndOpenStorage()
	if err != nil {
		return 0, err
	}
	defer func() { _ = storage.Close() }()

	updatedAt, err := storage.GetUpdatedAt()
	if err != nil {
		return 0, fmt.Errorf("load menu data revision for profile %q: %w", profile, err)
	}
	if updatedAt.IsZero() {
		return 0, nil
	}
	return updatedAt.UnixNano(), nil
}

// LoadArchivedMenuSnapshot returns a menu containing only archived sessions.
func (s *SessionDataService) LoadArchivedMenuSnapshot() (*MenuSnapshot, error) {
	if s == nil {
		return nil, fmt.Errorf("session data service is nil")
	}
	if s.openStorage == nil {
		return nil, fmt.Errorf("storage opener is not configured")
	}
	if s.now == nil {
		s.now = time.Now
	}

	storage, profile, err := s.resolveAndOpenStorage()
	if err != nil {
		return nil, err
	}
	defer func() { _ = storage.Close() }()

	instances, groupsData, err := storage.LoadWithGroups()
	if err != nil {
		return nil, fmt.Errorf("load sessions for profile %q: %w", profile, err)
	}
	archived := session.FilterInstancesByArchive(instances, true)
	return BuildMenuSnapshot(profile, archived, groupsData, s.now()), nil
}

type rawHookStatus struct {
	Status                   string `json:"status"`
	SessionID                string `json:"session_id"`
	Event                    string `json:"event"`
	Timestamp                int64  `json:"ts"`
	CodexStartedGeneration   string `json:"codex_started_generation"`
	CodexCompletedGeneration string `json:"codex_completed_generation"`
	CodexStartedSessionID    string `json:"codex_started_session_id"`
	CodexCompletedSessionID  string `json:"codex_completed_session_id"`
	// Cwd and Platform mirror the session-package readers. Platform must
	// survive this path: refreshStatuses feeds every instance through
	// UpdateHookStatus, and a Hermes delegate (platform "subagent") event
	// must be recognised there or it rebinds the entry (#2582).
	Cwd      string `json:"cwd"`
	Platform string `json:"platform"`
}

func defaultLoadHookStatuses() map[string]*session.HookStatus {
	hooksByInstance := make(map[string]*session.HookStatus)
	hooksDir := session.GetHooksDir()

	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		// ENOENT is benign — directory simply hasn't been created yet
		// because no hook event has fired in this profile. Anything else
		// (perm denied, IO error) is a real signal worth surfacing.
		if !errors.Is(err, os.ErrNotExist) {
			hookStatusLog.Warn("hook_status_dir_read_failed",
				slog.String("dir", hooksDir),
				slog.String("error", err.Error()),
			)
		}
		return hooksByInstance
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		instanceID := strings.TrimSuffix(entry.Name(), ".json")
		if instanceID == "" {
			continue
		}

		raw, err := os.ReadFile(filepath.Join(hooksDir, entry.Name()))
		if err != nil {
			hookStatusLog.Warn("hook_status_read_failed",
				slog.String("file", entry.Name()),
				slog.String("instance", instanceID),
				slog.String("error", err.Error()),
			)
			continue
		}

		var parsed rawHookStatus
		if err := json.Unmarshal(raw, &parsed); err != nil {
			hookStatusLog.Warn("hook_status_unmarshal_failed",
				slog.String("file", entry.Name()),
				slog.String("instance", instanceID),
				slog.String("error", err.Error()),
			)
			continue
		}
		if parsed.Status == "" {
			continue
		}

		updatedAt := time.Unix(parsed.Timestamp, 0)
		if parsed.Timestamp <= 0 {
			if info, err := entry.Info(); err == nil {
				updatedAt = info.ModTime()
			}
		}

		hooksByInstance[instanceID] = &session.HookStatus{
			Status:                   parsed.Status,
			SessionID:                parsed.SessionID,
			Event:                    parsed.Event,
			UpdatedAt:                updatedAt,
			CodexStartedGeneration:   parsed.CodexStartedGeneration,
			CodexCompletedGeneration: parsed.CodexCompletedGeneration,
			CodexStartedSessionID:    parsed.CodexStartedSessionID,
			CodexCompletedSessionID:  parsed.CodexCompletedSessionID,
			Cwd:                      parsed.Cwd,
			Platform:                 parsed.Platform,
		}
	}

	return hooksByInstance
}

// menuStatusWorkers bounds the concurrent per-session status checks in one
// menu load. Same pool size as the TUI sweep (ui/home.go): the tmux server
// serializes commands, so more workers do not help.
const menuStatusWorkers = 10

// menuCacheRefreshAge re-lists tmux sessions and panes during a long pass, well
// inside their TTL (2 s for sessions), so late rows still read the shared
// cache instead of probing tmux one session at a time.
const menuCacheRefreshAge = time.Second

// refreshMenuTmuxCaches re-lists tmux sessions and panes into the shared
// caches that per-session status checks read.
func refreshMenuTmuxCaches() {
	tmux.RefreshExistingSessions()
	tmux.RefreshPaneInfoCache()
}

func (s *SessionDataService) refreshStatuses(instances []*session.Instance) {
	// Keep tmux caches warm so per-instance status checks reflect current pane state.
	refreshCaches := refreshMenuTmuxCaches
	if s.refreshTmuxCaches != nil {
		refreshCaches = s.refreshTmuxCaches
	}
	refreshAge := menuCacheRefreshAge
	if s.cacheRefreshAge > 0 {
		refreshAge = s.cacheRefreshAge
	}
	refreshCaches()
	cachesAt := time.Now()

	var hooksByInstance map[string]*session.HookStatus
	if s.loadHookStatuses != nil {
		hooksByInstance = s.loadHookStatuses()
	}

	// Each check spawns tmux/ps subprocesses and waits on them. Run them on a
	// bounded pool like the TUI sweep does, instead of one after another: a
	// serial pass over hundreds of sessions took seconds, and once it outlived
	// the tmux session cache TTL (2 s) every later row fell back to its own
	// has-session/display-message probes, so the cost grew faster than N.
	// Each instance is touched by exactly one worker, and StatusUpdatePass is
	// safe for concurrent workers.
	var statusPass session.StatusUpdatePass
	var g errgroup.Group
	g.SetLimit(menuStatusWorkers)
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if time.Since(cachesAt) >= refreshAge {
			refreshCaches()
			cachesAt = time.Now()
		}
		g.Go(func() error {
			haveHookStatus := false
			if hooksByInstance != nil {
				if hs := hooksByInstance[inst.ID]; hs != nil {
					inst.UpdateHookStatus(hs)
					haveHookStatus = true
				}
			}

			// Without fresh hook data, force a full tmux status check for Claude
			// sessions. This avoids staying in stale idle state when hooks are not
			// emitting for an existing long-running session.
			if inst.GetToolThreadSafe() == "claude" && !haveHookStatus {
				inst.ForceNextStatusCheck()
			}

			if inst.GetTmuxSession() == nil {
				return nil
			}
			_ = statusPass.UpdateStatus(inst)
			return nil
		})
	}
	_ = g.Wait()
}
