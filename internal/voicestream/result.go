package voicestream

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// ResultItem is one row of the desk's show list: which session and its
// status, nothing else. The desk's own "line" (his dictated message for a
// tell, the spoken reply for a read) never goes on the bus.
type ResultItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Host   string `json:"host"`
	Status string `json:"status"`
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

// ResultThen is the desk's follow-up for the caller (CONTRACT section 2):
// "switch" = the mic is about to move to Target. Screens may show
// "Switching to <title>…" until a voice.state frame names that target.
type ResultThen struct {
	Action string  `json:"action"`
	Target *Target `json:"target"`
}

// ResultEvent is the data of a voice.result frame (FRAME.md). It never
// carries the heard sentence, the spoken line or the confirm token.
type ResultEvent struct {
	Host    string         `json:"host"`
	Intent  string         `json:"intent"`
	Target  *Target        `json:"target"`
	Show    ResultShow     `json:"show"`
	Confirm *ResultConfirm `json:"confirm"`
	Then    *ResultThen    `json:"then"`
	Refused *string        `json:"refused"`
	From    string         `json:"from"`
	At      string         `json:"at"`
}

// deskResult is the part of `voice do --json` (CONTRACT section 2) a
// frame may carry. "say" and the heard sentence are not decoded at all;
// confirm.token is dropped.
type deskResult struct {
	Handled bool       `json:"handled"`
	Intent  string     `json:"intent"`
	Target  *Target    `json:"target"`
	Show    ResultShow `json:"show"`
	Confirm *struct {
		Kind      string `json:"kind"`
		Prompt    string `json:"prompt"`
		ExpiresMs int64  `json:"expiresMs"`
	} `json:"confirm"`
	Then    *ResultThen `json:"then"`
	Refused *string     `json:"refused"`
	DryRun  bool        `json:"dryRun"`
}

// ErrNotPublishable: a result that is not handled or is a dry run puts
// nothing on the screens.
var ErrNotPublishable = errors.New("result not handled or a dry run: nothing to show")

// ResultFromDesk maps the desk's result JSON to a voice.result frame:
// intent, target, show, confirm (without its token), then, refused.
// from is the client (mac, glasses, phone, text). The caller has already
// redacted secrets from show and the confirm prompt.
func ResultFromDesk(raw []byte, from string) (ResultEvent, error) {
	var d deskResult
	if err := json.Unmarshal(raw, &d); err != nil {
		return ResultEvent{}, fmt.Errorf("decoding desk result: %w", err)
	}
	if !d.Handled || d.DryRun {
		return ResultEvent{}, ErrNotPublishable
	}
	r := ResultEvent{Intent: d.Intent, Target: d.Target, Show: showWithoutText(d.Show), Then: d.Then, Refused: d.Refused, From: from}
	if d.Confirm != nil {
		// The desk's prompt is the read-back of his dictated message ("Send
		// to alpha: <message>. Yes?"): it stays with the brain. The card
		// gets a prompt built from the target only.
		r.Confirm = &ResultConfirm{Kind: d.Confirm.Kind, Prompt: confirmPrompt(d.Confirm.Kind, d.Intent, d.Target), ExpiresMs: d.Confirm.ExpiresMs}
	}
	if r.Then != nil && r.Then.Action == "" {
		r.Then = nil
	}
	return r, nil
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

// confirmPrompt is the card's question without any of his words.
func confirmPrompt(kind, intent string, target *Target) string {
	title := ""
	if target != nil {
		title = target.Title
	}
	switch {
	case kind == "choice":
		return "Which one?"
	case intent == "tell" && title != "":
		return "Send to " + title + "?"
	case intent == "find" && title != "":
		return "Switch to " + title + "?"
	case title != "":
		return "Confirm for " + title + "?"
	}
	return "Confirm?"
}

// showWithoutText rebuilds the desk's show from id, title, host and status
// per item and a known kind: no free text reaches the frame. For to-dos
// (title is his text) only id and status are kept.
func showWithoutText(in ResultShow) ResultShow {
	out := ResultShow{Kind: "none", Items: make([]ResultItem, 0, len(in.Items))}
	switch in.Kind {
	case "sessions", "confirm", "search", "todos":
		out.Kind = in.Kind
	}
	for _, it := range in.Items {
		if in.Kind == "todos" {
			// a to-do's title is his dictated text: id and status only.
			out.Items = append(out.Items, ResultItem{ID: it.ID, Status: it.Status})
			continue
		}
		out.Items = append(out.Items, ResultItem{ID: it.ID, Title: it.Title, Host: it.Host, Status: it.Status})
	}
	return out
}
