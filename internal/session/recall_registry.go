package session

import (
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/recall/ingest"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// RecallRegistry adapts a profile's state.db to the ingester's Registry:
// deck ids from authoritative links only; the hints and tags of the bound
// instance plus any keyed on the harness conversation id, joined into the
// card's FTS columns; and the conversations whose rows changed since the
// last sweep. A nil db answers nothing. The CLI, the hook and the TUI
// share it so the three never project a card differently.
type RecallRegistry struct{ DB *statedb.StateDB }

var _ ingest.Registry = RecallRegistry{}

// DeckID returns the agent-deck session bound to a conversation.
func (r RecallRegistry) DeckID(harness, native string) string {
	if r.DB == nil {
		return ""
	}
	id, _ := r.DB.AuthoritativeLinkOwner(harness, native)
	return id
}

// ChangedSince lists conversations annotated or linked since.
func (r RecallRegistry) ChangedSince(since time.Time) []ingest.Ref {
	if r.DB == nil {
		return nil
	}
	refs, err := r.DB.RecallChangedRefs(since.Unix())
	if err != nil {
		return nil
	}
	out := make([]ingest.Ref, len(refs))
	for i, ref := range refs {
		out[i] = ingest.Ref{Harness: ref.Harness, NativeID: ref.NativeID}
	}
	return out
}

// Hints returns the space-joined "key=value" hints and the tags.
func (r RecallRegistry) Hints(harness, native string) (string, string) {
	if r.DB == nil {
		return "", ""
	}
	var hints, tags []string
	scopes := [][2]string{{statedb.HintScopeHarnessSession, native}}
	if deck := r.DeckID(harness, native); deck != "" {
		scopes = append(scopes, [2]string{statedb.HintScopeInstance, deck})
	}
	for _, sc := range scopes {
		if hs, err := r.DB.ListSessionHints(sc[0], sc[1]); err == nil {
			for _, h := range hs {
				hints = append(hints, h.Key+"="+h.Value)
			}
		}
		if ts, err := r.DB.ListSessionTags(sc[0], sc[1]); err == nil {
			for _, t := range ts {
				tags = append(tags, t.Tag)
			}
		}
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
