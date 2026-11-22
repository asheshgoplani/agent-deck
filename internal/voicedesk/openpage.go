package voicedesk

import (
	"context"
	"errors"
	"strings"
)

// Open a page a session made: the newest HTML or Markdown page it produced,
// opened on his phone and Mac app (`agent-deck open --last --session <id>`,
// branch feat/open-phone). Read-only: no yes.

// IntentOpenPage opens the newest page a session made.
const IntentOpenPage = "open_page"

// Page is what the opener did.
type Page struct {
	Title string `json:"title"`
	Path  string `json:"path"`
	Where string `json:"where"` // mac | phone | both | queued
}

// PageDeck is the optional page part of a Deck.
type PageDeck interface {
	OpenPage(ctx context.Context, s Session) (Page, error)
}

// ErrNoPage: the session made no page.
var ErrNoPage = errors.New("no page")

// ErrPageUnavailable: this build of agent-deck has no `open --last`.
var ErrPageUnavailable = errors.New("opening pages is not in this build")

func grammarOpenPage(a *patternAdder) {
	// soft: a name that is not exactly one of his sessions is talk
	// ("open the coverage report", "open the test report")
	a.add(IntentOpenPage, LangEN, true, false,
		"open [the] [last|latest|newest] page|report from|of {name}", "open [the] [last|latest|newest] page|report {name} made",
		"open {name} page|report", "show me what {name} made", "show me the page|report from|of {name}",
	)
	a.add(IntentOpenPage, LangHI, true, false,
		"{name} ka|ki|ke page|report kholo|dikhao", "{name} ne jo [page|report] banaya woh|vo|wo kholo|dikhao",
	)
	// no name: the session the mic is on (--on)
	a.add(IntentOpenPage, LangEN, false, false, "open the page you made", "show me what you made", "open the page it made")
	a.add(IntentOpenPage, LangHI, false, false, "jo [page|report] banaya woh|vo|wo kholo|dikhao", "jo banaya woh|vo|wo dikhao|kholo")
}

func (d *Desk) openPage(ctx context.Context, req Request, r Result, p Parsed, s Session) (Result, error) {
	name := spokenTitle(s.Title, s.Host)
	t := s.Ref()
	r.Target = &t
	pd, ok := d.Deck.(PageDeck)
	if !ok {
		r.Say = "Opening pages is not available in this build."
		return r, nil
	}
	if req.DryRun {
		r.Say = "Opening the newest page from " + name + "."
		return r, nil
	}
	pg, err := pd.OpenPage(ctx, s)
	switch {
	case errors.Is(err, ErrNoPage):
		r.Say = name + " has made no page yet."
		return r, nil
	case errors.Is(err, ErrPageUnavailable):
		r.Say = "Opening pages is not available in this build."
		return r, nil
	case errors.Is(err, ErrUnsupported):
		r.Say = "Pages of " + name + " cannot be opened by voice yet."
		return r, nil
	case err != nil:
		r.Say = "I could not open the page from " + name + "."
		return r, nil
	}
	title := pg.Title
	if title == "" {
		title = "the page"
	}
	r.Say = "Opening " + title + " from " + name + "."
	r.Show = Show{Kind: "sessions", Items: []ShowItem{{ID: s.ID, Title: s.Title, Host: s.Host, Status: s.Status, Line: title}}}
	return r, nil
}

// openPageOn: "jo page banaya woh kholo" without a name is the session the
// mic is on; without --on the desk asks for a name.
func (d *Desk) openPageOn(ctx context.Context, req Request, r Result, p Parsed) (Result, error) {
	if req.On == "" {
		refuse(&r, RefusedNotFound, "Which session's page? Say its name.")
		return r, nil
	}
	all, _ := d.sessions(ctx)
	for _, s := range d.Config.mine(all) {
		if s.ID == req.On {
			return d.openPage(ctx, req, r, p, s)
		}
	}
	refuse(&r, RefusedNotFound, "Which session's page? Say its name.")
	return r, nil
}

// exactWords: every spoken word is a word of the session's title or group
// as it is written ("test" is not "tests", "proactiv" is not "proactive").
func exactWords(q nameQuery, s Session) bool {
	ok, _, exact := coverage(q, s)
	return ok && exact >= len(q.words)
}

// openPageNamed: "open the page from X". The open forms overlap with
// plain talk ("open the coverage report"), so only a name that is one of
// his sessions word for word opens a page; anything else is talk.
func (d *Desk) openPageNamed(ctx context.Context, req Request, r Result, p Parsed) (Result, error) {
	// "proactive audit's page": the possessive is not part of the name
	p.Name = strings.TrimSpace(strings.NewReplacer("'s ", " ", "’s ", " ").Replace(p.Name + " "))
	if q0 := newNameQuery(p.Name, nil); len(q0.words) == 0 || talkWords(q0.words) {
		return notMine(p.Lang), nil // decided before any listing
	}
	all, _ := d.sessions(ctx)
	q := newNameQuery(p.Name, hostsOf(all))
	res := resolve(q, all, d.Config)
	switch res.Kind {
	case resolvedOther:
		refuse(&r, RefusedOtherPerson, otherLine(res.Person))
		return r, nil
	case resolvedOne:
		if !exactWords(q, res.Session) {
			return notMine(p.Lang), nil
		}
	case resolvedAmbiguous:
		var exact []Session
		for _, s := range res.Choices {
			if exactWords(q, s) {
				exact = append(exact, s)
			}
		}
		switch {
		case len(exact) == 0 || !res.Sure:
			return notMine(p.Lang), nil
		case len(exact) == 1:
			res = resolution{Kind: resolvedOne, Session: exact[0]}
		default:
			res.Choices = exact
		}
	default:
		return notMine(p.Lang), nil
	}
	if genericOnlyQ(q) && !saysWholeTitle(q, res) {
		return notMine(p.Lang), nil // "open the build report"
	}
	if res.Kind == resolvedAmbiguous {
		return d.ask(req, r, p, res)
	}
	return d.withTarget(ctx, req, p, res.Session, false)
}
