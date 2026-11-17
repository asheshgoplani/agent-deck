package voicedesk

import (
	"strings"
	"unicode/utf8"
)

// Intents of the desk (CONTRACT.md section 2).
const (
	IntentNeedsMe    = "needs_me"
	IntentStatus     = "status"
	IntentRead       = "read"
	IntentTell       = "tell"
	IntentFind       = "find"
	IntentOverview   = "overview"
	IntentList       = "list"
	IntentConfirmYes = "confirm_yes"
	IntentConfirmNo  = "confirm_no"
	IntentChoice     = "choice"
	IntentNone       = "none"
)

// Languages of a sentence.
const (
	LangEN = "en"
	LangHI = "hi"
)

// Parsed is what the grammar found in a sentence. Name and Message are cut
// from the words as heard; NameFolded is what the resolver compares.
type Parsed struct {
	Intent  string
	Lang    string
	Name    string   // spoken session name ("" when the intent has none)
	Words   []string // folded words of Name
	Message string   // tell: the message, as heard
	// Soft: an unknown name is not a refusal but "not mine" (read X,
	// what is X doing): those forms overlap with the daemon's look phrases
	// and with plain talk to the session.
	Soft bool
}

// element kinds of a compiled pattern
const (
	elLit  = iota // one of words
	elOpt         // optional one of words
	elName        // lazy, >= 1 word
	elMsg         // greedy rest, >= 1 word, optionally must start with one of words
)

type element struct {
	kind  int
	words []string // folded
}

type pattern struct {
	intent string
	lang   string
	soft   bool
	els    []element
}

// compile turns "tell {name} to|that {msg}" into elements. Syntax: words
// separated by spaces; a|b alternatives; [a|b] optional; {name}; {msg};
// {msg:a|b} = the message starts with one of these words (kept in it).
func compile(intent, lang string, soft bool, spec string) pattern {
	p := pattern{intent: intent, lang: lang, soft: soft}
	for _, tok := range strings.Fields(spec) {
		switch {
		case tok == "{name}":
			p.els = append(p.els, element{kind: elName})
		case tok == "{msg}":
			p.els = append(p.els, element{kind: elMsg})
		case strings.HasPrefix(tok, "{msg:"):
			p.els = append(p.els, element{kind: elMsg, words: foldAlts(strings.TrimSuffix(strings.TrimPrefix(tok, "{msg:"), "}"))})
		case strings.HasPrefix(tok, "["):
			p.els = append(p.els, element{kind: elOpt, words: foldAlts(strings.Trim(tok, "[]"))})
		default:
			p.els = append(p.els, element{kind: elLit, words: foldAlts(tok)})
		}
	}
	return p
}

func foldAlts(s string) []string {
	var out []string
	for _, a := range strings.Split(s, "|") {
		out = append(out, fold(a))
	}
	return out
}

// litMatch: the folded word equals one alternative, or for long words the
// consonant skeletons agree (Devanagari spellings of the same word).
func litMatch(w string, alts []string) bool {
	for _, a := range alts {
		if w == a {
			return true
		}
		if utf8.RuneCountInString(a) >= 5 && utf8.RuneCountInString(w) >= 5 && skeleton(w) == skeleton(a) {
			return true
		}
	}
	return false
}

type span struct{ from, to int } // word indexes, to exclusive

type capture struct {
	name, msg span
	hasName   bool
	hasMsg    bool
}

// match runs a pattern over the whole word list (anchored both ends).
func (p pattern) match(ws []string) (capture, bool) {
	var c capture
	var rec func(ei, wi int) bool
	rec = func(ei, wi int) bool {
		if ei == len(p.els) {
			return wi == len(ws)
		}
		e := p.els[ei]
		switch e.kind {
		case elLit:
			return wi < len(ws) && litMatch(ws[wi], e.words) && rec(ei+1, wi+1)
		case elOpt:
			if wi < len(ws) && litMatch(ws[wi], e.words) && rec(ei+1, wi+1) {
				return true
			}
			return rec(ei+1, wi)
		case elName: // lazy
			for end := wi + 1; end <= len(ws); end++ {
				c.name, c.hasName = span{wi, end}, true
				if rec(ei+1, end) {
					return true
				}
			}
			return false
		case elMsg: // greedy rest
			if wi >= len(ws) {
				return false
			}
			if len(e.words) > 0 && !litMatch(ws[wi], e.words) {
				return false
			}
			for end := len(ws); end > wi; end-- {
				c.msg, c.hasMsg = span{wi, end}, true
				if rec(ei+1, end) {
					return true
				}
			}
			return false
		}
		return false
	}
	ok := rec(0, 0)
	return c, ok
}

const questionWords = "what|whether|how|why|when|if|is|are|did|does|do|can|could|should|would|will|where|who|which"

// The grammar. Order matters: whole-sentence forms (overview, needs me,
// list) before forms with a name slot, so "how is everything doing" is
// never "status of everything".
var grammar = func() []pattern {
	var ps []pattern
	add := func(intent, lang string, soft bool, specs ...string) {
		for _, s := range specs {
			ps = append(ps, compile(intent, lang, soft, s))
		}
	}
	// overview
	add(IntentOverview, LangEN, false,
		"how is everything [going|doing]", "hows everything [going|doing]",
		"how are things [going]", "how is it all going", "how are all my sessions [doing]",
	)
	add(IntentOverview, LangHI, false,
		"sab [kuch] kaisa|kaise chal raha|rahe hai|hain", "sab [kuch] theek|thik hai [na|kya]",
		"kya sab [kuch] theek|thik hai", "sab kya kar rahe hai|hain",
	)
	// needs me
	add(IntentNeedsMe, LangEN, false,
		"who is waiting [for|on] [me]", "whos waiting [for|on] [me]", "who needs me [now]",
		"what needs me [now]", "is anyone|anybody waiting [for|on] [me]", "anyone|anybody waiting [for|on] [me]",
		"what is waiting [for|on] [me]", "whats waiting [for|on] [me]", "who is waiting for my answer|reply",
	)
	add(IntentNeedsMe, LangHI, false,
		"kaun mera|meri intezaar|intazaar|intjar|wait kar raha|rahi|rahe hai|hain",
		"mera|meri intezaar|intazaar|intjar|wait kaun kar raha|rahi|rahe hai|hain",
		"kaun intezaar|intazaar|intjar|wait kar raha|rahi|rahe hai|hain",
		"kisko|kise|kinko meri zaroorat|jarurat hai|hain",
		"meri zaroorat|jarurat kisko|kise hai",
	)
	// list
	add(IntentList, LangEN, false,
		"list [all] [my] sessions", "list all", "what|which sessions do i have [running]",
		"what are my sessions", "show my sessions", "show all my sessions",
	)
	add(IntentList, LangHI, false,
		"[meri] [saari|sari|sab] sessions|session|seshan|seshans batao|sunao|dikhao|bolo",
		"meri [saari|sari|sab] sessions|session|seshan|seshans kya|kaun hai|hain",
		"meri sessions kaun kaun si hai|hain",
	)
	// status
	add(IntentStatus, LangEN, false,
		"[what|whats] [is] [the] status of|on {name}", "give me the status of|on {name}",
		"tell me the status of|on {name}",
	)
	add(IntentStatus, LangEN, true,
		"how|hows [is] {name} doing|going", "what|whats [is] {name} doing", "what|whats [is] {name} up to",
	)
	add(IntentStatus, LangHI, false,
		"{name} ka|ki|ke status kya hai", "{name} ka|ki|ke status batao|bolo|sunao",
		"{name} ka|ki|ke haal kya hai", "{name} ka|ki|ke kya haal hai",
	)
	add(IntentStatus, LangHI, true,
		"{name} kya kar raha|rahi|rahe hai|hain",
	)
	// read
	add(IntentRead, LangEN, true,
		"read [me] [the] [last|latest] reply|answer|message of|from {name}",
		"read [me] {name} [last|latest] reply|answer|message",
		"what did {name} say|reply|answer|write", "what was {name} [last|latest] reply|answer|message",
		"read out {name}", "read {name}",
	)
	add(IntentRead, LangHI, true,
		"{name} ka|ki|ke [last|aakhri|akhri] jawab|reply|message padho|sunao|batao",
		"{name} ne kya bola|kaha|likha", "{name} ne kya jawab diya",
	)
	// tell
	add(IntentTell, LangEN, false,
		"tell {name} to|that {msg}", "ask {name} to|that {msg}",
		"ask {name} {msg:"+questionWords+"}", "tell {name} {msg:"+questionWords+"}",
	)
	add(IntentTell, LangHI, false,
		"{name} ko bolo|bol|kaho|batao [do] ki|ke {msg}", "{name} ko bolo|kaho|batao {msg}",
		"{name} ko bol do {msg}",
		"{name} se kaho|pucho|poocho|puchho [ki|ke] {msg}",
	)
	// find
	add(IntentFind, LangEN, false,
		"find [me] [the|my] {name} session|sessions",
		"where is [the|my] {name} session", "look for [the|my] {name} session",
	)
	// bare "find X" is also plain talk ("find the bug in the parser"): an
	// unknown X is not mine
	add(IntentFind, LangEN, true, "find [me] [the|my] {name}")
	add(IntentFind, LangHI, false,
		"{name} wali|wala|wale|vali|vala session|sessions dhoondo|dhundo|dhundho|dhoondho|khojo",
		"{name} session dhoondo|dhundo|dhundho|dhoondho|khojo",
	)
	add(IntentFind, LangHI, true, "{name} dhoondo|dhundo|dhundho|dhoondho|khojo")
	return ps
}()

// Words around a command that carry no meaning: the wake name and Hindi /
// English filler. Stripped only at the two ends of the sentence.
var (
	leadFillers  = foldSet("jarvis", "hey", "hi", "ok", "okay", "acha", "accha", "achha", "please", "zara", "jara", "bhai", "yaar", "um", "uh", "so", "and", "now", "can", "you", "could", "would")
	trailFillers = foldSet("please", "jarvis", "yaar", "zara", "jara", "ji", "bhai", "thanks")
)

// Names that point at the room or the screen, not a session: a sentence
// whose name is only these is the daemon's (look) or the session's.
var deicticNames = foldSet("this", "that", "it", "these", "those", "he", "she", "they", "him", "her", "them",
	"me", "us", "you", "ye", "yeh", "ise", "isko", "use", "usko", "wo", "woh", "vo", "voh", "sign", "page",
	"screen", "everything", "everyone", "everybody", "all", "things", "sab", "kuch", "something", "here", "there",
	"what", "which", "who", "the", "a", "an", "my", "your", "mera", "meri", "tum", "tumhe", "aap",
	"user", "users", "customer", "customers", "people", "team", "usko", "unko", "inko", "unhe", "use")

func foldSet(ws ...string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[fold(w)] = true
	}
	return m
}

// nameNoise is dropped from a captured name before resolving it.
var nameNoise = foldSet("the", "my", "a", "an", "session", "sessions", "wali", "wala", "wale", "vali", "vala", "ka", "ki", "ke")

// Parse finds the desk intent of a sentence. It touches nothing outside
// this package (no session list, no disk): "not mine" is decided here.
func Parse(sentence string) Parsed {
	sentence = strings.ToValidUTF8(sentence, " ")
	all := splitWords(sentence)
	cut := func(ws []word) string { return strings.TrimSpace(sentence[ws[0].start:ws[len(ws)-1].end]) }
	lang := LangEN
	for _, w := range all {
		if w.deva {
			lang = LangHI
		}
	}
	ws := trimFillers(all)
	if len(ws) == 0 {
		return Parsed{Intent: IntentNone, Lang: lang}
	}
	folded := foldedWords(ws)
	for _, p := range grammar {
		c, ok := p.match(folded)
		if !ok {
			continue
		}
		out := Parsed{Intent: p.intent, Lang: lang, Soft: p.soft}
		if p.lang == LangHI {
			out.Lang = LangHI
		}
		if c.hasName {
			nameWords := ws[c.name.from:c.name.to]
			nameWords = trimNameNoise(nameWords)
			if len(nameWords) == 0 || allIn(nameWords, deicticNames) {
				continue // "read this", "what is he doing": not a session name
			}
			out.Name = cut(nameWords)
			out.Words = foldedWords(nameWords)
		}
		if c.hasMsg {
			out.Message = strings.TrimSpace(strings.TrimRight(cut(ws[c.msg.from:c.msg.to])+tailPunct(sentence, ws[c.msg.to-1].end), " "))
		}
		return out
	}
	return Parsed{Intent: IntentNone, Lang: lang}
}

func trimFillers(ws []word) []word {
	for len(ws) > 0 && leadFillers[ws[0].folded] {
		ws = ws[1:]
	}
	for len(ws) > 0 && trailFillers[ws[len(ws)-1].folded] {
		ws = ws[:len(ws)-1]
	}
	return ws
}

func trimNameNoise(ws []word) []word {
	for len(ws) > 0 && nameNoise[ws[0].folded] {
		ws = ws[1:]
	}
	for len(ws) > 0 && nameNoise[ws[len(ws)-1].folded] {
		ws = ws[:len(ws)-1]
	}
	return ws
}

func allIn(ws []word, set map[string]bool) bool {
	for _, w := range ws {
		if !set[w.folded] {
			return false
		}
	}
	return true
}

// tailPunct keeps the end mark of a message ("... first?" stays a
// question) but nothing after it.
func tailPunct(sentence string, end int) string {
	rest := strings.TrimSpace(sentence[end:])
	if rest != "" && strings.ContainsRune(".?!", rune(rest[0])) {
		return rest[:1]
	}
	return ""
}

// Words that make a "name" read as talk, not as a session name: a clause
// joiner, a verb or a please ("status of the build please check it").
var clauseMarkers = foldSet("please", "and", "then", "but", "because", "so", "if", "when", "while", "after", "before",
	"with", "it", "is", "are", "was", "were", "be", "been", "do", "does", "did", "check", "fix", "run", "make", "see",
	"look", "try", "tell", "ask", "read", "find", "show", "why", "how", "what", "where", "which", "who", "we", "i",
	"you", "they", "aur", "phir", "lekin", "kyunki", "ki", "karo", "kar", "kare", "karna", "karke", "hai", "hain",
	"tha", "thi", "ho", "hua", "hue", "ruk", "jaye", "jao", "dekho", "batao", "theek", "thik")

// Ordinary nouns of coding talk. A name made only of these that matches no
// session is talk ("tell the build script to skip lint"), not a session.
var codeNouns = foldSet("file", "files", "function", "functions", "method", "class", "code", "script", "scripts",
	"test", "tests", "build", "builds", "api", "server", "client", "parser", "config", "configuration", "docs", "doc",
	"readme", "log", "logs", "error", "errors", "bug", "bugs", "branch", "commit", "commits", "pr", "issue", "issues",
	"database", "db", "query", "endpoint", "module", "package", "component", "page", "app", "ui", "cli", "model",
	"compiler", "linter", "lint", "pipeline", "ci", "job", "jobs", "deploy", "deployment", "migration", "schema",
	"user", "users", "output", "result", "results", "change", "changes", "diff", "repo", "project", "folder",
	"directory", "step", "steps", "stack", "trace", "message", "terminal", "shell", "command", "loop", "new", "old",
	"latest", "last", "first", "current", "this", "that", "everything", "all", "anything", "something", "is", "iss", "us", "ye", "yeh", "wo", "woh", "wala", "wali")

// talkLike: an unmatched name that is more than three content words, holds
// a clause marker, or is only coding nouns. Such a sentence goes on to the
// session he is on (handled:false) instead of "I found no session called".
func talkLike(words []string) bool {
	content := 0
	allCode := true
	for _, w := range words {
		if clauseMarkers[w] {
			return true
		}
		if nameNoise[w] || w == fold("of") || w == fold("for") || w == fold("in") || w == fold("on") || w == fold("to") {
			continue
		}
		content++
		if !codeNouns[w] {
			allCode = false
		}
	}
	return content > 3 || (content > 0 && allCode)
}

// genericOnly: every content word of the name is an ordinary coding noun
// ("the build", "tests", "config file").
func genericOnly(words []string) bool {
	content := 0
	for _, w := range words {
		if nameNoise[w] {
			continue
		}
		if !codeNouns[w] {
			return false
		}
		content++
	}
	return content > 0
}
