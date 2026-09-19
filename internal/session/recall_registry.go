package session

import (
	"os"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/ingest"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// RecallRegistry adapts every profile's state.db to the ingester's
// Registry. recall.db is machine-global and state.db per profile, and a
// deck session of profile A may run under account B (its transcript under
// B's config dir), so a link and its hints can live in any profile.
// Lookups try the transcript's profile first, then the invoking one, then
// the rest; the state.db that holds the authoritative link owns the
// card's instance hints (and, in the CLI, receives the usage). Only the
// invoking profile's state.db is created or migrated; the others are
// opened as they are and skipped when absent. The CLI, the hook and the
// TUI share it so the three never project a card differently.
type RecallRegistry struct {
	profile string
	dbs     map[string]*statedb.StateDB
	order   []string
	opened  []*statedb.StateDB
	owners  map[string]string // deck id -> the profile whose state.db holds its link
}

var _ ingest.Registry = (*RecallRegistry)(nil)

// NewRecallRegistry builds the registry over the invoking profile's open
// state.db (reg, may be nil) and every other profile's file on disk.
func NewRecallRegistry(profile string, reg *statedb.StateDB) *RecallRegistry {
	r := &RecallRegistry{profile: profile, dbs: map[string]*statedb.StateDB{}, owners: map[string]string{}}
	if reg != nil {
		r.dbs[profile] = reg
		r.order = append(r.order, profile)
	}
	names, _ := ListProfiles()
	sort.Strings(names)
	for _, name := range names {
		if _, ok := r.dbs[name]; ok {
			continue
		}
		path, err := GetDBPathForProfile(name)
		if err != nil {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		db, err := statedb.Open(path)
		if err != nil {
			continue
		}
		r.dbs[name] = db
		r.opened = append(r.opened, db)
		r.order = append(r.order, name)
	}
	return r
}

// Close closes the state.dbs the registry opened itself (never reg).
func (r *RecallRegistry) Close() {
	for _, db := range r.opened {
		_ = db.Close()
	}
}

// Profile is the invoking profile.
func (r *RecallRegistry) Profile() string { return r.profile }

// DB returns one profile's state.db (nil when not open).
func (r *RecallRegistry) DB(profile string) *statedb.StateDB { return r.dbs[profile] }

// lookup lists the profiles whose state.db to consult for a transcript of
// profile, that profile's own first.
func (r *RecallRegistry) lookup(profile string) []string {
	out := make([]string, 0, len(r.order))
	if r.dbs[profile] != nil {
		out = append(out, profile)
	}
	for _, name := range r.order {
		if name != profile {
			out = append(out, name)
		}
	}
	return out
}

// Owner returns the deck session bound to a conversation and the state.db
// holding that authoritative link.
func (r *RecallRegistry) Owner(profile, harness, native string) (string, *statedb.StateDB) {
	for _, name := range r.lookup(profile) {
		db := r.dbs[name]
		if id, err := db.AuthoritativeLinkOwner(harness, native); err == nil && id != "" {
			r.owners[id] = name
			return id, db
		}
	}
	return "", nil
}

// OwnerProfile names the profile whose state.db holds deck's link ("" when
// none does).
func (r *RecallRegistry) OwnerProfile(deck string) string { return r.owners[deck] }

// DeckID implements ingest.Registry.
func (r *RecallRegistry) DeckID(profile, harness, native string) string {
	id, _ := r.Owner(profile, harness, native)
	return id
}

// ChangedSince implements ingest.Registry: the union over every profile.
func (r *RecallRegistry) ChangedSince(since time.Time) []ingest.Ref {
	var out []ingest.Ref
	seen := map[ingest.Ref]bool{}
	for _, name := range r.order {
		refs, err := r.dbs[name].RecallChangedRefs(since.Unix())
		if err != nil {
			continue
		}
		for _, ref := range refs {
			k := ingest.Ref{Harness: ref.Harness, NativeID: ref.NativeID}
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// Hints joins the hints and tags of the bound instance (from the state.db
// that owns the link) and those keyed on the conversation id in any
// profile, for the card's FTS columns.
func (r *RecallRegistry) Hints(profile, harness, native string) (string, string) {
	var hints, tags []string
	seenHint, seenTag := map[string]bool{}, map[string]bool{}
	collect := func(db *statedb.StateDB, scopeKind, scopeID string) {
		if hs, err := db.ListSessionHints(scopeKind, scopeID); err == nil {
			for _, h := range hs {
				if kv := h.Key + "=" + h.Value; !seenHint[kv] {
					seenHint[kv] = true
					hints = append(hints, kv)
				}
			}
		}
		if ts, err := db.ListSessionTags(scopeKind, scopeID); err == nil {
			for _, t := range ts {
				if !seenTag[t.Tag] {
					seenTag[t.Tag] = true
					tags = append(tags, t.Tag)
				}
			}
		}
	}
	for _, name := range r.lookup(profile) {
		collect(r.dbs[name], statedb.HintScopeHarnessSession, native)
	}
	if deck, db := r.Owner(profile, harness, native); deck != "" {
		collect(db, statedb.HintScopeInstance, deck)
	}
	return strings.Join(hints, " "), strings.Join(tags, " ")
}

// RecallBusy reports a managed session mid-turn in this state.db (the
// status column), the rule the sweep gate uses.
func RecallBusy(db *statedb.StateDB) (bool, string) {
	if db == nil {
		return false, ""
	}
	rows, err := db.LoadInstances()
	if err != nil {
		return false, ""
	}
	for _, row := range rows {
		if ingest.BusyStatuses[row.Status] {
			return true, "session " + row.Title + " is busy"
		}
	}
	return false, ""
}
