package voicedesk

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/sessionmatch"
)

// Resolution kinds.
const (
	resolvedOne       = "one"
	resolvedAmbiguous = "ambiguous" // several sure hits, or look-alikes only
	resolvedOther     = "other"     // someone else's session
	resolvedNotFound  = "not_found"
)

type resolution struct {
	Kind    string
	Session Session
	Choices []Session // ambiguous: his candidates, best first
	Person  string    // other: whose
	Sure    bool      // ambiguous: false = look-alikes only ("Did you mean ...?")
	// Phonetic: the one session was found by sound only; the line names it
	Phonetic bool
}

// how a spoken word met a title word
const (
	wordNone     = iota
	wordSimilar  // edit distance only: a look-alike, always asked
	wordPhonetic // sounds the same ("resipee buk" / "recipe book"): accepted only when unique
	wordSure
)

type scored struct {
	s     Session
	level int     // weakest word: wordSure, wordPhonetic or wordSimilar
	score float64 // fit + live bonus: orders his candidates
	fit   float64 // how well the words fit the name, no status bonus
	base  float64
	exact int
}

// sessionWords are the words of a session's group path and title: folded
// for comparing, roman for the phonetic key.
func sessionWords(s Session) (folded, roman []string) {
	for _, w := range splitWords(s.Group + " " + s.Title) {
		folded = append(folded, w.folded)
		roman = append(roman, w.roman)
	}
	return folded, roman
}

// wordMeets says how one spoken word meets a session's words. Sure: equal,
// one a prefix of the other (4 letters or more: "conductors"/"conductor"),
// or, for a word heard in Devanagari, the same vowel classes. Phonetic: the
// same sound key ("kichan"/"kitchen", "vedar"/"weather"). Similar: the
// switch matcher's edit-distance rule. A spoken word with a digit must be
// equal (the switch number rule: "0956" is not "0955").
//
// num is the spoken word's number when it is one ("one" -> "1", see
// spokenNumber): it is also sure on a title word with the same digits
// ("worker one" / worker-1) or the same number word, and on nothing else
// ("one" is never "11", "nine five five" never "0955").
func wordMeets(w, roman string, deva bool, sw, sr []string, num string) int {
	digit := strings.IndexFunc(w, func(r rune) bool { return r >= '0' && r <= '9' }) >= 0
	best := wordNone
	pk := phoneticKey(roman)
	for i, x := range sw {
		if w == x {
			return wordSure
		}
		if num != "" && (x == num || englishNumbers[sr[i]] == num) {
			return wordSure
		}
		if digit {
			continue
		}
		if utf8.RuneCountInString(w) >= 4 && utf8.RuneCountInString(x) >= 4 && (strings.HasPrefix(x, w) || strings.HasPrefix(w, x)) {
			return wordSure
		}
		if deva && utf8.RuneCountInString(w) >= 3 && vowelClass(w) == vowelClass(x) {
			return wordSure
		}
		if len(pk) >= 2 && pk == phoneticKey(sr[i]) && vowelCompatible(roman, sr[i]) {
			best = max(best, wordPhonetic)
			continue
		}
		if sessionmatch.Similar(w, x) || (utf8.RuneCountInString(w) >= 4 && skeleton(w) == skeleton(x)) {
			best = max(best, wordSimilar)
		}
	}
	return best
}

// coverage checks that EVERY spoken word is in the session's title or group
// (CONTRACT.md section 4 rule 1). Two spoken words may also make one title
// word ("mac app" -> "macapp"). It returns ok=false when a word is missing,
// and the weakest level any word met its title word at.
func coverage(q nameQuery, s Session) (ok bool, level int, exact int) {
	sw, sr := sessionWords(s)
	level = wordSure
	words := q.words
	for i := 0; i < len(words); i++ {
		if i+1 < len(words) {
			joined := words[i] + words[i+1]
			if containsWord(sw, joined) {
				exact += 2
				i++
				continue
			}
			if n := q.number(i + 1); n != "" && containsWord(sw, words[i]+n) {
				exact += 2 // "redesign one" -> redesign1
				i++
				continue
			}
			if (q.deva[i] || q.deva[i+1]) && utf8.RuneCountInString(joined) >= 4 && vowelClassIn(sw, joined) {
				i++
				continue
			}
			if pk := phoneticKey(q.roman[i] + q.roman[i+1]); len(pk) >= 3 && phoneticIn(sr, pk, q.roman[i]) {
				level = min(level, wordPhonetic) // "mac app" / "मैक ऐप" -> macapp
				i++
				continue
			}
		}
		num := q.number(i)
		switch m := wordMeets(words[i], q.roman[i], q.deva[i], sw, sr, num); m {
		case wordNone:
			return false, wordNone, 0
		case wordSure:
			if containsWord(sw, words[i]) || (num != "" && containsWord(sw, num)) {
				exact++
			}
		default:
			level = min(level, m)
		}
	}
	return true, level, exact
}

func phoneticIn(list []string, key, spoken string) bool {
	for _, x := range list {
		if phoneticKey(x) == key && vowelCompatible(spoken, x) {
			return true
		}
	}
	return false
}

func vowelClassIn(list []string, w string) bool {
	vc := vowelClass(w)
	for _, x := range list {
		if vowelClass(x) == vc {
			return true
		}
	}
	return false
}

func containsWord(list []string, w string) bool {
	for _, x := range list {
		if x == w {
			return true
		}
	}
	return false
}

// resolve finds the one session a spoken name means, among every session
// the deck can see. It never guesses: several equal hits or look-alikes
// become a question; someone else's session is a refusal by name.
func resolve(p nameQuery, all []Session, cfg Config) resolution {
	if len(p.words) == 0 {
		return resolution{Kind: resolvedNotFound}
	}
	p = p.withHindiNumbers(all)
	var mine, others []scored
	spoken := strings.Join(p.roman, " ")
	for _, s := range all {
		if s.Archived {
			continue
		}
		if p.host != "" && !strings.EqualFold(s.Host, p.host) {
			continue
		}
		kind, _ := cfg.ownerOf(s)
		if kind == ownerNobody {
			continue
		}
		ok, level, exact := coverage(p, s)
		if !ok {
			continue
		}
		t := sessionmatch.Target{ID: s.ID, Title: s.Title, Group: s.Group, Status: s.Status}
		base := sessionmatch.TargetScore(spoken, t)
		if strings.EqualFold(strings.TrimSpace(s.Title), strings.TrimSpace(p.raw)) || foldPhrase(s.Title) == strings.Join(p.words, " ") {
			base = 1
		}
		fit := base + float64(exact)*0.01
		score := fit
		if s.Status == "running" || s.Status == "waiting" {
			score += sessionmatch.ActiveBonus
		}
		sc := scored{s: s, level: level, score: score, fit: fit, base: base, exact: exact}
		if kind == ownerOther {
			others = append(others, sc)
		} else {
			mine = append(mine, sc)
		}
	}
	order := func(xs []scored) {
		sort.SliceStable(xs, func(i, j int) bool {
			if xs[i].level != xs[j].level {
				return xs[i].level > xs[j].level
			}
			return xs[i].score > xs[j].score
		})
	}
	order(mine)
	order(others)
	sureMine, phonMine, likeMine := split(mine)
	sureOther, phonOther, _ := split(others)

	// Someone else's session that fits the spoken name better than any of
	// his (by the words alone, not by status): refused by name.
	if len(sureOther) > 0 && (len(sureMine) == 0 || bestFit(sureOther) > bestFit(sureMine)+0.05) {
		_, who := cfg.ownerOf(sureOther[0].s)
		return resolution{Kind: resolvedOther, Session: sureOther[0].s, Person: who}
	}
	if person := cfg.personNamed(p.words); person != "" && len(sureMine) == 0 {
		return resolution{Kind: resolvedOther, Person: person}
	}
	switch {
	case len(sureMine) == 1:
		return resolution{Kind: resolvedOne, Session: sureMine[0].s}
	case len(sureMine) > 1:
		top, next := sureMine[0], sureMine[1]
		if top.score-next.score >= sessionmatch.FuzzyGap-1e-9 || (top.base == 1 && next.base < 1) {
			return resolution{Kind: resolvedOne, Session: top.s}
		}
		return resolution{Kind: resolvedAmbiguous, Choices: sessionsOf(sureMine), Sure: true}
	case len(phonMine) == 1 && len(likeMine) == 0 && len(phonOther) == 0:
		// sounds like exactly one of his sessions and nothing else is close
		// ("gaarden plaanar" -> garden-planner): accepted; the line names it
		return resolution{Kind: resolvedOne, Session: phonMine[0].s, Phonetic: true}
	case len(phonMine)+len(likeMine) > 0:
		return resolution{Kind: resolvedAmbiguous, Choices: sessionsOf(append(append([]scored{}, phonMine...), likeMine...)), Sure: false}
	}
	if len(phonOther) > 0 {
		_, who := cfg.ownerOf(phonOther[0].s)
		return resolution{Kind: resolvedOther, Session: phonOther[0].s, Person: who}
	}
	if len(others) > 0 {
		_, who := cfg.ownerOf(others[0].s)
		return resolution{Kind: resolvedOther, Session: others[0].s, Person: who}
	}
	return resolution{Kind: resolvedNotFound}
}

// findAll is resolve for "find X": every session of his the name fits,
// sure hits first; look-alikes only when there is no sure hit.
func findAll(p nameQuery, all []Session, cfg Config) resolution {
	r := resolve(p, all, cfg)
	if r.Kind != resolvedOne {
		return r
	}
	p = p.withHindiNumbers(all)
	// resolve picked one; find still lists close equals so he can choose.
	var hits []scored
	for _, s := range cfg.mine(all) {
		if p.host != "" && !strings.EqualFold(s.Host, p.host) {
			continue
		}
		if ok, level, _ := coverage(p, s); ok && level == wordSure {
			hits = append(hits, scored{s: s})
		}
	}
	if len(hits) <= 1 {
		return r
	}
	choices := []Session{r.Session}
	for _, h := range hits {
		if h.s.ID != r.Session.ID || h.s.Host != r.Session.Host {
			choices = append(choices, h.s)
		}
	}
	return resolution{Kind: resolvedAmbiguous, Choices: choices, Sure: true}
}

func bestFit(xs []scored) float64 {
	b := 0.0
	for _, x := range xs {
		if x.fit > b {
			b = x.fit
		}
	}
	return b
}

func split(xs []scored) (sure, phonetic, like []scored) {
	for _, x := range xs {
		switch x.level {
		case wordSure:
			sure = append(sure, x)
		case wordPhonetic:
			phonetic = append(phonetic, x)
		default:
			like = append(like, x)
		}
	}
	return sure, phonetic, like
}

func sessionsOf(xs []scored) []Session {
	out := make([]Session, 0, len(xs))
	for _, x := range xs {
		out = append(out, x.s)
	}
	return out
}

// nameQuery is a spoken name ready to resolve.
type nameQuery struct {
	raw   string
	words []string // folded
	roman []string
	deva  []bool
	// hindiNum: the Hindi number word at this index reads as a number
	// (set by withHindiNumbers; off by default)
	hindiNum []bool
	host     string // "on boxone" / "boxone wala": only sessions of that host ("local" = this machine)
}

// hostWords are the ways a host is attached to a name.
var hostLead = foldSet("on", "at", "par", "pe", "per")
var hostTrail = foldSet("wala", "wali", "wale", "vala", "vali", "pe", "par", "per", "ka", "ki", "ke", "wala", "mein")

// newNameQuery builds the query and lifts a host off the name when its
// last words name one of the known hosts ("conductor on boxone",
// "boxone wala conductor").
func newNameQuery(name string, hosts []string) nameQuery {
	ws := trimNameNoise(splitWords(name))
	q := nameQuery{raw: rawJoin(ws)}
	isHost := func(w word) string {
		for _, h := range hosts {
			if h != "" && w.folded == foldPhrase(h) {
				return h
			}
		}
		return ""
	}
	// "... on <host>"
	if n := len(ws); n >= 3 && hostLead[ws[n-2].folded] {
		if h := isHost(ws[n-1]); h != "" {
			q.host, ws = h, ws[:n-2]
		}
	}
	// "<host> wala ..." / "<host> pe ..."
	if q.host == "" && len(ws) >= 3 && hostTrail[ws[1].folded] {
		if h := isHost(ws[0]); h != "" {
			q.host, ws = h, ws[2:]
		}
	}
	ws = trimNameNoise(ws)
	q.raw = rawJoin(ws)
	for _, w := range ws {
		q.words = append(q.words, w.folded)
		q.roman = append(q.roman, w.roman)
		q.deva = append(q.deva, w.deva)
	}
	return q
}

// genericOnlyQ is genericOnly with the listener's spellings ("बिल्ड").
func genericOnlyQ(q nameQuery) bool {
	content := 0
	for i, w := range q.words {
		if nameNoise[w] {
			continue
		}
		if !isCodeNoun(w, q.roman[i], q.deva[i]) {
			return false
		}
		content++
	}
	return content > 0
}

// Spoken numbers. A listener writes "worker one" for worker-1; the
// session's title has the digits. Only one to twenty, one word each: a
// longer number is heard as digits, and digits are compared as they are
// ("955" is never "0955").
var englishNumbers = map[string]string{
	"one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8",
	"nine": "9", "ten": "10", "eleven": "11", "twelve": "12", "thirteen": "13", "fourteen": "14",
	"fifteen": "15", "sixteen": "16", "seventeen": "17", "eighteen": "18", "nineteen": "19", "twenty": "20",
}

// hindiNumbers are one to ten in Roman Hindi and in Devanagari (keyed by
// the roman form). Several are ordinary words too ("do" is the verb in
// "band kar do"), so they count as numbers only where withHindiNumbers
// allows it.
var hindiNumbers = func() map[string]string {
	m := map[string]string{
		"ek": "1", "do": "2", "teen": "3", "char": "4", "chaar": "4", "paanch": "5", "panch": "5",
		"chhe": "6", "chhah": "6", "chah": "6", "saat": "7", "aath": "8", "nau": "9", "das": "10",
	}
	for deva, n := range map[string]string{
		"एक": "1", "दो": "2", "तीन": "3", "चार": "4", "पांच": "5", "पाँच": "5",
		"छह": "6", "छः": "6", "सात": "7", "आठ": "8", "नौ": "9", "दस": "10",
	} {
		m[toRoman(deva)] = n
	}
	return m
}()

// number is the digits a spoken word stands for, or "": a digit word is
// itself; a number word counts only after another name word ("worker
// one", never a lone "one"); a Hindi number word only where hindiNum
// allows it.
func (q nameQuery) number(i int) string {
	if i < 0 || i >= len(q.words) {
		return ""
	}
	w := q.words[i]
	if w != "" && strings.Trim(w, "0123456789") == "" {
		return w
	}
	if i == 0 {
		return ""
	}
	if n, ok := englishNumbers[q.roman[i]]; ok {
		return n
	}
	if i < len(q.hindiNum) && q.hindiNum[i] {
		return hindiNumbers[q.roman[i]]
	}
	return ""
}

// withHindiNumbers lets a Hindi number word after a name word read as a
// number, unless some session has that very word in its name ("plan do"
// is plan-do when there is one).
func (q nameQuery) withHindiNumbers(all []Session) nameQuery {
	q.hindiNum = make([]bool, len(q.words))
	for i := 1; i < len(q.words); i++ {
		if _, ok := hindiNumbers[q.roman[i]]; !ok {
			continue
		}
		literal := false
		for _, s := range all {
			if sw, _ := sessionWords(s); containsWord(sw, q.words[i]) {
				literal = true
				break
			}
		}
		q.hindiNum[i] = !literal
	}
	return q
}

// numberLast: the last word of a name is a number word after a name word
// that is also an ordinary word ("worker do": the verb do; "test ten":
// folded like "then"). Such a name is not talk for that word alone.
func numberLast(q nameQuery) bool {
	n := len(q.words)
	if n < 2 {
		return false
	}
	_, hi := hindiNumbers[q.roman[n-1]]
	_, en := englishNumbers[q.roman[n-1]]
	if !hi && !en {
		return false
	}
	for _, w := range q.words[:n-1] {
		if clauseMarkers[w] {
			return false
		}
	}
	return true
}
