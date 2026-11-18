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
// its text: the same word, or a prefix of it when the query word has 4 or
// more letters and the text word is at most 2 letters longer ("bank" ->
// "banks", "call" -> "calls"). A shorter prefix ("car" for "card"), a long
// tail ("bank" for "bankruptcy") and a one-letter difference ("maria" for
// "mario") are look-alikes and never match: the caller asks again rather
// than tick off the wrong item.
//
// An exact match (the item's words equal the query's words, in order)
// comes first: one open exact match wins over longer candidates, and when
// no open item is exact but a done one is, that done item is the answer
// (the caller reports it as already done), so saying "done pay rent"
// twice never ticks off "pay rental insurance". Otherwise one open
// candidate is the match and several are ambiguous. With no open
// candidate, one done candidate is the answer (already done again for a
// repeated "done bank"). The match never guesses.

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

// prefixMinRunes and prefixMaxTail bound a prefix match: "bank" matches
// "banks" but "car" does not match "card" and "bank" not "bankruptcy".
const (
	prefixMinRunes = 4
	prefixMaxTail  = 2
)

func wordMatches(q, t string) bool {
	if q == t {
		return true
	}
	qn, tn := len([]rune(q)), len([]rune(t))
	return qn >= prefixMinRunes && tn-qn <= prefixMaxTail && strings.HasPrefix(t, q)
}

func itemMatches(qw []string, text string) bool {
	tw := words(text)
	for _, q := range qw {
		found := false
		for _, t := range tw {
			if wordMatches(q, t) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
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
// It returns ErrNotFound, or an *AmbiguousError (errors.Is ErrAmbiguous)
// with the candidates oldest first.
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
		if !it.Done() && itemMatches(qw, it.Text) {
			cands = append(cands, i)
		}
	}
	switch len(cands) {
	case 0:
		// Nothing open matches: one done item that matches is the answer
		// (already done), so repeating "done bank" is not "no such to-do".
		done := -1
		for i, it := range items {
			if it.Done() && itemMatches(qw, it.Text) {
				if done >= 0 {
					return -1, ErrNotFound
				}
				done = i
			}
		}
		if done >= 0 {
			return done, nil
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
