package codex

import (
	"fmt"
	"strings"
)

// Span is a claimed region of an injected block, measured in characters.
//
// Attribution works by locating known content — an AGENTS.md file's bytes, the
// skills catalogue body — inside the block Codex actually injected. Spans are
// non-overlapping by construction, so the characters they claim can be summed
// and subtracted from the block without double counting.
type Span struct {
	// Start and End are rune offsets into the block, End exclusive.
	Start, End int
}

// Chars is the span's length.
func (s Span) Chars() int { return s.End - s.Start }

// spanSet claims non-overlapping regions of one block.
//
// The overlap check is what makes attribution safe when two sources contain the
// same text — two AGENTS.md files with identical content, or a nested file
// whose text is a substring of its parent's. Without it, both would claim the
// same characters and the block's residue would go negative.
type spanSet struct {
	runes  []rune
	claims []Span
}

// newSpanSet prepares a block for attribution.
func newSpanSet(block string) *spanSet { return &spanSet{runes: []rune(block)} }

// Chars is the block's total length.
func (s *spanSet) Chars() int { return len(s.runes) }

// Claim locates needle in the unclaimed part of the block and claims it.
//
// It returns the span and true on success. A needle that is absent, empty, or
// only present inside an already-claimed region returns false — the caller then
// reports it as unlocated rather than inventing a cost for it.
func (s *spanSet) Claim(needle string) (Span, bool) {
	n := []rune(needle)
	if len(n) == 0 || len(n) > len(s.runes) {
		return Span{}, false
	}
	for start := 0; start+len(n) <= len(s.runes); start++ {
		if !runesEqualAt(s.runes, n, start) {
			continue
		}
		span := Span{Start: start, End: start + len(n)}
		if s.overlaps(span) {
			continue
		}
		s.claims = append(s.claims, span)
		return span, true
	}
	return Span{}, false
}

// Text returns the block text a span covers.
func (s *spanSet) Text(span Span) string {
	if span.Start < 0 || span.End > len(s.runes) || span.Start >= span.End {
		return ""
	}
	return string(s.runes[span.Start:span.End])
}

// Claimed is the number of characters attributed so far.
func (s *spanSet) Claimed() int {
	total := 0
	for _, c := range s.claims {
		total += c.Chars()
	}
	return total
}

// Residue is the number of characters no source claimed: the harness's own
// wrapper text, plus anything the attribution failed to explain.
func (s *spanSet) Residue() int { return s.Chars() - s.Claimed() }

// MatchRate is the share of the block that was attributed, as a percentage. An
// empty block reports 100: there was nothing to explain.
func (s *spanSet) MatchRate() float64 {
	if s.Chars() == 0 {
		return 100
	}
	return float64(s.Claimed()) / float64(s.Chars()) * 100
}

// overlaps reports whether a candidate span intersects an existing claim.
func (s *spanSet) overlaps(candidate Span) bool {
	for _, c := range s.claims {
		if candidate.Start < c.End && c.Start < candidate.End {
			return true
		}
	}
	return false
}

// runesEqualAt reports whether needle occurs in haystack at start.
func runesEqualAt(haystack, needle []rune, start int) bool {
	for i, r := range needle {
		if haystack[start+i] != r {
			return false
		}
	}
	return true
}

// SkillEntry is one skill in the catalogue Codex injects.
//
// The catalogue is what actually costs tokens up front: a name, a truncated
// description and a path. The SKILL.md body behind the path loads only when the
// agent opens it, which is the potential-versus-actual distinction that decides
// whether deleting a skill buys anything.
type SkillEntry struct {
	// Name is the catalogue name, e.g. "firecrawl:firecrawl-scrape".
	Name string
	// Description is the truncated description Codex injected.
	Description string
	// Path is the SKILL.md path, with any root alias already expanded.
	Path string
	// Alias records the root alias the path was written with, e.g. "r14", so a
	// report can say where an expansion came from.
	Alias string
	// Line is the catalogue entry, verbatim, including its leading "- ".
	Line string
	// Chars is the entry's length in characters, including the newline that
	// separates it from the next entry.
	Chars int
}

// Dir returns the skill's directory, which is the unit a user removes.
func (e SkillEntry) Dir() string {
	if idx := strings.LastIndex(e.Path, "/"); idx > 0 {
		return e.Path[:idx]
	}
	return e.Path
}

// SkillCatalogue is the parsed form of the host_skills body.
type SkillCatalogue struct {
	// Roots maps a root alias to the absolute directory it stands for.
	Roots map[string]string
	// Entries are the catalogue entries, in injection order.
	Entries []SkillEntry
	// PreambleChars is everything that is not an entry: the protocol
	// explanation and the root table. It is harness overhead, not a skill.
	PreambleChars int
}

// ParseSkillCatalogue splits the injected skills catalogue into its protocol
// preamble, its root table and one record per skill.
//
// The format, as Codex writes it, is a markdown document whose root table lines
// read "- `r0` = `/abs/path`" and whose entries read
// "- name: description (file: path)". A line that does not match either shape is
// left in the preamble rather than guessed at, so a format change costs
// per-skill detail and never produces a wrong attribution.
func ParseSkillCatalogue(body string) SkillCatalogue {
	cat := SkillCatalogue{Roots: map[string]string{}}
	if strings.TrimSpace(body) == "" {
		return cat
	}

	lines := strings.SplitAfter(body, "\n")
	for _, raw := range lines {
		line := strings.TrimRight(raw, "\n")
		if alias, path, ok := parseRootLine(line); ok {
			cat.Roots[alias] = path
			cat.PreambleChars += len([]rune(raw))
			continue
		}
		entry, ok := parseSkillLine(line)
		if !ok {
			cat.PreambleChars += len([]rune(raw))
			continue
		}
		entry.Chars = len([]rune(raw))
		cat.Entries = append(cat.Entries, entry)
	}

	for i := range cat.Entries {
		cat.Entries[i].Path, cat.Entries[i].Alias = expandRoot(cat.Entries[i].Path, cat.Roots)
	}
	return cat
}

// EntryChars is the total cost of the catalogue's entries.
func (c SkillCatalogue) EntryChars() int {
	total := 0
	for _, e := range c.Entries {
		total += e.Chars
	}
	return total
}

// parseRootLine reads a root-table line of the form "- `r0` = `/abs/path`".
func parseRootLine(line string) (alias, path string, ok bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "- `")
	if !ok {
		return "", "", false
	}
	alias, rest, ok = strings.Cut(rest, "` = `")
	if !ok || alias == "" {
		return "", "", false
	}
	path, ok = strings.CutSuffix(rest, "`")
	if !ok || path == "" {
		return "", "", false
	}
	return alias, path, true
}

// parseSkillLine reads a catalogue line of the form
// "- name: description (file: path)".
func parseSkillLine(line string) (SkillEntry, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "- ")
	if !ok {
		return SkillEntry{}, false
	}
	name, rest, ok := strings.Cut(rest, ": ")
	if !ok || name == "" || strings.Contains(name, "`") {
		return SkillEntry{}, false
	}
	open := strings.LastIndex(rest, "(file: ")
	if open < 0 || !strings.HasSuffix(rest, ")") {
		return SkillEntry{}, false
	}
	path := rest[open+len("(file: ") : len(rest)-1]
	if strings.TrimSpace(path) == "" {
		return SkillEntry{}, false
	}
	return SkillEntry{
		Name:        name,
		Description: strings.TrimSpace(rest[:open]),
		Path:        path,
		Line:        strings.TrimSpace(line),
	}, true
}

// expandRoot resolves a "r14/rest" style path against the root table. A path
// that is already absolute, or whose alias is not in the table, is returned
// unchanged: an alias this build cannot resolve must not become a lever
// pointing at a directory that does not exist.
func expandRoot(path string, roots map[string]string) (resolved, alias string) {
	if strings.HasPrefix(path, "/") || len(roots) == 0 {
		return path, ""
	}
	head, rest, ok := strings.Cut(path, "/")
	if !ok {
		return path, ""
	}
	root, known := roots[head]
	if !known {
		return path, ""
	}
	return strings.TrimSuffix(root, "/") + "/" + rest, head
}

// describeKeys renders a world-state key set for a note, so a report can say
// which keys the release wrote rather than only which ones this build read.
func describeKeys(keys []string) string {
	if len(keys) == 0 {
		return "none"
	}
	return strings.Join(keys, ", ")
}

// pct renders a percentage with one decimal, for notes and caveats.
func pct(v float64) string { return fmt.Sprintf("%.1f%%", v) }
