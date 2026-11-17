package todos

import (
	"strings"
	"unicode"
)

// Matching an item by words of its text ("bank" for "call the bank").
//
// The query is split into words (letters and digits, lowercased). A few
// filler words are dropped ("the", "a", "an", "my", "to-do") unless nothing
// else is left. An open item is a candidate when EVERY remaining query word
// matches one word of its text: the same word, a prefix of it (query word
// of 3+ letters: "bank" -> "banks"), or one edit away (both 5+ letters:
// "dentist" -> "dentsit").
//
// Exactly one candidate is the match. Several candidates are ambiguous,
// with one exception: when exactly one of them is an exact match (its words
// equal the query's words, in order), that one wins. Done items are never
// candidates; tick off a done item by id. The match never guesses.

var fillerWords = map[string]bool{"the": true, "a": true, "an": true, "my": true, "todo": true, "to-do": true}

// words splits s into lowercase words of letters and digits.
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func queryWords(q string) []string {
	all := words(q)
	var out []string
	for _, w := range all {
		if !fillerWords[w] {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return all
	}
	return out
}

func wordMatches(q, t string) bool {
	if q == t {
		return true
	}
	qn, tn := len([]rune(q)), len([]rune(t))
	if qn >= 3 && strings.HasPrefix(t, q) {
		return true
	}
	if qn >= 5 && tn >= 5 && withinOneEdit(q, t) {
		return true
	}
	return false
}

// withinOneEdit reports whether a and b differ by at most one insertion,
// deletion, substitution or swap of two neighbours.
func withinOneEdit(a, b string) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	switch len(ra) - len(rb) {
	case 0:
		var diff []int
		for i := range ra {
			if ra[i] != rb[i] {
				diff = append(diff, i)
				if len(diff) > 2 {
					return false
				}
			}
		}
		switch len(diff) {
		case 0, 1:
			return true
		case 2:
			i, j := diff[0], diff[1]
			return j == i+1 && ra[i] == rb[j] && ra[j] == rb[i]
		}
		return false
	case 1:
		i, j, skipped := 0, 0, false
		for i < len(ra) && j < len(rb) {
			if ra[i] == rb[j] {
				i++
				j++
				continue
			}
			if skipped {
				return false
			}
			skipped = true
			i++
		}
		return true
	}
	return false
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
	var cands []int
	for i, it := range items {
		if !it.Done() && itemMatches(qw, it.Text) {
			cands = append(cands, i)
		}
	}
	switch len(cands) {
	case 0:
		return -1, ErrNotFound
	case 1:
		return cands[0], nil
	}
	exact := -1
	for _, i := range cands {
		if exactMatch(query, items[i].Text) {
			if exact >= 0 {
				exact = -2
				break
			}
			exact = i
		}
	}
	if exact >= 0 {
		return exact, nil
	}
	amb := &AmbiguousError{Query: query}
	for _, i := range cands {
		amb.Candidates = append(amb.Candidates, items[i])
	}
	sortItems(amb.Candidates)
	return -1, amb
}
