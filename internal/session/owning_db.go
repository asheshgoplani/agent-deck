package session

import (
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// withOwningStateDB runs a targeted write against the state database of the
// profile this instance was loaded from or saved to, never against another
// profile's.
//
// Targeted writes (last access, native session bindings, recall links, the
// restart outcome) used to go to statedb.GetGlobal(), the database of the
// profile the process started with. An instance loaded from another profile
// then had its write land in the wrong store, or vanish as a zero-row UPDATE
// (#2206 for the restart outcome). Every such write goes through here,
// whether or not the process has a global handle:
//
//   - the owning handle when it is open (a read-only owning handle is used
//     as is, so its write fails instead of bypassing the read-only load);
//   - when a short-lived Storage closed it, before or during the write, the
//     global handle only if it is the same file, otherwise the instance's
//     saved owning path reopened with its profile stamp checked, so a path
//     that now holds another profile's store is refused. The path is never
//     created: a profile deleted in the meantime stays deleted;
//   - with no saved owning path, the closed handle, so the write fails.
//
// An instance never loaded or saved through a Storage has no owner yet; it
// keeps the old behaviour and uses the global handle, or returns
// errNoStateDB when there is none.
func (i *Instance) withOwningStateDB(write func(*statedb.StateDB) error) error {
	owner := i.restartDB.Load()
	if owner == nil {
		db := statedb.GetGlobal()
		if db == nil {
			return errNoStateDB
		}
		return write(db)
	}
	if owner.ReadOnly() {
		return write(owner)
	}
	if owner.DB().Ping() == nil {
		err := write(owner)
		if err == nil || owner.DB().Ping() == nil {
			return err
		}
		// Closed between the ping and the write: retry below.
	}
	if g := statedb.GetGlobal(); g != nil && !g.ReadOnly() && owner.Path() != "" &&
		g.Path() == owner.Path() && g.DB().Ping() == nil {
		return write(g)
	}
	if i.storageSnapshot == nil || i.storageSnapshot.dbPath == "" {
		return write(owner)
	}
	path := i.storageSnapshot.dbPath
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("owning state database %s: %w", path, err)
	}
	db, err := statedb.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if p := owner.Profile(); p != "" {
		if err := db.BindProfile(p); err != nil {
			return err
		}
	}
	return write(db)
}
