package ui

// Onboarding reads across profiles (ASK-211 review round 1). Two things an
// empty profile's onboarding offers are not per profile at all:
//
//   - the Claude hooks go into Claude's own settings, shared by every
//     profile, so an answer to the hooks question recorded in any profile
//     is the answer (a decline anywhere must not be undone by the ticked
//     hooks line of a new profile's first session);
//   - tmux sessions are machine-wide, so the welcome's Import must not take
//     over a session another profile already owns.
//
// Every other profile's state.db is opened read-only (no schema, no
// writes) and closed again.

import (
	"log/slog"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// eachProfileDB calls fn with every profile's state.db, opened read-only.
// A profile whose database cannot be opened is skipped.
func eachProfileDB(fn func(profile string, db *statedb.StateDB)) {
	profiles, err := session.ListProfiles()
	if err != nil {
		uiLog.Warn("onboarding_list_profiles_failed", slog.String("error", err.Error()))
		return
	}
	for _, p := range profiles {
		path, err := session.GetDBPathForProfile(p)
		if err != nil {
			continue
		}
		db, err := statedb.OpenReadOnlyLive(path)
		if err != nil {
			continue
		}
		fn(p, db)
		_ = db.Close()
	}
}

// claudeHooksPrompted is the recorded answer to the Claude hooks question:
// own (this profile's "hooks_prompted") when there is one, else the answer
// any other profile recorded, a decline winning over an accept. "" means
// no profile has answered yet.
func claudeHooksPrompted(own string) string {
	if own != "" {
		return own
	}
	answer := ""
	eachProfileDB(func(_ string, db *statedb.StateDB) {
		v, _ := db.GetMeta("hooks_prompted")
		switch {
		case v == "declined":
			answer = v
		case v != "" && answer == "":
			answer = v
		}
	})
	return answer
}

// tmuxSessionsOwnedByProfiles is the set of tmux session names that any
// profile's sessions run in.
func tmuxSessionsOwnedByProfiles() map[string]bool {
	owned := map[string]bool{}
	eachProfileDB(func(_ string, db *statedb.StateDB) {
		rows, err := db.LoadInstances()
		if err != nil {
			return
		}
		for _, r := range rows {
			if r.TmuxSession != "" {
				owned[r.TmuxSession] = true
			}
		}
	})
	return owned
}
