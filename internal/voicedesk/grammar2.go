package voicedesk

import (
	"context"
	"strings"
)

// Phase 2 intents (CONTRACT-PHASE2.md).
const (
	IntentLaunch   = "launch"
	IntentStop     = "stop"
	IntentRestart  = "restart"
	IntentArchive  = "archive"
	IntentSearch   = "search"
	IntentWorkers  = "workers"
	IntentNext     = "next"
	IntentRefuse   = "refuse" // delete / remove a session: never by voice
	IntentTodoAdd  = "todo_add"
	IntentTodoList = "todo_list"
	IntentTodoDone = "todo_done"
)

type patternAdder struct{ ps []pattern }

func (a *patternAdder) add(intent, lang string, soft, all bool, specs ...string) {
	for _, s := range specs {
		p := compile(intent, lang, soft, s)
		p.all = all
		a.ps = append(a.ps, p)
	}
}

// grammarEarly runs before the phase-1 forms: these sentences would
// otherwise read as status ("deck ke workers kya kar rahe hain") or find
// ("find where we talked about the invoice").
func grammarEarly() []pattern {
	var a patternAdder
	grammarTodos(&a)
	grammarOpenPage(&a)
	// workers of a session
	a.add(IntentWorkers, LangEN, true,
		false,
		"what are [the] {name} workers doing", "what are the workers of {name} doing",
		"how are [the] {name} workers doing", "[show|list] [me] [the] workers of {name}",
		"what are [the] {name} children doing",
	)
	a.add(IntentWorkers, LangHI, true, false,
		"{name} ke workers kya kar rahe hai|hain", "{name} ke workers batao|dikhao|sunao",
		"{name} ke workers ka haal|status batao",
	)
	// search what was said
	a.add(IntentSearch, LangEN, false, false,
		"where|when did we talk|speak about {msg}", "where|when did we discuss {msg}",
		"search what was said about {msg}", "search [the] conversations|transcripts for|about {msg}",
		"find where we talked|spoke about {msg}", "find where we discussed {msg}",
		"what did we say about {msg}",
	)
	a.add(IntentSearch, LangHI, false, false,
		"{msg} ki|ke baat kahan|kab hui [thi]", "{msg} ke bare|baare mein kahan|kab baat hui [thi]",
		"{msg} ke bare|baare mein kya baat hui [thi]",
	)
	// the next one that needs me
	// (an optional part is one word: "[that needs me]" would never match)
	a.add(IntentNext, LangEN, false, false,
		"take me to the next one", "next one that needs me", "go to the next one", "go to the next one that needs me",
		"take me to the next waiting session", "next waiting session", "who is next",
		"show me the next one", "show me the next one that needs me",
		"next waiting [session|one]", "next one", "next session", "go to [the] next waiting [session|one]",
		"take me to [the] next waiting",
	)
	a.add(IntentNext, LangHI, false, false,
		"agla|agle wala|wale kholo|dikhao", "agle wale pe|par chalo|jao", "agli session kholo|dikhao",
		"agla kaun hai", "agla|agli|agle waiting [session|wala|wali] [kholo|dikhao]",
	)
	// launch a new session
	for _, verb := range []string{"start", "launch", "create", "open", "make"} {
		a.add(IntentLaunch, LangEN, false, false,
			verb+" [a] new session called|named {title} in|under [the] {group} [group|folder]",
			verb+" [a] new session called|named {title}",
		)
	}
	a.add(IntentLaunch, LangEN, false, false,
		"new session called|named {title} in|under [the] {group} [group|folder]", "new session called|named {title}",
	)
	a.add(IntentLaunch, LangHI, false, false,
		"[ek] nayi|naya|nai session|seshan shuru|start|banao|kholo [karo|kar] [do] naam|name {title} {group} mein",
		"[ek] nayi|naya|nai session|seshan shuru|start|banao|kholo [karo|kar] [do] naam|name {title}",
		"{group} mein [ek] nayi|naya|nai session|seshan shuru|start|banao|kholo [karo|kar] [do] naam|name {title}",
		"{title} naam ki|ka [ek] nayi|naya|nai session|seshan shuru|start|banao|kholo [karo|kar] [do]",
		"{group} mein {title} naam ki|ka [ek] nayi|naya|nai session|seshan shuru|start|banao|kholo [karo|kar] [do]",
	)
	// stop / restart / archive, all sessions the name fits
	a.add(IntentStop, LangEN, true, true, "stop all [the|my] {name}", "kill all [the|my] {name}")
	a.add(IntentRestart, LangEN, true, true, "restart all [the|my] {name}")
	a.add(IntentArchive, LangEN, true, true, "archive all [the|my] {name}")
	a.add(IntentStop, LangHI, true, true, "sab|saare|sare|saari {name} band|rok [karo|kar|kardo] [do]", "{name} sab band|rok [karo|kar|kardo] [do]")
	a.add(IntentRestart, LangHI, true, true, "sab|saare|sare|saari {name} restart [karo|kar|kardo] [do]")
	a.add(IntentArchive, LangHI, true, true, "sab|saare|sare|saari {name} archive [karo|kar|kardo] [do]")
	return a.ps
}

// grammarLate runs after the phase-1 forms.
func grammarLate() []pattern {
	var a patternAdder
	// one session (an unknown name is not mine: "stop listening" is the daemon's)
	a.add(IntentStop, LangEN, true, false, "stop [the|my] {name} [session]", "kill [the|my] {name} session")
	a.add(IntentRestart, LangEN, true, false, "restart [the|my] {name} [session]")
	a.add(IntentArchive, LangEN, true, false, "archive [the|my] {name} [session]")
	a.add(IntentStop, LangHI, true, false, "{name} [ko] band|rok [karo|kar|kardo] [do]", "{name} [ko] stop [karo|kar|kardo] [do]")
	a.add(IntentRestart, LangHI, true, false, "{name} [ko] restart [karo|kar|kardo] [do]", "{name} [ko] phir|fir|dobara [se] shuru|start [karo|kar|kardo] [do]")
	a.add(IntentArchive, LangHI, true, false, "{name} [ko] archive [karo|kar|kardo] [do]")
	// delete / remove: parseDelete (Parse), refused, never done by voice
	return a.ps
}

// daemonObjects are what the voice daemon itself stops, restarts or
// closes ("stop listening", "meeting band karo"): never a session name for
// a lifecycle verb, decided before any listing.
var daemonObjects = foldSet("listening", "talking", "speaking", "reading", "meeting", "voice", "mic", "music",
	"audio", "sound", "recording", "front", "mode", "glasses", "call", "camera", "it", "this", "that", "everything",
	"all", "sab", "kuch", "ye", "yeh", "isse", "isko", "of")

func lifecycleIntent(intent string) bool {
	return intent == IntentStop || intent == IntentRestart || intent == IntentArchive || intent == IntentRefuse
}

// Parse finds the desk intent of a sentence. It touches nothing outside
// this package (no session list, no disk): "not mine" is decided here.
// A sentence with a delete verb is a delete (refused by the desk when it
// names a session), unless it is a message (tell, search, a to-do, a new
// session's name): "tell X to delete the temp files" stays a tell.
func Parse(sentence string) Parsed {
	p := parseForms(sentence)
	switch p.Intent {
	case IntentTell, IntentSearch, IntentTodoAdd, IntentTodoList, IntentTodoDone, IntentLaunch:
		return p
	}
	if d, ok := parseDelete(sentence); ok {
		return d
	}
	return p
}

// foldWordSet folds each entry the way heard words are folded (Devanagari
// is romanised first), so "हटा" meets "hata".
func foldWordSet(ws ...string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[foldPhrase(w)] = true
	}
	return m
}

// deleteVerbs: a delete in English, Roman Hindi, Devanagari and as the
// listener spells it. Voice never deletes. Stop words (kill, close, end,
// terminate, maar do) are not deletes: they stay the stop forms or talk.
var deleteVerbs = foldWordSet("delete", "remove", "erase", "destroy", "wipe", "trash", "nuke", "drop", "rid",
	"dilit", "dileet", "dilet", "dilete", "deleet", "rimoov", "rimuv", "rimove",
	"hata", "hataa", "hatao", "hataao", "hatana", "mita", "mitaa", "mitao", "mitaao", "mitana",
	"nikal", "nikaal", "nikalo", "nikaalo", "uda", "udaa", "udao", "udaao", "khatam", "khatm",
	"डिलीट", "हटा", "हटाओ", "हटाना", "मिटा", "मिटाओ", "मिटाना", "निकाल", "निकालो", "उड़ा", "उड़ाओ", "खत्म", "ख़त्म",
	// inflected and other verbs (re-review r2 R2)
	"deleted", "deleting", "deletes", "removed", "removing", "erased", "destroyed", "wiped", "trashed", "nuked",
	"purge", "purged", "purging", "scrap", "scrapped", "discard", "discarded",
	"hatwa", "hatwao", "hatwado", "mitwa", "mitwao", "hataiye", "hataiyе", "hatayiye", "mitaiye", "saaf", "saf", "phenk", "phek", "fenk",
	"हटाइए", "हटाइये", "हटवा", "हटवाओ", "मिटवा", "मिटाइए", "साफ", "साफ़", "फेंक", "फेंको")

// deleteLead: words around a delete that only make it polite or say who
// does it ("i want you to", "go ahead and", "let's", "can be", "for me",
// "now", "mujhe ... hai"). Dropped like the fillers.
var deleteLead = foldWordSet("i", "want", "you", "to", "go", "ahead", "let", "lets", "let's", "us", "can", "could",
	"should", "would", "will", "be", "must", "now", "for", "me", "na", "hai", "mujhe", "jo", "kuch", "away", "throw",
	"है", "मुझे", "जो", "कुछ")

// deleteDeckWords: "from the deck", "from agent deck": where to delete from.
var deleteDeckWords = foldWordSet("from", "deck", "agent")

// deleteFillers are dropped around the name of a delete: articles, "session",
// helpers (karo, do), joiners (and, aur), objects (it, isko) and the
// lifecycle verbs said with it ("stop beta notes and delete it").
var deleteFillers = foldWordSet("the", "my", "a", "an", "session", "sessions", "seshan", "sesan", "सेशन", "सेशन्स",
	"group", "groups", "folder", "ko", "को", "karo", "kar", "kardo", "karna", "kare", "kijiye", "do", "de", "dena", "dijiye",
	"करो", "कर", "दो", "दे", "and", "aur", "और", "then", "phir", "fir", "also", "bhi", "it", "them", "isko", "usko", "inko",
	"unko", "ise", "use", "इसे", "उसे", "इसको", "please", "permanently", "forever", "completely", "stop", "kill", "archive",
	"band", "बंद", "rok", "restart", "get", "of")

// deleteAll: "delete all workers", "sab workers hata do".
var deleteAll = foldWordSet("all", "every", "each", "sab", "saare", "sare", "saari", "sari", "sabhi", "सब", "सारे", "सभी")

// hindiDeleteWords make the reply language Hindi.
var hindiDeleteWords = foldWordSet("ko", "karo", "kardo", "aur", "hata", "hatao", "mita", "mitao", "nikal", "nikalo",
	"uda", "udao", "khatam", "band", "sab", "saare", "sare", "saari", "sabhi", "seshan", "dijiye", "dena")

// parseDelete: a sentence holding a delete verb. The name is what is left
// once the verbs and fillers are gone; the desk refuses only when it names
// a session (or a group of his), anything else is talk.
func parseDelete(sentence string) (Parsed, bool) {
	ws := trimFillers(splitWords(strings.ToValidUTF8(sentence, " ")))
	found := false
	out := Parsed{Intent: IntentRefuse, Lang: LangEN, Soft: true}
	var name []word
	for _, w := range ws {
		if w.deva || hindiDeleteWords[w.folded] {
			out.Lang = LangHI
		}
		switch {
		case deleteVerbs[w.folded]:
			found = true
		case deleteAll[w.folded]:
			out.All = true
		case w.folded == foldPhrase("everything") || w.folded == foldPhrase("sabkuch"):
			out.All = true
		case deleteFillers[w.folded], deleteLead[w.folded]:
		default:
			name = append(name, w)
		}
	}
	if !found && hasThrowAway(ws) {
		found = true // "throw away beta notes"
	}
	name = dropDeckPlace(name)
	if !found {
		return Parsed{}, false
	}
	if len(name) > 0 {
		out.Name = rawJoin(name)
		out.Words = foldedWords(name)
	}
	return out, true
}

// numberedName: a short name that ends in a number ("test 1", "worker
// one"), not a line, step or item of the code.
func numberedName(words []string) bool {
	n := len(words)
	if n < 2 || n > 3 {
		return false
	}
	last := words[n-1]
	isNum := hasDigitWord([]string{last}) || spokenNumberWords[last]
	if !isNum {
		return false
	}
	return !codeCounters[words[n-2]]
}

var codeCounters = foldWordSet("line", "lines", "step", "item", "row", "column", "page", "point", "number", "version",
	"chapter", "section")

var spokenNumberWords = foldWordSet("one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten")

// possessiveless: "sams" (heard "sam's") is also tried as "sam".
func possessiveless(words []string) []string {
	out := append([]string(nil), words...)
	for _, w := range words {
		if len(w) > 2 && strings.HasSuffix(w, "s") {
			out = append(out, strings.TrimSuffix(w, "s"))
		}
	}
	return out
}

func hasThrowAway(ws []word) bool {
	for i := 0; i+1 < len(ws); i++ {
		if ws[i].folded == foldPhrase("throw") && ws[i+1].folded == foldPhrase("away") {
			return true
		}
	}
	return false
}

// dropDeckPlace drops a trailing "from [agent] deck" ("remove beta notes
// from the deck"); a name of its own that holds "deck" stays.
func dropDeckPlace(name []word) []word {
	for i, w := range name {
		if w.folded != foldPhrase("from") {
			continue
		}
		rest := name[i+1:]
		ok := len(rest) > 0
		for _, x := range rest {
			if !deleteDeckWords[x.folded] {
				ok = false
			}
		}
		if ok {
			return name[:i]
		}
	}
	return name
}

// talkWords: a name that reads as talk before any listing: a clause
// marker or more than three content words ("the old function in beta notes").
func talkWords(words []string) bool {
	content := 0
	for _, w := range words {
		if clauseMarkers[w] {
			return true
		}
		if nameNoise[w] || w == fold("of") || w == fold("for") || w == fold("in") || w == fold("on") || w == fold("to") || w == fold("from") {
			continue
		}
		content++
	}
	return content == 0 || content > 3
}

func onlyDeictic(words []string) bool {
	for _, w := range words {
		if !deicticNames[w] {
			return false
		}
	}
	return true
}

const (
	sayNoDelete     = "I never delete a session by voice; do it on screen."
	sayNoDeleteMany = "I never delete sessions by voice; do it on screen."
)

// refuseDelete: a delete that names a session of his (or a group of his,
// or "all workers") is refused with a spoken line and nothing is done;
// someone else's session is refused by name; a sentence that names no
// session ("delete the session cookie", "remove the unused import") is
// talk to the session he is on.
func (d *Desk) refuseDelete(ctx context.Context, r Result, p Parsed) (Result, error) {
	q0 := newNameQuery(p.Name, nil)
	if p.All && len(q0.words) == 0 {
		// "delete everything", "delete all sessions", "sab hata do"
		refuse(&r, RefusedUnsafe, sayNoDeleteMany)
		return r, nil
	}
	if len(q0.words) == 0 || onlyDeictic(q0.words) {
		return notMine(p.Lang), nil // decided before any listing
	}
	talk := talkWords(q0.words)
	if talk && len(q0.words) > 5 {
		return notMine(p.Lang), nil
	}
	all, _ := d.sessions(ctx)
	q := newNameQuery(p.Name, hostsOf(all))
	if len(q.words) == 0 {
		return notMine(p.Lang), nil
	}
	if talk {
		// a name with a talk word ("fix") is a delete only when it says a
		// session's whole title ("delete tailscale fix on boxone")
		whole := false
		for _, s := range all {
			if !s.Archived && (q.host == "" || s.Host == q.host) && namesWhole(q, s) {
				whole = true
				break
			}
		}
		if !whole {
			return notMine(p.Lang), nil
		}
	}
	if p.All {
		for _, s := range d.Config.mine(all) {
			if q.host != "" && s.Host != q.host {
				continue
			}
			if ok, level, _ := coverage(q, s); ok && level >= wordPhonetic {
				refuse(&r, RefusedUnsafe, sayNoDeleteMany)
				return r, nil
			}
		}
		if who := d.Config.personNamed(possessiveless(q.words)); who != "" {
			refuse(&r, RefusedOtherPerson, otherLine(who)+" Nothing done.")
			return r, nil
		}
		return notMine(p.Lang), nil
	}
	res := resolve(q, all, d.Config)
	if res.Kind == resolvedNotFound && len(q.words) > 2 && q.words[len(q.words)-1] == foldPhrase("one") {
		// "delete the beta notes one"
		if q1 := newNameQuery(rawJoin(splitWords(p.Name)[:len(splitWords(p.Name))-1]), hostsOf(all)); len(q1.words) > 0 {
			if res1 := resolve(q1, all, d.Config); res1.Kind != resolvedNotFound {
				q, res = q1, res1
			}
		}
	}
	if numberedName(q.words) && res.Kind != resolvedOther {
		// "delete worker one", "delete test 1": a session's name
		if res.Kind == resolvedOne {
			t := res.Session.Ref()
			r.Target = &t
		}
		refuse(&r, RefusedUnsafe, sayNoDelete)
		return r, nil
	}
	if genericOnlyQ(q) && (res.Kind == resolvedOne || res.Kind == resolvedAmbiguous) && !saysWholeTitle(q, res) {
		return notMine(p.Lang), nil
	}
	switch res.Kind {
	case resolvedOther:
		refuse(&r, RefusedOtherPerson, otherLine(res.Person))
		return r, nil
	case resolvedOne:
		t := res.Session.Ref()
		r.Target = &t
		refuse(&r, RefusedUnsafe, sayNoDelete)
		return r, nil
	case resolvedAmbiguous:
		if !res.Sure {
			return notMine(p.Lang), nil // look-alikes only: no session was named
		}
		refuse(&r, RefusedUnsafe, sayNoDeleteMany)
		return r, nil
	}
	// a name with a number ("delete test 1", "delete worker one") reads as
	// a session's name, never as talk: refused, nothing typed
	if numberedName(q.words) {
		refuse(&r, RefusedUnsafe, sayNoDelete)
		return r, nil
	}
	// a session of nobody's ("delete the stray session"), said by its own words
	if !genericOnlyQ(q) {
		for _, s := range all {
			if s.Archived || (q.host != "" && s.Host != q.host) {
				continue
			}
			if ok, _, exact := coverage(q, s); ok && exact >= len(q.words) {
				refuse(&r, RefusedUnsafe, sayNoDelete)
				return r, nil
			}
		}
	}
	return notMine(p.Lang), nil
}
