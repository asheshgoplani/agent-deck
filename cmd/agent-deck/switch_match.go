package main

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// switchTarget is one session `agent-deck switch` can go to.
type switchTarget struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Group  string `json:"group"`
	Path   string `json:"path"`
	Status string `json:"status"`
}

// switchCandidate is one choice of an ambiguous switch.
type switchCandidate struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Group  string  `json:"group"`
	Status string  `json:"status"`
	Score  float64 `json:"score"`
}

const (
	switchMatchOne       = "match"
	switchMatchAmbiguous = "ambiguous"
	switchMatchNone      = "none"

	// The voice daemon's strict matchSession values (claude-code-voice
	// daemon/src/session-router.ts): a fuzzy score below the threshold is no
	// match, and the best one must lead the next by the gap.
	switchFuzzyThreshold = 0.55
	switchFuzzyGap       = 0.15
	switchActiveBonus    = 0.02
	switchMaxCandidates  = 5
	switchMinIDPrefix    = 4
)

type switchMatchResult struct {
	Kind       string
	Target     switchTarget
	Candidates []switchCandidate
}

// switchTargets keeps the sessions a switch may go to: archived ones are left out.
func switchTargets(instances []*session.InstanceData) []switchTarget {
	out := make([]switchTarget, 0, len(instances))
	for _, inst := range instances {
		if inst == nil || !inst.ArchivedAt.IsZero() {
			continue
		}
		out = append(out, switchTarget{ID: inst.ID, Title: inst.Title, Group: inst.GroupPath, Path: inst.ProjectPath, Status: string(inst.Status)})
	}
	return out
}

// matchSwitchTarget resolves a query in this order: exact id, unique id
// prefix, exact title (case-insensitive), then the voice daemon's strict
// fuzzy score over the title, with the group path as extra words. It never
// guesses: two equally good sessions are ambiguous.
func matchSwitchTarget(query string, targets []switchTarget) switchMatchResult {
	q := strings.TrimSpace(query)
	if q == "" {
		return switchMatchResult{Kind: switchMatchNone}
	}
	for _, t := range targets {
		if t.ID == q {
			return switchMatchResult{Kind: switchMatchOne, Target: t}
		}
	}
	var byPrefix []switchTarget
	if utf8.RuneCountInString(q) >= switchMinIDPrefix && !strings.ContainsAny(q, " \t") {
		for _, t := range targets {
			if strings.HasPrefix(t.ID, q) {
				byPrefix = append(byPrefix, t)
			}
		}
		if len(byPrefix) == 1 {
			return switchMatchResult{Kind: switchMatchOne, Target: byPrefix[0]}
		}
	}
	var byTitle []switchTarget
	for _, t := range targets {
		if strings.EqualFold(strings.TrimSpace(t.Title), q) {
			byTitle = append(byTitle, t)
		}
	}
	switch {
	case len(byTitle) == 1:
		return switchMatchResult{Kind: switchMatchOne, Target: byTitle[0]}
	case len(byTitle) > 1:
		return ambiguousSwitch(byTitle, 1)
	}
	if r := fuzzySwitchMatch(q, targets); r.Kind != switchMatchNone || len(byPrefix) < 2 {
		return r
	}
	return ambiguousSwitch(byPrefix, 1)
}

func ambiguousSwitch(targets []switchTarget, score float64) switchMatchResult {
	r := switchMatchResult{Kind: switchMatchAmbiguous}
	for i, t := range targets {
		if i == switchMaxCandidates {
			break
		}
		r.Candidates = append(r.Candidates, switchCandidate{ID: t.ID, Title: t.Title, Group: t.Group, Status: t.Status, Score: score})
	}
	return r
}

type scoredSwitchTarget struct {
	t           switchTarget
	base, score float64
}

func fuzzySwitchMatch(query string, targets []switchTarget) switchMatchResult {
	var scored []scoredSwitchTarget
	for _, t := range targets {
		base := switchTargetScore(query, t)
		score := base
		if t.Status == string(session.StatusRunning) || t.Status == string(session.StatusWaiting) {
			score += switchActiveBonus // live sessions win ties, idle ones still match
		}
		if score >= switchFuzzyThreshold {
			scored = append(scored, scoredSwitchTarget{t: t, base: base, score: score})
		}
	}
	if len(scored) == 0 {
		return switchMatchResult{Kind: switchMatchNone}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	top := scored[0]
	if len(scored) == 1 || top.score-scored[1].score >= switchFuzzyGap-1e-9 || (top.base == 1 && scored[1].base < 1) {
		return switchMatchResult{Kind: switchMatchOne, Target: top.t}
	}
	// Unlike the voice daemon, two sessions whose names read the same are
	// not settled by status: the caller is asked which one.
	r := switchMatchResult{Kind: switchMatchAmbiguous}
	for i, s := range scored {
		if i == switchMaxCandidates {
			break
		}
		r.Candidates = append(r.Candidates, switchCandidate{ID: s.t.ID, Title: s.t.Title, Group: s.t.Group, Status: s.t.Status, Score: math.Round(s.score*100) / 100})
	}
	return r
}

// switchTargetScore is the fuzzy score of one session: its title alone or
// its group path words followed by the title, whichever is better. A number
// in the query must appear exactly among those words ("0956" is not "0955").
func switchTargetScore(query string, t switchTarget) float64 {
	named := append(switchTokens(t.Group), switchTokens(t.Title)...)
	for _, w := range switchTokens(query) {
		if strings.IndexFunc(w, isASCIIDigit) >= 0 && !containsString(named, w) {
			return 0
		}
	}
	score := switchTitleScore(query, t.Title)
	if t.Group != "" {
		score = math.Max(score, switchTitleScore(query, t.Group+" "+t.Title))
	}
	return score
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

var (
	switchNonWord   = regexp.MustCompile(`[^\p{Latin}\p{Devanagari}\p{M}\p{N} ]`)
	switchStopWords = map[string]bool{"the": true, "session": true, "a": true, "to": true, "my": true, "one": true, "please": true}
)

// switchTokens is the voice daemon's tokens(): lowercase Latin, Devanagari
// and digit words, punctuation as spaces, stop words dropped.
func switchTokens(text string) []string {
	var out []string
	for _, w := range strings.Fields(switchNonWord.ReplaceAllString(strings.ToLower(text), " ")) {
		if !switchStopWords[w] {
			out = append(out, w)
		}
	}
	return out
}

// switchTitleScore is the voice daemon's titleScore(): 0..1 from exact,
// containment, token overlap and edit distance.
func switchTitleScore(query, title string) float64 {
	q, t := switchTokens(query), switchTokens(title)
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
			if x == w || (utf8.RuneCountInString(w) >= 4 && (strings.HasPrefix(x, w) || strings.HasPrefix(w, x))) || switchSimilar(w, x) {
				overlap++
				break
			}
		}
	}
	tokenScore := float64(overlap) / float64(max(len(q), min(len(t), len(q)+1)))
	ql, tl := utf8.RuneCountInString(qs), utf8.RuneCountInString(ts)
	edit := 1 - float64(levenshtein(qs, ts))/float64(max(ql, tl))
	return math.Max(tokenScore*0.85, edit*0.8)
}

func switchSimilar(a, b string) bool {
	la, lb := utf8.RuneCountInString(a), utf8.RuneCountInString(b)
	if la-lb > 2 || lb-la > 2 || la < 4 {
		return false
	}
	return levenshtein(a, b) <= max(1, la/4)
}

// levenshtein is the edit distance between a and b, by runes.
func levenshtein(a, b string) int {
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
