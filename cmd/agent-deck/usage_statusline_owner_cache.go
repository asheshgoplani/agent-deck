package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// The status line owner lookup (statuslineOwnerProfile) must know, for every
// profile store, which Claude session IDs it records. Claude Code runs the
// status line command on every refresh of every session, and opening each
// store per refresh cost ~0.65 ms of CPU per store: ~2 CPU seconds per refresh
// with 3075 stores on one machine (ASK-229). The index below keeps each
// store's Claude session IDs together with the size and mtime of its state.db
// and state.db-wal, so a refresh opens only the stores whose files changed.
//
// A WAL-mode write always changes state.db-wal (or, at checkpoint, state.db),
// so unchanged stamps mean an unchanged query result. To stay clear of coarse
// filesystem timestamps, a store whose files changed within
// statuslineOwnerSettle is never cached: it is read again on the next refresh.

const (
	statuslineOwnerIndexVersion = 1
	statuslineOwnerIndexFile    = "statusline-owners.json"
	statuslineOwnerSettle       = 2 * time.Second
)

// openStatuslineOwnerStore opens one profile store for the owner lookup; a
// seam so tests can count how many stores a status line refresh opens.
var openStatuslineOwnerStore = session.NewLiveReadOnlyStorageWithProfile

type statuslineFileStamp struct {
	Size  int64 `json:"size"`
	MTime int64 `json:"mtime"`
}

type statuslineStoreStamp struct {
	DB  statuslineFileStamp `json:"db"`
	WAL statuslineFileStamp `json:"wal"`
}

type statuslineOwnerEntry struct {
	Stamp statuslineStoreStamp `json:"stamp"`
	IDs   []string             `json:"ids,omitempty"`
}

type statuslineOwnerIndex struct {
	Version     int                             `json:"version"`
	ProfilesDir string                          `json:"profiles_dir"`
	Tools       []string                        `json:"tools"`
	Profiles    map[string]statuslineOwnerEntry `json:"profiles"`

	path    string
	dirty   bool
	present map[string]bool
}

func statuslineToolNames() []string {
	_, args := statedb.ClaudeToolPredicate()
	names := make([]string, 0, len(args))
	for _, arg := range args {
		if name, ok := arg.(string); ok {
			names = append(names, name)
		}
	}
	return names
}

// loadStatuslineOwnerIndex returns the cached index, or an empty one when it
// is missing, unreadable, or was built for other stores or tool names. It
// never fails: the index is only an optimisation.
func loadStatuslineOwnerIndex() *statuslineOwnerIndex {
	profilesDir, _ := session.GetProfilesDir()
	fresh := &statuslineOwnerIndex{
		Version:     statuslineOwnerIndexVersion,
		ProfilesDir: profilesDir,
		Tools:       statuslineToolNames(),
		Profiles:    map[string]statuslineOwnerEntry{},
		present:     map[string]bool{},
	}
	cacheDir, err := agentpaths.CacheDir()
	if err != nil || profilesDir == "" {
		return fresh
	}
	fresh.path = filepath.Join(cacheDir, statuslineOwnerIndexFile)
	data, err := os.ReadFile(fresh.path)
	if err != nil {
		return fresh
	}
	var cached statuslineOwnerIndex
	if json.Unmarshal(data, &cached) != nil || cached.Version != fresh.Version ||
		cached.ProfilesDir != fresh.ProfilesDir || !slices.Equal(cached.Tools, fresh.Tools) || cached.Profiles == nil {
		return fresh
	}
	fresh.Profiles = cached.Profiles
	return fresh
}

func statuslineStatFile(path string) (statuslineFileStamp, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return statuslineFileStamp{Size: -1}, nil
	}
	if err != nil {
		return statuslineFileStamp{}, err
	}
	return statuslineFileStamp{Size: info.Size(), MTime: info.ModTime().UnixNano()}, nil
}

// statuslineStampStore stamps the store of profile, a directory name from
// session.ListProfiles under profilesDir (session.GetDBPathForProfile's path,
// without resolving the profiles root again for each of thousands of stores).
func statuslineStampStore(profilesDir, profile string) (statuslineStoreStamp, bool) {
	if profilesDir == "" || profile != filepath.Base(profile) || profile == "." || profile == ".." {
		return statuslineStoreStamp{}, false
	}
	dbPath := filepath.Join(profilesDir, profile, "state.db")
	db, err := statuslineStatFile(dbPath)
	if err != nil || db.Size < 0 {
		return statuslineStoreStamp{}, false
	}
	wal, err := statuslineStatFile(dbPath + "-wal")
	if err != nil {
		return statuslineStoreStamp{}, false
	}
	// A missing or empty WAL holds no frames: the store's content is state.db
	// alone. Reading a store creates and removes an empty WAL, which must not
	// look like a change.
	if wal.Size <= 0 {
		wal = statuslineFileStamp{}
	}
	return statuslineStoreStamp{DB: db, WAL: wal}, true
}

func (s statuslineStoreStamp) settled(now time.Time) bool {
	cutoff := now.Add(-statuslineOwnerSettle).UnixNano()
	return s.DB.MTime < cutoff && s.WAL.MTime < cutoff
}

// claudeIDs returns the Claude session IDs profile's store records, from the
// index when the store's files are unchanged, else by reading the store.
// ok is false when the store cannot be read (the lookup skips it, as before).
func (x *statuslineOwnerIndex) claudeIDs(profile string) ([]string, bool) {
	x.present[profile] = true
	before, stamped := statuslineStampStore(x.ProfilesDir, profile)
	if stamped {
		if entry, hit := x.Profiles[profile]; hit && entry.Stamp == before {
			return entry.IDs, true
		}
	}
	ids, err := readStatuslineClaudeIDs(profile)
	if err != nil {
		return nil, false
	}
	after, stampedAfter := statuslineStampStore(x.ProfilesDir, profile)
	if stamped && stampedAfter && before == after && after.settled(time.Now()) {
		x.Profiles[profile] = statuslineOwnerEntry{Stamp: after, IDs: ids}
		x.dirty = true
	} else if _, had := x.Profiles[profile]; had {
		delete(x.Profiles, profile)
		x.dirty = true
	}
	return ids, true
}

// save drops entries for stores that no longer exist and writes the index
// when anything changed. complete is true when every listed profile was
// visited; only then can absent entries be pruned.
func (x *statuslineOwnerIndex) save(complete bool) {
	if complete {
		for profile := range x.Profiles {
			if !x.present[profile] {
				delete(x.Profiles, profile)
				x.dirty = true
			}
		}
	}
	if !x.dirty || x.path == "" {
		return
	}
	data, err := json.Marshal(x)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(x.path), 0o700) != nil {
		return
	}
	// Session IDs are private to the user: 0600.
	_ = atomicfile.WriteFile(x.path, data, 0o600)
}

// readStatuslineClaudeIDs reads the Claude session IDs recorded in profile's
// store: exactly the rows the owner lookup's former per-ID count matched.
func readStatuslineClaudeIDs(profile string) ([]string, error) {
	storage, err := openStatuslineOwnerStore(profile)
	if err != nil {
		return nil, err
	}
	defer storage.Close()
	toolCond, toolArgs := statedb.ClaudeToolPredicate()
	rows, err := storage.GetDB().DB().Query(`SELECT DISTINCT json_extract(tool_data, '$.claude_session_id') FROM instances WHERE `+toolCond+` AND typeof(json_extract(tool_data, '$.claude_session_id')) = 'text'`, toolArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return ids, nil
}
