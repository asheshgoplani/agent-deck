package query

import (
	"context"
	"encoding/json"
)

// Artifact is one derived row (internal/recall/enrich) as the CLI shows
// it. Stale means the session's content moved since the artifact was
// produced (input_rev < derived_rev): the text is shown, marked, and the
// next drain rewrites it.
type Artifact struct {
	Kind        string          `json:"kind"`
	Body        string          `json:"body"`
	BodyJSON    json.RawMessage `json:"body_json,omitempty"`
	Producer    string          `json:"producer"`
	ProducerVer string          `json:"producer_ver"`
	Confidence  float64         `json:"confidence"`
	InputRev    int64           `json:"input_rev"`
	DerivedRev  int64           `json:"derived_rev"`
	Stale       bool            `json:"stale"`
	CreatedAt   int64           `json:"created_at"`
}

// Artifacts lists the session's artifacts, kind order, with staleness
// computed against the session's current derived_rev.
func (s *Searcher) Artifacts(ctx context.Context, sessID int64) ([]Artifact, error) {
	rows, err := s.st.R.QueryContext(ctx, `SELECT a.kind, a.body, a.body_json, a.producer, a.producer_ver, a.confidence, a.input_rev, s.derived_rev, a.created_at
		FROM artifact a JOIN session s ON s.sess_id=a.sess_id WHERE a.sess_id=? ORDER BY a.kind, a.producer, a.producer_ver`, sessID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		var bodyJSON string
		if err := rows.Scan(&a.Kind, &a.Body, &bodyJSON, &a.Producer, &a.ProducerVer, &a.Confidence, &a.InputRev, &a.DerivedRev, &a.CreatedAt); err != nil {
			return nil, err
		}
		if json.Valid([]byte(bodyJSON)) {
			a.BodyJSON = json.RawMessage(bodyJSON)
		}
		a.Stale = a.InputRev < a.DerivedRev
		out = append(out, a)
	}
	return out, rows.Err()
}

// Line renders an artifact for a text tier: "kind: body" with the stale
// marker and, for a guess, its confidence.
func (a Artifact) Line() string {
	line := a.Kind + ": " + a.Body
	if a.Confidence < 1 {
		line += " [confidence " + trimFloat(a.Confidence) + "]"
	}
	if a.Stale {
		line += " [stale: session changed since; run 'agent-deck recall enrich']"
	}
	return line
}

func trimFloat(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
