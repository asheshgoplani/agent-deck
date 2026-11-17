package voicedesk

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Phase 2: launch, stop, restart, archive (each after a yes), search what
// was said, workers of a session, the next one that needs me. Voice never
// deletes, pushes or pays: "delete X session" is refused by name.

const maxPerYes = 5 // "stop all workers": at most five sessions per yes

func verbOf(intent string) (action, present, past string) {
	switch intent {
	case IntentStop:
		return actionStop, "Stop", "Stopped"
	case IntentRestart:
		return actionRestart, "Restart", "Restarted"
	case IntentArchive:
		return actionArchive, "Archive", "Archived"
	}
	return "", "", ""
}

func (d *Desk) runPhase2(ctx context.Context, req Request, r Result, p Parsed) (Result, error) {
	switch p.Intent {
	case IntentNext:
		rows, errs := d.Deck.NeedsMe(ctx)
		for _, s := range d.Config.mine(rows) {
			if s.ID == req.On {
				continue
			}
			d.switchTo(ctx, req, &r, s)
			if s.Reason != "" {
				r.Say = "Switching to " + spokenTitle(s.Title, s.Host) + ", it " + s.Reason + "."
			}
			r.Show = Show{Kind: "sessions", Items: items([]Session{s}, d.now())}
			return r, nil
		}
		r.Say = "Nobody else is waiting for you." + unreachable(errs)
		return r, nil
	case IntentSearch:
		return d.search(ctx, r, p)
	case IntentLaunch:
		return d.launch(ctx, req, r, p)
	}
	return r, errors.New("not a phase 2 intent: " + p.Intent)
}

func (d *Desk) search(ctx context.Context, r Result, p Parsed) (Result, error) {
	query := strings.TrimSpace(strings.TrimRight(p.Message, ".?!"))
	qw := trimNameNoise(splitWords(query))
	if len(qw) == 0 {
		return notMine(p.Lang), nil
	}
	query = rawJoin(qw)
	if HasSecret(query) {
		refuse(&r, RefusedUnsafe, "I will not search for a secret by voice.")
		return r, nil
	}
	hits, err := d.Deck.Search(ctx, query)
	switch {
	case errors.Is(err, ErrSearchOff):
		refuse(&r, RefusedUnsafe, "Search is off on this machine; turn on recall first.")
		return r, nil
	case err != nil:
		r.Say = "I could not search right now."
		return r, nil
	case len(hits) == 0:
		r.Say = "Nothing found about " + query + "."
		return r, nil
	}
	names := make([]string, 0, len(hits))
	r.Show = Show{Kind: "search", Items: []ShowItem{}}
	for _, h := range hits {
		title := h.Title
		if title == "" {
			title = "an untitled conversation"
		}
		names = append(names, spokenTitle(title, h.Host))
		line := Speakable(h.Snippet)
		r.Show.Items = append(r.Show.Items, ShowItem{ID: h.DeckID, Title: h.Title, Host: h.Host, Line: line})
	}
	r.Say = strings.Replace(spokenList(names), ":", " conversations:", 1)
	if len(hits) == 1 {
		r.Say = "One conversation: " + names[0] + "."
	}
	return r, nil
}

// kebab turns spoken words into a session title: "test one" -> "test-one".
func kebab(ws []word) string {
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		if w.deva {
			parts = append(parts, w.roman) // a title is typed in Latin letters
			continue
		}
		parts = append(parts, strings.ToLower(w.raw))
	}
	t := strings.Join(parts, "-")
	if rs := []rune(t); len(rs) > 60 {
		t = string(rs[:60])
	}
	return t
}

func (d *Desk) launch(ctx context.Context, req Request, r Result, p Parsed) (Result, error) {
	all, _ := d.Deck.Sessions(ctx)
	hosts := hostsOf(all)
	tw := splitWords(p.Title)
	spec := LaunchSpec{}
	// "test one on boxone"
	if n := len(tw); n >= 3 && hostLead[tw[n-2].folded] {
		for _, h := range hosts {
			if tw[n-1].folded == foldPhrase(h) {
				spec.Host, tw = h, tw[:n-2]
			}
		}
	}
	tw = trimNameNoise(tw)
	if len(tw) == 0 {
		return notMine(p.Lang), nil
	}
	spec.Title = kebab(tw)
	if HasSecret(spec.Title) {
		refuse(&r, RefusedUnsafe, "That name looks like a secret; type it instead.")
		return r, nil
	}
	group, profile, ok := d.launchGroup(p.Group, req.On, spec.Host, all)
	if !ok {
		refuse(&r, RefusedNotFound, "I found no group of yours called "+p.Group+".")
		return r, nil
	}
	spec.Group, spec.Profile = group, profile
	for _, s := range d.Config.mine(all) {
		if strings.EqualFold(s.Title, spec.Title) && s.Host == spec.Host {
			refuse(&r, RefusedUnsafe, "There is already a session called "+spokenTitle(s.Title, s.Host)+".")
			return r, nil
		}
	}
	where := ""
	if spec.Group != "" {
		where = " in " + spec.Group
	}
	if spec.Host != "" {
		where += " on " + spec.Host
	}
	prompt := "Start a new session " + spokenTitle(spec.Title, "") + where + "? Yes?"
	r.Say = prompt
	r.Show = Show{Kind: "confirm", Items: []ShowItem{{Title: spec.Title, Host: spec.Host, Line: "new session" + where}}}
	return d.pend(req, r, Pending{Profile: req.Profile, Kind: "yes", Action: actionLaunch, Intent: IntentLaunch, Lang: p.Lang, Launch: &spec}, prompt)
}

// launchGroup picks the group of a new session: the spoken one (matched to
// one of his groups by its words), else the group of the session the mic
// is on, else his first owner group. The profile is one his sessions of
// that group live in.
func (d *Desk) launchGroup(spoken, on, host string, all []Session) (group, profile string, ok bool) {
	mine := d.Config.mine(all)
	groups := map[string]string{} // group -> profile
	for _, s := range mine {
		if s.Host == host && s.Group != "" {
			if _, seen := groups[s.Group]; !seen {
				groups[s.Group] = s.Profile
			}
		}
	}
	for _, g := range d.Config.OwnerGroups {
		if _, seen := groups[g]; !seen && host == "" {
			groups[g] = ""
		}
	}
	if strings.TrimSpace(spoken) == "" {
		for _, s := range mine {
			if s.ID == on && s.Host == host {
				return s.Group, s.Profile, true
			}
		}
		if len(d.Config.OwnerGroups) > 0 {
			return d.Config.OwnerGroups[0], groups[d.Config.OwnerGroups[0]], true
		}
		return "", "", true
	}
	sw := foldedWords(trimNameNoise(splitWords(spoken)))
	var best []string
	for g := range groups {
		segs := strings.Split(strings.ToLower(g), "/")
		last := foldPhrase(segs[len(segs)-1])
		if last == strings.Join(sw, " ") || foldPhrase(g) == strings.Join(sw, " ") {
			best = append(best, g)
		}
	}
	if len(best) != 1 {
		return "", "", false
	}
	return best[0], groups[best[0]], true
}

// withTargetPhase2: one resolved session of his.
func (d *Desk) withTargetPhase2(ctx context.Context, req Request, r Result, p Parsed, s Session) (Result, error) {
	name := spokenTitle(s.Title, s.Host)
	switch p.Intent {
	case IntentRefuse:
		refuse(&r, RefusedUnsafe, "I never delete a session by voice; do it on screen.")
		return r, nil
	case IntentWorkers:
		kids, err := d.Deck.Children(ctx, s)
		kids = cleanAll(kids)
		switch {
		case errors.Is(err, ErrUnsupported):
			r.Say = "Workers of " + name + " are not reachable by voice yet."
			return r, nil
		case err != nil:
			r.Say = "I could not read the workers of " + name + "."
			return r, nil
		case len(kids) == 0:
			r.Say = name + " has no workers."
			return r, nil
		}
		parts := make([]string, 0, len(kids))
		for _, k := range kids {
			parts = append(parts, spokenTitle(k.Title, "")+" "+statusWords(k.Status))
		}
		r.Show = Show{Kind: "sessions", Items: items(kids, d.now())}
		r.Say = capitalize(lowerCount(len(kids))) + plural2(len(kids), " worker", " workers") + ": " + strings.Join(firstN(parts, maxSpokenItems), ", ")
		if len(kids) > maxSpokenItems {
			r.Say += fmt.Sprintf(", and %d more on screen", len(kids)-maxSpokenItems)
		}
		r.Say += "."
		return r, nil
	}
	action, present, _ := verbOf(p.Intent)
	if p.Intent == IntentStop && s.Status == "stopped" {
		r.Say = name + " is already stopped."
		return r, nil
	}
	prompt := present + " " + name + "? Yes?"
	r.Say = prompt
	t := s.Ref()
	r.Show = Show{Kind: "confirm", Items: items([]Session{s}, d.now())}
	return d.pend(req, r, Pending{Profile: req.Profile, Kind: "yes", Action: action, Intent: p.Intent, Lang: p.Lang, Target: &t}, prompt)
}

func plural2(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// many: "stop all workers" lists every session of his the name fits, and
// asks once for at most five of them.
func (d *Desk) many(ctx context.Context, req Request, r Result, p Parsed, q nameQuery, all []Session) (Result, error) {
	action, present, _ := verbOf(p.Intent)
	if action == "" || len(q.words) == 0 {
		return notMine(p.Lang), nil
	}
	var hits []Session
	for _, s := range d.Config.mine(all) {
		if q.host != "" && s.Host != q.host {
			continue
		}
		if p.Intent == IntentStop && s.Status == "stopped" {
			continue
		}
		if ok, level, _ := coverage(q, s); ok && level >= wordPhonetic {
			hits = append(hits, s)
		}
	}
	if len(hits) == 0 {
		if who := d.Config.personNamed(q.words); who != "" {
			refuse(&r, RefusedOtherPerson, otherLine(who)+" Nothing done.")
			return r, nil
		}
		return notMine(p.Lang), nil
	}
	sessionOrder(hits)
	asked := hits
	if len(asked) > maxPerYes {
		asked = asked[:maxPerYes]
	}
	names := make([]string, len(asked))
	refs := make([]Ref, len(asked))
	for i, s := range asked {
		names[i] = spokenTitle(s.Title, s.Host)
		refs[i] = s.Ref()
	}
	head := present + " " + lowerCount(len(asked))
	if len(hits) > len(asked) {
		head = present + " the first " + lowerCount(len(asked)) + " of " + lowerCount(len(hits))
	}
	list := strings.Join(firstN(names, maxSpokenItems), ", ")
	if len(names) > maxSpokenItems {
		list += fmt.Sprintf(", and %d more on screen", len(names)-maxSpokenItems)
	}
	prompt := head + ": " + list + ". Yes?"
	r.Say = prompt
	r.Show = Show{Kind: "confirm", Items: items(asked, d.now())}
	return d.pend(req, r, Pending{Profile: req.Profile, Kind: "yes", Action: action, Intent: p.Intent, Lang: p.Lang,
		Targets: refs, Left: len(hits) - len(asked)}, prompt)
}

// actPhase2 carries out a confirmed launch or a lifecycle verb on several sessions.
func (d *Desk) actPhase2(ctx context.Context, req Request, p Pending, lang string) (Result, error) {
	r := d.base(IntentConfirmYes, lang, req.DryRun)
	if p.Action == actionLaunch {
		if p.Launch == nil {
			refuse(&r, RefusedExpired, sayTooLate)
			return r, nil
		}
		name := spokenTitle(p.Launch.Title, p.Launch.Host)
		if req.DryRun {
			r.Say = "Started " + name + "."
			return r, nil
		}
		s, err := d.Deck.Launch(ctx, *p.Launch)
		if err != nil {
			r.Say = "Could not start " + name + "."
			return r, nil
		}
		t := s.Ref()
		r.Target = &t
		r.Say = "Started " + name + "."
		return r, nil
	}
	var ss []Session
	for _, t := range p.Targets {
		ss = append(ss, Session{ID: t.ID, Title: t.Title, Host: t.Host, Profile: t.Profile})
	}
	d.lifecycle(ctx, req, &r, p.Action, ss)
	if p.Left > 0 {
		r.Say += fmt.Sprintf(" %s more are left; say it again for them.", capitalize(lowerCount(p.Left)))
	}
	return r, nil
}

// lifecycle runs stop / restart / archive on sessions that are still his,
// after looking each one up again.
func (d *Desk) lifecycle(ctx context.Context, req Request, r *Result, action string, ss []Session) {
	verb, past := action, ""
	switch action {
	case actionStop:
		past = "Stopped"
	case actionRestart:
		past = "Restarted"
	case actionArchive:
		past = "Archived"
	}
	var done, failed []string
	for _, s := range ss {
		name := spokenTitle(s.Title, s.Host)
		if _, why, _ := d.recheckFresh(ctx, s, false); why != "" {
			failed = append(failed, name)
			continue
		}
		if !req.DryRun {
			if err := d.Deck.Act(ctx, s, verb); err != nil {
				failed = append(failed, name)
				continue
			}
		}
		done = append(done, name)
	}
	if len(ss) == 1 && r.Target == nil {
		t := ss[0].Ref()
		r.Target = &t
	}
	switch {
	case len(done) == 0:
		r.Say = "Nothing done: " + strings.Join(firstN(failed, maxSpokenItems), ", ") + " could not be reached."
	case len(ss) == 1:
		r.Say = past + " " + done[0] + "."
	default:
		r.Say = past + " " + lowerCount(len(done)) + "."
		if len(failed) > 0 {
			r.Say += " Not done: " + strings.Join(firstN(failed, maxSpokenItems), ", ") + "."
		}
	}
}
