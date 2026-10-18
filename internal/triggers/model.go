package triggers

import (
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Health values. "unknown" is only ever set for a host that could not say
// (an old remote or an unreachable one); it is never a guess.
const (
	HealthOK         = "ok"
	HealthFailing    = "failing"
	HealthDeadTarget = "dead-target"
	HealthNeverFired = "never-fired"
	HealthUnknown    = "unknown"
)

// Fire outcomes.
const (
	OutcomeDelivered  = "delivered"
	OutcomeQueued     = "queued"
	OutcomeFailed     = "failed"
	OutcomeDeadTarget = "dead-target"
	OutcomeMuted      = "muted"
)

// NoisyShare is the no-op share at which a trigger counts as very noisy.
const NoisyShare = 0.9

// Target is where a trigger sends, as written and as resolved now.
type Target struct {
	AsWritten string `json:"as_written"`
	By        string `json:"by"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Profile   string `json:"profile"`
	Resolved  bool   `json:"resolved"`
}

// View is one trigger in `triggers --json` (docs: CONTRACT in #2702).
// Every key is always present (empty when unknown) except last_fire and
// muted_until, which are omitted, and noop_share_24h, which is null.
type View struct {
	ID           string     `json:"id"`
	Host         string     `json:"host"`
	Kind         string     `json:"kind"`
	Source       string     `json:"source"`
	ConfigPath   string     `json:"config_path"`
	Verb         string     `json:"verb"`
	Target       Target     `json:"target"`
	Schedule     string     `json:"schedule"`
	Origin       string     `json:"origin"`
	Inferred     bool       `json:"inferred"`
	LastFire     *time.Time `json:"last_fire,omitempty"`
	LastLine     string     `json:"last_line"`
	LastOutcome  string     `json:"last_outcome"`
	Fires24h     int        `json:"fires_24h"`
	NoopShare24h *float64   `json:"noop_share_24h"`
	Health       string     `json:"health"`
	Muted        bool       `json:"muted"`
	MutedUntil   *time.Time `json:"muted_until,omitempty"`
	Paused       bool       `json:"paused"`
	Runnable     bool       `json:"runnable"`

	// firesBySession is the 24 h fire count per receiving session id; it
	// feeds the reverse view and the row badge.
	firesBySession map[string]int
	unit           string
	unitKind       string
}

// Resolved is a session a target points at.
type Resolved struct {
	SessionID string
	Title     string
	Profile   string
}

// Resolver finds the session a target (title or id) names, in any profile.
// profile narrows the search when the sender named one with -p.
type Resolver func(target, profile string) (Resolved, bool)

// Build turns stored rows and their 24 h stats into views.
func Build(rows []statedb.TriggerRow, stats map[string]*statedb.TriggerStats, resolve Resolver, host string, now time.Time) []View {
	out := make([]View, 0, len(rows))
	for _, r := range rows {
		if r.UnitKind == "" && r.Scanned && (r.Kind == KindLaunchd || r.Kind == KindSystemd) {
			r.UnitKind = r.Kind // a row scanned before unit_kind existed
		}
		v := View{
			ID: r.ID, Host: r.Host, Kind: r.Kind, Source: r.Source, ConfigPath: r.ConfigPath,
			Verb: r.Verb, Schedule: r.Schedule, Inferred: r.Inferred, unit: r.Unit, unitKind: r.UnitKind,
			Target:   Target{AsWritten: r.Target, By: r.TargetBy, Profile: r.TargetProfile},
			Runnable: r.Scanned && r.Unit != "" && (r.UnitKind == KindLaunchd || r.UnitKind == KindSystemd),
		}
		if v.Host == "" {
			v.Host = host
		}
		switch {
		case r.Pushed && r.Scanned:
			v.Origin = "push+scan"
		case r.Scanned:
			v.Origin = "scan"
		default:
			v.Origin = "push"
		}
		if r.MutedUntil.After(now) {
			v.Muted = true
			mu := r.MutedUntil
			v.MutedUntil = &mu
		}
		st := stats[r.ID]
		var last, active *statedb.TriggerFireRow
		if st != nil {
			last, active = st.LastFire, st.LastActive
			if active == nil && last != nil && last.Outcome != OutcomeMuted {
				active = last
			}
			v.Fires24h = st.Fires24h
			v.firesBySession = st.FiresByID
			if st.Fires24h > 0 {
				share := float64(st.Noops24h) / float64(st.Fires24h)
				v.NoopShare24h = &share
			}
		}
		if last != nil {
			ts := last.TS
			v.LastFire, v.LastLine, v.LastOutcome = &ts, last.Line, last.Outcome
		}
		if r.Target != "" && resolve != nil {
			if res, ok := resolve(r.Target, r.TargetProfile); ok {
				v.Target.SessionID, v.Target.Title, v.Target.Resolved = res.SessionID, res.Title, true
				if v.Target.Profile == "" {
					v.Target.Profile = res.Profile
				}
			}
		}
		v.Health = health(v, last, active)
		out = append(out, v)
	}
	return out
}

// health applies the rules in order: a target that names no session is the
// worst state, then a failed last fire, then a trigger that never fired.
// Muted fires are skipped (active is the newest fire that was not muted):
// muting a failing trigger must not make it look healthy.
func health(v View, last, active *statedb.TriggerFireRow) string {
	if v.Target.AsWritten != "" && !v.Target.Resolved {
		return HealthDeadTarget
	}
	if v.Target.AsWritten == "" && active != nil && active.Outcome == OutcomeDeadTarget {
		return HealthDeadTarget
	}
	if last == nil {
		return HealthNeverFired
	}
	if active != nil && active.Outcome == OutcomeFailed {
		return HealthFailing
	}
	return HealthOK
}

// IsProblem reports a row the "only problems" filter keeps.
func (v View) IsProblem() bool {
	return v.Health != HealthOK || (v.NoopShare24h != nil && *v.NoopShare24h >= NoisyShare)
}

// Wakes reports whether this trigger wakes the session (by its resolved
// target, or by having fired at it in the last 24 h).
func (v View) Wakes(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	return v.Target.SessionID == sessionID || v.firesBySession[sessionID] > 0
}

// FiresAt is the 24 h fire count at one session.
func (v View) FiresAt(sessionID string) int { return v.firesBySession[sessionID] }

// IsTriggerKind reports whether the row is a trigger by default (not a
// session-to-session message and not a person at a shell).
func (v View) IsTriggerKind() bool { return v.Kind != KindSession && v.Kind != KindCLI }

// Filter narrows a list. Empty fields do not filter.
type Filter struct {
	Kind      string
	Health    string // a health value, or "problems"
	SessionID string // reverse view: what wakes this session
	All       bool   // include session and cli senders
}

// Apply returns the matching views, sorted by host, kind and source (the
// reverse view sorts by fires at the session, busiest first).
func (f Filter) Apply(in []View) []View {
	var out []View
	for _, v := range in {
		if !f.All && !v.IsTriggerKind() && f.Kind != v.Kind {
			continue
		}
		if f.Kind != "" && v.Kind != f.Kind {
			continue
		}
		switch f.Health {
		case "":
		case "problems":
			if !v.IsProblem() {
				continue
			}
		default:
			if v.Health != f.Health {
				continue
			}
		}
		if f.SessionID != "" && !v.Wakes(f.SessionID) {
			continue
		}
		out = append(out, v)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if f.SessionID != "" && out[i].FiresAt(f.SessionID) != out[j].FiresAt(f.SessionID) {
			return out[i].FiresAt(f.SessionID) > out[j].FiresAt(f.SessionID)
		}
		a, b := out[i], out[j]
		if a.Host != b.Host {
			return a.Host < b.Host
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return strings.ToLower(a.Source) < strings.ToLower(b.Source)
	})
	return out
}

// Badge is the per-session "what wakes me" summary for a row badge.
type Badge struct {
	Count int
	Bad   bool // one of them is failing
}

// Badges counts the triggers that wake each session.
func Badges(views []View) map[string]Badge {
	out := map[string]Badge{}
	for _, v := range views {
		if !v.IsTriggerKind() {
			continue
		}
		seen := map[string]bool{}
		mark := func(sid string) {
			if sid == "" || seen[sid] {
				return
			}
			seen[sid] = true
			b := out[sid]
			b.Count++
			b.Bad = b.Bad || v.Health == HealthFailing
			out[sid] = b
		}
		mark(v.Target.SessionID)
		for sid := range v.firesBySession {
			mark(sid)
		}
	}
	return out
}

// Summary counts the header pills.
type Summary struct {
	Total, DeadTargets, Failing, VeryNoisy int
}

func Summarize(views []View) Summary {
	s := Summary{Total: len(views)}
	for _, v := range views {
		switch v.Health {
		case HealthDeadTarget:
			s.DeadTargets++
		case HealthFailing:
			s.Failing++
		}
		if v.NoopShare24h != nil && *v.NoopShare24h >= NoisyShare {
			s.VeryNoisy++
		}
	}
	return s
}

// ProfileSessions is one profile's sessions, for NewResolver.
type ProfileSessions struct {
	Profile  string
	Sessions []Resolved
}

// NewResolver matches a target against every profile's sessions: an exact
// id, an id prefix of at least 8 characters, or an exact title. When the
// sender named a profile, only that profile counts.
func NewResolver(sets []ProfileSessions) Resolver {
	return func(target, profile string) (Resolved, bool) {
		if target == "" {
			return Resolved{}, false
		}
		var byTitle *Resolved
		for _, set := range sets {
			if profile != "" && set.Profile != profile {
				continue
			}
			for i := range set.Sessions {
				s := set.Sessions[i]
				s.Profile = set.Profile
				if s.SessionID == target || (len(target) >= 8 && strings.HasPrefix(s.SessionID, target)) {
					return s, true
				}
				if byTitle == nil && s.Title == target {
					byTitle = &s
				}
			}
		}
		if byTitle != nil {
			return *byTitle, true
		}
		return Resolved{}, false
	}
}
