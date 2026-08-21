package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sixgate/artifact"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/assert"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/driver/teadrive"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/fixture"
	"github.com/asheshgoplani/agent-deck/internal/sixgate/oracle"
)

// ---------------------------------------------------------------------------
// drive — G1
// ---------------------------------------------------------------------------

// cmdDrive executes the G0 journey against the real model and records the
// frames. It refuses to run against a script that does not validate: driving a
// journey nobody wrote down produces a directory of screenshots, not evidence.
func cmdDrive(args []string) (int, error) {
	fs := flag.NewFlagSet("drive", flag.ContinueOnError)
	repo, gates := commonFlags(fs)
	fixtures := fs.String("fixtures", "", "comma-separated recorded cases to drive (default: every claude case)")
	keepRoot := fs.String("fixture-root", "", "materialize fixtures here instead of a temporary directory (for debugging)")
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
		printProblems("G0 does not validate, so there is no journey to drive", ps)
		return exitGate, nil
	}

	names, err := selectFixtures(*fixtures)
	if err != nil {
		return exitUsage, err
	}

	g1, ok := artifact.GateByID(artifact.G1)
	if !ok {
		return exitUsage, fmt.Errorf("gate G1 is missing from the catalogue")
	}
	g1Dir := t.GateDir(g1)
	if err := os.MkdirAll(g1Dir, 0o755); err != nil {
		return exitUsage, err
	}

	allPass := true
	for _, name := range names {
		root := *keepRoot
		if root == "" {
			root, err = os.MkdirTemp("", "sixgate-fixture-")
			if err != nil {
				return exitUsage, err
			}
			defer func(dir string) { _ = os.RemoveAll(dir) }(root)
		} else {
			root = filepath.Join(root, name)
			if err := os.MkdirAll(root, 0o755); err != nil {
				return exitUsage, err
			}
		}

		setup, err := fixture.FromCorpus(name, root)
		if err != nil {
			return exitUsage, err
		}
		out := filepath.Join(g1Dir, name)
		run, err := teadrive.Drive(teadrive.Options{
			Script:  s,
			Fixture: setup,
			OutDir:  out,
			Repo:    t.Repo,
			Tool:    version,
		})
		if err != nil {
			return exitUsage, err
		}
		fmt.Printf("%-24s %s  %d step(s), %d frame(s), tmux servers %d, PTY delta %d, %d ms\n",
			name, statusWord(run.Pass), len(run.Steps), countFrames(run), run.TmuxServersSpawned, run.PTY.Delta, run.DurationMS)
		for _, f := range run.Failures {
			fmt.Printf("    %s\n", f)
		}
		fmt.Printf("    %s\n", t.Rel(filepath.Join(out, "transcript.md")))
		if !run.Pass {
			allPass = false
		}
	}
	if !allPass {
		return exitGate, nil
	}
	fmt.Printf("\nG1 recorded. Nothing here says the journey was CORRECT — that is G2:\n  sixgate assert %s\n", t.Slug)
	return exitOK, nil
}

func countFrames(run *teadrive.Run) int {
	n := 0
	for _, s := range run.Steps {
		if s.ScreenFile != "" {
			n++
		}
	}
	return n
}

// selectFixtures resolves the -fixtures flag against the recorded corpus.
func selectFixtures(flagValue string) ([]string, error) {
	all, err := fixture.CorpusNames()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(flagValue) == "" {
		// The default is the Claude cases: the G0 journey presses C on a Claude
		// session, and a Codex fixture would exercise a different adapter with
		// different categories. The matrix (G3) is where every tool runs.
		var out []string
		for _, n := range all {
			if strings.HasPrefix(n, "claude-") {
				out = append(out, n)
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("the recorded corpus has no claude case to drive")
		}
		return out, nil
	}
	known := map[string]bool{}
	for _, n := range all {
		known[n] = true
	}
	var out []string
	for _, raw := range strings.Split(flagValue, ",") {
		n := strings.TrimSpace(raw)
		if n == "" {
			continue
		}
		if !known[n] {
			return nil, fmt.Errorf("no recorded case named %q; the corpus has: %s", n, strings.Join(all, ", "))
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-fixtures named nothing")
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// assert — G2
// ---------------------------------------------------------------------------

// cmdAssert reads the frames G1 recorded and decides whether the journey did
// what the script said. It never touches the product, and neither does the
// package it delegates to.
func cmdAssert(args []string) (int, error) {
	fs := flag.NewFlagSet("assert", flag.ContinueOnError)
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
		printProblems("G0 does not validate, so its assertions cannot be trusted", ps)
		return exitGate, nil
	}

	g1, _ := artifact.GateByID(artifact.G1)
	g2, _ := artifact.GateByID(artifact.G2)
	g4, _ := artifact.GateByID(artifact.G4)
	// G4's contract, addressed as a file. G2 does not import the oracle
	// package: the two gates share a document, not an implementation, because
	// two gates sharing an implementation cannot check each other.
	mustLabel := filepath.Join(t.GateDir(g4), oracle.MustLabelJSONFile)
	res, err := assert.Evaluate(s, t.GateDir(g1), mustLabel, version, time.Now())
	if err != nil {
		return exitUsage, err
	}
	if err := assert.Write(t.GateDir(g2), res); err != nil {
		return exitUsage, err
	}

	fmt.Printf("wrote %s\n", t.Rel(filepath.Join(t.GateDir(g2), "results.md")))
	switch {
	case res.MustLabel.Error != "":
		fmt.Printf("G4 contract:  %s could not be read — %s\n", res.MustLabel.Source, res.MustLabel.Error)
	case !res.MustLabel.Present:
		fmt.Printf("G4 contract:  none on disk; no on-screen estimate labels were checked\n")
	default:
		fmt.Printf("G4 contract:  %d unoracled figure(s), %d on-screen label assertion(s)\n",
			res.MustLabel.Entries, res.MustLabel.Assertions)
	}
	if len(res.Fixtures) == 0 {
		fmt.Printf("no G1 evidence under %s — run: sixgate drive %s\n", t.Rel(t.GateDir(g1)), t.Slug)
		return exitGate, nil
	}
	for _, f := range res.Fixtures {
		failed := 0
		for _, a := range f.Assertions {
			if !a.Pass {
				failed++
			}
		}
		fmt.Printf("%-24s %s  %d frame(s), %d assertion(s) (%d failed), %d blank finding(s)%s\n",
			f.Fixture, statusWord(f.Pass), len(f.Frames), len(f.Assertions), failed, len(f.BlankFindings),
			divergenceSuffix(f.FirstDivergentStep))
	}
	if !res.Pass {
		fmt.Printf("\nfirst divergent step: %s\n", orDash(res.FirstDivergentStep))
		return exitGate, nil
	}
	return exitOK, nil
}

func divergenceSuffix(step string) string {
	if step == "" {
		return ""
	}
	return "  first divergence at " + step
}

func statusWord(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
