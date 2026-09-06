package ui

import (
	"path/filepath"
	"strings"

	"github.com/sahilm/fuzzy"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// dirCompletionsFn returns the directories matching a partial path prefix.
// Package-level so tests can stub the filesystem out.
var dirCompletionsFn = session.GetDirectoryCompletions

// maxPathSuggestions caps how many entries a filtered suggestion list may
// hold. The dropdowns paginate around the cursor, but an unbounded list
// makes the "N more below" counter useless and keeps cursor math fiddly.
const maxPathSuggestions = 20

// fuzzyFilterPaths filters candidates against query with sahilm/fuzzy,
// case-insensitively, best match first. An empty (or whitespace-only) query
// returns the candidates unchanged. Ties keep the candidate order, so callers
// can pre-sort their corpus by recency/frequency.
func fuzzyFilterPaths(candidates []string, query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return candidates
	}

	// sahilm/fuzzy matches case-sensitively; lowercase both sides so "MYPROJ"
	// finds "/home/me/MyProject".
	lowered := make([]string, len(candidates))
	for i, c := range candidates {
		lowered[i] = strings.ToLower(c)
	}
	matches := fuzzy.FindFrom(strings.ToLower(query), loweredSource(lowered))

	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, candidates[m.Index])
	}
	return out
}

// loweredSource adapts a []string to fuzzy.Source.
type loweredSource []string

func (s loweredSource) String(i int) string { return s[i] }
func (s loweredSource) Len() int            { return len(s) }

// looksLikePartialPath reports whether the query should also pull live
// filesystem directory completions. Bare words ("deck") only fuzzy-filter the
// known corpus; anything path-shaped ("/ho", "~/dev", "./proj", "work/proj")
// is treated as navigation and gets real subdirectory suggestions appended.
func looksLikePartialPath(query string) bool {
	if strings.ContainsRune(query, '/') {
		return true
	}
	return strings.HasPrefix(query, "~")
}

// filterPathSuggestions merges two sources:
//  1. fuzzy-filtered corpus (recents, group defaults, …), ranked by match score;
//  2. live filesystem subdirectory completions when the query looks like a
//     partial path — this is what lets users discover directories they have
//     never opened in agent-deck before.
//
// The result is deduplicated (corpus wins over filesystem on exact matches)
// and capped at maxPathSuggestions.
func filterPathSuggestions(corpus []string, query string) []string {
	filtered := fuzzyFilterPaths(corpus, query)

	query = strings.TrimSpace(query)
	if !looksLikePartialPath(query) {
		return filtered
	}

	completions, err := dirCompletionsFn(query)
	if err != nil || len(completions) == 0 {
		return filtered
	}

	seen := make(map[string]bool, len(filtered))
	for _, p := range filtered {
		seen[p] = true
	}
	for _, c := range completions {
		c = filepath.Clean(c)
		if seen[c] {
			continue
		}
		seen[c] = true
		filtered = append(filtered, c)
		if len(filtered) >= maxPathSuggestions {
			break
		}
	}
	if len(filtered) > maxPathSuggestions {
		filtered = filtered[:maxPathSuggestions]
	}
	return filtered
}
