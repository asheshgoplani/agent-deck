package main

import "github.com/asheshgoplani/agent-deck/internal/session"

// refreshLiveStatus is session.RefreshLiveStatus: the status `list` shows,
// for commands that decide on it.
func refreshLiveStatus(instances []*session.Instance) {
	session.RefreshLiveStatus(instances)
}

// liveSwitchStatuses returns the live status of every non-archived session by id,
// for switch's candidates and its live-session bonus. A nil map (no state
// yet, or a read failure) leaves the stored statuses in place.
func liveSwitchStatuses(profile string) map[string]string {
	storage, err := session.NewLiveReadOnlyStorageWithProfile(profile)
	if err != nil {
		return nil
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		return nil
	}
	// Switch never targets an archived session, so only the others get the
	// status pass.
	instances = session.FilterInstancesByArchive(instances, false)
	refreshLiveStatus(instances)
	out := make(map[string]string, len(instances))
	for _, inst := range instances {
		if inst != nil {
			out[inst.ID] = string(inst.GetStatusThreadSafe())
		}
	}
	return out
}
