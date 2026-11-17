// Package voicedesk is the voice desk: it turns one heard sentence into a
// deck action (who needs me, status, read, tell, find, overview, list),
// applies the safety rules and returns the short line to speak. The
// contract is docs/voice.md ("voice do"). All deck access goes through the
// Deck interface, so the rules are tested on a saved session list.
package voicedesk

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Deck is everything the desk needs from agent-deck.
type Deck interface {
	// Sessions lists every session of this machine (all profiles) and of
	// every configured remote. An unreachable host is an error entry.
	Sessions(ctx context.Context) ([]Session, []HostError)
	// NeedsMe lists the waiting sessions, longest-waiting first.
	NeedsMe(ctx context.Context) ([]Session, []HostError)
	// LastReply is the text of the session's last reply.
	LastReply(ctx context.Context, s Session) (string, error)
	// Send delivers a message into the session.
	Send(ctx context.Context, s Session, message string) error
	// Show asks the screen (the Mac app) to show the session.
	Show(ctx context.Context, s Session) error
}

// HostError is one host that did not answer.
type HostError struct {
	Host  string `json:"host"`
	Error string `json:"error"`
}

// Request is one `voice do` call.
type Request struct {
	Sentence       string
	Profile        string // resolved profile: tokens are bound to it
	From           string // mac | glasses | phone | text
	On             string // id of the session the mic is on ("" = none)
	Pending        string // token of the question being answered
	AnswerDelayMs  int64
	HasAnswerDelay bool
	DryRun         bool
}

// Result is the one JSON object `voice do --json` prints (CONTRACT.md 2).
type Result struct {
	Handled bool     `json:"handled"`
	Intent  string   `json:"intent"`
	Lang    string   `json:"lang"`
	Target  *Ref     `json:"target"`
	Say     string   `json:"say"`
	Show    Show     `json:"show"`
	Confirm *Confirm `json:"confirm"`
	Then    *Then    `json:"then"`
	Refused *string  `json:"refused"`
	DryRun  bool     `json:"dryRun"`
}

// Show is what screens display.
type Show struct {
	Kind  string     `json:"kind"` // sessions | confirm | none
	Items []ShowItem `json:"items"`
}

// ShowItem is one row on screen.
type ShowItem struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Host   string `json:"host"`
	Status string `json:"status"`
	Line   string `json:"line"`
}

// Confirm is a question that waits for the next sentence.
type Confirm struct {
	Token     string `json:"token"`
	Kind      string `json:"kind"` // yes | choice
	Prompt    string `json:"prompt"`
	ExpiresMs int64  `json:"expiresMs"`
}

// Then tells the caller what to do after speaking.
type Then struct {
	Action string `json:"action"` // switch
	Target Ref    `json:"target"`
}

// Refusal reasons.
const (
	RefusedOtherPerson = "other-person"
	RefusedNotFound    = "not-found"
	RefusedAmbiguous   = "ambiguous"
	RefusedUnsafe      = "unsafe"
	RefusedExpired     = "expired"
)

// Spoken lines that are fixed.
const (
	sayTooLate     = "Too late, nothing sent."
	sayCancelled   = "Cancelled."
	saySecretReply = "There is a secret in this reply, read it on screen."                                 //nolint:gosec // G101: a spoken line, not a credential
	saySecretTell  = "There is a secret in this message, so I will not send it by voice. Type it instead." //nolint:gosec // G101: a spoken line, not a credential
)

// Desk runs requests.
type Desk struct {
	Deck   Deck
	Store  Store
	Config Config
	Now    func() time.Time
}

func (d *Desk) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func notMine(lang string) Result {
	if lang == "" {
		lang = LangEN
	}
	return Result{Handled: false, Intent: IntentNone, Lang: lang, Show: Show{Kind: "none", Items: []ShowItem{}}}
}

func (d *Desk) base(intent, lang string, dry bool) Result {
	return Result{Handled: true, Intent: intent, Lang: lang, Show: Show{Kind: "none", Items: []ShowItem{}}, DryRun: dry}
}

func refuse(r *Result, why, say string) {
	r.Refused = &why
	r.Say = say
}

// Do handles one sentence. It never returns handled=true with an empty say,
// and nothing that looks like a secret leaves it in say or show.
func (d *Desk) Do(ctx context.Context, req Request) (Result, error) {
	req.Sentence = strings.ToValidUTF8(req.Sentence, " ")
	if req.Pending != "" || Parse(req.Sentence).Intent != IntentNone {
		d.Store.Sweep()
	}
	r, err := d.do(ctx, req)
	if scrub(&r) && r.Confirm != nil {
		// a question whose words held a secret is not asked: a yes to a
		// prompt he did not hear must not act
		if !req.DryRun {
			_, _ = d.Store.Take(r.Confirm.Token)
		}
		r.Confirm = nil
		why := RefusedUnsafe
		r.Refused = &why
	}
	return r, err
}

// scrub is the last check before anything is spoken or shown: a line that
// holds a secret is dropped from show, a say that holds one is replaced.
func scrub(r *Result) (hit bool) {
	if HasSecret(r.Say) {
		r.Say = saySecretReply
		hit = true
	}
	if r.Confirm != nil && HasSecret(r.Confirm.Prompt) {
		r.Confirm.Prompt = saySecretReply
		hit = true
	}
	for i := range r.Show.Items {
		if HasSecret(r.Show.Items[i].Line) {
			r.Show.Items[i].Line = ""
		}
		if HasSecret(r.Show.Items[i].Title) {
			r.Show.Items[i].Title = ""
		}
	}
	if r.Target != nil && HasSecret(r.Target.Title) {
		t := *r.Target
		t.Title = ""
		r.Target = &t
		hit = true
	}
	if r.Then != nil && HasSecret(r.Then.Target.Title) {
		r.Then.Target.Title = ""
		hit = true
	}
	if r.Show.Items == nil {
		r.Show.Items = []ShowItem{}
	}
	if r.Show.Kind == "" {
		r.Show.Kind = "none"
	}
	if r.Lang == "" {
		r.Lang = LangEN
	}
	if r.Handled && strings.TrimSpace(r.Say) == "" {
		r.Say = "Done."
	}
	return hit
}

func (d *Desk) do(ctx context.Context, req Request) (Result, error) {
	if req.Pending != "" {
		if r, done, err := d.answer(ctx, req); done || err != nil {
			return r, err
		}
	}
	p := Parse(req.Sentence)
	if p.Intent == IntentNone {
		return notMine(p.Lang), nil // decided before any session listing
	}
	return d.run(ctx, req, p)
}

// answer handles a sentence given with --pending. done=false: the sentence
// is not an answer; the token is dropped and the sentence is handled fresh.
func (d *Desk) answer(ctx context.Context, req Request) (Result, bool, error) {
	kind, idx := classifyAnswer(req.Sentence)
	var p Pending
	var err error
	if req.DryRun {
		p, err = d.Store.Peek(req.Pending)
	} else {
		p, err = d.Store.Take(req.Pending) // single use: gone from here on
	}
	lang := Parse(req.Sentence).Lang
	if err != nil || !p.Valid(req.Profile) {
		switch kind {
		case AnswerYes, AnswerChoice:
			r := d.base(answerIntent(kind), lang, req.DryRun)
			refuse(&r, RefusedExpired, sayTooLate)
			return r, true, nil
		case AnswerNo:
			r := d.base(IntentConfirmNo, lang, req.DryRun)
			r.Say = sayCancelled
			return r, true, nil
		}
		return Result{}, false, nil
	}
	if p.Lang != "" && lang == LangEN && !hasDeva(req.Sentence) {
		lang = p.Lang
	}
	if kind == AnswerNone && p.Kind == "choice" {
		// a session's name as the choice
		if i := choiceByName(req.Sentence, p.Choices); i >= 0 {
			kind, idx = AnswerChoice, i
		}
	}
	if kind == AnswerNone {
		return Result{}, false, nil // dropped; fresh sentence
	}
	if kind == AnswerNo {
		r := d.base(IntentConfirmNo, lang, req.DryRun)
		r.Say = sayCancelled
		return r, true, nil
	}
	// timing: hard TTL always; the 10 s answer window by the caller's
	// measured delay, else by the token's age
	age := d.now().Sub(p.CreatedAt)
	late := age > HardTTL || age < 0 // a clock that went back is not trusted
	if req.HasAnswerDelay {
		late = late || req.AnswerDelayMs > AnswerWindow.Milliseconds()
	} else {
		late = late || age > AnswerWindow
	}
	if late {
		r := d.base(answerIntent(kind), lang, req.DryRun)
		refuse(&r, RefusedExpired, sayTooLate)
		return r, true, nil
	}
	switch {
	case p.Kind == "yes" && kind == AnswerYes:
		r, err := d.act(ctx, req, p, lang)
		return r, true, err
	case p.Kind == "choice" && (kind == AnswerChoice || (kind == AnswerYes && len(p.Choices) == 1)):
		if kind == AnswerYes {
			idx = 0
		}
		if idx < 0 || idx >= len(p.Choices) {
			r := d.base(IntentChoice, lang, req.DryRun)
			refuse(&r, RefusedAmbiguous, fmt.Sprintf("There are only %s.", lowerCount(len(p.Choices))))
			return r, true, nil
		}
		r, err := d.chosen(ctx, req, p, p.Choices[idx], lang)
		return r, true, err
	}
	// a yes to a choice question with several options, or a choice to a
	// yes question: not an answer to this question
	r := d.base(answerIntent(kind), lang, req.DryRun)
	refuse(&r, RefusedAmbiguous, "I need the first, the second or a name.")
	if p.Kind == "yes" {
		r.Say = "Please say yes or no."
	}
	return r, true, nil
}

func answerIntent(kind string) string {
	switch kind {
	case AnswerYes:
		return IntentConfirmYes
	case AnswerNo:
		return IntentConfirmNo
	}
	return IntentChoice
}

func hasDeva(s string) bool { return hasDevanagari(s) }

func choiceByName(sentence string, choices []Ref) int {
	q := newNameQuery(sentence, nil)
	if len(q.words) == 0 {
		return -1
	}
	hit := -1
	for i, c := range choices {
		ok, level, _ := coverage(q, Session{Title: c.Title})
		if ok && level >= wordPhonetic {
			if hit >= 0 {
				return -1 // the name fits two choices
			}
			hit = i
		}
	}
	return hit
}

// act carries out a confirmed action.
func (d *Desk) act(ctx context.Context, req Request, p Pending, lang string) (Result, error) {
	if p.Target == nil {
		r := d.base(IntentConfirmYes, lang, req.DryRun)
		refuse(&r, RefusedExpired, sayTooLate)
		return r, nil
	}
	s := Session{ID: p.Target.ID, Title: p.Target.Title, Host: p.Target.Host, Profile: p.Target.Profile}
	r := d.base(IntentConfirmYes, lang, req.DryRun)
	t := s.Ref()
	r.Target = &t
	// the session may have changed in the seconds since the question
	if why, say := d.recheck(ctx, s, p.Action == actionSend); why != "" {
		refuse(&r, why, say)
		return r, nil
	}
	switch p.Action {
	case actionSend:
		if !req.DryRun {
			if err := d.Deck.Send(ctx, s, p.Message); err != nil {
				r.Say = "Could not send to " + spokenTitle(s.Title, s.Host) + "."
				return r, nil
			}
		}
		r.Say = "Sent to " + spokenTitle(s.Title, s.Host) + "."
	case actionSwitch:
		d.switchTo(ctx, req, &r, s)
	default:
		refuse(&r, RefusedExpired, sayTooLate)
	}
	return r, nil
}

// recheck looks the session up again before acting on a yes: gone,
// archived, no longer his, or (for a send) stopped or failed -> refused.
func (d *Desk) recheck(ctx context.Context, s Session, sending bool) (why, say string) {
	all, _ := d.sessions(ctx)
	name := spokenTitle(s.Title, s.Host)
	for _, x := range all {
		if x.ID != s.ID || x.Host != s.Host {
			continue
		}
		if x.Archived {
			return RefusedNotFound, name + " is archived, nothing sent."
		}
		if k, who := d.Config.ownerOf(x); k != ownerMine {
			if k == ownerOther {
				return RefusedOtherPerson, otherLine(who)
			}
			return RefusedNotFound, name + " is not yours, nothing sent."
		}
		if sending && (x.Status == "stopped" || x.Status == "error") {
			return RefusedUnsafe, name + " is " + statusWords(x.Status) + ", nothing sent."
		}
		return "", ""
	}
	return RefusedNotFound, name + " is gone, nothing sent."
}

func (d *Desk) switchTo(ctx context.Context, req Request, r *Result, s Session) {
	if !req.DryRun {
		_ = d.Deck.Show(ctx, s) // best effort: the caller still moves the mic
	}
	t := s.Ref()
	r.Target = &t
	r.Then = &Then{Action: "switch", Target: t}
	r.Say = "Switching to " + spokenTitle(s.Title, s.Host) + "."
}

// chosen continues the original intent with the session he picked.
func (d *Desk) chosen(ctx context.Context, req Request, p Pending, c Ref, lang string) (Result, error) {
	s := Session{ID: c.ID, Title: c.Title, Host: c.Host, Profile: c.Profile}
	// refresh status etc. from the deck when the session is still there
	all, _ := d.sessions(ctx)
	found := false
	for _, x := range all {
		if x.ID == c.ID && x.Host == c.Host && !x.Archived {
			s, found = x, true
			break
		}
	}
	if !found {
		r := d.base(IntentChoice, lang, req.DryRun)
		refuse(&r, RefusedNotFound, spokenTitle(c.Title, c.Host)+" is gone.")
		return r, nil
	}
	if k, who := d.Config.ownerOf(s); k != ownerMine {
		r := d.base(IntentChoice, lang, req.DryRun)
		refuse(&r, RefusedOtherPerson, otherLine(who))
		return r, nil
	}
	pp := Parsed{Intent: p.Intent, Lang: lang, Message: p.Message}
	r, err := d.withTarget(ctx, req, pp, s, true)
	r.Intent = IntentChoice // the answer to a choice question (CONTRACT.md 2)
	return r, err
}

func otherLine(who string) string {
	if who == "" {
		return "That is someone else's session."
	}
	return "That is " + capitalize(who) + "'s session."
}

// run handles a fresh, parsed sentence.
func (d *Desk) run(ctx context.Context, req Request, p Parsed) (Result, error) {
	r := d.base(p.Intent, p.Lang, req.DryRun)
	switch p.Intent {
	case IntentNeedsMe:
		rows, errs := d.needsMeRows(ctx)
		d.needsMe(&r, d.Config.mine(rows), errs)
		return r, nil
	case IntentOverview, IntentList:
		all, errs := d.sessions(ctx)
		mine := d.Config.mine(all)
		if p.Intent == IntentOverview {
			d.overview(&r, mine, errs)
		} else {
			d.list(&r, mine, errs)
		}
		return r, nil
	}
	all, _ := d.sessions(ctx)
	q := newNameQuery(p.Name, hostsOf(all))
	var res resolution
	if p.Intent == IntentFind {
		res = findAll(q, all, d.Config)
	} else {
		res = resolve(q, all, d.Config)
	}
	// "status of the build", "test ko bolo ki ...": a name of only coding
	// nouns is talk unless it says a session's whole title
	if genericOnly(q.words) && (res.Kind == resolvedOne || res.Kind == resolvedAmbiguous) && !saysWholeTitle(q, res) {
		return notMine(p.Lang), nil
	}
	switch res.Kind {
	case resolvedOther:
		refuse(&r, RefusedOtherPerson, otherLine(res.Person))
		return r, nil
	case resolvedNotFound:
		if p.Soft || talkLike(q.words) {
			return notMine(p.Lang), nil
		}
		refuse(&r, RefusedNotFound, "I found no session of yours called "+strings.TrimSpace(q.raw)+".")
		return r, nil
	case resolvedAmbiguous:
		return d.ask(req, r, p, res)
	}
	p.Phonetic = res.Phonetic
	return d.withTarget(ctx, req, p, res.Session, false)
}

// ask turns several candidates into a choice question.
func (d *Desk) ask(req Request, r Result, p Parsed, res resolution) (Result, error) {
	choices := res.Choices
	if len(choices) > 3 {
		choices = choices[:3]
	}
	names := make([]string, len(choices))
	for i, c := range choices {
		names[i] = spokenTitle(c.Title, c.Host)
	}
	var prompt string
	switch {
	case p.Intent == IntentFind && res.Sure:
		prompt = spokenChoices(names, len(res.Choices))
	case len(names) == 1:
		prompt = "Did you mean " + names[0] + "?"
	case len(names) == 2:
		prompt = "Did you mean " + names[0] + " or " + names[1] + "?"
	default:
		prompt = "Did you mean " + strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1] + "?"
	}
	if p.Intent != IntentFind || !res.Sure {
		why := RefusedAmbiguous
		r.Refused = &why
	}
	r.Say = prompt
	r.Show = Show{Kind: "sessions", Items: items(res.Choices, d.now())}
	refs := make([]Ref, len(choices))
	for i, c := range choices {
		refs[i] = c.Ref()
	}
	pend := Pending{Profile: req.Profile, Kind: "choice", Action: actionChoose, Intent: p.Intent, Lang: p.Lang,
		Choices: refs, Message: p.Message}
	return d.pend(req, r, pend, prompt)
}

func spokenChoices(names []string, total int) string {
	ord := []string{"First", "second", "third"}
	line := countWord(total) + ": " + strings.Join(names, ", ")
	if total > len(names) {
		line += fmt.Sprintf(", and %d more on screen", total-len(names))
	}
	q := ord[0]
	for i := 1; i < len(names); i++ {
		if i == len(names)-1 {
			q += " or " + ord[i]
		} else {
			q += ", " + ord[i]
		}
	}
	return line + ". " + q + "?"
}

// pend stores a question (unless dry-run) and puts it in the result.
func (d *Desk) pend(req Request, r Result, p Pending, prompt string) (Result, error) {
	tok, err := NewToken()
	if err != nil {
		return r, err
	}
	p.Token, p.CreatedAt = tok, d.now()
	p.Seal()
	if !req.DryRun {
		if err := d.Store.Put(p); err != nil {
			return r, fmt.Errorf("saving the pending question: %w", err)
		}
	}
	r.Confirm = &Confirm{Token: tok, Kind: p.Kind, Prompt: prompt, ExpiresMs: AnswerWindow.Milliseconds()}
	return r, nil
}

// withTarget runs an intent on one resolved session of his.
func (d *Desk) withTarget(ctx context.Context, req Request, p Parsed, s Session, picked bool) (Result, error) {
	r := d.base(p.Intent, p.Lang, req.DryRun)
	t := s.Ref()
	r.Target = &t
	name := spokenTitle(s.Title, s.Host)
	switch p.Intent {
	case IntentStatus:
		line := name + " is " + statusWords(s.Status)
		if a := ago(s.LastActivity, d.now()); a != "" {
			if s.Status == "running" {
				line += ", " + a + " in"
			} else {
				line += ", " + a
			}
		}
		line += "."
		item := ShowItem{ID: s.ID, Title: s.Title, Host: s.Host, Status: s.Status, Line: statusWords(s.Status)}
		if reply, err := d.Deck.LastReply(ctx, s); err == nil && strings.TrimSpace(reply) != "" {
			if HasSecret(reply) {
				line += " " + saySecretReply
			} else if last := firstSentences(Speakable(reply), 1, 140); last != "" {
				line += " Last: " + ensureStop(last)
				item.Line = last
			}
		}
		r.Say = line
		r.Show = Show{Kind: "sessions", Items: []ShowItem{item}}
	case IntentRead:
		reply, err := d.Deck.LastReply(ctx, s)
		switch {
		case err != nil:
			r.Say = "I could not read " + name + "."
		case strings.TrimSpace(reply) == "":
			r.Say = name + " has no reply yet."
		case HasSecret(reply):
			r.Say = saySecretReply
		default:
			text := Speakable(reply)
			if text == "" {
				r.Say = name + "'s last reply is only code, read it on screen."
			} else if p.Phonetic {
				// found by sound only: say whose reply it is first
				r.Say = capitalize(name) + ": " + ensureStop(text)
				r.Show = Show{Kind: "sessions", Items: []ShowItem{{ID: s.ID, Title: s.Title, Host: s.Host, Status: s.Status, Line: text}}}
			} else {
				r.Say = capitalize(ensureStop(text))
				r.Show = Show{Kind: "sessions", Items: []ShowItem{{ID: s.ID, Title: s.Title, Host: s.Host, Status: s.Status, Line: text}}}
			}
		}
	case IntentTell:
		return d.tell(ctx, req, r, p, s, picked)
	case IntentFind:
		if picked {
			d.switchTo(ctx, req, &r, s)
			return r, nil
		}
		prompt := "Found " + name + ". Switch to it?"
		r.Say = prompt
		r.Show = Show{Kind: "sessions", Items: items([]Session{s}, d.now())}
		return d.pend(req, r, Pending{Profile: req.Profile, Kind: "yes", Action: actionSwitch, Intent: IntentFind, Lang: p.Lang, Target: &t}, prompt)
	default:
		return r, errors.New("intent without a target: " + p.Intent)
	}
	return r, nil
}

// tell applies the send rules: no secrets, no stopped sessions, a yes
// unless it goes to the session he is on and holds no risky word.
func (d *Desk) tell(ctx context.Context, req Request, r Result, p Parsed, s Session, picked bool) (Result, error) {
	name := spokenTitle(s.Title, s.Host)
	msg := strings.TrimSpace(p.Message)
	if HasSecret(msg) {
		refuse(&r, RefusedUnsafe, saySecretTell)
		return r, nil
	}
	switch s.Status {
	case "stopped", "error":
		refuse(&r, RefusedUnsafe, name+" is "+statusWords(s.Status)+", nothing sent.")
		return r, nil
	}
	onIt := req.On != "" && req.On == s.ID
	// after a choice the message was never read back: always ask
	if onIt && !risky(msg) && !picked {
		if !req.DryRun {
			if err := d.Deck.Send(ctx, s, msg); err != nil {
				r.Say = "Could not send to " + name + "."
				return r, nil
			}
		}
		r.Say = "Sent to " + name + "."
		return r, nil
	}
	spoken := firstSentences(msg, 3, 200)
	prompt := "Send to " + name + ": " + strings.TrimRight(spoken, ".!?…") + ". Yes?"
	r.Say = prompt
	t := s.Ref()
	r.Show = Show{Kind: "confirm", Items: []ShowItem{{ID: s.ID, Title: s.Title, Host: s.Host, Status: s.Status, Line: msg}}}
	return d.pend(req, r, Pending{Profile: req.Profile, Kind: "yes", Action: actionSend, Intent: IntentTell, Lang: p.Lang, Target: &t, Message: msg}, prompt)
}

func hostsOf(all []Session) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range all {
		if s.Host != "" && !seen[s.Host] {
			seen[s.Host] = true
			out = append(out, s.Host)
		}
	}
	return out
}

func items(ss []Session, now time.Time) []ShowItem {
	out := make([]ShowItem, 0, len(ss))
	for _, s := range ss {
		line := statusWords(s.Status)
		if s.Reason != "" {
			line = s.Reason
		}
		if a := ago(s.LastActivity, now); a != "" && s.Reason == "" {
			line += ", " + a
		}
		out = append(out, ShowItem{ID: s.ID, Title: s.Title, Host: s.Host, Status: s.Status, Line: line})
	}
	return out
}

func unreachable(errs []HostError) string {
	if len(errs) == 0 {
		return ""
	}
	hosts := make([]string, 0, len(errs))
	for _, e := range errs {
		if e.Host != "" {
			hosts = append(hosts, e.Host)
		}
	}
	if len(hosts) == 0 {
		return ""
	}
	sort.Strings(hosts)
	if len(hosts) == 1 {
		return " " + hosts[0] + " did not answer."
	}
	return " " + strings.Join(hosts, " and ") + " did not answer."
}

func (d *Desk) needsMe(r *Result, rows []Session, errs []HostError) {
	r.Show = Show{Kind: "sessions", Items: items(rows, d.now())}
	if len(rows) == 0 {
		r.Say = "Nobody is waiting for you." + unreachable(errs)
		return
	}
	names := make([]string, len(rows))
	for i, s := range rows {
		names[i] = spokenTitle(s.Title, s.Host)
	}
	r.Say = spokenList(names) + unreachable(errs)
}

// sessionOrder: waiting first, then working, failed, idle, the rest; then
// by title.
func sessionOrder(ss []Session) {
	rank := map[string]int{"waiting": 0, "running": 1, "error": 2, "idle": 3, "starting": 4, "stopped": 5}
	sort.SliceStable(ss, func(i, j int) bool {
		ri, ok := rank[ss[i].Status]
		if !ok {
			ri = 6
		}
		rj, ok := rank[ss[j].Status]
		if !ok {
			rj = 6
		}
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(ss[i].Title) < strings.ToLower(ss[j].Title)
	})
}

func (d *Desk) list(r *Result, mine []Session, errs []HostError) {
	sessionOrder(mine)
	r.Show = Show{Kind: "sessions", Items: items(mine, d.now())}
	if len(mine) == 0 {
		r.Say = "You have no sessions." + unreachable(errs)
		return
	}
	names := make([]string, len(mine))
	for i, s := range mine {
		names[i] = spokenTitle(s.Title, s.Host)
	}
	r.Say = spokenList(names) + unreachable(errs)
}

func (d *Desk) overview(r *Result, mine []Session, errs []HostError) {
	sessionOrder(mine)
	r.Show = Show{Kind: "sessions", Items: items(mine, d.now())}
	var running, waiting, idle int
	var failed []string
	for _, s := range mine {
		switch s.Status {
		case "running":
			running++
		case "waiting":
			waiting++
		case "error":
			failed = append(failed, spokenTitle(s.Title, s.Host))
		case "idle":
			idle++
		}
	}
	if len(mine) == 0 {
		r.Say = "You have no sessions." + unreachable(errs)
		return
	}
	var parts []string
	if running > 0 {
		parts = append(parts, lowerCount(running)+" working")
	}
	if waiting > 0 {
		parts = append(parts, lowerCount(waiting)+" waiting for you")
	}
	if len(failed) > 0 {
		f := lowerCount(len(failed)) + " failed: " + strings.Join(firstN(failed, 3), ", ")
		if len(failed) > 3 {
			f += fmt.Sprintf(", and %d more on screen", len(failed)-3)
		}
		parts = append(parts, f)
	}
	if len(parts) == 0 {
		r.Say = fmt.Sprintf("Nothing is working, %s idle.", lowerCount(idle)) + unreachable(errs)
		return
	}
	r.Say = capitalize(strings.Join(parts, ", ")) + "." + unreachable(errs)
}

func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

// saysWholeTitle: the spoken words cover every word of the title of the
// resolved session (or of one of the choices).
func saysWholeTitle(q nameQuery, res resolution) bool {
	cands := res.Choices
	if res.Kind == resolvedOne {
		cands = []Session{res.Session}
	}
	for _, s := range cands {
		all := true
		for _, tw := range splitWords(s.Title) {
			if wordMeets(tw.folded, tw.roman, false, q.words, q.roman) != wordSure {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
