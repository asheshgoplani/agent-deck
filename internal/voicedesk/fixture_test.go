package voicedesk

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC)

// fakeDeck is the saved session list (testdata/sessions.json) behind the
// Deck interface: no tmux, no ssh, no agent-deck state.
type fakeDeck struct {
	t         *testing.T
	sessions  []Session
	replies   map[string]string // id -> last reply
	errs      []HostError
	sent      []sentMsg
	shown     []string
	lists     int      // Sessions / NeedsMe calls
	acts      []string // "stop <id>"
	launched  []LaunchSpec
	hits      []SearchHit
	searchErr error
	children  map[string][]Session
	noList    bool // fail the test when the desk lists sessions
	// fresh: what Lookup returns for an id (the session changed after the
	// listing); absent ids are looked up in sessions
	fresh map[string]Session
}

type sentMsg struct{ id, host, msg, tool string }

func loadFixture(t *testing.T) *fakeDeck {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Sessions []struct {
			Session
			Minutes int `json:"minutes"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	d := &fakeDeck{t: t, replies: map[string]string{}}
	for _, r := range raw.Sessions {
		s := r.Session
		if r.Minutes > 0 {
			s.LastActivity = t0.Add(-time.Duration(r.Minutes) * time.Minute)
			if s.Status == "waiting" {
				s.WaitingSeconds = int64(r.Minutes * 60)
			}
		}
		d.sessions = append(d.sessions, s)
	}
	d.replies["a1000002-1700000002"] = "## Progress\n\nTests are running on the **new** build. I will report when they finish. Then I merge.\n\n```go\nfunc main() {}\n```"
	d.replies["a1000004-1700000004"] = "The audit found three stale sessions. Two of them are safe to archive. The third needs a look."
	d.replies["a1000001-1700000001"] = "<speak>Two PRs are open, both green.</speak> Long detail follows here with a table.\n| a | b |\n|---|---|"
	return d
}

func (d *fakeDeck) Sessions(context.Context) ([]Session, []HostError) {
	if d.noList {
		d.t.Fatalf("the desk listed sessions for a sentence that is not its own")
	}
	d.lists++
	return append([]Session(nil), d.sessions...), d.errs
}

func (d *fakeDeck) NeedsMe(context.Context) ([]Session, []HostError) {
	if d.noList {
		d.t.Fatalf("the desk listed sessions for a sentence that is not its own")
	}
	d.lists++
	var out []Session
	for _, s := range d.sessions {
		if s.Status == "waiting" && !s.Archived {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].WaitingSeconds > out[j].WaitingSeconds })
	return out, d.errs
}

func (d *fakeDeck) LastReply(_ context.Context, s Session) (string, error) {
	r, ok := d.replies[s.ID]
	if !ok {
		return "", nil
	}
	return r, nil
}

func (d *fakeDeck) Send(_ context.Context, s Session, msg string) error {
	if s.ID == "" {
		return errors.New("no id")
	}
	d.sent = append(d.sent, sentMsg{id: s.ID, host: s.Host, msg: msg, tool: s.Tool})
	return nil
}

func (d *fakeDeck) Show(_ context.Context, s Session) error {
	d.shown = append(d.shown, s.ID)
	return nil
}

// fixtureConfig: alex is the owner; sam and robin are other people.
func fixtureConfig() Config {
	return Config{
		OwnerGroups: []string{"team/alex"},
		People: map[string]Person{
			"sam":   {Groups: []string{"team/sam"}},
			"robin": {Groups: []string{"team/robin"}, Names: []string{"rob"}},
		},
	}
}

type harness struct {
	t     *testing.T
	deck  *fakeDeck
	store *MemStore
	desk  *Desk
	now   time.Time
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, deck: loadFixture(t), store: NewMemStore(), now: t0}
	h.desk = &Desk{Deck: h.deck, Store: h.store, Config: fixtureConfig(), Now: func() time.Time { return h.now }}
	return h
}

func (h *harness) do(req Request) Result {
	h.t.Helper()
	if req.Profile == "" {
		req.Profile = "work"
	}
	r, err := h.desk.Do(context.Background(), req)
	if err != nil {
		h.t.Fatalf("Do(%q): %v", req.Sentence, err)
	}
	if r.Handled && r.Say == "" {
		h.t.Fatalf("Do(%q): handled with an empty say", req.Sentence)
	}
	return r
}

func (h *harness) say(sentence string) Result {
	h.t.Helper()
	return h.do(Request{Sentence: sentence})
}

func refusedOf(r Result) string {
	if r.Refused == nil {
		return ""
	}
	return *r.Refused
}

func targetID(r Result) string {
	if r.Target == nil {
		return ""
	}
	return r.Target.ID
}

func (d *fakeDeck) Lookup(_ context.Context, s Session) (Session, bool, error) {
	if x, ok := d.fresh[s.ID]; ok {
		return x, true, nil
	}
	for _, x := range d.sessions {
		if x.ID == s.ID && x.Host == s.Host {
			return x, true, nil
		}
	}
	return Session{}, false, nil
}

func (d *fakeDeck) Act(_ context.Context, s Session, verb string) error {
	d.acts = append(d.acts, verb+" "+s.ID)
	for i := range d.sessions {
		if d.sessions[i].ID == s.ID {
			switch verb {
			case "stop":
				d.sessions[i].Status = "stopped"
			case "archive":
				d.sessions[i].Archived = true
			}
		}
	}
	return nil
}

func (d *fakeDeck) Launch(_ context.Context, spec LaunchSpec) (Session, error) {
	d.launched = append(d.launched, spec)
	return Session{ID: "n1000001-1700000099", Title: spec.Title, Group: spec.Group, Host: spec.Host, Profile: spec.Profile}, nil
}

func (d *fakeDeck) Search(_ context.Context, q string) ([]SearchHit, error) {
	if d.searchErr != nil {
		return nil, d.searchErr
	}
	var out []SearchHit
	for _, h := range d.hits {
		if strings.Contains(strings.ToLower(h.Snippet), strings.ToLower(q)) {
			out = append(out, h)
		}
	}
	return out, nil
}

func (d *fakeDeck) Children(_ context.Context, s Session) ([]Session, error) {
	if s.Host != "" {
		return nil, ErrUnsupported
	}
	return d.children[s.ID], nil
}
