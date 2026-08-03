// Package script defines SIXGATE's G0 artifact: the user journey written as
// literal keystrokes, authored BEFORE the feature is built.
//
// The framework exists because a feature was declared done on the strength of
// code analysis, corpus replay and adversarial review while nobody ever pressed
// the key. G0 is the gate that makes that impossible to repeat: if no one can
// write down the keys a human would press and what should appear on screen,
// the feature is not defined yet and there is nothing to build.
//
// A G0 script is deliberately driver-agnostic. It names keys, waits and
// expectations against the *rendered screen* — never against internals — so the
// same script can be executed by a bubbletea in-process driver, a real binary
// in a tmux pane, a browser harness or a mobile runner without editing a line.
// The schema below is the universal half of SIXGATE; drivers are the swappable
// half.
package script

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only `version:` a script may declare. It is bumped when
// a change would silently reinterpret an existing script; additive optional
// fields do not bump it.
const SchemaVersion = 1

// Script is a parsed G0 journey.
type Script struct {
	// Version pins the schema. Must equal SchemaVersion.
	Version int `yaml:"version"`

	// Slug names the feature and the artifact directory (docs/gates/<slug>).
	Slug string `yaml:"slug"`

	// Sentence is the journey in one line of English, in the words of whoever
	// asked for the feature. It is the thing a cold-eye reviewer is given.
	Sentence string `yaml:"sentence"`

	// Term is the terminal geometry the script assumes. Required: a screen
	// assertion without a known width is not reproducible.
	Term Term `yaml:"term"`

	// Owns lists the source files this feature is responsible for. VERDICT.md
	// hashes them so a transcript recorded three commits ago stops counting as
	// evidence. Two forms are supported, both repo-root relative:
	//
	//	internal/ui/context_pager.go        plain path or filepath.Glob pattern
	//	internal/ctxinspect/...             Go-style recursive directory
	Owns []string `yaml:"owns"`

	// Allow suppresses named Blank Detector rules for this feature. Every entry
	// must carry a justification; an unexplained suppression is how a blank
	// percentage ships.
	Allow []AllowEntry `yaml:"banned_screen_patterns_allow"`

	// Steps is the journey. Order is significant.
	Steps []Step `yaml:"steps"`
}

// Term is the terminal geometry a script is written against.
type Term struct {
	Width  int `yaml:"width"`
	Height int `yaml:"height"`
}

// AllowEntry suppresses one Blank Detector rule, with a reason on the record.
type AllowEntry struct {
	// Pattern is a Blank Detector rule ID (see internal/sixgate/lint).
	Pattern string `yaml:"pattern"`
	// Steps scopes the suppression to those capture steps. Empty suppresses the
	// rule across the whole journey.
	//
	// Scoping exists because the first real run produced the case for it. The
	// orphan-percent rule fired on the session list's filter legend, where a
	// bare "%" is the key you press rather than a number that failed to render
	// — and silencing it journey-wide would have disarmed it on the pager
	// frames, which is the exact screen the blank percentage shipped on. A
	// suppression that has to name the frames it covers keeps the guard where
	// the bug lives; a blanket one quietly removes it.
	Steps []string `yaml:"steps,omitempty"`
	// Justification explains why this rule cannot fire meaningfully here.
	Justification string `yaml:"justification"`
}

// Step is one beat of the journey: an action, an optional screen capture, and
// the expectations that capture must satisfy.
type Step struct {
	// ID is the artifact filename prefix, e.g. "03-inspector". Ordered
	// two-digit prefix so a directory listing reads as the journey.
	ID string `yaml:"id"`
	// Do is the action performed before capturing.
	Do Action `yaml:"do"`
	// Capture, when set, names the frame written for this step. A step without
	// a capture is a pure navigation beat and carries no expectations.
	Capture string `yaml:"capture"`
	// Expect are assertions read from the captured frame only.
	Expect []Expect `yaml:"expect"`
	// Note is free-text intent, reproduced into the transcript.
	Note string `yaml:"note"`
}

// Action is the verb of a step. Exactly one field must be set.
type Action struct {
	// Key sends a single key, e.g. "C", "enter", "esc", "down".
	Key *string `yaml:"key"`
	// Keys sends several keys in order.
	Keys []string `yaml:"keys"`
	// Type sends literal text as keystrokes.
	Type *string `yaml:"type"`
	// Run executes a shell command (CLI journeys, fixture nudges).
	Run *string `yaml:"run"`
	// Wait sleeps for a Go duration, e.g. "500ms". Use sparingly; prefer
	// WaitFor, which is deterministic.
	Wait *string `yaml:"wait"`
	// WaitFor blocks until the rendered screen contains this substring.
	WaitFor *string `yaml:"wait_for"`
}

// Expect is a single assertion against the rendered frame. Exactly one of the
// four assertion fields must be set.
type Expect struct {
	Contains    *string `yaml:"screen_contains"`
	NotContains *string `yaml:"screen_not_contains"`
	Matches     *string `yaml:"screen_matches"`
	NotMatches  *string `yaml:"screen_not_matches"`
	// Why records what regression this assertion is standing guard against.
	Why string `yaml:"why"`
}

// ActionKind names the verb a step uses.
type ActionKind string

// The recognised action verbs.
const (
	ActKey     ActionKind = "key"
	ActKeys    ActionKind = "keys"
	ActType    ActionKind = "type"
	ActRun     ActionKind = "run"
	ActWait    ActionKind = "wait"
	ActWaitFor ActionKind = "wait_for"
)

// Kinds reports every verb set on the action. Validation requires exactly one.
func (a Action) Kinds() []ActionKind {
	var out []ActionKind
	if a.Key != nil {
		out = append(out, ActKey)
	}
	if a.Keys != nil {
		out = append(out, ActKeys)
	}
	if a.Type != nil {
		out = append(out, ActType)
	}
	if a.Run != nil {
		out = append(out, ActRun)
	}
	if a.Wait != nil {
		out = append(out, ActWait)
	}
	if a.WaitFor != nil {
		out = append(out, ActWaitFor)
	}
	return out
}

// ExpectKind names the assertion an expectation carries.
type ExpectKind string

// The recognised assertion kinds.
const (
	ExpContains    ExpectKind = "screen_contains"
	ExpNotContains ExpectKind = "screen_not_contains"
	ExpMatches     ExpectKind = "screen_matches"
	ExpNotMatches  ExpectKind = "screen_not_matches"
)

// Kinds reports every assertion set. Validation requires exactly one.
func (e Expect) Kinds() []ExpectKind {
	var out []ExpectKind
	if e.Contains != nil {
		out = append(out, ExpContains)
	}
	if e.NotContains != nil {
		out = append(out, ExpNotContains)
	}
	if e.Matches != nil {
		out = append(out, ExpMatches)
	}
	if e.NotMatches != nil {
		out = append(out, ExpNotMatches)
	}
	return out
}

// AllowedRules returns every Blank Detector suppression as ruleID -> reason,
// regardless of scope. It is for reporting what a script switched off; it is
// NOT what a scanner should consult, because it would silence a scoped
// suppression everywhere. Use AllowedRulesFor.
func (s *Script) AllowedRules() map[string]string {
	out := make(map[string]string, len(s.Allow))
	for _, a := range s.Allow {
		out[a.Pattern] = a.Justification
	}
	return out
}

// AllowedRulesFor returns the suppressions in force on one step's frame.
//
// This is the map a Blank Detector scan must be given. A journey-wide entry
// applies everywhere; a scoped entry applies only to the steps it names, so a
// rule silenced on the session list is still armed on the pager.
func (s *Script) AllowedRulesFor(stepID string) map[string]string {
	out := make(map[string]string, len(s.Allow))
	for _, a := range s.Allow {
		if len(a.Steps) == 0 {
			out[a.Pattern] = a.Justification
			continue
		}
		for _, id := range a.Steps {
			if id == stepID {
				out[a.Pattern] = a.Justification
				break
			}
		}
	}
	return out
}

// ScopeLabel describes where a suppression applies, for a report.
func (a AllowEntry) ScopeLabel() string {
	if len(a.Steps) == 0 {
		return "every frame in the journey"
	}
	return "only " + strings.Join(a.Steps, ", ")
}

// CaptureSteps returns only the steps that produce a frame.
func (s *Script) CaptureSteps() []Step {
	var out []Step
	for _, st := range s.Steps {
		if st.Capture != "" {
			out = append(out, st)
		}
	}
	return out
}

// Parse decodes a G0 script with unknown fields rejected. A typo in a schema
// key must fail loudly rather than silently drop an assertion.
func Parse(r io.Reader) (*Script, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var s Script
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("empty script")
		}
		return nil, err
	}
	// A second document is almost certainly a mistake and would be ignored.
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("script contains more than one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return nil, err
	}
	return &s, nil
}

// Load reads and decodes a G0 script from disk. It does not validate.
func Load(path string) (*Script, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	s, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Problem is one validation failure, addressed by a schema path so the author
// can find it without counting lines.
type Problem struct {
	Path string
	Msg  string
}

func (p Problem) String() string { return p.Path + ": " + p.Msg }

// Problems is an ordered list of validation failures.
type Problems []Problem

// Error renders the problems as a single message.
func (ps Problems) Error() string {
	parts := make([]string, 0, len(ps))
	for _, p := range ps {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, "\n")
}

var (
	slugRe   = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	stepIDRe = regexp.MustCompile(`^[0-9]{2}-[a-z0-9]+(?:-[a-z0-9]+)*$`)
	capRe    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// KnownRuleIDs is the set of Blank Detector rule IDs an allowlist may name. It
// is injected rather than imported so this package stays free of lint's
// dependencies and can be reused by a non-terminal driver.
type KnownRuleIDs map[string]bool

// Validate checks the script against the schema. A nil return means the script
// is a usable acceptance test; anything else must block scaffolding of G1..G5.
//
// known may be nil, in which case allowlist rule IDs are not checked against
// the detector catalogue (only that they carry a justification).
func (s *Script) Validate(known KnownRuleIDs) Problems {
	var ps Problems
	add := func(path, format string, args ...any) {
		ps = append(ps, Problem{Path: path, Msg: fmt.Sprintf(format, args...)})
	}

	if s.Version != SchemaVersion {
		add("version", "must be %d, got %d", SchemaVersion, s.Version)
	}
	if s.Slug == "" {
		add("slug", "required")
	} else if !slugRe.MatchString(s.Slug) {
		add("slug", "must be kebab-case ([a-z0-9] and single dashes), got %q", s.Slug)
	}
	if strings.TrimSpace(s.Sentence) == "" {
		add("sentence", "required: one line of English describing the journey, in the asker's words")
	}
	if s.Term.Width <= 0 {
		add("term.width", "required and must be > 0 (a screen assertion without a width is not reproducible)")
	}
	if s.Term.Height <= 0 {
		add("term.height", "required and must be > 0")
	}
	if len(s.Owns) == 0 {
		add("owns", "required: list the source files this feature owns so VERDICT can detect a stale transcript")
	}
	for i, o := range s.Owns {
		if strings.TrimSpace(o) == "" {
			add(fmt.Sprintf("owns[%d]", i), "empty pattern")
			continue
		}
		if strings.HasPrefix(o, "/") {
			add(fmt.Sprintf("owns[%d]", i), "must be repo-root-relative, got absolute path %q", o)
		}
		// "internal/pkg/..." is the recursive-directory form and is fine; a
		// literal ".." segment is an escape out of the repo and is not.
		for _, seg := range strings.Split(filepath.ToSlash(o), "/") {
			if seg == ".." {
				add(fmt.Sprintf("owns[%d]", i), "must not escape the repo root with %q, got %q", "..", o)
				break
			}
		}
	}

	// The capture steps, resolved first so a scoped suppression can be checked
	// against them: naming a step that does not capture a frame would silence
	// nothing while looking like it silenced something.
	captureIDs := map[string]bool{}
	for _, st := range s.Steps {
		if st.Capture != "" {
			captureIDs[st.ID] = true
		}
	}

	seenAllow := map[string]bool{}
	for i, a := range s.Allow {
		p := fmt.Sprintf("banned_screen_patterns_allow[%d]", i)
		switch {
		case a.Pattern == "":
			add(p+".pattern", "required: name the Blank Detector rule ID being suppressed")
		case seenAllow[a.Pattern]:
			add(p+".pattern", "duplicate suppression of rule %q", a.Pattern)
		case known != nil && !known[a.Pattern]:
			add(p+".pattern", "unknown Blank Detector rule %q", a.Pattern)
		}
		seenAllow[a.Pattern] = true
		if len(strings.TrimSpace(a.Justification)) < 20 {
			add(p+".justification", "required, and must be a real sentence (>= 20 chars): an unexplained suppression is how a blank percentage ships")
		}
		seenStep := map[string]bool{}
		for j, id := range a.Steps {
			sp := fmt.Sprintf("%s.steps[%d]", p, j)
			switch {
			case strings.TrimSpace(id) == "":
				add(sp, "empty step id")
			case seenStep[id]:
				add(sp, "duplicate step %q", id)
			case !captureIDs[id]:
				add(sp, "no capture step with id %q; a suppression scoped to a frame that is never recorded silences nothing", id)
			}
			seenStep[id] = true
		}
	}

	if len(s.Steps) == 0 {
		add("steps", "required: at least one step")
	}
	seenID := map[string]bool{}
	seenCap := map[string]bool{}
	captures := 0
	for i, st := range s.Steps {
		p := fmt.Sprintf("steps[%d]", i)
		switch {
		case st.ID == "":
			add(p+".id", "required")
		case !stepIDRe.MatchString(st.ID):
			add(p+".id", "must look like %q (ordered two-digit prefix, kebab-case label), got %q", "03-inspector", st.ID)
		case seenID[st.ID]:
			add(p+".id", "duplicate step id %q", st.ID)
		}
		seenID[st.ID] = true

		kinds := st.Do.Kinds()
		switch len(kinds) {
		case 1:
			validateAction(p+".do", st.Do, kinds[0], add)
		case 0:
			add(p+".do", "required: exactly one of key|keys|type|run|wait|wait_for")
		default:
			add(p+".do", "exactly one verb allowed, got %v", kinds)
		}

		if st.Capture != "" {
			captures++
			if !capRe.MatchString(st.Capture) {
				add(p+".capture", "must be kebab-case, got %q", st.Capture)
			}
			if seenCap[st.Capture] {
				add(p+".capture", "duplicate capture name %q (frames would overwrite each other)", st.Capture)
			}
			seenCap[st.Capture] = true
			if len(st.Expect) == 0 {
				add(p+".expect", "required: a captured frame with no assertion is a screenshot nobody reads")
			}
		} else if len(st.Expect) > 0 {
			add(p+".expect", "step has assertions but no capture; assertions read the captured frame only")
		}

		for j, e := range st.Expect {
			ep := fmt.Sprintf("%s.expect[%d]", p, j)
			ek := e.Kinds()
			switch len(ek) {
			case 1:
				validateExpect(ep, e, ek[0], add)
			case 0:
				add(ep, "required: exactly one of screen_contains|screen_not_contains|screen_matches|screen_not_matches")
			default:
				add(ep, "exactly one assertion allowed, got %v", ek)
			}
		}
	}
	if captures == 0 && len(s.Steps) > 0 {
		add("steps", "no step captures a frame: G1 would produce no evidence and G2 nothing to assert on")
	}
	if len(ps) == 0 {
		return nil
	}
	return ps
}

func validateAction(path string, a Action, kind ActionKind, add func(string, string, ...any)) {
	switch kind {
	case ActKey:
		if strings.TrimSpace(*a.Key) == "" {
			add(path+".key", "empty key")
		}
	case ActKeys:
		if len(a.Keys) == 0 {
			add(path+".keys", "empty key list")
		}
		for i, k := range a.Keys {
			if strings.TrimSpace(k) == "" {
				add(fmt.Sprintf("%s.keys[%d]", path, i), "empty key")
			}
		}
	case ActType:
		if *a.Type == "" {
			add(path+".type", "empty text")
		}
	case ActRun:
		if strings.TrimSpace(*a.Run) == "" {
			add(path+".run", "empty command")
		}
	case ActWait:
		d, err := time.ParseDuration(*a.Wait)
		if err != nil {
			add(path+".wait", "not a Go duration (%q): %v", *a.Wait, err)
		} else if d <= 0 {
			add(path+".wait", "must be > 0")
		} else if d > 30*time.Second {
			add(path+".wait", "%s is longer than 30s; a gate that sleeps that long is hiding a missing wait_for", d)
		}
	case ActWaitFor:
		if strings.TrimSpace(*a.WaitFor) == "" {
			add(path+".wait_for", "empty substring: would match immediately and prove nothing")
		}
	}
}

func validateExpect(path string, e Expect, kind ExpectKind, add func(string, string, ...any)) {
	switch kind {
	case ExpContains:
		if *e.Contains == "" {
			add(path+".screen_contains", "empty substring matches everything")
		}
	case ExpNotContains:
		if *e.NotContains == "" {
			add(path+".screen_not_contains", "empty substring matches everything")
		}
	case ExpMatches:
		mustCompile(path+".screen_matches", *e.Matches, add)
	case ExpNotMatches:
		mustCompile(path+".screen_not_matches", *e.NotMatches, add)
	}
}

func mustCompile(path, pat string, add func(string, string, ...any)) {
	if pat == "" {
		add(path, "empty pattern matches everything")
		return
	}
	if _, err := regexp.Compile(pat); err != nil {
		add(path, "not a valid Go regexp: %v", err)
	}
}
