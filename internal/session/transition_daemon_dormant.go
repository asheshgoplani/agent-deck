package session

import (
	"os"
	"path/filepath"
	"time"
)

// Dormant profiles (ASK-229).
//
// The daemon walks every profile directory on every 1-3 s pass. A host whose
// profiles directory filled up with empty profiles (3,074 of them on deckbox,
// left by test runs under the real HOME, next to one profile with 146
// sessions) made each pass open and query thousands of SQLite stores, and
// getStorage kept every one of them open for the daemon's life: ~29 % of a
// core and ~1 GB RSS (modernc SQLite's C heap, ~270 KB and 3 fds per store).
//
// An empty profile is now loaded once, its store closed, and the profile
// marked dormant with a stamp of its state.db and state.db-wal (size and
// mtime). Every later pass compares the stamp, two stats, and skips the
// profile while it holds. Anything that adds a session writes the store, so
// the stamp moves and the next pass syncs the profile as before; a profile
// that is synced that way and still empty goes back to dormant. As a
// backstop each dormant profile is also re-synced after
// dormantProfileRecheck, at most dormantRechecksPerPass per pass so a large
// set never lands in one pass.

// dormantProfileRecheck is how long a dormant profile is trusted on its stamp
// alone before one full re-sync. A variable so tests can shorten it.
var dormantProfileRecheck = 5 * time.Minute

// dormantRechecksPerPass bounds the backstop re-syncs in one pass.
const dormantRechecksPerPass = 32

// profileStoreStamp is what a write to a profile's store changes.
type profileStoreStamp struct {
	dbSize, dbModNs   int64
	walSize, walModNs int64
	walExists         bool
}

type dormantProfile struct {
	dir     string
	stamp   profileStoreStamp
	checked time.Time
}

func readProfileStoreStamp(dir string) (profileStoreStamp, bool) {
	fi, err := os.Stat(filepath.Join(dir, "state.db"))
	if err != nil {
		return profileStoreStamp{}, false
	}
	st := profileStoreStamp{dbSize: fi.Size(), dbModNs: fi.ModTime().UnixNano()}
	if wi, err := os.Stat(filepath.Join(dir, "state.db-wal")); err == nil {
		st.walExists = true
		st.walSize = wi.Size()
		st.walModNs = wi.ModTime().UnixNano()
	}
	return st, true
}

// skipDormantProfile reports whether profile can sit out this pass: it is
// dormant, its store stamp is unchanged, and its backstop re-sync is not due
// (or this pass has no re-sync left). Otherwise the profile leaves the
// dormant set and the caller syncs it.
func (d *TransitionDaemon) skipDormantProfile(profile string, now time.Time, rechecks *int) bool {
	dp, ok := d.dormant[profile]
	if !ok {
		return false
	}
	cur, ok := readProfileStoreStamp(dp.dir)
	if !ok || cur != dp.stamp {
		delete(d.dormant, profile)
		return false
	}
	if now.Sub(dp.checked) >= dormantProfileRecheck && *rechecks > 0 {
		*rechecks--
		delete(d.dormant, profile)
		return false
	}
	return true
}

// releaseEmptyProfileStore closes the store of a profile the pass just found
// empty and marks it dormant. The stamp is read after the close: closing the
// last connection checkpoints and removes the WAL, which must not count as a
// change on the next pass.
func (d *TransitionDaemon) releaseEmptyProfileStore(profile string, now time.Time) {
	if !d.lastSyncEmpty {
		return
	}
	if s := d.storages[profile]; s != nil {
		_ = s.Close()
	}
	delete(d.storages, profile)
	dir, err := GetProfileDir(profile)
	if err != nil {
		return
	}
	stamp, ok := readProfileStoreStamp(dir)
	if !ok {
		return
	}
	if d.dormant == nil {
		d.dormant = map[string]dormantProfile{}
	}
	d.dormant[profile] = dormantProfile{dir: dir, stamp: stamp, checked: now}
}

// pruneDormantProfiles forgets dormant profiles that are no longer listed.
func (d *TransitionDaemon) pruneDormantProfiles(profiles []string) {
	if len(d.dormant) <= len(profiles) {
		return
	}
	listed := make(map[string]struct{}, len(profiles))
	for _, p := range profiles {
		listed[p] = struct{}{}
	}
	for p := range d.dormant {
		if _, ok := listed[p]; !ok {
			delete(d.dormant, p)
		}
	}
}
