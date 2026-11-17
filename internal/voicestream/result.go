package voicestream

import (
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// ResultItem is one row of the desk's show list.
type ResultItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Host   string `json:"host"`
	Status string `json:"status"`
	Line   string `json:"line"`
}

// ResultShow is the desk's `show` (CONTRACT.md section 2).
type ResultShow struct {
	Kind  string       `json:"kind"`
	Items []ResultItem `json:"items"`
}

// ResultConfirm is the confirm card without its token: screens show the
// question, only the brain holds the token.
type ResultConfirm struct {
	Kind      string `json:"kind"`
	Prompt    string `json:"prompt"`
	ExpiresMs int64  `json:"expiresMs"`
}

// ResultEvent is the data of a voice.result frame (FRAME.md). It never
// carries the heard sentence or the spoken line.
type ResultEvent struct {
	Host    string         `json:"host"`
	Intent  string         `json:"intent"`
	Target  *Target        `json:"target"`
	Show    ResultShow     `json:"show"`
	Confirm *ResultConfirm `json:"confirm"`
	Refused *string        `json:"refused"`
	From    string         `json:"from"`
	At      string         `json:"at"`
}

// SessionID is the frame's session_id: the target when it is on this host.
func (r ResultEvent) SessionID() string {
	if r.Target != nil && r.Target.Host == "" {
		return r.Target.ID
	}
	return ""
}

// PublishResult puts the desk's result on the profile's bus (never blocks;
// the bus may drop under load, see docs/events.md).
func PublishResult(profile string, r ResultEvent) {
	if r.At == "" {
		r.At = rfc3339ms(time.Now())
	}
	if r.Show.Items == nil {
		r.Show.Items = []ResultItem{}
	}
	if r.Show.Kind == "" {
		r.Show.Kind = "none"
	}
	events.PublishProfile(profile, KindResult, r.SessionID(), r)
}
