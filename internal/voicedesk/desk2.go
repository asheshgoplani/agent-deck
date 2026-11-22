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
	case IntentTodoAdd, IntentTodoList, IntentTodoDone:
		return d.runTodo(ctx, req, r, p)
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
	hits = d.hisHits(ctx, hits)
	if len(hits) == 0 {
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

// hisHits keeps the search hits whose deck session is his by the owner
// rules (the same rules as stop and archive; an archived session of his
// still counts: it is his conversation). Recall indexes every conversation
// on the machine and its remotes, so a hit with no deck session, or with a
// deck id the session list does not know, could be anyone's: it is dropped.
// When no hit is left the answer is "nothing found", which says nothing
// about other people's conversations.
func (d *Desk) hisHits(ctx context.Context, hits []SearchHit) []SearchHit {
	all, _ := d.sessions(ctx)
	var out []SearchHit
	for _, h := range hits {
		if h.DeckID == "" {
			continue
		}
		for _, s := range all {
			if s.ID != h.DeckID || s.Host != h.Host {
				continue
			}
			if k, _ := d.Config.ownerOf(s); k == ownerMine {
				out = append(out, h)
			}
			break
		}
	}
	return out
}

// hisKids keeps the workers that are his sessions. A child carries no
// group, so each one is looked up in the session list; a child the list
// does not know, or someone else's (a worker another person started under
// his conductor), is left out.
func (d *Desk) hisKids(ctx context.Context, kids []Session) []Session {
	if len(kids) == 0 {
		return kids
	}
	all, _ := d.sessions(ctx)
	var out []Session
	for _, k := range kids {
		for _, s := range all {
			if s.ID == k.ID && s.Host == k.Host {
				if kind, _ := d.Config.ownerOf(s); kind == ownerMine && !s.Archived {
					out = append(out, k)
				}
				break
			}
		}
	}
	return out
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
	// "test one in workers on boxone": the host comes after the group
	spokenGroup := p.Group
	if gw := splitWords(spokenGroup); spec.Host == "" && len(gw) >= 3 && hostLead[gw[len(gw)-2].folded] {
		for _, h := range hosts {
			if h != "" && gw[len(gw)-1].folded == foldPhrase(h) {
				spec.Host, spokenGroup = h, rawJoin(gw[:len(gw)-2])
			}
		}
	}
	tw = trimNameNoise(tw)
	if len(tw) == 0 {
		return notMine(p.Lang), nil
	}
	if !plainName(p.Title) {
		refuse(&r, RefusedUnsafe, "A session name can only use plain words; type it instead.")
		return r, nil
	}
	spec.Title = kebab(tw)
	if HasSecret(spec.Title) {
		refuse(&r, RefusedUnsafe, "That name looks like a secret; type it instead.")
		return r, nil
	}
	group, profile, ok := d.launchGroup(spokenGroup, req.On, spec.Host, all)
	if !ok {
		refuse(&r, RefusedNotFound, "I found no group of yours called "+spokenGroup+".")
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
	// The folder is the group's own default folder, read by the deck; with
	// none, nothing starts (no guessed folder, never a heard path).
	folder, line := "", ""
	if pl, ok := d.Deck.(LaunchPlanner); ok {
		planned, err := pl.PlanLaunch(ctx, spec)
		switch {
		case errors.Is(err, ErrNoFolder):
			g := spec.Group
			if g == "" {
				g = "this group"
			}
			refuse(&r, RefusedNotFound, "The group "+g+" has no default folder, so I will not start it. Set one on a screen first.")
			return r, nil
		case err != nil:
			r.Say = "I could not read the folder of " + spec.Group + "."
			return r, nil
		}
		spec = planned
		folder = ", in folder " + spokenFolder(spec.Path) + ", with " + spec.Tool
		line = " · " + spec.Path + " · " + spec.Tool
	}
	prompt := "Start a new session " + spokenTitle(spec.Title, "") + where + folder + "? Yes?"
	r.Say = prompt
	r.Show = Show{Kind: "confirm", Items: []ShowItem{{Title: spec.Title, Host: spec.Host, Line: "new session" + where + line}}}
	return d.pend(req, r, Pending{Profile: req.Profile, Kind: "yes", Action: actionLaunch, Intent: IntentLaunch, Lang: p.Lang, Launch: &spec}, prompt)
}

// plainName: a spoken name is words, not a path, a flag or a shell
// command ("../../etc/passwd", "foo; rm", "--cmd bash"). Such a name is
// refused with its own line; a secret is still refused as a secret.
func plainName(title string) bool {
	t := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(title), ".?!"))
	if strings.ContainsAny(t, "/\\;|&$`\"<>(){}[]*~#=\n\r") || strings.Contains(t, "..") {
		return false
	}
	for _, f := range strings.Fields(t) {
		if strings.HasPrefix(f, "-") {
			return false
		}
	}
	return true
}

// spokenFolder is the last part of a folder, said as words.
func spokenFolder(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	if p == "" {
		return "root"
	}
	return spokenTitle(p, "")
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
		if err == nil {
			kids = d.hisKids(ctx, kids)
		}
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
// asks once for at most five of them. Every session the yes acts on is
// named in the question (on glasses there is no screen). A conductor and
// the session the mic is on are left out of a bulk list unless the words
// name them ("stop all conductors", or the session's whole title).
func (d *Desk) many(ctx context.Context, req Request, r Result, p Parsed, q nameQuery, all []Session) (Result, error) {
	action, present, _ := verbOf(p.Intent)
	if action == "" || len(q.words) == 0 {
		return notMine(p.Lang), nil
	}
	var hits, matched, archived, left []Session
	for _, s := range all {
		if k, _ := d.Config.ownerOf(s); k != ownerMine {
			continue
		}
		if q.host != "" && s.Host != q.host {
			continue
		}
		if ok, level, _ := coverage(q, s); !ok || level < wordPhonetic {
			continue
		}
		if s.Archived {
			archived = append(archived, s)
			continue
		}
		matched = append(matched, s)
		if p.Intent == IntentStop && s.Status == "stopped" {
			continue
		}
		if (isConductor(s) && !namesConductor(q, s)) || (req.On != "" && s.ID == req.On && !namesWhole(q, s)) {
			left = append(left, s)
			continue
		}
		hits = append(hits, s)
	}
	if len(hits) == 0 {
		name := strings.TrimSpace(q.raw)
		switch {
		case len(left) > 0:
			names := make([]string, len(left))
			for i, s := range left {
				names[i] = spokenTitle(s.Title, s.Host)
			}
			refuse(&r, RefusedUnsafe, "Nothing done: I leave "+strings.Join(firstN(names, maxSpokenItems), ", ")+
				" out of a group "+strings.ToLower(present)+"; name it on its own.")
			return r, nil
		case len(matched) > 0 && p.Intent == IntentStop:
			r.Say = "No " + name + " are running."
			return r, nil
		case len(matched) == 0 && len(archived) > 0:
			switch p.Intent {
			case IntentStop:
				r.Say = "No " + name + " are running."
			case IntentArchive:
				r.Say = "No " + name + " left to archive."
			default:
				r.Say = "No " + name + " left to restart; they are archived."
			}
			return r, nil
		}
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
	// every name is spoken: the yes acts on exactly these
	prompt := head + ": " + strings.Join(names, ", ") + ". Yes?"
	r.Say = prompt
	r.Show = Show{Kind: "confirm", Items: items(asked, d.now())}
	return d.pend(req, r, Pending{Profile: req.Profile, Kind: "yes", Action: action, Intent: p.Intent, Lang: p.Lang,
		Targets: refs, Left: len(hits) - len(asked)}, prompt)
}

// isConductor: agent-deck titles a conductor "conductor-<name>".
func isConductor(s Session) bool {
	t := strings.ToLower(cleanTitle(s.Title))
	return t == "conductor" || strings.HasPrefix(t, "conductor-")
}

// namesConductor: the words say "conductor(s)" or the whole title.
func namesConductor(q nameQuery, s Session) bool {
	for _, w := range q.words {
		if w == foldPhrase("conductor") || w == foldPhrase("conductors") {
			return true
		}
	}
	return namesWhole(q, s)
}

// namesWhole: every word of the session's title is said.
func namesWhole(q nameQuery, s Session) bool {
	return saysWholeTitle(q, resolution{Kind: resolvedOne, Session: s})
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
