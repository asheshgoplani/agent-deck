package session

import (
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PathSource labels where a path candidate originated. It drives both the
// display hint in the picker and the base weight used for ranking.
type PathSource string

const (
	// SourceRecent is a path that backs an existing (or past) session.
	SourceRecent PathSource = "recent"
	// SourceGroup is a group's configured/derived default path.
	SourceGroup PathSource = "group"
	// SourceZoxide is a hit from the zoxide frecency database.
	SourceZoxide PathSource = "zoxide"
	// SourceLiteral is a path the user typed verbatim (added by the caller).
	SourceLiteral PathSource = "literal"
)

// PathCandidate is a single ranked suggestion for a new session's project path.
type PathCandidate struct {
	Path       string
	Source     PathSource
	LastAccess time.Time // zero when unknown (group/zoxide)
	Score      float64
}

// PathSuggestInput carries the already-collected raw inputs. All I/O (the
// zoxide query, filesystem probing) is performed by the caller so PathSuggest
// stays pure and deterministically testable — the single ranking used by the
// TUI picker, the CLI and the ADE endpoints.
type PathSuggestInput struct {
	Instances     []*Instance // recents source (project paths + access times)
	GroupDefaults []string    // resolved DefaultPathForGroup values
	Zoxide        []string    // zoxide results, best-first
	Query         string      // case-insensitive substring filter (optional)
	Now           time.Time   // reference time for recency decay; zero → now
	Limit         int         // max candidates; <=0 → defaultPathSuggestLimit
}

const (
	defaultPathSuggestLimit = 10

	weightRecent = 100.0
	weightGroup  = 60.0
	weightZoxide = 40.0

	// recencyHalfLifeHours: a recent path loses half its recency weight after
	// one week of inactivity (frecency decay).
	recencyHalfLifeHours = 24.0 * 7.0
	frequencyBonus       = 3.0
)

// PathSuggest merges the candidate sources, frecency-ranks them, dedups by
// normalized path (keeping the strongest source), applies the query filter and
// returns at most Limit candidates, best-first.
func PathSuggest(in PathSuggestInput) []PathCandidate {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultPathSuggestLimit
	}

	// Accumulate per normalized path. A path seen in several sources keeps the
	// strongest source (recent > group > zoxide) and the summed score, so a
	// path that is both a recent AND a zoxide hit ranks above a bare hit.
	byPath := make(map[string]*PathCandidate)
	// hasZoxide tracks whether a candidate has a zoxide contribution so the
	// query filter can exempt it: zoxide already fuzzy-matched the query, so a
	// substring test would wrongly drop non-substring fuzzy hits (query "dck"
	// → "/agent-deck").
	hasZoxide := make(map[string]bool)
	upsert := func(raw string, src PathSource, last time.Time, score float64) {
		p := cleanSuggestPath(raw)
		if p == "" {
			return
		}
		if src == SourceZoxide {
			hasZoxide[p] = true
		}
		existing, ok := byPath[p]
		if !ok {
			byPath[p] = &PathCandidate{Path: p, Source: src, LastAccess: last, Score: score}
			return
		}
		existing.Score += score
		if sourceRank(src) > sourceRank(existing.Source) {
			existing.Source = src
		}
		if last.After(existing.LastAccess) {
			existing.LastAccess = last
		}
	}

	// Recents: dedup by path first so frequency (session count) can boost.
	type recentAgg struct {
		last  time.Time
		count int
	}
	recents := make(map[string]*recentAgg)
	for _, inst := range in.Instances {
		if inst == nil {
			continue
		}
		p := inst.ProjectPath
		if inst.WorktreeRepoRoot != "" {
			p = inst.WorktreeRepoRoot
		}
		p = cleanSuggestPath(p)
		if p == "" {
			continue
		}
		at := inst.LastAccessedAt
		if at.IsZero() {
			at = inst.CreatedAt
		}
		agg, ok := recents[p]
		if !ok {
			recents[p] = &recentAgg{last: at, count: 1}
			continue
		}
		agg.count++
		if at.After(agg.last) {
			agg.last = at
		}
	}
	for p, agg := range recents {
		score := weightRecent*recencyFactor(agg.last, now) + frequencyBonus*float64(agg.count-1)
		upsert(p, SourceRecent, agg.last, score)
	}

	// Group defaults: constant weight; order-insensitive.
	for _, p := range in.GroupDefaults {
		upsert(p, SourceGroup, time.Time{}, weightGroup)
	}

	// Zoxide: preserve the DB's own ordering (best-first) as a tie-breaker.
	for i, p := range in.Zoxide {
		upsert(p, SourceZoxide, time.Time{}, weightZoxide+float64(len(in.Zoxide)-i)*0.01)
	}

	out := make([]PathCandidate, 0, len(byPath))
	q := strings.ToLower(strings.TrimSpace(in.Query))
	for _, c := range byPath {
		if q != "" && !hasZoxide[c.Path] && !strings.Contains(strings.ToLower(c.Path), q) {
			continue
		}
		out = append(out, *c)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if !out[i].LastAccess.Equal(out[j].LastAccess) {
			return out[i].LastAccess.After(out[j].LastAccess)
		}
		return out[i].Path < out[j].Path
	})

	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// recencyFactor returns a value in (0,1] that decays with age using the
// configured half-life. A zero timestamp is treated as very old.
func recencyFactor(last, now time.Time) float64 {
	if last.IsZero() {
		return 0
	}
	ageHours := now.Sub(last).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	return math.Pow(0.5, ageHours/recencyHalfLifeHours)
}

func sourceRank(s PathSource) int {
	switch s {
	case SourceRecent:
		return 3
	case SourceGroup:
		return 2
	case SourceZoxide:
		return 1
	default:
		return 0
	}
}

// cleanSuggestPath cleans a path for stable dedup (trims trailing slashes,
// resolves "." segments) without touching the empty string. Unlike the
// package's normalizePath it performs no filesystem I/O (no symlink resolution),
// keeping the ranking pure and deterministic.
func cleanSuggestPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}
