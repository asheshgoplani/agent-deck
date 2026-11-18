package todos

import (
	"strings"
	"unicode"
)

// Matching an item by words of its text ("bank" for "call the bank").
//
// The query is split into words: runs of letters, digits and combining
// marks (so a Devanagari word keeps its vowel signs and stays one word),
// lowercased. Filler words ("the", "a", "an", "my", "todo", "to-do",
// "to do") are dropped; a query of only filler words names nothing. An
// item is a candidate when EVERY remaining query word matches one word of
// its text: the same word, or the same word with a plural or inflection
// ending ("s", "es", Devanagari "ें", "ों") on either side ("bank" and
// "banks", "tax" and "taxes", "किताब" and "किताबें"; the bare word has 3
// or more letters). Anything else is no match: a short prefix ("car" for
// "card"), a long tail ("bank" for "bankruptcy") and a one-letter
// difference ("maria" for "mario") never match.
//
// A close match is a query word of 4 or more letters that starts an item
// word at most 2 letters longer without being one of those endings
// ("plan" for "plane", "rent" for "rental"). It is never ticked off: when
// no open item matches but open items match closely, the answer is a
// *CloseMatchError with them, so the caller can ask "Did you mean ...?".
//
// An exact match (the item's words equal the query's words, in order)
// comes first: one open exact match wins over longer candidates, and when
// no open item is exact but a done one is, that done item is the answer
// (the caller reports it as already done), so saying "done pay rent"
// twice never ticks off "pay rental insurance". Otherwise one open
// candidate is the match and several are ambiguous. With no open
// candidate, one done candidate is the answer (already done again for a
// repeated "done bank"); then open close matches are a close match. The
// match never guesses.

var fillerWords = map[string]bool{"the": true, "a": true, "an": true, "my": true, "todo": true}

// words splits s into lowercase words of letters, digits and combining
// marks (Devanagari vowel signs and virama are marks, not letters).
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r)
	})
}

func queryWords(q string) []string {
	all := words(q)
	var out []string
	for i := 0; i < len(all); i++ {
		w := all[i]
		if w == "to" && i+1 < len(all) && all[i+1] == "do" {
			i++ // "to-do" and "to do" split into two words
			continue
		}
		if !fillerWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// inflectionEndings are the endings that make two words the same word:
// "bank"/"banks", "tax"/"taxes", "किताब"/"किताबें", "दोस्त"/"दोस्तों".
// "es" counts only after a sibilant stem (see esAfter): box/boxes, not
// tim/times.
var inflectionEndings = []string{"s", "es", "ें", "ों"}

// esAfter reports whether "es" may follow stem: s, x, z, ch or sh only.
// Anywhere else "es" is "e"+"s" of a different word (times/tim, jones/jon).
func esAfter(stem string) bool {
	for _, e := range []string{"s", "x", "z", "ch", "sh"} {
		if strings.HasSuffix(stem, e) {
			return true
		}
	}
	return false
}

// inflectionMinRunes is the shortest bare word an ending may follow, so
// "i" never matches "is".
const inflectionMinRunes = 3

// closeMinRunes and closeMaxTail bound a close match: "plan" for "plane"
// is close (asked about, never ticked off); "car" for "card" and "bank"
// for "bankruptcy" are not even close.
const (
	closeMinRunes = 4
	closeMaxTail  = 2
)

type wordMatch int

const (
	wordNone  wordMatch = iota
	wordClose           // a look-alike: ask, never tick off
	wordSame            // equal, or equal but for an inflection ending
)

func inflectionOf(long, short string) bool {
	if len([]rune(short)) < inflectionMinRunes {
		return false
	}
	for _, e := range inflectionEndings {
		if e == "es" && !esAfter(short) {
			continue
		}
		if long == short+e {
			return true
		}
	}
	return false
}

func wordMatches(q, t string) wordMatch {
	if q == t || inflectionOf(t, q) || inflectionOf(q, t) {
		return wordSame
	}
	qn, tn := len([]rune(q)), len([]rune(t))
	if qn >= closeMinRunes && tn-qn <= closeMaxTail && strings.HasPrefix(t, q) {
		return wordClose
	}
	return wordNone
}

// itemMatches returns how well the item's text matches the query words:
// wordSame when every query word is the same as one of its words,
// wordClose when every query word is at least close to one, else wordNone.
func itemMatches(qw []string, text string) wordMatch {
	tw := words(text)
	res := wordSame
	for _, q := range qw {
		best := wordNone
		for _, t := range tw {
			if m := wordMatches(q, t); m > best {
				best = m
				if best == wordSame {
					break
				}
			}
		}
		if best == wordNone {
			return wordNone
		}
		if best < res {
			res = best
		}
	}
	return res
}

func exactMatch(q, text string) bool {
	a, b := words(q), words(text)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Match returns the one open item that query names by words of its text.
// It returns ErrNotFound, an *AmbiguousError (errors.Is ErrAmbiguous) or
// a *CloseMatchError (errors.Is ErrCloseMatch), with the candidates oldest
// first.
func Match(items []Item, query string) (Item, error) {
	i, err := matchIndex(items, query)
	if err != nil {
		return Item{}, err
	}
	return items[i], nil
}

func matchIndex(items []Item, query string) (int, error) {
	qw := queryWords(query)
	if len(qw) == 0 {
		return -1, ErrNotFound
	}
	openExact, doneExact := -1, -1
	nOpenExact := 0
	for i, it := range items {
		if !exactMatch(query, it.Text) {
			continue
		}
		if it.Done() {
			if doneExact < 0 || it.DoneAt.After(*items[doneExact].DoneAt) {
				doneExact = i
			}
			continue
		}
		openExact = i
		nOpenExact++
	}
	if nOpenExact == 1 {
		return openExact, nil
	}
	if nOpenExact == 0 && doneExact >= 0 {
		return doneExact, nil
	}
	var cands []int
	for i, it := range items {
		if !it.Done() && itemMatches(qw, it.Text) == wordSame {
			cands = append(cands, i)
		}
	}
	switch len(cands) {
	case 0:
		// Nothing open matches: one done item that matches is the answer
		// (already done), so repeating "done bank" is not "no such to-do".
		done := -1
		for i, it := range items {
			if it.Done() && itemMatches(qw, it.Text) == wordSame {
				if done >= 0 {
					return -1, ErrNotFound
				}
				done = i
			}
		}
		if done >= 0 {
			return done, nil
		}
		// Open items that only look alike ("plan" for "book the plane")
		// are never ticked off: the caller asks "Did you mean ...?".
		closeErr := &CloseMatchError{Query: query}
		for _, it := range items {
			if !it.Done() && itemMatches(qw, it.Text) == wordClose {
				closeErr.Candidates = append(closeErr.Candidates, it)
			}
		}
		if len(closeErr.Candidates) > 0 {
			sortItems(closeErr.Candidates)
			return -1, closeErr
		}
		return -1, ErrNotFound
	case 1:
		return cands[0], nil
	}
	amb := &AmbiguousError{Query: query}
	for _, i := range cands {
		amb.Candidates = append(amb.Candidates, items[i])
	}
	sortItems(amb.Candidates)
	return -1, amb
}
