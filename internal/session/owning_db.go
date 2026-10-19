package session

import (
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
// (#2206 for the restart outcome). Every such write goes through here:
//
//   - the owning handle when it is open and writable;
//   - otherwise the global handle only when it is the same file;
//   - otherwise, when the owning handle was writable and has been closed (a
//     short-lived Storage), the owning file reopened with its profile stamp
//     checked, so a path that now holds another profile's store is refused.
//
// An instance loaded read-only from another profile's store gets no write.
//
// An instance never loaded or saved through a Storage has no owner yet; it
// keeps the old behaviour and uses the global handle.
func (i *Instance) withOwningStateDB(write func(*statedb.StateDB) error) error {
	owner := i.restartDB.Load()
	if owner == nil {
		db := statedb.GetGlobal()
		if db == nil {
			return errNoStateDB
		}
		return write(db)
	}
	if !owner.ReadOnly() && owner.DB().Ping() == nil {
		return write(owner)
	}
	if g := statedb.GetGlobal(); g != nil && !g.ReadOnly() && owner.Path() != "" &&
		g.Path() == owner.Path() && g.DB().Ping() == nil {
		return write(g)
	}
	if owner.ReadOnly() || owner.Path() == "" {
		// A read-only load promised not to touch the store, so it is not
		// reopened for writing here.
		return errNoStateDB
	}
	db, err := statedb.Open(owner.Path())
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
