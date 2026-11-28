// Package sessionmatch is the spoken-name session matcher of the voice desk
// (`agent-deck voice do`). It is a copy of the `agent-deck switch` matcher's
// rules (cmd/agent-deck/switch_match.go, the voice daemon's strict
// matchSession port: exact id, unique id prefix, exact title, then a fuzzy
// score over the title with the group path as extra words). `switch` does
// not use this package yet: unifying the two is a follow-up, and until then
// a change to one must be made in the other.
package sessionmatch

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Target is one session `agent-deck switch` can go to.
type Target struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Group  string `json:"group"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

// Candidate is one choice of an ambiguous switch.
type Candidate struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Group  string  `json:"group"`
	Status string  `json:"status"`
	Score  float64 `json:"score"`
}

const (
	MatchOne       = "match"
	MatchAmbiguous = "ambiguous"
	MatchNone      = "none"

	// The voice daemon's strict matchSession values (claude-code-voice
	// daemon/src/session-router.ts): a fuzzy score below the threshold is no
	// match, and the best one must lead the next by the gap.
	FuzzyThreshold = 0.55
	FuzzyGap       = 0.15
	ActiveBonus    = 0.02
	MaxCandidates  = 5
	MinIDPrefix    = 4
)

type Result struct {
	Kind       string
	Target     Target
	Candidates []Candidate
}

// Match resolves a query in this order: exact id, unique id
// prefix, exact title (case-insensitive), then the voice daemon's strict
// fuzzy score over the title, with the group path as extra words. It never
// guesses: two equally good sessions are ambiguous.
func Match(query string, targets []Target) Result {
	q := strings.TrimSpace(query)
	if q == "" {
		return Result{Kind: MatchNone}
	}
	for _, t := range targets {
		if t.ID == q {
			return Result{Kind: MatchOne, Target: t}
		}
	}
	var byPrefix []Target
	if utf8.RuneCountInString(q) >= MinIDPrefix && !strings.ContainsAny(q, " \t") {
		for _, t := range targets {
			if strings.HasPrefix(t.ID, q) {
				byPrefix = append(byPrefix, t)
			}
		}
		if len(byPrefix) == 1 {
			return Result{Kind: MatchOne, Target: byPrefix[0]}
		}
	}
	var byTitle []Target
	for _, t := range targets {
		if strings.EqualFold(strings.TrimSpace(t.Title), q) {
			byTitle = append(byTitle, t)
		}
	}
	switch {
	case len(byTitle) == 1:
		return Result{Kind: MatchOne, Target: byTitle[0]}
	case len(byTitle) > 1:
		return ambiguous(byTitle, 1)
	}
	if r := fuzzyMatch(q, targets); r.Kind != MatchNone || len(byPrefix) < 2 {
		return r
	}
	return ambiguous(byPrefix, 1)
}

func ambiguous(targets []Target, score float64) Result {
	r := Result{Kind: MatchAmbiguous}
	for i, t := range targets {
		if i == MaxCandidates {
			break
		}
		r.Candidates = append(r.Candidates, Candidate{ID: t.ID, Title: t.Title, Group: t.Group, Status: t.Status, Score: score})
	}
	return r
}

type scoredTarget struct {
	t           Target
	base, score float64
}

func fuzzyMatch(query string, targets []Target) Result {
	var scored []scoredTarget
	for _, t := range targets {
		base := TargetScore(query, t)
		score := base
		if t.Status == "running" || t.Status == "waiting" {
			score += ActiveBonus // live sessions win ties, idle ones still match
		}
		if score >= FuzzyThreshold {
			scored = append(scored, scoredTarget{t: t, base: base, score: score})
		}
	}
	if len(scored) == 0 {
		return Result{Kind: MatchNone}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	top := scored[0]
	if len(scored) == 1 || top.score-scored[1].score >= FuzzyGap-1e-9 || (top.base == 1 && scored[1].base < 1) {
		return Result{Kind: MatchOne, Target: top.t}
	}
	// Unlike the voice daemon, two sessions whose names read the same are
	// not settled by status: the caller is asked which one.
	r := Result{Kind: MatchAmbiguous}
	for i, s := range scored {
		if i == MaxCandidates {
			break
		}
		r.Candidates = append(r.Candidates, Candidate{ID: s.t.ID, Title: s.t.Title, Group: s.t.Group, Status: s.t.Status, Score: math.Round(s.score*100) / 100})
	}
	return r
}

// TargetScore is the fuzzy score of one session: its title alone or
// its group path words followed by the title, whichever is better. A number
// in the query must appear exactly among those words ("0956" is not "0955").
func TargetScore(query string, t Target) float64 {
	named := append(Tokens(t.Group), Tokens(t.Title)...)
	for _, w := range Tokens(query) {
		if strings.IndexFunc(w, isASCIIDigit) >= 0 && !containsString(named, w) {
			return 0
		}
	}
	score := TitleScore(query, t.Title)
	if t.Group != "" {
		score = math.Max(score, TitleScore(query, t.Group+" "+t.Title))
	}
	return score
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

var (
	nonWord   = regexp.MustCompile(`[^\p{Latin}\p{Devanagari}\p{M}\p{N} ]`)
	stopWords = map[string]bool{"the": true, "session": true, "a": true, "to": true, "my": true, "one": true, "please": true}
)

// Tokens is the voice daemon's tokens(): lowercase Latin, Devanagari
// and digit words, punctuation as spaces, stop words dropped.
func Tokens(text string) []string {
	var out []string
	for _, w := range strings.Fields(nonWord.ReplaceAllString(strings.ToLower(text), " ")) {
		if !stopWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// TitleScore is the voice daemon's titleScore(): 0..1 from exact,
// containment, token overlap and edit distance.
func TitleScore(query, title string) float64 {
	q, t := Tokens(query), Tokens(title)
	if len(q) == 0 || len(t) == 0 {
		return 0
	}
	qs, ts := strings.Join(q, " "), strings.Join(t, " ")
	if qs == ts {
		return 1
	}
	if strings.Contains(ts, qs) || strings.Contains(qs, ts) {
		return 0.9
	}
	overlap := 0
	for _, w := range q {
		for _, x := range t {
			if x == w || (utf8.RuneCountInString(w) >= 4 && (strings.HasPrefix(x, w) || strings.HasPrefix(w, x))) || Similar(w, x) {
				overlap++
				break
			}
		}
	}
	tokenScore := float64(overlap) / float64(max(len(q), min(len(t), len(q)+1)))
	ql, tl := utf8.RuneCountInString(qs), utf8.RuneCountInString(ts)
	edit := 1 - float64(Levenshtein(qs, ts))/float64(max(ql, tl))
	return math.Max(tokenScore*0.85, edit*0.8)
}

func Similar(a, b string) bool {
	la, lb := utf8.RuneCountInString(a), utf8.RuneCountInString(b)
	if la-lb > 2 || lb-la > 2 || la < 4 {
		return false
	}
	return Levenshtein(a, b) <= max(1, la/4)
}

// Levenshtein is the edit distance between a and b, by runes.
func Levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		diag := prev[0]
		prev[0] = i
		for j := 1; j <= len(rb); j++ {
			tmp := prev[j]
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			prev[j] = min(prev[j]+1, prev[j-1]+1, diag+cost)
			diag = tmp
		}
	}
	return prev[len(rb)]
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
