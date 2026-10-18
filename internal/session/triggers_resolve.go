package session

import (
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
)

// TriggerResolver resolves trigger targets against the sessions of every
// profile on this host (a title breaks on rename, an id on recreate, and a
// sender may target any profile). Each profile's database is opened
// read-only; current holds the caller's already-loaded profile, if any.
func TriggerResolver(currentProfile string, current []*Instance) triggers.Resolver {
	if current == nil {
		return TriggerResolverForSessions(currentProfile, nil)
	}
	return TriggerResolverForSessions(currentProfile, resolvedFromInstances(current))
}

// TriggerResolverForSessions is TriggerResolver for a caller that already
// holds a snapshot of its profile's sessions (the TUI takes it on its own
// goroutine, then resolves in a background command). A nil current means
// the current profile is read from its database like the others.
func TriggerResolverForSessions(currentProfile string, current []triggers.Resolved) triggers.Resolver {
	var sets []triggers.ProfileSessions
	if current != nil {
		sets = append(sets, triggers.ProfileSessions{Profile: currentProfile, Sessions: current})
	}
	profiles, _ := ListProfiles()
	for _, p := range profiles {
		if current != nil && p == currentProfile {
			continue
		}
		path, err := GetDBPathForProfile(p)
		if err != nil {
			continue
		}
		db, err := statedb.OpenReadOnlyLive(path)
		if err != nil {
			continue
		}
		rows, err := db.LoadInstances()
		_ = db.Close()
		if err != nil {
			continue
		}
		set := triggers.ProfileSessions{Profile: p}
		for _, r := range rows {
			set.Sessions = append(set.Sessions, triggers.Resolved{SessionID: r.ID, Title: r.Title})
		}
		sets = append(sets, set)
	}
	return triggers.NewResolver(sets)
}

func resolvedFromInstances(insts []*Instance) []triggers.Resolved {
	out := make([]triggers.Resolved, 0, len(insts))
	for _, inst := range insts {
		if inst != nil {
			out = append(out, triggers.Resolved{SessionID: inst.ID, Title: inst.Title})
		}
	}
	return out
}
