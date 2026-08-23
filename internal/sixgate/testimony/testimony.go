// Package testimony is SIXGATE's G4b: it grades the context inspector's
// IDENTITY claims against the one oracle that exists on every conversational
// harness — the session's own agent, asked to quote what it can see.
//
// WHY A FOURTH ORACLE. G4's panel oracles verify COUNTS: a number on our
// screen against a number somebody else's software produced. But /context
// exists only on Claude, and /status on Codex prints totals with no names in
// them — so on most harnesses there is no independent panel that can say WHICH
// files, skills and text are actually in the window. The agent can. Its
// context is literally in front of it, and it can be asked for checkable
// strings: "quote the first line of your project CLAUDE.md", "is this skill
// fully loaded or only listed?". Testimony is the only cross-harness oracle
// for identity, and identity is all it is trusted with.
//
// THE DIVISION OF LABOUR, WHICH THIS PACKAGE ENFORCES BY CONSTRUCTION:
//
//   - testimony verifies IDENTITY — which files, which skills, which text;
//   - G4's panel oracles verify COUNTS — how many tokens each of them costs;
//   - --strict verifies SELF-CONSISTENCY — the report against its own sums.
//
// Three independent layers. Nothing in this package reads, parses or compares
// a token figure, and that is not an omission: an agent cannot count its own
// tokens, so grading a count against testimony would launder an opinion into
// a measurement. [ScopeNote] states this at the head of every artifact.
//
// WHAT TESTIMONY IS NOT. It is not measurement, and it is not infallible.
// Agents summarize, paraphrase and forget — which is why every question must
// demand a QUOTE (a checkable string: the first line of a file, an exact
// description), never an opinion, and why a mismatch here is a FINDING to
// investigate rather than automatically an inspector bug. The rows say
// "disagree", not "the inspector is wrong".
//
// THE STRUCTURAL RULE, same as G2's and G4's. This package imports the
// standard library and nothing else. Its inputs are text: the probe's
// recorded replies, and the inspector's own JSON documents read as data. A
// comparator that could import the product could ask it to explain itself,
// and a claim graded against its author's explanation is not graded.
//
// CONSENT. Asking a session questions means typing into a live, billed
// terminal. Nothing in this package can do that; the conversation is driven
// by a separate, human-invoked verb (`sixgate probe`, a main package nothing
// can import), and that verb converses ONLY with a disposable probe session
// it created itself. Grading an EXISTING session's context by testimony is
// allowed only with a human's explicit per-run consent, and the sanctioned
// form is manual: the human asks the questions in their own terminal and
// hands the replies over. No automation in this repository types into a
// session it did not create.
package testimony

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Artifact filenames written into the G4b gate directory.
const (
	// Dir is the G4b evidence directory under docs/gates/<slug>/. It is a
	// SUPPLEMENT to G4, not a seventh gate: the gate catalogue stays at six,
	// and `verdict --check` does not require it. A run that exists vouches for
	// itself; a feature without one has simply not been probed.
	Dir            = "G4b-testimony"
	ReportJSONFile = "testimony.json"
	ReportMDFile   = "testimony.md"
	TranscriptFile = "transcript.md"
)

// ScopeNote heads every artifact this package writes. It is the contract:
// identity in, counts out.
const ScopeNote = "Testimony is IDENTITY evidence only: it verifies WHICH files, skills and text " +
	"are in the probe's context, never HOW MANY tokens they cost. Token counts are out of scope " +
	"for G4b by design — an agent cannot count its own tokens, so grading a count against " +
	"testimony would launder an opinion into a measurement. Counts belong to G4's panel oracles; " +
	"self-consistency belongs to --strict. Three independent layers."

// Verdict is one row's grade.
type Verdict string

// The recognised verdicts. There is no "pass with reservations": a row either
// agrees, records a disagreement worth investigating, or admits that nothing
// could be checked — and an unverifiable row is never silently a pass.
const (
	VerdictAgree        Verdict = "agree"
	VerdictDisagree     Verdict = "disagree"
	VerdictUnverifiable Verdict = "unverifiable"
)

// Recipe is the KNOWN context the probe was launched with. Every question the
// probe is asked resolves to a string this recipe planted, which is what makes
// the answers checkable rather than opinions.
type Recipe struct {
	// Nonce is the run's random marker, embedded in the beacon line and the
	// skill description so a reply can be tied to THIS recipe and no other.
	Nonce string `json:"nonce"`
	// FirstLine is the project CLAUDE.md's first line, verbatim.
	FirstLine string `json:"first_line"`
	// SkillName names the planted skill.
	SkillName string `json:"skill_name"`
	// SkillDescription is the planted skill's description, verbatim.
	SkillDescription string `json:"skill_description"`
}

// Answer is one question and the probe's reply, verbatim.
type Answer struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Reply    string `json:"reply"`
}

// Probe identifies the disposable session that testified.
type Probe struct {
	Title        string `json:"title"`
	Workdir      string `json:"workdir"`
	Harness      string `json:"harness"`
	LifecycleCLI string `json:"lifecycle_cli"`
	InspectorCLI string `json:"inspector_cli"`
}

// Teardown records the disposable-probe lifecycle's mandatory ending. A probe
// that outlives its run is a leak into somebody's live fleet, so the driver
// records not just that it tried but that the fleet list PROVED the probe
// gone.
type Teardown struct {
	Stopped      bool   `json:"stopped"`
	Removed      bool   `json:"removed"`
	VerifiedGone bool   `json:"verified_gone"`
	Detail       string `json:"detail,omitempty"`
}

// Row is one identity claim, graded.
type Row struct {
	ID string `json:"id"`
	// Claim names the identity fact being checked, in plain words.
	Claim string `json:"claim"`
	// Inspector is what `session context` asserts about it.
	Inspector string `json:"inspector"`
	// Testimony is what the probe's agent said, condensed to the checked part.
	Testimony string  `json:"testimony"`
	Verdict   Verdict `json:"verdict"`
	Pass      bool    `json:"pass"`
	// Note explains the grade — and for a disagreement, says what to
	// investigate rather than whom to blame.
	Note string `json:"note,omitempty"`
}

// Report is the G4b artifact.
type Report struct {
	Version     int      `json:"version"`
	Tool        string   `json:"tool"`
	GeneratedAt string   `json:"generated_at"`
	Slug        string   `json:"slug"`
	Scope       string   `json:"scope"`
	Pass        bool     `json:"pass"`
	Probe       Probe    `json:"probe"`
	Recipe      Recipe   `json:"recipe"`
	Rows        []Row    `json:"rows"`
	Answers     []Answer `json:"answers"`
	Teardown    Teardown `json:"teardown"`
	Problems    []string `json:"problems,omitempty"`
}

// Input carries everything Compare needs, as data. The JSON documents are the
// inspector's own output, read here as text — never re-derived by importing
// the code that wrote them.
type Input struct {
	Slug   string
	Probe  Probe
	Recipe Recipe
	// Answers are the probe's replies, in question order.
	Answers []Answer
	// ReportJSON is `session context <probe> --json`, verbatim.
	ReportJSON []byte
	// MemoryItemJSON is `session context <probe> --item <memory:id> --json`
	// for the recipe's CLAUDE.md — the L3 text the inspector points at. May be
	// nil when the inspector reported no such item; that is graded, not hidden.
	MemoryItemJSON []byte
	// ResolvePath optionally canonicalises a filesystem path (symlinks,
	// /private aliases). The comparator itself never touches the filesystem;
	// the driver, which may, supplies this. Nil is fine.
	ResolvePath func(string) string
	Tool        string
	Now         time.Time
}

// ---------------------------------------------------------------------------
// the inspector's wire shape, read as data
// ---------------------------------------------------------------------------

// wireItem mirrors just the identity-bearing fields of the inspector's item
// document. Token fields are deliberately not declared: what this package
// cannot parse it cannot be tempted to grade.
type wireItem struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Detail  string `json:"detail"`
	Content struct {
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	} `json:"content"`
	Load struct {
		State string `json:"state"`
	} `json:"load"`
	Children []wireItem `json:"children"`
}

type wireReport struct {
	Report struct {
		Categories []struct {
			Name  string     `json:"name"`
			Items []wireItem `json:"items"`
		} `json:"categories"`
	} `json:"report"`
}

type wireItemDoc struct {
	Item wireItem `json:"item"`
}

// memoryIDPrefix and the category names are quoted from the inspector's wire
// format, which is versioned (schema_version) and covered by golden fixtures.
const (
	memoryIDPrefix     = "memory:"
	skillIDPrefix      = "skill:"
	categoryMemory     = "memory-files"
	categorySkills     = "skills"
	loadStateLoaded    = "loaded"
	listedAtStartupTag = "listed at startup"
)

// flatten walks items depth-first, children included, so a row nested under a
// rollup is still found.
func flatten(items []wireItem) []wireItem {
	var out []wireItem
	for _, it := range items {
		out = append(out, it)
		out = append(out, flatten(it.Children)...)
	}
	return out
}

// categoryItems returns the flattened items of one named category.
func categoryItems(rep *wireReport, name string) []wireItem {
	for _, c := range rep.Report.Categories {
		if c.Name == name {
			return flatten(c.Items)
		}
	}
	return nil
}

// FindProjectMemoryID locates, in the inspector's own report, the id of the
// memory item for the recipe's project CLAUDE.md. The id is taken from the
// document rather than constructed, because the inspector records paths as it
// saw them (on macOS /tmp is an alias of /private/tmp) and an id we invented
// could name a row that is not there.
func FindProjectMemoryID(reportJSON []byte, workdir string) (string, error) {
	var rep wireReport
	if err := json.Unmarshal(reportJSON, &rep); err != nil {
		return "", fmt.Errorf("the inspector's report did not parse as JSON: %w", err)
	}
	want := normPath(path.Join(filepath2slash(workdir), "CLAUDE.md"))
	for _, it := range categoryItems(&rep, categoryMemory) {
		p, ok := strings.CutPrefix(it.ID, memoryIDPrefix)
		if !ok {
			continue
		}
		if normPath(filepath2slash(p)) == want {
			return it.ID, nil
		}
	}
	return "", fmt.Errorf("the inspector's %s category has no item for %s/CLAUDE.md — the recipe planted that file, so its absence is itself a finding", categoryMemory, workdir)
}

// filepath2slash keeps the package free of filepath (whose behaviour is
// host-dependent); gate artifacts and item ids are slash paths already.
func filepath2slash(p string) string { return strings.ReplaceAll(p, "\\", "/") }

// normPath makes two spellings of one file comparable: macOS's /tmp is a
// symlink to /private/tmp, and the inspector and the agent are each entitled
// to either spelling.
func normPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimSuffix(p, "/")
	if rest, ok := strings.CutPrefix(p, "/private/"); ok {
		p = "/" + rest
	}
	return p
}

// ---------------------------------------------------------------------------
// the comparison
// ---------------------------------------------------------------------------

// The four claims every run grades. The set is fixed: a run that quietly
// graded fewer claims would look like a run that passed more of them.
const (
	claimMemoryFirstLine = "memory-first-line"
	claimMemoryFiles     = "memory-files-loaded"
	claimSkillState      = "skill-load-state"
	claimSkillQuote      = "skill-description-quote"
)

// Compare grades the inspector's identity claims against the probe's
// testimony. It returns a report even when every row is unverifiable; the
// report then fails, because a run that learned nothing must never read as a
// pass.
func Compare(in Input) (*Report, error) {
	rep := &Report{
		Version:     1,
		Tool:        in.Tool,
		GeneratedAt: in.Now.UTC().Format(time.RFC3339),
		Slug:        in.Slug,
		Scope:       ScopeNote,
		Probe:       in.Probe,
		Recipe:      in.Recipe,
		Answers:     in.Answers,
	}

	var wire wireReport
	if err := json.Unmarshal(in.ReportJSON, &wire); err != nil {
		return nil, fmt.Errorf("the inspector's report did not parse as JSON: %w", err)
	}

	answers := map[string]string{}
	for _, a := range in.Answers {
		answers[a.ID] = a.Reply
	}

	rep.Rows = append(rep.Rows,
		compareFirstLine(in, answers["q1"]),
		compareMemoryFiles(in, &wire, answers["q2"]),
		compareSkillState(in, &wire, answers["q3"]),
		compareSkillQuote(in, &wire, answers["q4"]),
	)

	rep.Pass = true
	for _, r := range rep.Rows {
		if r.Pass {
			continue
		}
		rep.Pass = false
		rep.Problems = append(rep.Problems, fmt.Sprintf("%s: %s — %s", r.ID, r.Verdict, r.Note))
	}
	return rep, nil
}

// compareFirstLine grades the L3 text the inspector points at for the
// project CLAUDE.md against the line the probe quoted back.
//
// The check is "the quoted line appears verbatim in the inspector's text",
// not "the two first lines are equal" — the first live run taught the
// difference. The harness injects a file behind a wrapper line of its own
// ("Contents of <path> (project instructions, …):"), the inspector honestly
// reports the injected block wrapper included, and the agent, asked for the
// file's first line, quotes the file. Both are right; only the old claim
// mapping was wrong.
func compareFirstLine(in Input, reply string) Row {
	row := Row{
		ID:    claimMemoryFirstLine,
		Claim: "the beacon line the probe quotes appears verbatim in the text the inspector attributes to the project CLAUDE.md",
	}
	testified := pickQuotedLine(reply, in.Recipe.Nonce)
	row.Testimony = testified

	if len(in.MemoryItemJSON) == 0 {
		row.Verdict, row.Pass = VerdictUnverifiable, false
		row.Inspector = "—"
		row.Note = "the inspector produced no --item document for the recipe's CLAUDE.md, so its text claim could not be read; the recipe planted that file, so this is a finding"
		return row
	}
	var doc wireItemDoc
	if err := json.Unmarshal(in.MemoryItemJSON, &doc); err != nil {
		row.Verdict, row.Pass = VerdictUnverifiable, false
		row.Inspector = "—"
		row.Note = "the inspector's --item document did not parse: " + err.Error()
		return row
	}
	inspText := doc.Item.Content.Text
	row.Inspector = firstLine(inspText)

	quote := normalizeQuote(testified)
	lineNo := findLine(inspText, quote)
	switch {
	case strings.TrimSpace(inspText) == "":
		row.Verdict, row.Pass = VerdictUnverifiable, false
		row.Note = "the inspector's --item carries no text for this file, so there is nothing to hold the quote against"
	case testified == "":
		row.Verdict, row.Pass = VerdictUnverifiable, false
		row.Note = "the probe's reply contains no line carrying the recipe nonce, so no quote was obtained; agents can refuse or paraphrase — re-ask before reading anything into this"
	case lineNo == 1:
		row.Verdict, row.Pass = VerdictAgree, true
		row.Note = "the probe quoted the beacon line and it is byte-equal (after trimming wrapping quotes) to the first line of the text the inspector attributes to this file"
	case lineNo > 1:
		row.Verdict, row.Pass = VerdictAgree, true
		row.Note = fmt.Sprintf("the probe's quote is line %d of the inspector's text; the lines above it are the harness's own injection wrapper (\"Contents of <path> …:\"), which the inspector reports because those bytes are genuinely in the window", lineNo)
	default:
		row.Verdict, row.Pass = VerdictDisagree, false
		row.Note = "the quoted line appears nowhere in the inspector's text; a finding to investigate — the agent may have paraphrased, or the inspector may be pointing at the wrong bytes"
		if doc.Item.Content.Truncated {
			row.Note += " (the inspector's text is truncated, so the line may also lie beyond the recorded prefix)"
		}
	}
	return row
}

// findLine returns the 1-based line of text whose quote-normalised form equals
// the quote, or 0 when no line does.
func findLine(text, quote string) int {
	if quote == "" {
		return 0
	}
	for i, line := range strings.Split(text, "\n") {
		if normalizeQuote(line) == quote {
			return i + 1
		}
	}
	return 0
}

// compareMemoryFiles grades the inspector's loaded memory-file rows against
// the paths the probe names.
func compareMemoryFiles(in Input, wire *wireReport, reply string) Row {
	row := Row{
		ID:    claimMemoryFiles,
		Claim: "every memory file the inspector grades as loaded is one the probe can see",
	}
	var loaded []string
	for _, it := range categoryItems(wire, categoryMemory) {
		p, ok := strings.CutPrefix(it.ID, memoryIDPrefix)
		if !ok || it.Load.State != loadStateLoaded {
			continue
		}
		loaded = append(loaded, p)
	}
	sort.Strings(loaded)
	row.Inspector = strings.Join(loaded, " ")

	named := extractPaths(reply)
	row.Testimony = strings.Join(named, " ")

	if len(loaded) == 0 {
		row.Verdict, row.Pass = VerdictUnverifiable, false
		row.Note = "the inspector reports no loaded memory files at all; the recipe planted one, so this is a finding"
		return row
	}

	canon := func(p string) string {
		if in.ResolvePath != nil {
			p = in.ResolvePath(p)
		}
		return normPath(filepath2slash(p))
	}
	namedSet := map[string]bool{}
	for _, p := range named {
		namedSet[canon(p)] = true
	}
	var missing []string
	for _, p := range loaded {
		if !namedSet[canon(p)] {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		row.Verdict, row.Pass = VerdictAgree, true
		row.Note = fmt.Sprintf("the probe named every one of the %d files the inspector grades as loaded", len(loaded))
		return row
	}
	row.Verdict, row.Pass = VerdictDisagree, false
	row.Note = fmt.Sprintf("the probe did not name: %s. A finding to investigate, not automatically an inspector bug — agents summarize and may see a file under another spelling (mirrors, symlinks)", strings.Join(missing, ", "))
	return row
}

// compareSkillState grades loaded-versus-listed for the planted skill. The
// inspector's model says a listed skill's catalogue LINE is in the prefix and
// its body is not; the probe, which can see its own context, is asked which
// of the two states it observes.
func compareSkillState(in Input, wire *wireReport, reply string) Row {
	row := Row{
		ID:    claimSkillState,
		Claim: fmt.Sprintf("the skill %q is listed (name and description in a catalogue), not fully loaded", in.Recipe.SkillName),
	}
	item, found := findSkill(wire, in.Recipe.SkillName)

	word := stateWord(reply)
	row.Testimony = word
	if word == "" {
		row.Testimony = "(no single LISTED/LOADED verdict in the reply)"
	}

	switch {
	case !found:
		row.Inspector = "—"
		row.Verdict, row.Pass = VerdictUnverifiable, false
		row.Note = "the inspector has no item for the planted skill; the recipe put it on disk and the probe was launched in that directory, so this is a finding"
	case !strings.Contains(item.Detail, listedAtStartupTag) || item.Load.State != loadStateLoaded:
		row.Inspector = fmt.Sprintf("state=%s detail=%q", item.Load.State, item.Detail)
		row.Verdict, row.Pass = VerdictUnverifiable, false
		row.Note = "the inspector reports the skill in a state this comparison has no testimony mapping for; grade it by hand"
	case word == "":
		row.Inspector = "LISTED — catalogue line in the prefix, body on disk"
		row.Verdict, row.Pass = VerdictUnverifiable, false
		row.Note = "the reply carries neither exactly one LISTED nor exactly one LOADED, so the testimony is not a checkable string; re-ask"
	case word == "LISTED":
		row.Inspector = "LISTED — catalogue line in the prefix, body on disk"
		row.Verdict, row.Pass = VerdictAgree, true
		row.Note = "both sides say the catalogue line is present and the body is not"
	default:
		row.Inspector = "LISTED — catalogue line in the prefix, body on disk"
		row.Verdict, row.Pass = VerdictDisagree, false
		row.Note = "the probe says the full body is loaded and the inspector says it is not; a finding to investigate — either the harness preloads bodies now, or the agent misread its own context"
	}
	return row
}

// compareSkillQuote grades the inspector's catalogue text for the planted
// skill against the description the probe quotes.
func compareSkillQuote(in Input, wire *wireReport, reply string) Row {
	row := Row{
		ID:    claimSkillQuote,
		Claim: "the planted skill's description reaches the probe exactly as written, and the inspector's catalogue text carries the same bytes",
	}
	item, found := findSkill(wire, in.Recipe.SkillName)
	desc := collapseSpace(in.Recipe.SkillDescription)
	inTestimony := strings.Contains(collapseSpace(reply), desc)
	row.Testimony = condense(pickQuotedLine(reply, in.Recipe.Nonce))

	switch {
	case !found:
		row.Inspector = "—"
		row.Verdict, row.Pass = VerdictUnverifiable, false
		row.Note = "the inspector has no item for the planted skill, so its text claim could not be read"
	case !strings.Contains(collapseSpace(item.Content.Text), desc):
		row.Inspector = condense(item.Content.Text)
		row.Verdict, row.Pass = VerdictDisagree, false
		row.Note = "the inspector's catalogue text for this skill does not carry the recipe's description; a finding — the split may have mis-assigned catalogue lines"
	case !inTestimony:
		row.Inspector = condense(item.Content.Text)
		row.Verdict, row.Pass = VerdictDisagree, false
		row.Note = "the probe could not quote the planted description; a finding to investigate, not automatically an inspector bug — the agent may have paraphrased, so re-ask before concluding the catalogue never reached it"
	default:
		row.Inspector = condense(item.Content.Text)
		row.Verdict, row.Pass = VerdictAgree, true
		row.Note = "the recipe's description, nonce included, appears verbatim in the probe's quote and in the inspector's catalogue text"
	}
	return row
}

func findSkill(wire *wireReport, name string) (wireItem, bool) {
	for _, it := range categoryItems(wire, categorySkills) {
		if it.ID == skillIDPrefix+name {
			return it, true
		}
	}
	return wireItem{}, false
}

// ---------------------------------------------------------------------------
// testimony text handling
// ---------------------------------------------------------------------------

// pickQuotedLine finds the reply line carrying the marker — agents wrap
// quotes in prose despite instructions, and the marker is what makes the
// quote findable anyway. Falls back to the whole trimmed reply.
func pickQuotedLine(reply, marker string) string {
	for _, line := range strings.Split(reply, "\n") {
		if marker != "" && strings.Contains(line, marker) {
			return strings.TrimSpace(line)
		}
	}
	return strings.TrimSpace(reply)
}

// normalizeQuote strips the wrapping an agent adds around a verbatim quote —
// backticks, straight and curly quotes — without touching the quote's body.
func normalizeQuote(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`\"'“”‘’")
	return strings.TrimSpace(s)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// stateWord returns "LISTED" or "LOADED" when the reply commits to exactly
// one of them, and "" otherwise. Both-or-neither is not testimony, it is
// noise, and grading noise would be the laundering this package refuses.
func stateWord(reply string) string {
	listed := len(wordListed.FindAllString(reply, -1))
	loaded := len(wordLoaded.FindAllString(reply, -1))
	switch {
	case listed > 0 && loaded == 0:
		return "LISTED"
	case loaded > 0 && listed == 0:
		return "LOADED"
	default:
		return ""
	}
}

var (
	wordListed = regexp.MustCompile(`\bLISTED\b`)
	wordLoaded = regexp.MustCompile(`\bLOADED\b`)
	// pathRe matches absolute POSIX paths in prose. Trailing sentence
	// punctuation is trimmed afterwards rather than encoded here.
	pathRe = regexp.MustCompile(`(?:^|[\s,;("'` + "`" + `])(/[^\s,;)"'` + "`" + `]+)`)
)

// extractPaths pulls the absolute paths out of a reply, in order, deduplicated.
func extractPaths(reply string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range pathRe.FindAllStringSubmatch(reply, -1) {
		p := strings.TrimRight(m[1], ".:!?")
		if p == "/" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// condense shortens a value for a table cell without deciding anything on the
// shortened form — every grade above is computed on the full text first.
func condense(s string) string {
	s = collapseSpace(s)
	if len(s) > 160 {
		return s[:157] + "…"
	}
	return s
}
