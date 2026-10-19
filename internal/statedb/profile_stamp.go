package statedb

import (
	"errors"
	"fmt"
	"strings"
)

// ErrProfileMismatch reports that a state database is stamped for another
// profile than the one the caller resolved.
var ErrProfileMismatch = errors.New("state database belongs to another profile")

// metaProfileKey holds the name of the profile a state.db belongs to.
const metaProfileKey = "profile"

// ProfileMismatchError names the database, the profile it is stamped for and
// the profile the caller wanted.
type ProfileMismatchError struct {
	Path    string
	Stamped string
	Wanted  string
}

func (e *ProfileMismatchError) Error() string {
	return fmt.Sprintf("state database %s belongs to profile %q, not %q; nothing was written. "+
		"If you renamed or copied this profile directory on purpose, clear the stamp with: "+
		"sqlite3 %s \"DELETE FROM metadata WHERE key='profile'\"",
		e.Path, e.Stamped, e.Wanted, e.Path)
}

func (e *ProfileMismatchError) Is(target error) bool { return target == ErrProfileMismatch }

// BindProfile asserts that this database belongs to profile and remembers the
// name for Profile.
//
// Wrong-profile writes have escaped more than once: a restart outcome written
// to the process-global database of another profile, a watcher and a remote
// agent serving a different profile than -p named, an inbox export reading
// every profile. Each was fixed where it happened. This is the check at the
// store itself: a writable database records the profile it was first opened
// for, and every later bind for a different profile is refused before
// anything is written, so a path that resolves to the wrong store fails
// loudly instead of mixing two profiles' sessions.
//
// A database without a stamp (every store created before this check) is
// stamped by its first writable bind. A read-only handle never writes; it is
// checked only when a stamp is already there. Names compare case-insensitively
// because a case-insensitive file system maps "Work" and "work" to the same
// directory, which is one profile, not two.
func (s *StateDB) BindProfile(profile string) error {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return errors.New("statedb: bind profile: empty profile name")
	}
	if !s.readOnly {
		if err := withBusyRetry(func() error {
			_, err := s.db.Exec(`INSERT OR IGNORE INTO metadata (key, value) VALUES (?, ?)`, metaProfileKey, profile)
			return err
		}); err != nil {
			return fmt.Errorf("statedb: stamp profile %q: %w", profile, err)
		}
	}
	stamped, err := s.GetMeta(metaProfileKey)
	if err != nil {
		if s.readOnly {
			// A read-only store too old to have a metadata table carries no
			// stamp to compare.
			s.profile = profile
			return nil
		}
		return fmt.Errorf("statedb: read profile stamp: %w", err)
	}
	if stamped != "" && !strings.EqualFold(stamped, profile) {
		return &ProfileMismatchError{Path: s.path, Stamped: stamped, Wanted: profile}
	}
	s.profile = profile
	return nil
}

// Profile returns the profile BindProfile confirmed, or "" when the handle was
// never bound (tests, tools that open a database by path).
func (s *StateDB) Profile() string { return s.profile }

// ReadOnly reports whether this handle was opened read-only.
func (s *StateDB) ReadOnly() bool { return s.readOnly }
