package voicedesk

import "strings"

// Owner rule: voice never targets a session of another local profile and
// never a conductor (his or anyone's, renamed or not). Find, its yes, its
// choice and every switch (also "take me to the next one") keep to these.

// isConductor: the deck says the session is a conductor (its conductor
// flag, carried by list --json conductor_name, so a renamed conductor still
// counts), or, for a host too old to say, its title is "conductor" /
// "conductor-<name>" or it sits in the conductor group that setup uses.
func isConductor(s Session) bool {
	if s.Conductor {
		return true
	}
	t := strings.ToLower(cleanTitle(s.Title))
	if t == "conductor" || strings.HasPrefix(t, "conductor-") {
		return true
	}
	g := strings.ToLower(strings.Trim(s.Group, "/ "))
	return g == "conductor" || strings.HasPrefix(g, "conductor/")
}

// inProfile: a session of this machine is in the profile voice runs in. A
// remote's session is asked for that remote's configured profile only
// (needs-me), so it counts. An empty profile on either side is not known
// and is kept (the deck fills it for every local row).
func inProfile(s Session, profile string) bool {
	return s.Host != "" || profile == "" || s.Profile == "" || s.Profile == profile
}

// switchable: voice may find it and move the mic to it.
func switchable(s Session, profile string) bool {
	return inProfile(s, profile) && !isConductor(s)
}

// switchableOnly keeps the sessions voice may switch to.
func switchableOnly(all []Session, profile string) []Session {
	out := make([]Session, 0, len(all))
	for _, s := range all {
		if switchable(s, profile) {
			out = append(out, s)
		}
	}
	return out
}

// notSwitchableLine says why a session is not switched to.
func notSwitchableLine(s Session, profile string) string {
	name := spokenTitle(s.Title, s.Host)
	if isConductor(s) {
		return capitalize(name) + " is a conductor; voice does not switch to conductors. Nothing done."
	}
	_ = profile
	return capitalize(name) + " is in another profile; nothing done."
}
