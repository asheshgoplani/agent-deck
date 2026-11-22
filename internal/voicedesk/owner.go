package voicedesk

import (
	"context"
	"sort"
	"strings"
	"time"
)

// Session is one session the desk can see: this machine (Host "") or a
// remote (Host = the remote's name).
type Session struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Group          string    `json:"group"`
	Profile        string    `json:"profile"`
	Host           string    `json:"host"`
	Status         string    `json:"status"`
	Substate       string    `json:"substate,omitempty"`
	Tool           string    `json:"tool,omitempty"`   // harness: claude, codex, shell ...
	Reason         string    `json:"reason,omitempty"` // needs-me: why it waits
	LastActivity   time.Time `json:"last_activity,omitempty"`
	ParentID       string    `json:"parent_id,omitempty"`
	Archived       bool      `json:"archived,omitempty"`
	WaitingSeconds int64     `json:"waiting_seconds,omitempty"`
	// Conductor: the deck says this session is a conductor (list --json
	// conductor_name, from its conductor flag, so a renamed one keeps it).
	Conductor bool `json:"conductor,omitempty"`
}

// Ref is the short form of a session in results and pending actions.
type Ref struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Host    string `json:"host"`
	Profile string `json:"profile,omitempty"`
	Tool    string `json:"-"` // carried to the send guard, not part of the result
}

func (s Session) Ref() Ref {
	return Ref{ID: s.ID, Title: s.Title, Host: s.Host, Profile: s.Profile, Tool: s.Tool}
}

// Person is someone else whose sessions are visible to the deck (a shared
// profile or remote). Their sessions are never a voice target.
type Person struct {
	Groups   []string // group paths (a group and everything below it)
	Profiles []string // whole profiles
	Names    []string // extra spoken names; the person's key is always one
}

// Config is the owner rule set ([voice] in config.toml).
//
//	[voice]
//	owner_groups   = ["team/alex"]   # his groups (and their subgroups)
//	owner_profiles = ["work"]        # every session of these profiles is his
//	[voice.people.sam]
//	groups   = ["team/sam"]
//	profiles = []
//	names    = ["samuel"]
//
// With neither owner_groups nor owner_profiles set, every session that is
// not someone else's counts as his (a one-person install).
type Config struct {
	OwnerGroups   []string
	OwnerProfiles []string
	People        map[string]Person
}

// Owner kinds.
const (
	ownerMine   = "mine"
	ownerOther  = "other"
	ownerNobody = "nobody"
)

func groupCovers(prefix, group string) int {
	p := strings.Trim(strings.ToLower(strings.TrimSpace(prefix)), "/")
	g := strings.Trim(strings.ToLower(strings.TrimSpace(group)), "/")
	if p == "" {
		return -1
	}
	if g == p || strings.HasPrefix(g, p+"/") {
		return len(p)
	}
	return -1
}

func profileIn(profile string, list []string) bool {
	for _, p := range list {
		if strings.EqualFold(strings.TrimSpace(p), profile) {
			return true
		}
	}
	return false
}

// ownerOf says whose session s is. The most specific rule wins (a longer
// group prefix beats a shorter one, any group beats a whole profile); a tie
// between him and someone else goes to the other person (never act on a
// session that might not be his).
func (c Config) ownerOf(s Session) (kind, person string) {
	bestMine := -2
	for _, g := range c.OwnerGroups {
		if n := groupCovers(g, s.Group); n > bestMine {
			bestMine = n
		}
	}
	if bestMine < 0 && profileIn(s.Profile, c.OwnerProfiles) {
		bestMine = 0
	}
	bestOther, who := -2, ""
	names := make([]string, 0, len(c.People))
	for n := range c.People {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := c.People[n]
		for _, g := range p.Groups {
			if l := groupCovers(g, s.Group); l > bestOther {
				bestOther, who = l, n
			}
		}
		if bestOther < 0 && profileIn(s.Profile, p.Profiles) {
			bestOther, who = 0, n
		}
	}
	switch {
	case bestOther >= 0 && bestOther >= bestMine:
		return ownerOther, who
	case bestMine >= 0:
		return ownerMine, ""
	case len(c.OwnerGroups) == 0 && len(c.OwnerProfiles) == 0:
		return ownerMine, ""
	}
	return ownerNobody, ""
}

// personNamed returns the person whose name is one of the spoken words.
func (c Config) personNamed(words []string) string {
	names := make([]string, 0, len(c.People))
	for n := range c.People {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		all := append([]string{n}, c.People[n].Names...)
		for _, a := range all {
			fa := foldPhrase(a)
			if fa == "" {
				continue
			}
			for _, w := range words {
				if w == fa {
					return n
				}
			}
		}
	}
	return ""
}

// Mine says whether s is his by the owner rules (archived or not).
func (c Config) Mine(s Session) bool {
	k, _ := c.ownerOf(s)
	return k == ownerMine
}

// mine keeps his sessions (not archived).
func (c Config) mine(all []Session) []Session {
	var out []Session
	for _, s := range all {
		if s.Archived {
			continue
		}
		if k, _ := c.ownerOf(s); k == ownerMine {
			out = append(out, s)
		}
	}
	return out
}

// micMarker is the voice daemon's title prefix for the session that has
// the mic ("🎙 garden planner"). It is never part of a name: the desk
// strips it everywhere (say, show, target, matching) and pairs a session
// with the voice state by id only.
const micMarker = "🎙"

// cleanTitle drops the mic marker (with or without the emoji variation
// selector) and the spaces around it.
func cleanTitle(t string) string {
	t = strings.TrimSpace(t)
	for strings.HasPrefix(t, micMarker) {
		t = strings.TrimPrefix(t, micMarker)
		t = strings.TrimPrefix(t, "️")
		t = strings.TrimSpace(t)
	}
	return t
}

func cleanAll(ss []Session) []Session {
	for i := range ss {
		ss[i].Title = cleanTitle(ss[i].Title)
	}
	return ss
}

func (d *Desk) sessions(ctx context.Context) ([]Session, []HostError) {
	ss, errs := d.Deck.Sessions(ctx)
	return cleanAll(ss), errs
}

func (d *Desk) needsMeRows(ctx context.Context) ([]Session, []HostError) {
	ss, errs := d.Deck.NeedsMe(ctx)
	return cleanAll(ss), errs
}
