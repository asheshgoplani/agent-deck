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
	r := ResultEvent{Intent: closedWord(d.Intent, "none"), Target: d.Target, Show: showWithoutText(d.Show), Then: d.Then, Refused: refusedWord(d.Refused), From: from}
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

// SessionIDFor is SessionID on profile's bus: "" when the target is a
// session of another profile (a frame on one profile's bus never names
// another profile's session as its session_id).
func (r ResultEvent) SessionIDFor(profile string) string {
	if r.Target != nil && r.Target.Profile != "" && profile != "" && r.Target.Profile != profile {
		return ""
	}
	return r.SessionID()
}

// refusals are the desk's refusal words (voicedesk Refused*). Any other
// text is not copied: it reads as "unsafe", still a refusal.
var refusals = map[string]bool{"other-person": true, "not-found": true, "ambiguous": true, "unsafe": true, "expired": true}

func refusedWord(in *string) *string {
	if in == nil {
		return nil
	}
	w := *in
	if !refusals[w] {
		w = "unsafe"
	}
	return &w
}

// closedWord keeps a label that is one short lower-case word ("tell",
// "todo_list", "waiting", "not-found"); anything else becomes alt. Labels
// are never his words.
func closedWord(in, alt string) string {
	if in == "" || len(in) > 24 {
		return alt
	}
	for _, c := range in {
		if (c < 'a' || c > 'z') && c != '_' && c != '-' {
			return alt
		}
	}
	return in
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
	events.PublishProfile(profile, KindResult, r.SessionIDFor(profile), r)
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
// per item and a known kind: no free text reaches the frame. Only the
// session kinds (sessions, confirm, search: a title is a session's title)
// keep title and host; to-dos (the title is his text) and any other kind
// keep id and status only, so a new or misspelt kind can never leak.
func showWithoutText(in ResultShow) ResultShow {
	out := ResultShow{Kind: "none", Items: make([]ResultItem, 0, len(in.Items))}
	sessionKind := false
	switch in.Kind {
	case "sessions", "confirm", "search":
		out.Kind, sessionKind = in.Kind, true
	case "todos":
		out.Kind = in.Kind
	}
	for _, it := range in.Items {
		item := ResultItem{ID: it.ID, Status: closedWord(it.Status, "")}
		if sessionKind {
			item.Title, item.Host = it.Title, it.Host
		}
		out.Items = append(out.Items, item)
	}
	return out
}
