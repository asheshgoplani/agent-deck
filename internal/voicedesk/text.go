package voicedesk

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// A heard sentence arrives in English, in Roman Hindi ("deck conductor ko
// bolo ki ...") or, from some listeners, in Devanagari. Everything the desk
// compares goes through one pipeline:
//
//	raw  -> words      (punctuation out, case kept, apostrophes joined)
//	word -> roman      (Devanagari transliterated, Latin lowercased)
//	roman -> folded    (spelling variants of Roman Hindi collapsed)
//
// Words keep their index, so a message slot ("... ko bolo ki <msg>") is cut
// from the original words and sent as he said it.

type word struct {
	raw    string // as heard (punctuation removed)
	roman  string // lowercase Latin
	folded string // spelling-folded, used for every comparison
	deva   bool   // the word was written in Devanagari
	start  int    // byte offsets in the sentence
	end    int
}

// splitWords cuts a sentence into words. Letters, digits and combining marks
// stay; an apostrophe inside a word is dropped ("what's" -> "whats");
// everything else separates words.
func splitWords(s string) []word {
	s = strings.ToValidUTF8(s, " ")
	var out []word
	var cur []rune
	start, pos := -1, 0
	flush := func(end int) {
		if len(cur) == 0 {
			return
		}
		raw := string(cur)
		cur = cur[:0]
		r := toRoman(raw)
		out = append(out, word{raw: raw, roman: r, folded: fold(r), deva: hasDevanagari(raw), start: start, end: end})
		start = -1
	}
	rs := []rune(s)
	for i, c := range rs {
		switch {
		case unicode.IsLetter(c) || unicode.IsDigit(c) || unicode.Is(unicode.M, c):
			if start < 0 {
				start = pos
			}
			cur = append(cur, c)
		case (c == '\'' || c == '’') && len(cur) > 0 && i+1 < len(rs) && unicode.IsLetter(rs[i+1]):
			// joined: what's -> whats
		case c == '-' && len(cur) > 0 && i+1 < len(rs) && (unicode.IsLetter(rs[i+1]) || unicode.IsDigit(rs[i+1])):
			flush(pos) // to-do and phone-build split into words
		default:
			flush(pos)
		}
		pos += utf8.RuneLen(c)
	}
	flush(pos)
	return out
}

func hasDevanagari(s string) bool {
	for _, r := range s {
		if r >= 0x0900 && r <= 0x097F {
			return true
		}
	}
	return false
}

func foldedWords(ws []word) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.folded
	}
	return out
}

func rawJoin(ws []word) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = w.raw
	}
	return strings.Join(parts, " ")
}

// foldPhrase folds every word of a phrase: the form the grammar and the
// answer lists are compared in.
func foldPhrase(s string) string {
	return strings.Join(foldedWords(splitWords(s)), " ")
}

// fold collapses the spellings a listener uses for the same Roman Hindi
// word: aa/a, ee/i, oo/u, w/v, z/j, ph/f, q/k, ck/k, c/k (not ch), doubled
// letters, aspirates (dh/d, th/t ...), a final "h" after a vowel, a final
// nasal n after a vowel in words of four letters or more. Both
// sides of every comparison are folded, so English words survive too
// ("check" -> "chek" on both sides).
func fold(s string) string {
	s = strings.ToLower(s)
	if s == "" {
		return s
	}
	r := strings.NewReplacer(
		// aspiration is written or dropped at will ("padho"/"parho", "dhoondo"/"doondo")
		"dh", "d", "th", "t", "bh", "b", "kh", "k", "gh", "g", "jh", "j",
		"aa", "a", "ee", "i", "ii", "i", "oo", "u", "uu", "u",
		"ph", "f", "ck", "k", "q", "k", "w", "v", "z", "j", "x", "ks",
		"ou", "o", "au", "o", "ai", "e", "ei", "e", "y", "i",
	)
	s = r.Replace(s)
	// c -> k except in "ch".
	var b strings.Builder
	rs := []rune(s)
	for i, c := range rs {
		if c == 'c' && !(i+1 < len(rs) && rs[i+1] == 'h') {
			b.WriteRune('k')
			continue
		}
		b.WriteRune(c)
	}
	rs = []rune(b.String())
	// doubled letters
	out := make([]rune, 0, len(rs))
	for i, c := range rs {
		if i > 0 && rs[i-1] == c {
			continue
		}
		out = append(out, c)
	}
	// "nahin"/"nahi", "hain"/"hai": a trailing nasal n after a vowel goes.
	if n := len(out); n >= 4 && out[n-1] == 'n' && isVowel(out[n-2]) {
		out = out[:n-1]
	}
	// "nahh"/"ji h": a trailing h after a vowel goes ("hah" stays "ha").
	if n := len(out); n >= 3 && out[n-1] == 'h' && isVowel(out[n-2]) {
		out = out[:n-1]
	}
	return string(out)
}

func isVowel(r rune) bool { return strings.ContainsRune("aeiou", r) }

// skeleton is a word without its vowels after the first letter: the part
// of a name that survives the trip through Devanagari and back
// ("कंडक्टर" -> "kandaktar" -> "kndktr" == "conductor" -> "kndktr").
func skeleton(folded string) string {
	rs := []rune(folded)
	if len(rs) == 0 {
		return ""
	}
	out := []rune{rs[0]}
	for _, c := range rs[1:] {
		if isVowel(c) || c == 'h' {
			continue
		}
		if c == 'n' { // nasal before a consonant is often dropped or added
			continue
		}
		if out[len(out)-1] == c {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}

// --- Devanagari to Roman --------------------------------------------------

var devaConsonant = map[rune]string{
	'क': "k", 'ख': "kh", 'ग': "g", 'घ': "gh", 'ङ': "n",
	'च': "ch", 'छ': "chh", 'ज': "j", 'झ': "jh", 'ञ': "n",
	'ट': "t", 'ठ': "th", 'ड': "d", 'ढ': "dh", 'ण': "n",
	'त': "t", 'थ': "th", 'द': "d", 'ध': "dh", 'न': "n",
	'प': "p", 'फ': "f", 'ब': "b", 'भ': "bh", 'म': "m",
	'य': "y", 'र': "r", 'ल': "l", 'व': "v", 'श': "sh",
	'ष': "sh", 'स': "s", 'ह': "h", 'ळ': "l",
	// precomposed nukta letters U+0958..U+095F
	'\u0958': "q", '\u0959': "kh", '\u095A': "g", '\u095B': "z", '\u095C': "d", '\u095D': "dh", '\u095E': "f", '\u095F': "y",
}

var devaVowel = map[rune]string{
	'अ': "a", 'आ': "aa", 'इ': "i", 'ई': "ee", 'उ': "u", 'ऊ': "oo", 'ऋ': "ri",
	'ए': "e", 'ऐ': "ai", 'ओ': "o", 'औ': "au", 'ऑ': "o", 'ऍ': "e",
}

var devaMatra = map[rune]string{
	'ा': "aa", 'ि': "i", 'ी': "ee", 'ु': "u", 'ू': "oo", 'ृ': "ri",
	'े': "e", 'ै': "ai", 'ो': "o", 'ौ': "au", 'ॉ': "o", 'ॅ': "e",
}

const (
	devaVirama     = '्'
	devaNukta      = '़'
	devaAnusvara   = 'ं'
	devaChandra    = 'ँ'
	devaVisarga    = 'ः'
	devaAvagraha   = 'ऽ'
	devaDigitZero  = '०'
	devaDigitNine  = '९'
	devaDanda      = '।'
	devaDoubleDand = '॥'
)

// toRoman transliterates one Devanagari word the way listeners and people
// write Hindi in Latin letters (schwa after the last consonant dropped:
// "बोलो" -> "bolo", "कंडक्टर" -> "kandaktar"). Latin is only lowercased.
func toRoman(s string) string {
	if !hasDevanagari(s) {
		return strings.ToLower(s)
	}
	rs := []rune(s)
	var b strings.Builder
	pendingA := false // a consonant was written and still owns its inherent "a"
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if c == devaNukta {
			continue
		}
		if cons, ok := devaConsonant[c]; ok {
			if pendingA {
				b.WriteString("a")
			}
			// nukta forms written as base + nukta
			if i+1 < len(rs) && rs[i+1] == devaNukta {
				switch c {
				case 'ज':
					cons = "z"
				case 'फ':
					cons = "f"
				case 'क':
					cons = "q"
				}
				i++
			}
			b.WriteString(cons)
			pendingA = true
			continue
		}
		if m, ok := devaMatra[c]; ok {
			b.WriteString(m)
			pendingA = false
			continue
		}
		if v, ok := devaVowel[c]; ok {
			if pendingA {
				b.WriteString("a")
			}
			b.WriteString(v)
			pendingA = false
			continue
		}
		switch {
		case c == devaVirama:
			pendingA = false
		case c == devaAnusvara || c == devaChandra:
			if pendingA {
				b.WriteString("a")
				pendingA = false
			}
			b.WriteString("n")
		case c == devaVisarga:
			if pendingA {
				b.WriteString("a")
				pendingA = false
			}
			b.WriteString("h")
		case c >= devaDigitZero && c <= devaDigitNine:
			if pendingA {
				b.WriteString("a")
				pendingA = false
			}
			b.WriteRune('0' + (c - devaDigitZero))
		case c == devaAvagraha || c == devaDanda || c == devaDoubleDand:
		default:
			if pendingA {
				b.WriteString("a")
				pendingA = false
			}
			b.WriteRune(unicode.ToLower(c))
		}
	}
	// final schwa is silent in Hindi; a one-letter word keeps it ("न" -> "na")
	out := b.String()
	if pendingA && len([]rune(out)) <= 1 {
		out += "a"
	}
	return out
}

// vowelClass keeps every consonant and only the class of each vowel
// (a/o/u -> a, e/i -> i): Devanagari writes English o and u with the
// schwa ("कंडक्टर" -> "kandaktar" = "konduktor") but keeps e/i apart from
// a/o/u, so "टोस्टर" (toaster) is not "tester" and "बोलिंग" is not "billing".
// A word heard in Devanagari is a sure match only at this level; a mere
// consonant-skeleton match is a look-alike and is asked about.
func vowelClass(folded string) string {
	// English silent e: "notes"/"नोट्स", "code"/"कोड"
	if n := len(folded); n >= 4 && strings.HasSuffix(folded, "es") && !isVowel(rune(folded[n-3])) {
		folded = folded[:n-2] + "s"
	} else if n >= 4 && strings.HasSuffix(folded, "e") && !isVowel(rune(folded[n-2])) {
		folded = folded[:n-1]
	}
	var b strings.Builder
	for _, c := range folded {
		switch c {
		case 'a', 'o', 'u':
			b.WriteRune('a')
		case 'e', 'i':
			b.WriteRune('i')
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}
