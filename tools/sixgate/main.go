// Command sixgate runs SIXGATE, an artifact-first acceptance framework whose
// single rule is: "done" is not a builder's claim, it is an artifact.
//
// It exists because a feature was once declared done on the strength of code
// analysis, a 2,961-transcript corpus replay and an adversarial review — and
// nobody ever pressed the key. The first thing its user saw was a blank
// percentage. Static verification cannot detect "this feels broken on arrival";
// only a recording of the software being used can.
//
// This binary is deliberately not a `go test` harness. Gates run against the
// artifact a user actually gets, on any machine, with no test runner — and in
// this repository `go test` against a real home directory has destroyed live
// data three times, so the gates must not need it.
//
// Verbs implemented here (the contract and skeleton):
//
//	scaffold   create a gate tree; G0 only, until G0 validates
//	validate   check a G0 script against the schema
//	drive      G1: execute the journey and record what a human would see
//	drive-b    G1/G3: same journey against the SHIPPED BINARY in a real pane,
//	           on a dedicated socket it has to prove it gave back
//	assert     G2: read those frames and decide whether the journey held
//	matrix     G3: run the journey in every declared world — every adapter, every
//	           session state, empty and enormous, narrow terminals, and a machine
//	           that has never run the software — each in its own sandbox, capped
//	oracle     G4: compare every number on screen against a number somebody else
//	           produced, and emit the list of figures for which nobody did
//	probe      G4b: launch a disposable probe session with a known context
//	           recipe, ask its agent quote-based identity questions, grade the
//	           inspector against the testimony, and tear the probe down ALWAYS
//	coldeye    G5: hand a reviewer the binary and one sentence, grade what returns
//	verdict    roll the evidence on disk up into VERDICT.md / VERDICT.json
//	selfcheck  prove the framework's own rules still hold
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/artifact"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/lint"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/script"
)

// version identifies the tool in every artifact it writes.
const version = "sixgate/0.1.0"

var _ = "tmux -C"

// Exit codes. 1 means a gate said no; 2 means the tool could not run.
const (
	exitOK    = 0
	exitGate  = 1
	exitUsage = 2
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	verb := os.Args[1]
	args := os.Args[2:]
	var err error
	var code int
	switch verb {
	case "scaffold":
		code, err = cmdScaffold(args)
	case "validate":
		code, err = cmdValidate(args)
	case "drive":
		code, err = cmdDrive(args)
	case "drive-b":
		code, err = cmdDriveB(args)
	case "assert":
		code, err = cmdAssert(args)
	case "matrix":
		code, err = cmdMatrix(args)
	case "oracle":
		code, err = cmdOracle(args)
	case "probe":
		code, err = cmdProbe(args)
	case "coldeye":
		code, err = cmdColdeye(args)
	case "verdict":
		code, err = cmdVerdict(args)
	case "selfcheck":
		code, err = cmdSelfcheck(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "sixgate: unknown verb %q\n\n", verb)
		usage()
		os.Exit(exitUsage)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "sixgate %s: %v\n", verb, err)
		if code == exitOK {
			code = exitUsage
		}
	}
	os.Exit(code)
}

func usage() {
	fmt.Fprint(os.Stderr, `sixgate — artifact-first acceptance gates ("no transcript, not done")

usage:
  sixgate scaffold <slug> [-sentence "..."]   create the gate tree; writes G0 only,
                                              and unlocks G1..G5 once G0 validates
  sixgate validate <slug>                     validate the G0 script
  sixgate drive    <slug> [-fixtures a,b]     G1: run the journey, record every frame
                                              (Driver A: in-process model, no tmux, no pty)
  sixgate drive-b  <slug> [-rows a,b]         G1/G3: run the SHIPPED BINARY in a real pane on a
                                              dedicated socket, and prove the server was given back
  sixgate assert   <slug>                     G2: assert on those frames, never on internals
  sixgate matrix   <slug> [-rows a,b]         G3: run the journey in every world matrix.yaml declares,
                                              each in its own sandbox, each under a wall-time cap
  sixgate oracle compare <slug>               G4: grade every declared figure against the oracle
                                              files on disk; emit must-label.json for G2
  sixgate oracle collect <slug> -case <id> --yes
                                              G4: obtain an oracle from a LIVE session. Refuses
                                              without --yes, refuses inside a matrix row, and
                                              runs only the argv the declaration carries
  sixgate probe <slug> --yes                  G4b: launch a DISPOSABLE probe session with a known
                                              context recipe, ask quote-based identity questions,
                                              grade the inspector against the testimony, and tear
                                              the probe down ALWAYS (identity only, never counts)
  sixgate coldeye brief   <slug>              G5: build the reviewer's world — the binary and one
                                              sentence, and an assertion that it holds nothing else
  sixgate coldeye outcome <slug>              G5: grade the returned report against its resolutions
  sixgate verdict  <slug> [--check]           write, or re-verify, VERDICT.md/.json
  sixgate selfcheck                           prove the framework's own rules hold

common flags:
  -repo <dir>        repository root (default: nearest ancestor with go.mod)
  -gates-dir <path>  repo-relative gate root (default: docs/gates)

the gates:
  G0 SCRIPT     the journey as literal keystrokes, written BEFORE building
  G1 DRIVE      execute G0 against the real artifact, capture what a human sees
  G2 ASSERT     assert on the rendered output, never on internals
  G3 MATRIX     every tool, every state, empty and enormous, and a cold first run
  G4 ORACLE     every number checked against a truth the author did not write
  G4b TESTIMONY the probe's own agent, asked for quotes — identity, never counts
                (a supplement to G4, not a seventh gate; the catalogue stays six)
  G5 COLD-EYE   a reviewer given only the binary and one sentence
`)
}

// commonFlags wires the flags every verb shares.
func commonFlags(fs *flag.FlagSet) (repo, gates *string) {
	repo = fs.String("repo", "", "repository root (default: nearest ancestor with go.mod)")
	gates = fs.String("gates-dir", artifact.DefaultGatesRoot, "repo-relative gate root")
	return repo, gates
}

// parseFlags parses a flag set that also takes positional arguments, in any
// order. Go's flag package stops at the first non-flag word, which would
// silently ignore `-repo` in `sixgate scaffold widget -repo /tmp/x` — and a
// tool that quietly writes into the wrong repository is not a safety property
// anybody should have to remember.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func resolveTree(repoFlag, gatesFlag, slug string) (artifact.Tree, error) {
	repo := repoFlag
	if repo == "" {
		found, err := findRepoRoot()
		if err != nil {
			return artifact.Tree{}, err
		}
		repo = found
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return artifact.Tree{}, err
	}
	if slug == "" {
		return artifact.Tree{}, errors.New("a slug is required")
	}
	return artifact.NewTree(abs, gatesFlag, slug), nil
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.mod found in any parent directory; pass -repo")
		}
		dir = parent
	}
}

// loadScript reads, validates and placeholder-checks a G0 script.
func loadScript(t artifact.Tree) (*script.Script, script.Problems, error) {
	path := t.ScriptPath()
	raw, err := os.ReadFile(path) //nolint:gosec // path derived from the gate tree
	if err != nil {
		return nil, nil, err
	}
	s, err := script.Load(path)
	if err != nil {
		return nil, script.Problems{{Path: artifact.ScriptFile, Msg: err.Error()}}, nil
	}
	ps := s.Validate(lint.KnownRuleIDs())
	ps = append(ps, script.CheckPlaceholders(raw)...)
	if s.Slug != "" && s.Slug != t.Slug {
		ps = append(ps, script.Problem{
			Path: "slug",
			Msg:  fmt.Sprintf("is %q but the script lives in docs/gates/%s; the directory and the slug must agree", s.Slug, t.Slug),
		})
	}
	if len(ps) == 0 {
		return s, nil, nil
	}
	return s, ps, nil
}

func printProblems(header string, ps script.Problems) {
	fmt.Printf("%s (%d problem(s)):\n", header, len(ps))
	for _, p := range ps {
		fmt.Printf("  %s\n", p)
	}
}

// ---------------------------------------------------------------------------
// scaffold
// ---------------------------------------------------------------------------

// cmdScaffold enforces the ordering rule. First run writes G0 and nothing else.
// A later run unlocks G1..G5 — but only once G0 actually validates. You cannot
// start collecting evidence for a journey nobody wrote down.
func cmdScaffold(args []string) (int, error) {
	fs := flag.NewFlagSet("scaffold", flag.ContinueOnError)
	repo, gates := commonFlags(fs)
	sentence := fs.String("sentence", "", "the journey in one line of English, in the asker's own words")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return exitUsage, err
	}
	t, err := resolveTree(*repo, *gates, first(pos))
	if err != nil {
		return exitUsage, err
	}

	if _, err := os.Stat(t.ScriptPath()); errors.Is(err, os.ErrNotExist) {
		if err := t.CreateRoot(); err != nil {
			return exitUsage, err
		}
		if err := os.WriteFile(t.ScriptPath(), []byte(renderTemplate(t.Slug, *sentence)), 0o644); err != nil { //nolint:gosec // committed artifact
			return exitUsage, err
		}
		fmt.Printf("wrote %s\n", t.Rel(t.ScriptPath()))
		fmt.Printf("G1..G5 are LOCKED until it validates.\n")
		fmt.Printf("Write the journey as literal keystrokes, then: sixgate scaffold %s\n", t.Slug)
		return exitOK, nil
	} else if err != nil {
		return exitUsage, err
	}

	_, ps, err := loadScript(t)
	if err != nil {
		return exitUsage, err
	}
	if ps != nil {
		printProblems("G0 does not validate, so G1..G5 stay LOCKED", ps)
		return exitGate, nil
	}
	made, err := t.Unlock()
	if err != nil {
		return exitUsage, err
	}
	fmt.Printf("G0 validates. %s\n", unlockSummary(made))
	fmt.Printf("Next: run G1 against the real built artifact and capture what a human would see.\n")
	return exitOK, nil
}

func unlockSummary(made []string) string {
	if len(made) == 0 {
		return "G1..G5 were already unlocked."
	}
	return "Unlocked:\n  " + strings.Join(made, "\n  ")
}

// ---------------------------------------------------------------------------
// validate
// ---------------------------------------------------------------------------

func cmdValidate(args []string) (int, error) {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	repo, gates := commonFlags(fs)
	pos, err := parseFlags(fs, args)
	if err != nil {
		return exitUsage, err
	}
	t, err := resolveTree(*repo, *gates, first(pos))
	if err != nil {
		return exitUsage, err
	}
	s, ps, err := loadScript(t)
	if err != nil {
		return exitUsage, err
	}
	if ps != nil {
		printProblems(t.Rel(t.ScriptPath())+" is not a usable acceptance test", ps)
		return exitGate, nil
	}
	caps := s.CaptureSteps()
	assertions := 0
	for _, st := range caps {
		assertions += len(st.Expect)
	}
	fmt.Printf("%s OK\n", t.Rel(t.ScriptPath()))
	fmt.Printf("  journey:    %s\n", s.Sentence)
	fmt.Printf("  steps:      %d (%d capture frames, %d on-screen assertions)\n", len(s.Steps), len(caps), assertions)
	fmt.Printf("  terminal:   %dx%d\n", s.Term.Width, s.Term.Height)
	fmt.Printf("  owns:       %s\n", strings.Join(s.Owns, ", "))
	if len(s.Allow) == 0 {
		fmt.Printf("  blank-lint: all %d rules active on every frame\n", len(lint.RuleIDs()))
	} else {
		fmt.Printf("  blank-lint: %d of %d rules suppressed, each with a justification:\n", len(s.Allow), len(lint.RuleIDs()))
		for _, a := range s.Allow {
			// The scope is printed, not just the count: a suppression that
			// names its frames leaves the rule armed everywhere else, and a
			// reader who cannot see the difference cannot review it.
			fmt.Printf("              %-16s on %s\n", a.Pattern, a.ScopeLabel())
		}
	}
	return exitOK, nil
}

// ---------------------------------------------------------------------------
// verdict
// ---------------------------------------------------------------------------

func cmdVerdict(args []string) (int, error) {
	fs := flag.NewFlagSet("verdict", flag.ContinueOnError)
	repo, gates := commonFlags(fs)
	check := fs.Bool("check", false, "re-verify a written verdict instead of writing one")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return exitUsage, err
	}
	t, err := resolveTree(*repo, *gates, first(pos))
	if err != nil {
		return exitUsage, err
	}
	s, ps, err := loadScript(t)
	if err != nil {
		return exitUsage, err
	}
	if *check {
		return verdictCheck(t, s, ps)
	}
	return verdictWrite(t, s, ps)
}

func verdictWrite(t artifact.Tree, s *script.Script, ps script.Problems) (int, error) {
	in := artifact.BuildInput{
		Tree:     t,
		Slug:     t.Slug,
		Tool:     version,
		Now:      time.Now(),
		G0Status: artifact.StatusPass,
	}
	if s != nil {
		in.Sentence = s.Sentence
		in.Owns = s.Owns
	}
	if ps != nil {
		in.G0Status = artifact.StatusFail
		in.G0Detail = fmt.Sprintf("%d schema problem(s); run: sixgate validate %s", len(ps), t.Slug)
	}
	if len(in.Owns) == 0 {
		// Without an owns list there is nothing to bind the evidence to, so the
		// verdict would vouch for software it cannot identify. Say so rather
		// than writing a document that looks authoritative and is not.
		in.Owns = nil
	}
	v, err := artifact.Build(in)
	if err != nil {
		return exitUsage, err
	}
	if err := artifact.Write(t, v); err != nil {
		return exitUsage, err
	}
	fmt.Printf("wrote %s and %s\n", t.Rel(t.VerdictMarkdownPath()), t.Rel(t.VerdictJSONPath()))
	for _, g := range v.Gates {
		fmt.Printf("  %-3s %-17s %-11s %s\n", g.ID, g.Name, g.Status, g.Detail)
	}
	fmt.Printf("overall: %s\n", v.Overall)
	fmt.Printf("this verb reports; it does not gate. The gate is: sixgate verdict %s --check\n", t.Slug)
	return exitOK, nil
}

func verdictCheck(t artifact.Tree, s *script.Script, ps script.Problems) (int, error) {
	var owns []string
	if s != nil {
		owns = s.Owns
	}
	if ps != nil {
		printProblems("G0 does not validate, so no verdict can stand", ps)
		return exitGate, nil
	}
	res, err := artifact.Check(t, owns, mustLabelCoupling)
	if err != nil {
		return exitUsage, err
	}
	if res.OK() {
		fmt.Printf("VERDICT holds for %s at %s\n", t.Slug, res.Verdict.GitSHA)
		fmt.Printf("  all six gates PASS; every owned source still hashes to what the evidence covered;\n")
		fmt.Printf("  and every figure G4 found no oracle for is labelled an estimate on a frame G2 read\n")
		return exitOK, nil
	}
	fmt.Printf("VERDICT does not hold for %s (%d problem(s)):\n", t.Slug, len(res.Problems))
	for _, p := range res.Problems {
		fmt.Printf("  %s\n", p)
	}
	return exitGate, nil
}

// first returns the first positional argument, or "".
func first(pos []string) string {
	if len(pos) == 0 {
		return ""
	}
	return pos[0]
}
