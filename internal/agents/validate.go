package agents

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Severity separates "this definition is wrong" from "a human should look".
type Severity string

const (
	// SeverityError means the definition is invalid and must not be treated
	// as usable.
	SeverityError Severity = "error"
	// SeverityWarn means it parses and is usable, but something in it is
	// suspect — most often portability rot copied in from the source.
	SeverityWarn Severity = "warn"
)

// Finding is one validation result.
type Finding struct {
	Severity Severity `json:"severity"`
	Field    string   `json:"field"`
	Message  string   `json:"message"`
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: %s: %s", f.Severity, f.Field, f.Message)
}

// Findings is an ordered set of results.
type Findings []Finding

// HasErrors reports whether any finding is fatal.
func (fs Findings) HasErrors() bool {
	for _, f := range fs {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (fs *Findings) errorf(field, format string, args ...any) {
	*fs = append(*fs, Finding{Severity: SeverityError, Field: field, Message: fmt.Sprintf(format, args...)})
}

func (fs *Findings) warnf(field, format string, args ...any) {
	*fs = append(*fs, Finding{Severity: SeverityWarn, Field: field, Message: fmt.Sprintf(format, args...)})
}

// harnessTokens are names a portable role must not contain. A role describes a
// profession; the post picks the tool. Catching this at validation is what
// keeps "switch harness with one field" honest.
var harnessTokens = []string{
	"claude", "codex", "deepseek", "hermes", "gemini", "copilot", "tmux",
}

// Credential detection is best-effort in BOTH directions and is documented as
// such. It exists to stop the obvious leak — a token pasted into a conductor's
// Markdown — not to be a proof. A miss puts a secret in the registry; a false
// positive silently drops the file the role is built from. Both are real
// costs, so the rules below ask for a credential-SHAPED value, and ask for
// context whenever the shape alone is ambiguous.

// secretWord names text as being ABOUT a credential. On its own it proves
// nothing: "never put a token in a role directory" is good policy prose.
var secretWord = regexp.MustCompile(`(?i)(api[_ -]?key|secret|token|password|passwd|passphrase|bearer|authorization|client[_ -]?secret|private[_ -]?key|credential)`)

// credentialPrefix matches issued-credential shapes that are self-evident
// wherever they appear, with no surrounding context needed.
var credentialPrefix = regexp.MustCompile(
	`\b(sk-[A-Za-z0-9_\-]{16,}` +
		`|gh[pousr]_[A-Za-z0-9]{16,}` +
		`|xox[baprs]-[A-Za-z0-9\-]{10,}` +
		`|AKIA[0-9A-Z]{12,}` +
		`|eyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,})\b` +
		`|-----BEGIN [A-Z ]*PRIVATE KEY-----`)

// uriCredential matches a password embedded in a connection URI, e.g.
// postgres://svc:hunter2@db.example.com:5432/app.
var uriCredential = regexp.MustCompile(`\b[a-z][a-z0-9+.\-]*://[^\s:/@]+:[^\s@/]{3,}@`)

// groupedToken matches a value transcribed in groups — "abcd efgh ijkl mnop",
// "7f3a-91b2-cc40". Group length is 3-8 rather than exactly 4.
var groupedToken = regexp.MustCompile(`\b[A-Za-z0-9]{3,8}(?:[ -][A-Za-z0-9]{3,8}){2,}\b`)

// secretAssignment matches a secret-named key being given a value. The value
// is checked separately: "token: use the connector store" is an assignment by
// shape and prose by content.
var secretAssignment = regexp.MustCompile(
	`(?i)\b(api[_-]?key|secret|token|password|passwd|passphrase|bearer|client[_-]?secret|private[_-]?key|app[_-]?password)\b[^\n]{0,24}?[:=]\s*(\S.*)$`)

// pureHex matches a git SHA or a hash digest, which are not credentials and
// are common in a LEARNINGS.md.
var pureHex = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// wordToken matches a single natural-language word.
var wordToken = regexp.MustCompile(`^[A-Za-z]+$`)

// charClasses counts which character classes a token draws on.
func charClasses(token string) (lower, upper, digit, symbol bool) {
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		case r == '-' || r == '_' || r == '+' || r == '/' || r == '=' || r == '.':
			symbol = true
		}
	}
	return
}

// looksLikeIdentifier reports whether a token is a kebab/snake identifier —
// several short segments — rather than an issued secret. Segments may contain
// digits ("...-checklist-v2"), which an alphabetic-only test rejected.
func looksLikeIdentifier(token string) bool {
	segments := strings.FieldsFunc(token, func(r rune) bool { return r == '-' || r == '_' })
	if len(segments) < 3 {
		return false
	}
	for _, segment := range segments {
		if len(segment) > 14 || !alnumToken.MatchString(segment) {
			return false
		}
	}
	return true
}

var alnumToken = regexp.MustCompile(`^[A-Za-z0-9]+$`)

// structuralChars are the characters that mark a token as markup, a path, or a
// URL rather than a credential: a Markdown table rule, a file path and a
// fenced code span are all long strings that are plainly not secrets.
// structuralChars mark a token as markup rather than a credential: a Markdown
// table rule, a fenced span, a bracketed placeholder.
//
// "/" is deliberately NOT here. Base64 secrets carry "/" and "+" routinely —
// the canonical AWS secret access key does — and excluding the whole token on
// sight let one through in an explicit `aws_secret_access_key =` assignment.
// Paths are excluded by shape instead, in looksLikePath.
const structuralChars = "|\\<>()[]{}~!?,;\"'" + "`" + "*"

// looksLikePath reports whether a slash-bearing token is a filesystem path or
// URL rather than a base64 secret.
func looksLikePath(token string) bool {
	if strings.Contains(token, "://") || strings.HasPrefix(token, "/") || strings.HasPrefix(token, "~") || strings.HasPrefix(token, "./") {
		return true
	}
	return strings.Count(token, "/") >= 3
}

// looksLikeMixedCaseIdentifier reports whether an all-alphabetic token is a
// CamelCase identifier — a Go test or type name — rather than a secret.
// Several case transitions in a row is what distinguishes
// "TestContextPagerNilReceiverIsSafe" from an unbroken random run.
func looksLikeMixedCaseIdentifier(token string) bool {
	if !wordToken.MatchString(token) {
		return false
	}
	transitions := 0
	for i := 1; i < len(token); i++ {
		prev, cur := token[i-1], token[i]
		if prev >= 'a' && prev <= 'z' && cur >= 'A' && cur <= 'Z' {
			transitions++
		}
	}
	return transitions >= 3
}

// credentialTokenShape reports whether a token could be a credential at all,
// before any length or context rule is applied.
func credentialTokenShape(token string) (string, bool) {
	trimmed := strings.Trim(token, `"'`+"`"+`.,;:()[]{}<>`)
	if len(trimmed) < 12 || strings.ContainsAny(trimmed, structuralChars) || looksLikePath(trimmed) {
		return "", false
	}
	// A slash-bearing token that draws on only one character class is a
	// relative path, not a secret: "internal/agents/validate.go" is a
	// perfectly ordinary thing to write on a line that also says "token".
	// A base64 secret carrying a slash spans several classes — the canonical
	// AWS key has lower, upper and digits — so this separates them without
	// reintroducing the two-slash path rule that would have swallowed it.
	if strings.Contains(trimmed, "/") && namedClasses(trimmed) < 2 {
		return "", false
	}
	// Mostly alphanumeric, and containing at least one letter. A run of
	// punctuation is not a secret however long it is.
	if !mostlyAlphanumeric(trimmed) {
		return "", false
	}
	if looksLikeIdentifier(trimmed) {
		return "", false
	}
	return trimmed, true
}

// mostlyAlphanumeric reports whether a token is predominantly letters and
// digits and contains at least one letter.
func mostlyAlphanumeric(token string) bool {
	alnum, letters := 0, 0
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			letters++
			alnum++
		case r >= '0' && r <= '9':
			alnum++
		}
	}
	return letters > 0 && alnum*100 >= len(token)*85
}

// hasSeparators reports whether a token is broken up by - or _, which every
// natural compound term is and an unbroken secret is not.
func hasSeparators(token string) bool {
	return strings.ContainsAny(token, "-_")
}

// namedClasses counts the character classes that carry information: symbols
// are excluded, because "model-unavailable" is not a mixed-alphabet secret.
func namedClasses(token string) int {
	lower, upper, digit, _ := charClasses(token)
	count := 0
	for _, present := range []bool{lower, upper, digit} {
		if present {
			count++
		}
	}
	return count
}

// selfEvidentSecretValue reports whether a token is credential-shaped strongly
// enough to flag with no surrounding context.
func selfEvidentSecretValue(token string) bool {
	trimmed, ok := credentialTokenShape(token)
	if !ok || pureHex.MatchString(trimmed) {
		// A hex digest needs context: a bare git SHA is not a credential,
		// while "API key: <hex>" is — and that is the assignment path.
		return false
	}
	switch {
	case len(trimmed) >= 24 && namedClasses(trimmed) >= 3:
		return true
	case len(trimmed) >= 28 && !hasSeparators(trimmed) && !looksLikeMixedCaseIdentifier(trimmed):
		// A single-case unbroken run this long is not a word in any document:
		// base32 seeds and lowercase-only keys land here. A CamelCase symbol
		// name is excluded — "TestContextPagerNilReceiverIsSafe" is exactly the
		// kind of thing a LEARNINGS.md records.
		return true
	}
	return false
}

// assignmentSecretValue reports whether a value given to a secret-named key is
// credential-shaped.
//
// Two exemptions are deliberately skipped here, because an explicit
// `password: <value>` names the value for us and the shape heuristics no
// longer have to carry the decision alone:
//   - pure hex, so "API key: <40 hex>" is caught while a bare git SHA is not;
//   - the identifier exemption, so a dash-separated passphrase
//     ("Tr0ub4dor-and-3-horses") is caught while
//     "release-candidate-verification-checklist-v2" in prose is not.
//
// The second exemption is a KNOWN, accepted trade rather than an oversight. A
// passphrase password and a kebab-case document slug are the same shape —
// "correct-horse-battery-staple" could be either — so a secret-named key
// introducing a slug ("Secret runbook: quarterly-key-rotation-checklist") is
// refused. Catching a real passphrase is worth occasionally declining to copy
// a file, because that failure is LOUD: a warning naming the line plus an
// unresolved item on the definition, never a silent drop. Narrowing this
// further would reopen the passphrase miss, which is silent.
func assignmentSecretValue(token string) bool {
	trimmed := strings.Trim(token, `"'`+"`"+`.,;:()[]{}<>`)
	if len(trimmed) < 16 || strings.ContainsAny(trimmed, structuralChars) || looksLikePath(trimmed) {
		return false
	}
	return mostlyAlphanumeric(trimmed)
}

// introducedSecretValue reports whether a value introduced directly by a
// secret word on the same line ("The app password is correct-horse-battery")
// is credential-shaped. Same reasoning as assignmentSecretValue: the sentence
// has already told us what the value is.
func introducedSecretValue(line string) bool {
	for _, loc := range secretWord.FindAllStringIndex(line, -1) {
		remainder := strings.TrimSpace(line[loc[1]:])
		for _, filler := range []string{"is", "was", "=", ":"} {
			remainder = strings.TrimSpace(strings.TrimPrefix(remainder, filler))
		}
		fields := strings.Fields(remainder)
		if len(fields) == 0 {
			continue
		}
		if assignmentSecretValue(fields[0]) && hasSeparators(fields[0]) {
			return true
		}
	}
	return false
}

// contextualSecretValue reports whether a token is credential-shaped on a line
// that is already ABOUT a credential, where a lower bar is appropriate.
func contextualSecretValue(token string) bool {
	trimmed, ok := credentialTokenShape(token)
	if !ok {
		return false
	}
	if namedClasses(trimmed) >= 2 && !hasSeparators(trimmed) {
		return true
	}
	// A single-class unbroken run: an app password with its spaces removed.
	return len(trimmed) >= 14 && !hasSeparators(trimmed) && namedClasses(trimmed) >= 1
}

// dashGroupedKey matches a key transcribed as dash-separated hex quads, e.g.
// "7f3a-91b2-cc40".
//
// This is the ONLY grouped shape flagged without context, and it is
// deliberately narrow. A previous version flagged any run of short chunks
// where one chunk mixed a letter and a digit — which makes "g14", "60s",
// "sha256", "utf8" and "x86" credentials, and refused this fleet's own
// documents. Separator and alphabet both have to be right here.
var dashGroupedKey = regexp.MustCompile(`\b[0-9a-fA-F]{4}(?:-[0-9a-fA-F]{4}){2,}\b`)

// groupedSecretToken reports whether a line carries a dash-grouped hex key.
func groupedSecretToken(line string) bool {
	return dashGroupedKey.MatchString(line)
}

// uniformGroupedToken reports whether a string is ENTIRELY a value
// transcribed as equal-length groups, e.g. "abcd efgh ijkl mnop".
//
// Uniformity alone is not enough: "keep them safe from harm" is five equal
// four-letter words of ordinary English. So this is only ever consulted for a
// string that is either the whole line (under a credential heading) or the
// remainder immediately after a secret word — never for a run buried in a
// sentence.
func uniformGroupedToken(line string) bool {
	trimmed := strings.TrimSpace(strings.Trim(strings.TrimSpace(line), `"'`+"`"+`.,;:`))
	if trimmed == "" || !groupedToken.MatchString(trimmed) {
		return false
	}
	// The whole string must BE the run.
	if groupedToken.FindString(trimmed) != trimmed {
		return false
	}
	groups := splitGroups(trimmed)
	if len(groups) < 3 {
		return false
	}
	width := len(groups[0])
	if width < 4 {
		return false
	}
	for _, group := range groups {
		if len(group) != width {
			return false
		}
	}
	return true
}

// groupedAfterSecretWord reports whether a transcribed value immediately
// follows a secret word on the same line, as in "...the app password abcd efgh
// ijkl mnop". Anchoring to the secret word is what keeps ordinary prose out:
// "Token handling rules follow" has a secret word too, but what follows it is
// not a uniform transcription.
func groupedAfterSecretWord(line string) bool {
	for _, loc := range secretWord.FindAllStringIndex(line, -1) {
		remainder := strings.TrimLeft(line[loc[1]:], " \t:=is")
		if uniformGroupedToken(remainder) {
			return true
		}
	}
	return false
}

func splitGroups(token string) []string {
	return strings.FieldsFunc(token, func(r rune) bool { return r == ' ' || r == '-' })
}

// contextLines returns the recent non-empty lines that can supply "this is
// about a credential" context for the line at index i.
//
// A heading followed by its value is the most natural way a credential ends up
// in Markdown, and a strictly per-line scan cannot see it. Fence markers and
// bare heading punctuation are skipped so a value inside a fenced block still
// sees the heading above it.
func contextLines(lines []string, i int) string {
	// Bounded by DISTANCE, not by content-line count. Skipping blanks and
	// fences "for free" gave a fenced, code-heavy file — which is exactly what
	// a conductor CLAUDE.md is — an unbounded window, so any line could
	// inherit credential context from far above it.
	const window = 4
	var collected []string
	for j := i - 1; j >= 0 && j >= i-window; j-- {
		candidate := strings.TrimSpace(lines[j])
		if candidate == "" || strings.HasPrefix(candidate, "```") || strings.Trim(candidate, "#-* ") == "" {
			continue
		}
		collected = append(collected, candidate)
	}
	return strings.Join(collected, "\n")
}

// scanTableRow tests each cell of a Markdown table row as if it were its own
// line, so "| GMAIL_APP_PASSWORD | abcd efgh ijkl mnop |" is seen.
func scanTableRow(row string) bool {
	cells := strings.Split(strings.Trim(row, "| "), "|")
	if len(cells) < 2 {
		return false
	}
	var context string
	for _, cell := range cells {
		context += " " + strings.TrimSpace(cell)
	}
	if !secretWord.MatchString(context) {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if cell == "" || secretWord.MatchString(cell) {
			continue
		}
		if uniformGroupedToken(cell) {
			return true
		}
		for _, token := range strings.Fields(cell) {
			if contextualSecretValue(token) {
				return true
			}
		}
	}
	return false
}

// ScanForCredentials returns the 1-based line numbers of lines that look like
// they carry a real credential. See the note at the top of this block: it is
// best-effort, and deliberately asks for a credential-shaped VALUE rather than
// firing on the mere mention of one.
func ScanForCredentials(body string) []int {
	lines := strings.Split(body, "\n")
	var found []int

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// A Markdown table row holds independent cells; a value in one of them
		// is not "the whole line", so scan each cell as its own line too.
		if strings.Count(trimmed, "|") >= 2 {
			if scanTableRow(trimmed) {
				found = append(found, i+1)
				continue
			}
		}

		// Self-evident: an issued token, a URI password, a PEM header, a
		// grouped value that mixes letters and digits, or a long mixed-class
		// run. None of these need context.
		if credentialPrefix.MatchString(trimmed) || uriCredential.MatchString(trimmed) || groupedSecretToken(trimmed) {
			found = append(found, i+1)
			continue
		}
		selfEvident := false
		for _, token := range strings.Fields(trimmed) {
			if selfEvidentSecretValue(token) {
				selfEvident = true
				break
			}
		}
		if selfEvident {
			found = append(found, i+1)
			continue
		}

		// Contextual: this line, or the few lines above it, is about a
		// credential AND this line carries a credential-shaped value.
		context := trimmed + "\n" + contextLines(lines, i)
		if !secretWord.MatchString(context) {
			continue
		}
		if assignment := secretAssignment.FindStringSubmatch(trimmed); assignment != nil {
			value := strings.TrimSpace(assignment[2])
			if uniformGroupedToken(value) {
				found = append(found, i+1)
				continue
			}
			if fields := strings.Fields(value); len(fields) > 0 && assignmentSecretValue(fields[0]) {
				found = append(found, i+1)
				continue
			}
		}
		// A transcription either IS the line (under a credential heading) or
		// follows a secret word directly. A uniform run buried in a sentence
		// is prose — "keep them safe from harm" is five equal four-letter words.
		if uniformGroupedToken(trimmed) || groupedAfterSecretWord(trimmed) || introducedSecretValue(trimmed) {
			found = append(found, i+1)
			continue
		}
		for _, token := range strings.Fields(trimmed) {
			if contextualSecretValue(token) {
				found = append(found, i+1)
				break
			}
		}
	}
	return found
}

// hostnamePattern catches machine-specific references in role content.
var hostnamePattern = regexp.MustCompile(`(?i)\b([a-z0-9][a-z0-9\-]*\.(local|lan|internal|home|arpa))\b|\b\d{1,3}(\.\d{1,3}){3}\b`)

// ValidateRole checks a role manifest for the portability invariants. It does
// not read the role's Markdown bodies; ValidateRoleContent does that.
func ValidateRole(r *Role) Findings {
	var fs Findings
	if r == nil {
		fs.errorf("role", "role is nil")
		return fs
	}
	if strings.TrimSpace(r.APIVersion) != APIVersion {
		fs.errorf("role.apiVersion", "want %q, got %q", APIVersion, r.APIVersion)
	}
	if strings.TrimSpace(r.Kind) != KindRole {
		fs.errorf("role.kind", "want %q, got %q", KindRole, r.Kind)
	}
	if strings.TrimSpace(r.Metadata.Name) == "" {
		fs.errorf("role.metadata.name", "required")
	}
	if strings.TrimSpace(r.Metadata.Version) == "" {
		fs.errorf("role.metadata.version", "required")
	}

	// Every file reference must be relative and contained in the role dir.
	refs := map[string]string{}
	if r.Spec.Entrypoint != "" {
		refs["role.spec.entrypoint"] = r.Spec.Entrypoint
	}
	for i, p := range r.Spec.Policy {
		refs[fmt.Sprintf("role.spec.policy[%d]", i)] = p
	}
	for name, p := range r.Spec.Playbooks {
		refs["role.spec.playbooks."+name] = p
	}
	for name, p := range r.Spec.Workflows {
		refs["role.spec.workflows."+name] = p
	}
	if r.Spec.Learnings != nil && r.Spec.Learnings.File != "" {
		refs["role.spec.learnings.file"] = r.Spec.Learnings.File
	}

	fields := make([]string, 0, len(refs))
	for field := range refs {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		ref := refs[field]
		if filepath.IsAbs(ref) {
			fs.errorf(field, "absolute path %q; role references must be relative to the role directory", ref)
			continue
		}
		if !filepath.IsLocal(filepath.Clean(ref)) {
			fs.errorf(field, "%q escapes the role directory", ref)
		}
	}

	// A role that names a harness is not portable.
	lowerName := strings.ToLower(r.Metadata.Name + " " + r.Spec.Description)
	for _, tok := range harnessTokens {
		if strings.Contains(lowerName, tok) {
			fs.warnf("role.metadata.name", "mentions harness %q; roles should be harness-neutral", tok)
			break
		}
	}
	return fs
}

// ValidateRoleContent checks one role file body for the things a role must
// never carry.
//
// A credential is an ERROR, not a warning: the caller must not copy the body
// into the registry, because doing so would put the secret in a second place
// on disk. Portability rot — a hostname, an absolute home path — stays a
// warning, because the user's Markdown is authoritative and is never rewritten.
func ValidateRoleContent(field, body string) Findings {
	var fs Findings
	if lines := ScanForCredentials(body); len(lines) > 0 {
		fs.errorf(field, "line %s looks like it carries a credential; it was not copied into the role. "+
			"Move the value into a connector's private store, then re-adopt", formatLineList(lines))
	}
	if hostnamePattern.MatchString(body) {
		fs.warnf(field, "contains a hostname or IP; machine specifics belong on the post, not the role")
	}
	if strings.Contains(body, "/Users/") || strings.Contains(body, "/home/") {
		fs.warnf(field, "contains an absolute home path; it will not resolve on another machine")
	}
	return fs
}

// formatLineList renders line numbers for a message, bounded so a file full of
// exports does not produce an unreadable finding.
func formatLineList(lines []int) string {
	const max = 5
	parts := make([]string, 0, max+1)
	for i, line := range lines {
		if i == max {
			parts = append(parts, fmt.Sprintf("and %d more", len(lines)-max))
			break
		}
		parts = append(parts, strconv.Itoa(line))
	}
	return strings.Join(parts, ", ")
}

// secretPattern names an identifier that refers to a credential. Used for
// connector names and environment KEY names, where there is no value to scan.
var secretPattern = secretWord

// ValidatePost checks a post manifest.
func ValidatePost(p *Post) Findings {
	var fs Findings
	if p == nil {
		fs.errorf("post", "post is nil")
		return fs
	}
	if strings.TrimSpace(p.APIVersion) != APIVersion {
		fs.errorf("post.apiVersion", "want %q, got %q", APIVersion, p.APIVersion)
	}
	if strings.TrimSpace(p.Kind) != KindPost {
		fs.errorf("post.kind", "want %q, got %q", KindPost, p.Kind)
	}
	if strings.TrimSpace(p.Metadata.Name) == "" {
		fs.errorf("post.metadata.name", "required")
	}
	if strings.TrimSpace(p.Metadata.PostID) == "" {
		fs.errorf("post.metadata.postId", "required; triggers and delivery target the post id, never the title")
	}
	if strings.TrimSpace(p.Spec.Role.Name) == "" {
		fs.errorf("post.spec.role.name", "required")
	}

	// Every chain must terminate at a human principal.
	if strings.TrimSpace(p.Spec.Placement.ReportsTo) == "" {
		fs.errorf("post.spec.placement.reportsTo", "required; every post reports to a manager post or a human principal")
	}

	switch p.Spec.Classification {
	case ClassAgent, ClassConnector, ClassService, ClassExternal, ClassDebris:
	case "":
		fs.errorf("post.spec.classification", "required")
	default:
		fs.errorf("post.spec.classification", "unknown classification %q", p.Spec.Classification)
	}

	// Phase-1 invariant. Recognition only: nothing this package emits or reads
	// may claim to be live, and no trigger it carries may claim to be armed.
	if p.Spec.Enabled {
		fs.errorf("post.spec.enabled", "phase 1 emits disabled posts only")
	}

	seen := map[string]bool{}
	for i, t := range p.Spec.Triggers {
		field := fmt.Sprintf("post.spec.triggers[%d]", i)
		if strings.TrimSpace(t.Name) == "" {
			fs.errorf(field+".name", "required")
		} else if seen[t.Name] {
			fs.errorf(field+".name", "duplicate trigger name %q", t.Name)
		}
		seen[t.Name] = true

		switch t.Type {
		case TriggerCron, TriggerMailDoorbell, TriggerFileWatch, TriggerWebhook, TriggerSessionTransition, TriggerOpaque:
		default:
			fs.errorf(field+".type", "unknown trigger kind %q", t.Type)
		}
		if t.Enabled {
			fs.errorf(field+".enabled", "phase 1 emits disabled triggers only; the source automation still owns the firing")
		}
		if !t.External {
			fs.errorf(field+".external", "phase 1 triggers are external: the plist, timer or manager that fires today keeps owning it")
		}
		if t.External && strings.TrimSpace(t.ExternalSource) == "" {
			fs.errorf(field+".externalSource", "an external trigger must name the file that owns its firing")
		}
		if t.Type == TriggerCron && t.Schedule != "" && strings.TrimSpace(t.Timezone) == "" {
			fs.warnf(field+".timezone", "cron schedule without an explicit timezone; next-due rendering assumes local time")
		}
		// The injection invariant, enforced rather than documented.
		if placeholderPattern.MatchString(t.Deliver) {
			fs.errorf(field+".deliver", "delivery string interpolates source-controlled content; it must be a fixed local string")
		}
	}

	for i, c := range p.Spec.Connectors {
		field := fmt.Sprintf("post.spec.connectors[%d]", i)
		if strings.TrimSpace(c.Name) == "" {
			fs.errorf(field+".name", "required")
		}
		if secretPattern.MatchString(c.Name) {
			fs.warnf(field+".name", "connector name looks like a credential reference; connectors hold references, never secrets")
		}
	}
	return fs
}

// placeholderPattern catches the convenient-but-forbidden templates from the
// design, plus the shell-flavoured ones a hand-written definition reaches for:
// {{sender}}, ${SUBJECT}, $SENDER, $(cmd), backticks, %(path)s and %s.
//
// Adoption cannot produce any of these — fixedDeliveryFor is a closed switch
// over role constants — so this guards hand-authored and imported
// definitions, which is exactly the population a validator exists for.
var placeholderPattern = regexp.MustCompile(
	`\{\{[^}]*\}\}` +
		`|\$\{[A-Za-z_][A-Za-z0-9_]*\}` +
		`|\$[A-Za-z_][A-Za-z0-9_]*` +
		`|\$\([^)]*\)` +
		"|`[^`]*`" +
		`|%\([A-Za-z_]+\)s` +
		`|%[sdvq]\b`)

// ValidateDefinition validates a post together with its role.
func ValidateDefinition(p *Post, r *Role) Findings {
	fs := ValidatePost(p)
	if r != nil {
		fs = append(fs, ValidateRole(r)...)
		if p != nil && r.Metadata.Name != "" && p.Spec.Role.Name != "" && p.Spec.Role.Name != r.Metadata.Name {
			fs.errorf("post.spec.role.name", "post names role %q but the role manifest is %q", p.Spec.Role.Name, r.Metadata.Name)
		}
	}
	return fs
}

// ValidateReportsTo walks the reports_to graph across a set of posts and
// reports cycles and chains that never reach a human principal. Adoption emits
// a flat "everyone reports to the manager" shape, but a hand-edited registry
// can grow a loop, and a loop is the one error that makes escalation silently
// unreachable.
func ValidateReportsTo(posts []*Post) Findings {
	var fs Findings
	byName := map[string]*Post{}
	for _, p := range posts {
		if p != nil && p.Metadata.Name != "" {
			byName[p.Metadata.Name] = p
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		visited := map[string]bool{}
		current := byName[name]
		reachedHuman := false
		alreadyReported := false
		for current != nil {
			if visited[current.Metadata.Name] {
				fs.errorf("post."+name+".spec.placement.reportsTo",
					"reports_to cycle through %q; escalation would never reach a human", current.Metadata.Name)
				alreadyReported = true
				break
			}
			visited[current.Metadata.Name] = true
			next := strings.TrimSpace(current.Spec.Placement.ReportsTo)
			if strings.HasPrefix(next, "human:") {
				reachedHuman = true
				break
			}
			parent, ok := byName[next]
			if !ok {
				fs.warnf("post."+name+".spec.placement.reportsTo",
					"reports to %q, which is not a known post or a human principal", next)
				alreadyReported = true
				break
			}
			current = parent
		}
		// A chain that ends without reaching a human principal is reported.
		// The previous form computed reachedHuman and then discarded it, so
		// an orphaned chain produced no finding at all.
		if !reachedHuman && !alreadyReported {
			fs.warnf("post."+name+".spec.placement.reportsTo",
				"reports_to chain does not terminate at a human principal; escalation has no destination")
		}
	}
	return fs
}
